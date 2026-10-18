package account

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

const fullDefault = `{
  "model": "opus",
  "effortLevel": "high",
  "env": {"CLAUDE_CODE_SUBAGENT_MODEL": "sonnet"},
  "hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "/home/u/.claude/hooks/block-bunny-writes.sh"}]}]},
  "permissions": {"deny": ["Read(.ssh/**)"]},
  "enabledPlugins": {"superpowers@official": true},
  "deny": ["Read(.env*)"],
  "statusLine": {"type": "command", "command": "/home/u/.claude/statusline-command.sh"},
  "theme": "dark"
}`

func settingsOf(t *testing.T, r Resolved) map[string]any {
	t.Helper()
	return readJSON(t, filepath.Join(r.ConfigDir, "settings.json"))
}

func writeSettings(t *testing.T, r Resolved, m map[string]any) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.ConfigDir, "settings.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func keysOf(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func recordedKeys(t *testing.T, r Resolved) []string {
	t.Helper()
	m := readJSON(t, filepath.Join(r.ConfigDir, manifestName))
	var out []string
	for _, k := range m["settings"].([]any) {
		out = append(out, k.(string))
	}
	sort.Strings(out)
	return out
}

var sixKeys = []string{"effortLevel", "enabledPlugins", "env", "hooks", "model", "permissions"}

func TestInheritCopiesOnlyTheSixKeysAndRecordsThem(t *testing.T) {
	s, _ := newStore(t)
	writeDefaultSettings(t, s, fullDefault)
	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true})
	if err != nil {
		t.Fatal(err)
	}
	got := settingsOf(t, r)
	want := append([]string{"statusLine"}, sixKeys...)
	sort.Strings(want)
	if !reflect.DeepEqual(keysOf(got), want) {
		t.Fatalf("keys = %v, want %v", keysOf(got), want)
	}
	if got["deny"] != nil || got["theme"] != nil {
		t.Fatal("top-level deny / unlisted keys must not be copied")
	}
	if sl := got["statusLine"].(map[string]any); sl["command"] != s.ScriptPath() {
		t.Fatalf("statusLine must stay agent-box's: %v", sl)
	}
	if got["model"] != "opus" {
		t.Fatalf("model = %v", got["model"])
	}
	if !reflect.DeepEqual(recordedKeys(t, r), sixKeys) {
		t.Fatalf("manifest settings = %v", recordedKeys(t, r))
	}
	if fi, _ := os.Stat(filepath.Join(r.ConfigDir, "settings.json")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode().Perm())
	}
}

func TestInheritOffRemovesExactlyTheCopiedKeys(t *testing.T) {
	s, _ := newStore(t)
	writeDefaultSettings(t, s, fullDefault)
	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true})
	if err != nil {
		t.Fatal(err)
	}
	// A key the user adds by hand afterwards must survive.
	m := settingsOf(t, r)
	m["theme"] = "light"
	writeSettings(t, r, m)

	if _, err := s.Update("work", "", false); err != nil {
		t.Fatal(err)
	}
	got := settingsOf(t, r)
	if !reflect.DeepEqual(keysOf(got), []string{"statusLine", "theme"}) {
		t.Fatalf("after inherit off keys = %v", keysOf(got))
	}
	if man := readJSON(t, filepath.Join(r.ConfigDir, manifestName)); man["settings"] != nil {
		t.Fatalf("manifest still lists settings: %v", man)
	}
}

func TestInheritNeverTouchesAForeignStatusLine(t *testing.T) {
	s, data := newStore(t)
	writeDefaultSettings(t, s, fullDefault)
	cfg := filepath.Join(data, "accounts", "work", "claude")
	os.MkdirAll(cfg, 0o700)
	os.WriteFile(filepath.Join(cfg, "settings.json"),
		[]byte(`{"statusLine":{"type":"command","command":"/usr/bin/abtop"},"theme":"dark"}`), 0o600)
	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true})
	if err != nil {
		t.Fatal(err)
	}
	got := settingsOf(t, r)
	if sl := got["statusLine"].(map[string]any); sl["command"] != "/usr/bin/abtop" {
		t.Fatalf("foreign statusLine replaced: %v", sl)
	}
	if got["theme"] != "dark" || got["hooks"] == nil {
		t.Fatalf("settings = %v", got)
	}
	if _, err := s.Update("work", "", false); err != nil {
		t.Fatal(err)
	}
	got = settingsOf(t, r)
	if sl := got["statusLine"].(map[string]any); sl["command"] != "/usr/bin/abtop" || got["theme"] != "dark" || len(got) != 2 {
		t.Fatalf("after off = %v", got)
	}
}

func TestInheritRefusesSecretNamedEnvEntries(t *testing.T) {
	s, _ := newStore(t)
	writeDefaultSettings(t, s, `{"env":{
      "CLAUDE_CODE_SUBAGENT_MODEL":"sonnet",
      "AB_PROD_PASS":"hunter2-literal",
      "CLAUDE_CODE_OAUTH_TOKEN":"sk-oauth-literal",
      "ANTHROPIC_API_KEY":"sk-ant-literal",
      "OPENAI_API_KEY":"sk-openai-literal",
      "CLAUDE_CONFIG_DIR":"/elsewhere",
      "CODEX_HOME":"/elsewhere"}}`)
	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true})
	if err != nil {
		t.Fatal(err)
	}
	env := settingsOf(t, r)["env"].(map[string]any)
	if !reflect.DeepEqual(keysOf(env), []string{"CLAUDE_CODE_SUBAGENT_MODEL"}) {
		t.Fatalf("env = %v", env)
	}
	raw, _ := os.ReadFile(filepath.Join(r.ConfigDir, "settings.json"))
	for _, secret := range []string{"hunter2-literal", "sk-oauth-literal", "sk-ant-literal", "sk-openai-literal", "/elsewhere"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("%q reached the account's settings.json", secret)
		}
	}
}

func TestInheritEnvWithOnlyRefusedEntriesIsNotCopied(t *testing.T) {
	s, _ := newStore(t)
	writeDefaultSettings(t, s, `{"env":{"AB_X":"v"},"model":"opus"}`)
	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := settingsOf(t, r); got["env"] != nil || !reflect.DeepEqual(recordedKeys(t, r), []string{"model"}) {
		t.Fatalf("settings = %v recorded = %v", got, recordedKeys(t, r))
	}
}

func TestInheritPropagatesChangedAndRemovedDefaultKeys(t *testing.T) {
	s, _ := newStore(t)
	writeDefaultSettings(t, s, fullDefault)
	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true})
	if err != nil {
		t.Fatal(err)
	}
	writeDefaultSettings(t, s, `{"model":"sonnet","hooks":{"PreToolUse":[]}}`)
	if err := s.Refresh("work"); err != nil {
		t.Fatal(err)
	}
	got := settingsOf(t, r)
	if !reflect.DeepEqual(keysOf(got), []string{"hooks", "model", "statusLine"}) {
		t.Fatalf("keys = %v", keysOf(got))
	}
	if got["model"] != "sonnet" {
		t.Fatalf("model not propagated: %v", got["model"])
	}
	if !reflect.DeepEqual(recordedKeys(t, r), []string{"hooks", "model"}) {
		t.Fatalf("recorded = %v", recordedKeys(t, r))
	}
	// No default settings.json at all: everything copied is withdrawn.
	os.Remove(filepath.Join(s.defaultDir, "settings.json"))
	if err := s.Refresh("work"); err != nil {
		t.Fatal(err)
	}
	if got := settingsOf(t, r); !reflect.DeepEqual(keysOf(got), []string{"statusLine"}) {
		t.Fatalf("keys after default vanished = %v", keysOf(got))
	}
}

func TestInheritKeepsUserKeyAbsentFromDefault(t *testing.T) {
	s, data := newStore(t)
	writeDefaultSettings(t, s, `{"model":"opus"}`)
	cfg := filepath.Join(data, "accounts", "work", "claude")
	os.MkdirAll(cfg, 0o700)
	// The user's own effortLevel; the default has none, agent-box never copied it.
	os.WriteFile(filepath.Join(cfg, "settings.json"), []byte(`{"effortLevel":"low"}`), 0o600)
	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true})
	if err != nil {
		t.Fatal(err)
	}
	got := settingsOf(t, r)
	if got["effortLevel"] != "low" || got["model"] != "opus" {
		t.Fatalf("settings = %v", got)
	}
	if !reflect.DeepEqual(recordedKeys(t, r), []string{"model"}) {
		t.Fatalf("recorded = %v", recordedKeys(t, r))
	}
	if _, err := s.Update("work", "", false); err != nil {
		t.Fatal(err)
	}
	if got := settingsOf(t, r); got["effortLevel"] != "low" || got["model"] != nil {
		t.Fatalf("after off = %v", got)
	}
}

// The PROD `coding` account was patched by hand (hooks/env/deny copied in,
// nothing recorded). The default is the source of truth while inherit is on, so
// a same-named unrecorded key is adopted: overwritten and recorded. Keys the
// stopgap added that are not inherited (top-level deny) are left alone.
func TestInheritAdoptsHandAppliedStopgapKeys(t *testing.T) {
	s, data := newStore(t)
	writeDefaultSettings(t, s, fullDefault)
	cfg := filepath.Join(data, "accounts", "coding", "claude")
	os.MkdirAll(cfg, 0o700)
	os.WriteFile(filepath.Join(cfg, "settings.json"), []byte(`{
      "statusLine":{"type":"command","command":"/old/script.sh"},
      "hooks":{"PreToolUse":[{"stale":true}]},
      "env":{"CLAUDE_CODE_SUBAGENT_MODEL":"haiku","MINE":"1"},
      "deny":["Read(.env*)"]}`), 0o600)
	r, err := s.Create(Account{Name: "coding", Provider: ProviderClaude, InheritPersonal: true})
	if err != nil {
		t.Fatal(err)
	}
	got := settingsOf(t, r)
	if env := got["env"].(map[string]any); !reflect.DeepEqual(env, map[string]any{"CLAUDE_CODE_SUBAGENT_MODEL": "sonnet"}) {
		t.Fatalf("env not overwritten by the default: %v", env)
	}
	if h, _ := got["hooks"].(map[string]any); h["PreToolUse"].([]any)[0].(map[string]any)["matcher"] != "Bash" {
		t.Fatalf("hooks not overwritten: %v", got["hooks"])
	}
	if got["deny"] == nil {
		t.Fatal("a non-inherited stopgap key must be left alone")
	}
	if !reflect.DeepEqual(recordedKeys(t, r), sixKeys) {
		t.Fatalf("recorded = %v", recordedKeys(t, r))
	}
}

func TestInheritKeepsCopyWhenDefaultSettingsUnreadable(t *testing.T) {
	s, _ := newStore(t)
	writeDefaultSettings(t, s, `{"model":"opus","hooks":{"a":1}}`)
	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true})
	if err != nil {
		t.Fatal(err)
	}
	writeDefaultSettings(t, s, `{"model": "opu`) // mid-write
	if err := s.Refresh("work"); err != nil {
		t.Fatal(err)
	}
	if got := settingsOf(t, r); got["model"] != "opus" || got["hooks"] == nil {
		t.Fatalf("last good copy wiped: %v", got)
	}
	if !reflect.DeepEqual(recordedKeys(t, r), []string{"hooks", "model"}) {
		t.Fatalf("recorded = %v", recordedKeys(t, r))
	}
}

func TestInheritDoesNotRewriteSettingsWhenNothingChanged(t *testing.T) {
	s, _ := newStore(t)
	writeDefaultSettings(t, s, fullDefault)
	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(r.ConfigDir, "settings.json")
	before, _ := os.Stat(p)
	if err := s.Refresh("work"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(p)
	if !os.SameFile(before, after) {
		t.Fatal("settings.json rewritten although nothing changed")
	}
}

func TestInheritOffAccountNeverGetsSettings(t *testing.T) {
	s, _ := newStore(t)
	writeDefaultSettings(t, s, fullDefault)
	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude})
	if err != nil {
		t.Fatal(err)
	}
	if got := settingsOf(t, r); !reflect.DeepEqual(keysOf(got), []string{"statusLine"}) {
		t.Fatalf("keys = %v", keysOf(got))
	}
}

func TestRefreshUnknownAccount(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Refresh("nope"); err == nil {
		t.Fatal("want ErrUnknown")
	}
	if err := s.Refresh(Default); err != nil {
		t.Fatalf("default is a no-op: %v", err)
	}
}

// A manifest is a file on disk the user (or a box that reached it) can edit. It
// may only ever authorise removing the six inheritable keys, never statusLine
// or anything else in settings.json.
func TestInheritIgnoresTamperedManifestKeys(t *testing.T) {
	s, _ := newStore(t)
	writeDefaultSettings(t, s, `{"model":"opus"}`)
	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true})
	if err != nil {
		t.Fatal(err)
	}
	m := settingsOf(t, r)
	m["theme"] = "dark"
	m["hooks"] = map[string]any{"stale": true}
	writeSettings(t, r, m)
	if err := os.WriteFile(filepath.Join(r.ConfigDir, manifestName),
		[]byte(`{"settings":["statusLine","theme","hooks","model"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh("work"); err != nil {
		t.Fatal(err)
	}
	got := settingsOf(t, r)
	if got["statusLine"] == nil || got["theme"] != "dark" {
		t.Fatalf("a tampered manifest removed a key it does not own: %v", got)
	}
	if got["hooks"] != nil || got["model"] != "opus" {
		t.Fatalf("listed inheritable keys must still follow the default: %v", got)
	}
	if !reflect.DeepEqual(recordedKeys(t, r), []string{"model"}) {
		t.Fatalf("recorded = %v", recordedKeys(t, r))
	}
	// Same through the off path.
	if err := os.WriteFile(filepath.Join(r.ConfigDir, manifestName), []byte(`{"settings":["statusLine","theme"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update("work", "", false); err != nil {
		t.Fatal(err)
	}
	if got := settingsOf(t, r); got["statusLine"] == nil || got["theme"] != "dark" {
		t.Fatalf("off path removed a foreign key: %v", got)
	}
}

// A default settings.json holding a bare `null` parses without error to a nil
// map; it is as unreadable as invalid JSON and must not withdraw the copy.
func TestInheritKeepsCopyWhenDefaultSettingsIsNotAnObject(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `"x"`, `7`} {
		s, _ := newStore(t)
		writeDefaultSettings(t, s, `{"model":"opus","hooks":{"a":1}}`)
		r, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true})
		if err != nil {
			t.Fatal(err)
		}
		writeDefaultSettings(t, s, body)
		if err := s.Refresh("work"); err != nil {
			t.Fatal(err)
		}
		if got := settingsOf(t, r); got["model"] != "opus" || got["hooks"] == nil {
			t.Fatalf("default %s wiped the copy: %v", body, got)
		}
	}
}

// By shape, not just by name: a credential under a harmless name is refused too.
func TestInheritRefusesCredentialShapedEnvValues(t *testing.T) {
	s, _ := newStore(t)
	writeDefaultSettings(t, s, `{"env":{
      "CLAUDE_CODE_SUBAGENT_MODEL":"sonnet",
      "HARMLESS_NAME":"sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789",
      "ALSO_FINE":"ghp_abcdefghijklmnopqrstuvwxyz0123456789",
      "AWS_THING":"AKIAABCDEFGHIJKLMNOP"}}`)
	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true})
	if err != nil {
		t.Fatal(err)
	}
	env := settingsOf(t, r)["env"].(map[string]any)
	if !reflect.DeepEqual(keysOf(env), []string{"CLAUDE_CODE_SUBAGENT_MODEL"}) {
		t.Fatalf("env = %v", env)
	}
	raw, _ := os.ReadFile(filepath.Join(r.ConfigDir, "settings.json"))
	for _, secret := range []string{"sk-ant-api03", "ghp_abcdef", "AKIAABCDEF"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("%q reached the account's settings.json", secret)
		}
	}
}

func TestInheritLinksPluginsAndRecordsIt(t *testing.T) {
	s, _ := newStore(t)
	os.MkdirAll(filepath.Join(s.defaultDir, "plugins", "cache"), 0o700)
	os.WriteFile(filepath.Join(s.defaultDir, "plugins", "installed_plugins.json"), []byte(`{"version":2,"plugins":{}}`), 0o600)
	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true})
	if err != nil {
		t.Fatal(err)
	}
	tgt, err := os.Readlink(filepath.Join(r.ConfigDir, "plugins"))
	if err != nil || tgt != filepath.Join(s.defaultDir, "plugins") {
		t.Fatalf("plugins link = %q %v", tgt, err)
	}
	if m := readJSON(t, filepath.Join(r.ConfigDir, manifestName)); !reflect.DeepEqual(m["links"], []any{"plugins"}) {
		t.Fatalf("manifest = %v", m)
	}
	if _, err := s.Update("work", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(r.ConfigDir, "plugins")); err == nil {
		t.Fatal("plugins link must go when inherit is off")
	}
	if _, err := os.Stat(filepath.Join(s.defaultDir, "plugins", "installed_plugins.json")); err != nil {
		t.Fatal("toggling off must never touch the default's plugins")
	}
}

// A real plugins dir already in the account (marketplaces the account fetched
// itself) is never replaced; only the two entries that resolve installed
// plugins are linked into it, and only those are removed again.
func TestInheritLinksPluginEntriesIntoExistingPluginsDir(t *testing.T) {
	s, data := newStore(t)
	os.MkdirAll(filepath.Join(s.defaultDir, "plugins", "cache"), 0o700)
	os.WriteFile(filepath.Join(s.defaultDir, "plugins", "installed_plugins.json"), []byte(`{}`), 0o600)
	mine := filepath.Join(data, "accounts", "work", "claude", "plugins")
	os.MkdirAll(filepath.Join(mine, "marketplaces"), 0o700)
	os.WriteFile(filepath.Join(mine, "known_marketplaces.json"), []byte(`{}`), 0o600)
	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true})
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(mine); fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("an existing plugins dir was replaced")
	}
	for _, n := range []string{"installed_plugins.json", "cache"} {
		if tgt, err := os.Readlink(filepath.Join(mine, n)); err != nil || tgt != filepath.Join(s.defaultDir, "plugins", n) {
			t.Fatalf("%s link = %q %v", n, tgt, err)
		}
	}
	if _, err := s.Update("work", "", false); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"installed_plugins.json", "cache"} {
		if _, err := os.Lstat(filepath.Join(mine, n)); err == nil {
			t.Fatalf("%s link must go when inherit is off", n)
		}
	}
	for _, n := range []string{"known_marketplaces.json", "marketplaces"} {
		if _, err := os.Lstat(filepath.Join(mine, n)); err != nil {
			t.Fatalf("the account's own %s was removed", n)
		}
	}
	_ = r
}

// An account plugins dir that holds none of the resolving entries (only
// something stray) gets all four linked, so the plugins actually load.
func TestInheritCompletesAnAccountPluginsDirThatLacksTheEntries(t *testing.T) {
	s, data := newStore(t)
	dp := filepath.Join(s.defaultDir, "plugins")
	os.MkdirAll(filepath.Join(dp, "cache"), 0o700)
	os.MkdirAll(filepath.Join(dp, "marketplaces"), 0o700)
	for _, f := range []string{"installed_plugins.json", "known_marketplaces.json"} {
		os.WriteFile(filepath.Join(dp, f), []byte(`{}`), 0o600)
	}
	mine := filepath.Join(data, "accounts", "work", "claude", "plugins")
	os.MkdirAll(mine, 0o700)
	os.WriteFile(filepath.Join(mine, "stray.json"), []byte(`{}`), 0o600)
	if _, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"installed_plugins.json", "cache", "known_marketplaces.json", "marketplaces"} {
		if tgt, err := os.Readlink(filepath.Join(mine, n)); err != nil || tgt != filepath.Join(dp, n) {
			t.Fatalf("%s link = %q %v", n, tgt, err)
		}
	}
}

func TestInheritWithoutDefaultPluginsLinksNothing(t *testing.T) {
	s, _ := newStore(t)
	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude, InheritPersonal: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(r.ConfigDir, "plugins")); err == nil {
		t.Fatal("no default plugins: nothing to link")
	}
}
