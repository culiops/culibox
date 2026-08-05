package analytics

import (
	"os"
	"path/filepath"
	"testing"
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
	_, off, _, _ := s.State(path)
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
