package session

import (
	"testing"
	"time"
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
