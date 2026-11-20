package account

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/statusline"
)

func readJSON(t *testing.T, p string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestProvisionWiresStatusLine(t *testing.T) {
	s, _ := newStore(t)
	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude})
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(s.ScriptPath())
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("script not extracted as 0755: %v %v", fi, err)
	}
	sl := readJSON(t, filepath.Join(r.ConfigDir, "settings.json"))["statusLine"].(map[string]any)
	if sl["type"] != "command" || sl["command"] != s.ScriptPath() {
		t.Fatalf("statusLine = %v", sl)
	}
}

func TestProvisionRewritesScriptOnlyWhenBytesDiffer(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Create(Account{Name: "work", Provider: ProviderClaude}); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(s.ScriptPath())
	if err != nil {
		t.Fatal(err)
	}
	// Same bytes: a second provision must leave the file (inode) alone.
	if _, err := s.Update("work", "", false); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(s.ScriptPath())
	if !os.SameFile(before, after) || !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("script rewritten although its bytes did not change")
	}
	// Stale bytes (an older build's script): refreshed.
	if err := os.WriteFile(s.ScriptPath(), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update("work", "", false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(s.ScriptPath()); string(b) != string(statusline.Script) {
		t.Fatal("stale script not refreshed")
	}
}

func TestProvisionKeepsForeignStatusLine(t *testing.T) {
	s, data := newStore(t)
	cfg := filepath.Join(data, "accounts", "work", "claude")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := `{"statusLine":{"type":"command","command":"/usr/bin/abtop"},"theme":"dark"}`
	if err := os.WriteFile(filepath.Join(cfg, "settings.json"), []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(Account{Name: "work", Provider: ProviderClaude}); err != nil {
		t.Fatal(err)
	}
	m := readJSON(t, filepath.Join(cfg, "settings.json"))
	if m["statusLine"].(map[string]any)["command"] != "/usr/bin/abtop" || m["theme"] != "dark" {
		t.Fatalf("foreign settings clobbered: %v", m)
	}
}

func TestProvisionKeepsNonObjectStatusLine(t *testing.T) {
	for _, sl := range []string{`"/usr/bin/abtop"`, `["x"]`, `null`, `7`} {
		s, data := newStore(t)
		cfg := filepath.Join(data, "accounts", "work", "claude")
		if err := os.MkdirAll(cfg, 0o700); err != nil {
			t.Fatal(err)
		}
		in := `{"statusLine":` + sl + `,"theme":"dark"}`
		if err := os.WriteFile(filepath.Join(cfg, "settings.json"), []byte(in), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Create(Account{Name: "work", Provider: ProviderClaude}); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(filepath.Join(cfg, "settings.json"))
		if string(b) != in {
			t.Fatalf("statusLine %s: settings.json rewritten: %s", sl, b)
		}
	}
}

func TestProvisionInheritToggle(t *testing.T) {
	s, _ := newStore(t)
	// default config: CLAUDE.md + skills present, agents/commands absent
	os.WriteFile(filepath.Join(s.defaultDir, "CLAUDE.md"), []byte("x"), 0o600)
	os.MkdirAll(filepath.Join(s.defaultDir, "skills"), 0o700)
	os.WriteFile(filepath.Join(s.defaultDir, ".credentials.json"), []byte("SECRET"), 0o600)
	os.WriteFile(filepath.Join(filepath.Dir(s.defaultDir), ".claude.json"),
		[]byte(`{"mcpServers":{"remote":{"url":"https://mcp.example/x"},"leaky":{"command":"sh","env":{"TOKEN":"s3cr3t-literal"}}},"oauthAccount":{"x":1}}`), 0o600)

	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"CLAUDE.md", "skills"} {
		if tgt, err := os.Readlink(filepath.Join(r.ConfigDir, n)); err != nil || tgt != filepath.Join(s.defaultDir, n) {
			t.Fatalf("%s link = %q %v", n, tgt, err)
		}
	}
	for _, n := range []string{"agents", "commands", ".credentials.json"} {
		if _, err := os.Lstat(filepath.Join(r.ConfigDir, n)); err == nil {
			t.Fatalf("%s must not be created", n)
		}
	}
	// .claude.json is the onboarding seed only: nothing of the default's
	// ~/.claude.json (its login, its servers) is copied into the account.
	if st := readJSON(t, filepath.Join(r.ConfigDir, ".claude.json")); len(st) != 2 || st["hasCompletedOnboarding"] != true || st["theme"] != "dark" {
		t.Fatalf(".claude.json = %v, want only the onboarding seed", st)
	}
	mcpPath := filepath.Join(r.ConfigDir, MCPFileName)
	mcp := readJSON(t, mcpPath)
	servers := mcp["mcpServers"].(map[string]any)
	if _, ok := servers["remote"]; !ok || mcp["oauthAccount"] != nil {
		t.Fatalf("mcp file = %v", mcp)
	}
	if _, ok := servers["leaky"]; ok {
		t.Fatalf("server with literal env must be filtered out: %v", servers)
	}
	if raw, _ := os.ReadFile(mcpPath); strings.Contains(string(raw), "s3cr3t-literal") {
		t.Fatal("a literal secret reached the written MCP file")
	}
	if m := readJSON(t, filepath.Join(r.ConfigDir, manifestName)); m["mcp"] != true {
		t.Fatalf("manifest = %v", m)
	}

	// A file the user put there by hand must survive toggling off.
	os.WriteFile(filepath.Join(r.ConfigDir, "mine.txt"), []byte("keep"), 0o600)
	if _, err := s.Update("work", "", false); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"CLAUDE.md", "skills", MCPFileName} {
		if _, err := os.Lstat(filepath.Join(r.ConfigDir, n)); err == nil {
			t.Fatalf("%s should be removed when inherit is off", n)
		}
	}
	if _, err := os.Stat(filepath.Join(s.defaultDir, "skills")); err != nil {
		t.Fatal("toggling off must never touch the default dir")
	}
	if _, err := os.Stat(filepath.Join(r.ConfigDir, "mine.txt")); err != nil {
		t.Fatal("user file removed")
	}
}

func TestProvisionNeverReplacesExistingEntry(t *testing.T) {
	s, data := newStore(t)
	os.MkdirAll(filepath.Join(s.defaultDir, "skills"), 0o700)
	cfg := filepath.Join(data, "accounts", "work", "claude")
	os.MkdirAll(filepath.Join(cfg, "skills"), 0o700) // user's own skills dir
	if _, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(filepath.Join(cfg, "skills")); fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("existing skills dir replaced by a symlink")
	}
	if _, err := s.Update("work", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg, "skills")); err != nil {
		t.Fatal("user's skills dir removed on toggle-off (not in manifest)")
	}
}

func TestProvisionRefusesPlantedSymlinkOutsideDataDir(t *testing.T) {
	s, data := newStore(t)
	cfg := filepath.Join(data, "accounts", "work", "claude")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "victim.json")
	if err := os.WriteFile(outside, []byte(`{"keep":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cfg, "settings.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(Account{Name: "work", Provider: ProviderClaude}); err == nil {
		t.Fatal("provision followed a symlink out of the data dir")
	}
	if b, _ := os.ReadFile(outside); string(b) != `{"keep":true}` {
		t.Fatalf("file outside the data dir was written: %s", b)
	}
}

func writeDefaultSettings(t *testing.T, s *Store, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.defaultDir, "settings.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProvisionWritesStatusLineDelegate(t *testing.T) {
	s, _ := newStore(t)
	writeDefaultSettings(t, s, `{"statusLine":{"type":"command","command":"/home/u/.claude/statusline-command.sh"}}`)
	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(r.ConfigDir, DelegateFileName)
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "/home/u/.claude/statusline-command.sh" {
		t.Fatalf("delegate = %q %v", b, err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("delegate mode = %v", fi.Mode().Perm())
	}
	if m := readJSON(t, filepath.Join(r.ConfigDir, manifestName)); m["delegate"] != true {
		t.Fatalf("manifest = %v", m)
	}
	// The account's own statusLine still points at our script, unchanged.
	sl := readJSON(t, filepath.Join(r.ConfigDir, "settings.json"))["statusLine"].(map[string]any)
	if sl["command"] != s.ScriptPath() {
		t.Fatalf("statusLine = %v", sl)
	}
	// Idempotent: re-provisioning keeps the same content and manifest flag.
	if _, err := s.Update("work", "", true); err != nil {
		t.Fatal(err)
	}
	if b2, _ := os.ReadFile(p); string(b2) != string(b) {
		t.Fatalf("delegate changed on re-provision: %q", b2)
	}
	if m := readJSON(t, filepath.Join(r.ConfigDir, manifestName)); m["delegate"] != true {
		t.Fatalf("manifest after re-provision = %v", m)
	}
}

func TestProvisionNoDelegateWithoutUsableStatusLine(t *testing.T) {
	cases := map[string]func(*Store) string{
		"no settings":   func(*Store) string { return "" },
		"no statusLine": func(*Store) string { return `{"theme":"dark"}` },
		"empty command": func(*Store) string { return `{"statusLine":{"type":"command","command":"  "}}` },
		"not a command": func(*Store) string { return `{"statusLine":{"type":"static","command":"x"}}` },
		"our own path": func(s *Store) string {
			return `{"statusLine":{"type":"command","command":"` + s.ScriptPath() + `"}}`
		},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := newStore(t)
			if b := body(s); b != "" {
				writeDefaultSettings(t, s, b)
			}
			r, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(r.ConfigDir, DelegateFileName)); err == nil {
				t.Fatal("delegate must not be written")
			}
		})
	}
}

func TestProvisionNoDelegateWhenInheritOff(t *testing.T) {
	s, _ := newStore(t)
	writeDefaultSettings(t, s, `{"statusLine":{"type":"command","command":"/x/sl.sh"}}`)
	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(r.ConfigDir, DelegateFileName)); err == nil {
		t.Fatal("delegate written although inherit is off")
	}
}

func TestProvisionRemovesDelegateWhenInheritFlipsOff(t *testing.T) {
	s, _ := newStore(t)
	writeDefaultSettings(t, s, `{"statusLine":{"type":"command","command":"/x/sl.sh"}}`)
	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(r.ConfigDir, DelegateFileName)
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update("work", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(p); err == nil {
		t.Fatal("delegate should be removed when inherit is off")
	}
}

func TestProvisionRemovesDelegateWhenDefaultDropsStatusLine(t *testing.T) {
	s, _ := newStore(t)
	writeDefaultSettings(t, s, `{"statusLine":{"type":"command","command":"/x/sl.sh"}}`)
	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true})
	if err != nil {
		t.Fatal(err)
	}
	writeDefaultSettings(t, s, `{}`)
	if _, err := s.Update("work", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(r.ConfigDir, DelegateFileName)); err == nil {
		t.Fatal("stale delegate kept")
	}
}

func TestProvisionKeepsDelegateAgentBoxDidNotWrite(t *testing.T) {
	s, data := newStore(t)
	cfg := filepath.Join(data, "accounts", "work", "claude")
	os.MkdirAll(cfg, 0o700)
	hand := filepath.Join(cfg, DelegateFileName)
	os.WriteFile(hand, []byte("/mine.sh"), 0o600)
	// Default has no statusLine, then inherit flips off: neither path may delete it.
	if _, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(hand); string(b) != "/mine.sh" {
		t.Fatalf("hand-made delegate touched: %q", b)
	}
	if _, err := s.Update("work", "", false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(hand); string(b) != "/mine.sh" {
		t.Fatalf("hand-made delegate removed on toggle-off: %q", b)
	}
}

func TestProvisionRefusesPlantedDelegateSymlink(t *testing.T) {
	s, data := newStore(t)
	writeDefaultSettings(t, s, `{"statusLine":{"type":"command","command":"/x/sl.sh"}}`)
	cfg := filepath.Join(data, "accounts", "work", "claude")
	os.MkdirAll(cfg, 0o700)
	outside := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(outside, []byte("keep"), 0o600)
	os.Symlink(outside, filepath.Join(cfg, DelegateFileName))
	if _, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "keep" {
		t.Fatalf("file outside the data dir was written: %s", b)
	}
}

func TestOpenReprovisionsExistingAccounts(t *testing.T) {
	s, data := newStore(t)
	writeDefaultSettings(t, s, `{"statusLine":{"type":"command","command":"/x/sl.sh"}}`)
	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true})
	if err != nil {
		t.Fatal(err)
	}
	// Roll the account back to what an older build left behind: stale script,
	// no delegate file, manifest without the delegate flag.
	if err := os.WriteFile(s.ScriptPath(), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(r.ConfigDir, DelegateFileName))
	os.WriteFile(filepath.Join(r.ConfigDir, manifestName), []byte(`{"mcp":true}`), 0o600)
	def := s.defaultDir
	s.Close()

	s2, err := Open(filepath.Join(data, "accounts.json"), data, def)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if b, _ := os.ReadFile(s2.ScriptPath()); string(b) != string(statusline.Script) {
		t.Fatal("stale script not refreshed on Open")
	}
	if b, err := os.ReadFile(filepath.Join(r.ConfigDir, DelegateFileName)); err != nil || string(b) != "/x/sl.sh" {
		t.Fatalf("delegate after Open = %q %v", b, err)
	}
}

func TestOpenSurvivesOneAccountFailingToProvision(t *testing.T) {
	s, data := newStore(t)
	writeDefaultSettings(t, s, `{"statusLine":{"type":"command","command":"/x/sl.sh"}}`)
	bad, err := s.Create(Account{Name: "bad", Provider: ProviderClaude, InheritPersonal: true})
	if err != nil {
		t.Fatal(err)
	}
	good, err := s.Create(Account{Name: "good", Provider: ProviderClaude, InheritPersonal: true})
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt bad's settings.json so provision refuses it; remove good's delegate.
	os.WriteFile(filepath.Join(bad.ConfigDir, "settings.json"), []byte("{not json"), 0o600)
	os.Remove(filepath.Join(good.ConfigDir, DelegateFileName))
	def := s.defaultDir
	s.Close()

	s2, err := Open(filepath.Join(data, "accounts.json"), data, def)
	if err != nil {
		t.Fatalf("Open must not fail on one bad account: %v", err)
	}
	defer s2.Close()
	if _, err := os.Stat(filepath.Join(good.ConfigDir, DelegateFileName)); err != nil {
		t.Fatalf("other accounts must still be re-provisioned: %v", err)
	}
}

// Re-provision runs on every startup, so a ~/.claude.json caught mid-write (or
// otherwise invalid) must not wipe every account's MCP servers.
func TestProvisionKeepsMCPWhenSourceInvalid(t *testing.T) {
	s, _ := newStore(t)
	src := filepath.Join(filepath.Dir(s.defaultDir), ".claude.json")
	os.WriteFile(src, []byte(`{"mcpServers":{"remote":{"url":"https://mcp.example/x"}}}`), 0o600)
	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true})
	if err != nil {
		t.Fatal(err)
	}
	mcpPath := filepath.Join(r.ConfigDir, MCPFileName)
	os.WriteFile(src, []byte(`{"mcpServers":{"rem`), 0o600)
	if _, err := s.Update("work", "", true); err != nil {
		t.Fatal(err)
	}
	if _, ok := readJSON(t, mcpPath)["mcpServers"].(map[string]any)["remote"]; !ok {
		t.Fatal("invalid source wiped the account's MCP servers")
	}
	// An absent source is a real "no servers": the copy is emptied.
	os.Remove(src)
	if _, err := s.Update("work", "", true); err != nil {
		t.Fatal(err)
	}
	if n := len(readJSON(t, mcpPath)["mcpServers"].(map[string]any)); n != 0 {
		t.Fatalf("absent source left %d servers", n)
	}
}

// writeAccountSettings writes <data>/accounts/<name>/claude/settings.json.
func writeAccountSettings(t *testing.T, data, name, body string, mode os.FileMode) string {
	t.Helper()
	dir := filepath.Join(data, "accounts", name, "claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRewriteStatusLinePrefix(t *testing.T) {
	data := t.TempDir()
	p := writeAccountSettings(t, data, "work",
		`{"model":"opus","statusLine":{"type":"command","command":"/r/agent-box/bin/agentbox-statusline.sh","padding":0},"env":{"A":"1"}}`, 0o640)
	if err := RewriteStatusLinePrefix(data, "/r/agent-box", "/r/culibox"); err != nil {
		t.Fatal(err)
	}
	m := readJSON(t, p)
	sl, _ := m["statusLine"].(map[string]any)
	if sl["command"] != "/r/culibox/bin/agentbox-statusline.sh" {
		t.Errorf("command = %v", sl["command"])
	}
	if sl["type"] != "command" || sl["padding"] != float64(0) {
		t.Errorf("other statusLine keys not kept: %v", sl)
	}
	if m["model"] != "opus" || m["env"].(map[string]any)["A"] != "1" {
		t.Errorf("other keys not kept: %v", m)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("mode = %o, want 640 (kept)", fi.Mode().Perm())
	}
	// Idempotent.
	if err := RewriteStatusLinePrefix(data, "/r/agent-box", "/r/culibox"); err != nil {
		t.Fatal(err)
	}
	if got := readJSON(t, p)["statusLine"].(map[string]any)["command"]; got != "/r/culibox/bin/agentbox-statusline.sh" {
		t.Errorf("after second run command = %v", got)
	}
}

func TestRewriteStatusLinePrefixKeepsSiblingPrefix(t *testing.T) {
	data := t.TempDir()
	body := `{"statusLine":{"type":"command","command":"/r/agent-box-prod/bin/agentbox-statusline.sh"}}`
	p := writeAccountSettings(t, data, "work", body, 0o600)
	if err := RewriteStatusLinePrefix(data, "/r/agent-box", "/r/culibox"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != body {
		t.Errorf("sibling-prefix settings changed: %s", b)
	}
}

func TestRewriteStatusLinePrefixLeavesUserCommand(t *testing.T) {
	data := t.TempDir()
	user := `{"statusLine":{"type":"command","command":"~/bin/my-status.sh"}}`
	p := writeAccountSettings(t, data, "work", user, 0o600)
	none := `{"model":"opus"}`
	q := writeAccountSettings(t, data, "home", none, 0o600)
	if err := RewriteStatusLinePrefix(data, "/r/agent-box", "/r/culibox"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != user {
		t.Errorf("user command changed: %s", b)
	}
	if b, _ := os.ReadFile(q); string(b) != none {
		t.Errorf("settings without statusLine changed: %s", b)
	}
	// No accounts dir at all is not an error.
	if err := RewriteStatusLinePrefix(t.TempDir(), "/r/agent-box", "/r/culibox"); err != nil {
		t.Errorf("no accounts dir: %v", err)
	}
}
