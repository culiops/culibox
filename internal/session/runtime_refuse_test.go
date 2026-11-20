package session

import (
	"errors"
	"strings"
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

// hostBlockLauncher is a tieredLauncher that also names why a profile may not
// run on the host (the optional HostBlockResolver), like the real launcher.
type hostBlockLauncher struct {
	tieredLauncher
	reason string
}

func (l hostBlockLauncher) HostBlockFor(string) (string, error) { return l.reason, nil }

func TestSpawnHostRefusedByHostBlock(t *testing.T) {
	const reason = "this profile is set to box only"
	l := hostBlockLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}, reason: reason}
	m := NewManager(l, nil, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)

	_, err := m.Spawn(launcher.SpawnSpec{Agent: "p", Cwd: t.TempDir(), Runtime: config.RuntimeHost})
	if err == nil {
		t.Fatal("a host spawn the profile forbids was accepted")
	}
	if !errors.Is(err, box.ErrUnboxableWorkspace) {
		t.Fatalf("err = %v, want ErrUnboxableWorkspace so the UI shows it", err)
	}
	if !strings.Contains(err.Error(), reason) || !strings.Contains(err.Error(), `"p"`) {
		t.Fatalf("err = %q, want the profile and the reason", err)
	}
	if boxes.prepares != 0 || boxes.wraps != 0 || len(m.List()) != 0 {
		t.Fatalf("refused spawn prepared=%d wrapped=%d sessions=%d", boxes.prepares, boxes.wraps, len(m.List()))
	}

	// The same profile in a box passes this check.
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "p", Cwd: t.TempDir(), Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatalf("box spawn refused: %v", err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
}

// A profile with nothing forbidding the host is not touched by the check.
func TestSpawnHostAllowedWhenHostBlockEmpty(t *testing.T) {
	l := hostBlockLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeHost}}
	m := NewManager(l, nil, 1<<16, time.Minute)
	m.SetBoxes(&fakeBoxes{})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "p", Cwd: t.TempDir(), Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
}

// The bound-on-host refusal (the second net, for a launcher without a
// HostBlockResolver) is a message the UI shows, not "could not start session".
func TestSpawnBoundHostRefusalIsShown(t *testing.T) {
	m, _, _, _ := boundManager(t, []string{"sh", "-c", "sleep 30"}, config.RuntimeBox, time.Minute)
	_, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir(), Runtime: config.RuntimeHost})
	if err == nil {
		t.Fatal("a bound profile spawned on the host")
	}
	if !errors.Is(err, box.ErrUnboxableWorkspace) {
		t.Fatalf("err = %v, want ErrUnboxableWorkspace so the UI shows it", err)
	}
	if !strings.Contains(err.Error(), "has an AWS binding, so it can only run in a box, never on the host tier") {
		t.Fatalf("err = %q, wording changed", err)
	}
}

// The production launcher is what answers; a rename on either side would
// otherwise silently turn the host-block check off (box_only has no second net).
var _ HostBlockResolver = (*launcher.AgentLauncher)(nil)
