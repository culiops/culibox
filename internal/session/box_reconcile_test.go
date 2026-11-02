package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

// failingAttachBackend is alive at the abduco layer but cannot produce an attach
// PTY — the daemon-restart case where the agent survived but re-attaching fails.
type failingAttachBackend struct{ *fakeBackend }

func (f failingAttachBackend) Attach(string, uint16, uint16) (*exec.Cmd, *os.File, error) {
	return nil, nil, os.ErrPermission
}

// F2 PROBE: reattach fails, but the agent is still alive inside its box.
// Reconcile explicitly leaves the abduco session running for a later restart —
// so the box MUST be kept too. If the box layer never learns the workspace is
// live, its reaper destroys the container the surviving agent runs in.
func TestFailedReattachStillKeepsTheBox(t *testing.T) {
	dir := t.TempDir()
	be := newFakeBackend()
	be.alive["bash-1"] = true
	be.list = []string{"bash-1"}

	m := NewManager(tieredLauncher{argv: []string{"true"}, runtime: config.RuntimeBox},
		failingAttachBackend{be}, 1<<16, time.Minute)
	if err := m.SetSidecarPath(filepath.Join(t.TempDir(), "sessions.json")); err != nil {
		t.Fatal(err)
	}
	m.sidecar.put("bash-1", sessionMeta{
		Agent: "bash", Cwd: dir, Created: time.Now(), Runtime: config.RuntimeBox, Box: "ab-box-live",
	})
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)

	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if !boxes.adoptedBox("ab-box-live") {
		t.Fatal("box layer never learned the box is live after a failed reattach; " +
			"its reaper will destroy the container the surviving agent runs in")
	}
}

// F3 PROBE: the workspace directory no longer resolves (moved or deleted), so
// the box name cannot be recomputed from the path. The agent is still alive, so
// the box must still be kept.
func TestUnresolvableWorkspaceStillKeepsTheBox(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "deleted-workspace")
	if err := os.Mkdir(gone, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil { // the path no longer resolves
		t.Fatal(err)
	}

	be := newFakeBackend()
	be.alive["bash-1"] = true
	be.list = []string{"bash-1"}
	m := NewManager(tieredLauncher{argv: []string{"true"}, runtime: config.RuntimeBox}, be, 1<<16, time.Minute)
	if err := m.SetSidecarPath(filepath.Join(t.TempDir(), "sessions.json")); err != nil {
		t.Fatal(err)
	}
	m.sidecar.put("bash-1", sessionMeta{
		Agent: "bash", Cwd: gone, Created: time.Now(), Runtime: config.RuntimeBox, Box: "ab-box-gonepath",
	})
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)

	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	// The box is claimed by its STORED name, so a workspace path that no longer
	// resolves cannot cost a live session its container.
	if !boxes.adoptedBox("ab-box-gonepath") {
		t.Fatal("an unresolvable workspace path left the box unclaimed; the reaper will " +
			"destroy a container whose agent is still alive")
	}
}

// The tier must be decided exactly once. It used to be computed independently
// by launcher.Build (from the spec/config) and by session.runtimeFor (which also
// requires a box layer) — so with containment unavailable, a base:inherit agent
// ran on the HOST but was composed with boxed semantics: curated env, no login
// wrap, bin left unresolved. The manager now stamps the decided tier onto the
// spec, so the launcher always composes for the tier the session actually runs on.
func TestNoBoxLayerStillGivesHostEnvSemantics(t *testing.T) {
	lnch := launcher.New([]config.AgentConfig{
		{Name: "bash", Bin: "bash", Base: config.BaseInherit},
	}, fakeSecretResolver{}, nil)
	rec := &recordingLauncher{Builder: lnch, resolver: lnch}

	// No SetBoxes: containment unavailable, exactly like a host without docker.
	m := NewManager(rec, nil, 1<<16, time.Minute)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash"})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)

	if s.Runtime != config.RuntimeHost {
		t.Fatalf("session runtime = %q, want host with no box layer", s.Runtime)
	}
	if got := rec.lastSpec().Runtime; got != config.RuntimeHost {
		t.Fatalf("launcher was told to compose for tier %q while the session runs on %q: "+
			"the tier is decided twice and the two disagree", got, s.Runtime)
	}
}

// recordingLauncher captures the spec the manager actually hands the launcher.
type recordingLauncher struct {
	launcher.Builder
	resolver *launcher.AgentLauncher

	mu   sync.Mutex
	seen launcher.SpawnSpec
}

func (r *recordingLauncher) Build(spec launcher.SpawnSpec) ([]string, []string, error) {
	r.mu.Lock()
	r.seen = spec
	r.mu.Unlock()
	return r.Builder.Build(spec)
}

func (r *recordingLauncher) RuntimeFor(spec launcher.SpawnSpec) (string, error) {
	return r.resolver.RuntimeFor(spec)
}

func (r *recordingLauncher) lastSpec() launcher.SpawnSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen
}

type fakeSecretResolver struct{}

func (fakeSecretResolver) Resolve(string) (string, bool, error) { return "", false, nil }
