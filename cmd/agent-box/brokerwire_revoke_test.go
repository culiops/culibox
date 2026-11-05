package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/ghapp"
	"github.com/culiops/culibox/internal/session"
)

// stopMinter mints fixed tokens and, on Revoke, records whether the broker's
// socket still accepted connections: Stop must revoke before the server closes.
type stopMinter struct {
	sock string

	mu         sync.Mutex
	minted     []string
	revoked    []string
	socketOpen []bool
}

func (m *stopMinter) Check(string) error { return nil }

func (m *stopMinter) Mint(_ context.Context, _ ghapp.MintInput) (ghapp.Minted, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tok := "ghs_STOPTOKEN" + string(rune('0'+len(m.minted)))
	m.minted = append(m.minted, tok)
	return ghapp.Minted{Token: ghapp.Token{Value: tok, ExpiresAt: time.Now().Add(time.Hour)}, InstallationID: 9}, nil
}

func (m *stopMinter) Revoke(_ context.Context, tok string) error {
	open := false
	if c, err := net.DialTimeout("unix", m.sock, time.Second); err == nil {
		c.Close()
		open = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revoked = append(m.revoked, tok)
	m.socketOpen = append(m.socketOpen, open)
	return nil
}

// postGHToken asks the broker socket for a GitHub token and returns the status
// and body.
func postGHToken(t *testing.T, sock, token string) (int, string) {
	t.Helper()
	c := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}, Timeout: 10 * time.Second}
	req, _ := http.NewRequest(http.MethodPost, "http://broker/v1/github/token", strings.NewReader(`{"owner":"acme","repo":"api"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestBrokerStopRevokesAll(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket transport")
	}
	dataDir := shortRoot(t)
	sessions := map[string]*session.Session{
		"gh-1": {ID: "gh-1", Agent: "claude", Runtime: config.RuntimeBox, BrokerNonce: wireNonce,
			GitHub: &config.GitHubBinding{Owners: []config.GitHubOwner{{Owner: "acme", App: "main", Repos: []string{"api"}}}}},
	}
	svc, err := startBroker(dataDir, dataDir, "linux", 0,
		func(id string) (*session.Session, bool) { s, ok := sessions[id]; return s, ok },
		func(string) (session.Info, bool) { return session.Info{}, false }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			svc.Stop(context.Background())
		}
	}()
	sock := boxSock(t, svc, wireBox)
	fm := &stopMinter{sock: sock}
	svc.Hooks.B.SetGitHub(fm)

	tok := svc.Hooks.Token("gh-1", wireNonce)
	if code, body := postGHToken(t, sock, tok); code != 200 {
		t.Fatalf("mint: %d %s", code, body)
	}
	fm.mu.Lock()
	n := len(fm.minted)
	fm.mu.Unlock()
	if n != 1 {
		t.Fatalf("minted %d tokens, want 1", n)
	}

	svc.Stop(context.Background())
	stopped = true

	fm.mu.Lock()
	defer fm.mu.Unlock()
	if len(fm.revoked) != 1 || fm.revoked[0] != fm.minted[0] {
		t.Fatalf("Stop revoked %v, want %v", fm.revoked, fm.minted)
	}
	if !fm.socketOpen[0] {
		t.Fatal("the broker server was already closed when Stop revoked: revoke must come first")
	}
}
