package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/launcher"
)

// devnullSession is a Session whose PTY is /dev/null: Write succeeds and goes
// nowhere, which is all the submitted-flag logic needs.
func devnullSession(t *testing.T) *Session {
	t.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return &Session{ID: "s1", ptmx: f}
}

func submittedOf(s *Session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.submitted
}

// Only Enter counts. xterm sends focus reports and CPR/DA replies on its own, so
// "any byte" would mark a never-prompted agent as prompted the moment you click
// into its pane.
func TestWriteMarksSubmittedOnlyOnEnter(t *testing.T) {
	for _, in := range []string{"\x1b[I", "\x1b[O", "\x1b[12;5R", "\x1b[?1;2c", "hello", "\n"} {
		s := devnullSession(t)
		if _, err := s.Write([]byte(in)); err != nil {
			t.Fatal(err)
		}
		if submittedOf(s) {
			t.Errorf("Write(%q) marked the session submitted; only Enter (\\r) may", in)
		}
	}
	s := devnullSession(t)
	calls := 0
	s.onSubmitted = func() { calls++ }
	if _, err := s.Write([]byte("fix the bug\r")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	if !submittedOf(s) {
		t.Fatal("Enter did not mark the session submitted")
	}
	if calls != 1 {
		t.Errorf("onSubmitted called %d times, want 1 (false->true edge only)", calls)
	}
}

func TestSessionMetaWithoutSubmittedDecodesNil(t *testing.T) {
	var md sessionMeta
	old := `{"agent":"claude","cwd":"/tmp/proj","created":"2026-08-01T00:00:00Z","runtime":"host"}`
	if err := json.Unmarshal([]byte(old), &md); err != nil {
		t.Fatal(err)
	}
	if md.Submitted != nil {
		t.Fatalf("Submitted=%v from a pre-feature record, want nil (unknown)", *md.Submitted)
	}
}

func TestSidecarSubmittedRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	sc, err := openSidecar(path)
	if err != nil {
		t.Fatal(err)
	}
	sc.put("claude-1", sessionMeta{Agent: "claude", Submitted: boolPtr(false)})
	sc.setSubmitted("claude-1")
	sc.setSubmitted("nope") // absent id: no-op, never resurrects an entry
	re, err := openSidecar(path)
	if err != nil {
		t.Fatal(err)
	}
	md := re.all()["claude-1"]
	if md.Submitted == nil || !*md.Submitted {
		t.Fatalf("Submitted=%v after setSubmitted, want true persisted", md.Submitted)
	}
	if _, ok := re.all()["nope"]; ok {
		t.Fatal("setSubmitted created an entry for an unknown id")
	}
}

func TestReconcileRestoresSubmitted(t *testing.T) {
	cases := []struct {
		name string
		md   *bool
		want bool
	}{
		// A stale amber once beats hiding a waiting agent.
		{"pre-feature entry restores as submitted", nil, true},
		{"explicit false stays false", boolPtr(false), false},
		{"explicit true stays true", boolPtr(true), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fb := newFakeBackend()
			fb.seedLive("claude-2")
			path := writeSidecar(t, t.TempDir(), map[string]sessionMeta{
				"claude-2": {Agent: "claude", Cwd: "/work/proj", Created: time.Now(), Submitted: c.md},
			})
			m := newReconcileManager(t, fb, path)
			if err := m.Reconcile(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Kill("claude-2") })
			s, ok := m.Get("claude-2")
			if !ok {
				t.Fatal("survivor not re-attached")
			}
			if got := submittedOf(s); got != c.want {
				t.Errorf("submitted=%v want %v", got, c.want)
			}
		})
	}
}

func TestReconcileOrphanRestoresAsSubmitted(t *testing.T) {
	fb := newFakeBackend()
	fb.seedLive("claude-3") // a socket with no sidecar entry at all
	m := newReconcileManager(t, fb, filepath.Join(t.TempDir(), "sessions.json"))
	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill("claude-3") })
	s, ok := m.Get("claude-3")
	if !ok {
		t.Fatal("orphan not adopted")
	}
	if !submittedOf(s) {
		t.Error("orphan-adopted session restored as never-submitted; want true")
	}
}

func TestSpawnPersistsSubmittedOnFirstEnter(t *testing.T) {
	fb := newFakeBackend()
	path := filepath.Join(t.TempDir(), "sessions.json")
	m := newReconcileManager(t, fb, path)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	md := m.sidecar.all()[s.ID]
	if md.Submitted == nil || *md.Submitted {
		t.Fatalf("spawn wrote Submitted=%v, want an explicit false", md.Submitted)
	}
	if _, err := s.Write([]byte("hi\r")); err != nil {
		t.Fatal(err)
	}
	re, err := openSidecar(path)
	if err != nil {
		t.Fatal(err)
	}
	if md := re.all()[s.ID]; md.Submitted == nil || !*md.Submitted {
		t.Fatalf("Submitted=%v on disk after the first Enter, want true", md.Submitted)
	}
}
