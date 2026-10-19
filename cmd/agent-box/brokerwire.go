package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/culiops/agent-box/internal/auth"
	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/ghapp"
	"github.com/culiops/agent-box/internal/session"
	"github.com/culiops/agent-box/internal/ssologin"
	"github.com/culiops/agent-box/internal/web"
)

// The broker is the only implementation of session.BrokerHooks.
var _ session.BrokerHooks = broker.Hooks{}

// errBrokerListen marks the one startup failure that is not fatal: the broker
// could not open its listener. The console still runs; profiles with an AWS
// binding are then refused at spawn, with the reason in the log.
var errBrokerListen = errors.New("credential broker disabled")

// brokerService is the running credential broker and what the rest of serve
// needs from it.
type brokerService struct {
	Hooks  broker.Hooks         // installed on the session manager
	Status web.AWSStatusAdapter // behind GET /api/sessions/{id}/aws; serve adds the egress counters
	SSO    *ssologin.Runner     // the in-UI AWS sign-in; nil when the aws CLI is not on the daemon's PATH
	Stop   func(context.Context)
}

// lookAWS finds the aws CLI once at boot; a variable so tests can point it at a
// fake.
var lookAWS = func() (string, error) { return exec.LookPath("aws") }

// definedTokens returns a filter that keeps the entries whose name is in
// entries, the list read at daemon start. A session carries the entries it was
// spawned with, so an entry deleted from static_tokens since (the daemon was
// restarted, the session was reattached) is dropped from its view and its
// fetches are denied_scope; the entries' values are never re-read from here.
func definedTokens(entries []config.StaticToken) func([]config.StaticToken) []config.StaticToken {
	known := make(map[string]bool, len(entries))
	for _, e := range entries {
		known[e.Name] = true
	}
	return func(in []config.StaticToken) []config.StaticToken {
		var out []config.StaticToken
		for _, e := range in {
			if known[e.Name] {
				out = append(out, e)
			}
		}
		return out
	}
}

// startBroker brings the credential broker up: the persisted HMAC secret, the
// audit store, the listener and the HTTP server. dataDir holds broker.secret and
// audit.db; stateRoot (symlink-resolved on darwin) holds the socket and the
// per-binding box files. A failure of the secret or the audit store is fatal for
// the caller (a broker without them could mint nothing or forge tokens); a
// failure to listen wraps errBrokerListen.
func startBroker(dataDir, stateRoot, goos string, port int, get func(id string) (*session.Session, bool),
	info func(id string) (session.Info, bool), apps []config.GitHubApp, entries []config.StaticToken) (*brokerService, error) {
	sec, err := auth.NewCookieAuth(filepath.Join(dataDir, "broker.secret"))
	if err != nil {
		return nil, fmt.Errorf("broker secret: %w", err)
	}
	// Fail closed: tokens are HMACs under this key, and an empty or short key
	// would make them forgeable.
	if n := len(sec.Secret()); n != 32 {
		return nil, fmt.Errorf("broker secret: want 32 bytes, have %d", n)
	}
	audit, err := broker.OpenAudit(filepath.Join(dataDir, "audit.db"))
	if err != nil {
		return nil, fmt.Errorf("audit store: %w", err)
	}
	// Write windows live in memory only, so a restart ended any that were open.
	// Give them their terminal audit row; never block or fail startup on it.
	if n, err := audit.EndOpenWindows(time.Now()); err != nil {
		log.Printf("broker: closing write windows left open by the last run: %v", err)
	} else if n > 0 {
		log.Printf("broker: wrote %d terminal audit row(s) for write windows open at the last shutdown", n)
	}
	defined := definedTokens(entries)
	brk := broker.New(sec.Secret(), func(id string) (broker.SessionView, bool) {
		s, ok := get(id)
		if !ok {
			return broker.SessionView{}, false
		}
		return broker.SessionView{Agent: s.Agent, Nonce: s.BrokerNonce, Boxed: s.Runtime == config.RuntimeBox, GitHub: s.GitHub,
			Live: !s.Exited(), AWS: s.AWS, Exclusive: s.Exclusive, Tokens: defined(s.Tokens), PATs: defined(s.PATs)}, true
	}, broker.STSMinter{}, audit)
	// github_apps is read once, at daemon start: a change needs a restart. With
	// none configured the route keeps answering 503.
	if len(apps) > 0 {
		brk.SetGitHub(ghapp.NewMinter(apps, ghapp.Client{}))
	}

	ep, err := broker.Listen(goos, stateRoot, port)
	if err != nil {
		_ = audit.Close()
		return nil, fmt.Errorf("%w: %v", errBrokerListen, err)
	}
	srv := &http.Server{Handler: brk.Handler(), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second}
	go func() {
		if err := srv.Serve(ep.Listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("credential broker stopped: %v", err)
		}
	}()
	// The aws CLI is resolved once. Without it the SSO card offers the host
	// command only (the endpoint answers 502 with it).
	var sso *ssologin.Runner
	if bin, err := lookAWS(); err != nil {
		log.Printf("aws CLI not found on PATH; the SSO card will offer the host command only")
	} else {
		sso = ssologin.New(bin, os.Environ())
	}
	return &brokerService{
		Hooks: broker.Hooks{B: brk, StateRoot: stateRoot, EP: ep},
		Status: web.AWSStatusAdapter{Get: info, Stats: brk.Stats,
			Recent: func(id string, n int) ([]broker.Row, error) {
				return audit.ForSessionExcept(id, broker.ProviderStatic, n)
			},
			RecentStatic: func(id string, n int) ([]broker.Row, error) {
				return audit.ForSessionProvider(id, broker.ProviderStatic, n)
			}},
		SSO: sso,
		Stop: func(ctx context.Context) {
			// First: a killed sign-in writes its sso_login_failed row, and the
			// audit store must still be open for it.
			if sso != nil {
				sso.Close()
			}
			// Before the server closes: after this the route answers 503 and
			// mints nothing, so no token can be issued past the revoke. The
			// audit store is still open for its rows.
			brk.RevokeAllGitHub(ctx)
			if err := srv.Shutdown(ctx); err != nil {
				_ = srv.Close() // a mint outlived the grace period; the audit store closes next
			}
			_ = audit.Close()
		},
	}, nil
}
