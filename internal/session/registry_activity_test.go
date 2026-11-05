package session

import (
	"os"
	"testing"
	"time"
)

// TestRegActivity is the whole activity decision for a live session: Claude
// Code's registry status when there is one, PTY recency (with the viewer-input
// mute) only when there is not (#77).
func TestRegActivity(t *testing.T) {
	now := time.Now()
	const recent, stale = time.Second, time.Minute
	cases := []struct {
		name      string
		reg       string        // registry status; "" = no live entry
		ago       time.Duration // when the last PTY output landed
		muted     bool          // that output fell inside a mute window
		submitted bool
		exited    bool
		detached  bool
		want      string
	}{
		{"no registry: fresh output is working", "", recent, false, true, false, false, "working"},
		{"no registry: muted output (viewer echo) is not work", "", recent, true, true, false, false, "waiting"},
		{"no registry: silence after a turn is waiting", "", stale, false, true, false, false, "waiting"},
		{"no registry: silence before any turn is idle", "", stale, false, false, false, false, "idle"},
		{"busy: working though the PTY is silent", regStatusBusy, 30 * time.Second, false, true, false, false, "working"},
		{"busy but silent past regStaleAfter: stale, the PTY rule decides", regStatusBusy, 2 * time.Minute, false, true, false, false, "waiting"},
		{"shell but silent past regStaleAfter: the PTY rule decides", regStatusShell, 2 * time.Minute, false, true, false, false, "waiting"},
		{"busy: working with output", regStatusBusy, recent, false, true, false, false, "working"},
		{"idle: an unmuted repaint does not make it working", regStatusIdle, recent, false, true, false, false, "waiting"},
		{"idle: silent after a turn is waiting", regStatusIdle, stale, false, true, false, false, "waiting"},
		{"idle: never given a turn stays idle", regStatusIdle, recent, false, false, false, false, "idle"},
		{"waiting: a prompt that animates is still waiting", regStatusWaiting, recent, false, true, false, false, "waiting"},
		{"waiting: silent prompt is waiting", regStatusWaiting, stale, false, true, false, false, "waiting"},
		{"shell: background shell is not your turn", regStatusShell, 30 * time.Second, false, true, false, false, "working"},
		{"shell: with output", regStatusShell, recent, false, true, false, false, "working"},
		{"unknown status falls back to the PTY: fresh", "thinking", recent, false, true, false, false, "working"},
		{"unknown status falls back to the PTY: silent", "thinking", stale, false, true, false, false, "waiting"},
		{"exit dominates the registry", regStatusBusy, recent, false, true, true, false, "done"},
		{"detached dominates the registry", regStatusBusy, recent, false, true, false, true, "detached"},
	}
	for _, c := range cases {
		s := &Session{status: statusLive, agentKind: true, submitted: c.submitted, lastActivityAt: now.Add(-time.Hour)}
		if c.exited {
			s.status, s.exited = statusExited, true
		}
		if c.detached {
			s.status = statusDetached
		}
		at := now.Add(-c.ago)
		if c.muted {
			s.muteActivity(at.Add(-100 * time.Millisecond))
		}
		s.noteOutput(at)
		q, silent := s.quietActivity()
		if got, _ := regActivity(s.Info().Activity, q, c.reg, silent); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// List folds the registry status into the activity it reports, and says where
// the activity came from: "agent" (the agent's own status report) or "pty"
// (the silence fallback), so the client debounces only the latter.
func TestListActivityFollowsTheRegistry(t *testing.T) {
	cases := []struct {
		name, reg string        // reg "" = no valid registry entry
		silent    time.Duration // how long the PTY has been quiet
		want, src string
	}{
		{"busy and silent", regStatusBusy, 30 * time.Second, "working", "agent"},
		{"idle with a viewer repaint", regStatusIdle, 0, "waiting", "agent"},
		{"shell and silent", regStatusShell, 30 * time.Second, "working", "agent"},
		{"no registry: the PTY decides", "", 30 * time.Second, "waiting", "pty"},
		{"stale busy: the PTY decides", regStatusBusy, 2 * time.Minute, "waiting", "pty"},
	}
	for _, c := range cases {
		m, home := newLocatorManager(t)
		body := "{}" // fails validation: no entry
		if c.reg != "" {
			body = regJSON(101, "aaaa-1", "5000", c.reg)
		}
		writeReg(t, home, 101, body)
		m.sessions["s1"].lastActivityAt = time.Now().Add(-c.silent)
		for _, i := range m.List() {
			if i.ID == "s1" && (i.Activity != c.want || i.ActivitySource != c.src) {
				t.Errorf("%s: activity=%q source=%q want %q %q", c.name, i.Activity, i.ActivitySource, c.want, c.src)
			}
		}
	}
}

// Viewer input (focus, mouse, keys) makes Claude repaint within milliseconds
// (measured, #77). That echo must not read as agent work, but output after the
// short window must.
func TestWriteMutesActivity(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devnull.Close() })
	base := time.Now().Add(-time.Hour)
	s := &Session{ptmx: devnull, lastActivityAt: base}
	before := time.Now()
	if _, err := s.Write([]byte("\x1b[I")); err != nil { // focus-in report
		t.Fatal(err)
	}
	s.noteOutput(before.Add(50 * time.Millisecond))
	if !s.lastActivityAt.Equal(base) {
		t.Fatalf("the repaint right after a Write counted as activity: %v", s.lastActivityAt)
	}
	later := time.Now().Add(attachRepaintGrace + time.Millisecond)
	s.noteOutput(later)
	if !s.lastActivityAt.Equal(later) {
		t.Fatalf("output after the mute window did not count: got %v want %v", s.lastActivityAt, later)
	}
}

// A submitted line is not a viewer echo: its echo and the agent's start of work
// must count, which also makes a second SendMessage right after see "working".
func TestSubmittedInputIsNotMuted(t *testing.T) {
	t0 := time.Now().Add(-time.Hour)
	cases := []struct {
		name   string
		writes []string // written 100 ms apart, starting at t0
	}{
		{"a line ending in Enter", []string{"hi\r"}},
		{"a pasted line ending in newline", []string{"hi\n"}},
		{"Enter ends the mute its keystrokes started", []string{"h", "i", "\r"}},
	}
	for _, c := range cases {
		s := &Session{lastActivityAt: t0.Add(-time.Hour)}
		at := t0
		for _, w := range c.writes {
			s.noteInput(at, []byte(w))
			at = at.Add(100 * time.Millisecond)
		}
		s.noteOutput(at)
		if !s.lastActivityAt.Equal(at) {
			t.Errorf("%s: output right after the line did not count", c.name)
		}
	}
}

// Input (steady typing, mouse motion) may keep activity muted for at most
// inputMuteMax in each idleThreshold window, so at least 7 s of every 10 s is
// unmuted and real work always shows. A pause does not refill the budget; the
// next window does.
func TestInputMuteIsBudgetedPerWindow(t *testing.T) {
	t0 := time.Now().Add(-time.Hour)
	s := &Session{lastActivityAt: t0.Add(-time.Hour)}
	for d := time.Duration(0); d <= 5*time.Second; d += 200 * time.Millisecond {
		s.noteInput(t0.Add(d), []byte("x"))
	}
	if s.noteOutput(t0.Add(2 * time.Second)); !s.lastActivityAt.Before(t0) {
		t.Fatal("output 2 s into a typing run counted: the run is still muted")
	}
	at := t0.Add(3*time.Second + 500*time.Millisecond)
	if s.noteOutput(at); !s.lastActivityAt.Equal(at) {
		t.Fatalf("output 3.5 s into a typing run was muted: the mute must be capped")
	}
	pause := t0.Add(5*time.Second + attachRepaintGrace + 500*time.Millisecond) // after a pause, same window
	s.noteInput(pause, []byte("x"))
	if s.noteOutput(pause.Add(50 * time.Millisecond)); !s.lastActivityAt.Equal(pause.Add(50 * time.Millisecond)) {
		t.Fatal("a pause refilled the mute budget inside the same window")
	}
	next := t0.Add(idleThreshold + time.Second) // a new window: the budget is back
	s.noteInput(next, []byte("x"))
	if s.noteOutput(next.Add(50 * time.Millisecond)); s.lastActivityAt.Equal(next.Add(50 * time.Millisecond)) {
		t.Fatal("input in a new window was not muted")
	}
}

// Input every 1.01 s (just past the grace, so every write used to start a new
// "run") kept activity muted ~99% of the time: a 0.6 s spinner read waiting.
// With the per-window budget the spinner reads working throughout.
func TestSpacedInputCannotHideASpinner(t *testing.T) {
	t0 := time.Now().Add(-time.Hour)
	s := &Session{lastActivityAt: t0, agentKind: true, submitted: true}
	const span = 30 * time.Second
	in, out := time.Duration(0), 600*time.Millisecond
	for out <= span {
		if in <= out {
			s.noteInput(t0.Add(in), []byte("x"))
			in += 1010 * time.Millisecond
			continue
		}
		at := t0.Add(out)
		s.noteOutput(at)
		if got := activityOf(false, false, 0, s.lastActivityAt, at, idleThreshold, true, true); got != "working" {
			t.Fatalf("at %v a 0.6 s spinner under spaced input reads %q", out, got)
		}
		out += 600 * time.Millisecond
	}
}

// Interrupt (Esc) and Answer are control actions, like a submitted line: the
// agent's reaction is real activity and must count at once, not be muted as a
// viewer echo.
func TestControlActionsAreNotMuted(t *testing.T) {
	check := func(name string, s *Session) {
		t.Helper()
		at := time.Now().Add(50 * time.Millisecond)
		s.mu.Lock()
		s.noteOutput(at)
		got := s.lastActivityAt
		s.mu.Unlock()
		if !got.Equal(at) {
			t.Errorf("%s: the agent's reaction right after it was muted", name)
		}
	}
	s := &Session{ID: "a", Bin: "/usr/bin/claude", status: statusLive}
	pipeTo(t, s)
	m := &Manager{sessions: map[string]*Session{"a": s}}
	if err := m.Interrupt("a"); err != nil {
		t.Fatal(err)
	}
	check("Interrupt", s)

	am, _, _ := answerManager(t)
	if err := am.Answer("s1", "yes", hexSHA(s1Ask)); err != nil {
		t.Fatal(err)
	}
	check("Answer", am.sessions["s1"])
}

// List's reply_ready is SendMessage's own verdict, so the UI never offers a
// send the server refuses (a turn just ended: registry idle, PTY still
// "working") nor blocks one it accepts (shell with a quiet PTY).
func TestListReplyReadyMatchesSendMessage(t *testing.T) {
	cases := []struct {
		name, reg    string        // reg "" = no valid registry entry
		silent       time.Duration // how long the PTY has been quiet
		replyable    bool
		pending      bool // the transcript ends on a tool_use
		off, nonAgnt bool // replies kill-switch off; a plain shell session
		want         bool
	}{
		{"turn just ended: registry idle, PTY still working", regStatusIdle, 0, true, false, false, false, false},
		{"idle and quiet", regStatusIdle, 30 * time.Second, true, false, false, false, true},
		{"shell and quiet", regStatusShell, 30 * time.Second, true, false, false, false, true},
		{"busy and quiet", regStatusBusy, 30 * time.Second, true, false, false, false, false},
		{"stale busy", regStatusBusy, 2 * time.Minute, true, false, false, false, true},
		{"registry waiting: a prompt", regStatusWaiting, 30 * time.Second, true, false, false, false, false},
		{"no registry, quiet", "", 30 * time.Second, true, false, false, false, true},
		{"no registry, output", "", 0, true, false, false, false, false},
		{"pending tool call", regStatusIdle, 30 * time.Second, true, true, false, false, false},
		{"not opted in", regStatusIdle, 30 * time.Second, false, false, false, false, false},
		{"replies switched off", regStatusIdle, 30 * time.Second, true, false, true, false, false},
		{"not an agent session", regStatusIdle, 30 * time.Second, true, false, false, true, false},
	}
	for _, c := range cases {
		m, home := newLocatorManager(t)
		body := "{}" // fails validation: no entry
		if c.reg != "" {
			body = regJSON(101, "aaaa-1", "5000", c.reg)
		}
		writeReg(t, home, 101, body)
		if c.pending {
			pending := `{"message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}` + "\n"
			if err := os.WriteFile(home+"/.claude/projects/"+projectSlug(locCwd)+"/aaaa-1.jsonl", []byte(pending), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		s := m.sessions["s1"]
		s.claudeSessionID = "aaaa-1" // bound by its conversation id even with no registry entry
		s.replyable = c.replyable
		s.lastActivityAt = time.Now().Add(-c.silent)
		if c.nonAgnt {
			s.Bin, s.agentKind = "/bin/zsh", false
		}
		m.SetRepliesEnabled(!c.off)
		devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = devnull.Close() })
		s.ptmx = devnull
		var got *bool
		for _, i := range m.List() {
			if i.ID == "s1" {
				got = i.ReplyReady
			}
		}
		if got == nil {
			t.Errorf("%s: List carries no reply_ready", c.name)
			continue
		}
		sendErr := m.SendMessage("s1", "next step")
		if *got != c.want || *got != (sendErr == nil) {
			t.Errorf("%s: reply_ready=%v SendMessage err=%v, want ready=%v and the two to agree", c.name, *got, sendErr, c.want)
		}
	}
}

// SendMessage's guard: the registry may only TIGHTEN the PTY verdict, never
// loosen it. The agent can write its own registry (a boxed one always can), so
// a forged "idle" must not let a line be typed into a prompt the PTY shows
// animating — that dialog reads single keys.
func TestSendMessageRegistryOnlyTightens(t *testing.T) {
	cases := []struct {
		name, reg string
		silent    time.Duration // how long the PTY has been quiet
		want      error
	}{
		{"busy refuses though the PTY is quiet", regStatusBusy, 30 * time.Second, ErrNotWaiting},
		{"a stale busy (PTY silent past regStaleAfter) no longer refuses", regStatusBusy, 2 * time.Minute, nil},
		{"idle never overrides an animating PTY", regStatusIdle, 0, ErrNotWaiting},
		{"shell with a quiet PTY accepts a reply: Claude is at its prompt", regStatusShell, 30 * time.Second, nil},
		{"shell never overrides an animating PTY", regStatusShell, 0, ErrNotWaiting},
		{"idle with a quiet PTY accepts a reply", regStatusIdle, 30 * time.Second, nil},
	}
	for _, c := range cases {
		m, home := newLocatorManager(t)
		writeReg(t, home, 101, regJSON(101, "aaaa-1", "5000", c.reg))
		s := m.sessions["s1"]
		s.replyable = true
		s.lastActivityAt = time.Now().Add(-c.silent)
		devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = devnull.Close() })
		s.ptmx = devnull
		if err := m.SendMessage("s1", "next step"); err != c.want {
			t.Errorf("%s: err=%v want %v", c.name, err, c.want)
		}
	}
}
