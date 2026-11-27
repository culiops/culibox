package analytics

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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
	// Cost is what the parser would have stored: the bundle priced as one message.
	cost := bundleCost(model, in, out, cr, cc)
	p.Tokens[TokKey{Day: day, Model: model}] = &Tok{Input: in, Output: out, CacheRead: cr, CacheCreation: cc, Prompts: prompts, Cost: cost}
	return p
}

func TestApplyIsAdditive(t *testing.T) {
	s := openTemp(t)
	if err := s.Apply("S1", "proj", "/t/a.jsonl", RootInfo{}, delta("2026-08-05", "m1", 100, 20, 5, 3, 2), true, 500, 500, 1, nil); err != nil {
		t.Fatal(err)
	}
	// A second batch for the same session/day/model adds to the first.
	if err := s.Apply("S1", "proj", "/t/a.jsonl", RootInfo{}, delta("2026-08-05", "m1", 50, 10, 1, 0, 1), false, 800, 800, 2, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ByDayModel("2026-08-01", "2026-08-31", Where{})
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
	if _, _, ok, _, _, _ := s.State("/t/a.jsonl"); ok {
		t.Fatal("state should be absent before any Apply")
	}
	if err := s.Apply("S1", "proj", "/t/a.jsonl", RootInfo{}, newParsed(), true, 1234, 1200, 42, nil); err != nil {
		t.Fatal(err)
	}
	size, off, ok, _, _, err := s.State("/t/a.jsonl")
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
	if err := s.Apply("SAME", "agent-console", "/t/old.jsonl", RootInfo{}, delta("2026-08-05", "m1", 100, 0, 0, 0, 1), true, 1, 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply("SAME", "agent-box", "/t/new.jsonl", RootInfo{}, delta("2026-08-05", "m1", 200, 0, 0, 0, 1), true, 1, 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	sessions, err := s.Sessions("2026-08-01", "2026-08-31", 10, Where{})
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
	if err := s.Apply("S1", "p", "/t/a.jsonl", RootInfo{}, delta("2026-08-05", "m1", 200, 60, 30, 10, 4), true, 1, 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Sessions("2026-08-01", "2026-08-31", 10, Where{})
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

// ByDayModel and Sessions attach a dollar cost read from the stored cost_usd column.
func TestRowsCarryCost(t *testing.T) {
	s := openTemp(t)
	// 1,000,000 opus input tokens = $5 exactly.
	if err := s.Apply("S1", "p", "/t/a.jsonl", RootInfo{}, delta("2026-08-05", "claude-opus-5", 1_000_000, 0, 0, 0, 1), true, 1, 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	dm, err := s.ByDayModel("2026-08-01", "2026-08-31", Where{})
	if err != nil || len(dm) != 1 {
		t.Fatalf("ByDayModel err=%v rows=%d", err, len(dm))
	}
	if !approx(dm[0].Cost, 5) {
		t.Errorf("ByDayModel cost = %v, want 5", dm[0].Cost)
	}
	ss, err := s.Sessions("2026-08-01", "2026-08-31", 10, Where{})
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
	s.Apply("S1", "p", "/t/a.jsonl", RootInfo{}, delta("2026-08-05", "claude-opus-5", 1_000_000, 0, 0, 0, 2), true, 1, 1, 1, nil)
	s.Apply("S2", "p", "/t/b.jsonl", RootInfo{}, delta("2026-08-06", "claude-opus-5", 1_000_000, 0, 0, 0, 3), true, 1, 1, 1, nil)
	// A different model.
	s.Apply("S3", "p", "/t/c.jsonl", RootInfo{}, delta("2026-08-06", "claude-haiku-4-5", 1_000_000, 0, 0, 0, 1), true, 1, 1, 1, nil)
	rows, err := s.ByModel("2026-08-01", "2026-08-31", Where{})
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
	s.Apply("SAME", "alpha", "/t/a.jsonl", RootInfo{}, delta("2026-08-05", "claude-opus-5", 1_000_000, 0, 0, 0, 1), true, 1, 1, 1, nil)
	s.Apply("SAME", "beta", "/t/b.jsonl", RootInfo{}, delta("2026-08-05", "claude-opus-5", 1_000_000, 0, 0, 0, 1), true, 1, 1, 1, nil)
	rows, err := s.ByModel("2026-08-01", "2026-08-31", Where{})
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
	s.Apply("S1", "alpha", "/t/a.jsonl", RootInfo{}, delta("2026-08-05", "claude-opus-5", 1_000_000, 0, 0, 0, 1), true, 1, 1, 1, nil)
	s.Apply("S2", "alpha", "/t/b.jsonl", RootInfo{}, delta("2026-08-05", "claude-sonnet-4-6", 1_000_000, 0, 0, 0, 1), true, 1, 1, 1, nil)
	s.Apply("S3", "beta", "/t/c.jsonl", RootInfo{}, delta("2026-08-05", "claude-opus-5", 1_000_000, 0, 0, 0, 1), true, 1, 1, 1, nil)
	rows, err := s.ByProject("2026-08-01", "2026-08-31", Where{})
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
	s.Apply("S1", "p", "/t/a.jsonl", RootInfo{}, delta("2026-08-05", "claude-opus-5", 1_000_000, 0, 0, 0, 2), true, 1, 1, 1, nil)
	s.Apply("S1", "p", "/t/b.jsonl", RootInfo{}, delta("2026-08-06", "claude-sonnet-4-6", 1_000_000, 0, 0, 0, 3), true, 1, 1, 1, nil)
	// A cheaper single-model session.
	s.Apply("S2", "p", "/t/c.jsonl", RootInfo{}, delta("2026-08-05", "claude-haiku-4-5", 1_000_000, 0, 0, 0, 1), true, 1, 1, 1, nil)
	rows, err := s.SessionCosts("2026-08-01", "2026-08-31", Where{})
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
	s.Apply("SAME", "alpha", "/t/a.jsonl", RootInfo{}, delta("2026-08-05", "claude-opus-5", 1_000_000, 0, 0, 0, 1), true, 1, 1, 1, nil)
	s.Apply("SAME", "beta", "/t/b.jsonl", RootInfo{}, delta("2026-08-05", "claude-opus-5", 1_000_000, 0, 0, 0, 1), true, 1, 1, 1, nil)
	rows, err := s.SessionCosts("2026-08-01", "2026-08-31", Where{})
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
	if err := s.Apply("S1", "p", "/t/a.jsonl", RootInfo{}, p, true, 1, 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	tools, err := s.Tools("2026-08-01", "2026-08-31", 10, Where{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name != "brainstorming" || tools[0].Records != 3 {
		t.Fatalf("tools wrong: %+v", tools)
	}
}

// v0Schema is the analytics schema before the dedupe fix: no seen_ids, no
// cache_creation_1h, no cost_usd, user_version 0.
const v0Schema = `
CREATE TABLE ingest_state (path TEXT PRIMARY KEY, size INTEGER NOT NULL, read_offset INTEGER NOT NULL, mtime INTEGER NOT NULL);
CREATE TABLE session_rollup (
  day TEXT NOT NULL, session_id TEXT NOT NULL, project TEXT NOT NULL, model TEXT NOT NULL,
  input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read INTEGER NOT NULL DEFAULT 0, cache_creation INTEGER NOT NULL DEFAULT 0,
  prompts INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (day, session_id, project, model));
CREATE TABLE attribution_rollup (
  day TEXT NOT NULL, session_id TEXT NOT NULL, project TEXT NOT NULL, kind TEXT NOT NULL, name TEXT NOT NULL,
  records INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (day, session_id, project, kind, name));
INSERT INTO ingest_state VALUES ('/t/a.jsonl', 10, 10, 1);
INSERT INTO session_rollup(day,session_id,project,model,input_tokens) VALUES ('2026-08-05','S1','p','m1',777);
INSERT INTO attribution_rollup VALUES ('2026-08-05','S1','p','skill','x',3);
`

// makeV0DB builds an old-schema DB in WAL mode. The returned connection is left
// OPEN so the data sits in the -wal file, like a live PROD DB.
func makeV0DB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(v0Schema); err != nil {
		t.Fatal(err)
	}
	return db
}

func countRows(t *testing.T, path, table string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s in %s: %v", table, path, err)
	}
	return n
}

func userVersion(t *testing.T, s *Store) int {
	t.Helper()
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// An old (v0) DB is backed up through SQLite, wiped, and rebuilt empty exactly
// once; a second boot neither re-wipes nor touches the backup.
func TestOpenMigratesOnceAndBacksUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "analytics.db")
	old := makeV0DB(t, path)
	defer old.Close()
	bak := path + ".pre-dedupe.bak"

	s, err := Open(path) // old connection still open: its rows live in the WAL
	if err != nil {
		t.Fatal(err)
	}
	if v := userVersion(t, s); v != 3 {
		t.Errorf("user_version = %d, want 3", v)
	}
	for _, tbl := range []string{"session_rollup", "ingest_state", "attribution_rollup"} {
		if n := countRows(t, path, tbl); n != 0 {
			t.Errorf("%s has %d rows after migration, want 0", tbl, n)
		}
	}
	if n := countRows(t, bak, "session_rollup"); n != 1 {
		t.Errorf("backup session_rollup rows = %d, want 1 (WAL content must be in the backup)", n)
	}
	if n := countRows(t, bak, "ingest_state"); n != 1 {
		t.Errorf("backup ingest_state rows = %d, want 1", n)
	}
	if _, err := os.Stat(bak + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp backup file left behind: %v", err)
	}
	fi, err := os.Stat(bak)
	if err != nil {
		t.Fatal(err)
	}
	before := fi.ModTime()

	// New data after the rebuild must survive a second boot.
	if err := s.Apply("S2", "p", "/t/b.jsonl", RootInfo{}, delta("2026-08-06", "claude-opus-5", 5, 0, 0, 0, 1), true, 1, 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	s.Close()
	time.Sleep(20 * time.Millisecond)
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if n := countRows(t, path, "session_rollup"); n != 1 {
		t.Errorf("rows after second boot = %d, want 1 (no re-wipe)", n)
	}
	fi2, err := os.Stat(bak)
	if err != nil {
		t.Fatal(err)
	}
	if !fi2.ModTime().Equal(before) {
		t.Errorf("backup was rewritten: mtime %v -> %v", before, fi2.ModTime())
	}
	if n := countRows(t, bak, "session_rollup"); n != 1 {
		t.Errorf("backup rows after second boot = %d, want 1", n)
	}
}

// A DB that already has a backup (e.g. a crash after the backup but before the
// wipe committed) is wiped without the backup being overwritten.
func TestOpenKeepsExistingBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "analytics.db")
	old := makeV0DB(t, path)
	defer old.Close()
	bak := path + ".pre-dedupe.bak"
	if err := os.WriteFile(bak, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if b, _ := os.ReadFile(bak); string(b) != "precious" {
		t.Errorf("existing backup was overwritten: %q", b)
	}
	if n := countRows(t, path, "session_rollup"); n != 0 {
		t.Errorf("rows = %d, want 0 after migration", n)
	}
}

// A brand-new DB gets the current schema and no backup.
func TestOpenFreshDBMakesNoBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "analytics.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v := userVersion(t, s); v != 3 {
		t.Errorf("user_version = %d, want 3", v)
	}
	if _, err := os.Stat(path + ".pre-dedupe.bak"); !os.IsNotExist(err) {
		t.Errorf("fresh DB made a backup: %v", err)
	}
}

// If the backup cannot be made, Open fails and the old data is untouched.
func TestOpenBackupFailureKeepsOldData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "analytics.db")
	old := makeV0DB(t, path)
	defer old.Close()
	// A directory squatting on the backup path makes a valid backup impossible.
	if err := os.Mkdir(path+".pre-dedupe.bak", 0o700); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(path); err == nil {
		s.Close()
		t.Fatal("Open succeeded without a usable backup")
	}
	if n := countRows(t, path, "session_rollup"); n != 1 {
		t.Errorf("old rows = %d, want 1: data must survive a failed backup", n)
	}
}

// The backup target is a bound parameter, not spliced into the SQL text; a quote
// in the path must not break the statement (or fail the backup).
func TestOpenBackupPathWithQuote(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "it's a dir")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "an'alytics.db")
	old := makeV0DB(t, path)
	defer old.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n := countRows(t, path+".pre-dedupe.bak", "session_rollup"); n != 1 {
		t.Errorf("backup rows = %d, want 1", n)
	}
}

// schemaV2 is the analytics schema at user_version 2 (before session_dim,
// driver_rollup and ingest_state.backfilled), copied verbatim so the v2 -> v3
// migration is tested against what shipped.
const schemaV2 = `
CREATE TABLE IF NOT EXISTS ingest_state (
  path TEXT PRIMARY KEY,
  size INTEGER NOT NULL,
  read_offset INTEGER NOT NULL,
  mtime INTEGER NOT NULL,
  seen_ids TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS session_rollup (
  day TEXT NOT NULL,
  session_id TEXT NOT NULL,
  project TEXT NOT NULL,
  model TEXT NOT NULL,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read INTEGER NOT NULL DEFAULT 0,
  cache_creation INTEGER NOT NULL DEFAULT 0,
  cache_creation_1h INTEGER NOT NULL DEFAULT 0,
  cost_usd REAL NOT NULL DEFAULT 0,
  prompts INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (day, session_id, project, model)
);
CREATE TABLE IF NOT EXISTS attribution_rollup (
  day TEXT NOT NULL,
  session_id TEXT NOT NULL,
  project TEXT NOT NULL,
  kind TEXT NOT NULL,
  name TEXT NOT NULL,
  records INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (day, session_id, project, kind, name)
);
CREATE INDEX IF NOT EXISTS idx_session_day ON session_rollup(day);
CREATE INDEX IF NOT EXISTS idx_attr_day ON attribution_rollup(day);
`

// makeV2DB builds a version-2 DB with 3 session_rollup, 2 attribution_rollup and
// 2 ingest_state rows, then closes it. It returns the summed session cost.
func makeV2DB(t *testing.T, path string) float64 {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		schemaV2,
		`INSERT INTO ingest_state(path,size,read_offset,mtime,seen_ids) VALUES ('/t/a.jsonl',10,10,1,'m1 m2'),('/t/b.jsonl',20,15,2,'')`,
		`INSERT INTO session_rollup(day,session_id,project,model,input_tokens,cost_usd,prompts) VALUES
		  ('2026-08-05','S1','p','m1',100,1.25,2),
		  ('2026-08-05','S1','p','m2',200,2.5,1),
		  ('2026-08-06','S2','q','m1',300,0.125,4)`,
		`INSERT INTO attribution_rollup VALUES ('2026-08-05','S1','p','skill','x',3),('2026-08-06','S2','q','agent','y',1)`,
		`PRAGMA user_version = 2`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return 1.25 + 2.5 + 0.125
}

func tableExists(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// A version-2 DB keeps every row: v3 only adds tables and a column.
func TestMigrateV2ToV3KeepsRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "analytics.db")
	wantCost := makeV2DB(t, path)

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for tbl, want := range map[string]int{"session_rollup": 3, "attribution_rollup": 2, "ingest_state": 2} {
		if n := countRows(t, path, tbl); n != want {
			t.Errorf("%s rows = %d, want %d", tbl, n, want)
		}
	}
	var cost float64
	if err := s.db.QueryRow(`SELECT SUM(cost_usd) FROM session_rollup`).Scan(&cost); err != nil {
		t.Fatal(err)
	}
	if cost != wantCost {
		t.Errorf("SUM(cost_usd) = %v, want %v", cost, wantCost)
	}
	if v := userVersion(t, s); v != 3 {
		t.Errorf("user_version = %d, want 3", v)
	}
	fi, err := os.Stat(path + ".pre-v3.bak")
	if err != nil {
		t.Fatalf("pre-v3 backup: %v", err)
	}
	if !fi.Mode().IsRegular() {
		t.Errorf("pre-v3 backup is not a regular file: %v", fi.Mode())
	}
	if n := countRows(t, path+".pre-v3.bak", "session_rollup"); n != 3 {
		t.Errorf("backup session_rollup rows = %d, want 3", n)
	}
	if _, err := os.Stat(path + ".pre-dedupe.bak"); !os.IsNotExist(err) {
		t.Errorf("v2 -> v3 must not take the rebuild backup: %v", err)
	}
	for _, tbl := range []string{"session_dim", "driver_rollup"} {
		if !tableExists(t, s, tbl) {
			t.Errorf("table %s missing", tbl)
		}
	}
	var withBackfilled0 int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ingest_state WHERE backfilled = 0`).Scan(&withBackfilled0); err != nil {
		t.Fatal(err)
	}
	if withBackfilled0 != 2 {
		t.Errorf("ingest_state rows with backfilled = 0: %d, want 2", withBackfilled0)
	}
	// The new tables take the spec columns.
	if _, err := s.db.Exec(`INSERT INTO session_dim(session_id,project,via,via_path) VALUES ('S1','p','clone','/x')`); err != nil {
		t.Errorf("session_dim insert: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO driver_rollup(day,session_id,project,kind,name) VALUES ('2026-08-05','S1','p','skill','x')`); err != nil {
		t.Errorf("driver_rollup insert: %v", err)
	}
}

// A v3 DB reopens cleanly and takes no second backup.
func TestMigrateV3Reopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "analytics.db")
	makeV2DB(t, path)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path + ".pre-v3.bak")
	if err != nil {
		t.Fatal(err)
	}
	before := fi.ModTime()
	s.Close()
	time.Sleep(20 * time.Millisecond)

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if v := userVersion(t, s2); v != 3 {
		t.Errorf("user_version = %d, want 3", v)
	}
	fi2, err := os.Stat(path + ".pre-v3.bak")
	if err != nil {
		t.Fatal(err)
	}
	if !fi2.ModTime().Equal(before) {
		t.Errorf("backup rewritten on reopen: mtime %v -> %v", before, fi2.ModTime())
	}
	if n := countRows(t, path, "session_rollup"); n != 3 {
		t.Errorf("session_rollup rows after reopen = %d, want 3", n)
	}
}

// A version-0 DB still takes the v2 drop-and-rebuild path, then lands on v3.
func TestMigrateV0StillRebuilds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "analytics.db")
	old := makeV0DB(t, path)
	defer old.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v := userVersion(t, s); v != 3 {
		t.Errorf("user_version = %d, want 3", v)
	}
	fi, err := os.Stat(path + ".pre-dedupe.bak")
	if err != nil || !fi.Mode().IsRegular() {
		t.Errorf("pre-dedupe backup missing or not a file: %v", err)
	}
	if _, err := os.Stat(path + ".pre-v3.bak"); !os.IsNotExist(err) {
		t.Errorf("a rebuilt DB has nothing to keep; pre-v3 backup should not exist: %v", err)
	}
	for _, tbl := range []string{"session_rollup", "ingest_state", "attribution_rollup", "session_dim", "driver_rollup"} {
		if !tableExists(t, s, tbl) {
			t.Errorf("table %s missing", tbl)
		}
	}
	for _, tbl := range []string{"session_rollup", "ingest_state", "attribution_rollup"} {
		if n := countRows(t, path, tbl); n != 0 {
			t.Errorf("%s rows = %d, want 0 after rebuild", tbl, n)
		}
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('ingest_state') WHERE name='backfilled'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("ingest_state.backfilled column: n=%d err=%v", n, err)
	}
}

// Every query reports the cost stored at ingest (Tok.Cost), not one recomputed
// from the summed tokens.
func TestRowsCarryStoredCost(t *testing.T) {
	s := openTemp(t)
	p := newParsed()
	p.Tokens[TokKey{Day: "2026-08-05", Model: "claude-opus-5"}] = &Tok{Input: 10, Output: 1, CacheCreation: 7, CacheWrite1h: 4, Prompts: 1, Cost: 1.23}
	if err := s.Apply("S1", "p", "/t/a.jsonl", RootInfo{}, p, true, 1, 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	// A second batch accumulates on upsert.
	p2 := newParsed()
	p2.Tokens[TokKey{Day: "2026-08-05", Model: "claude-opus-5"}] = &Tok{CacheCreation: 3, CacheWrite1h: 2, Cost: 0.77}
	if err := s.Apply("S1", "p", "/t/a.jsonl", RootInfo{}, p2, false, 2, 2, 2, nil); err != nil {
		t.Fatal(err)
	}
	const want = 2.0
	dm, err := s.ByDayModel("2026-08-01", "2026-08-31", Where{})
	if err != nil || len(dm) != 1 || !approx(dm[0].Cost, want) {
		t.Errorf("ByDayModel = %+v err=%v, want cost %v", dm, err, want)
	}
	bm, err := s.ByModel("2026-08-01", "2026-08-31", Where{})
	if err != nil || len(bm) != 1 || !approx(bm[0].Cost, want) {
		t.Errorf("ByModel = %+v err=%v, want cost %v", bm, err, want)
	}
	bp, err := s.ByProject("2026-08-01", "2026-08-31", Where{})
	if err != nil || len(bp) != 1 || !approx(bp[0].Cost, want) {
		t.Errorf("ByProject = %+v err=%v, want cost %v", bp, err, want)
	}
	ss, err := s.Sessions("2026-08-01", "2026-08-31", 10, Where{})
	if err != nil || len(ss) != 1 || !approx(ss[0].Cost, want) {
		t.Errorf("Sessions = %+v err=%v, want cost %v", ss, err, want)
	}
	sc, err := s.SessionCosts("2026-08-01", "2026-08-31", Where{})
	if err != nil || len(sc) != 1 || !approx(sc[0].Cost, want) {
		t.Errorf("SessionCosts = %+v err=%v, want cost %v", sc, err, want)
	}
	var cc1h int64
	if err := s.db.QueryRow(`SELECT cache_creation_1h FROM session_rollup`).Scan(&cc1h); err != nil || cc1h != 6 {
		t.Errorf("cache_creation_1h = %d err=%v, want 6", cc1h, err)
	}
}

// ingestPaths lists every ingest_state path, sorted.
func ingestPaths(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT path FROM ingest_state ORDER BY path`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func TestRewritePathPrefix(t *testing.T) {
	s := openTemp(t)
	for _, p := range []string{"/r/agent-box/x.jsonl", "/r/agent-box/accounts/w/claude/projects/p/y.jsonl", "/r/agent-box", "/home/u/.claude/projects/p/z.jsonl"} {
		if err := s.Apply("S1", "proj", p, RootInfo{}, newParsed(), true, 10, 10, 1, nil); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.RewritePathPrefix("/r/agent-box", "/r/culibox")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("copied %d rows, want 3", n)
	}
	// R12: the rows are copied, not moved: the legacy rows stay, so a rollback
	// to an older binary (which reads through the link at the old path) does
	// not re-read every transcript from 0.
	got := strings.Join(ingestPaths(t, s), " ")
	want := "/home/u/.claude/projects/p/z.jsonl /r/agent-box /r/agent-box/accounts/w/claude/projects/p/y.jsonl /r/agent-box/x.jsonl /r/culibox /r/culibox/accounts/w/claude/projects/p/y.jsonl /r/culibox/x.jsonl"
	if got != want {
		t.Errorf("paths = %s\nwant    %s", got, want)
	}
	// The offset travels with the copy: the transcript is not re-read.
	for _, p := range []string{"/r/culibox/x.jsonl", "/r/agent-box/x.jsonl"} {
		if _, off, ok, _, _, _ := s.State(p); !ok || off != 10 {
			t.Errorf("state of %s after rewrite: ok=%v offset=%d, want true 10", p, ok, off)
		}
	}
	// Idempotent: a second run (a resumed move) inserts nothing.
	if n, err := s.RewritePathPrefix("/r/agent-box", "/r/culibox"); err != nil || n != 0 {
		t.Errorf("second rewrite = %d, %v; want 0, nil", n, err)
	}
	if got2 := strings.Join(ingestPaths(t, s), " "); got2 != want {
		t.Errorf("paths after the second run = %s", got2)
	}
}

// A row already under the new prefix (written by a serve that ran on the new
// root) wins: the copy does not overwrite it and does not fail.
func TestRewritePathPrefixKeepsExistingNewRow(t *testing.T) {
	s := openTemp(t)
	if err := s.Apply("S1", "proj", "/r/agent-box/x.jsonl", RootInfo{}, newParsed(), true, 10, 10, 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply("S1", "proj", "/r/culibox/x.jsonl", RootInfo{}, newParsed(), true, 20, 20, 1, nil); err != nil {
		t.Fatal(err)
	}
	n, err := s.RewritePathPrefix("/r/agent-box", "/r/culibox")
	if err != nil || n != 0 {
		t.Fatalf("rewrite = %d, %v; want 0, nil", n, err)
	}
	if _, off, ok, _, _, _ := s.State("/r/culibox/x.jsonl"); !ok || off != 20 {
		t.Errorf("new row offset = %d (ok=%v), want 20 kept", off, ok)
	}
}

func TestRewritePathPrefixKeepsSiblingPrefix(t *testing.T) {
	s := openTemp(t)
	for _, p := range []string{"/r/agent-box-prod/x.jsonl", "/r/agent-boxx", "/r/agent-box/y.jsonl"} {
		if err := s.Apply("S1", "proj", p, RootInfo{}, newParsed(), true, 10, 10, 1, nil); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.RewritePathPrefix("/r/agent-box", "/r/culibox")
	if err != nil || n != 1 {
		t.Fatalf("rewrite = %d, %v; want 1, nil", n, err)
	}
	got := strings.Join(ingestPaths(t, s), " ")
	if want := "/r/agent-box-prod/x.jsonl /r/agent-box/y.jsonl /r/agent-boxx /r/culibox/y.jsonl"; got != want {
		t.Errorf("paths = %s, want %s", got, want)
	}
}

// Adoption calls RewritePathPrefix once per moved entry under the box
// manager's lock (#330): the rows under the root are found through the path
// index, never by reading the whole table.
func TestRewritePathPrefixUsesThePathIndex(t *testing.T) {
	s := openTemp(t)
	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+rewritePrefixQuery, underRoot("/r/agent-box")...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if got := strings.Join(plan, "; "); !strings.Contains(got, "SEARCH") || strings.Contains(got, "SCAN") {
		t.Fatalf("query plan %q: want a SEARCH on the path index, no SCAN", got)
	}
}

// The range is bytewise: names that sort right around "<root>/" (".", "-", "0"
// after the root) are not under it, and every name below it is, whatever its
// bytes.
func TestRewritePathPrefixRangeEdges(t *testing.T) {
	s := openTemp(t)
	under := []string{"/r/a", "/r/a/", "/r/a/x", "/r/a/été.jsonl", "/r/a/\xff", "/r/a//x"}
	beside := []string{"/r/a-prod/x", "/r/a.bak/x", "/r/a0/x", "/r/a0", "/r/", "/r/b/x", "/r/A/x"}
	for _, p := range append(slices.Clone(under), beside...) {
		if err := s.Apply("S1", "proj", p, RootInfo{}, newParsed(), true, 10, 10, 1, nil); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.RewritePathPrefix("/r/a", "/n")
	if err != nil || n != len(under) {
		t.Fatalf("rewrite = %d, %v; want %d", n, err, len(under))
	}
	for _, p := range under {
		if _, _, ok, _, _, _ := s.State("/n" + p[len("/r/a"):]); !ok {
			t.Errorf("%q not copied", p)
		}
	}
	for _, p := range ingestPaths(t, s) {
		if strings.HasPrefix(p, "/n") && !slices.ContainsFunc(under, func(u string) bool { return "/n"+u[len("/r/a"):] == p }) {
			t.Errorf("copied %q, which is not under the root", p)
		}
	}
}

// dimRow is one session_dim row's dims.
type dimRow struct {
	cwd, branch, runtime, account, rootKey string
	parsed                                 int
}

func readDim(t *testing.T, s *Store, sid, project string) (dimRow, bool) {
	t.Helper()
	var d dimRow
	err := s.db.QueryRow(`SELECT cwd,branch,runtime,account,root_key,parsed FROM session_dim WHERE session_id=? AND project=?`, sid, project).
		Scan(&d.cwd, &d.branch, &d.runtime, &d.account, &d.rootKey, &d.parsed)
	if err == sql.ErrNoRows {
		return d, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return d, true
}

// The launch folder is the first one seen; a later chunk never replaces it. A
// branch is replaced by a newer non-empty one and kept through an empty one.
func TestApplyKeepsFirstCwd(t *testing.T) {
	s := openTemp(t)
	apply := func(cwd, branch string, off int64) {
		t.Helper()
		p := newParsed()
		p.Cwd, p.Branch = cwd, branch
		if err := s.Apply("S1", "p", "/t/a.jsonl", RootInfo{Runtime: "box", Key: "B1"}, p, off == 10, off, off, 1, nil); err != nil {
			t.Fatal(err)
		}
	}
	apply("/a", "x", 10)
	apply("/b", "", 20)
	d, ok := readDim(t, s, "S1", "p")
	if !ok || d.cwd != "/a" || d.branch != "x" || d.parsed != 1 || d.runtime != "box" || d.rootKey != "B1" {
		t.Fatalf("dim = %+v (ok=%v), want cwd /a, branch x, parsed 1, box B1", d, ok)
	}
	apply("", "y", 30)
	if d, _ := readDim(t, s, "S1", "p"); d.cwd != "/a" || d.branch != "y" {
		t.Fatalf("dim = %+v, want cwd /a kept, branch y", d)
	}
	// A first chunk with no cwd (summary lines first), then later ones that have it.
	late := func(cwd string, off int64) {
		t.Helper()
		p := newParsed()
		p.Cwd = cwd
		if err := s.Apply("S2", "p", "/t/b.jsonl", RootInfo{}, p, off == 1, off, off, 1, nil); err != nil {
			t.Fatal(err)
		}
	}
	late("", 1)
	late("/late", 2)
	late("/later", 3)
	if d, _ := readDim(t, s, "S2", "p"); d.cwd != "/late" {
		t.Fatalf("cwd = %q, want the first non-empty one, /late", d.cwd)
	}
}

// Apply adds driver deltas, upgrades a legacy parsed=0 row in place, and leaves
// the columns the workspace resolver owns alone.
func TestApplyAddsDriversAndLeavesResolvedDimsAlone(t *testing.T) {
	s := openTemp(t)
	if _, err := s.db.Exec(`INSERT INTO session_dim(session_id,project,workspace,unmatched,profile,via,via_path,parsed) VALUES('S1','p','ws',1,'prof','clone','/x',0)`); err != nil {
		t.Fatal(err)
	}
	key := DriverKey{Day: "2026-08-05", Kind: "mcp_tool", Name: "t", Parent: "srv"}
	for i := 1; i <= 2; i++ {
		p := newParsed()
		p.Drivers[key] = &DriverVal{Cost: 1.5, Tokens: 10}
		if err := s.Apply("S1", "p", "/t/a.jsonl", RootInfo{Runtime: "host"}, p, i == 1, int64(i), int64(i), 1, nil); err != nil {
			t.Fatal(err)
		}
	}
	var cost float64
	var tok int64
	if err := s.db.QueryRow(`SELECT cost_usd, tokens FROM driver_rollup WHERE session_id='S1' AND kind='mcp_tool' AND parent='srv'`).Scan(&cost, &tok); err != nil {
		t.Fatal(err)
	}
	if cost != 3 || tok != 20 {
		t.Errorf("driver = %v, %d; want 3, 20", cost, tok)
	}
	var ws, prof, via, vp string
	var un, parsed int
	if err := s.db.QueryRow(`SELECT workspace,unmatched,profile,via,via_path,parsed FROM session_dim WHERE session_id='S1'`).Scan(&ws, &un, &prof, &via, &vp, &parsed); err != nil {
		t.Fatal(err)
	}
	if ws != "ws" || un != 1 || prof != "prof" || via != "clone" || vp != "/x" {
		t.Errorf("resolved columns changed: %s %d %s %s %s", ws, un, prof, via, vp)
	}
	if parsed != 1 {
		t.Errorf("a legacy parsed=0 row was not upgraded in place: parsed=%d", parsed)
	}
}

// The backfilled mark moves with the offset when a data root moves.
func TestCarryOffsetsKeepsBackfilled(t *testing.T) {
	s := openTemp(t)
	for _, p := range []string{"/r/old/a.jsonl", "/r/old/b.jsonl"} {
		if err := s.Apply("S1", "p", p, RootInfo{}, newParsed(), true, 10, 10, 1, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`UPDATE ingest_state SET backfilled=0 WHERE path='/r/old/b.jsonl'`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.RewritePathPrefix("/r/old", "/r/new"); err != nil || n != 2 {
		t.Fatalf("rewrite = %d, %v", n, err)
	}
	for p, want := range map[string]bool{"/r/new/a.jsonl": true, "/r/new/b.jsonl": false} {
		_, off, ok, _, bf, err := s.State(p)
		if err != nil || !ok || off != 10 || bf != want {
			t.Errorf("State(%s) = offset %d ok %v backfilled %v err %v; want 10 true %v", p, off, ok, bf, err, want)
		}
	}
}

func TestSeedLegacyDims(t *testing.T) {
	const home, data = "/h/u", "/h/u/.local/share/culibox"
	cl := func(p string) (RootInfo, string, bool) { return classifyPath(p, home, data) }
	s := openTemp(t)
	for _, p := range []string{
		home + "/.claude/projects/-p/H1.jsonl",
		home + "/.claude/projects/-p/H2/subagents/agent-a.jsonl",
		data + "/accounts/work/claude/projects/-p/A1.jsonl",
		data + "/boxstate/B1/.claude/projects/-p/X1.jsonl",
		data + "/boxstate/.conversations/c9/-p/C1.jsonl",
		"/elsewhere/-q/U1.jsonl",
	} {
		if err := s.Apply("", "p", p, RootInfo{}, newParsed(), true, 10, 10, 1, nil); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.SeedLegacyDims(cl)
	if err != nil {
		t.Fatal(err)
	}
	if n != 6 {
		t.Fatalf("seeded %d, want 6", n)
	}
	for _, w := range []struct {
		sid, project string
		d            dimRow
	}{
		{"H1", "-p", dimRow{runtime: "host"}},
		{"H2", "-p", dimRow{runtime: "host"}},
		{"A1", "-p", dimRow{runtime: "host", account: "work"}},
		{"X1", "-p", dimRow{runtime: "box", rootKey: "B1"}},
		{"C1", "-p", dimRow{runtime: "box", rootKey: ".conversations/c9"}},
		{"U1", "-q", dimRow{}},
	} {
		d, ok := readDim(t, s, w.sid, w.project)
		if !ok || d != w.d {
			t.Errorf("dim %s/%s = %+v (ok=%v), want %+v", w.project, w.sid, d, ok, w.d)
		}
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM session_dim`).Scan(&total); err != nil || total != 6 {
		t.Errorf("session_dim has %d rows (err %v), want 6", total, err)
	}
	// Once a session is parsed, seeding leaves its row as it is.
	if _, err := s.db.Exec(`UPDATE session_dim SET parsed=1, cwd='/real', runtime='box' WHERE session_id='H1'`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.SeedLegacyDims(cl); err != nil || n != 0 {
		t.Errorf("second seed = %d, %v; want 0", n, err)
	}
	if d, _ := readDim(t, s, "H1", "-p"); d.cwd != "/real" || d.runtime != "box" || d.parsed != 1 {
		t.Errorf("a parsed row was changed by seeding: %+v", d)
	}
}
