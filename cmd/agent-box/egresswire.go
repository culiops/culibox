package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/culiops/agent-box/internal/auth"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/egress"
	"github.com/culiops/agent-box/internal/session"
)

// The egress service is the only implementation of session.EgressHooks.
var _ session.EgressHooks = (*egress.Service)(nil)

// errEgressUnsupported is why startEgress refuses a platform: bound boxes there
// would have no allowlist in front of them. The caller leaves the hooks unset,
// so every bound spawn is refused (fail closed) and unbound boxes are untouched.
var errEgressUnsupported = errors.New("egress control for macOS boxes is not available yet")

// bootReadyTimeout bounds bringing the listener up at boot for a network that
// already exists.
const bootReadyTimeout = 30 * time.Second

// egressService is the running egress proxy and what the rest of serve needs
// from it.
type egressService struct {
	Svc   *egress.Service                      // installed on the session manager and the box layer
	Stats func(id string) (egress.Stats, bool) // behind the egress object of GET /api/sessions/{id}/aws
	Stop  func(context.Context)
}

// egressLookup is the proxy's view of a session. It reads the registry through
// get (Manager.Get, which takes the manager lock for the map read only) and the
// session's own lock for Exited; the proxy calls it with none of its own locks
// held, so neither lock order is ever inverted. A session that has left the
// registry is unknown, which the proxy refuses.
func egressLookup(get func(id string) (*session.Session, bool)) egress.Lookup {
	return func(id string) (egress.SessionView, bool) {
		s, ok := get(id)
		if !ok {
			return egress.SessionView{}, false
		}
		return egress.SessionView{Nonce: s.EgressNonce, Box: s.Box, Boxed: s.Runtime == config.RuntimeBox,
			Live: !s.Exited(), AWS: s.AWS}, true
	}
}

// egressServiceOptions is the Linux service configuration: an internal-only
// network on which boxes cannot reach each other, loopback kept off the proxy, and the one env var the measured gate
// G2 needs for plain Node fetch to honour HTTPS_PROXY. The other measured env
// (hooks.go measuredEnv) is added by the service itself.
func egressServiceOptions(cfg config.Config, goos string, proxy *egress.Proxy, d egress.Docker) egress.ServiceOptions {
	return egress.ServiceOptions{
		Proxy:    proxy,
		Docker:   d,
		GOOS:     goos,
		Instance: cfg.BoxInstance(),
		Port:     cfg.EgressPortOrDefault(),
		Want:     egress.Want{Internal: true, NoICC: true},
		NoProxy:  "localhost,127.0.0.1,::1",
		ExtraEnv: []string{"NODE_USE_ENV_PROXY=1"},
	}
}

// bridgeBoxes picks, from `docker ps --format '{{.Names}}\t{{.Networks}}'`
// output, every box whose network set is not exactly {network}.
func bridgeBoxes(psOut, network string) []string {
	var out []string
	for _, line := range strings.Split(psOut, "\n") {
		name, nets, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || name == "" {
			continue
		}
		if nets != network {
			out = append(out, name)
		}
	}
	return out
}

// warnBridgeBoxes logs the boxes of this instance that are still on the old
// bridge (or on it as well as the egress network). They keep running; new
// spawns into them are refused while they run sessions, and they are recreated
// when idle. A docker error is logged, never fatal.
func warnBridgeBoxes(ctx context.Context, d egress.Docker, instance, network string) {
	out, err := d.Run(ctx, []string{"ps", "--filter", "label=" + egress.InstanceLabel + "=" + instance,
		"--format", "{{.Names}}\t{{.Networks}}"})
	if err != nil {
		log.Printf("egress: could not list boxes to check their network: %v", err)
		return
	}
	if names := bridgeBoxes(out, network); len(names) > 0 {
		log.Printf("egress: boxes still outside %s (new spawns in them are refused while they run sessions; recreated when idle): %s",
			network, strings.Join(names, ", "))
	}
}

// startEgress builds the egress proxy: the persisted HMAC secret (its own file,
// not the broker's), the audit store, and the lazy network + listener service.
// Nothing listens yet unless the egress network already exists (a restored
// daemon: live bound sessions must keep working); otherwise the first bound
// spawn creates it. A failure here is not fatal for the daemon: without egress
// control bound profiles refuse to spawn, which is the fail-closed state.
func startEgress(dataDir string, layer *boxLayer, cfg config.Config, goos string,
	get func(id string) (*session.Session, bool)) (*egressService, error) {
	// TODO(Task 18): darwin option (a) replaces this refusal.
	if goos == "darwin" {
		return nil, errEgressUnsupported
	}
	sec, err := auth.NewCookieAuth(filepath.Join(dataDir, "egress.secret"))
	if err != nil {
		return nil, fmt.Errorf("egress secret: %w", err)
	}
	// Fail closed: tokens are HMACs under this key, and an empty or short key
	// would make them forgeable.
	if n := len(sec.Secret()); n != 32 {
		return nil, fmt.Errorf("egress secret: want 32 bytes, have %d", n)
	}
	audit, err := egress.OpenAudit(filepath.Join(dataDir, "audit.db"))
	if err != nil {
		return nil, fmt.Errorf("egress audit store: %w", err)
	}
	proxy := egress.New(egress.Options{Secret: sec.Secret(), Lookup: egressLookup(get), Audit: audit})
	svc := egress.NewService(egressServiceOptions(cfg, goos, proxy, layer.cli))
	// Learn the host's own addresses once now; Ready refreshes them on every
	// call and refuses when they cannot be read, so a failure here only logs.
	if err := svc.RefreshHostAddrs(); err != nil {
		log.Printf("egress: could not read the host's addresses (%v); boxes are refused until they can be", err)
	}

	// Spec correction 10: an existing network means bound sessions may be
	// running, so the listener comes up now. Otherwise the first bound spawn
	// creates the network.
	ctx, cancel := context.WithTimeout(context.Background(), bootReadyTimeout)
	defer cancel()
	if svc.Exists(ctx) {
		if _, err := svc.Ready(ctx); err != nil {
			log.Printf("egress: network %s exists but the proxy could not start (%v); bound sessions are refused until it can", svc.Network(), err)
		}
	}
	return &egressService{
		Svc:   svc,
		Stats: proxy.Stats,
		Stop: func(ctx context.Context) {
			// Close blocks until every tunnel is audited (or ctx ends); only then
			// is the audit handle closed, so no tunnel's row is lost to a closed store.
			svc.Close(ctx)
			_ = audit.Close()
		},
	}, nil
}
