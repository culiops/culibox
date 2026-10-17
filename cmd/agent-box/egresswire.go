package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
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
		return egress.SessionView{Nonce: s.AWSNonce, Box: s.Box, Boxed: s.Runtime == config.RuntimeBox,
			Live: !s.Exited(), AWS: s.AWS}, true
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
	svc := egress.NewService(egress.ServiceOptions{
		Proxy:    proxy,
		Docker:   layer.cli,
		GOOS:     goos,
		Instance: cfg.BoxInstance(),
		Port:     cfg.EgressPortOrDefault(),
		Want:     egress.Want{Internal: true},
	})

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
