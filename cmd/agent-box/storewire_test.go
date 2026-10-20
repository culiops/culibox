package main

import (
	"context"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/secretstore"
	"github.com/culiops/agent-box/internal/session"
)

// The daemon's one store serves a `store` static-token reference through the
// broker, and a lock takes effect on the next fetch.
func TestBrokerServesStoreTokenAndHonoursLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket transport")
	}
	dataDir := shortRoot(t)
	k := secretstore.NewFakeKeeper(true)
	st := secretstore.Open(dataDir, k, nil)
	if err := st.Init([]byte("correct horse battery"), "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.Unlock([]byte("correct horse battery"), time.Hour, "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.Set("grafana", []byte("tok-STORED-5678"), "test"); err != nil {
		t.Fatal(err)
	}
	entry := config.StaticToken{Name: "grafana", Value: config.KeyRef{Type: "store", ID: "grafana"}, Env: []string{"X_TOKEN"},
		Egress: []config.TokenEgress{{Host: "x.example.com"}}}
	sessions := map[string]*session.Session{
		"tok-1": {ID: "tok-1", Agent: "claude", Runtime: config.RuntimeBox, BrokerNonce: wireNonce, Tokens: []config.StaticToken{entry}},
	}
	svc, err := startBrokerWithStore(dataDir, dataDir, "linux", 0,
		func(id string) (*session.Session, bool) { s, ok := sessions[id]; return s, ok },
		func(string) (session.Info, bool) { return session.Info{}, false }, nil, []config.StaticToken{entry}, st)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Stop(context.Background())
	sock := filepath.Join(dataDir, "broker", "sock", "broker.sock")
	tok := svc.Hooks.Token("tok-1", wireNonce)

	if code, body := postStaticToken(t, sock, tok, "grafana"); code != http.StatusOK || !strings.Contains(body, "tok-STORED-5678") {
		t.Fatalf("unlocked store token: status %d body %q", code, body)
	}
	if err := st.Lock("test"); err != nil {
		t.Fatal(err)
	}
	code, body := postStaticToken(t, sock, tok, "grafana")
	if code == http.StatusOK || strings.Contains(body, "tok-STORED-5678") {
		t.Fatalf("locked store token: status %d body %q, want a refusal", code, body)
	}
}

// Without a store (a nil *Store), a store reference refuses cleanly instead of
// panicking through a typed-nil interface.
func TestBrokerNilStoreRefusesStoreReference(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket transport")
	}
	dataDir := shortRoot(t)
	entry := config.StaticToken{Name: "grafana", Value: config.KeyRef{Type: "store", ID: "grafana"}, Env: []string{"X_TOKEN"},
		Egress: []config.TokenEgress{{Host: "x.example.com"}}}
	sessions := map[string]*session.Session{
		"tok-1": {ID: "tok-1", Agent: "claude", Runtime: config.RuntimeBox, BrokerNonce: wireNonce, Tokens: []config.StaticToken{entry}},
	}
	svc, err := startBroker(dataDir, dataDir, "linux", 0,
		func(id string) (*session.Session, bool) { s, ok := sessions[id]; return s, ok },
		func(string) (session.Info, bool) { return session.Info{}, false }, nil, []config.StaticToken{entry})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Stop(context.Background())
	sock := filepath.Join(dataDir, "broker", "sock", "broker.sock")
	if code, _ := postStaticToken(t, sock, svc.Hooks.Token("tok-1", wireNonce), "grafana"); code == http.StatusOK {
		t.Fatalf("a store reference was served with no store (status %d)", code)
	}
}
