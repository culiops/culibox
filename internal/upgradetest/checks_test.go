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
