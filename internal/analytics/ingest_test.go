package analytics

import (
	"os"
	"path/filepath"
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
	ingestRoot(s, root, &st)
	if st.Files != 1 {
		t.Fatalf("first pass files=%d want 1", st.Files)
	}
	rows, _ := s.ByDayModel("2026-08-01", "2026-08-31")
	if len(rows) != 1 || rows[0].Input != 100 || rows[0].Output != 20 || rows[0].Prompts != 1 {
		t.Fatalf("after first pass: %+v", rows)
	}

	// Re-ingest with no change → nothing new.
	st = IngestStats{}
	ingestRoot(s, root, &st)
	if st.Files != 0 || st.BytesRead != 0 {
		t.Fatalf("no-change pass read data: %+v", st)
	}
	rows, _ = s.ByDayModel("2026-08-01", "2026-08-31")
	if rows[0].Input != 100 {
		t.Fatalf("no-change pass double-counted: %+v", rows[0])
	}

	// Append one line → only the new bytes are read and added.
	appendFile(t, path, line3)
	st = IngestStats{}
	ingestRoot(s, root, &st)
	if st.Files != 1 || st.BytesRead != int64(len(line3)) {
		t.Fatalf("append pass: files=%d bytes=%d want 1/%d", st.Files, st.BytesRead, len(line3))
	}
	rows, _ = s.ByDayModel("2026-08-01", "2026-08-31")
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
	ingestRoot(s, root, &st)

	// Truncate to fewer bytes than we already consumed.
	writeFile(t, path, line1)
	st = IngestStats{}
	ingestRoot(s, root, &st)

	rows, _ := s.ByDayModel("2026-08-01", "2026-08-31")
	if rows[0].Input != 100 || rows[0].Output != 20 {
		t.Fatalf("shrink double-counted: %+v", rows[0])
	}
	_, off, _, _, _ := s.State(path)
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
	ingestRoot(s, root, &st)

	rows, err := s.Sessions("2026-08-01", "2026-08-31", 10)
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
		ingestRoot(s, root, &st)
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
	ingestRoot(s, root, &st)
	if st.Files != 0 || st.BytesRead != 0 || st.Errors != 1 {
		t.Fatalf("symlink not refused: %+v", st)
	}
	rows, _ := s.ByDayModel("2026-08-01", "2026-08-31")
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
	ingestRoot(s, root, &st)

	appendFile(t, path, rec("msg_A", "2026-08-05T10:00:03Z")+rec("msg_B", "2026-08-05T10:00:04Z"))
	st = IngestStats{}
	ingestRoot(s, root, &st)

	rows, err := s.ByDayModel("2026-08-01", "2026-08-31")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Input != 20 || rows[0].Output != 10 {
		t.Fatalf("rows = %+v, want one row in=20 out=10 (msg_A once, msg_B once)", rows)
	}
	_, _, _, seen, err := s.State(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "msg_A" || seen[1] != "msg_B" {
		t.Fatalf("seen = %v want [msg_A msg_B]", seen)
	}
}
