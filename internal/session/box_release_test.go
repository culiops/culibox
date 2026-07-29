package session

import (
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/launcher"
)

// F1 PROBE: a boxed session that goes DETACHED (agent alive, attach pipe lost)
// and is then killed must still release its box reference. If it does not, the
// refcount never reaches zero, the idle timer is never armed, and the box
// outlives its TTL forever.
func TestDetachedThenKilledReleasesBox(t *testing.T) {
	be := newFakeBackend()
	m := NewManager(tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}, be, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)

	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	// Drive the session into `detached`: the agent (inner) stays alive while the
	// attach process dies — exactly what a lost `abduco -a` client looks like.
	be.mu.Lock()
	att := be.attached[s.ID]
	be.mu.Unlock()
	_ = att.Process.Kill()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.Info().Status == statusDetached {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := s.Info().Status; got != statusDetached {
		t.Fatalf("precondition failed: status = %q, want detached", got)
	}
	if _, releases, _ := boxes.counts(); releases != 0 {
		t.Fatalf("a detached session must NOT release its box (agent still alive); releases=%d", releases)
	}

	if err := m.Kill(s.ID); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, releases, _ := boxes.counts(); releases == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, releases, _ := boxes.counts()
	t.Fatalf("detached-then-killed released the box %d times, want 1 (refcount leak: box never reaped)", releases)
}
