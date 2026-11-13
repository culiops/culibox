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
	"github.com/culiops/culibox/internal/boxaddr"
	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credref"
	"github.com/culiops/culibox/internal/ghapp"
	"github.com/culiops/culibox/internal/proxyline"
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
	return broker.SessionView{Agent: s.Agent, Nonce: s.BrokerNonce, Boxed: s.Runtime == config.RuntimeBox, Box: s.Box, GitHub: s.GitHub,
		Live: !s.Exited(), AWS: s.AWS, AWSConns: s.AWSConns, Exclusive: s.Exclusive, Tokens: defined(s.Tokens), PATs: defined(s.PATs)}
}

// How long the broker keeps a connection that is not in the middle of a
// request, and how long it waits for one request to arrive in full. Variables
// so a test can shrink them.
var (
	brokerIdleTimeout = 30 * time.Second
	brokerReadTimeout = 15 * time.Second
)

// newBrokerServer is the broker's HTTP server, shared by every listener (the
// Linux per-box sockets and the shared one, the macOS TCP listener).
//
// IdleTimeout: without it a keep-alive connection that has had its one request
// answered is held forever, and on macOS one box can fill the connection budget
// with such connections. 30 s, as the egress proxy keeps its idle ones.
//
// ReadTimeout bounds receiving ONE request (headers and body), so a client that
// stalls a body cannot hold a connection either. It does not run while a handler
// does: net/http clears the read deadline when the handler starts reading
// ahead, so an STS or GitHub mint that takes 10 or 20 seconds is never cut by
// it (measured). It is 15 s because the broker's own clients give up on a call
// after 10 s (AWS, static tokens) or 15 s (GitHub): a request still arriving
// after that has no one waiting for it, and a request that is a few hundred
// bytes never needs more than a moment.
func newBrokerServer(h http.Handler) *http.Server {
	return &http.Server{Handler: h, BaseContext: broker.BaseContext,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: brokerReadTimeout, IdleTimeout: brokerIdleTimeout,
		WriteTimeout: 30 * time.Second}
}

// serveBroker serves one listener until it closes. A listener closing is how a
// box's socket ends (its last session left) or the daemon stops: not an error.
// One that fails for another reason is logged, a per-box socket by its box so
// the one broken box can be told from the broker stopping. Either way Serve has
// closed it: nothing is served over it any more (fail closed).
func serveBroker(srv *http.Server, l net.Listener) {
	err := srv.Serve(l)
	if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return
	}
	if n, ok := l.(interface{ BoxName() string }); ok {
		log.Printf("broker socket for box %s stopped: %v", n.BoxName(), err)
		return
	}
	log.Printf("credential broker stopped: %v", err)
}

// startBroker brings the credential broker up: the persisted HMAC secret, the
// audit store, the listener and the HTTP server. dataDir holds broker.secret and
// audit.db; stateRoot (symlink-resolved on darwin) holds the socket and the
// per-binding box files. A failure of the secret or the audit store is fatal for
// the caller (a broker without them could mint nothing or forge tokens); a
// failure to listen wraps errBrokerListen.
func startBroker(dataDir, stateRoot, goos string, port int, get func(id string) (*session.Session, bool),
	info func(id string) (session.Info, bool), apps []config.GitHubApp, entries []config.StaticToken) (*brokerService, error) {
	return startBrokerWithStore(dataDir, stateRoot, goos, port, get, info, apps, entries, nil, nil, proxyline.Key{})
}

// startBrokerWithStore is startBroker with the daemon's encrypted store, which
// serves `store` value references for static tokens and GitHub App keys. A nil
// vstore leaves the references refusing, exactly as without a store. addrs is
// the layer's box-address cache, which decides the box of a request that
// arrives on no per-box socket (macOS); nil leaves every such request refused.
// relayKey is the key the relay proves on macOS (the layer's); the darwin
// listener refuses to open without it, and Linux ignores it.
func startBrokerWithStore(dataDir, stateRoot, goos string, port int, get func(id string) (*session.Session, bool),
	info func(id string) (session.Info, bool), apps []config.GitHubApp, entries []config.StaticToken,
	vstore *secretstore.Store, addrs *boxaddr.Cache, relayKey proxyline.Key) (*brokerService, error) {
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
	// Only a real cache: a nil *Cache inside the interface would panic on the
	// first request instead of refusing. No goos branch: a Linux request always
	// arrives on a per-box (or the shared) socket, which decides before this is
	// consulted.
	if addrs != nil {
		brk.SetBoxSource(addrs)
	}

	ep, err := broker.Listen(goos, stateRoot, port, relayKey)
	if err != nil {
		_ = audit.Close()
		return nil, fmt.Errorf("%w: %v", errBrokerListen, err)
	}
	// BaseContext puts the listener's identity (a box's own socket, or the old
	// shared one) into every request context: the broker learns which box
	// presents a token from the socket it arrived on.
	srv := newBrokerServer(brk.Handler())
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
