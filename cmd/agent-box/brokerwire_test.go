package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/session"
)

const wireNonce = "0123456789abcdef0123456789abcdef"

func wireBinding() *config.AWSBinding {
	return &config.AWSBinding{SourceProfile: "no-such-profile", RoleARN: "arn:aws:iam::123456789012:role/agentbox-agent",
		Region: "ap-southeast-1", S3Read: []string{"culishop-tfstate"}}
}

// postCreds sends one credential request over the broker's unix socket.
func postCreds(t *testing.T, sock, token string) int {
	t.Helper()
	c := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}, Timeout: 10 * time.Second}
	req, _ := http.NewRequest(http.MethodPost, "http://broker/v1/credentials", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestStartBrokerServesAuditsAndStops(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket transport")
	}
	// A throwaway data dir and an AWS config that cannot reach the real host's:
	// the mint below must fail locally, never call STS.
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "none"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "none"))
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	dataDir := t.TempDir()

	sessions := map[string]*session.Session{
		"claude-3": {ID: "claude-3", Agent: "claude", Runtime: config.RuntimeBox, AWS: wireBinding(), AWSNonce: wireNonce},
		"claude-4": {ID: "claude-4", Agent: "claude", Runtime: config.RuntimeHost, AWS: wireBinding(), AWSNonce: wireNonce},
	}
	svc, err := startBroker(dataDir, dataDir, "linux", 0,
		func(id string) (*session.Session, bool) { s, ok := sessions[id]; return s, ok },
		func(id string) (session.Info, bool) {
			if _, ok := sessions[id]; !ok {
				return session.Info{}, false
			}
			return session.Info{ID: id, AWS: &session.AWSInfo{Label: "x"}}, true
		})
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			svc.Stop(context.Background())
		}
	}()

	// The secret persisted (32 bytes, 0600), the audit store exists, the socket is 0600.
	if fi, err := os.Stat(filepath.Join(dataDir, "broker.secret")); err != nil || fi.Size() != 32 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("broker.secret: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "audit.db")); err != nil {
		t.Fatalf("audit.db: %v", err)
	}
	sock := filepath.Join(dataDir, "broker", "sock", "broker.sock")
	if fi, err := os.Stat(sock); err != nil || fi.Mode().Perm() != 0o600 || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket: %v %v", fi, err)
	}

	// The handler is attached and signs with the persisted secret.
	if got := postCreds(t, sock, "garbage"); got != http.StatusUnauthorized {
		t.Errorf("garbage token: %d want 401", got)
	}
	if got := postCreds(t, sock, svc.Hooks.Token("claude-3", strings.Repeat("b", 32))); got != http.StatusForbidden {
		t.Errorf("another spawn's nonce: %d want 403", got)
	}
	if got := postCreds(t, sock, svc.Hooks.Token("claude-4", wireNonce)); got != http.StatusForbidden {
		t.Errorf("host-tier session: %d want 403 (lookup must report Boxed from Runtime)", got)
	}
	// The live boxed session with its own nonce passes the lookup (Nonce, Boxed,
	// Live and AWS all filled) and reaches the minter, which fails on the missing
	// source profile: 502, not 401/403.
	if got := postCreds(t, sock, svc.Hooks.Token("claude-3", wireNonce)); got != http.StatusBadGateway {
		t.Fatalf("live boxed session: %d want 502 from the minter", got)
	}

	// The status route sees the failed mint through the audit store.
	st, ok := svc.Status.AWSStatus("claude-3")
	if !ok {
		t.Fatal("no status for a bound session")
	}
	raw, _ := json.Marshal(st)
	if !strings.Contains(string(raw), "sts_error") {
		t.Errorf("status lacks the audited failed mint: %s", raw)
	}

	svc.Stop(context.Background())
	stopped = true
	c, err := net.Dial("unix", sock)
	if err == nil {
		c.Close()
		t.Error("broker still accepting after Stop")
	}
}

func TestStartBrokerReusesItsSecretAcrossRestarts(t *testing.T) {
	dataDir := t.TempDir()
	get := func(string) (*session.Session, bool) { return nil, false }
	info := func(string) (session.Info, bool) { return session.Info{}, false }
	a, err := startBroker(dataDir, dataDir, "linux", 0, get, info)
	if err != nil {
		t.Fatal(err)
	}
	tokA := a.Hooks.Token("claude-3", wireNonce)
	a.Stop(context.Background())
	b, err := startBroker(dataDir, dataDir, "linux", 0, get, info)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Stop(context.Background())
	if b.Hooks.Token("claude-3", wireNonce) != tokA {
		t.Fatal("tokens changed across a restart: a reattached box would lose its credentials")
	}
}

func TestStartBrokerRefusesAShortSecret(t *testing.T) {
	dataDir := t.TempDir()
	// A secret file of the wrong size is regenerated by NewCookieAuth; an
	// unreadable one is refused. Either way startBroker never runs on a short key.
	if err := os.Mkdir(filepath.Join(dataDir, "broker.secret"), 0o700); err != nil { // a directory: unreadable as a file
		t.Fatal(err)
	}
	_, err := startBroker(dataDir, dataDir, "linux", 0,
		func(string) (*session.Session, bool) { return nil, false }, func(string) (session.Info, bool) { return session.Info{}, false })
	if err == nil || errors.Is(err, errBrokerListen) || !strings.Contains(err.Error(), "broker secret") {
		t.Fatalf("err = %v, want a fatal broker secret error", err)
	}
}

func TestStartBrokerListenFailureIsNotFatal(t *testing.T) {
	dataDir := t.TempDir()
	// A plain file where the socket dir's parent must be makes Listen fail.
	if err := os.WriteFile(filepath.Join(dataDir, "broker"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := startBroker(dataDir, dataDir, "linux", 0,
		func(string) (*session.Session, bool) { return nil, false }, func(string) (session.Info, bool) { return session.Info{}, false })
	if !errors.Is(err, errBrokerListen) {
		t.Fatalf("err = %v, want errBrokerListen", err)
	}
}

// The lookup the broker uses carries Exclusive from the session: without it
// every grant is refused "box is shared".
func TestBrokerLookupCarriesExclusive(t *testing.T) {
	dataDir := t.TempDir()
	sessions := map[string]*session.Session{"aws-1": boundSess("aws-1"), "aws-2": boundSess("aws-2")}
	sessions["aws-1"].AWSNonce = wireNonce
	sessions["aws-2"].AWSNonce = wireNonce
	sessions["aws-2"].Exclusive = false
	lookAWSBefore := lookAWS
	lookAWS = func() (string, error) { return "", errors.New("none") }
	defer func() { lookAWS = lookAWSBefore }()
	svc, err := startBroker(dataDir, dataDir, "linux", 0,
		func(id string) (*session.Session, bool) { s, ok := sessions[id]; return s, ok },
		func(string) (session.Info, bool) { return session.Info{}, false })
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Stop(context.Background())
	if svc.SSO != nil {
		t.Error("no aws CLI: the runner must be nil")
	}
	// The grant gets as far as the exclusive check only through the lookup's
	// Exclusive: a shared box is refused on it, an exclusive one is not.
	_, err = svc.Hooks.B.Elevate("aws-2", broker.ElevateRequest{Minutes: 30, Reason: "x", Cred: broker.CredCookie})
	if err == nil || !strings.Contains(err.Error(), "shared") {
		t.Errorf("non-exclusive session: %v", err)
	}
	_, err = svc.Hooks.B.Elevate("aws-1", broker.ElevateRequest{Minutes: 30, Reason: "x", Cred: broker.CredCookie})
	if err != nil && strings.Contains(err.Error(), "shared") {
		t.Errorf("exclusive session refused as shared: %v", err)
	}
}

// Stop closes the sign-in runner before the audit store: a flow it kills writes
// its sso_login_failed row while the store is still open.
func TestStopClosesTheSignInRunnerBeforeTheAuditStore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in for the aws CLI")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "aws")
	// Prints a device code like the real CLI, then waits to be killed.
	script := "#!/bin/sh\ncat <<'EOF'\nBrowser will not be automatically opened.\nPlease visit the following URL:\n\n" +
		"https://d-0000000000.awsapps.com/start/#/device\n\nThen enter the code:\n\nABCD-EFGH\n\n" +
		"Alternatively, you may visit the following URL which will autofill the code upon loading:\n" +
		"https://d-0000000000.awsapps.com/start/#/device?user_code=ABCD-EFGH\nEOF\nexec /bin/sleep 600\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	lookAWSBefore := lookAWS
	lookAWS = func() (string, error) { return fake, nil }
	defer func() { lookAWS = lookAWSBefore }()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "none"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "none"))

	dataDir := t.TempDir()
	sessions := map[string]*session.Session{"aws-1": boundSess("aws-1")}
	svc, err := startBroker(dataDir, dataDir, "linux", 0,
		func(id string) (*session.Session, bool) { s, ok := sessions[id]; return s, ok },
		func(string) (session.Info, bool) { return session.Info{}, false })
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			svc.Stop(context.Background())
		}
	}()
	if svc.SSO == nil {
		t.Fatal("aws resolved but no runner")
	}
	brk := svc.Hooks.B
	es := elevationService{brk: brk, runner: svc.SSO, get: func(id string) (*session.Session, bool) { s, ok := sessions[id]; return s, ok }}
	st, err := es.SSOLogin("aws-1")
	if err != nil || st.Code != "ABCD-EFGH" {
		t.Fatalf("start: %+v %v", st, err)
	}
	svc.Stop(context.Background())
	stopped = true

	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM approval_audit WHERE session_id = 'aws-1' AND action = 'sso_login_failed'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("sso_login_failed rows after Stop: %d (%v)", n, err)
	}
}
