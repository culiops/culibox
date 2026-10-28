package analytics

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseTranscriptSkipsOverlongLine(t *testing.T) {
	long := `{"sessionId":"S1","timestamp":"2026-08-05T10:00:03Z","message":{"role":"assistant","model":"m1","usage":{"input_tokens":999},"x":"` +
		strings.Repeat("a", 17<<20) + `"}}` + "\n"
	in := line1 + long + line2
	p, sid, consumed, err := ParseTranscript(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if sid != "S1" {
		t.Errorf("sid=%q", sid)
	}
	if consumed != int64(len(in)) {
		t.Errorf("consumed=%d want %d (a terminated over-long line must be consumed)", consumed, len(in))
	}
	got := p.Tokens[TokKey{Day: "2026-08-05", Model: "m1"}]
	if got == nil || got.Input != 100 || got.Output != 20 || got.Prompts != 1 {
		t.Fatalf("long line parsed or normal lines lost: %+v", got)
	}
}

func TestParseTranscriptOverlongUnterminatedTailConsumed(t *testing.T) {
	in := line1 + strings.Repeat("a", 17<<20)
	_, _, consumed, over, _, err := parseTranscript(strings.NewReader(in), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !over || consumed != int64(len(in)) {
		t.Fatalf("over=%v consumed=%d want true/%d", over, consumed, len(in))
	}
	// A short unterminated tail is still left for the next run.
	_, _, consumed, over, _, _ = parseTranscript(strings.NewReader(line1+`{"a":`), nil)
	if over || consumed != int64(len(line1)) {
		t.Fatalf("short tail: over=%v consumed=%d want false/%d", over, consumed, len(line1))
	}
}

// A box can make a sparse, newline-free file of any size. Ingest must stay
// bounded in memory, advance to EOF, and not re-read it on the next pass.
func TestIngestSparseNoNewlineBounded(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "-home-proj", "S1.jsonl")
	writeFile(t, path, "")
	const size = 1 << 30
	if err := os.Truncate(path, size); err != nil {
		t.Fatal(err)
	}
	s := openTemp(t)

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	var st IngestStats
	ingestRoot(s, root, &st)
	runtime.ReadMemStats(&after)
	if d := after.TotalAlloc - before.TotalAlloc; d > 64<<20 {
		t.Errorf("ingest allocated %d MiB for a newline-free file, want < 64", d>>20)
	}
	if st.Errors != 0 {
		t.Errorf("errors=%d", st.Errors)
	}
	_, off, _, _, err := s.State(path)
	if err != nil {
		t.Fatal(err)
	}
	if off != size {
		t.Fatalf("offset=%d want %d", off, size)
	}

	st = IngestStats{}
	ingestRoot(s, root, &st)
	if st.BytesRead != 0 || st.Files != 0 {
		t.Fatalf("second pass re-read the file: %+v", st)
	}
}
