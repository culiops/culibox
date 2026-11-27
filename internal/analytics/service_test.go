package analytics

import (
	"path/filepath"
	"testing"
	"time"
)

// Roots are glob patterns re-expanded on every Ingest, so an account (or box)
// created after the service started is picked up without a restart.
func TestIngestRegloadsRootPatterns(t *testing.T) {
	base := t.TempDir()
	svc, err := New(filepath.Join(t.TempDir(), "a.db"),
		[]string{filepath.Join(base, "accounts", "*", "claude", "projects")}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if _, err := svc.Ingest(); err != nil { // nothing exists yet
		t.Fatal(err)
	}
	rows, _ := svc.ByDayModel("2026-08-01", "2026-08-31")
	if len(rows) != 0 {
		t.Fatalf("rows before any account exists: %+v", rows)
	}

	// An account created after the service started:
	writeFile(t, filepath.Join(base, "accounts", "work", "claude", "projects", "-p", "S1.jsonl"), line1+line2)
	st, err := svc.Ingest()
	if err != nil {
		t.Fatal(err)
	}
	if st.Files != 1 {
		t.Fatalf("second pass files=%d want 1", st.Files)
	}
	rows, _ = svc.ByDayModel("2026-08-01", "2026-08-31")
	if len(rows) != 1 || rows[0].Input != 100 || rows[0].Output != 20 {
		t.Fatalf("account transcript not ingested: %+v", rows)
	}
}

// A literal root is a pattern that matches itself, so configured roots keep working.
func TestIngestLiteralRootStillWorks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "-p", "S1.jsonl"), line1+line2)
	svc, err := New(filepath.Join(t.TempDir(), "a.db"), []string{root}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if st, err := svc.Ingest(); err != nil || st.Files != 1 {
		t.Fatalf("literal root: st=%+v err=%v", st, err)
	}
}

func costDelta(day, model string, cost float64) *Parsed {
	p := newParsed()
	p.Tokens[TokKey{Day: day, Model: model}] = &Tok{Input: 1, Cost: cost}
	return p
}

// A conversation's cost is the sum over every day it ran, not a day window.
func TestCostMapAllDays(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "a.db"), nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if got := svc.CostOf([]string{"A"}); got != 0 {
		t.Fatalf("CostOf before any rows = %v, want 0", got)
	}
	for i, d := range []struct {
		sid, day string
		cost     float64
	}{{"A", "2026-08-01", 1.0}, {"A", "2026-08-09", 2.0}, {"B", "2026-08-09", 5.0}} {
		if err := svc.store.Apply(d.sid, "proj", "/t/"+d.sid+d.day+".jsonl", RootInfo{}, costDelta(d.day, "m1", d.cost), 1, 1, int64(i), nil); err != nil {
			t.Fatal(err)
		}
	}
	// Apply alone does not move the cached map: it is rebuilt per ingest pass.
	if got := svc.CostOf([]string{"A"}); got != 0 {
		t.Fatalf("CostOf before refresh = %v, want 0 (map is rebuilt by Ingest, not by reads)", got)
	}
	svc.refreshCosts()
	if got := svc.CostOf([]string{"A"}); got != 3.0 {
		t.Errorf("CostOf([A]) = %v, want 3.0", got)
	}
	if got := svc.CostOf([]string{"A", "B"}); got != 8.0 {
		t.Errorf("CostOf([A B]) = %v, want 8.0", got)
	}
	if got := svc.CostOf([]string{"unknown"}); got != 0 {
		t.Errorf("CostOf([unknown]) = %v, want 0", got)
	}
}

// Ingest rebuilds the map, and Start-time reads see rows that were already in
// the DB: a daemon restart must not show $0.00 until the next tick.
func TestIngestRebuildsCostMap(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "a.db")
	// claude-sonnet-4-5 is priced, so the ingested usage has a non-zero cost.
	tr := `{"sessionId":"S1","timestamp":"2026-08-05T10:00:05Z","message":{"role":"assistant","model":"claude-sonnet-4-5","usage":{"input_tokens":1000000,"output_tokens":0}}}` + "\n"
	writeFile(t, filepath.Join(root, "-p", "S1.jsonl"), tr)
	svc, err := New(dbPath, []string{root}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Ingest(); err != nil {
		t.Fatal(err)
	}
	want := svc.CostOf([]string{"S1"})
	if want <= 0 {
		t.Fatalf("CostOf after Ingest = %v, want > 0", want)
	}
	svc.Close()

	// Reopen the same DB: nothing new to ingest, yet the map must be built.
	svc2, err := New(dbPath, []string{root}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer svc2.Close()
	if got := svc2.CostOf([]string{"S1"}); got != want {
		t.Errorf("CostOf on a reopened DB = %v, want %v", got, want)
	}
}

// A failed rebuild keeps the previous map rather than wiping every cost to 0.
func TestRefreshCostsKeepsMapOnError(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "a.db"), nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.store.Apply("A", "proj", "/t/a.jsonl", RootInfo{}, costDelta("2026-08-01", "m1", 4.0), 1, 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	svc.refreshCosts()
	svc.Close() // every later query errors
	svc.refreshCosts()
	if got := svc.CostOf([]string{"A"}); got != 4.0 {
		t.Errorf("CostOf after a failed refresh = %v, want the previous 4.0", got)
	}
}

// The pass-local state cache must follow Apply: the same transcript reached
// twice in one pass (overlapping roots) is read from its new offset the second
// time, never counted again.
func TestIngestSamePathTwiceInOnePassCountsOnce(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "-p", "S1.jsonl"), line1+line2)
	svc, err := New(filepath.Join(t.TempDir(), "a.db"), []string{root, root}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	for pass := 0; pass < 2; pass++ {
		if _, err := svc.Ingest(); err != nil {
			t.Fatal(err)
		}
		rows, _ := svc.ByDayModel("2026-08-01", "2026-08-31")
		if len(rows) != 1 || rows[0].Input != 100 || rows[0].Output != 20 {
			t.Fatalf("pass %d: rows %+v, want input 100 output 20 counted once", pass, rows)
		}
	}
}
