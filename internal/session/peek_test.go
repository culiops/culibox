package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func pendingTail(block string) []byte {
	return []byte(`{"message":{"role":"assistant","content":[{"type":"text","text":"checking"},` + block + `]}}` + "\n")
}

func TestParsePeekTailAsk(t *testing.T) {
	cases := []struct{ name, block, want string }{
		{"bash shows its command", `{"type":"tool_use","name":"Bash","input":{"command":"rm -rf build","description":"clean"}}`, "Bash(rm -rf build)"},
		{"edit shows its path", `{"type":"tool_use","name":"Edit","input":{"file_path":"internal/web/app.js","old_string":"a","new_string":"b"}}`, "Edit(internal/web/app.js)"},
		{"read shows its path", `{"type":"tool_use","name":"Read","input":{"file_path":"peek.go"}}`, "Read(peek.go)"},
		{"notebook shows its path", `{"type":"tool_use","name":"NotebookEdit","input":{"notebook_path":"a.ipynb"}}`, "NotebookEdit(a.ipynb)"},
		{"unknown tool shows the name only", `{"type":"tool_use","name":"WebFetch","input":{"url":"https://x"}}`, "WebFetch"},
		{"no input shows the name only", `{"type":"tool_use","name":"Bash"}`, "Bash"},
		{"collapses to one line", `{"type":"tool_use","name":"Bash","input":{"command":"cd /x &&\n  make\ttest"}}`, "Bash(cd /x && make test)"},
		{"control characters never survive", `{"type":"tool_use","name":"Bash","input":{"command":"echo \u001b[31mred\u0007"}}`, "Bash(echo [31mred)"},
		{"C1 control characters never survive", `{"type":"tool_use","name":"Bash","input":{"command":"echo \u009b31mred\u009d"}}`, "Bash(echo 31mred)"},
		{"unicode format characters never survive", `{"type":"tool_use","name":"Bash","input":{"command":"echo ​hi‮"}}`, "Bash(echo hi)"},
		{"bidi isolate characters never survive", `{"type":"tool_use","name":"Bash","input":{"command":"rm ⁦evil⁩.txt"}}`, "Bash(rm evil .txt)"},
	}
	for _, c := range cases {
		p, ok := parsePeekTail(pendingTail(c.block))
		if !ok || !p.Pending {
			t.Fatalf("%s: ok=%v pending=%v, want a pending peek", c.name, ok, p.Pending)
		}
		if p.Ask != c.want {
			t.Errorf("%s: Ask=%q want %q", c.name, p.Ask, c.want)
		}
	}
}

func TestParsePeekTailAskTruncatesByRune(t *testing.T) {
	long := strings.Repeat("é", 200)
	p, _ := parsePeekTail(pendingTail(`{"type":"tool_use","name":"Bash","input":{"command":"` + long + `"}}`))
	r := []rune(p.Ask)
	if len(r) != askMax || !strings.HasPrefix(p.Ask, "Bash(") || !strings.HasSuffix(p.Ask, "…") {
		t.Errorf("Ask=%q (%d runes), want %d runes starting Bash( and ending …", p.Ask, len(r), askMax)
	}
}

func TestParsePeekTailAskEmptyWhenNotPending(t *testing.T) {
	p, _ := parsePeekTail([]byte(`{"message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}` + "\n"))
	if p.Pending || p.Ask != "" {
		t.Errorf("Pending=%v Ask=%q for a finished turn, want false and empty", p.Pending, p.Ask)
	}
}

// Claude Code appends role-less bookkeeping records (cost-state, last-prompt,
// ai-title…) — measured after a prompt on 2.1.284. "Last record" must mean the
// last MESSAGE record, or one trailing line flips an ask into "your turn".
func TestParsePeekTailIgnoresRolelessTrailingRecords(t *testing.T) {
	tail := `{"message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}` + "\n" +
		`{"type":"cost-state","sessionId":"x","totalCostUSD":0.01}` + "\n" +
		`{"type":"last-prompt","lastPrompt":"run ls","sessionId":"x"}` + "\n"
	p, ok := parsePeekTail([]byte(tail))
	if !ok || !p.Pending || p.Ask != "Bash(ls)" {
		t.Errorf("ok=%v Pending=%v Ask=%q, want true/true/Bash(ls)", ok, p.Pending, p.Ask)
	}
}

// Controller ruling (D3): a stale, already-superseded tool_use earlier in the
// tail must never leak into Pending or Ask — only the true last role-bearing
// record (skipping role-less bookkeeping) decides them. Without the once-only
// sawLast gate scoped to the first role-bearing record found from the end, an
// older tool_use could otherwise resurrect Pending after the agent has already
// finished with plain text.
func TestParsePeekTailStaleOlderToolUseNeverOverwritesPending(t *testing.T) {
	tail := `{"message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"rm -rf build"}}]}}` + "\n" +
		`{"type":"cost-state","sessionId":"x","totalCostUSD":0.01}` + "\n" +
		`{"message":{"role":"assistant","content":[{"type":"text","text":"all done, nothing pending"}]}}` + "\n"
	p, ok := parsePeekTail([]byte(tail))
	if !ok || p.Pending || p.Ask != "" {
		t.Errorf("ok=%v Pending=%v Ask=%q, want ok=true Pending=false Ask=empty: the true last record is plain assistant text, an older stale tool_use must not resurrect Pending", ok, p.Pending, p.Ask)
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
		// Unicode format characters (zero-width, bidi overrides/isolates) are
		// not IsControl and must not survive into the sidebar/notification.
		{"hi​there", "hi there"},
		{"‮evil⁦hidden⁩‬", "evil hidden"},
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
	m.SetRepliesEnabled(true) // the reply tests exercise the feature; the off switch has its own tests
	m.sessions["s1"] = &Session{
		ID: "s1", Agent: "claude", Cwd: cwd, Bin: "/usr/local/bin/claude",
		Created: time.Now().Add(-time.Hour),
		// A claude that has been given a turn; zero lastActivityAt reads stale,
		// so Info() says "waiting" — what the reply tests assume.
		agentKind: true, submitted: true,
		// Bound to its transcript the way a session that has been seen live
		// is: by its persisted conversation id (session.jsonl above). Usage,
		// summary and peek read only a bound transcript, never a cwd guess.
		claudeSessionID: "session",
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
	// Rewrite the content, pin the OLD mtime, but keep the SAME byte length:
	// with mtime and size both unchanged the cache key is a hit and the
	// stale parse is served — the one shape this cache still cannot see
	// through (a coarse mtime AND a same-size edit, both at once).
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	sameSize := `{"message":{"role":"assistant","content":[{"type":"text","text":"fifth"}]}}` + "\n"
	if len(sameSize) != len(body) {
		t.Fatalf("test setup: sameSize must match body's byte length (%d vs %d)", len(sameSize), len(body))
	}
	if err := os.WriteFile(path, []byte(sameSize), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if p, _ := m.PeekFor("s1"); p.Text != "first" {
		t.Errorf("Text=%q want cached %q (same mtime, same size)", p.Text, "first")
	}
	// Rewrite again, still at the SAME mtime, but this time with a DIFFERENT
	// byte length — the coarse-mtime scenario this cache key must catch
	// (fix, control-actions final wave). Size differing must miss the cache.
	next := `{"message":{"role":"assistant","content":[{"type":"text","text":"second, and longer"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(next), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if p, _ := m.PeekFor("s1"); p.Text != "second, and longer" {
		t.Errorf("Text=%q want re-read %q (same mtime, size changed)", p.Text, "second, and longer")
	}
	// Now advance mtime too: the cache must miss and re-read.
	later := fi.ModTime().Add(2 * time.Second)
	final := `{"message":{"role":"assistant","content":[{"type":"text","text":"third"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(final), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if p, _ := m.PeekFor("s1"); p.Text != "third" {
		t.Errorf("Text=%q want re-read %q", p.Text, "third")
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

func TestCanReply(t *testing.T) {
	cases := []struct {
		replyable bool
		activity  string
		pending   bool
		wantOK    bool
		wantWhy   string
	}{
		{true, "waiting", false, true, ""},
		{false, "waiting", false, false, "not_replyable"},
		{true, "working", false, false, "not_waiting"},
		{true, "done", false, false, "not_waiting"},
		{true, "crashed", false, false, "not_waiting"},
		{true, "detached", false, false, "not_waiting"},
		{true, "waiting", true, false, "pending"},
		// pending is checked BEFORE activity. A session sitting on a permission
		// prompt redraws it, which keeps resetting the idle timer, so it reads
		// as `working` — the one case the pending branch exists for was also
		// the case activity-first never reached. The refusal is identical
		// either way; only the words the operator sees change.
		{true, "working", true, false, "pending"},
		// replyable is checked first so the most actionable reason wins.
		{false, "working", true, false, "not_replyable"},
	}
	for _, c := range cases {
		ok, why := canReply(c.replyable, c.activity, c.pending)
		if ok != c.wantOK || why != c.wantWhy {
			t.Errorf("canReply(%v,%q,%v)=(%v,%q) want (%v,%q)",
				c.replyable, c.activity, c.pending, ok, why, c.wantOK, c.wantWhy)
		}
	}
}

func TestValidReplyText(t *testing.T) {
	if err := validReplyText("do the thing"); err != nil {
		t.Errorf("rejected a plain line: %v", err)
	}
	for _, bad := range []string{"", "   ", "two\nlines", "carriage\rreturn", "bell\a", "esc\x1b[31m", "tab\there", "del\x7f"} {
		if err := validReplyText(bad); err == nil {
			t.Errorf("accepted %q, want an error", bad)
		}
	}
	// A long-but-clean line is fine; only shape is checked, not length.
	long := strings.Repeat("a", 4000)
	if err := validReplyText(long); err != nil {
		t.Errorf("rejected a long clean line: %v", err)
	}
}

func TestSendMessageRefusesWhenGuardsFail(t *testing.T) {
	body := `{"message":{"role":"assistant","content":[{"type":"text","text":"which file?"}]}}` + "\n"
	m, _ := newPeekTestManager(t, "/tmp/proj", body)

	// Not opted in.
	if err := m.SendMessage("s1", "hello"); err != ErrNotReplyable {
		t.Errorf("err=%v want ErrNotReplyable", err)
	}
	// Unknown session.
	if err := m.SendMessage("nope", "hello"); err != ErrNoSession {
		t.Errorf("err=%v want ErrNoSession", err)
	}
	// Bad text is rejected before any guard, so it cannot leak session state.
	if err := m.SendMessage("s1", "two\nlines"); err != ErrBadText {
		t.Errorf("err=%v want ErrBadText", err)
	}
}

func TestSendMessageRefusesWhenPending(t *testing.T) {
	body := `{"message":{"role":"assistant","content":[{"type":"text","text":"checking"},{"type":"tool_use","name":"Bash"}]}}` + "\n"
	m, _ := newPeekTestManager(t, "/tmp/proj", body)
	if err := m.SetReplyable("s1", true); err != nil {
		t.Fatal(err)
	}
	if err := m.SendMessage("s1", "yes"); err != ErrPending {
		t.Errorf("err=%v want ErrPending", err)
	}
}

// The pending guard's ground truth is the transcript. When PeekFor misses there
// IS no ground truth — and a miss used to yield the zero Peek, whose
// pending=false PASSED the guard. Fail closed instead.
func TestSendMessageFailsClosedWhenTheTranscriptIsUnreadable(t *testing.T) {
	body := `{"message":{"role":"assistant","content":[{"type":"text","text":"which file?"}]}}` + "\n"
	m, path := newPeekTestManager(t, "/tmp/proj", body)
	if err := m.SetReplyable("s1", true); err != nil {
		t.Fatal(err)
	}
	// Make the session look maximally sendable to every OTHER guard: opted in,
	// idle long enough that activityOf reads `waiting` (a zero lastActivityAt
	// already does), and holding a real writable fd so the send can complete.
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devnull.Close() })
	m.sessions["s1"].ptmx = devnull
	if err := m.SendMessage("s1", "the second one"); err != nil {
		t.Fatalf("precondition: a readable transcript must send, got %v", err)
	}
	// Now remove the transcript: PeekFor misses and the third guard has nothing
	// to test. That must refuse, not wave the message through.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := m.SendMessage("s1", "still there?"); err != ErrNoTranscript {
		t.Errorf("err=%v want ErrNoTranscript", err)
	}
}

// A raw $SHELL session has no transcript, so every transcript-derived guard is
// vacuous on it and the opt-in would be the only thing standing between an API
// call and a line typed into a shell. isClaudeBin is the seam; enforce it in
// the session layer so no caller can reach the write without crossing it.
func TestSendMessageRefusesANonClaudeSession(t *testing.T) {
	body := `{"message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}` + "\n"
	m, _ := newPeekTestManager(t, "/tmp/proj", body)
	if err := m.SetReplyable("s1", true); err != nil {
		t.Fatal(err)
	}
	// A real writable fd, so that without the gate this test reports err=nil —
	// i.e. the line was actually typed at the shell — rather than an incidental
	// error from a nil PTY.
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devnull.Close() })
	m.sessions["s1"].ptmx = devnull
	m.sessions["s1"].Bin = "/bin/zsh"
	if err := m.SendMessage("s1", "rm -rf /"); err != ErrNotClaude {
		t.Errorf("err=%v want ErrNotClaude", err)
	}
}

func TestSetReplyableRoundTrips(t *testing.T) {
	m, _ := newPeekTestManager(t, "/tmp/proj", "")
	if m.List()[0].Replyable {
		t.Fatal("Replyable defaults to true, want false")
	}
	if err := m.SetReplyable("s1", true); err != nil {
		t.Fatalf("SetReplyable(true) on a live claude session: %v", err)
	}
	if !m.List()[0].Replyable {
		t.Error("Replyable=false after SetReplyable(true)")
	}
	if err := m.SetReplyable("s1", false); err != nil {
		t.Fatalf("SetReplyable(false): %v", err)
	}
	if m.List()[0].Replyable {
		t.Error("Replyable=true after SetReplyable(false)")
	}
	if err := m.SetReplyable("nope", true); err != ErrNoSession {
		t.Errorf("err=%v for an unknown id, want ErrNoSession", err)
	}
}

// The opt-in is the whole control on a path that types into a live terminal.
// It must be scoped by the same isClaudeBin seam SendMessage enforces, in the
// session layer — where no future caller can go around it — and not by which
// rows the sidebar happens to draw a checkbox on.
func TestSetReplyableRefusesToGrantOnANonClaudeSession(t *testing.T) {
	m, _ := newPeekTestManager(t, "/tmp/proj", "")
	m.sessions["s1"].Bin = "/bin/zsh"
	if err := m.SetReplyable("s1", true); err != ErrNotClaude {
		t.Fatalf("err=%v want ErrNotClaude", err)
	}
	if m.List()[0].Replyable {
		t.Error("Replyable=true on a shell session after a refused grant")
	}
}

// Revoking is never gated: a session that holds the flag (restored from a
// sidecar, or whose Bin resolved differently later) must always be able to
// give it up, or the UI's only off switch stops working on exactly the
// sessions an operator most wants to shut.
func TestSetReplyableAlwaysAllowsRevoking(t *testing.T) {
	m, _ := newPeekTestManager(t, "/tmp/proj", "")
	if err := m.SetReplyable("s1", true); err != nil {
		t.Fatal(err)
	}
	m.sessions["s1"].Bin = "/bin/zsh" // no longer resolves as claude
	if err := m.SetReplyable("s1", false); err != nil {
		t.Fatalf("revoke refused: %v", err)
	}
	if m.List()[0].Replyable {
		t.Error("Replyable=true after a revoke")
	}
}

func TestListReportsClaude(t *testing.T) {
	m, _ := newPeekTestManager(t, "/tmp/proj", "")
	if !m.List()[0].Claude {
		t.Error("Claude=false for a claude bin")
	}
	m.sessions["s1"].Bin = "/bin/zsh"
	if m.List()[0].Claude {
		t.Error("Claude=true for a shell bin")
	}
}

// The kill-switch (config replies_enabled, Manager.SetRepliesEnabled) refuses a
// GRANT but never a revoke, so an operator can always take an opt-in back.
func TestSetReplyableRefusedWhenRepliesDisabled(t *testing.T) {
	m, _ := newPeekTestManager(t, "/tmp/proj", "")
	m.SetRepliesEnabled(false)
	if err := m.SetReplyable("s1", true); !errors.Is(err, ErrRepliesDisabled) {
		t.Fatalf("SetReplyable(true) err=%v want ErrRepliesDisabled", err)
	}
	if m.List()[0].Replyable {
		t.Error("Replyable=true after a refused grant")
	}
	if err := m.SetReplyable("s1", false); err != nil {
		t.Errorf("SetReplyable(false) err=%v want nil: revoking must always work", err)
	}
}

// replyable is forced true on the struct (a stale state) so the test proves
// SendMessage refuses on the switch itself, not via the replyable invariant.
func TestSendMessageRefusedWhenRepliesDisabled(t *testing.T) {
	body := `{"message":{"role":"assistant","content":[{"type":"text","text":"which file?"}]}}` + "\n"
	m, _ := newPeekTestManager(t, "/tmp/proj", body)
	m.SetRepliesEnabled(false)
	s := m.sessions["s1"]
	s.mu.Lock()
	s.replyable = true
	s.mu.Unlock()
	r := pipeTo(t, s)
	if err := m.SendMessage("s1", "hi"); !errors.Is(err, ErrRepliesDisabled) {
		t.Fatalf("err=%v want ErrRepliesDisabled", err)
	}
	if got := written(t, r); got != "" {
		t.Errorf("PTY received %q, want nothing", got)
	}
	// Control: the same state with the switch on does reach the PTY, so the
	// refusal above is the switch and not some other guard.
	m.SetRepliesEnabled(true)
	if err := m.SendMessage("s1", "hi"); err != nil {
		t.Fatalf("switch on: %v", err)
	}
	if got := written(t, r); got != "hi\r" {
		t.Errorf("switch on: PTY received %q want %q", got, "hi\r")
	}
}

// fakeCost is a CostSource that records what it was asked and prices every id
// at a flat rate, so a test can tell "summed" from "not asked".
type fakeCost struct {
	mu    sync.Mutex
	calls [][]string
	per   float64
}

func (f *fakeCost) CostOf(sids []string) float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string(nil), sids...))
	return f.per * float64(len(sids))
}

func TestListCostOnlyWhenComplete(t *testing.T) {
	m, _ := newPeekTestManager(t, "/tmp/proj", "")
	add := func(id, bin string, ids []string, complete bool) {
		m.sessions[id] = &Session{
			ID: id, Agent: "claude", Bin: bin, Created: time.Now().Add(-time.Hour),
			claudeSessionIDs: ids, costComplete: complete,
		}
	}
	const claude = "/usr/local/bin/claude"
	add("complete", claude, []string{"c1", "c2"}, true)
	add("zero", claude, nil, true)
	add("one", claude, []string{"o1"}, true)
	add("partial", claude, []string{"p1"}, false)
	add("codex", "/usr/local/bin/codex", []string{"x1"}, true)
	delete(m.sessions, "s1")

	cost := func(out []Info, id string) *float64 {
		for _, i := range out {
			if i.ID == id {
				return i.CostUSD
			}
		}
		t.Fatalf("session %q not listed", id)
		return nil
	}

	// No cost source: never present.
	for _, id := range []string{"complete", "zero", "one", "partial", "codex"} {
		if c := cost(m.List(), id); c != nil {
			t.Errorf("no source: %s cost_usd=%v, want absent", id, *c)
		}
	}

	fc := &fakeCost{per: 1.5}
	m.SetCostSource(fc)
	out := m.List()
	if c := cost(out, "complete"); c == nil || *c != 3.0 {
		t.Errorf("complete claude session: cost_usd=%v, want 3", c)
	}
	// Complete but no conversation recorded: we never saw one, so a $0.00 would
	// be a guess (the registry may never validate). Absent.
	if c := cost(out, "zero"); c != nil {
		t.Errorf("complete, no ids: cost_usd=%v, want absent", *c)
	}
	// One known conversation with no ingested rows yet: a real $0.00 (lag).
	if c := cost(out, "one"); c == nil || *c != 1.5 {
		t.Errorf("complete, one id: cost_usd=%v, want 1.5 present", c)
	}
	if c := cost(out, "partial"); c != nil {
		t.Errorf("incomplete history: cost_usd=%v, want absent", *c)
	}
	if c := cost(out, "codex"); c != nil {
		t.Errorf("non-claude session: cost_usd=%v, want absent", *c)
	}
	for _, call := range fc.calls {
		for _, id := range call {
			if id == "p1" || id == "x1" {
				t.Errorf("CostOf was asked about %q, which must not be priced", id)
			}
		}
	}

	// One id with no ingested rows prices to 0: present, and exactly 0.
	m.SetCostSource(&fakeCost{per: 0})
	if c := cost(m.List(), "one"); c == nil || *c != 0 {
		t.Errorf("one id, no ingested rows: cost_usd=%v, want 0 present", c)
	}
	m.SetCostSource(fc)

	// Absent means the key is missing from the JSON; zero means "cost_usd":0.
	for _, i := range out {
		raw, _ := json.Marshal(i)
		has := strings.Contains(string(raw), `"cost_usd"`)
		if want := i.ID == "complete" || i.ID == "one"; has != want {
			t.Errorf("%s: cost_usd key present=%v, want %v (%s)", i.ID, has, want, raw)
		}
	}
}

// A typed nil (a nil *T stored in the interface) must read as "no source".
func TestSetCostSourceTypedNil(t *testing.T) {
	m, _ := newPeekTestManager(t, "/tmp/proj", "")
	m.sessions["s1"].costComplete = true
	var nilSvc *fakeCost
	m.SetCostSource(nilSvc)
	if c := m.List()[0].CostUSD; c != nil {
		t.Errorf("typed-nil source: cost_usd=%v, want absent (and no panic)", *c)
	}
}
