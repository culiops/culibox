package analytics

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProjectFromRel(t *testing.T) {
	cases := map[string]string{
		"-home-x/uuid.jsonl":                    "-home-x",
		"-home-x/uuid/subagents/agent-y.jsonl":  "-home-x",
		"file.jsonl":                            "", // no slug dir
		"-home-culiops-agent-box/abc-def.jsonl": "-home-culiops-agent-box",
	}
	for in, want := range cases {
		if got := projectFromRel(in); got != want {
			t.Errorf("projectFromRel(%q) = %q, want %q", in, got, want)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

const line1 = `{"sessionId":"S1","timestamp":"2026-08-05T10:00:00Z","message":{"role":"user","content":"hi"}}` + "\n"
const line2 = `{"sessionId":"S1","timestamp":"2026-08-05T10:00:05Z","message":{"role":"assistant","model":"m1","usage":{"input_tokens":100,"output_tokens":20}}}` + "\n"
const line3 = `{"sessionId":"S1","timestamp":"2026-08-05T10:01:00Z","message":{"role":"assistant","model":"m1","usage":{"input_tokens":50,"output_tokens":10}}}` + "\n"

func TestIngestIncremental(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "-home-proj", "S1.jsonl")
	writeFile(t, path, line1+line2)
	s := openTemp(t)

	var st IngestStats
	ingestRoot(s, root, nil, &st)
	if st.Files != 1 {
		t.Fatalf("first pass files=%d want 1", st.Files)
	}
	rows, _ := s.ByDayModel("2026-08-01", "2026-08-31", Where{})
	if len(rows) != 1 || rows[0].Input != 100 || rows[0].Output != 20 || rows[0].Prompts != 1 {
		t.Fatalf("after first pass: %+v", rows)
	}

	// Re-ingest with no change → nothing new.
	st = IngestStats{}
	ingestRoot(s, root, nil, &st)
	if st.Files != 0 || st.BytesRead != 0 {
		t.Fatalf("no-change pass read data: %+v", st)
	}
	rows, _ = s.ByDayModel("2026-08-01", "2026-08-31", Where{})
	if rows[0].Input != 100 {
		t.Fatalf("no-change pass double-counted: %+v", rows[0])
	}

	// Append one line → only the new bytes are read and added.
	appendFile(t, path, line3)
	st = IngestStats{}
	ingestRoot(s, root, nil, &st)
	if st.Files != 1 || st.BytesRead != int64(len(line3)) {
		t.Fatalf("append pass: files=%d bytes=%d want 1/%d", st.Files, st.BytesRead, len(line3))
	}
	rows, _ = s.ByDayModel("2026-08-01", "2026-08-31", Where{})
	if rows[0].Input != 150 || rows[0].Output != 30 {
		t.Fatalf("after append: %+v want in=150 out=30", rows[0])
	}
}

// A file that shrinks must not be re-added from zero (rollups are additive).
func TestIngestShrinkNoDouble(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "-home-proj", "S1.jsonl")
	writeFile(t, path, line1+line2)
	s := openTemp(t)
	var st IngestStats
	ingestRoot(s, root, nil, &st)

	// Truncate to fewer bytes than we already consumed.
	writeFile(t, path, line1)
	st = IngestStats{}
	ingestRoot(s, root, nil, &st)

	rows, _ := s.ByDayModel("2026-08-01", "2026-08-31", Where{})
	if rows[0].Input != 100 || rows[0].Output != 20 {
		t.Fatalf("shrink double-counted: %+v", rows[0])
	}
	_, off, _, _, _, _ := s.State(path)
	if off != int64(len(line1)) {
		t.Errorf("offset not clamped to new size: got %d want %d", off, len(line1))
	}
}

// A nested subagent transcript: its tokens count, attributed to the PARENT
// session id (from the record) and the top-level project slug.
func TestIngestSubagentAttributedToParent(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "-home-proj", "PARENT-uuid", "subagents", "agent-x.jsonl")
	writeFile(t, sub,
		`{"sessionId":"PARENT-uuid","isSidechain":true,"timestamp":"2026-08-05T10:00:00Z","message":{"role":"assistant","model":"m1","usage":{"input_tokens":42,"output_tokens":7}}}`+"\n")
	s := openTemp(t)
	var st IngestStats
	ingestRoot(s, root, nil, &st)

	rows, err := s.Sessions("2026-08-01", "2026-08-31", 10, Where{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0].SessionID != "PARENT-uuid" || rows[0].Project != "-home-proj" || rows[0].Input != 42 {
		t.Fatalf("subagent not attributed to parent/project: %+v", rows[0])
	}
}

// A FIFO under a root (e.g. planted in boxstate by a contained agent) must be
// refused without blocking the serialized ingest pass.
func TestIngestSkipsFIFO(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "-home-proj", "x.jsonl")
	if err := os.MkdirAll(filepath.Dir(fifo), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	s := openTemp(t)

	var st IngestStats
	done := make(chan struct{})
	go func() {
		defer close(done)
		ingestRoot(s, root, nil, &st)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		// Unblock the wedged goroutine so the temp dir can be cleaned up.
		if f, err := os.OpenFile(fifo, os.O_RDWR, 0); err == nil {
			f.Close()
		}
		t.Fatal("ingestRoot blocked on a FIFO")
	}
	if st.Errors != 1 || st.Files != 0 || st.BytesRead != 0 {
		t.Fatalf("FIFO not refused: %+v", st)
	}
}

// A symlinked transcript must not be followed: its target is outside the root
// and the box chose it.
func TestIngestSkipsSymlinkedJSONL(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.jsonl")
	writeFile(t, outside, line1+line2)
	link := filepath.Join(root, "-home-proj", "x.jsonl")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	s := openTemp(t)

	var st IngestStats
	ingestRoot(s, root, nil, &st)
	if st.Files != 0 || st.BytesRead != 0 || st.Errors != 1 {
		t.Fatalf("symlink not refused: %+v", st)
	}
	rows, _ := s.ByDayModel("2026-08-01", "2026-08-31", Where{})
	if len(rows) != 0 {
		t.Fatalf("symlink target was ingested: %+v", rows)
	}
}

// A message whose content blocks span two ingest batches (a live transcript
// mid-write) is counted once.
func TestDedupeAcrossBatches(t *testing.T) {
	rec := func(id, ts string) string {
		return `{"sessionId":"S1","timestamp":"` + ts + `","message":{"id":"` + id + `","role":"assistant","model":"m1","usage":{"input_tokens":10,"output_tokens":5}}}` + "\n"
	}
	root := t.TempDir()
	path := filepath.Join(root, "-home-proj", "S1.jsonl")
	writeFile(t, path, rec("msg_A", "2026-08-05T10:00:01Z")+rec("msg_A", "2026-08-05T10:00:02Z"))
	s := openTemp(t)
	var st IngestStats
	ingestRoot(s, root, nil, &st)

	appendFile(t, path, rec("msg_A", "2026-08-05T10:00:03Z")+rec("msg_B", "2026-08-05T10:00:04Z"))
	st = IngestStats{}
	ingestRoot(s, root, nil, &st)

	rows, err := s.ByDayModel("2026-08-01", "2026-08-31", Where{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Input != 20 || rows[0].Output != 10 {
		t.Fatalf("rows = %+v, want one row in=20 out=10 (msg_A once, msg_B once)", rows)
	}
	_, _, _, seen, _, err := s.State(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "msg_A" || seen[1] != "msg_B" {
		t.Fatalf("seen = %v want [msg_A msg_B]", seen)
	}
}

// Three counted messages with costs, a folder, a branch and attributions. The
// first has no attribution, so the driver sums are a strict part of the cost.
const (
	dl1 = `{"sessionId":"S1","cwd":"/work/x","gitBranch":"main","timestamp":"2026-08-05T10:00:01Z","message":{"id":"d1","role":"assistant","model":"claude-opus-5","usage":{"input_tokens":1000000,"output_tokens":0}}}` + "\n"
	dl2 = `{"sessionId":"S1","cwd":"/work/y","gitBranch":"feat","timestamp":"2026-08-05T10:00:02Z","attributionSkill":"brainstorming","attributionMcpServer":"playwright","attributionMcpTool":"browser_close","message":{"id":"d2","role":"assistant","model":"claude-opus-5","usage":{"input_tokens":2000000,"output_tokens":0}}}` + "\n"
	dl3 = `{"sessionId":"S1","cwd":"/work/z","timestamp":"2026-08-05T10:00:03Z","attributionSkill":"brainstorming","message":{"id":"d3","role":"assistant","model":"claude-sonnet-4-6","usage":{"input_tokens":3000000,"output_tokens":0}}}` + "\n"
)

func scalar(t *testing.T, s *Store, q string, args ...any) float64 {
	t.Helper()
	var v sql.NullFloat64
	if err := s.db.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v.Float64
}

// perModel is each model's summed cost in session_rollup.
func perModel(t *testing.T, s *Store) map[string]float64 {
	t.Helper()
	rows, err := s.db.Query(`SELECT model, SUM(cost_usd) FROM session_rollup GROUP BY model`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	m := map[string]float64{}
	for rows.Next() {
		var k string
		var v float64
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		m[k] = v
	}
	return m
}

func backfilledOf(t *testing.T, s *Store, path string) bool {
	t.Helper()
	_, _, ok, _, bf, err := s.State(path)
	if err != nil || !ok {
		t.Fatalf("State(%s): ok=%v err=%v", path, ok, err)
	}
	return bf
}

// A transcript counted by an older version gets its dims and drivers once, from
// the bytes already read, and no total moves.
func TestBackfillDoesNotChangeRollups(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "-home-proj", "S1.jsonl")
	writeFile(t, path, dl1+dl2+dl3)
	s := openTemp(t)
	var st IngestStats
	ingestRoot(s, root, nil, &st)
	if st.Files != 1 {
		t.Fatalf("first pass files=%d", st.Files)
	}
	wantCost := scalar(t, s, `SELECT SUM(cost_usd) FROM session_rollup`)
	if wantCost <= 0 {
		t.Fatalf("fixture has no cost: %v", wantCost)
	}
	wantModels := perModel(t, s)
	wantAttr := scalar(t, s, `SELECT SUM(records) FROM attribution_rollup`)
	if wantAttr <= 0 {
		t.Fatal("fixture has no attribution records")
	}
	wantDrivers := scalar(t, s, `SELECT SUM(cost_usd) FROM driver_rollup WHERE kind='skill'`)
	if wantDrivers <= 0 || wantDrivers >= wantCost {
		t.Fatalf("skill drivers %v should be a strict part of the cost %v", wantDrivers, wantCost)
	}

	// Make it look like a v2 file: counted, with no dims or drivers.
	if _, err := s.db.Exec(`UPDATE ingest_state SET backfilled=0; DELETE FROM driver_rollup; DELETE FROM session_dim`); err != nil {
		t.Fatal(err)
	}

	st = IngestStats{}
	ingestRoot(s, root, nil, &st)
	if st.Files != 0 || st.BytesRead != 0 {
		t.Errorf("backfill counted as new bytes: %+v", st)
	}
	if got := scalar(t, s, `SELECT SUM(cost_usd) FROM session_rollup`); got != wantCost {
		t.Errorf("session_rollup cost %v after backfill, want %v", got, wantCost)
	}
	for m, w := range wantModels {
		if got := perModel(t, s)[m]; got != w {
			t.Errorf("model %s cost %v after backfill, want %v", m, got, w)
		}
	}
	if got := scalar(t, s, `SELECT SUM(cost_usd) FROM driver_rollup WHERE kind='skill'`); got != wantDrivers {
		t.Errorf("skill driver cost %v after backfill, want %v", got, wantDrivers)
	}
	if got := scalar(t, s, `SELECT SUM(cost_usd) FROM driver_rollup WHERE kind='mcp_tool' AND parent='playwright'`); got <= 0 {
		t.Errorf("mcp_tool driver missing after backfill: %v", got)
	}
	if got := scalar(t, s, `SELECT SUM(records) FROM attribution_rollup`); got != wantAttr {
		t.Errorf("attribution records %v after backfill, want %v", got, wantAttr)
	}
	if !backfilledOf(t, s, path) {
		t.Error("backfilled still 0 after the backfill")
	}
	d, ok := readDim(t, s, "S1", "-home-proj")
	if !ok || d.parsed != 1 || d.cwd != "/work/x" || d.branch != "feat" {
		t.Errorf("dim = %+v (ok=%v), want parsed 1, first cwd /work/x, last branch feat", d, ok)
	}

	// A third pass must not add the drivers again.
	st = IngestStats{}
	ingestRoot(s, root, nil, &st)
	if got := scalar(t, s, `SELECT SUM(cost_usd) FROM driver_rollup WHERE kind='skill'`); got != wantDrivers {
		t.Errorf("skill driver cost %v after the third pass, want %v unchanged", got, wantDrivers)
	}
	if got := scalar(t, s, `SELECT SUM(cost_usd) FROM session_rollup`); got != wantCost {
		t.Errorf("session_rollup cost %v after the third pass, want %v", got, wantCost)
	}
}

// A legacy file that also grew: the same pass backfills the old bytes and reads
// the new ones. session_rollup gains only the new line; drivers cover all of it.
func TestBackfillThenAppendInOnePass(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "-home-proj", "S1.jsonl")
	writeFile(t, path, dl1+dl2)
	s := openTemp(t)
	var st IngestStats
	ingestRoot(s, root, nil, &st)
	costBefore := scalar(t, s, `SELECT SUM(cost_usd) FROM session_rollup`)
	if _, err := s.db.Exec(`UPDATE ingest_state SET backfilled=0; DELETE FROM driver_rollup; DELETE FROM session_dim`); err != nil {
		t.Fatal(err)
	}
	appendFile(t, path, dl3)

	st = IngestStats{}
	ingestRoot(s, root, nil, &st)
	if st.BytesRead != int64(len(dl3)) {
		t.Errorf("read %d new bytes, want %d", st.BytesRead, len(dl3))
	}
	// Reference: the same three lines ingested fresh.
	ref := t.TempDir()
	writeFile(t, filepath.Join(ref, "-home-proj", "S1.jsonl"), dl1+dl2+dl3)
	rs := openTemp(t)
	ingestRoot(rs, ref, nil, &IngestStats{})
	if got, want := scalar(t, s, `SELECT SUM(cost_usd) FROM session_rollup`), scalar(t, rs, `SELECT SUM(cost_usd) FROM session_rollup`); got != want || got <= costBefore {
		t.Errorf("session_rollup cost %v, want %v (before %v)", got, want, costBefore)
	}
	for _, kind := range []string{"skill", "mcp_server", "mcp_tool"} {
		q := `SELECT SUM(cost_usd) FROM driver_rollup WHERE kind=?`
		if got, want := scalar(t, s, q, kind), scalar(t, rs, q, kind); got != want || got <= 0 {
			t.Errorf("%s driver cost %v, want %v (a fresh ingest)", kind, got, want)
		}
	}
}

// A first-seen file is born backfilled, and its drivers are one parse of it.
func TestNewFileBornBackfilled(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "-home-proj", "S1.jsonl")
	writeFile(t, path, dl1+dl2+dl3)
	s := openTemp(t)
	ingestRoot(s, root, nil, &IngestStats{})
	if !backfilledOf(t, s, path) {
		t.Fatal("a new file was not born backfilled")
	}
	p, _, _, _, _, err := parseTranscript(strings.NewReader(dl1+dl2+dl3), nil)
	if err != nil {
		t.Fatal(err)
	}
	var want float64
	for k, v := range p.Drivers {
		if k.Kind == "skill" {
			want += v.Cost
		}
	}
	if got := scalar(t, s, `SELECT SUM(cost_usd) FROM driver_rollup WHERE kind='skill'`); got != want || want <= 0 {
		t.Errorf("skill driver cost %v, want one parse's %v", got, want)
	}
	// No second pass adds more.
	ingestRoot(s, root, nil, &IngestStats{})
	if got := scalar(t, s, `SELECT SUM(cost_usd) FROM driver_rollup WHERE kind='skill'`); got != want {
		t.Errorf("skill driver cost %v after a no-change pass, want %v", got, want)
	}
}

// A row read from offset 0 with backfilled=0 (a v2 file whose first read had not
// consumed a full line) becomes backfilled by the Apply that reads it from 0,
// so it is not backfilled a second time on top of that read.
func TestApplyFromZeroMarksBackfilled(t *testing.T) {
	s := openTemp(t)
	if err := s.Apply("S1", "p", "/t/a.jsonl", RootInfo{}, newParsed(), true, 5, 0, 1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE ingest_state SET backfilled=0`); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply("S1", "p", "/t/a.jsonl", RootInfo{}, newParsed(), true, 9, 9, 2, nil); err != nil {
		t.Fatal(err)
	}
	if !backfilledOf(t, s, "/t/a.jsonl") {
		t.Error("a read from offset 0 did not mark the row backfilled")
	}
	// A read from a later offset keeps the mark it finds.
	if _, err := s.db.Exec(`UPDATE ingest_state SET backfilled=0`); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply("S1", "p", "/t/a.jsonl", RootInfo{}, newParsed(), false, 12, 12, 3, nil); err != nil {
		t.Fatal(err)
	}
	if backfilledOf(t, s, "/t/a.jsonl") {
		t.Error("a read from offset 9 marked the row backfilled")
	}
	// The read's own start decides, not the offset the row holds: a row carried
	// here with offset 12 and read from 0 (the pass missed the carry) is marked.
	if err := s.Apply("S1", "p", "/t/a.jsonl", RootInfo{}, newParsed(), true, 12, 12, 4, nil); err != nil {
		t.Fatal(err)
	}
	if !backfilledOf(t, s, "/t/a.jsonl") {
		t.Error("a read from offset 0 over a row at offset 12 did not mark it backfilled")
	}
}

// A host-root pass takes the runtime from where the file sits, never from its
// content, and the legacy seed runs once before the first pass.
func TestIngestRecordsRuntimeFromRoot(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, "data")
	host := filepath.Join(home, ".claude", "projects", "-p", "S1.jsonl")
	acct := filepath.Join(data, "accounts", "work", "claude", "projects", "-p", "S2.jsonl")
	box := filepath.Join(data, "boxstate", "B1", ".claude", "projects", "-p", "S3.jsonl")
	conv := filepath.Join(data, "boxstate", ".conversations", "c9", "-p", "S4.jsonl")
	// The content claims nothing about where it ran.
	for _, p := range []string{host, acct, box, conv} {
		writeFile(t, p, strings.ReplaceAll(dl1, `"S1"`, `"`+strings.TrimSuffix(filepath.Base(p), ".jsonl")+`"`))
	}
	svc, err := New(filepath.Join(t.TempDir(), "a.db"), DefaultRoots(home, data), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	svc.SetBoxState(filepath.Join(data, "boxstate"))
	svc.SetHome(home)

	// A session an older version counted: a row in ingest_state, no dim yet.
	legacy := filepath.Join(home, ".claude", "projects", "-p", "OLD.jsonl")
	if err := svc.store.Apply("", "-p", legacy, RootInfo{}, newParsed(), true, 10, 10, 1, nil); err != nil {
		t.Fatal(err)
	}
	if st, err := svc.Ingest(); err != nil || st.Errors != 0 || st.Files != 4 {
		t.Fatalf("st=%+v err=%v", st, err)
	}
	for _, w := range []struct {
		sid string
		d   dimRow
	}{
		{"S1", dimRow{cwd: "/work/x", branch: "main", runtime: "host", parsed: 1}},
		{"S2", dimRow{cwd: "/work/x", branch: "main", runtime: "host", account: "work", parsed: 1}},
		{"S3", dimRow{cwd: "/work/x", branch: "main", runtime: "box", rootKey: "B1", parsed: 1}},
		{"S4", dimRow{cwd: "/work/x", branch: "main", runtime: "box", rootKey: ".conversations/c9", parsed: 1}},
		{"OLD", dimRow{runtime: "host", parsed: 0}},
	} {
		d, ok := readDim(t, svc.store, w.sid, "-p")
		if !ok || d != w.d {
			t.Errorf("dim %s = %+v (ok=%v), want %+v", w.sid, d, ok, w.d)
		}
	}
}

const (
	mainCwdLine = `{"sessionId":"S1","cwd":"/work/main","timestamp":"2026-08-05T10:00:01Z","message":{"id":"mm1","role":"assistant","model":"claude-opus-5","usage":{"input_tokens":1000000,"output_tokens":0}}}` + "\n"
	subCwdLine  = `{"sessionId":"S1","cwd":"/work/main/.claude/worktrees/sub","timestamp":"2026-08-05T10:00:02Z","attributionAgent":"helper","message":{"id":"sm1","role":"assistant","model":"claude-opus-5","usage":{"input_tokens":2000000,"output_tokens":0}}}` + "\n"
)

func checkMainCwdWins(t *testing.T, s *Store) {
	t.Helper()
	d, ok := readDim(t, s, "S1", "-p")
	if !ok || d.cwd != "/work/main" || d.parsed != 1 {
		t.Errorf("dim = %+v (ok=%v), want the main file's cwd /work/main, parsed 1", d, ok)
	}
	// The subagent file still counts: its driver cost is stored.
	if got := scalar(t, s, `SELECT SUM(cost_usd) FROM driver_rollup WHERE kind='agent' AND name='helper'`); got <= 0 {
		t.Errorf("subagent driver cost missing: %v", got)
	}
}

// <sid> sorts before <sid>.jsonl, so a walk reads the subagent files first. The
// session's folder is still its main file's launch folder, never a subagent's,
// on a first ingest and on the backfill of a legacy session.
func TestSubagentNeverSetsCwdHostRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "-p", "S1", "subagents", "agent-a.jsonl"), subCwdLine)
	writeFile(t, filepath.Join(root, "-p", "S1.jsonl"), mainCwdLine)
	s := openTemp(t)
	ingestRoot(s, root, nil, &IngestStats{})
	checkMainCwdWins(t, s)

	// The one-time backfill walks the same order.
	if _, err := s.db.Exec(`UPDATE ingest_state SET backfilled=0; DELETE FROM driver_rollup; DELETE FROM session_dim`); err != nil {
		t.Fatal(err)
	}
	ingestRoot(s, root, nil, &IngestStats{})
	checkMainCwdWins(t, s)
}

func TestSubagentNeverSetsCwdBoxRoot(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "boxstate", "A", ".claude", "projects", "-p")
	writeFile(t, filepath.Join(proj, "S1", "subagents", "agent-a.jsonl"), subCwdLine)
	writeFile(t, filepath.Join(proj, "S1.jsonl"), mainCwdLine)
	svc := boxSvc(t, base)
	if st, err := svc.Ingest(); err != nil || st.Errors != 0 || st.Files != 2 {
		t.Fatalf("st=%+v err=%v", st, err)
	}
	checkMainCwdWins(t, svc.store)
	if d, _ := readDim(t, svc.store, "S1", "-p"); d.runtime != "box" || d.rootKey != "A" {
		t.Errorf("subagent file lost the runtime: %+v", d)
	}

	if _, err := svc.store.db.Exec(`UPDATE ingest_state SET backfilled=0; DELETE FROM driver_rollup; DELETE FROM session_dim`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Ingest(); err != nil {
		t.Fatal(err)
	}
	checkMainCwdWins(t, svc.store)
}

func TestIsSubagentPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/r/-p/S1/subagents/agent-a.jsonl": true,
		"/r/-p/S1.jsonl":                   false,
		"/r/-p/subagents.jsonl":            false,
		"/subagents/-p/S1.jsonl":           false,
		"S1.jsonl":                         false,
	} {
		if got := isSubagentPath(p); got != want {
			t.Errorf("isSubagentPath(%q) = %v, want %v", p, got, want)
		}
	}
}
