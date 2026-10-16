package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"time"

	"github.com/culiops/agent-box/internal/auth"
	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/session"
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
	Status web.AWSStatusService // behind GET /api/sessions/{id}/aws
	Stop   func(context.Context)
}

// startBroker brings the credential broker up: the persisted HMAC secret, the
// audit store, the listener and the HTTP server. dataDir holds broker.secret and
// audit.db; stateRoot (symlink-resolved on darwin) holds the socket and the
// per-binding box files. A failure of the secret or the audit store is fatal for
// the caller (a broker without them could mint nothing or forge tokens); a
// failure to listen wraps errBrokerListen.
func startBroker(dataDir, stateRoot, goos string, port int, get func(id string) (*session.Session, bool),
	info func(id string) (session.Info, bool)) (*brokerService, error) {
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
	brk := broker.New(sec.Secret(), func(id string) (broker.SessionView, bool) {
		s, ok := get(id)
		if !ok {
			return broker.SessionView{}, false
		}
		return broker.SessionView{Agent: s.Agent, Nonce: s.AWSNonce, Boxed: s.Runtime == config.RuntimeBox,
			Live: !s.Exited(), AWS: s.AWS}, true
	}, broker.STSMinter{}, audit)

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
	return &brokerService{
		Hooks:  broker.Hooks{B: brk, StateRoot: stateRoot, EP: ep},
		Status: web.AWSStatusAdapter{Get: info, Stats: brk.Stats, Recent: audit.ForSession},
		Stop: func(ctx context.Context) {
			if err := srv.Shutdown(ctx); err != nil {
				_ = srv.Close() // a mint outlived the grace period; the audit store closes next
			}
			_ = audit.Close()
		},
	}, nil
}
