package session

import (
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/launcher"
)

func TestActivityOf(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name   string
		exited bool
		known  bool
		code   int
		last   time.Time
		want   string
	}{
		{"working: output just now", false, false, 0, now, "working"},
		{"working: 9s ago", false, false, 0, now.Add(-9 * time.Second), "working"},
		{"waiting: exactly at threshold", false, false, 0, now.Add(-idleThreshold), "waiting"}, // now-last == idle, `< idle` is false
		{"waiting: 11s ago", false, false, 0, now.Add(-11 * time.Second), "waiting"},
		{"done: clean exit", true, true, 0, now, "done"},
		{"crashed: known nonzero", true, true, 1, now, "crashed"},
		{"done: nonzero but untrusted", true, false, 1, now, "done"},
		{"done: exited untrusted zero", true, false, 0, now, "done"},
	}
	for _, c := range cases {
		got := activityOf(c.exited, c.known, c.code, c.last, now, idleThreshold)
		if got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestInfoActivity(t *testing.T) {
	now := time.Now()
	t.Run("live recent -> working", func(t *testing.T) {
		s := &Session{status: statusLive, lastActivityAt: now}
		if got := s.Info().Activity; got != "working" {
			t.Fatalf("got %q want working", got)
		}
	})
	t.Run("live stale -> waiting", func(t *testing.T) {
		s := &Session{status: statusLive, lastActivityAt: now.Add(-time.Minute)}
		if got := s.Info().Activity; got != "waiting" {
			t.Fatalf("got %q want waiting", got)
		}
	})
	t.Run("exited nonzero trusted -> crashed", func(t *testing.T) {
		s := &Session{status: statusExited, exited: true, exitCodeKnown: true, exitCode: 2}
		if got := s.Info().Activity; got != "crashed" {
			t.Fatalf("got %q want crashed", got)
		}
	})
	t.Run("detached -> detached", func(t *testing.T) {
		s := &Session{status: statusDetached}
		if got := s.Info().Activity; got != "detached" {
			t.Fatalf("got %q want detached", got)
		}
	})
}

// TestReapExitCodeActivity exercises the REAL reap() exit-code path (not hand-set
// fields): an ephemeral (nil-backend) session's cmd IS the agent, so its exit
// code is trusted. This is the only runtime path that sets exitCodeKnown, so
// without it "crashed" is unverified — and it proves the trust rule end to end.
func TestReapExitCodeActivity(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"sh", "-c", "exit 0"}, "done"},
		{[]string{"sh", "-c", "exit 3"}, "crashed"},
	}
	for _, c := range cases {
		m := NewManager(mockBuilder{argv: c.argv}, nil, 1<<16, time.Minute)
		s, err := m.Spawn(launcher.SpawnSpec{Agent: "sh"})
		if err != nil {
			t.Fatalf("%v: spawn: %v", c.argv, err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for !s.Info().Exited {
			if time.Now().After(deadline) {
				t.Fatalf("%v: did not exit within 3s", c.argv)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if got := s.Info().Activity; got != c.want {
			t.Fatalf("%v: activity=%q want %q", c.argv, got, c.want)
		}
	}
}
