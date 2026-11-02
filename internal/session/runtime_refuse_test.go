package session

import (
	"errors"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

// requestingLauncher is a ctlLauncher that also names the tier a profile asked
// for (the optional TierRequester), like the real launcher.
type requestingLauncher struct {
	*ctlLauncher
	requested string
}

func (l requestingLauncher) RequestedRuntime(launcher.SpawnSpec) (string, error) {
	return l.requested, nil
}

// newRequestingManager: an ephemeral manager with NO box layer (SetBoxes is
// never called), which is exactly the instance shape under test.
func newRequestingManager(t *testing.T, l requestingLauncher) *Manager {
	t.Helper()
	return NewManager(l, nil, 1<<16, time.Minute)
}

// An explicit box request on an instance with no box layer must fail loudly: a
// silent fall-back to host would run the agent uncontained.
func TestSpawnRefusesExplicitBoxWithoutContainment(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sleep", "30"}, bin: "/usr/bin/claude"}
	m, _ := newCtlManager(t, l)
	_, err := m.Spawn(launcher.SpawnSpec{Agent: "a", Runtime: config.RuntimeBox, Cwd: t.TempDir()})
	if !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("err = %v, want box.ErrBoxUnavailable", err)
	}
	if n := len(m.List()); n != 0 {
		t.Fatalf("%d sessions registered after a refused spawn", n)
	}
	if len(l.specs) != 0 {
		t.Fatalf("launcher built %d commands for a refused spawn", len(l.specs))
	}
}

func TestSpawnRefusesProfileBoxWithoutContainment(t *testing.T) {
	l := requestingLauncher{ctlLauncher: &ctlLauncher{argv: []string{"sleep", "30"}, bin: "/usr/bin/claude"}, requested: config.RuntimeBox}
	m := newRequestingManager(t, l)
	_, err := m.Spawn(launcher.SpawnSpec{Agent: "a", Cwd: t.TempDir()})
	if !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("err = %v, want box.ErrBoxUnavailable", err)
	}
	if n := len(m.List()); n != 0 {
		t.Fatalf("%d sessions registered after a refused spawn", n)
	}
	if len(l.specs) != 0 {
		t.Fatalf("launcher built %d commands for a refused spawn", len(l.specs))
	}
}

func TestSpawnFallsBackToHostWhenNothingAskedForBox(t *testing.T) {
	l := requestingLauncher{ctlLauncher: &ctlLauncher{argv: []string{"sleep", "30"}, bin: "/usr/bin/claude"}, requested: ""}
	m := newRequestingManager(t, l)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "a", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if s.Runtime != config.RuntimeHost {
		t.Fatalf("runtime = %q, want host", s.Runtime)
	}
}

func TestSpawnExplicitHostWithoutContainment(t *testing.T) {
	// The profile asks for box, the spawn explicitly says host: the explicit
	// request wins and nothing is refused.
	l := requestingLauncher{ctlLauncher: &ctlLauncher{argv: []string{"sleep", "30"}, bin: "/usr/bin/claude"}, requested: config.RuntimeBox}
	m := newRequestingManager(t, l)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "a", Runtime: config.RuntimeHost, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if s.Runtime != config.RuntimeHost {
		t.Fatalf("runtime = %q, want host", s.Runtime)
	}
}
