package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
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

	created   bool // Prepare's "created" answer
	info      CloneInfo
	refreshes []RefreshOpts
	lives     []bool // what opts.LiveSession() answered, per Refresh
	released  int
	order     *callLog // optional: shared with fakeBoxes.order
	onRelease func()   // runs inside the release func, before the clone lock is dropped

	locks     map[string]chan struct{} // per-clone-dir lock, like the real hooks'
	entered   chan struct{}            // optional: signalled on entry to Refresh, before the lock
	afterLock func()                   // optional: runs once the lock is held, before LiveSession is asked

	deadlines []time.Time // the ctx deadline Refresh was given, per call
	returns   []returnCall
	retSum    ReturnSummary // what ReturnWork answers (Trigger is filled in)
	retEnter  chan struct{} // optional: signalled on entry to ReturnWork
	retHold   chan struct{} // optional: ReturnWork waits for this to be closed
}

// returnCall is one ReturnWork call: what the manager passed, and what the
// LiveSession it passed answered.
type returnCall struct {
	trigger string
	clone   config.Clone
	live    bool
}

func (f *fakeCloneHooks) ReturnWork(_ context.Context, c config.Clone, o ReturnOpts) ReturnSummary {
	if f.retEnter != nil {
		f.retEnter <- struct{}{}
	}
	if f.retHold != nil {
		<-f.retHold
	}
	live := o.LiveSession != nil && o.LiveSession()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.returns = append(f.returns, returnCall{trigger: o.Trigger, clone: c, live: live})
	sum := f.retSum
	sum.Trigger = o.Trigger
	return sum
}

func (f *fakeCloneHooks) returnCalls() []returnCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.returns)
}

func (f *fakeCloneHooks) HostOrigin(_ context.Context, of string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.originOf = append(f.originOf, of)
	return f.origin, f.originErr
}

func (f *fakeCloneHooks) Prepare(_ context.Context, c config.Clone, withMemory bool) (box.CloneMounts, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepares = append(f.prepares, c)
	f.withMemory = append(f.withMemory, withMemory)
	if f.prepareErr != nil {
		return box.CloneMounts{}, false, f.prepareErr
	}
	return box.CloneMounts{Hash: "h"}, f.created, nil
}

// Refresh takes the clone's lock first and only then asks LiveSession, as the
// real hooks must; release drops the lock.
func (f *fakeCloneHooks) Refresh(ctx context.Context, c config.Clone, o RefreshOpts) (CloneInfo, func()) {
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	f.mu.Lock()
	if f.locks == nil {
		f.locks = map[string]chan struct{}{}
	}
	lock := f.locks[c.Dir]
	if lock == nil {
		lock = make(chan struct{}, 1)
		f.locks[c.Dir] = lock
	}
	f.mu.Unlock()
	lock <- struct{}{}
	if f.afterLock != nil {
		f.afterLock()
	}
	live := o.LiveSession != nil && o.LiveSession()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes = append(f.refreshes, o)
	dl, _ := ctx.Deadline()
	f.deadlines = append(f.deadlines, dl)
	f.lives = append(f.lives, live)
	f.order.add("refresh")
	return f.info, func() {
		f.order.add("release")
		if f.onRelease != nil {
			f.onRelease()
		}
		f.mu.Lock()
		f.released++
		f.mu.Unlock()
		<-lock
	}
}

func (f *fakeCloneHooks) nRefreshes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.refreshes)
}

func (f *fakeCloneHooks) nReleased() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.released
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
	gh  *config.GitHubBinding
	env []string // the profile's own env, added to Build's
}

func (l cloneGHLauncher) Build(spec launcher.SpawnSpec) ([]string, []string, error) {
	argv, env, err := l.cloneLauncher.Build(spec)
	return argv, append(slices.Clone(env), l.env...), err
}

func (l cloneGHLauncher) GitHubFor(agent string) *config.GitHubBinding {
	if agent != "clone" || l.gh == nil {
		return nil
	}
	return l.gh.Clone()
}

func spawnCloneGH(t *testing.T, gh *config.GitHubBinding, hooks *fakeCloneHooks, profileEnv ...string) (*fakeBoxes, *Session, error) {
	t.Helper()
	cl, dir := testCloneLauncher(t, config.RuntimeBox)
	m := NewManager(cloneGHLauncher{cloneLauncher: cl, gh: gh, env: profileEnv}, nil, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	m.SetClone(hooks)
	m.SetBroker(&fakeBrokerHooks{})
	m.SetEgress(&fakeEgressHooks{})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err == nil {
		t.Cleanup(func() { _ = m.Kill(s.ID) })
	}
	return boxes, s, err
}

func TestSpawnCloneSetsGHRepo(t *testing.T) {
	bound := &config.GitHubBinding{Owners: []config.GitHubOwner{
		{Owner: "culiops", App: "main", Repos: []string{"agent-box", "other"}},
	}}
	// The same owner list with contents write: the App token can push.
	write := &config.GitHubBinding{Owners: bound.Owners,
		Permissions: map[string]string{"contents": "write", "metadata": "read"}}
	pat := &config.GitHubBinding{Owners: []config.GitHubOwner{
		{Owner: "culiops", Token: "gh-pat", Repos: []string{"agent-box"}},
	}}
	const rewriteKey = "GIT_CONFIG_KEY_2=url.https://github.com/culiops/agent-box.git.pushInsteadOf"
	origin := "https://github.com/culiops/agent-box.git"
	cases := []struct {
		name         string
		hooks        *fakeCloneHooks
		binds        *config.GitHubBinding
		profileEnv   []string
		want         []string
		wantPush     []string // GIT_CONFIG_KEY_2 / GIT_CONFIG_VALUE_2 / COUNT, in env order
		wantPushRepo string
		wantOf       bool // HostOrigin must have been asked about clone_of
	}{
		{"origin in binding", &fakeCloneHooks{origin: origin}, bound, nil,
			[]string{"GH_REPO=culiops/agent-box"}, []string{"GIT_CONFIG_COUNT=2"}, "", true},
		{"origin in binding, contents write", &fakeCloneHooks{origin: origin}, write, nil,
			[]string{"GH_REPO=culiops/agent-box"},
			[]string{"GIT_CONFIG_COUNT=3", rewriteKey, "GIT_CONFIG_VALUE_2=/home/u/repo"}, "culiops/agent-box", true},
		{"origin in binding, PAT owner", &fakeCloneHooks{origin: origin}, pat, nil,
			[]string{"GH_REPO=culiops/agent-box"},
			[]string{"GIT_CONFIG_COUNT=3", rewriteKey, "GIT_CONFIG_VALUE_2=/home/u/repo"}, "culiops/agent-box", true},
		{"profile env cannot keep its own index 2", &fakeCloneHooks{origin: origin}, write,
			[]string{"GIT_CONFIG_KEY_2=core.sshCommand", "GIT_CONFIG_VALUE_2=evil"},
			[]string{"GH_REPO=culiops/agent-box"},
			[]string{"GIT_CONFIG_COUNT=3", rewriteKey, "GIT_CONFIG_VALUE_2=/home/u/repo"}, "culiops/agent-box", true},
		{"profile env index 2 dies without a push route", &fakeCloneHooks{origin: origin}, bound,
			[]string{"GIT_CONFIG_KEY_2=core.sshCommand", "GIT_CONFIG_VALUE_2=evil"},
			[]string{"GH_REPO=culiops/agent-box"}, []string{"GIT_CONFIG_COUNT=2"}, "", true},
		{"ssh form, other case", &fakeCloneHooks{origin: "git@github.com:Culiops/Agent-Box.git"}, bound, nil,
			[]string{"GH_REPO=Culiops/Agent-Box"}, []string{"GIT_CONFIG_COUNT=2"}, "", true},
		{"repo not in binding", &fakeCloneHooks{origin: "https://github.com/culiops/secret"}, write, nil,
			nil, []string{"GIT_CONFIG_COUNT=2"}, "", true},
		{"owner not in binding", &fakeCloneHooks{origin: "https://github.com/evil/agent-box"}, write, nil,
			nil, []string{"GIT_CONFIG_COUNT=2"}, "", true},
		{"not a github origin", &fakeCloneHooks{origin: "/srv/git/agent-box.git"}, write, nil,
			nil, []string{"GIT_CONFIG_COUNT=2"}, "", true},
		{"no origin", &fakeCloneHooks{}, write, nil, nil, []string{"GIT_CONFIG_COUNT=2"}, "", true},
		{"origin read fails", &fakeCloneHooks{originErr: errors.New("boom")}, write, nil,
			nil, []string{"GIT_CONFIG_COUNT=2"}, "", true},
		{"no github binding", &fakeCloneHooks{origin: origin}, nil, nil, nil, nil, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			boxes, s, err := spawnCloneGH(t, c.binds, c.hooks, c.profileEnv...)
			if err != nil {
				t.Fatalf("spawn must succeed whatever the origin says: %v", err)
			}
			if got := envNamed(boxes.lastEnv, "GH_REPO"); !slices.Equal(got, c.want) {
				t.Fatalf("GH_REPO env = %q, want %q", got, c.want)
			}
			var gotPush []string
			for _, name := range []string{"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_2", "GIT_CONFIG_VALUE_2"} {
				gotPush = append(gotPush, envNamed(boxes.lastEnv, name)...)
			}
			if !slices.Equal(gotPush, c.wantPush) {
				t.Fatalf("git config env = %q, want %q", gotPush, c.wantPush)
			}
			if got := s.Info().Clone.PushRepo; got != c.wantPushRepo {
				t.Fatalf("Clone.PushRepo = %q, want %q", got, c.wantPushRepo)
			}
			if c.wantOf && !slices.Equal(c.hooks.originOf, []string{"/home/u/repo"}) {
				t.Fatalf("HostOrigin asked about %q, want the host repo only (once)", c.hooks.originOf)
			}
			if !c.wantOf && len(c.hooks.originOf) != 0 {
				t.Fatalf("HostOrigin called for a session without a GitHub binding: %q", c.hooks.originOf)
			}
		})
	}
}

func TestWithGitPushRewrite(t *testing.T) {
	const key = "GIT_CONFIG_KEY_2=url.https://github.com/o/r.git.pushInsteadOf"
	cases := []struct {
		name string
		env  []string
		want []string
	}{
		{"extends the count and appends the pair",
			[]string{"A=1", "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=a"},
			[]string{"A=1", "GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_0=a", key, "GIT_CONFIG_VALUE_2=/of"}},
		{"drops an existing pair at the new index",
			[]string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_2=core.sshCommand", "GIT_CONFIG_VALUE_2=evil"},
			[]string{"GIT_CONFIG_COUNT=3", key, "GIT_CONFIG_VALUE_2=/of"}},
		{"no count: unchanged", []string{"A=1", "GIT_CONFIG_KEY_2=x"}, []string{"A=1", "GIT_CONFIG_KEY_2=x"}},
		{"count not a number: unchanged", []string{"GIT_CONFIG_COUNT=two"}, []string{"GIT_CONFIG_COUNT=two"}},
		{"negative count: unchanged", []string{"GIT_CONFIG_COUNT=-1"}, []string{"GIT_CONFIG_COUNT=-1"}},
	}
	for _, c := range cases {
		in := slices.Clone(c.env)
		got := withGitPushRewrite(in, "/of", "o/r")
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
		if !slices.Equal(in, c.env) {
			t.Errorf("%s: the input slice was modified: %q", c.name, in)
		}
	}
}

func TestCloneSpawnRefreshesAfterPrepare(t *testing.T) {
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	order := &callLog{}
	hooks := &fakeCloneHooks{created: true, order: order}
	m, boxes := cloneManager(t, l, hooks)
	boxes.order = order
	hooks.onRelease = func() {
		// The session is published before the lock is released: m.mu is free
		// here because Spawn calls release after Unlock.
		m.mu.Lock()
		n := len(m.sessions)
		m.mu.Unlock()
		if n != 1 {
			t.Errorf("at release time m.sessions holds %d sessions, want the new one", n)
		}
	}
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })

	if hooks.nRefreshes() != 1 {
		t.Fatalf("Refresh called %d times, want 1", hooks.nRefreshes())
	}
	if !hooks.refreshes[0].Created {
		t.Fatal("RefreshOpts.Created must carry Prepare's answer")
	}
	// "prepare" is the box layer's own Prepare, which runs before the clone's.
	var got []string
	for _, c := range order.got() {
		if c == "refresh" || c == "wrap" || c == "release" {
			got = append(got, c)
		}
	}
	if want := []string{"refresh", "wrap", "release"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if hooks.nReleased() != 1 {
		t.Fatalf("released %d times, want 1", hooks.nReleased())
	}
}

func TestCloneSpawnLiveSessionFlag(t *testing.T) {
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	hooks := &fakeCloneHooks{}
	m, _ := cloneManager(t, l, hooks)
	s1, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	_ = m.Kill(s1.ID)
	_ = m.Kill(s2.ID)
	s3, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s3.ID) })
	if len(hooks.lives) != 3 {
		t.Fatalf("Refresh called %d times, want 3", len(hooks.lives))
	}
	for i, want := range []bool{false, true, false} {
		if got := hooks.lives[i]; got != want {
			t.Errorf("spawn %d: LiveSession = %v, want %v", i+1, got, want)
		}
	}
}

func TestCloneSpawnReleasesOnWrapFailure(t *testing.T) {
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	hooks := &fakeCloneHooks{}
	m, boxes := cloneManager(t, l, hooks)
	boxes.wrapErr = errors.New("docker said no")
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("a failed Wrap still spawned")
	}
	if hooks.nRefreshes() != 1 || hooks.nReleased() != 1 {
		t.Fatalf("refreshes=%d released=%d, want 1 and 1", hooks.nRefreshes(), hooks.nReleased())
	}
}

func TestCloneSpawnReleasesOnEarlyFailure(t *testing.T) {
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	hooks := &fakeCloneHooks{}
	m, boxes := cloneManager(t, l, hooks)
	m.SetBoxedRecorder(func(string) error { return errors.New("x") })
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("a failed recorder still spawned")
	}
	if boxes.wraps != 0 {
		t.Fatal("Wrap ran after the recorder refused")
	}
	if hooks.nReleased() != 1 {
		t.Fatalf("released %d times, want 1", hooks.nReleased())
	}
}

func TestCloneSpawnRefreshErrorDoesNotBlock(t *testing.T) {
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	hooks := &fakeCloneHooks{info: CloneInfo{Error: "boom"}}
	m, _ := cloneManager(t, l, hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if c := s.Info().Clone; c == nil || c.Error != "boom" {
		t.Fatalf("Info().Clone = %+v, want Error boom", c)
	}
}

func TestCloneInfoOnTheWire(t *testing.T) {
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	no := false
	hooks := &fakeCloneHooks{info: CloneInfo{Refreshed: true, Base: "main", Branch: "feat/x", Ahead: 2, Behind: 0, HeadInHost: &no, StaleBranches: []string{"old"}}}
	m, _ := cloneManager(t, l, hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	b, err := json.Marshal(s.Info())
	if err != nil {
		t.Fatal(err)
	}
	want := `"clone":{"refreshed":true,"base":"main","branch":"feat/x","ahead":2,"behind":0,"head_in_host":false,"stale_branches":["old"]}`
	if !strings.Contains(string(b), want) {
		t.Fatalf("wire shape = %s, want it to contain %s", b, want)
	}
	// Info hands out a copy: mutating it must not reach the session.
	in := s.Info()
	in.Clone.StaleBranches[0] = "changed"
	*in.Clone.HeadInHost = true
	if again := s.Info().Clone; again.StaleBranches[0] != "old" || *again.HeadInHost {
		t.Fatalf("Info().Clone aliases the session's: %+v", again)
	}
}

func TestCloneInfoSurvivesRestore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	yes := true
	want := CloneInfo{Refreshed: true, Base: "master", Branch: "b", Ahead: 1, Behind: 3, FFFrom: "a", FFTo: "b", FFCount: 3,
		HeadInHost: &yes, StaleBranches: []string{"x", "y"}, Rewritten: true, Unverified: true}
	hooks := &fakeCloneHooks{info: want}
	fb := newFakeBackend()
	m := NewManager(l, fb, 1<<16, time.Minute)
	if err := m.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	m.SetBoxes(&fakeBoxes{})
	m.SetClone(hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if got := sidecarEntry(t, path, s.ID).Clone; !reflect.DeepEqual(got, &want) {
		t.Fatalf("sidecar Clone = %+v, want %+v", got, &want)
	}
	fb.seedLive(s.ID)
	m2 := NewManager(l, fb, 1<<16, time.Minute)
	if err := m2.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	m2.SetBoxes(&fakeBoxes{})
	if err := m2.Reconcile(); err != nil {
		t.Fatal(err)
	}
	r, ok := m2.Get(s.ID)
	if !ok {
		t.Fatal("session not reattached")
	}
	t.Cleanup(func() { _ = m2.Kill(r.ID) })
	if got := r.Info().Clone; !reflect.DeepEqual(got, &want) {
		t.Fatalf("restored Clone = %+v, want %+v", got, &want)
	}
}

func TestNonCloneSpawnHasNoCloneInfo(t *testing.T) {
	for _, rt := range []string{config.RuntimeBox, config.RuntimeHost} {
		l := cloneLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: rt}}
		hooks := &fakeCloneHooks{}
		m, _ := cloneManager(t, l, hooks)
		s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: t.TempDir()})
		if err != nil {
			t.Fatalf("%s: %v", rt, err)
		}
		if hooks.nRefreshes() != 0 {
			t.Errorf("%s: Refresh called for a non-clone spawn", rt)
		}
		if s.Info().Clone != nil {
			t.Errorf("%s: Info().Clone = %+v, want nil", rt, s.Info().Clone)
		}
		_ = m.Kill(s.ID)
	}
}

// The ended session is still in m.sessions (retention) when Restart spawns its
// replacement: an exited session must not count as live, or the daily
// kill-then-respawn loop would never refresh.
func TestRestartRefreshesAgain(t *testing.T) {
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	hooks := &fakeCloneHooks{}
	m, _ := cloneManager(t, l, hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.signal(syscall.SIGTERM)
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not exit")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !s.Exited() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	ns, err := m.Restart(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(ns.ID) })
	if len(hooks.refreshes) != 2 {
		t.Fatalf("Refresh called %d times, want 2", len(hooks.refreshes))
	}
	if len(hooks.lives) != 2 || hooks.lives[1] {
		t.Fatal("the exited session counted as live on restart")
	}
}

// Spawn A holds the clone lock and is not yet published; spawn B is already
// waiting for that lock. B must ask LiveSession only after it gets the lock,
// when A is published, so it sees A as live and does not refresh over A's box.
func TestCloneSpawnLiveSessionSeenAfterConcurrentPublish(t *testing.T) {
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	hooks := &fakeCloneHooks{entered: make(chan struct{}, 2)}
	m, _ := cloneManager(t, l, hooks)

	unblockA := make(chan struct{})
	var first sync.Once
	hooks.afterLock = func() {
		first.Do(func() { <-unblockA }) // only the spawn that gets the lock first waits
	}

	type result struct {
		s   *Session
		err error
	}
	spawn := func() chan result {
		ch := make(chan result, 1)
		go func() {
			s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
			ch <- result{s, err}
		}()
		return ch
	}
	a := spawn()
	<-hooks.entered // A is inside Refresh and holds the lock
	b := spawn()
	<-hooks.entered // B has reached Refresh; it can only wait on A's lock
	close(unblockA)

	for _, ch := range []chan result{a, b} {
		select {
		case r := <-ch:
			if r.err != nil {
				t.Fatal(r.err)
			}
			t.Cleanup(func() { _ = m.Kill(r.s.ID) })
		case <-time.After(10 * time.Second):
			t.Fatal("spawn did not finish")
		}
	}
	hooks.mu.Lock()
	defer hooks.mu.Unlock()
	if len(hooks.lives) != 2 || hooks.lives[0] || !hooks.lives[1] {
		t.Fatalf("LiveSession answers = %v, want [false true]: B must see A's published session", hooks.lives)
	}
}

// argvLauncher builds an argv that shows what the session layer asked for: the
// bin, the extra args, then "--" and a first message, like the real Build.
type argvLauncher struct {
	cloneGHLauncher
	bin string
}

func (l argvLauncher) BinFor(launcher.SpawnSpec) (string, error) { return l.bin, nil }

func (l argvLauncher) Build(spec launcher.SpawnSpec) ([]string, []string, error) {
	_, env, err := l.cloneGHLauncher.Build(spec)
	argv := append([]string{l.bin}, spec.ExtraArgs...)
	return append(argv, "--", "first message"), env, err
}

func spawnGuidance(t *testing.T, bin, agent string, noAutoReturn bool, gh *config.GitHubBinding) []string {
	t.Helper()
	cl, dir := testCloneLauncher(t, config.RuntimeBox)
	cl.clone.NoAutoReturn = noAutoReturn
	m := NewManager(argvLauncher{cloneGHLauncher: cloneGHLauncher{cloneLauncher: cl, gh: gh}, bin: bin}, nil, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	m.SetClone(&fakeCloneHooks{origin: "https://github.com/culiops/agent-box.git"})
	m.SetBroker(&fakeBrokerHooks{})
	m.SetEgress(&fakeEgressHooks{})
	if agent != "clone" {
		dir = t.TempDir()
	}
	s, err := m.Spawn(launcher.SpawnSpec{Agent: agent, Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	return boxes.lastArgv
}

func TestCloneSpawnClaudeGetsGuidance(t *testing.T) {
	write := &config.GitHubBinding{
		Owners:      []config.GitHubOwner{{Owner: "culiops", App: "main", Repos: []string{"agent-box"}}},
		Permissions: map[string]string{"contents": "write", "metadata": "read"},
	}
	read := &config.GitHubBinding{Owners: write.Owners}

	t.Run("claude clone spawn with push gets the push line before --", func(t *testing.T) {
		argv := spawnGuidance(t, "claude", "clone", false, write)
		i := slices.Index(argv, "--append-system-prompt")
		sep := slices.Index(argv, "--")
		if i < 0 || i+1 >= len(argv) || sep < 0 || i+1 > sep-1 {
			t.Fatalf("argv = %q: want --append-system-prompt and its value before --", argv)
		}
		if want := cloneGuidance("/home/u/repo", true, true); argv[i+1] != want {
			t.Fatalf("value = %q, want %q", argv[i+1], want)
		}
	})
	t.Run("no push route and auto return off", func(t *testing.T) {
		argv := spawnGuidance(t, "claude", "clone", true, read)
		if got, want := argAfter(argv, "--append-system-prompt"), cloneGuidance("/home/u/repo", false, false); got != want {
			t.Fatalf("value = %q, want %q", got, want)
		}
	})
	t.Run("a shell in the clone gets no guidance", func(t *testing.T) {
		if argv := spawnGuidance(t, "bash", "clone", false, write); slices.Contains(argv, "--append-system-prompt") {
			t.Fatalf("argv = %q", argv)
		}
	})
	t.Run("a boxed claude that is not a clone gets no guidance", func(t *testing.T) {
		if argv := spawnGuidance(t, "claude", "plain", false, nil); slices.Contains(argv, "--append-system-prompt") {
			t.Fatalf("argv = %q", argv)
		}
	})
}
