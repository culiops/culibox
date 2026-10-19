package ghapp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/culiops/agent-box/internal/config"
)

func TestMinterCachesInstallationID(t *testing.T) {
	dir := t.TempDir()
	p := writeKey(t, dir, "k.pem", 0o600, false, genKey(t))
	var instCalls, tokCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/installation") && r.Method == "GET":
			instCalls.Add(1)
			if r.URL.Path != "/repos/Acme/a/installation" && r.URL.Path != "/repos/acme/a/installation" {
				t.Errorf("path = %s", r.URL.Path)
			}
			io.WriteString(w, `{"id":9}`)
		case r.URL.Path == "/app/installations/9/access_tokens":
			tokCalls.Add(1)
			w.WriteHeader(201)
			io.WriteString(w, `{"token":"ghs_1","expires_at":"2026-10-19T12:00:00Z"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	m := NewMinter([]config.GitHubApp{{Name: "app", ClientID: "cid", PrivateKey: config.KeyRef{Type: "file", Path: p}}}, Client{BaseURL: srv.URL})
	if err := m.Check("app"); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"Acme", "acme"} {
		got, err := m.Mint(context.Background(), MintInput{App: "app", Owner: owner, Repos: []string{"a", "b"}, Permissions: map[string]string{"contents": "read"}})
		if err != nil {
			t.Fatal(err)
		}
		if got.Value != "ghs_1" || got.InstallationID != 9 {
			t.Fatalf("minted = %+v", got)
		}
	}
	if instCalls.Load() != 1 || tokCalls.Load() != 2 {
		t.Fatalf("installation calls=%d token calls=%d", instCalls.Load(), tokCalls.Load())
	}
}

func TestMinterUnknownApp(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	m := NewMinter(nil, Client{BaseURL: srv.URL})
	if err := m.Check("nope"); err == nil {
		t.Fatal("Check: want error")
	}
	if _, err := m.Mint(context.Background(), MintInput{App: "nope", Owner: "o", Repos: []string{"r"}}); err == nil {
		t.Fatal("Mint: want error")
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("unexpected http calls: %d", n)
	}
}

func TestMinterMintNeedsRepos(t *testing.T) {
	m := NewMinter([]config.GitHubApp{{Name: "app"}}, Client{})
	if _, err := m.Mint(context.Background(), MintInput{App: "app", Owner: "o"}); err == nil {
		t.Fatal("want error for no repos")
	}
}
