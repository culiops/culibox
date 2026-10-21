package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/culiops/agent-box/internal/auth"
	"github.com/culiops/agent-box/internal/boxvm"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/egress"
	"github.com/culiops/agent-box/internal/session"
)

// The egress service is the only implementation of session.EgressHooks.
var _ session.EgressHooks = (*egress.Service)(nil)

// limaProxyHost is what a darwin box calls the Mac: lima's host.lima.internal,
// pinned in the box's /etc/hosts by --add-host (box.Egress.AddHosts) to
// boxvm.LimaHostGateway. The gateway reaches the Mac's 127.0.0.1, where the
// proxy listens.
const limaProxyHost = "host.lima.internal"

// bootReadyTimeout bounds bringing the listener up at boot for a network that
// already exists.
const bootReadyTimeout = 30 * time.Second

// egressService is the running egress proxy and what the rest of serve needs
// from it.
type egressService struct {
	Svc      *egress.Service                      // installed on the session manager and the box layer
	Stats    func(id string) (egress.Stats, bool) // behind the egress object of GET /api/sessions/{id}/aws
	Stop     func(context.Context)
	AddHosts []string
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
			Live: !s.Exited(), AWS: s.AWS, AWSConns: s.AWSConns, GitHub: s.GitHub, Tokens: s.Tokens, PATs: s.PATs}, true
	}
}

// egressServiceOptions is the service configuration. Linux: an internal-only
// network on which boxes cannot reach each other, loopback kept off the proxy,
// and the one env var the measured gate G2 needs for plain Node fetch to honour
// HTTPS_PROXY. The other measured env (hooks.go measuredEnv) is added by the
// service itself.
//
// darwin (option (a)): the network cannot be --internal inside the lima VM (the
// route to the host gateway is the point), so it is an ordinary bridge on the
// fixed boxvm.EgressSubnet, and the VM's provision rules (boxvm) drop
// everything from that subnet except the broker and this proxy's port on the
// gateway. The proxy listens on the Mac's 127.0.0.1 and boxes dial it as
// host.lima.internal; NO_PROXY keeps that name, and the box's own loopback,
// direct (the broker is reached on the gateway, not through the proxy).
func egressServiceOptions(cfg config.Config, goos string, proxy *egress.Proxy, d egress.Docker) egress.ServiceOptions {
	o := egress.ServiceOptions{
		Proxy:    proxy,
		Docker:   d,
		GOOS:     goos,
		Instance: cfg.BoxInstance(),
		Port:     cfg.EgressPortOrDefault(),
		Want:     egress.Want{Internal: true, NoICC: true},
		NoProxy:  "localhost,127.0.0.1,::1",
		ExtraEnv: []string{"NODE_USE_ENV_PROXY=1"},
		// Only for the overlap WARN: the proxy reads each session's own
		// spawn-time entries through the lookup.
		StaticTokens: cfg.StaticTokens,
	}
	if goos == "darwin" {
		o.Want = egress.Want{Internal: false, Subnet: boxvm.EgressSubnet}
		o.ProxyHost = limaProxyHost
		o.NoProxy = "localhost,127.0.0.1,::1," + limaProxyHost
	}
	return o
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
	// darwin: with the VM stopped there is no docker to ask and no bound session
	// running; the first bound spawn's Ready runs after prepare started the VM.
	if !layer.vmStopped(ctx) && svc.Exists(ctx) {
		if _, err := svc.Ready(ctx); err != nil {
			log.Printf("egress: network %s exists but the proxy could not start (%v); bound sessions are refused until it can", svc.Network(), err)
		}
	}
	var addHosts []string
	if goos == "darwin" {
		addHosts = []string{limaProxyHost + ":" + boxvm.LimaHostGateway}
	}
	return &egressService{
		AddHosts: addHosts,
		Svc:      svc,
		Stats:    proxy.Stats,
		Stop: func(ctx context.Context) {
			// Close blocks until every tunnel is audited (or ctx ends); only then
			// is the audit handle closed, so no tunnel's row is lost to a closed store.
			svc.Close(ctx)
			_ = audit.Close()
		},
	}, nil
}
