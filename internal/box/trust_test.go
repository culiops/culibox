package box

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const trustWS = "/ws/repo"

// trustRoot returns an os.Root over a fresh directory, standing in for a box home.
func trustRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root, dir
}

func readClaudeState(t *testing.T, dir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("box home state does not parse: %v\n%s", err, raw)
	}
	return d
}

func trusted(d map[string]any, ws string) bool {
	projects, _ := d["projects"].(map[string]any)
	entry, _ := projects[ws].(map[string]any)
	return entry["hasTrustDialogAccepted"] == true
}

// Claude Code's trust dialog defaults to "No, exit", so one stray Enter in the
// web terminal killed a freshly spawned boxed session. A new box home trusts
// the one workspace the box serves.
func TestNewBoxHomeTrustsItsWorkspace(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	dir, err := m.ensureStateDir("ab-box-trust1", trustWS, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := readClaudeState(t, dir)
	if d["hasCompletedOnboarding"] != true {
		t.Fatalf("onboarding marker lost: %v", d)
	}
	if !trusted(d, trustWS) {
		t.Fatalf("workspace not pre-trusted: %v", d)
	}
}

// A box home from before this change already holds the agent's own state: the
// trust entry is merged in and nothing else changes.
func TestTrustMergeKeepsOtherKeys(t *testing.T) {
	root, dir := trustRoot(t)
	p := filepath.Join(dir, ".claude.json")
	in := `{"userID":"u1","numStartups":7,"projects":{"/other":{"hasTrustDialogAccepted":true},` +
		`"/ws/repo":{"allowedTools":["Bash"],"hasTrustDialogAccepted":false}}}`
	if err := os.WriteFile(p, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := trustWorkspace(root, trustWS); err != nil {
		t.Fatal(err)
	}
	d := readClaudeState(t, dir)
	if d["userID"] != "u1" || d["numStartups"] != float64(7) {
		t.Fatalf("top-level keys changed: %v", d)
	}
	projects := d["projects"].(map[string]any)
	if !trusted(d, "/other") {
		t.Fatalf("another project's trust was lost: %v", projects)
	}
	entry := projects[trustWS].(map[string]any)
	if tools, _ := entry["allowedTools"].([]any); len(tools) != 1 || tools[0] != "Bash" {
		t.Fatalf("the workspace entry's other keys were lost: %v", entry)
	}
	if !trusted(d, trustWS) {
		t.Fatalf("workspace not trusted after merge: %v", entry)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600 (the file holds agent state)", fi.Mode().Perm())
	}
}

func assertUnchanged(t *testing.T, p string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s was rewritten:\n got %q\nwant %q", p, got, want)
	}
}

// A file the agent left unparsable is not ours to repair: left alone, and the
// spawn still goes ahead (the dialog shows, as it always did).
func TestTrustLeavesUnparsableFileAlone(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	dir := m.stateDir("ab-box-broken")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, ".claude.json")
	body := []byte(`{"broken`)
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := trustWorkspace(root, trustWS); err == nil {
		t.Fatal("an unparsable file must be reported, not merged")
	}
	if _, err := m.ensureStateDir("ab-box-broken", trustWS, nil); err != nil {
		t.Fatalf("trust must never fail a spawn: %v", err)
	}
	assertUnchanged(t, p, body)
}

func TestTrustLeavesNonObjectProjectsAlone(t *testing.T) {
	for _, body := range []string{`{"projects":[1]}`, `{"projects":{"/ws/repo":"x"}}`, `null`, `[1]`} {
		root, dir := trustRoot(t)
		p := filepath.Join(dir, ".claude.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := trustWorkspace(root, trustWS); err == nil {
			t.Fatalf("%s: a shape we do not understand must be left alone", body)
		}
		assertUnchanged(t, p, []byte(body))
	}
}

// The box writes its home as the daemon user, so between two spawns it can
// replace .claude.json with a symlink to a host file. Never followed, never
// replaced.
func TestTrustLeavesSymlinkAlone(t *testing.T) {
	root, dir := trustRoot(t)
	host := filepath.Join(t.TempDir(), "host.json")
	body := []byte(`{"host":true}`)
	if err := os.WriteFile(host, body, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".claude.json")
	if err := os.Symlink(host, link); err != nil {
		t.Fatal(err)
	}
	if err := trustWorkspace(root, trustWS); err == nil {
		t.Fatal("a symlinked state file must be refused")
	}
	assertUnchanged(t, host, body)
	if target, err := os.Readlink(link); err != nil || target != host {
		t.Fatalf("the link itself was replaced: %q %v", target, err)
	}
}

func TestTrustLeavesOversizedFileAlone(t *testing.T) {
	root, dir := trustRoot(t)
	p := filepath.Join(dir, ".claude.json")
	body := []byte(`{"pad":"` + strings.Repeat("a", maxClaudeStateSize) + `"}`)
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := trustWorkspace(root, trustWS); err == nil {
		t.Fatal("a file over the size cap must be left alone")
	}
	assertUnchanged(t, p, body)
}

// Already trusted: no write at all, so the file the agent owns is not churned.
func TestTrustIsIdempotent(t *testing.T) {
	root, dir := trustRoot(t)
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := trustWorkspace(root, trustWS); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, ".claude.json")
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(p, past, past); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(p)
	if err := trustWorkspace(root, trustWS); err != nil {
		t.Fatal(err)
	}
	assertUnchanged(t, p, before)
	fi, _ := os.Stat(p)
	if !fi.ModTime().Equal(past) {
		t.Fatalf("an already-trusted file was rewritten (mtime %v)", fi.ModTime())
	}
}

// A crash between creating the temp file and the rename leaves a stray file;
// the next spawn must still merge, and a successful run leaves no temp behind.
func TestTrustSurvivesLeftoverTempFile(t *testing.T) {
	root, dir := trustRoot(t)
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(dir, trustTempPrefix+"stale")
	if err := os.WriteFile(stray, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := trustWorkspace(root, trustWS); err != nil {
		t.Fatal(err)
	}
	if !trusted(readClaudeState(t, dir), trustWS) {
		t.Fatal("not trusted")
	}
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if len(names) != 2 {
		t.Fatalf("a successful merge left files behind: %v", names)
	}
}

// Host spawns keep the dialog: nothing here may write the host's own state,
// the default account's or another account's.
func TestEnsureStateDirNeverTouchesHostClaudeState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	hostState := filepath.Join(home, ".claude.json")
	acctState := filepath.Join(home, "accounts", "work", ".claude.json")
	body := []byte(`{"host":true}`)
	if err := os.MkdirAll(filepath.Dir(acctState), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{hostState, acctState} {
		if err := os.WriteFile(p, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m, _ := newTestManagerWithFake(t)
	dir, err := m.ensureStateDir("ab-box-hostcheck", home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dir, m.opts.StateRoot) {
		t.Fatalf("box home %q is outside the state root", dir)
	}
	assertUnchanged(t, hostState, body)
	assertUnchanged(t, acctState, body)
}

// Each account gets its own box (and home) for a workspace; each home trusts it.
func TestAccountBoxHomesEachTrustTheWorkspace(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	ws := t.TempDir()
	var dirs []string
	for _, k := range []BoxKey{{}, {Account: "work"}} {
		name, err := m.boxName(ws, k)
		if err != nil {
			t.Fatal(err)
		}
		dir, err := m.ensureStateDir(name, ws, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !trusted(readClaudeState(t, dir), ws) {
			t.Fatalf("account %q box home does not trust %s", k.Account, ws)
		}
		dirs = append(dirs, dir)
	}
	if dirs[0] == dirs[1] {
		t.Fatal("accounts must not share a box home")
	}
}
