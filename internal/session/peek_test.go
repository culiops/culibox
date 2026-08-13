package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParsePeekTailFindsLastAssistantText(t *testing.T) {
	tail := `{"message":{"role":"assistant","content":[{"type":"text","text":"first answer"}]}}
{"message":{"role":"user","content":"go on"}}
{"message":{"role":"assistant","content":[{"type":"text","text":"second answer"}]}}
`
	p, ok := parsePeekTail([]byte(tail))
	if !ok {
		t.Fatal("ok=false, want true")
	}
	if p.Text != "second answer" {
		t.Errorf("Text=%q want %q", p.Text, "second answer")
	}
	if p.Pending {
		t.Error("Pending=true, want false: the last turn ended on text")
	}
}

func TestParsePeekTailPendingWhenLastBlockIsToolUse(t *testing.T) {
	tail := `{"message":{"role":"assistant","content":[{"type":"text","text":"let me check"},{"type":"tool_use","name":"Bash"}]}}
`
	p, ok := parsePeekTail([]byte(tail))
	if !ok {
		t.Fatal("ok=false, want true")
	}
	if !p.Pending {
		t.Error("Pending=false, want true")
	}
	if p.Text != "let me check" {
		t.Errorf("Text=%q want %q", p.Text, "let me check")
	}
}

func TestParsePeekTailNotPendingOnceToolResultArrives(t *testing.T) {
	tail := `{"message":{"role":"assistant","content":[{"type":"text","text":"checking"},{"type":"tool_use","name":"Bash"}]}}
{"message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}
`
	p, _ := parsePeekTail([]byte(tail))
	if p.Pending {
		t.Error("Pending=true, want false: a tool_result followed the tool_use")
	}
}

func TestParsePeekTailSkipsCorruptLeadingLine(t *testing.T) {
	// A 256KiB tail almost always starts mid-record. That line must be skipped.
	tail := `role":"assistant","content":[{"type":"text","text":"truncated"}]}}
{"message":{"role":"assistant","content":[{"type":"text","text":"good"}]}}
`
	p, ok := parsePeekTail([]byte(tail))
	if !ok || p.Text != "good" {
		t.Errorf("Text=%q ok=%v want %q true", p.Text, ok, "good")
	}
}

func TestParsePeekTailEmpty(t *testing.T) {
	if _, ok := parsePeekTail(nil); ok {
		t.Error("ok=true on empty input, want false")
	}
	if _, ok := parsePeekTail([]byte("\n  \n")); ok {
		t.Error("ok=true on blank lines, want false")
	}
}

func TestParsePeekTailToolOnlyTurnHasNoText(t *testing.T) {
	tail := `{"message":{"role":"assistant","content":[{"type":"tool_use","name":"Read"}]}}
`
	p, ok := parsePeekTail([]byte(tail))
	if !ok {
		t.Fatal("ok=false, want true: a record WAS parsed, there is just no text")
	}
	if p.Text != "" {
		t.Errorf("Text=%q want empty", p.Text)
	}
	if !p.Pending {
		t.Error("Pending=false, want true")
	}
}

func TestSummaryLine(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "x"
	}
	cases := []struct{ in, want string }{
		{"", ""},
		{"   \n\t\n", ""},
		{"hello", "hello"},
		{"\n\n  first real line  \nsecond", "first real line"},
		{long, long[:summaryMax-1] + "…"},
		// Rune-safe: cutting a multi-byte character in half renders as a
		// replacement glyph, so truncation counts runes, not bytes.
		{"héllo", "héllo"},
	}
	for _, c := range cases {
		if got := summaryLine(c.in); got != c.want {
			t.Errorf("summaryLine(%.20q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestSummaryLineTruncatesByRune(t *testing.T) {
	in := ""
	for i := 0; i < 200; i++ {
		in += "é" // 2 bytes each
	}
	got := summaryLine(in)
	if r := []rune(got); len(r) != summaryMax {
		t.Errorf("len=%d runes want %d (%q)", len(r), summaryMax, got)
	}
}

// newPeekTestManager builds a Manager with one claude session whose transcript
// lives under home/.claude/projects/<slug>/. Returns the transcript path.
func newPeekTestManager(t *testing.T, cwd, body string) (*Manager, string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", projectSlug(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &Manager{
		home:     home,
		sessions: map[string]*Session{},
	}
	m.sessions["s1"] = &Session{
		ID: "s1", Agent: "claude", Cwd: cwd, Bin: "/usr/local/bin/claude",
		Created: time.Now().Add(-time.Hour),
	}
	return m, p
}

func TestPeekForReadsTranscript(t *testing.T) {
	body := `{"message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}` + "\n"
	m, _ := newPeekTestManager(t, "/tmp/proj", body)
	p, ok := m.PeekFor("s1")
	if !ok || p.Text != "done" {
		t.Fatalf("PeekFor = %+v, %v; want text %q, true", p, ok, "done")
	}
}

func TestPeekForRejectsNonClaudeBin(t *testing.T) {
	body := `{"message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}` + "\n"
	m, _ := newPeekTestManager(t, "/tmp/proj", body)
	m.sessions["s1"].Bin = "/usr/local/bin/codex"
	if _, ok := m.PeekFor("s1"); ok {
		t.Error("ok=true for a non-claude bin, want false")
	}
}

func TestPeekForUnknownSession(t *testing.T) {
	m, _ := newPeekTestManager(t, "/tmp/proj", "")
	if _, ok := m.PeekFor("nope"); ok {
		t.Error("ok=true for an unknown id, want false")
	}
}

func TestPeekForCachesUntilMtimeChanges(t *testing.T) {
	body := `{"message":{"role":"assistant","content":[{"type":"text","text":"first"}]}}` + "\n"
	m, path := newPeekTestManager(t, "/tmp/proj", body)
	if p, _ := m.PeekFor("s1"); p.Text != "first" {
		t.Fatalf("Text=%q want %q", p.Text, "first")
	}
	// Rewrite the content but pin the OLD mtime: a cache keyed on mtime must
	// still serve the first read.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	next := `{"message":{"role":"assistant","content":[{"type":"text","text":"second"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(next), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if p, _ := m.PeekFor("s1"); p.Text != "first" {
		t.Errorf("Text=%q want cached %q", p.Text, "first")
	}
	// Now advance mtime: the cache must miss and re-read.
	later := fi.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if p, _ := m.PeekFor("s1"); p.Text != "second" {
		t.Errorf("Text=%q want re-read %q", p.Text, "second")
	}
}

func TestListFillsSummaryWithoutDeadlock(t *testing.T) {
	body := `{"message":{"role":"assistant","content":[{"type":"text","text":"line one\nline two"}]}}` + "\n"
	m, _ := newPeekTestManager(t, "/tmp/proj", body)

	// A deadlock here hangs the whole test binary, so bound it.
	done := make(chan []Info, 1)
	go func() { done <- m.List() }()
	select {
	case out := <-done:
		if len(out) != 1 {
			t.Fatalf("len(List())=%d want 1", len(out))
		}
		if out[0].Summary != "line one" {
			t.Errorf("Summary=%q want %q", out[0].Summary, "line one")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("List() deadlocked: PeekFor takes m.mu, so it cannot be called while List holds it")
	}
}

func TestSessionMetaWithoutReplyableDefaultsToFalse(t *testing.T) {
	// A sidecar written before this feature has no "replyable" key. It must
	// decode to false: a session must never become repliable by surviving a
	// daemon restart.
	var md sessionMeta
	old := `{"agent":"claude","cwd":"/tmp/proj","created":"2026-08-01T00:00:00Z","runtime":"host"}`
	if err := json.Unmarshal([]byte(old), &md); err != nil {
		t.Fatal(err)
	}
	if md.Replyable {
		t.Error("Replyable=true from a pre-feature record, want false")
	}
}

func TestSetReplyableRoundTrips(t *testing.T) {
	m, _ := newPeekTestManager(t, "/tmp/proj", "")
	if m.List()[0].Replyable {
		t.Fatal("Replyable defaults to true, want false")
	}
	if !m.SetReplyable("s1", true) {
		t.Fatal("SetReplyable returned false for a live session")
	}
	if !m.List()[0].Replyable {
		t.Error("Replyable=false after SetReplyable(true)")
	}
	if !m.SetReplyable("s1", false) {
		t.Fatal("SetReplyable(false) returned false")
	}
	if m.List()[0].Replyable {
		t.Error("Replyable=true after SetReplyable(false)")
	}
	if m.SetReplyable("nope", true) {
		t.Error("SetReplyable returned true for an unknown id")
	}
}
