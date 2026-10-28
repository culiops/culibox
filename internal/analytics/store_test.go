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
	if err := s.Apply("S1", "proj", "/t/a.jsonl", delta("2026-08-05", "m1", 100, 20, 5, 3, 2), 500, 500, 1, nil); err != nil {
		t.Fatal(err)
	}
	// A second batch for the same session/day/model adds to the first.
	if err := s.Apply("S1", "proj", "/t/a.jsonl", delta("2026-08-05", "m1", 50, 10, 1, 0, 1), 800, 800, 2, nil); err != nil {
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
	if _, _, ok, _, _ := s.State("/t/a.jsonl"); ok {
		t.Fatal("state should be absent before any Apply")
	}
	if err := s.Apply("S1", "proj", "/t/a.jsonl", newParsed(), 1234, 1200, 42, nil); err != nil {
		t.Fatal(err)
	}
	size, off, ok, _, err := s.State("/t/a.jsonl")
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
	if err := s.Apply("SAME", "agent-console", "/t/old.jsonl", delta("2026-08-05", "m1", 100, 0, 0, 0, 1), 1, 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply("SAME", "agent-box", "/t/new.jsonl", delta("2026-08-05", "m1", 200, 0, 0, 0, 1), 1, 1, 1, nil); err != nil {
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
	if err := s.Apply("S1", "p", "/t/a.jsonl", delta("2026-08-05", "m1", 200, 60, 30, 10, 4), 1, 1, 1, nil); err != nil {
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

// Ingest must refuse to run concurrently — two overlapping passes would read the
// same offset and additively double-count. Holding the lock simulates a pass in
// flight (e.g. the startup scan) while a "Re-scan" click calls Ingest.
func TestIngestRefusesConcurrent(t *testing.T) {
	svc := &Service{store: openTemp(t), roots: nil}
	svc.mu.Lock() // pretend a pass is running
	_, err := svc.Ingest()
	svc.mu.Unlock()
	if err != ErrIngestBusy {
		t.Fatalf("concurrent Ingest err = %v, want ErrIngestBusy", err)
	}
	// Once free, it runs normally.
	if _, err := svc.Ingest(); err != nil {
		t.Fatalf("Ingest after unlock: %v", err)
	}
}

// ByDayModel and Sessions attach a dollar cost derived from CostUSD.
func TestRowsCarryCost(t *testing.T) {
	s := openTemp(t)
	// 1,000,000 opus input tokens = $5 exactly.
	if err := s.Apply("S1", "p", "/t/a.jsonl", delta("2026-08-05", "claude-opus-5", 1_000_000, 0, 0, 0, 1), 1, 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	dm, err := s.ByDayModel("2026-08-01", "2026-08-31")
	if err != nil || len(dm) != 1 {
		t.Fatalf("ByDayModel err=%v rows=%d", err, len(dm))
	}
	if !approx(dm[0].Cost, 5) {
		t.Errorf("ByDayModel cost = %v, want 5", dm[0].Cost)
	}
	ss, err := s.Sessions("2026-08-01", "2026-08-31", 10)
	if err != nil || len(ss) != 1 {
		t.Fatalf("Sessions err=%v rows=%d", err, len(ss))
	}
	if !approx(ss[0].Cost, 5) {
		t.Errorf("Sessions cost = %v, want 5", ss[0].Cost)
	}
}

// ByModel aggregates across days/projects/sessions per model, counts distinct
// sessions, and attaches cost.
func TestByModel(t *testing.T) {
	s := openTemp(t)
	// Two sessions, same model, different days → 1 model row, 2 sessions.
	s.Apply("S1", "p", "/t/a.jsonl", delta("2026-08-05", "claude-opus-5", 1_000_000, 0, 0, 0, 2), 1, 1, 1, nil)
	s.Apply("S2", "p", "/t/b.jsonl", delta("2026-08-06", "claude-opus-5", 1_000_000, 0, 0, 0, 3), 1, 1, 1, nil)
	// A different model.
	s.Apply("S3", "p", "/t/c.jsonl", delta("2026-08-06", "claude-haiku-4-5", 1_000_000, 0, 0, 0, 1), 1, 1, 1, nil)
	rows, err := s.ByModel("2026-08-01", "2026-08-31")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("model rows = %d, want 2", len(rows))
	}
	var opus *ModelRow
	for i := range rows {
		if rows[i].Model == "claude-opus-5" {
			opus = &rows[i]
		}
	}
	if opus == nil {
		t.Fatal("no opus row")
	}
	if opus.Input != 2_000_000 || opus.Prompts != 5 || opus.Sessions != 2 || !approx(opus.Cost, 10) {
		t.Errorf("opus row wrong: %+v", opus)
	}
}

// ByModel's distinct-session count keys on (session_id, project): the same UUID
// under two project slugs is two sessions (the rename case the PK guards).
func TestByModelSessionsCountAcrossProjects(t *testing.T) {
	s := openTemp(t)
	s.Apply("SAME", "alpha", "/t/a.jsonl", delta("2026-08-05", "claude-opus-5", 1_000_000, 0, 0, 0, 1), 1, 1, 1, nil)
	s.Apply("SAME", "beta", "/t/b.jsonl", delta("2026-08-05", "claude-opus-5", 1_000_000, 0, 0, 0, 1), 1, 1, 1, nil)
	rows, err := s.ByModel("2026-08-01", "2026-08-31")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Sessions != 2 {
		t.Fatalf("same id under two projects → want 1 model row with 2 sessions, got %+v", rows)
	}
}

// ByProject returns per-(project,model) rows with cost, so the frontend can build
// project totals and the per-project model mix in one call.
func TestByProject(t *testing.T) {
	s := openTemp(t)
	s.Apply("S1", "alpha", "/t/a.jsonl", delta("2026-08-05", "claude-opus-5", 1_000_000, 0, 0, 0, 1), 1, 1, 1, nil)
	s.Apply("S2", "alpha", "/t/b.jsonl", delta("2026-08-05", "claude-sonnet-4-6", 1_000_000, 0, 0, 0, 1), 1, 1, 1, nil)
	s.Apply("S3", "beta", "/t/c.jsonl", delta("2026-08-05", "claude-opus-5", 1_000_000, 0, 0, 0, 1), 1, 1, 1, nil)
	rows, err := s.ByProject("2026-08-01", "2026-08-31")
	if err != nil {
		t.Fatal(err)
	}
	// alpha has two models, beta one → 3 rows.
	if len(rows) != 3 {
		t.Fatalf("project rows = %d, want 3", len(rows))
	}
	var alphaCost float64
	for _, r := range rows {
		if r.Project == "alpha" {
			alphaCost += r.Cost
		}
	}
	// alpha = opus $5 + sonnet $3 = $8.
	if !approx(alphaCost, 8) {
		t.Errorf("alpha total cost = %v, want 8", alphaCost)
	}
}

// SessionCosts folds every (day,model) row of a session into one per-session
// aggregate with a dollar cost — the complete set, NOT a day-ordered cap — so
// the Sessions view's count/histogram/top-by-cost stay correct on wide windows.
func TestSessionCosts(t *testing.T) {
	s := openTemp(t)
	// One session spanning two days and two models → one aggregate, cost summed,
	// model "mixed", day = the later one.
	s.Apply("S1", "p", "/t/a.jsonl", delta("2026-08-05", "claude-opus-5", 1_000_000, 0, 0, 0, 2), 1, 1, 1, nil)
	s.Apply("S1", "p", "/t/b.jsonl", delta("2026-08-06", "claude-sonnet-4-6", 1_000_000, 0, 0, 0, 3), 1, 1, 1, nil)
	// A cheaper single-model session.
	s.Apply("S2", "p", "/t/c.jsonl", delta("2026-08-05", "claude-haiku-4-5", 1_000_000, 0, 0, 0, 1), 1, 1, 1, nil)
	rows, err := s.SessionCosts("2026-08-01", "2026-08-31")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("session aggregates = %d, want 2", len(rows))
	}
	// Sorted by cost descending → S1 first ($5 opus + $3 sonnet = $8).
	if rows[0].SessionID != "S1" || !approx(rows[0].Cost, 8) || rows[0].Model != "mixed" ||
		rows[0].Day != "2026-08-06" || rows[0].Prompts != 5 {
		t.Errorf("S1 aggregate wrong: %+v", rows[0])
	}
	if rows[1].Model != "claude-haiku-4-5" {
		t.Errorf("single-model session should keep its model: %+v", rows[1])
	}
}

// The same session UUID under two project slugs is TWO sessions (the
// agent-console → agent-box rename produced exactly this) — the aggregate must
// keep them separate, matching the rollup PK.
func TestSessionCostsProjectDisambiguates(t *testing.T) {
	s := openTemp(t)
	s.Apply("SAME", "alpha", "/t/a.jsonl", delta("2026-08-05", "claude-opus-5", 1_000_000, 0, 0, 0, 1), 1, 1, 1, nil)
	s.Apply("SAME", "beta", "/t/b.jsonl", delta("2026-08-05", "claude-opus-5", 1_000_000, 0, 0, 0, 1), 1, 1, 1, nil)
	rows, err := s.SessionCosts("2026-08-01", "2026-08-31")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("same session id under two projects = %d rows, want 2", len(rows))
	}
}

func TestToolsAggregate(t *testing.T) {
	s := openTemp(t)
	p := newParsed()
	p.Attrs[AttrKey{Day: "2026-08-05", Kind: "skill", Name: "brainstorming"}] = 3
	p.Attrs[AttrKey{Day: "2026-08-05", Kind: "mcp_server", Name: "playwright"}] = 1
	if err := s.Apply("S1", "p", "/t/a.jsonl", p, 1, 1, 1, nil); err != nil {
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
