package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	lastAcct string // account the last Wrap was called with
	lastKey  box.BoxKey
	lastEnv  []string
	lastArgv []string
	lastRO   []box.Mount
	releases int
	adopts   int
	kills    int
	adopted  []string

	wrapErr    error // returned by Wrap when set
	prepares   int
	prepareErr error
	order      *callLog // optional: shared with fakeBundles to pin call order
}

// callLog records the order of calls across fakes.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (c *callLog) add(s string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, s)
}

func (c *callLog) got() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

func (f *fakeBoxes) Prepare(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepares++
	f.order.add("prepare")
	return f.prepareErr
}

func (f *fakeBoxes) Wrap(_ context.Context, _ string, _ string, key box.BoxKey, argv, env []string, extraRO []box.Mount) ([]string, []string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wraps++
	f.lastAcct = key.Account
	f.lastKey = key
	f.lastEnv = append([]string(nil), env...)
	f.lastArgv = append([]string(nil), argv...)
	f.lastRO = append([]box.Mount(nil), extraRO...)
	f.order.add("wrap")
	if f.wrapErr != nil {
		return nil, nil, "", f.wrapErr
	}
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
	order    *callLog // optional: shared with fakeBoxes to pin call order
	askedBin string
	mounts   []box.Mount
	flags    []string
}

func (f *fakeBundles) For(bin string) ([]box.Mount, []string, string, error) {
	f.askedBin = bin
	f.order.add("bundle")
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

// Prepare runs before the bundle is built and before Wrap, and a Prepare
// failure stops both: the bundle's MCP probe needs the daemon Prepare readies.
func TestBoxedSpawnPreparesBeforeBundleAndWrap(t *testing.T) {
	order := &callLog{}
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	boxes.order = order
	m.SetBundles(&fakeBundles{order: order})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if got, want := order.got(), []string{"prepare", "bundle", "wrap"}; len(got) != 3 ||
		got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("call order = %v, want %v", got, want)
	}

	order = &callLog{}
	m2, boxes2 := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	boxes2.order = order
	boxes2.prepareErr = fmt.Errorf("%w: box VM not set up", box.ErrBoxUnavailable)
	bundles := &fakeBundles{order: order}
	m2.SetBundles(bundles)
	if _, err := m2.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox}); !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("want ErrBoxUnavailable, got %v", err)
	}
	if got := order.got(); len(got) != 1 || got[0] != "prepare" {
		t.Fatalf("a failed Prepare must stop the bundle and Wrap, calls = %v", got)
	}
}

// A host-tier spawn never prepares the box layer.
func TestHostSpawnDoesNotPrepare(t *testing.T) {
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if boxes.prepares != 0 {
		t.Fatalf("host tier must not Prepare, got %d", boxes.prepares)
	}
}

// A containment-check failure from Wrap reaches the user exactly as the box
// layer worded it: no session prefix and no hint to run on the host instead.
func TestBoxUnavailableFromWrapPassesThroughUnwrapped(t *testing.T) {
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	boxes.wrapErr = fmt.Errorf("%w: new box reached the host loopback", box.ErrBoxUnavailable)
	_, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox})
	if !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("want ErrBoxUnavailable, got %v", err)
	}
	if err.Error() != boxes.wrapErr.Error() {
		t.Fatalf("error = %q, want it passed through unchanged (%q)", err, boxes.wrapErr)
	}
	if strings.Contains(err.Error(), "spawn with runtime") {
		t.Fatalf("must not suggest the host tier: %v", err)
	}
}

// The sweep judges a box by what the registry says is still running: a boxed
// session counts until it exits, a host session never does.
func TestLiveBoxCountsCountsOnlyUnexitedBoxedSessions(t *testing.T) {
	m, _ := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	a, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(a.ID)
	b, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	h, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(h.ID)

	if got := m.LiveBoxCounts(); got["ab-box-test"] != 2 || len(got) != 1 {
		t.Fatalf("want {ab-box-test:2}, got %v", got)
	}
	if err := m.Kill(b.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("killed session never exited")
	}
	if got := m.LiveBoxCounts(); got["ab-box-test"] != 1 || len(got) != 1 {
		t.Fatalf("want {ab-box-test:1} after one exits, got %v", got)
	}
}
