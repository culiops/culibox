package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/session"
)

const wireNonce = "0123456789abcdef0123456789abcdef"

// wireBox is the box the wire tests' sessions run in.
const wireBox = "ab-box-0123456789ab"

// boxSock takes svc's broker socket for box (what a bound spawn does) and
// returns its path. The reference is never released: Stop closes every socket.
func boxSock(t *testing.T, svc *brokerService, box string) string {
	t.Helper()
	m, err := svc.Hooks.AcquireBox(box)
	if err != nil || len(m) != 1 {
		t.Fatalf("AcquireBox(%s) = %+v, %v", box, m, err)
	}
	return filepath.Join(m[0].Host, "s")
}

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
	dataDir := shortRoot(t) // holds broker/sock/broker.sock and broker/b/<hex>/s

	sessions := map[string]*session.Session{
		"claude-3": {ID: "claude-3", Agent: "claude", Runtime: config.RuntimeBox, Box: wireBox, AWS: wireBinding(), BrokerNonce: wireNonce},
		"claude-4": {ID: "claude-4", Agent: "claude", Runtime: config.RuntimeHost, AWS: wireBinding(), BrokerNonce: wireNonce},
	}
	svc, err := startBroker(dataDir, dataDir, "linux", 0,
		func(id string) (*session.Session, bool) { s, ok := sessions[id]; return s, ok },
		func(id string) (session.Info, bool) {
			if _, ok := sessions[id]; !ok {
				return session.Info{}, false
			}
			return session.Info{ID: id, AWS: &session.AWSInfo{Label: "x"}}, true
		}, nil, nil)
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
	sock := boxSock(t, svc, wireBox)
	if fi, err := os.Stat(sock); err != nil || fi.Mode().Perm() != 0o600 || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket: %v %v", fi, err)
	}
	// The shared socket (the one boxes from before the per-box sockets mount)
	// still exists too.
	shared := filepath.Join(dataDir, "broker", "sock", "broker.sock")
	if fi, err := os.Stat(shared); err != nil || fi.Mode().Perm() != 0o600 || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("shared socket: %v %v", fi, err)
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
	for _, p := range []string{sock, shared} {
		if c, err := net.Dial("unix", p); err == nil {
			c.Close()
			t.Errorf("broker still accepting on %s after Stop", p)
		}
	}
	if _, err := os.Lstat(sock); err == nil {
		t.Error("the per-box socket file survived Stop")
	}
}

func TestStartBrokerReusesItsSecretAcrossRestarts(t *testing.T) {
	dataDir := shortRoot(t)
	get := func(string) (*session.Session, bool) { return nil, false }
	info := func(string) (session.Info, bool) { return session.Info{}, false }
	a, err := startBroker(dataDir, dataDir, "linux", 0, get, info, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tokA := a.Hooks.Token("claude-3", wireNonce)
	a.Stop(context.Background())
	b, err := startBroker(dataDir, dataDir, "linux", 0, get, info, nil, nil)
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
	// A secret file of the wrong size and an unreadable one are both refused by
	// NewCookieAuth. Either way startBroker never runs on a short key.
	if err := os.Mkdir(filepath.Join(dataDir, "broker.secret"), 0o700); err != nil { // a directory: unreadable as a file
		t.Fatal(err)
	}
	_, err := startBroker(dataDir, dataDir, "linux", 0,
		func(string) (*session.Session, bool) { return nil, false }, func(string) (session.Info, bool) { return session.Info{}, false }, nil, nil)
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
		func(string) (*session.Session, bool) { return nil, false }, func(string) (session.Info, bool) { return session.Info{}, false }, nil, nil)
	if !errors.Is(err, errBrokerListen) {
		t.Fatalf("err = %v, want errBrokerListen", err)
	}
}

// The lookup the broker uses carries Exclusive from the session: without it
// every grant is refused "box is shared".
func TestBrokerLookupCarriesExclusive(t *testing.T) {
	dataDir := shortRoot(t)
	sessions := map[string]*session.Session{"aws-1": boundSess("aws-1"), "aws-2": boundSess("aws-2")}
	sessions["aws-1"].BrokerNonce = wireNonce
	sessions["aws-2"].BrokerNonce = wireNonce
	sessions["aws-2"].Exclusive = false
	lookAWSBefore := lookAWS
	lookAWS = func() (string, error) { return "", errors.New("none") }
	defer func() { lookAWS = lookAWSBefore }()
	svc, err := startBroker(dataDir, dataDir, "linux", 0,
		func(id string) (*session.Session, bool) { s, ok := sessions[id]; return s, ok },
		func(string) (session.Info, bool) { return session.Info{}, false }, nil, nil)
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
	v, err := svc.Hooks.B.Elevate("aws-1", broker.ElevateRequest{Minutes: 30, Reason: "x", Cred: broker.CredCookie})
	if err != nil || !v.Active {
		t.Errorf("exclusive live bound session must be granted: %+v, %v", v, err)
	}
}

// The elevation service maps the grant's reason, confirm label and credential
// kind into the broker's request: three adjacent strings that a transposition
// would break silently.
func TestGrantMapsReasonLabelAndCredIntoTheBrokerRequest(t *testing.T) {
	dataDir := shortRoot(t)
	prod := boundSess("aws-1")
	prod.BrokerNonce = wireNonce
	prod.AWS.Prod, prod.AWS.Label = true, "x"
	sessions := map[string]*session.Session{"aws-1": prod}
	lookAWSBefore := lookAWS
	lookAWS = func() (string, error) { return "", errors.New("none") }
	defer func() { lookAWS = lookAWSBefore }()
	svc, err := startBroker(dataDir, dataDir, "linux", 0,
		func(id string) (*session.Session, bool) { s, ok := sessions[id]; return s, ok },
		func(string) (session.Info, bool) { return session.Info{}, false }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Stop(context.Background())
	blockMsg := ""
	e := elevationService{brk: svc.Hooks.B, get: func(id string) (*session.Session, bool) { s, ok := sessions[id]; return s, ok },
		claim: func(context.Context, string, string) (box.GrantBlock, error) {
			return box.GrantBlock{Why: blockMsg}, nil
		}}
	svc.Hooks.B.SetClaimCheck(e.claimCheck)

	refused := func(what, want string, err error) {
		t.Helper()
		if !errors.Is(err, broker.ErrElevateRefused) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v (want a refusal containing %q)", what, err, want)
		}
	}
	_, err = e.Grant("aws-1", broker.ElevateRequest{Minutes: 30, Reason: strings.Repeat("r", 201), ConfirmLabel: "x", Cred: broker.CredCookie})
	refused("201-char reason", "one line", err)
	_, err = e.Grant("aws-1", broker.ElevateRequest{Minutes: 30, Reason: "apply", ConfirmLabel: "wrong", Cred: broker.CredCookie})
	refused("wrong label", "type the binding label", err)
	_, err = e.Grant("aws-1", broker.ElevateRequest{Minutes: 30, Reason: "apply", ConfirmLabel: "apply", Cred: broker.CredCookie}) // label and reason transposed would pass this
	refused("label equal to the reason", "type the binding label", err)
	_, err = e.Grant("aws-1", broker.ElevateRequest{Minutes: 30, Reason: "apply", ConfirmLabel: "x", Cred: broker.CredBearer})
	refused("bearer credential", "signed-in browser", err)

	blockMsg = "another box (instance PROD) uses /w/proj"
	_, err = e.Grant("aws-1", broker.ElevateRequest{Minutes: 30, Reason: "apply", ConfirmLabel: "x", Cred: broker.CredCookie})
	refused("claim check", blockMsg, err)
	if strings.Contains(err.Error(), "could not check") {
		t.Errorf("the claim-check message was replaced: %v", err)
	}

	blockMsg = ""
	v, err := e.Grant("aws-1", broker.ElevateRequest{Minutes: 30, Reason: "apply vpc", ConfirmLabel: "x", Cred: broker.CredCookie})
	if err != nil || !v.Active || v.Reason != "apply vpc" {
		t.Fatalf("a correct grant: %+v, %v", v, err)
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

	dataDir := shortRoot(t)
	sessions := map[string]*session.Session{"aws-1": boundSess("aws-1")}
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
	if svc.SSO == nil {
		t.Fatal("aws resolved but no runner")
	}
	brk := svc.Hooks.B
	es := elevationService{brk: brk, runner: svc.SSO, get: func(id string) (*session.Session, bool) { s, ok := sessions[id]; return s, ok }}
	st, err := es.SSOLogin("aws-1", "")
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

// A write window open when the daemon went down has no terminal approval row;
// startBroker writes one (`ended`), once, however many times it starts.
func TestStartBrokerClosesWindowsLeftOpenByTheLastRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket transport")
	}
	dataDir := shortRoot(t)
	seed, err := broker.OpenAudit(filepath.Join(dataDir, "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.RecordApproval(broker.ApprovalRow{Time: time.Now(), SessionID: "aws-1", Profile: "aws",
		AccountID: "123456789012", RoleARN: "arn:aws:iam::123456789012:role/agentbox-agent-write",
		Action: broker.ActionGranted, Until: time.Now().Add(time.Hour), Minutes: 60, Reason: "apply", Cred: "cookie"}); err != nil {
		t.Fatal(err)
	}
	seed.Close()
	for i := 0; i < 2; i++ {
		svc, err := startBroker(dataDir, dataDir, "linux", 0,
			func(string) (*session.Session, bool) { return nil, false },
			func(string) (session.Info, bool) { return session.Info{}, false }, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		svc.Stop(context.Background())
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM approval_audit WHERE session_id = 'aws-1' AND action = 'ended'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("ended rows after two starts: %d (%v)", n, err)
	}
}

// A GitHub-bound live boxed session is the only shape that reaches the minter
// check in serveGitHubToken: an unknown session is refused with 403 before it.
func TestStartBrokerWiresGitHub(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket transport")
	}
	sessions := map[string]*session.Session{
		"gh-1": {ID: "gh-1", Agent: "claude", Runtime: config.RuntimeBox, Box: wireBox, BrokerNonce: wireNonce,
			GitHub: &config.GitHubBinding{Owners: []config.GitHubOwner{{Owner: "acme", App: "main", Repos: []string{"api"}}}}},
	}
	get := func(id string) (*session.Session, bool) { s, ok := sessions[id]; return s, ok }
	info := func(string) (session.Info, bool) { return session.Info{}, false }
	// The key file never exists: the minter loads keys lazily, so a configured
	// App fails at mint time (502), while an unconfigured broker answers 503.
	apps := []config.GitHubApp{{Name: "main", ClientID: "Iv1.x",
		PrivateKey: config.KeyRef{Type: "file", Path: "/no/such/agent-box-test.pem"}}}

	cases := []struct {
		name string
		apps []config.GitHubApp
		want int
		body string
	}{
		{"no apps: GitHub unconfigured", nil, http.StatusServiceUnavailable, "GitHub is not configured"},
		{"apps: minter wired", apps, http.StatusBadGateway, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := shortRoot(t)
			svc, err := startBroker(dataDir, dataDir, "linux", 0, get, info, tc.apps, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Stop(context.Background())
			sock := boxSock(t, svc, wireBox)
			code, body := postGHToken(t, sock, svc.Hooks.Token("gh-1", wireNonce))
			if code != tc.want {
				t.Fatalf("status %d (%s), want %d", code, body, tc.want)
			}
			if tc.body != "" && !strings.Contains(body, tc.body) {
				t.Fatalf("body %q lacks %q", body, tc.body)
			}
		})
	}
}

func postStaticToken(t *testing.T, sock, token, name string) (int, string) {
	t.Helper()
	c := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}, Timeout: 10 * time.Second}
	req, _ := http.NewRequest(http.MethodPost, "http://broker/v1/static/token", strings.NewReader(`{"name":"`+name+`"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func tokenEntry(name, path string) config.StaticToken {
	return config.StaticToken{Name: name, Value: config.KeyRef{Type: "file", Path: path}, Env: []string{"X_TOKEN"},
		Egress: []config.TokenEgress{{Host: "x.example.com"}}}
}

// A session carries the entries it was spawned with. One that static_tokens no
// longer defines is dropped from the broker's view of the session (the broker
// answers denied_scope), while a still-defined one is served.
func TestBrokerLookupDropsUndefinedEntries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket transport")
	}
	dataDir := shortRoot(t)
	valFile := filepath.Join(t.TempDir(), "kept")
	if err := os.WriteFile(valFile, []byte("tok-SECRET-1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	kept, old := tokenEntry("kept", valFile), tokenEntry("old", valFile)
	sessions := map[string]*session.Session{
		"tok-1": {ID: "tok-1", Agent: "claude", Runtime: config.RuntimeBox, Box: wireBox, BrokerNonce: wireNonce, Tokens: []config.StaticToken{kept, old}},
	}
	svc, err := startBroker(dataDir, dataDir, "linux", 0,
		func(id string) (*session.Session, bool) { s, ok := sessions[id]; return s, ok },
		func(string) (session.Info, bool) { return session.Info{}, false }, nil, []config.StaticToken{kept})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Stop(context.Background())
	sock := boxSock(t, svc, wireBox)
	tok := svc.Hooks.Token("tok-1", wireNonce)

	if code, body := postStaticToken(t, sock, tok, "kept"); code != http.StatusOK || !strings.Contains(body, "tok-SECRET-1234") {
		t.Fatalf("defined entry: status %d body %q", code, body)
	}
	code, body := postStaticToken(t, sock, tok, "old")
	if code != http.StatusForbidden || !strings.Contains(body, "no token entry named") {
		t.Fatalf("deleted entry: status %d body %q, want 403 denied_scope", code, body)
	}
	if strings.Contains(body, "tok-SECRET-1234") {
		t.Fatalf("a value leaked into a refusal: %q", body)
	}
}

func TestDefinedTokensFiltersTokensAndPATs(t *testing.T) {
	ts := &tokenSet{}
	ts.Set([]config.StaticToken{{Name: "a"}, {Name: "c"}})
	keep := definedTokens(ts)
	in := []config.StaticToken{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	got := keep(in)
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
		t.Fatalf("got %+v", got)
	}
	if keep(nil) != nil || definedTokens(nil)(in) != nil {
		t.Fatal("nothing to keep must be nil, so a session without tokens keeps its exact view")
	}
}

func TestTokenVarsListsEnvReferences(t *testing.T) {
	entries := []config.StaticToken{
		{Name: "a", Value: config.KeyRef{Type: "env", Var: "A_TOKEN"}},
		{Name: "b", Value: config.KeyRef{Type: "file", Path: "/x/b"}},
		{Name: "c", Value: config.KeyRef{Type: "env", Var: "C_TOKEN"}},
	}
	if got := strings.Join(tokenVars(entries), ","); got != "A_TOKEN,C_TOKEN" {
		t.Fatalf("tokenVars = %q", got)
	}
	if tokenVars(nil) != nil {
		t.Fatal("no entries, no vars")
	}
}

func TestEntryMCPCommandsAddsTheLauncher(t *testing.T) {
	entries := []config.StaticToken{
		{Name: "a"},
		{Name: "b", MCP: &config.TokenMCP{Server: "grafana", Command: "mcp-grafana"}},
		{Name: "c", MCP: &config.TokenMCP{Server: "sentry", Command: "mcp-server-sentry"}},
	}
	if got := strings.Join(entryMCPCommands(entries), ","); got != "agentbox-env,mcp-grafana,mcp-server-sentry" {
		t.Fatalf("entryMCPCommands = %q", got)
	}
	if entryMCPCommands(entries[:1]) != nil || entryMCPCommands(nil) != nil {
		t.Fatal("no entry server, nothing extra to probe")
	}
}

// A connection-bound session reaches the broker with its connections (the
// clone it was spawned with, even if aws_connections no longer defines them)
// and no inline binding.
func TestBrokerSessionViewCarriesAWSConns(t *testing.T) {
	s := boundSess("aws-1")
	s.AWS = nil
	s.AWSConns = []config.AWSConnection{{Name: "sbx-a", AWSBinding: *wireBinding()}}
	s.BrokerNonce = wireNonce
	v := brokerSessionView(s, definedTokens(nil))
	if v.AWS != nil || len(v.AWSConns) != 1 || v.AWSConns[0].Name != "sbx-a" || !v.Boxed || v.Nonce != wireNonce {
		t.Fatalf("view = %+v", v)
	}
}

func TestBrokerSessionViewCarriesBox(t *testing.T) {
	s := boundSess("aws-1")
	s.Box = wireBox
	s.BrokerNonce = wireNonce
	if v := brokerSessionView(s, definedTokens(nil)); v.Box != wireBox {
		t.Fatalf("view.Box = %q, want the session's box %q: the broker compares it with the presenting box", v.Box, wireBox)
	}
	s.Box = ""
	if v := brokerSessionView(s, definedTokens(nil)); v.Box != "" {
		t.Fatalf("view.Box = %q for a session with no box", v.Box)
	}
}

// postCredsBody is postCreds with the reply body.
func postCredsBody(t *testing.T, sock, token string) (int, string) {
	t.Helper()
	c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}, Timeout: 10 * time.Second}
	req, _ := http.NewRequest(http.MethodPost, "http://broker/v1/credentials", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// boxWireBroker starts a broker with one bound session, claude-3, in wireBox.
func boxWireBroker(t *testing.T) (*brokerService, string) {
	t.Helper()
	dataDir := shortRoot(t)
	sessions := map[string]*session.Session{
		"claude-3": {ID: "claude-3", Agent: "claude", Runtime: config.RuntimeBox, Box: wireBox, AWS: wireBinding(), BrokerNonce: wireNonce},
	}
	svc, err := startBroker(dataDir, dataDir, "linux", 0,
		func(id string) (*session.Session, bool) { s, ok := sessions[id]; return s, ok },
		func(id string) (session.Info, bool) {
			if _, ok := sessions[id]; !ok {
				return session.Info{}, false
			}
			return session.Info{ID: id, AWS: &session.AWSInfo{Label: "x"}}, true
		}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Stop(context.Background()) })
	return svc, dataDir
}

// The box a token is presented from is the box whose socket it arrives on: the
// token of a session in wireBox is refused on another box's socket, with an
// audit row the session's Access tab shows, and no credential.
func TestStartBrokerRefusesATokenOnAnotherBoxsSocket(t *testing.T) {
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "none"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "none"))
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	svc, _ := boxWireBroker(t)

	tok := svc.Hooks.Token("claude-3", wireNonce)
	other := boxSock(t, svc, "ab-box-bbbbbbbbbbbb")
	if got := postCreds(t, other, tok); got != http.StatusForbidden {
		t.Fatalf("a token presented on another box's socket: %d want 403", got)
	}
	st, ok := svc.Status.AWSStatus("claude-3")
	if !ok {
		t.Fatal("no status for a bound session")
	}
	if raw, _ := json.Marshal(st); !strings.Contains(string(raw), "denied_box") {
		t.Fatalf("the refusal is not audited as denied_box: %s", raw)
	}
	// The owning box's socket passes the box check and reaches the minter,
	// which fails on the missing source profile: 502, not 403.
	if got := postCreds(t, boxSock(t, svc, wireBox), tok); got != http.StatusBadGateway {
		t.Fatalf("the owning box: %d want 502 from the minter", got)
	}
}

// A box started before the per-box sockets still mounts the shared one. It is
// refused with a message that says to respawn, and the row says why.
func TestStartBrokerSharedSocketTellsTheBoxToRespawn(t *testing.T) {
	svc, dataDir := boxWireBroker(t)

	shared := filepath.Join(dataDir, "broker", "sock", "broker.sock")
	code, body := postCredsBody(t, shared, svc.Hooks.Token("claude-3", wireNonce))
	if code != http.StatusForbidden || !strings.Contains(body, "respawn this session") {
		t.Fatalf("shared socket: %d %q, want 403 with the respawn message", code, body)
	}
	st, ok := svc.Status.AWSStatus("claude-3")
	if !ok {
		t.Fatal("no status for a bound session")
	}
	if raw, _ := json.Marshal(st); !strings.Contains(string(raw), "denied_box") {
		t.Fatalf("the refusal is not audited as denied_box: %s", raw)
	}
}

// failListener is a listener whose Accept fails for good (not a close), the
// way a socket that went bad would; box makes it look like a per-box listener.
type failListener struct {
	box string
	err error
}

func (f failListener) Accept() (net.Conn, error) { return nil, f.err }
func (f failListener) Close() error              { return nil }
func (f failListener) Addr() net.Addr            { return &net.UnixAddr{Name: "test", Net: "unix"} }

type namedFailListener struct{ failListener }

func (f namedFailListener) BoxName() string { return f.box }

// captureLog returns what log.Printf wrote while fn ran.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf strings.Builder
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	fn()
	return buf.String()
}

// A per-box socket that dies on its own is logged with its box, so the one
// broken box can be told from a broker that stopped; fail closed either way (a
// listener that is not serving answers nobody).
func TestServeBrokerNamesTheBoxWhenItsSocketStops(t *testing.T) {
	boom := errors.New("accept failed for good")
	srv := &http.Server{}
	got := captureLog(t, func() { serveBroker(srv, namedFailListener{failListener{box: wireBox, err: boom}}) })
	if !strings.Contains(got, "broker socket for box "+wireBox+" stopped") || !strings.Contains(got, boom.Error()) {
		t.Fatalf("log = %q, want the per-box message naming %s", got, wireBox)
	}
	if strings.Contains(got, "credential broker stopped") {
		t.Fatalf("a per-box socket stopping is not the broker stopping: %q", got)
	}

	// Any other listener (the shared socket, the darwin TCP one) keeps the old message.
	got = captureLog(t, func() { serveBroker(srv, failListener{err: boom}) })
	if !strings.Contains(got, "credential broker stopped") || strings.Contains(got, "broker socket for box") {
		t.Fatalf("log = %q, want the broker message", got)
	}

	// A listener that was closed (the box's last session left, or shutdown) is not an error.
	for _, closed := range []error{http.ErrServerClosed, net.ErrClosed} {
		got = captureLog(t, func() { serveBroker(srv, namedFailListener{failListener{box: wireBox, err: closed}}) })
		if got != "" {
			t.Fatalf("a closed listener (%v) was logged: %q", closed, got)
		}
	}
}

func TestBrokerServerTimeoutsAreSet(t *testing.T) {
	srv := newBrokerServer(http.NotFoundHandler())
	if srv.IdleTimeout != 30*time.Second || srv.ReadTimeout != 15*time.Second ||
		srv.ReadHeaderTimeout != 5*time.Second || srv.WriteTimeout != 30*time.Second || srv.BaseContext == nil {
		t.Fatalf("server = idle %v read %v header %v write %v basecontext %v",
			srv.IdleTimeout, srv.ReadTimeout, srv.ReadHeaderTimeout, srv.WriteTimeout, srv.BaseContext != nil)
	}
}

// shrinkBrokerTimeouts makes the broker's idle and read timeouts short for one test.
func shrinkBrokerTimeouts(t *testing.T, idle, read time.Duration) {
	t.Helper()
	oldIdle, oldRead := brokerIdleTimeout, brokerReadTimeout
	brokerIdleTimeout, brokerReadTimeout = idle, read
	t.Cleanup(func() { brokerIdleTimeout, brokerReadTimeout = oldIdle, oldRead })
}

// closedWithin reports whether the server ends c within d (EOF or a reset).
func closedWithin(c net.Conn, d time.Duration) bool {
	c.SetReadDeadline(time.Now().Add(d))
	buf := make([]byte, 512)
	for {
		if _, err := c.Read(buf); err != nil {
			var ne net.Error
			return !(errors.As(err, &ne) && ne.Timeout())
		}
	}
}

// A connection whose request was answered is not held for ever: on macOS one box
// opening many of them and idling would otherwise stall every other box.
func TestBrokerServerClosesAnIdleKeepAliveConnection(t *testing.T) {
	shrinkBrokerTimeouts(t, 150*time.Millisecond, 5*time.Second)
	svc, _ := boxWireBroker(t)
	c, err := net.DialTimeout("unix", boxSock(t, svc, wireBox), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(c, "POST /v1/credentials HTTP/1.1\r\nHost: b\r\nAuthorization: Bearer garbage\r\nContent-Length: 0\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: "POST"})
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("first request: %v %v", resp, err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if !closedWithin(c, 3*time.Second) {
		t.Fatal("the broker kept an idle keep-alive connection open past its idle timeout")
	}
}

// A client that sends its headers and then stalls the body is cut off.
func TestBrokerServerCutsAStalledRequestBody(t *testing.T) {
	shrinkBrokerTimeouts(t, 30*time.Second, 200*time.Millisecond)
	svc, _ := boxWireBroker(t)
	c, err := net.DialTimeout("unix", boxSock(t, svc, wireBox), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	io.WriteString(c, "POST /v1/static/token HTTP/1.1\r\nHost: b\r\nAuthorization: Bearer garbage\r\nContent-Length: 100\r\n\r\n{")
	if !closedWithin(c, 3*time.Second) {
		t.Fatal("the broker kept a connection with a stalled request body open past its read timeout")
	}
}

// The read timeout bounds receiving a request, not answering it: a mint that
// takes far longer than it must still finish, with its request context alive.
// (This is the measurement the timeout's value rests on, see newBrokerServer.)
func TestBrokerServerReadTimeoutDoesNotCutASlowHandler(t *testing.T) {
	shrinkBrokerTimeouts(t, 30*time.Second, 150*time.Millisecond)
	ctxErr := make(chan error, 1)
	srv := newBrokerServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(700 * time.Millisecond) // several read timeouts: a slow STS or GitHub mint
		ctxErr <- r.Context().Err()
		io.WriteString(w, "minted")
	}))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	defer srv.Close()
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Post("http://"+l.Addr().String(), "application/json", strings.NewReader(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "minted" {
		t.Fatalf("reply %q", b)
	}
	if err := <-ctxErr; err != nil {
		t.Fatalf("the request context was cancelled during a slow handler: %v", err)
	}
}
