package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/account"
	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/launcher"
)

// cloneLauncher is a tieredLauncher whose profile "clone" has a clone spec, and
// whose other profiles have none. acct, when set, is the resolved account.
type cloneLauncher struct {
	tieredLauncher
	clone    *config.Clone
	cloneErr error
	acct     *account.Resolved
}

func (l cloneLauncher) CloneFor(agent string) (*config.Clone, error) {
	if agent != "clone" {
		return nil, nil
	}
	if l.cloneErr != nil {
		return nil, l.cloneErr
	}
	if l.clone == nil {
		return nil, nil
	}
	c := *l.clone
	return &c, nil
}

func (l cloneLauncher) AccountFor(launcher.SpawnSpec) (account.Resolved, error) {
	if l.acct != nil {
		return *l.acct, nil
	}
	return account.DefaultResolved(""), nil
}

// fakeCloneHooks records Prepare and BoxOwned calls and returns fixed answers.
type fakeCloneHooks struct {
	mu          sync.Mutex
	prepares    []config.Clone
	withMemory  []bool
	prepareErr  error
	owned       string // non-empty = BoxOwned reports this reason
	ownedChecks []string
	origin      string // HostOrigin's answer
	originErr   error
	originOf    []string // the clone_of paths HostOrigin was asked about
}

func (f *fakeCloneHooks) HostOrigin(_ context.Context, of string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.originOf = append(f.originOf, of)
	return f.origin, f.originErr
}

func (f *fakeCloneHooks) Prepare(_ context.Context, c config.Clone, withMemory bool) (box.CloneMounts, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepares = append(f.prepares, c)
	f.withMemory = append(f.withMemory, withMemory)
	if f.prepareErr != nil {
		return box.CloneMounts{}, f.prepareErr
	}
	return box.CloneMounts{Hash: "h"}, nil
}

func (f *fakeCloneHooks) BoxOwned(cwd string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ownedChecks = append(f.ownedChecks, cwd)
	return f.owned, f.owned != ""
}

func (f *fakeCloneHooks) nPrepares() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.prepares)
}

func (f *fakeCloneHooks) nOwnedChecks() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.ownedChecks)
}

func cloneManager(t *testing.T, l cloneLauncher, hooks CloneHooks) (*Manager, *fakeBoxes) {
	t.Helper()
	m := NewManager(l, nil, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	if hooks != nil {
		m.SetClone(hooks)
	}
	return m, boxes
}

func testCloneLauncher(t *testing.T, runtime string) (cloneLauncher, string) {
	t.Helper()
	// The dir exists because the fake Prepare creates nothing, and the started
	// process needs a cwd; the real hooks create the clone before Wrap.
	dir := filepath.Join(t.TempDir(), "clone")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return cloneLauncher{
		tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: runtime},
		clone:          &config.Clone{Of: "/home/u/repo", Dir: dir, Shares: []string{"CLAUDE.md"}},
	}, dir
}

func TestCloneSpawnPreparesAndKeysTheBox(t *testing.T) {
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	hooks := &fakeCloneHooks{}
	m, boxes := cloneManager(t, l, hooks)

	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })

	if hooks.nPrepares() != 1 {
		t.Fatalf("Prepare called %d times, want 1", hooks.nPrepares())
	}
	if got := hooks.prepares[0]; got.Of != "/home/u/repo" || got.Dir != dir {
		t.Fatalf("Prepare got %+v", got)
	}
	if !hooks.withMemory[0] {
		t.Fatal("the default account must get the memory mount")
	}
	if boxes.lastKey.Clone == nil || boxes.lastKey.Clone.Hash != "h" {
		t.Fatalf("Wrap key.Clone = %+v, want hash h", boxes.lastKey.Clone)
	}
	if s.Cwd != dir {
		t.Fatalf("session cwd = %q, want the clone dir %q", s.Cwd, dir)
	}
	if hooks.nOwnedChecks() != 0 {
		t.Fatal("a clone spawn must not be asked whether its own dir is box-owned")
	}
}

func TestCloneSpawnAcceptsAnUncleanSpelling(t *testing.T) {
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	hooks := &fakeCloneHooks{}
	m, _ := cloneManager(t, l, hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir + "/"})
	if err != nil {
		t.Fatal(err)
	}
	_ = m.Kill(s.ID)
}

func TestCloneSpawnNonDefaultAccountGetsNoMemory(t *testing.T) {
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	w := workAccount(false)
	l.acct = &w
	hooks := &fakeCloneHooks{}
	m, _ := cloneManager(t, l, hooks)

	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if hooks.nPrepares() != 1 || hooks.withMemory[0] {
		t.Fatalf("prepares=%d withMemory=%v, want 1 call without memory", hooks.nPrepares(), hooks.withMemory)
	}
}

func TestCloneSpawnRefusedOnHostTier(t *testing.T) {
	for _, c := range []struct{ name, tier, override string }{
		{"override", config.RuntimeBox, config.RuntimeHost},
		{"profile tier", config.RuntimeHost, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			l, dir := testCloneLauncher(t, c.tier)
			hooks := &fakeCloneHooks{}
			m, boxes := cloneManager(t, l, hooks)
			s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir, Runtime: c.override})
			if err == nil {
				_ = m.Kill(s.ID)
				t.Fatal("a clone profile spawned on the host tier")
			}
			if !strings.Contains(err.Error(), "clone-mode profile") || !strings.Contains(err.Error(), "never on the host tier") {
				t.Fatalf("err = %v", err)
			}
			if !errors.Is(err, box.ErrUnboxableWorkspace) {
				t.Fatalf("err = %v, want ErrUnboxableWorkspace so the UI shows it", err)
			}
			if hooks.nPrepares() != 0 || boxes.wraps != 0 {
				t.Fatalf("refused spawn prepared=%d wrapped=%d", hooks.nPrepares(), boxes.wraps)
			}
			if len(m.List()) != 0 {
				t.Fatal("a refused spawn left a session")
			}
		})
	}
}

func TestCloneSpawnRefusedWhenContainmentIsOff(t *testing.T) {
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	hooks := &fakeCloneHooks{}
	m, _ := cloneManager(t, l, hooks)
	m.SetBoxes(nil) // runtimeFor falls back to host
	if s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir}); err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("a clone profile spawned with containment unavailable")
	}
	if hooks.nPrepares() != 0 {
		t.Fatal("Prepare ran on a refused spawn")
	}
}

func TestCloneSpawnRefusedOutsideItsClone(t *testing.T) {
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	hooks := &fakeCloneHooks{}
	m, boxes := cloneManager(t, l, hooks)
	for _, cwd := range []string{filepath.Join(dir, "internal"), filepath.Dir(dir), "", filepath.Join(dir, "..", "other")} {
		s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: cwd})
		if err == nil {
			_ = m.Kill(s.ID)
			t.Fatalf("cwd %q spawned", cwd)
		}
		if !errors.Is(err, box.ErrUnboxableWorkspace) {
			t.Fatalf("cwd %q: err = %v, want ErrUnboxableWorkspace", cwd, err)
		}
		if !strings.Contains(err.Error(), dir) {
			t.Fatalf("cwd %q: the message must name the clone dir: %v", cwd, err)
		}
	}
	if hooks.nPrepares() != 0 || boxes.wraps != 0 {
		t.Fatalf("refused spawns prepared=%d wrapped=%d", hooks.nPrepares(), boxes.wraps)
	}
}

func TestCloneSpawnRefusedWithoutHooks(t *testing.T) {
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	m, boxes := cloneManager(t, l, nil)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("a clone profile spawned without clone hooks")
	}
	if !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("err = %v, want ErrBoxUnavailable", err)
	}
	if boxes.wraps != 0 {
		t.Fatal("Wrap ran on a refused spawn")
	}
}

func TestCloneSpawnPrepareErrorIsReturnedAsIs(t *testing.T) {
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	want := box.ErrUnboxableWorkspace
	hooks := &fakeCloneHooks{prepareErr: want}
	m, boxes := cloneManager(t, l, hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("a failed Prepare still spawned")
	}
	if err != want {
		t.Fatalf("err = %v, want it unwrapped", err)
	}
	if boxes.wraps != 0 {
		t.Fatal("Wrap ran after a failed Prepare")
	}
}

func TestCloneSpawnBadCloneConfigIsRefused(t *testing.T) {
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	l.cloneErr = errors.New("clone_of must be absolute")
	hooks := &fakeCloneHooks{}
	m, boxes := cloneManager(t, l, hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("a profile with an invalid clone spawned")
	}
	if !strings.Contains(err.Error(), `profile "clone"`) || !strings.Contains(err.Error(), "clone_of must be absolute") {
		t.Fatalf("err = %v", err)
	}
	if !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("err = %v, want ErrBoxUnavailable so the UI shows it", err)
	}
	if hooks.nPrepares() != 0 || boxes.wraps != 0 {
		t.Fatal("a refused spawn went on")
	}
}

func TestHostSpawnInBoxOwnedDirRefused(t *testing.T) {
	l := cloneLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeHost}}
	hooks := &fakeCloneHooks{owned: "/c/clone is inside the clone of a clone-mode profile"}
	m, boxes := cloneManager(t, l, hooks)

	s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: t.TempDir()})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("a host-tier spawn in a box-owned dir started")
	}
	if !errors.Is(err, box.ErrBoxOwned) || !strings.Contains(err.Error(), "inside the clone") {
		t.Fatalf("err = %v, want ErrBoxOwned with the reason", err)
	}
	if hooks.nOwnedChecks() != 1 {
		t.Fatalf("BoxOwned asked %d times", hooks.nOwnedChecks())
	}
	if len(m.List()) != 0 || boxes.wraps != 0 {
		t.Fatal("a refused spawn left a session")
	}
}

func TestHostSpawnOutsideBoxOwnedDirStarts(t *testing.T) {
	l := cloneLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeHost}}
	hooks := &fakeCloneHooks{}
	m, _ := cloneManager(t, l, hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	_ = m.Kill(s.ID)
	if hooks.nOwnedChecks() != 1 {
		t.Fatalf("a host spawn must consult BoxOwned once, got %d", hooks.nOwnedChecks())
	}
}

func TestBoxedNonCloneSpawnNeverAsksBoxOwned(t *testing.T) {
	l := cloneLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}}
	hooks := &fakeCloneHooks{owned: "would refuse"}
	m, _ := cloneManager(t, l, hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("a boxed spawn in a box-owned dir must work (the box is the owner's): %v", err)
	}
	_ = m.Kill(s.ID)
	if hooks.nOwnedChecks() != 0 {
		t.Fatal("a boxed non-clone spawn consulted BoxOwned")
	}
}

func TestNonCloneBoxedSpawnHasNilCloneKey(t *testing.T) {
	l := cloneLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}}
	hooks := &fakeCloneHooks{}
	m, boxes := cloneManager(t, l, hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if boxes.lastKey.Clone != nil {
		t.Fatalf("key.Clone = %+v, want nil", boxes.lastKey.Clone)
	}
	if hooks.nPrepares() != 0 {
		t.Fatal("Prepare ran for a non-clone spawn")
	}
}

func TestHostSpawnWithoutHooksIsUnchanged(t *testing.T) {
	l := cloneLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeHost}}
	m, _ := cloneManager(t, l, nil)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	_ = m.Kill(s.ID)
}

// Restart re-enters Spawn with the session's own cwd and runtime, so the clone
// refusals apply to it: a session whose profile is later changed to a clone
// elsewhere cannot be restarted outside that clone.
func TestRestartReentersTheCloneRefusals(t *testing.T) {
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	hooks := &fakeCloneHooks{}
	m, _ := cloneManager(t, l, hooks)
	old := &Session{ID: "clone-1", Agent: "clone", name: "c", Cwd: filepath.Join(dir, "sub"), Runtime: config.RuntimeBox, status: statusExited}
	m.mu.Lock()
	m.sessions[old.ID] = old
	m.mu.Unlock()
	_, err := m.Restart(old.ID)
	if !errors.Is(err, box.ErrUnboxableWorkspace) {
		t.Fatalf("Restart err = %v, want ErrUnboxableWorkspace", err)
	}
	if hooks.nPrepares() != 0 {
		t.Fatal("Prepare ran for a refused restart")
	}
}

// A relative cwd never compares as under an absolute clone dir, but the process
// resolves it against the daemon's cwd: BoxOwned must see the absolute path.
func TestHostSpawnRelativeCwdIsCheckedAbsolute(t *testing.T) {
	l := cloneLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeHost}}
	hooks := &fakeCloneHooks{owned: "inside a clone"}
	m, boxes := cloneManager(t, l, hooks)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join("..", filepath.Base(wd)) // resolves to wd, but is spelled relatively
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: rel})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("a relative cwd bypassed the box-owned check")
	}
	if !errors.Is(err, box.ErrBoxOwned) {
		t.Fatalf("err = %v, want ErrBoxOwned", err)
	}
	if len(hooks.ownedChecks) != 1 || hooks.ownedChecks[0] != wd {
		t.Fatalf("BoxOwned saw %q, want the absolute %q", hooks.ownedChecks, wd)
	}
	if len(m.List()) != 0 || boxes.wraps != 0 {
		t.Fatal("a refused spawn left a session")
	}
}

// The clone-profile cwd comparison is also made on the absolute path.
func TestCloneSpawnAcceptsItsCloneSpelledRelatively(t *testing.T) {
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(wd, dir)
	if err != nil || filepath.IsAbs(rel) {
		t.Skipf("no relative spelling of %s from %s: %v", dir, wd, err)
	}
	hooks := &fakeCloneHooks{}
	m, _ := cloneManager(t, l, hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: rel})
	if err != nil {
		t.Fatal(err)
	}
	_ = m.Kill(s.ID)
}

// cloneGHLauncher gives the clone profile a GitHub binding too.
type cloneGHLauncher struct {
	cloneLauncher
	gh *config.GitHubBinding
}

func (l cloneGHLauncher) GitHubFor(agent string) *config.GitHubBinding {
	if agent != "clone" || l.gh == nil {
		return nil
	}
	return l.gh.Clone()
}

func spawnCloneGH(t *testing.T, gh *config.GitHubBinding, hooks *fakeCloneHooks) (*fakeBoxes, error) {
	t.Helper()
	cl, dir := testCloneLauncher(t, config.RuntimeBox)
	m := NewManager(cloneGHLauncher{cloneLauncher: cl, gh: gh}, nil, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	m.SetClone(hooks)
	m.SetBroker(&fakeBrokerHooks{})
	m.SetEgress(&fakeEgressHooks{})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err == nil {
		t.Cleanup(func() { _ = m.Kill(s.ID) })
	}
	return boxes, err
}

func TestSpawnCloneSetsGHRepo(t *testing.T) {
	bound := &config.GitHubBinding{Owners: []config.GitHubOwner{
		{Owner: "culiops", App: "main", Repos: []string{"agent-box", "other"}},
	}}
	cases := []struct {
		name   string
		hooks  *fakeCloneHooks
		binds  *config.GitHubBinding
		want   []string
		wantOf bool // HostOrigin must have been asked about clone_of
	}{
		{"origin in binding", &fakeCloneHooks{origin: "https://github.com/culiops/agent-box.git"}, bound, []string{"GH_REPO=culiops/agent-box"}, true},
		{"ssh form, other case", &fakeCloneHooks{origin: "git@github.com:Culiops/Agent-Box.git"}, bound, []string{"GH_REPO=Culiops/Agent-Box"}, true},
		{"repo not in binding", &fakeCloneHooks{origin: "https://github.com/culiops/secret"}, bound, nil, true},
		{"owner not in binding", &fakeCloneHooks{origin: "https://github.com/evil/agent-box"}, bound, nil, true},
		{"not a github origin", &fakeCloneHooks{origin: "/srv/git/agent-box.git"}, bound, nil, true},
		{"no origin", &fakeCloneHooks{}, bound, nil, true},
		{"origin read fails", &fakeCloneHooks{originErr: errors.New("boom")}, bound, nil, true},
		{"no github binding", &fakeCloneHooks{origin: "https://github.com/culiops/agent-box"}, nil, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			boxes, err := spawnCloneGH(t, c.binds, c.hooks)
			if err != nil {
				t.Fatalf("spawn must succeed whatever the origin says: %v", err)
			}
			if got := envNamed(boxes.lastEnv, "GH_REPO"); !slices.Equal(got, c.want) {
				t.Fatalf("GH_REPO env = %q, want %q", got, c.want)
			}
			if c.wantOf && !slices.Equal(c.hooks.originOf, []string{"/home/u/repo"}) {
				t.Fatalf("HostOrigin asked about %q, want the host repo only", c.hooks.originOf)
			}
			if !c.wantOf && len(c.hooks.originOf) != 0 {
				t.Fatalf("HostOrigin called for a session without a GitHub binding: %q", c.hooks.originOf)
			}
		})
	}
}
