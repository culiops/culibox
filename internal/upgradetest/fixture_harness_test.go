//go:build upgrade

package upgradetest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Fixed fixture values (plan: Global Constraints).
const (
	passphrase = "upgrade-test-passphrase-229b"
	account    = "alt"
	tokenName  = "upg"
)

var storeValues = map[string]string{"UPG_ONE": "one-229b", "UPG_TWO": "two-229b"}

// AuditRow is one event of GET /api/audit, reduced to what identifies it
// across versions (the rendered "what" text and the cursor are UI, not data).
type AuditRow struct {
	TS      int64  `json:"ts"`
	Kind    string `json:"kind"`
	Outcome string `json:"outcome"`
	What    string `json:"what"`
}

// Fixture is what vN-1 wrote and what it answered, recorded before the
// upgrade so vN (and vN-1 again, on rollback) can be checked against it.
type Fixture struct {
	Root, Home, Fake string
	Log              string // the serve log both binaries append to
	Token, Bearer    string
	Accounts         json.RawMessage
	Profiles         json.RawMessage
	Audit            []AuditRow
	AuditDB          map[string][]map[string]any
	Analytics        json.RawMessage
	LiveSession      string
	LiveSrvPID       int
	StoreUnlockable  bool
	StoreInitPath    string // "api" or "cli"
}

// WriteFakes writes the fake agent binaries into dir (outside HOME):
// upg-agent sleeps, claude exits at once, upg-secret writes the store value it
// was given to secret.out.
func WriteFakes(t *testing.T, dir string) {
	t.Helper()
	fakes := map[string]string{
		"upg-agent":  "#!/bin/sh\nexec sleep 600\n",
		"claude":     "#!/bin/sh\nexit 0\n",
		"upg-secret": "#!/bin/sh\nprintf 'UPG_ONE=%s\\n' \"$UPG_ONE\" >\"$(dirname \"$0\")/secret.out\"\nexec sleep 30\n",
	}
	for name, body := range fakes {
		writeFile(t, filepath.Join(dir, name), body, 0o755)
	}
}

// WriteConfig writes root/config.json: host tier by default, analytics on,
// the store as a secret source, and the upg-secret agent that reads UPG_ONE.
func WriteConfig(t *testing.T, root, port, fake string) {
	t.Helper()
	cfg := map[string]any{
		"bind_addr":       "127.0.0.1:" + port,
		"default_runtime": "host",
		"analytics":       map[string]any{"enabled": true},
		"secret_sources":  []any{map[string]any{"type": "store"}},
		"agents": []any{
			map[string]any{"name": "sh", "bin": "/bin/sh"},
			map[string]any{"name": "upg-secret", "bin": filepath.Join(fake, "upg-secret"), "secrets": []string{"UPG_ONE"}},
		},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "config.json"), string(b)+"\n", 0o600)
}

// transcript is one Claude Code transcript with two assistant records that
// internal/analytics/parse.go counts.
func transcript(sid string, ts time.Time) string {
	var b strings.Builder
	for i := 0; i < 2; i++ {
		rec := map[string]any{
			"timestamp": ts.Add(time.Duration(i) * time.Minute).Format(time.RFC3339Nano),
			"sessionId": sid,
			"type":      "assistant",
			"message": map[string]any{
				"id": fmt.Sprintf("msg_%s_%d", sid[:8], i), "role": "assistant", "model": "claude-sonnet-4-5",
				"content": []any{map[string]any{"type": "text", "text": "ok"}},
				"usage":   map[string]any{"input_tokens": 10, "output_tokens": 5},
			},
		}
		line, _ := json.Marshal(rec)
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// WriteTranscripts writes one transcript for the default account (under HOME)
// and one for the named account (under the data root).
func WriteTranscripts(t *testing.T, home, root string) {
	t.Helper()
	ts := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	writeFile(t, filepath.Join(home, ".claude/projects/-tmp-upg-a/0b4d3a1e-0000-4000-8000-000000000001.jsonl"),
		transcript("0b4d3a1e-0000-4000-8000-000000000001", ts), 0o600)
	writeFile(t, filepath.Join(root, "accounts", account, "claude/projects/-tmp-upg-b/0b4d3a1e-0000-4000-8000-000000000002.jsonl"),
		transcript("0b4d3a1e-0000-4000-8000-000000000002", ts), 0o600)
}

// SeedOffline writes the clone records no API can create without docker, in
// the formats of cmd/culibox/clonewire.go and internal/clone (baserec.go,
// importrec.go, returnstate.go), plus the darwin-only files on darwin.
func SeedOffline(t *testing.T, root, tmp string) {
	t.Helper()
	clone := filepath.Join(tmp, "clones", "upg")
	if err := os.MkdirAll(clone, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(clone)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(resolved))
	key := hex.EncodeToString(sum[:])
	commit := strings.Repeat("a1", 20)
	writeFile(t, filepath.Join(root, "clone-dirs"), clone+"\n", 0o600)
	writeFile(t, filepath.Join(root, "clone-bases", key), commit+"\n", 0o600)
	writeFile(t, filepath.Join(root, "clone-imports", key), strings.Repeat("b2", 32)+"\n", 0o600)
	writeFile(t, filepath.Join(root, "clone-return", key+".json"),
		`{"v":1,"at":"2026-11-13T10:00:00Z","base":"`+commit+`"}`+"\n", 0o600)
	seedDarwin(t, root)
}

// Populate fills the data root through vN-1's API. live keeps one host
// session running across the upgrade (S1); otherwise it is ended first.
func Populate(t *testing.T, c *Client, prevBin string, env Env, f *Fixture, live bool) {
	t.Helper()
	tok, err := os.ReadFile(filepath.Join(f.Root, "login.token"))
	if err != nil {
		t.Fatal(err)
	}
	f.Token = strings.TrimSpace(string(tok))
	if err := c.Login(f.Token); err != nil {
		t.Fatal(err)
	}
	var created struct {
		Token string `json:"token"`
	}
	must(t, c, "POST", "/api/tokens", map[string]string{"name": tokenName}, &created)
	f.Bearer = created.Token
	must(t, c, "POST", "/api/accounts", map[string]string{"name": account, "provider": "claude"}, nil)
	must(t, c, "POST", "/api/profiles", map[string]any{"name": "upg-host", "bin": filepath.Join(f.Fake, "upg-agent"), "runtime": "host"}, nil)
	must(t, c, "POST", "/api/profiles", map[string]any{"name": "upg-alt", "bin": filepath.Join(f.Fake, "claude"), "runtime": "host", "account": account}, nil)

	var st struct {
		Unlocked bool `json:"unlocked"`
	}
	code, err := c.Do("POST", "/api/store/init", map[string]string{"passphrase": passphrase}, &st)
	switch {
	case err == nil:
		f.StoreInitPath, f.StoreUnlockable = "api", st.Unlocked
	case code == 409 && strings.Contains(err.Error(), "seccomp"):
		storeInitCLI(t, prevBin, env)
		f.StoreInitPath = "cli"
	default:
		t.Fatalf("store init: %v", err)
	}
	t.Logf("PASS store created through the %s (unlocked: %v)", f.StoreInitPath, f.StoreUnlockable)
	for id, v := range storeValues {
		must(t, c, "PUT", "/api/store/values/"+id, map[string]string{"value": v}, nil)
	}

	WriteTranscripts(t, f.Home, f.Root)
	must(t, c, "POST", "/api/analytics/ingest", nil, nil)

	var sess struct {
		ID string `json:"id"`
	}
	must(t, c, "POST", "/api/sessions", map[string]string{"agent": "upg-host", "runtime": "host", "cwd": f.Home}, &sess)
	if sess.ID == "" {
		t.Fatal("spawn returned no session id")
	}
	if live {
		f.LiveSession = sess.ID
		f.LiveSrvPID = srvPID(f.Root, sess.ID)
	} else {
		must(t, c, "DELETE", "/api/sessions/"+sess.ID, nil, nil)
		waitGone(t, c, sess.ID)
	}

	f.Accounts = getRaw(t, c, "/api/accounts")
	f.Profiles = getRaw(t, c, "/api/profiles")
	f.Audit = auditRows(t, c)
	f.Analytics = getRaw(t, c, analyticsPath())
	if len(f.Audit) < 3 {
		t.Fatalf("vN-1 recorded %d audit rows, want at least 3 (store init and two values)", len(f.Audit))
	}
}

func storeInitCLI(t *testing.T, prevBin string, env Env) {
	t.Helper()
	cmd := exec.Command(prevBin, "store", "init")
	cmd.Env = env.Vars()
	cmd.Stdin = strings.NewReader(passphrase + "\n")
	out, err := cmd.CombinedOutput()
	t.Logf("OBSERVE store init on the CLI (the API refused on the Docker seccomp probe): %v: %s", err, strings.TrimSpace(string(out)))
}

// srvPID is the abduco server pid in root/sessions/pids/<id>.pid, or 0.
func srvPID(root, id string) int {
	b, err := os.ReadFile(filepath.Join(root, "sessions", "pids", id+".pid"))
	if err != nil {
		return 0
	}
	var inner, srv int
	for _, f := range strings.Fields(string(b)) {
		fmt.Sscanf(f, "inner=%d", &inner)
		fmt.Sscanf(f, "srv=%d", &srv)
	}
	return srv
}

func waitGone(t *testing.T, c *Client, id string) {
	t.Helper()
	for i := 0; i < 50; i++ {
		var list []struct {
			ID     string `json:"id"`
			Exited bool   `json:"exited"`
		}
		must(t, c, "GET", "/api/sessions", nil, &list)
		found := false
		for _, s := range list {
			if s.ID == id && !s.Exited {
				found = true
			}
		}
		if !found {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("session %s still running after DELETE", id)
}

// analyticsPath covers yesterday through tomorrow, so the fixture's records
// (an hour ago) are inside the range whatever the clock's UTC day.
func analyticsPath() string {
	now := time.Now().UTC()
	return "/api/analytics/summary?from=" + now.AddDate(0, 0, -1).Format("2006-01-02") + "&to=" + now.AddDate(0, 0, 1).Format("2006-01-02")
}

func must(t *testing.T, c *Client, method, path string, body, out any) {
	t.Helper()
	if _, err := c.Do(method, path, body, out); err != nil {
		t.Fatal(err)
	}
}

func getRaw(t *testing.T, c *Client, path string) json.RawMessage {
	t.Helper()
	var raw json.RawMessage
	must(t, c, "GET", path, nil, &raw)
	return raw
}

func auditRows(t *testing.T, c *Client) []AuditRow {
	t.Helper()
	var resp struct {
		Events []AuditRow `json:"events"`
	}
	must(t, c, "GET", "/api/audit", nil, &resp)
	return resp.Events
}
