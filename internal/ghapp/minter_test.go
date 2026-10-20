package ghapp

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/credref"
	"github.com/culiops/agent-box/internal/secretstore"
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

func TestMinterRefusesEmptyOrWritePermissions(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	p := writeKey(t, t.TempDir(), "k.pem", 0o600, false, genKey(t))
	m := NewMinter([]config.GitHubApp{{Name: "app", ClientID: "cid", PrivateKey: config.KeyRef{Type: "file", Path: p}}}, Client{BaseURL: srv.URL})
	cases := []struct {
		name  string
		perms map[string]string
		want  string
	}{
		{"nil", nil, ""},
		{"empty", map[string]string{}, ""},
		{"write", map[string]string{"contents": "write"}, "contents"},
		{"mixed", map[string]string{"metadata": "read", "issues": "admin"}, "issues"},
	}
	for _, tc := range cases {
		_, err := m.Mint(context.Background(), MintInput{App: "app", Owner: "o", Repos: []string{"r"}, Permissions: tc.perms})
		if err == nil {
			t.Fatalf("%s: want error", tc.name)
		}
		if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error %q does not name %q", tc.name, err, tc.want)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("http calls = %d, want 0", n)
	}
}

func TestMinterMintNeedsRepos(t *testing.T) {
	m := NewMinter([]config.GitHubApp{{Name: "app"}}, Client{})
	if _, err := m.Mint(context.Background(), MintInput{App: "app", Owner: "o"}); err == nil {
		t.Fatal("want error for no repos")
	}
}

type fakeKeyStore struct {
	raw   []byte
	err   error
	calls int
}

func (f *fakeKeyStore) Get(id string) ([]byte, error) { f.calls++; return f.raw, f.err }

func pemOf(k *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
}

func storeMinter(fs *fakeKeyStore) *Minter {
	m := NewMinter([]config.GitHubApp{{Name: "app", ClientID: "cid", PrivateKey: config.KeyRef{Type: "store", ID: "gh-key"}}}, Client{})
	m.SetStore(fs)
	return m
}

// A PEM key has newlines; credref.Value would refuse it. The store path must
// hand the raw bytes to the PEM parser.
func TestMinterStoreKeyPEMNotValueFiltered(t *testing.T) {
	fs := &fakeKeyStore{raw: pemOf(genKey(t))}
	if err := storeMinter(fs).Check("app"); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

func TestMinterStoreKeyNotCached(t *testing.T) {
	fs := &fakeKeyStore{raw: pemOf(genKey(t))}
	m := storeMinter(fs)
	for i := 0; i < 2; i++ {
		if err := m.Check("app"); err != nil {
			t.Fatal(err)
		}
	}
	if fs.calls != 2 {
		t.Fatalf("getter calls = %d, want 2", fs.calls)
	}
	fs.err = secretstore.ErrLocked
	err := m.Check("app")
	if !errors.Is(err, credref.ErrLocked) {
		t.Fatalf("after lock: %v", err)
	}
}

func TestMinterStoreKeyNoStore(t *testing.T) {
	m := NewMinter([]config.GitHubApp{{Name: "app", ClientID: "cid", PrivateKey: config.KeyRef{Type: "store", ID: "gh-key"}}}, Client{})
	err := m.Check("app")
	if err == nil || !strings.Contains(err.Error(), "store references need the agent-box store (not configured)") {
		t.Fatalf("no store: %v", err)
	}
}

func TestMinterStoreKeyBadPEMNamesNoBytes(t *testing.T) {
	fs := &fakeKeyStore{raw: []byte("SECRET-NOT-PEM")}
	err := storeMinter(fs).Check("app")
	if err == nil || strings.Contains(err.Error(), "SECRET-NOT-PEM") || !strings.Contains(err.Error(), "store gh-key") {
		t.Fatalf("bad pem: %v", err)
	}
}

func TestLoadKeyStillFileOnly(t *testing.T) {
	if _, err := LoadKey(config.KeyRef{Type: "store", ID: "x"}); err == nil {
		t.Fatal("LoadKey must refuse store refs")
	}
}
