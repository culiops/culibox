package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/culiops/culibox/internal/auth"
	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credref"
	"github.com/culiops/culibox/internal/ghapp"
	"github.com/culiops/culibox/internal/secretstore"
	"github.com/culiops/culibox/internal/session"
	"github.com/culiops/culibox/internal/ssologin"
	"github.com/culiops/culibox/internal/web"
)

// The broker is the only implementation of session.BrokerHooks.
var _ session.BrokerHooks = broker.Hooks{}

// The sso_direct read-only gate at spawn is the broker's too.
var _ session.SSODirectChecker = broker.Hooks{}

// errBrokerListen marks the one startup failure that is not fatal: the broker
// could not open its listener. The console still runs; profiles with an AWS
// binding are then refused at spawn, with the reason in the log.
var errBrokerListen = errors.New("credential broker disabled")

// brokerService is the running credential broker and what the rest of serve
// needs from it.
type brokerService struct {
	Hooks  broker.Hooks         // installed on the session manager
	Status web.AWSStatusAdapter // behind GET /api/sessions/{id}/aws; serve adds the egress counters
	// Activity feeds the credential and approval rows of GET /api/sessions/{id}/activity.
	Activity web.ActivityAuditAdapter
	SSO      *ssologin.Runner // the in-UI AWS sign-in; nil when the aws CLI is not on the daemon's PATH
	// Minter and Sim are what the broker mints and probes with; the credentials
	// screen's AWS service reuses them.
	Minter broker.Minter
	Sim    awsprobe.Simulator
	Stop   func(context.Context)

	// Tokens and GitHub are the live holders credLive.Apply keeps current:
	// the defined-entries filter behind the session view, and the App minter.
	Tokens *tokenSet
	GitHub *ghapp.Minter
}

// probeStopWait bounds how long Stop waits for base-role re-probes in flight:
// just over one probe's own timeout.
const probeStopWait = 6 * time.Second

// lookAWS finds the aws CLI once at boot; a variable so tests can point it at a
// fake.
var lookAWS = func() (string, error) { return exec.LookPath("aws") }

// definedTokens returns a filter that keeps the entries whose name is in ts,
// read at call time: a credential save swaps the set, so an entry deleted
// since a session was spawned is dropped from its view and its fetches are
// denied_scope, and one added since is honoured. A session carries the entries
// it was spawned with; their values are never re-read from here.
func definedTokens(ts *tokenSet) func([]config.StaticToken) []config.StaticToken {
	return func(in []config.StaticToken) []config.StaticToken {
		entries := ts.Get()
		var out []config.StaticToken
		for _, e := range in {
			if slices.ContainsFunc(entries, func(d config.StaticToken) bool { return d.Name == e.Name }) {
				out = append(out, e)
			}
		}
		return out
	}
}

// brokerSessionView is what the broker may know of a session. A session
// carries the AWS connections it was spawned with (a copy that outlives their
// removal from aws_connections), so unlike token entries they are passed through
// without filtering.
func brokerSessionView(s *session.Session, defined func([]config.StaticToken) []config.StaticToken) broker.SessionView {
	return broker.SessionView{Agent: s.Agent, Nonce: s.BrokerNonce, Boxed: s.Runtime == config.RuntimeBox, GitHub: s.GitHub,
		Live: !s.Exited(), AWS: s.AWS, AWSConns: s.AWSConns, Exclusive: s.Exclusive, Tokens: defined(s.Tokens), PATs: defined(s.PATs)}
}

// serveBroker serves one listener until it closes. A listener closing is how a
// box's socket ends (its last session left) or the daemon stops: not an error.
func serveBroker(srv *http.Server, l net.Listener) {
	if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		log.Printf("credential broker stopped: %v", err)
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
	return startBrokerWithStore(dataDir, stateRoot, goos, port, get, info, apps, entries, nil)
}

// startBrokerWithStore is startBroker with the daemon's encrypted store, which
// serves `store` value references for static tokens and GitHub App keys. A nil
// vstore leaves the references refusing, exactly as without a store.
func startBrokerWithStore(dataDir, stateRoot, goos string, port int, get func(id string) (*session.Session, bool),
	info func(id string) (session.Info, bool), apps []config.GitHubApp, entries []config.StaticToken,
	vstore *secretstore.Store) (*brokerService, error) {
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
	// The same minter and simulator back the credentials screen's base-role
	// check, so a check is the mint and probe a spawn makes.
	var stsMinter broker.Minter = broker.STSMinter{}
	var simulator awsprobe.Simulator = awsprobe.IAMSimulator{}
	tokens := &tokenSet{}
	tokens.Set(entries)
	defined := definedTokens(tokens)
	brk := broker.New(sec.Secret(), func(id string) (broker.SessionView, bool) {
		s, ok := get(id)
		if !ok {
			return broker.SessionView{}, false
		}
		return brokerSessionView(s, defined), true
	}, stsMinter, audit)
	brk.SetSimulator(simulator)
	brk.SetGate(broker.IAMGate)
	// The minter always exists, so an App defined from the web after boot works
	// at once (credLive.Apply -> SetApps); with no App defined every check and
	// mint fails as an unknown App.
	minter := ghapp.NewMinter(apps, ghapp.Client{})
	// A nil *Store must not become a non-nil interface: a store reference
	// then reports "not configured" instead of panicking.
	if vstore != nil {
		minter.SetStore(vstore)
	}
	brk.SetGitHub(minter)
	if vstore != nil {
		brk.SetResolver(credref.Default{Store: vstore})
	}

	ep, err := broker.Listen(goos, stateRoot, port)
	if err != nil {
		_ = audit.Close()
		return nil, fmt.Errorf("%w: %v", errBrokerListen, err)
	}
	// BaseContext puts the listener's identity (a box's own socket, or the old
	// shared one) into every request context: the broker learns which box
	// presents a token from the socket it arrived on.
	srv := &http.Server{Handler: brk.Handler(), BaseContext: broker.BaseContext,
		ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second}
	go serveBroker(srv, ep.Listener)
	if ep.Boxes != nil {
		// A listener per bound box, started as the session layer takes a box's
		// socket and closed when its last session ends.
		ep.Boxes.Start(func(l net.Listener) { go serveBroker(srv, l) })
	}
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
		Status: web.AWSStatusAdapter{Get: info, Stats: brk.Stats, ConnStats: brk.ConnStats,
			Recent: func(id string, n int) ([]broker.Row, error) {
				return audit.ForSessionExcept(id, broker.ProviderStatic, n)
			},
			RecentStatic: func(id string, n int) ([]broker.Row, error) {
				return audit.ForSessionProvider(id, broker.ProviderStatic, n)
			},
			Approvals: func(id string, since int64, n int) ([]broker.ApprovalRow, error) {
				return audit.ActivityApprovals(id, since, 0, n)
			}},
		Activity: web.ActivityAuditAdapter{
			Credentials: audit.ActivityRows,
			Approvals:   audit.ActivityApprovals,
			Minted:      audit.CountMinted,
		},
		SSO: sso, Tokens: tokens, GitHub: minter, Minter: stsMinter, Sim: simulator,
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
			if ep.Boxes != nil {
				ep.Boxes.Close()
			}
			if err := srv.Shutdown(ctx); err != nil {
				_ = srv.Close() // a mint outlived the grace period; the audit store closes next
			}
			// A base-role re-probe started by a mint writes its audit row; let
			// the ones in flight finish (each within its own 5s timeout) first.
			brk.StopProbes(probeStopWait)
			_ = audit.Close()
		},
	}, nil
}
