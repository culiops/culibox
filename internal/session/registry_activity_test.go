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
		{"busy: working though the PTY is silent", regStatusBusy, stale, false, true, false, false, "working"},
		{"busy: working with output", regStatusBusy, recent, false, true, false, false, "working"},
		{"idle: an unmuted repaint does not make it working", regStatusIdle, recent, false, true, false, false, "waiting"},
		{"idle: silent after a turn is waiting", regStatusIdle, stale, false, true, false, false, "waiting"},
		{"idle: never given a turn stays idle", regStatusIdle, recent, false, false, false, false, "idle"},
		{"waiting: a prompt that animates is still waiting", regStatusWaiting, recent, false, true, false, false, "waiting"},
		{"waiting: silent prompt is waiting", regStatusWaiting, stale, false, true, false, false, "waiting"},
		{"shell: background shell is not your turn", regStatusShell, stale, false, true, false, false, "working"},
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
		if got := regActivity(s.Info().Activity, s.quietActivity(), c.reg); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// List folds the registry status into the activity it reports.
func TestListActivityFollowsTheRegistry(t *testing.T) {
	cases := []struct {
		name, reg string
		fresh     bool // PTY output just now
		want      string
	}{
		{"busy and silent", regStatusBusy, false, "working"},
		{"idle with a viewer repaint", regStatusIdle, true, "waiting"},
		{"shell and silent", regStatusShell, false, "working"},
	}
	for _, c := range cases {
		m, home := newLocatorManager(t)
		writeReg(t, home, 101, regJSON(101, "aaaa-1", "5000", c.reg))
		if c.fresh {
			m.sessions["s1"].lastActivityAt = time.Now()
		}
		for _, i := range m.List() {
			if i.ID == "s1" && i.Activity != c.want {
				t.Errorf("%s: activity=%q want %q", c.name, i.Activity, c.want)
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

// SendMessage must judge "waiting" the way List does: a registry that says the
// turn is running refuses a reply even though the PTY has gone quiet.
func TestSendMessageFollowsTheRegistry(t *testing.T) {
	m, home := newLocatorManager(t)
	writeReg(t, home, 101, regJSON(101, "aaaa-1", "5000", regStatusBusy))
	s := m.sessions["s1"]
	s.replyable = true
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devnull.Close() })
	s.ptmx = devnull
	if err := m.SendMessage("s1", "next step"); err != ErrNotWaiting {
		t.Errorf("busy registry, silent PTY: err=%v want ErrNotWaiting", err)
	}
	writeReg(t, home, 101, regJSON(101, "aaaa-1", "5000", regStatusIdle))
	s.lastActivityAt = time.Now() // an idle repaint
	if err := m.SendMessage("s1", "next step"); err != nil {
		t.Errorf("idle registry with a repaint: err=%v want nil", err)
	}
}
