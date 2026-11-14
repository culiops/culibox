//go:build upgrade

package upgradetest

import (
	"strings"
	"testing"
)

func entryOf(data string) *Entry { return &Entry{Type: "file", Data: []byte(data)} }

func TestSessionsRuleAllowsOnlyRemovals(t *testing.T) {
	r := sessionsRule(nil)
	before := entryOf(`{"a":{"x":1},"b":{}}`)
	if err := r.Check(before, entryOf(`{"a":{"x":1}}`)); err != nil {
		t.Fatalf("a removal was refused: %v", err)
	}
	if err := r.Check(before, entryOf(`{}`)); err != nil {
		t.Fatalf("dropping every entry was refused: %v", err)
	}
	if err := r.Check(before, entryOf(`{"a":{"x":2}}`)); err == nil || !strings.Contains(err.Error(), `"a"`) {
		t.Fatalf("a changed entry was allowed: %v", err)
	}
	if err := r.Check(before, entryOf(`{"a":{"x":1},"c":{}}`)); err == nil || !strings.Contains(err.Error(), `"c"`) {
		t.Fatalf("a new entry was allowed: %v", err)
	}
	if err := r.Check(before, entryOf(`not json`)); err == nil {
		t.Fatal("an unparseable sessions.json was allowed")
	}
}

func TestSessionsRuleRewrite(t *testing.T) {
	r := sessionsRule(func(s string) string { return strings.ReplaceAll(s, "/L/", "/N/") })
	before := entryOf(`{"a":{"config_dir":"/L/accounts/x"}}`)
	if err := r.Check(before, entryOf(`{"a":{"config_dir":"/N/accounts/x"}}`)); err != nil {
		t.Fatalf("the root rewrite was refused: %v", err)
	}
	if err := r.Check(before, entryOf(`{"a":{"config_dir":"/Z/accounts/x"}}`)); err == nil {
		t.Fatal("a rewrite to another root was allowed")
	}
}

func TestProjectToKeys(t *testing.T) {
	before := `[{"name":"p","bin":"/b"}]`
	if err := sameProjected(before, `[{"name":"p","bin":"/b","rev":"r1"}]`, nil); err != nil {
		t.Fatalf("a key only vN has was not dropped: %v", err)
	}
	if err := sameProjected(before, `[{"name":"p","bin":"/c"}]`, nil); err == nil || !strings.Contains(err.Error(), "bin") {
		t.Fatalf("a changed value was not reported: %v", err)
	}
	if err := sameProjected(before, `[]`, nil); err == nil {
		t.Fatal("a missing profile was not reported")
	}
	if err := sameProjected(`{"x":"/L/a"}`, `{"x":"/N/a","y":1}`, func(s string) string { return strings.ReplaceAll(s, "/L/", "/N/") }); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
}

func TestContainsAuditRows(t *testing.T) {
	before := []AuditRow{{TS: 1, Kind: "store", Outcome: "init"}, {TS: 2, Kind: "store", Outcome: "value_set"}}
	if missing := missingRows(before, append([]AuditRow{{TS: 3, Kind: "store", Outcome: "x"}}, before...)); len(missing) != 0 {
		t.Fatalf("missing %v", missing)
	}
	if missing := missingRows(before, before[:1]); len(missing) != 1 || missing[0].Outcome != "value_set" {
		t.Fatalf("missing %v", missing)
	}
}

func TestTokensRuleAllowsOnlyLastUsed(t *testing.T) {
	r := tokensRule()
	before := entryOf(`[{"id":"a","name":"upg","created_at":1,"hash":"h"}]`)
	if err := r.Check(before, entryOf(`[{"id":"a","name":"upg","created_at":1,"last_used_at":5,"hash":"h"}]`)); err != nil {
		t.Fatalf("a last_used_at bump was refused: %v", err)
	}
	if err := r.Check(before, entryOf(`[{"id":"a","name":"upg","created_at":1,"hash":"h2"}]`)); err == nil {
		t.Fatal("a changed hash was allowed")
	}
	if err := r.Check(before, entryOf(`[]`)); err == nil {
		t.Fatal("a dropped token was allowed")
	}
	used := entryOf(`[{"id":"a","name":"upg","created_at":1,"last_used_at":5,"hash":"h"}]`)
	if err := r.Check(used, entryOf(`[{"id":"a","name":"upg","created_at":1,"last_used_at":4,"hash":"h"}]`)); err == nil {
		t.Fatal("last_used_at going back was allowed")
	}
}

func TestMoveMapper(t *testing.T) {
	m := MoveMapper(".local/share/agent-box", ".local/share/culibox")
	for in, want := range map[string]string{
		".local/share/agent-box/x/y": ".local/share/culibox/x/y",
		".local/share/agent-box/x":   ".local/share/culibox/x",
		".local/share/agent-box":     ".local/share/agent-box", // ignored: CheckMoved asserts it
		".local/share/agent-boxer/x": ".local/share/agent-boxer/x",
		".claude/projects/a/b.jsonl": ".claude/projects/a/b.jsonl",
		".local/share/culibox/stray": ".local/share/culibox/stray",
	} {
		if got := m(in); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
}

func TestSettingsRuleAllowsOnlyRootRewrite(t *testing.T) {
	r := settingsRule(moveRewrite)
	before := entryOf(`{"statusLine":{"type":"command","command":"/h/.local/share/agent-box/bin/s.sh"},"k":1}`)
	if err := r.Check(before, entryOf(`{"statusLine":{"type":"command","command":"/h/.local/share/culibox/bin/s.sh"},"k":1}`)); err != nil {
		t.Fatalf("the root rewrite was refused: %v", err)
	}
	if err := r.Check(before, entryOf(`{"statusLine":{"type":"command","command":"/h/.local/share/culibox/bin/s.sh"},"k":2}`)); err == nil {
		t.Fatal("another change was allowed")
	}
	if err := r.Check(before, entryOf(`{"statusLine":{"type":"command","command":"/z/bin/s.sh"},"k":1}`)); err == nil {
		t.Fatal("a rewrite to another root was allowed")
	}
}

func TestAllowRulesRefuseUnknownRootChanges(t *testing.T) {
	before := Snapshot{"r/profiles.json": *entryOf("[]"), "r/credentials.json": *entryOf("{}")}
	after := Snapshot{
		"r/profiles.json":    {Type: "file", Data: []byte("[1]"), SHA256: "x"},
		"r/unexpected.json":  *entryOf("{}"),
		"r/last-version":     *entryOf("v2 abc\n"),
		"r/backups":          {Type: "dir"},
		"r/backups/x/a.json": *entryOf("{}"),
	}
	got := strings.Join(Diff(before, after, DiffOptions{Rules: AllowRules("r", nil)}), "\n")
	for _, want := range []string{"r/profiles.json: changed", "r/unexpected.json: new", "r/credentials.json: vanished"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
	for _, allowed := range []string{"last-version", "backups"} {
		if strings.Contains(got, allowed) {
			t.Errorf("%s is part A's and checked by backupProblems, not here:\n%s", allowed, got)
		}
	}
}

func TestSessionIDsRuleAllowsOnlyGrowth(t *testing.T) {
	r := sessionIDsRule()
	before := entryOf(`{"upg-host":1}`)
	if err := r.Check(before, entryOf(`{"upg-host":1,"upg-secret":1}`)); err != nil {
		t.Fatalf("a counter for a session the test spawned on vN was refused: %v", err)
	}
	if err := r.Check(before, entryOf(`{"upg-host":2}`)); err != nil {
		t.Fatalf("a counter moving forward was refused: %v", err)
	}
	if err := r.Check(before, entryOf(`{"upg-host":1}`)); err != nil {
		t.Fatalf("an unchanged file was refused: %v", err)
	}
	if err := r.Check(before, entryOf(`{"upg-secret":1}`)); err == nil {
		t.Fatal("a dropped counter was allowed (ids could be reissued)")
	}
	if err := r.Check(entryOf(`{"upg-host":3}`), entryOf(`{"upg-host":2}`)); err == nil {
		t.Fatal("a counter going back was allowed (ids could be reissued)")
	}
	if err := r.Check(before, entryOf(`{"upg-host":"1"}`)); err == nil {
		t.Fatal("a counter that is not a number was allowed")
	}
	moded := entryOf(`{"upg-host":1}`)
	moded.Mode = 0o644
	if err := r.Check(before, moded); err == nil {
		t.Fatal("a mode change was allowed")
	}
}
