package session

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// pipeTo points s's PTY at a pipe and returns its read end.
func pipeTo(t *testing.T, s *Session) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	s.ptmx = w
	return r
}

// written drains what has been written so far.
func written(t *testing.T, r *os.File) string {
	t.Helper()
	var out []byte
	buf := make([]byte, 64)
	for {
		_ = r.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		n, err := r.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			return string(out)
		}
	}
}

func hexSHA(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }

func TestInterruptPicksTheKeyServerSide(t *testing.T) {
	for _, c := range []struct{ bin, want string }{
		{"/usr/bin/claude", "\x1b"}, {"codex", "\x1b"}, {"/bin/bash", "\x03"},
	} {
		s := &Session{ID: "a", Bin: c.bin, status: statusLive}
		r := pipeTo(t, s)
		m := &Manager{sessions: map[string]*Session{"a": s}}
		if err := m.Interrupt("a"); err != nil {
			t.Fatalf("%s: %v", c.bin, err)
		}
		if got := written(t, r); got != c.want {
			t.Errorf("%s: wrote %q want %q", c.bin, got, c.want)
		}
		if submittedOf(s) {
			t.Errorf("%s: an interrupt marked the session submitted", c.bin)
		}
	}
}

func TestInterruptRefusesWhatIsNotRunning(t *testing.T) {
	for _, st := range []string{statusExited, statusDetached} {
		s := &Session{ID: "a", Bin: "/usr/bin/claude", status: st}
		r := pipeTo(t, s)
		m := &Manager{sessions: map[string]*Session{"a": s}}
		if err := m.Interrupt("a"); !errors.Is(err, ErrNotLive) {
			t.Errorf("%s: err=%v want ErrNotLive", st, err)
		}
		if got := written(t, r); got != "" {
			t.Errorf("%s: wrote %q", st, got)
		}
	}
	if err := (&Manager{sessions: map[string]*Session{}}).Interrupt("nope"); !errors.Is(err, ErrNoSession) {
		t.Errorf("unknown: err=%v want ErrNoSession", err)
	}
}

// An unresolved Bin ("") must never fall through to Ctrl+C: it may be a
// claude session whose bin just hasn't been backfilled yet (ResolveBins runs
// post-reconcile), and two Ctrl+C in a row can exit a claude session rather
// than merely interrupt it. Refuse instead of guessing, and write nothing.
func TestInterruptRefusesAnUnresolvedBin(t *testing.T) {
	s := &Session{ID: "a", Bin: "", status: statusLive}
	r := pipeTo(t, s)
	m := &Manager{sessions: map[string]*Session{"a": s}}
	if err := m.Interrupt("a"); !errors.Is(err, ErrBinUnresolved) {
		t.Fatalf("err=%v want ErrBinUnresolved", err)
	}
	if got := written(t, r); got != "" {
		t.Fatalf("wrote %q want nothing", got)
	}
}

// A shell's Ctrl+C carries none of the agent Esc / rewind-menu hazard, so it
// must never be throttled by escGate: two back-to-back interrupts both land.
func TestInterruptCtrlCIsNeverDebounced(t *testing.T) {
	s := &Session{ID: "a", Bin: "/bin/bash", status: statusLive}
	r := pipeTo(t, s)
	m := &Manager{sessions: map[string]*Session{"a": s}}
	if err := m.Interrupt("a"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := m.Interrupt("a"); err != nil {
		t.Fatalf("second (immediate): %v", err)
	}
	if got := written(t, r); got != "\x03\x03" {
		t.Fatalf("wrote %q want two Ctrl+C", got)
	}
}

const s1Ask = "Bash(touch /tmp/a)"

// answerManager: s1 is a live, replyable claude sitting on one Bash prompt,
// registry "waiting". Returns the manager, the PTY reader and the home dir.
func answerManager(t *testing.T) (*Manager, *os.File, string) {
	t.Helper()
	m, home := newLocatorManager(t)
	writeTxLines(t, home, locCwd, "aaaa-1", time.Now(), txUser("go"), txRec("assistant", txThinking()), txRec("assistant", txBash("touch /tmp/a")))
	writeReg(t, home, 101, regJSON(101, "aaaa-1", "5000", "waiting"))
	s := m.sessions["s1"]
	s.replyable = true
	s.status = statusLive
	return m, pipeTo(t, s), home
}

func TestAnswerWritesExactlyOneKey(t *testing.T) {
	for _, c := range []struct{ choice, want string }{{"yes", "1"}, {"no", "\x1b"}} {
		m, r, _ := answerManager(t)
		if err := m.Answer("s1", c.choice, hexSHA(s1Ask)); err != nil {
			t.Fatalf("%s: %v", c.choice, err)
		}
		if got := written(t, r); got != c.want {
			t.Errorf("%s: wrote %q want exactly %q (never a \\r)", c.choice, got, c.want)
		}
	}
}

func TestAnswerGates(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(t *testing.T, m *Manager, home string)
		choice string
		sha    string
		want   error
	}{
		{"bad choice", nil, "always", hexSHA(s1Ask), ErrBadChoice},
		{"not claude", func(t *testing.T, m *Manager, _ string) { m.sessions["s1"].Bin = "/bin/bash" }, "yes", hexSHA(s1Ask), ErrNotClaude},
		{"not replyable", func(t *testing.T, m *Manager, _ string) { m.sessions["s1"].replyable = false }, "yes", hexSHA(s1Ask), ErrNotReplyable},
		{"registry not blocked", func(t *testing.T, _ *Manager, home string) {
			writeReg(t, home, 101, regJSON(101, "aaaa-1", "5000", "idle"))
		}, "yes", hexSHA(s1Ask), ErrNotBlocked},
		// exited and detached are both caught by the live gate now, same as
		// Interrupt — before Answer ever reaches the registry/AskFull checks.
		{"exited", func(t *testing.T, m *Manager, _ string) { m.sessions["s1"].status = statusExited }, "no", hexSHA(s1Ask), ErrNotLive},
		{"detached", func(t *testing.T, m *Manager, _ string) { m.sessions["s1"].status = statusDetached }, "no", hexSHA(s1Ask), ErrNotLive},
		{"two calls pending", func(t *testing.T, _ *Manager, home string) {
			// Real parallel tool calls share ONE message.id across separate
			// records (probe E: one assistant record per tool_use, same
			// message.id) — txRecID, not plain txRec's fresh id each call:
			// the Messages API requires every tool_use resolved before the
			// next assistant message, so two DIFFERENT message.ids each
			// left unresolved isn't a transcript shape that can occur.
			writeTxLines(t, home, locCwd, "aaaa-1", time.Now().Add(2*time.Second), txUser("go"),
				txRecID("assistant", "msg_parallel", txBash("touch /tmp/e1")), txRecID("assistant", "msg_parallel", txBash("touch /tmp/e2")))
		}, "yes", hexSHA("Bash(touch /tmp/e2)"), ErrPromptChanged},
		{"hash mismatch", nil, "yes", hexSHA("Bash(touch /tmp/other)"), ErrPromptChanged},
		// The p.AskFull == "" branch of the gate, exercised directly (an
		// empty-string hash — hexSHA("") — so a hash-mismatch short-circuit
		// could never be mistaken for covering this branch).
		{"edit pending has no AskFull (not in askableArgs)", func(t *testing.T, _ *Manager, home string) {
			writeTxLines(t, home, locCwd, "aaaa-1", time.Now().Add(2*time.Second), txUser("go"),
				txRec("assistant", txToolUse("Edit", "file_path", "/tmp/x", "toolu_edit1")))
		}, "yes", hexSHA(""), ErrPromptChanged},
		{"over-cap bash has no AskFull", func(t *testing.T, _ *Manager, home string) {
			writeTxLines(t, home, locCwd, "aaaa-1", time.Now().Add(2*time.Second), txUser("go"),
				txRec("assistant", txBash("echo "+strings.Repeat("y", askFullMax))))
		}, "yes", hexSHA(""), ErrPromptChanged},
		{"two calls pending has no AskFull", func(t *testing.T, _ *Manager, home string) {
			writeTxLines(t, home, locCwd, "aaaa-1", time.Now().Add(2*time.Second), txUser("go"),
				txRecID("assistant", "msg_parallel2", txBash("touch /tmp/g1")), txRecID("assistant", "msg_parallel2", txBash("touch /tmp/g2")))
		}, "yes", hexSHA(""), ErrPromptChanged},
		// !havePeek: the registry says waiting, but the transcript it points
		// at cannot be read (e.g. removed between the registry write and the
		// read). Refused exactly like a real prompt-changed case, never
		// treated as "not blocked".
		{"transcript missing though registry is waiting", func(t *testing.T, _ *Manager, home string) {
			p := filepath.Join(home, ".claude", "projects", projectSlug(locCwd), "aaaa-1.jsonl")
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
		}, "yes", hexSHA(s1Ask), ErrPromptChanged},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, r, home := answerManager(t)
			if c.setup != nil {
				c.setup(t, m, home)
			}
			if err := m.Answer("s1", c.choice, c.sha); !errors.Is(err, c.want) {
				t.Fatalf("err=%v want %v", err, c.want)
			}
			if got := written(t, r); got != "" {
				t.Fatalf("a refused answer still wrote %q", got)
			}
		})
	}
	m, _, _ := answerManager(t)
	if err := m.Answer("nope", "yes", hexSHA(s1Ask)); !errors.Is(err, ErrNoSession) {
		t.Fatalf("unknown: err=%v want ErrNoSession", err)
	}
}

// The prompt never moves between these two calls (same transcript, same
// mtime): AskFull is identical both times, so only the FIRST call may write.
// The second is refused by the lastAnswered memo, not by a changed prompt.
func TestAnswerRefusesARepeatOnAnUnchangedPrompt(t *testing.T) {
	m, r, _ := answerManager(t)
	if err := m.Answer("s1", "yes", hexSHA(s1Ask)); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := m.Answer("s1", "yes", hexSHA(s1Ask)); !errors.Is(err, ErrPromptChanged) {
		t.Fatalf("second: err=%v want ErrPromptChanged", err)
	}
	if got := written(t, r); got != "1" {
		t.Fatalf("wrote %q want exactly one \"1\" total", got)
	}
}

// Regression: Claude Code keeps appending role-less bookkeeping records
// (last-prompt, mode, ai-title, permission-mode, …) to the transcript while
// a call sits unresolved. parsePeekTailCut skips them — AskFull/askID stay
// the same — but they still move the file's mtime and size. The memo must
// still recognize this as the SAME prompt (real ids are keyed on path+id
// alone) and refuse the second Answer; before this fix it did not, and a
// stray second "1" reached the PTY.
func TestAnswerMemoSurvivesABookkeepingAppend(t *testing.T) {
	m, home := newLocatorManager(t)
	const askID = "toolu_fixed_1"
	askRec := txRecID("assistant", "msg_fixed", txToolUse("Bash", "command", "touch /tmp/a", askID))
	writeTxLines(t, home, locCwd, "aaaa-1", time.Now(), txUser("go"), askRec)
	writeReg(t, home, 101, regJSON(101, "aaaa-1", "5000", "waiting"))
	s := m.sessions["s1"]
	s.replyable = true
	s.status = statusLive
	r := pipeTo(t, s)

	if err := m.Answer("s1", "yes", hexSHA("Bash(touch /tmp/a)")); err != nil {
		t.Fatalf("first: %v", err)
	}

	// Same two lines, same askRec/id — plus a bookkeeping record with no
	// "message" at all (Message.Role == "" is skipped by parsePeekTailCut) —
	// and a strictly newer mtime, so the file's identity moves but the call
	// does not. Registry still "waiting".
	writeTxLines(t, home, locCwd, "aaaa-1", time.Now().Add(2*time.Second), txUser("go"), askRec,
		`{"type":"last-prompt","prompt":"go"}`)

	if err := m.Answer("s1", "yes", hexSHA("Bash(touch /tmp/a)")); !errors.Is(err, ErrPromptChanged) {
		t.Fatalf("second: err=%v want ErrPromptChanged", err)
	}
	if got := written(t, r); got != "1" {
		t.Fatalf("wrote %q want exactly one \"1\" total", got)
	}
}

// The positive mirror of TestAnswerMemoSurvivesABookkeepingAppend: a brand
// NEW tool_use id — a different call Claude Code happens to have rendered
// with the identical text — must be answerable even though AskFull's string
// is byte-identical to the one already recorded. The memo is keyed on
// path+id, never on the rendered text.
func TestAnswerMemoAllowsANewToolUseIDWithTheSameCommandText(t *testing.T) {
	m, home := newLocatorManager(t)
	first := txRecID("assistant", "msg_a", txToolUse("Bash", "command", "touch /tmp/a", "toolu_first"))
	writeTxLines(t, home, locCwd, "aaaa-1", time.Now(), txUser("go"), first)
	writeReg(t, home, 101, regJSON(101, "aaaa-1", "5000", "waiting"))
	s := m.sessions["s1"]
	s.replyable = true
	s.status = statusLive
	r := pipeTo(t, s)

	if err := m.Answer("s1", "yes", hexSHA(s1Ask)); err != nil {
		t.Fatalf("first: %v", err)
	}

	// A second, distinct call (a fresh tool_use id) that happens to run the
	// exact same command — the prior call resolves (a tool_result), and this
	// new one is the tail's own pending call.
	second := txRecID("assistant", "msg_b", txToolUse("Bash", "command", "touch /tmp/a", "toolu_second"))
	writeTxLines(t, home, locCwd, "aaaa-1", time.Now().Add(2*time.Second), txUser("go"), first, txResult("toolu_first"), second)

	if err := m.Answer("s1", "yes", hexSHA(s1Ask)); err != nil {
		t.Fatalf("second (new tool_use id, same command text): %v", err)
	}
	if got := written(t, r); got != "11" {
		t.Fatalf("wrote %q want two \"1\"s (both distinct calls answered)", got)
	}
}

// The positive mirror for the transcript path half of the memo key: /clear
// starts a NEW sessionId, i.e. a brand-new transcript file. Even a pending
// call sharing the exact same tool_use id (a coincidence — ids are unique
// per file, never reused, but the memo must not rely on that) in the NEW
// file must be answerable: it is the path changing that unlocks it here,
// not the id.
func TestAnswerMemoAllowsANewTranscriptPathAfterClear(t *testing.T) {
	m, home := newLocatorManager(t)
	call := func() map[string]any { return txToolUse("Bash", "command", "touch /tmp/a", "toolu_shared") }
	writeTxLines(t, home, locCwd, "aaaa-1", time.Now(), txUser("go"), txRecID("assistant", "msg_pre_clear", call()))
	writeReg(t, home, 101, regJSON(101, "aaaa-1", "5000", "waiting"))
	s := m.sessions["s1"]
	s.replyable = true
	s.status = statusLive
	r := pipeTo(t, s)

	if err := m.Answer("s1", "yes", hexSHA(s1Ask)); err != nil {
		t.Fatalf("first: %v", err)
	}

	// /clear: a new sessionId, so locate() points at a different file —
	// registry now names it, and that file holds its own first pending call.
	writeTxLines(t, home, locCwd, "eeee-9", time.Now(), txUser("go"), txRecID("assistant", "msg_post_clear", call()))
	writeReg(t, home, 101, regJSON(101, "eeee-9", "5000", "waiting"))

	if err := m.Answer("s1", "yes", hexSHA(s1Ask)); err != nil {
		t.Fatalf("second (new transcript path after /clear): %v", err)
	}
	if got := written(t, r); got != "11" {
		t.Fatalf("wrote %q want two \"1\"s (both distinct calls answered)", got)
	}
}

// Two Answer calls racing on the same session (a double-click, or two tabs)
// must not both pass the gates: answerMu makes gate-check-through-Write one
// critical section, so exactly one of them writes and the other sees the
// state its winner just recorded as already-answered.
func TestAnswerConcurrentCallsWriteExactlyOnce(t *testing.T) {
	m, r, _ := answerManager(t)
	const n = 8
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range errs {
		go func(i int) {
			defer wg.Done()
			errs[i] = m.Answer("s1", "yes", hexSHA(s1Ask))
		}(i)
	}
	wg.Wait()
	oks := 0
	for _, err := range errs {
		switch {
		case err == nil:
			oks++
		case !errors.Is(err, ErrPromptChanged):
			t.Errorf("unexpected error: %v", err)
		}
	}
	if oks != 1 {
		t.Fatalf("%d calls succeeded, want exactly 1", oks)
	}
	if got := written(t, r); got != "1" {
		t.Fatalf("wrote %q want exactly one \"1\" total", got)
	}
}

// Interrupt's agent Esc and Answer("no")'s Esc share one debounce per
// session (P9: a second Esc within Claude Code's own double-press window can
// open the rewind menu on a now-idle session). The clock is injected via
// nowFunc so the test never sleeps escDebounceWindow.
func TestEscDebounceSharedByInterruptAndAnswerNo(t *testing.T) {
	m, r, _ := answerManager(t)
	orig := nowFunc
	t.Cleanup(func() { nowFunc = orig })
	now := time.Now()
	nowFunc = func() time.Time { return now }

	if err := m.Interrupt("s1"); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if err := m.Answer("s1", "no", hexSHA(s1Ask)); !errors.Is(err, ErrTooSoon) {
		t.Fatalf("answer(no) within the window: err=%v want ErrTooSoon", err)
	}
	if got := written(t, r); got != "\x1b" {
		t.Fatalf("wrote %q want exactly one Esc (the interrupt's)", got)
	}

	now = now.Add(escDebounceWindow + time.Millisecond)
	if err := m.Answer("s1", "no", hexSHA(s1Ask)); err != nil {
		t.Fatalf("answer(no) past the window: %v", err)
	}
	if got := written(t, r); got != "\x1b" {
		t.Fatalf("wrote %q want exactly one Esc (the answer's)", got)
	}
}

// The web client hashes with a pure-JS SHA-256 (attention.js sha256Hex); this
// is the same vector its test pins, so the two sides cannot drift.
func TestAnswerHashMatchesTheWebClientVector(t *testing.T) {
	const ask = "Bash(rm -rf build) — é ü 漢 😀"
	const vec = "b8c0c145bfbc2ff37428ec2d15dc9ac3c6bf196fa6e8340756f91ec23d84166a"
	if !askHashMatches(ask, vec) || !askHashMatches(ask, strings.ToUpper(vec)) {
		t.Fatal("the shared vector does not match")
	}
	if askHashMatches(ask, vec[:63]+"0") || askHashMatches(ask, "") {
		t.Fatal("a wrong hash matched")
	}
}

// replyable is forced true (answerManager does it) so this proves Answer
// refuses on the kill-switch itself; nothing may reach the PTY.
func TestAnswerRefusedWhenRepliesDisabled(t *testing.T) {
	m, r, _ := answerManager(t)
	m.SetRepliesEnabled(false)
	if err := m.Answer("s1", "yes", hexSHA(s1Ask)); !errors.Is(err, ErrRepliesDisabled) {
		t.Fatalf("err=%v want ErrRepliesDisabled", err)
	}
	if got := written(t, r); got != "" {
		t.Errorf("PTY received %q, want nothing", got)
	}
}
