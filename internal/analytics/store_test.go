package analytics

import (
	"path/filepath"
	"testing"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "analytics.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func delta(day, model string, in, out, cr, cc, prompts int64) *Parsed {
	p := newParsed()
	p.Tokens[TokKey{Day: day, Model: model}] = &Tok{Input: in, Output: out, CacheRead: cr, CacheCreation: cc, Prompts: prompts}
	return p
}

func TestApplyIsAdditive(t *testing.T) {
	s := openTemp(t)
	if err := s.Apply("S1", "proj", "/t/a.jsonl", delta("2026-08-05", "m1", 100, 20, 5, 3, 2), 500, 500, 1); err != nil {
		t.Fatal(err)
	}
	// A second batch for the same session/day/model adds to the first.
	if err := s.Apply("S1", "proj", "/t/a.jsonl", delta("2026-08-05", "m1", 50, 10, 1, 0, 1), 800, 800, 2); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ByDayModel("2026-08-01", "2026-08-31")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.Input != 150 || r.Output != 30 || r.CacheRead != 6 || r.CacheCreation != 3 || r.Prompts != 3 {
		t.Errorf("additive apply wrong: %+v", r)
	}
}

func TestStateRoundTrip(t *testing.T) {
	s := openTemp(t)
	if _, _, ok, _ := s.State("/t/a.jsonl"); ok {
		t.Fatal("state should be absent before any Apply")
	}
	if err := s.Apply("S1", "proj", "/t/a.jsonl", newParsed(), 1234, 1200, 42); err != nil {
		t.Fatal(err)
	}
	size, off, ok, err := s.State("/t/a.jsonl")
	if err != nil || !ok {
		t.Fatalf("state missing after Apply: ok=%v err=%v", ok, err)
	}
	if size != 1234 || off != 1200 {
		t.Errorf("state = size %d off %d, want 1234/1200", size, off)
	}
}

// Same session UUID under two different project slugs must NOT collide (the
// agent-console → agent-box rename produced exactly this).
func TestProjectDisambiguatesSameSession(t *testing.T) {
	s := openTemp(t)
	if err := s.Apply("SAME", "agent-console", "/t/old.jsonl", delta("2026-08-05", "m1", 100, 0, 0, 0, 1), 1, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply("SAME", "agent-box", "/t/new.jsonl", delta("2026-08-05", "m1", 200, 0, 0, 0, 1), 1, 1, 1); err != nil {
		t.Fatal(err)
	}
	sessions, err := s.Sessions("2026-08-01", "2026-08-31", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("rows = %d, want 2 (one per project)", len(sessions))
	}
}

func TestTokensPerPrompt(t *testing.T) {
	s := openTemp(t)
	// 300 total tokens (100+20+... let's make it clean), 4 prompts → 75/prompt.
	if err := s.Apply("S1", "p", "/t/a.jsonl", delta("2026-08-05", "m1", 200, 60, 30, 10, 4), 1, 1, 1); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Sessions("2026-08-01", "2026-08-31", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TokensPerPrompt != 75 {
		t.Fatalf("tokens/prompt = %v, want 75 (rows=%d)", rows, len(rows))
	}
}

func TestToolsAggregate(t *testing.T) {
	s := openTemp(t)
	p := newParsed()
	p.Attrs[AttrKey{Day: "2026-08-05", Kind: "skill", Name: "brainstorming"}] = 3
	p.Attrs[AttrKey{Day: "2026-08-05", Kind: "mcp_server", Name: "playwright"}] = 1
	if err := s.Apply("S1", "p", "/t/a.jsonl", p, 1, 1, 1); err != nil {
		t.Fatal(err)
	}
	tools, err := s.Tools("2026-08-01", "2026-08-31", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name != "brainstorming" || tools[0].Records != 3 {
		t.Fatalf("tools wrong: %+v", tools)
	}
}
