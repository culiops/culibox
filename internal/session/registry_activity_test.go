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

// Back-to-back input (steady typing, mouse motion) may keep activity muted for
// at most inputMuteMax in one run, so it cannot hide real work forever. A pause
// longer than the grace starts a new run.
func TestInputMuteIsCapped(t *testing.T) {
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
	resume := t0.Add(5*time.Second + attachRepaintGrace + 500*time.Millisecond) // after a pause
	s.noteInput(resume, []byte("x"))
	if s.noteOutput(resume.Add(50 * time.Millisecond)); !s.lastActivityAt.Equal(at) {
		t.Fatal("a keystroke after a pause did not start a new muted run")
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
