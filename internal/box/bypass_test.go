package box

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bypassAccepted(d map[string]any) bool { return d["bypassPermissionsModeAccepted"] == true }

// A box home is created for a session that runs with --dangerously-skip-permissions.
// Claude Code then shows a one-time "Bypass Permissions mode" warning whose
// cursor is on "No, exit", and nothing answers it in an unattended session.
// The key it writes when accepted is merged into the home's state file; every
// other key keeps its value.
func TestBypassAcceptMergeKeepsOtherKeys(t *testing.T) {
	root, dir := trustRoot(t)
	p := filepath.Join(dir, ".claude.json")
	in := `{"userID":"u1","numStartups":7,"hasCompletedOnboarding":true,` +
		`"projects":{"/ws/repo":{"allowedTools":["Bash"],"hasTrustDialogAccepted":true}}}`
	if err := os.WriteFile(p, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := acceptBypassPermissions(root); err != nil {
		t.Fatal(err)
	}
	d := readClaudeState(t, dir)
	if !bypassAccepted(d) {
		t.Fatalf("bypassPermissionsModeAccepted not set: %v", d)
	}
	if d["userID"] != "u1" || d["numStartups"] != float64(7) || d["hasCompletedOnboarding"] != true {
		t.Fatalf("a top-level key changed: %v", d)
	}
	projects, _ := d["projects"].(map[string]any)
	entry, _ := projects["/ws/repo"].(map[string]any)
	tools, _ := entry["allowedTools"].([]any)
	if entry["hasTrustDialogAccepted"] != true || len(tools) != 1 || tools[0] != "Bash" {
		t.Fatalf("a project entry changed: %v", d["projects"])
	}
}

// The agent's own file is not rewritten when the key is already there.
func TestBypassAcceptLeavesAnAcceptedFileUntouched(t *testing.T) {
	root, dir := trustRoot(t)
	p := filepath.Join(dir, ".claude.json")
	in := []byte(`{"bypassPermissionsModeAccepted":true,"a":   1}`)
	if err := os.WriteFile(p, in, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := acceptBypassPermissions(root); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(p); !bytes.Equal(raw, in) {
		t.Fatalf("file rewritten: %s", raw)
	}
}

// A user who answered "No" has a false in the file: the session's option is the
// stronger statement of consent, so the key is set.
func TestBypassAcceptOverwritesAFalse(t *testing.T) {
	root, dir := trustRoot(t)
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"bypassPermissionsModeAccepted":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := acceptBypassPermissions(root); err != nil {
		t.Fatal(err)
	}
	if !bypassAccepted(readClaudeState(t, dir)) {
		t.Fatal("a false was left in place")
	}
}

// What the merge does not understand it leaves alone, and says so.
func TestBypassAcceptLeavesUnreadableStateAlone(t *testing.T) {
	for name, in := range map[string]string{
		"not json":   `{"a":`,
		"an array":   `[1,2]`,
		"null":       `null`,
		"a string":   `"x"`,
		"empty file": ``,
	} {
		t.Run(name, func(t *testing.T) {
			root, dir := trustRoot(t)
			p := filepath.Join(dir, ".claude.json")
			if err := os.WriteFile(p, []byte(in), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := acceptBypassPermissions(root); err == nil {
				t.Fatal("want an error")
			}
			if raw, _ := os.ReadFile(p); string(raw) != in {
				t.Fatalf("file changed: %s", raw)
			}
		})
	}
}

// A symlink planted where the state file belongs is not followed out of the home.
func TestBypassAcceptRefusesASymlinkedStateFile(t *testing.T) {
	root, dir := trustRoot(t)
	outside := filepath.Join(t.TempDir(), "victim.json")
	if err := os.WriteFile(outside, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, ".claude.json")); err != nil {
		t.Fatal(err)
	}
	if err := acceptBypassPermissions(root); err == nil {
		t.Fatal("a symlinked state file must be refused")
	}
	if raw, _ := os.ReadFile(outside); string(raw) != `{}` {
		t.Fatalf("the file behind the link changed: %s", raw)
	}
}

// Wrap seeds the key only when the session layer asks (PreAcceptBypass): the
// option the user chose for the profile or spawn is the consent. A box whose
// session does not bypass keeps the dialog, and trust is independent of it.
func TestWrapPreAcceptsBypassOnlyWhenAsked(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	for _, c := range []struct {
		name        string
		key         BoxKey
		wantBypass  bool
		wantTrusted bool
	}{
		{"not asked", BoxKey{}, false, false},
		{"asked", BoxKey{PreAcceptBypass: true}, true, false},
		{"asked in clone mode", BoxKey{PreAcceptBypass: true, Clone: &CloneMounts{Hash: "h"}}, true, false},
		{"asked with trust", BoxKey{PreAcceptBypass: true, PreTrust: true}, true, true},
		{"trust only", BoxKey{PreTrust: true}, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ws := realTemp(t)
			_, _, name, err := m.Wrap(context.Background(), ws, "s1", c.key, []string{"claude"}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			d := readClaudeState(t, m.stateDir(name))
			if d["hasCompletedOnboarding"] != true {
				t.Fatalf("onboarding seed missing: %v", d)
			}
			if got := bypassAccepted(d); got != c.wantBypass {
				t.Fatalf("bypass accepted = %v, want %v (%v)", got, c.wantBypass, d)
			}
			if got := trusted(d, ws); got != c.wantTrusted {
				t.Fatalf("trusted = %v, want %v (%v)", got, c.wantTrusted, d)
			}
		})
	}
}

// A home that already holds the agent's state (a box that was stopped) gets the
// key merged in when a bypass session recreates the box.
func TestWrapPreAcceptsBypassIntoAnExistingHome(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	ws := realTemp(t)
	name, err := m.boxName(ws, BoxKey{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ensureStateDir(name, "", nil); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(m.stateDir(name), ".claude.json")
	if err := os.WriteFile(p, []byte(`{"mine":"kept","hasCompletedOnboarding":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.Wrap(context.Background(), ws, "s1", BoxKey{PreAcceptBypass: true}, []string{"claude"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), `"kept"`) || !bypassAccepted(readClaudeState(t, m.stateDir(name))) {
		t.Fatalf("existing home not merged: %s", raw)
	}
}

// Consent is not identity: it must not change the box name, or a bypass session
// and a normal one for the same workspace would stop sharing a box.
func TestPreAcceptBypassDoesNotChangeTheBoxName(t *testing.T) {
	m := newTestManager(t)
	ws := t.TempDir()
	a, _ := m.boxName(ws, BoxKey{})
	b, _ := m.boxName(ws, BoxKey{PreAcceptBypass: true})
	if a != b {
		t.Fatalf("PreAcceptBypass changed the box name: %s vs %s", a, b)
	}
}

// Host-tier state is never touched: the seed only ever opens a box home under
// the state root.
func TestBypassSeedNeverTouchesHostState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	hostState := filepath.Join(home, ".claude.json")
	body := []byte(`{"host":true}`)
	if err := os.WriteFile(hostState, body, 0o600); err != nil {
		t.Fatal(err)
	}
	m, _ := newTestManagerWithFake(t)
	ws := realTemp(t)
	_, _, name, err := m.Wrap(context.Background(), ws, "s1", BoxKey{PreAcceptBypass: true}, []string{"claude"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(m.stateDir(name), m.opts.StateRoot) {
		t.Fatalf("box home %q is outside the state root", m.stateDir(name))
	}
	assertUnchanged(t, hostState, body)
}
