package analytics

import (
	"path/filepath"
	"slices"
	"sort"
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

// resolvedDim is the resolved columns of one session_dim row.
type resolvedDim struct {
	workspace, profile, via, viaPath string
	unmatched                        int
}

func readResolved(t *testing.T, svc *Service, sid, project string) resolvedDim {
	t.Helper()
	var d resolvedDim
	err := svc.store.db.QueryRow(`SELECT workspace,unmatched,profile,via,via_path FROM session_dim WHERE session_id=? AND project=?`,
		sid, project).Scan(&d.workspace, &d.unmatched, &d.profile, &d.via, &d.viaPath)
	if err != nil {
		t.Fatalf("read dim %s/%s: %v", sid, project, err)
	}
	return d
}

// boxTranscript writes a one-line box transcript for session sid whose first cwd
// is cwd, under box b of the service's box-state dir.
func boxTranscript(t *testing.T, base, b, project, sid, cwd string) {
	t.Helper()
	line := `{"sessionId":"` + sid + `","cwd":"` + cwd + `","timestamp":"2026-08-05T10:00:00Z","message":{"role":"assistant","model":"m1","usage":{"input_tokens":10,"output_tokens":2}}}` + "\n"
	writeFile(t, filepath.Join(base, "boxstate", b, ".claude", "projects", project, sid+".jsonl"), line)
}

// A session is filed under its repo when first seen. Editing the profile later
// must not move it; a session first seen after the edit follows the new config.
func TestResolvePendingIsSnapshot(t *testing.T) {
	base := t.TempDir()
	svc := boxSvc(t, base)
	cloneOf := "/r1"
	svc.SetResolveEnv(func() ResolveEnv {
		return ResolveEnv{Profiles: []ProfileRef{{Name: "p", DefaultCwd: "/clones/c", CloneOf: cloneOf}}}
	})

	boxTranscript(t, base, "b1", "-clones-c", "S1", "/clones/c")
	if _, err := svc.Ingest(); err != nil {
		t.Fatal(err)
	}
	if d := readResolved(t, svc, "S1", "-clones-c"); d.workspace != "/r1" || d.via != "clone" || d.profile != "p" || d.unmatched != 0 || d.viaPath != "/clones/c" {
		t.Fatalf("S1 first pass = %+v, want /r1 via clone, profile p", d)
	}

	cloneOf = "/r2"
	boxTranscript(t, base, "b1", "-clones-c", "S2", "/clones/c")
	if _, err := svc.Ingest(); err != nil {
		t.Fatal(err)
	}
	if d := readResolved(t, svc, "S1", "-clones-c"); d.workspace != "/r1" {
		t.Fatalf("S1 after the profile edit = %+v, want it to stay /r1", d)
	}
	if d := readResolved(t, svc, "S2", "-clones-c"); d.workspace != "/r2" {
		t.Fatalf("S2 (first seen after the edit) = %+v, want /r2", d)
	}
}

// A legacy session no folder explained stays pending, and is resolved on the
// first pass after a profile makes its slug known.
func TestResolvePendingRetriesUnmatched(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "a.db"), nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if _, err := svc.store.db.Exec(`INSERT INTO session_dim(session_id,project,runtime,parsed) VALUES('L1','-work-app','box',0)`); err != nil {
		t.Fatal(err)
	}
	env := ResolveEnv{}
	svc.SetResolveEnv(func() ResolveEnv { return env })

	if _, err := svc.Ingest(); err != nil {
		t.Fatal(err)
	}
	if d := readResolved(t, svc, "L1", "-work-app"); d.unmatched != 1 || d.workspace != "-work-app" || d.via != "legacy" {
		t.Fatalf("empty env = %+v, want unmatched under its slug", d)
	}

	env = ResolveEnv{Profiles: []ProfileRef{{Name: "app", DefaultCwd: "/work/app"}}}
	if _, err := svc.Ingest(); err != nil {
		t.Fatal(err)
	}
	if d := readResolved(t, svc, "L1", "-work-app"); d.unmatched != 0 || d.workspace != "/work/app" || d.via != "legacy" {
		t.Fatalf("after the profile = %+v, want /work/app, matched", d)
	}
}

// With no env func set the pass still resolves every pending row, from a zero env.
func TestResolveNoEnvIsHarmless(t *testing.T) {
	base := t.TempDir()
	svc := boxSvc(t, base)
	boxTranscript(t, base, "b1", "-srv-x", "S1", "/srv/x")
	if _, err := svc.store.db.Exec(`INSERT INTO session_dim(session_id,project,runtime,parsed) VALUES('L1','-nowhere','box',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Ingest(); err != nil {
		t.Fatal(err)
	}
	if d := readResolved(t, svc, "S1", "-srv-x"); d.workspace != "/srv/x" || d.via != "inplace" || d.unmatched != 0 {
		t.Fatalf("S1 = %+v, want /srv/x in place", d)
	}
	if d := readResolved(t, svc, "L1", "-nowhere"); d.unmatched != 1 || d.workspace != "-nowhere" {
		t.Fatalf("L1 = %+v, want unmatched under its slug", d)
	}
}

// KnownPaths offers the workspaces of matched sessions and the cwds of parsed
// ones, never the slug an unmatched row carries as its workspace.
func TestKnownPaths(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "a.db"), nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	for _, q := range []string{
		`INSERT INTO session_dim(session_id,project,cwd,workspace,unmatched,parsed) VALUES('A','p','/a/wt','/a',0,1)`,
		`INSERT INTO session_dim(session_id,project,cwd,workspace,unmatched,parsed) VALUES('B','p','','/b',0,0)`,
		`INSERT INTO session_dim(session_id,project,cwd,workspace,unmatched,parsed) VALUES('C','p','','-slug',1,0)`,
		`INSERT INTO session_dim(session_id,project,cwd,workspace,unmatched,parsed) VALUES('D','p','','',0,0)`,
		`INSERT INTO session_dim(session_id,project,cwd,workspace,unmatched,parsed) VALUES('E','p','/a','/a',0,1)`,
	} {
		if _, err := svc.store.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	got, err := svc.store.KnownPaths()
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if want := []string{"/a", "/a/wt", "/b"}; !slices.Equal(got, want) {
		t.Fatalf("KnownPaths = %v, want %v", got, want)
	}
}
