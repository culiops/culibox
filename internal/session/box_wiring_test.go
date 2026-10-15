package session

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/launcher"
)

// tieredLauncher is a builder that also answers RuntimeFor, like the real one.
type tieredLauncher struct {
	argv    []string
	runtime string
}

func (l tieredLauncher) Build(launcher.SpawnSpec) ([]string, []string, error) {
	return l.argv, []string{"TERM=xterm"}, nil
}

func (l tieredLauncher) BinFor(launcher.SpawnSpec) (string, error) { return "claude", nil }

func (l tieredLauncher) RuntimeFor(spec launcher.SpawnSpec) (string, error) {
	if spec.Runtime != "" {
		return spec.Runtime, nil
	}
	return l.runtime, nil
}

type fakeBoxes struct {
	mu       sync.Mutex
	wraps    int
	releases int
	adopts   int
	kills    int
	adopted  []string
}

func (f *fakeBoxes) Wrap(_ context.Context, _ string, _ string, argv, env []string, _ []box.Mount) ([]string, []string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wraps++
	// /usr/bin/env, not docker: it exists on Linux and macOS alike and really
	// runs argv, so the kill/exit tests drive a live agent instead of a
	// `docker exec` that fails at once.
	return append([]string{"/usr/bin/env"}, argv...), env, "ab-box-test", nil
}

func (f *fakeBoxes) AdoptBox(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adopts++
	f.adopted = append(f.adopted, name)
}

func (f *fakeBoxes) ReleaseBox(string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
}

func (f *fakeBoxes) KillSession(_ context.Context, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kills++
	return nil
}

func (f *fakeBoxes) Runtime() string { return "runc" }

func (f *fakeBoxes) counts() (int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.wraps, f.releases, f.adopts
}

func (f *fakeBoxes) adoptedBox(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.adopted {
		if a == name {
			return true
		}
	}
	return false
}

func (f *fakeBoxes) killCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.kills
}

func managerWithBoxes(t *testing.T, argv []string) (*Manager, *fakeBoxes) {
	t.Helper()
	m := NewManager(tieredLauncher{argv: argv, runtime: config.RuntimeBox}, nil, 1<<16, time.Minute)
	b := &fakeBoxes{}
	m.SetBoxes(b)
	return m, b
}

func TestSpawnWithHostRuntimeSkipsTheBox(t *testing.T) {
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if wraps, _, _ := boxes.counts(); wraps != 0 {
		t.Fatalf("trusted-host spawn must not touch the box layer, got %d wraps", wraps)
	}
	if s.Runtime != config.RuntimeHost {
		t.Fatalf("session runtime = %q, want host", s.Runtime)
	}
}

func TestSpawnWithBoxRuntimeWrapsArgvOnce(t *testing.T) {
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if wraps, _, _ := boxes.counts(); wraps != 1 {
		t.Fatalf("boxed spawn must wrap argv exactly once, got %d", wraps)
	}
	if s.Info().Runtime != config.RuntimeBox {
		t.Fatalf("Info().Runtime = %q, want box", s.Info().Runtime)
	}
}

// Without a box layer installed, containment is never assumed.
func TestSpawnWithoutBoxesFallsBackToHost(t *testing.T) {
	m := NewManager(tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}, nil, 1<<16, time.Minute)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash"})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if s.Runtime != config.RuntimeHost {
		t.Fatalf("runtime = %q, want host when no box layer is installed", s.Runtime)
	}
}

func TestKilledBoxedSessionReleasesItsBoxExactlyOnce(t *testing.T) {
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Kill(s.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, releases, _ := boxes.counts(); releases == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, releases, _ := boxes.counts(); releases != 1 {
		t.Fatalf("killed boxed session released its box %d times, want exactly 1", releases)
	}
}

// An agent that exits on its own must release too, or the box lingers past its
// TTL forever.
func TestExitedBoxedSessionReleasesItsBox(t *testing.T) {
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "exit 0"})
	if _, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, releases, _ := boxes.counts(); releases == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, releases, _ := boxes.counts()
	t.Fatalf("exited boxed session released %d times, want 1", releases)
}

// Signals do not cross `docker exec`. Killing only the host-side client would
// leave the agent running inside a box other sessions keep alive — the same
// orphan class as the abduco attach clients that once pegged a core.
func TestKillingABoxedSessionAlsoKillsTheAgentInsideTheBox(t *testing.T) {
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Kill(s.ID); err != nil {
		t.Fatal(err)
	}
	if got := boxes.killCount(); got != 1 {
		t.Fatalf("in-box kill called %d times, want exactly 1", got)
	}
}

// A host-tier session has no box to reach into.
func TestKillingAHostSessionDoesNotTouchTheBoxLayer(t *testing.T) {
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Kill(s.ID); err != nil {
		t.Fatal(err)
	}
	if got := boxes.killCount(); got != 0 {
		t.Fatalf("host session triggered %d in-box kills, want 0", got)
	}
}

// fakeBundles records what the session asked for.
type fakeBundles struct {
	askedBin string
	mounts   []box.Mount
	flags    []string
}

func (f *fakeBundles) For(bin string) ([]box.Mount, []string, string, error) {
	f.askedBin = bin
	return f.mounts, f.flags, "deadbeef", nil
}

// The bundle's flags must reach the agent, and its mounts must reach the box.
func TestBoxedSpawnAppliesTheConfigBundle(t *testing.T) {
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	bundles := &fakeBundles{
		mounts: []box.Mount{{Host: "/b/skills", Box: "/home/agent/.claude/skills"}},
		flags:  []string{"--strict-mcp-config"},
	}
	m.SetBundles(bundles)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if bundles.askedBin == "" {
		t.Fatal("the session never asked the bundle layer which layout applies")
	}
	if wraps, _, _ := boxes.counts(); wraps != 1 {
		t.Fatalf("expected one wrap, got %d", wraps)
	}
}

// A host-tier session gets no bundle: there is no box to mount anything into.
func TestHostSpawnDoesNotConsultTheBundle(t *testing.T) {
	m, _ := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	bundles := &fakeBundles{}
	m.SetBundles(bundles)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if bundles.askedBin != "" {
		t.Fatalf("host tier must not consult the bundle layer, asked for %q", bundles.askedBin)
	}
}
