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

	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
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

	repoReturns []string        // the cwd ReturnForRepo was asked about, per call
	repoSums    []ReturnSummary // what ReturnForRepo answers
	repoDl      []time.Duration // the ctx budget ReturnForRepo was given, per call
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

func (f *fakeCloneHooks) ReturnForRepo(ctx context.Context, cwd string) []ReturnSummary {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repoReturns = append(f.repoReturns, cwd)
	if dl, ok := ctx.Deadline(); ok {
		f.repoDl = append(f.repoDl, time.Until(dl))
	}
	return slices.Clone(f.repoSums)
}

func (f *fakeCloneHooks) repoCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.repoReturns)
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
	// The refusal a web UI user reads says what to do, after the reason (new-session cwd).
	if !strings.HasSuffix(err.Error(), "inside the clone of a clone-mode profile; in the web UI, pick a folder outside the copy, such as the original repo") {
		t.Fatalf("err = %q, want the reason followed by the web UI hint", err)
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

// A Host spawn brings the clones of its repo home first, after every refusal; the
// outcome rides on the session's Info, deep-copied.
func TestSpawnHostRunsRepoReturn(t *testing.T) {
	n := 2
	l := cloneLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeHost}}
	hooks := &fakeCloneHooks{repoSums: []ReturnSummary{{Trigger: ReturnOnHost, Profile: "p", Home: []string{"box/x"}, Uncommitted: &n}}}
	m, _ := cloneManager(t, l, hooks)
	wd := t.TempDir()
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: wd})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if got := hooks.repoCalls(); len(got) != 1 || got[0] != wd {
		t.Fatalf("ReturnForRepo asked about %q, want [%s]", got, wd)
	}
	hooks.mu.Lock()
	dl := hooks.repoDl
	hooks.mu.Unlock()
	if len(dl) != 1 || dl[0] <= 0 || dl[0] > 20*time.Second {
		t.Fatalf("the return budget was %v, want within 20s", dl)
	}
	info := s.Info()
	if len(info.HostReturn) != 1 || info.HostReturn[0].Profile != "p" || info.HostReturn[0].Home[0] != "box/x" {
		t.Fatalf("Info.HostReturn = %+v", info.HostReturn)
	}
	*info.HostReturn[0].Uncommitted = 9
	info.HostReturn[0].Home[0] = "changed"
	again := s.Info().HostReturn[0]
	if again.Home[0] != "box/x" || *again.Uncommitted != 2 {
		t.Fatal("Info handed out the session's own HostReturn, not a copy")
	}
}

func TestSpawnHostReturnSkippedWhenNotHost(t *testing.T) {
	l := cloneLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}}
	hooks := &fakeCloneHooks{repoSums: []ReturnSummary{{Trigger: ReturnOnHost}}}
	m, _ := cloneManager(t, l, hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if got := hooks.repoCalls(); len(got) != 0 {
		t.Fatalf("a box spawn ran the host return: %v", got)
	}
	if len(s.Info().HostReturn) != 0 {
		t.Fatal("a box session carries a host return")
	}
}

func TestSpawnHostRefusedNeverReturns(t *testing.T) {
	l := cloneLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeHost}}
	hooks := &fakeCloneHooks{owned: "inside a clone", repoSums: []ReturnSummary{{Trigger: ReturnOnHost}}}
	m, _ := cloneManager(t, l, hooks)
	if s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: t.TempDir()}); err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("a host spawn in a box-owned dir started")
	}
	if got := hooks.repoCalls(); len(got) != 0 {
		t.Fatalf("a refused spawn ran the host return: %v", got)
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

// Restart re-enters Spawn with the session's own cwd and runtime. A real clone
// session (CloneOf set) is spawned with the profile deciding, so it cannot
// restart outside its clone dir: the clone refusals apply. An in-place session
// (no CloneOf) of a profile that later became a clone restarts in place; see
// TestRestartInPlaceOnCloneProfileStaysInPlace.
func TestRestartReentersTheCloneRefusals(t *testing.T) {
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	hooks := &fakeCloneHooks{}
	m, _ := cloneManager(t, l, hooks)
	// A clone session (CloneOf set); an in-place one restarts in place instead.
	old := &Session{ID: "clone-1", Agent: "clone", name: "c", Cwd: filepath.Join(dir, "sub"), Runtime: config.RuntimeBox, CloneOf: l.clone.Of, status: statusExited}
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

// The daemon's cwd reached through a symlink (macOS /tmp and /var, a symlinked
// home): Spawn checks the absolute path filepath.Abs makes from the logical cwd,
// so the process must run in that same directory. Handed the relative spelling,
// the kernel resolves `..` from the PHYSICAL cwd and lands elsewhere (#313).
func TestSpawnRunsInTheCwdItChecked(t *testing.T) {
	cases := []struct {
		name    string
		agent   string
		runtime string
		clone   bool
		abs     bool // cwd spelled root/link/../work instead of ../work
	}{
		{"clone profile", "clone", config.RuntimeBox, true, false},
		{"host tier", "plain", config.RuntimeHost, false, false},
		{"clone profile, absolute", "clone", config.RuntimeBox, true, true},
		{"host tier, absolute", "plain", config.RuntimeHost, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			// root/link -> root/deep/wd: from the link, `../work` is root/work
			// logically but root/deep/work physically, and both exist.
			work, decoy := filepath.Join(root, "work"), filepath.Join(root, "deep", "work")
			for _, d := range []string{work, decoy, filepath.Join(root, "deep", "wd")} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(filepath.Join(root, "deep", "wd"), filepath.Join(root, "link")); err != nil {
				t.Fatal(err)
			}
			t.Chdir(filepath.Join(root, "link")) // sets PWD too, as a shell would

			l := cloneLauncher{tieredLauncher: tieredLauncher{
				argv:    []string{"sh", "-c", "pwd -P > ran-here; exec sleep 30"},
				runtime: tc.runtime,
			}}
			if tc.clone {
				l.clone = &config.Clone{Of: "/home/u/repo", Dir: work}
			}
			hooks := &fakeCloneHooks{}
			m, _ := cloneManager(t, l, hooks)
			cwd := filepath.Join("..", "work")
			if tc.abs {
				// Not filepath.Join: that would clean the `..` away.
				cwd = root + string(filepath.Separator) + "link" + string(filepath.Separator) + cwd
			}
			s, err := m.Spawn(launcher.SpawnSpec{Agent: tc.agent, Cwd: cwd})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Kill(s.ID) })
			if !tc.clone && (len(hooks.ownedChecks) != 1 || hooks.ownedChecks[0] != work) {
				t.Fatalf("BoxOwned saw %q, want %q", hooks.ownedChecks, work)
			}
			if s.Cwd != work {
				t.Errorf("session Cwd = %q, want the checked %q", s.Cwd, work)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(work, "ran-here")); err == nil {
					break
				}
				if _, err := os.Stat(filepath.Join(decoy, "ran-here")); err == nil {
					t.Fatalf("the process ran in %s, not in the checked %s", decoy, work)
				}
				if time.Now().After(deadline) {
					t.Fatal("the process never wrote its cwd")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
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

// A Host session's GitHub env already holds five entries (the helper reset,
// the helper pair and the two SSH-to-HTTPS rewrites): the push rewrite goes
// after them, at 5, and leaves them as they were.
func TestClonePushRewriteAppendsAfterHostRewrites(t *testing.T) {
	in := []string{"GIT_CONFIG_COUNT=5",
		"GIT_CONFIG_KEY_0=credential.https://github.com.helper", "GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=credential.https://github.com.helper", "GIT_CONFIG_VALUE_1=!node '/d/agentbox-cred.js' github-git",
		"GIT_CONFIG_KEY_2=credential.https://github.com.useHttpPath", "GIT_CONFIG_VALUE_2=true",
		"GIT_CONFIG_KEY_3=url.https://github.com/.insteadOf", "GIT_CONFIG_VALUE_3=git@github.com:",
		"GIT_CONFIG_KEY_4=url.https://github.com/.insteadOf", "GIT_CONFIG_VALUE_4=ssh://git@github.com/"}
	want := append([]string{"GIT_CONFIG_COUNT=6"}, in[1:]...)
	want = append(want, "GIT_CONFIG_KEY_5=url.https://github.com/o/r.git.pushInsteadOf", "GIT_CONFIG_VALUE_5=/of")
	if got := withGitPushRewrite(slices.Clone(in), "/of", "o/r"); !slices.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
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

func countArg(argv []string, arg string) int {
	n := 0
	for _, a := range argv {
		if a == arg {
			n++
		}
	}
	return n
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
		// The clone binds GitHub, so the credential note follows the clone line
		// in the same, single value.
		want := cloneGuidance("/home/u/repo", true, true) + " " + credGuidance(config.Bindings{GitHub: write})
		if argv[i+1] != want {
			t.Fatalf("value = %q, want %q", argv[i+1], want)
		}
	})
	t.Run("no push route and auto return off", func(t *testing.T) {
		argv := spawnGuidance(t, "claude", "clone", true, read)
		want := cloneGuidance("/home/u/repo", false, false) + " " + credGuidance(config.Bindings{GitHub: read})
		if got := argAfter(argv, "--append-system-prompt"); got != want {
			t.Fatalf("value = %q, want %q", got, want)
		}
		if n := countArg(argv, "--append-system-prompt"); n != 1 {
			t.Fatalf("--append-system-prompt %d times in %q", n, argv)
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

// specRecorder records the spec of every Build, so a test sees what a restart
// asked Spawn for.
type specRecorder struct {
	cloneGHLauncher
	mu    *sync.Mutex
	specs *[]launcher.SpawnSpec
}

func (l specRecorder) Build(spec launcher.SpawnSpec) ([]string, []string, error) {
	l.mu.Lock()
	*l.specs = append(*l.specs, spec)
	l.mu.Unlock()
	return l.cloneGHLauncher.Build(spec)
}

// inPlaceDir is the repo the clone profile is a clone of: it exists, so a box
// spawn can start in it.
func inPlaceDir(t *testing.T, l *cloneLauncher) string {
	t.Helper()
	a := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(a, 0o755); err != nil {
		t.Fatal(err)
	}
	c := *l.clone
	c.Of = a
	l.clone = &c
	return a
}

// clone:false drops the profile's clone setup for this spawn and nothing else:
// no clone dir, no clone_share mounts, no clone-derived GH_REPO.
func TestSpawnCloneFalseRunsInPlace(t *testing.T) {
	l, _ := testCloneLauncher(t, config.RuntimeBox)
	a := inPlaceDir(t, &l)
	hooks := &fakeCloneHooks{origin: "https://github.com/culiops/agent-box.git"}
	gh := &config.GitHubBinding{Owners: []config.GitHubOwner{{Owner: "culiops", App: "main", Repos: []string{"agent-box"}}}}
	m := NewManager(cloneGHLauncher{cloneLauncher: l, gh: gh}, nil, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	m.SetClone(hooks)
	m.SetBroker(&fakeBrokerHooks{})
	m.SetEgress(&fakeEgressHooks{})

	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Runtime: config.RuntimeBox, Clone: boolPtr(false), Cwd: a})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	info := s.Info()
	if info.CloneOf != "" || info.Clone != nil {
		t.Fatalf("CloneOf=%q Clone=%+v, want neither", info.CloneOf, info.Clone)
	}
	if info.Cwd != a {
		t.Fatalf("Cwd = %q, want %q", info.Cwd, a)
	}
	if hooks.nPrepares() != 0 || len(hooks.refreshes) != 0 {
		t.Fatalf("prepares=%d refreshes=%d, want a spawn with no clone setup", hooks.nPrepares(), len(hooks.refreshes))
	}
	if boxes.lastKey.Clone != nil {
		t.Fatalf("box key carries clone mounts %+v", boxes.lastKey.Clone)
	}
	if got := envNamed(boxes.lastEnv, "GH_REPO"); len(got) != 0 {
		t.Fatalf("GH_REPO = %q, want none derived from the clone", got)
	}
	if len(hooks.originOf) != 0 {
		t.Fatalf("the clone's origin was read: %q", hooks.originOf)
	}
	// The binding's own git config (COUNT=2) stays; the clone's push rewrite
	// (a third pair, KEY_2) does not exist.
	if got := envNamed(boxes.lastEnv, "GIT_CONFIG_KEY_2"); len(got) != 0 {
		t.Fatalf("git push rewrite env = %q, want none", got)
	}
	if got := envNamed(boxes.lastEnv, "GIT_CONFIG_COUNT"); !slices.Equal(got, []string{"GIT_CONFIG_COUNT=2"}) {
		t.Fatalf("GIT_CONFIG_COUNT = %q, want the binding's own", got)
	}
}

func TestSpawnCloneFalseOnHostAllowed(t *testing.T) {
	l, _ := testCloneLauncher(t, config.RuntimeBox)
	a := inPlaceDir(t, &l)
	hooks := &fakeCloneHooks{}
	m, _ := cloneManager(t, l, hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Runtime: config.RuntimeHost, Clone: boolPtr(false), Cwd: a})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if s.Info().Runtime != config.RuntimeHost {
		t.Fatalf("Runtime = %q, want host", s.Info().Runtime)
	}
}

// clone:false with no runtime must not put a clone profile on the host by
// omission: the tier resolves to host here (the profile's own), and the spawn
// never asked for it, so the clone-mode refusal still applies.
func TestSpawnCloneFalseWithoutRuntimeNeverReachesHost(t *testing.T) {
	l, _ := testCloneLauncher(t, config.RuntimeHost)
	a := inPlaceDir(t, &l)
	hooks := &fakeCloneHooks{}
	m, boxes := cloneManager(t, l, hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Clone: boolPtr(false), Cwd: a})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatalf("a clone profile ran on the host (runtime %q) by omission", s.Info().Runtime)
	}
	if !strings.Contains(err.Error(), "is a clone-mode profile") || !errors.Is(err, box.ErrUnboxableWorkspace) {
		t.Fatalf("err = %v, want the clone-mode refusal", err)
	}
	if boxes.wraps != 0 || len(m.List()) != 0 {
		t.Fatalf("a refused spawn wrapped=%d sessions=%d", boxes.wraps, len(m.List()))
	}
	// Asked for explicitly, the same spawn is the user's call and runs.
	s, err = m.Spawn(launcher.SpawnSpec{Agent: "clone", Runtime: config.RuntimeHost, Clone: boolPtr(false), Cwd: a})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if s.Info().Runtime != config.RuntimeHost {
		t.Fatalf("Runtime = %q, want host", s.Info().Runtime)
	}
}

// An old client sends no clone field: the profile decides, as before.
func TestSpawnCloneNilKeepsProfileBehaviour(t *testing.T) {
	t.Run("box goes to the clone", func(t *testing.T) {
		l, dir := testCloneLauncher(t, config.RuntimeBox)
		a := inPlaceDir(t, &l)
		hooks := &fakeCloneHooks{}
		m, _ := cloneManager(t, l, hooks)
		s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Runtime: config.RuntimeBox, Cwd: dir})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = m.Kill(s.ID) })
		if s.Info().CloneOf != a {
			t.Fatalf("CloneOf = %q, want %q", s.Info().CloneOf, a)
		}
	})
	t.Run("host is refused", func(t *testing.T) {
		l, _ := testCloneLauncher(t, config.RuntimeBox)
		a := inPlaceDir(t, &l)
		m, _ := cloneManager(t, l, &fakeCloneHooks{})
		s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Runtime: config.RuntimeHost, Cwd: a})
		if err == nil {
			_ = m.Kill(s.ID)
			t.Fatal("a clone profile spawned on the host tier")
		}
		if !strings.Contains(err.Error(), "is a clone-mode profile") || !errors.Is(err, box.ErrUnboxableWorkspace) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestSpawnCloneTrueWithoutSetup(t *testing.T) {
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	l.clone = nil
	hooks := &fakeCloneHooks{}
	m, boxes := cloneManager(t, l, hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Runtime: config.RuntimeBox, Clone: boolPtr(true), Cwd: dir})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("clone:true spawned a profile with no clone setup")
	}
	if !errors.Is(err, box.ErrUnboxableWorkspace) || err.Error() != `profile "clone": this profile has no clone setup` {
		t.Fatalf("err = %v", err)
	}
	if boxes.wraps != 0 || len(m.List()) != 0 {
		t.Fatalf("a refused spawn wrapped=%d sessions=%d", boxes.wraps, len(m.List()))
	}
}

// clone:true on a profile that has a setup is today's clone spawn.
func TestSpawnCloneTrueWithSetupIsACloneSpawn(t *testing.T) {
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	hooks := &fakeCloneHooks{}
	m, boxes := cloneManager(t, l, hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Clone: boolPtr(true), Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if hooks.nPrepares() != 1 || boxes.lastKey.Clone == nil {
		t.Fatalf("prepares=%d key.Clone=%+v", hooks.nPrepares(), boxes.lastKey.Clone)
	}
}

// A restart keeps the choice: the persisted CloneOf is the choice. An in-place
// session of a clone profile restarts in place (not re-resolved into a clone
// and refused for its cwd); a real clone session restarts as today.
func TestRestartInPlaceOnCloneProfileStaysInPlace(t *testing.T) {
	newMgr := func(t *testing.T) (*Manager, *fakeCloneHooks, *[]launcher.SpawnSpec, cloneLauncher, string) {
		cl, _ := testCloneLauncher(t, config.RuntimeBox)
		cl.tieredLauncher.argv = []string{"sh", "-c", "exit 0"}
		a := inPlaceDir(t, &cl)
		var mu sync.Mutex
		var specs []launcher.SpawnSpec
		m := NewManager(specRecorder{cloneGHLauncher: cloneGHLauncher{cloneLauncher: cl}, mu: &mu, specs: &specs}, nil, 1<<16, time.Minute)
		hooks := &fakeCloneHooks{}
		m.SetBoxes(&fakeBoxes{})
		m.SetClone(hooks)
		return m, hooks, &specs, cl, a
	}
	t.Run("in place", func(t *testing.T) {
		m, hooks, specs, _, a := newMgr(t)
		old := exitedSession(t, m, launcher.SpawnSpec{Agent: "clone", Runtime: config.RuntimeBox, Clone: boolPtr(false), Cwd: a})
		ns, err := m.Restart(old.ID)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = m.Kill(ns.ID) })
		got := (*specs)[len(*specs)-1]
		if got.Clone == nil || *got.Clone {
			t.Fatalf("restart spec Clone = %v, want non-nil false", got.Clone)
		}
		if ns.Info().CloneOf != "" || ns.Info().Cwd != a || hooks.nPrepares() != 0 {
			t.Fatalf("CloneOf=%q Cwd=%q prepares=%d, want an in-place restart", ns.Info().CloneOf, ns.Info().Cwd, hooks.nPrepares())
		}
	})
	t.Run("in place on the host", func(t *testing.T) {
		// The session asked for the host explicitly, so its recorded runtime is
		// host and a restart passes it back: the clone-by-omission refusal does
		// not fire.
		m, hooks, _, _, a := newMgr(t)
		old := exitedSession(t, m, launcher.SpawnSpec{Agent: "clone", Runtime: config.RuntimeHost, Clone: boolPtr(false), Cwd: a})
		ns, err := m.Restart(old.ID)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = m.Kill(ns.ID) })
		if ns.Info().Runtime != config.RuntimeHost || ns.Info().CloneOf != "" || hooks.nPrepares() != 0 {
			t.Fatalf("Runtime=%q CloneOf=%q prepares=%d, want an in-place host restart", ns.Info().Runtime, ns.Info().CloneOf, hooks.nPrepares())
		}
	})
	t.Run("clone", func(t *testing.T) {
		m, hooks, specs, cl, _ := newMgr(t)
		old := exitedSession(t, m, launcher.SpawnSpec{Agent: "clone", Runtime: config.RuntimeBox, Cwd: cl.clone.Dir})
		if old.Info().CloneOf == "" {
			t.Fatal("the original is not a clone session")
		}
		ns, err := m.Restart(old.ID)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = m.Kill(ns.ID) })
		if got := (*specs)[len(*specs)-1]; got.Clone != nil {
			t.Fatalf("restart spec Clone = %v, want nil (the profile decides, as today)", *got.Clone)
		}
		if ns.Info().CloneOf != old.Info().CloneOf || hooks.nPrepares() != 2 {
			t.Fatalf("CloneOf=%q prepares=%d, want the clone again", ns.Info().CloneOf, hooks.nPrepares())
		}
	})
}

// slowRepoHooks is a fakeCloneHooks whose ReturnForRepo ignores its budget and
// blocks until released, like a docker run that outlives its kill.
type slowRepoHooks struct {
	*fakeCloneHooks
	release chan struct{}
	called  chan struct{}
}

func (h *slowRepoHooks) ReturnForRepo(ctx context.Context, cwd string) []ReturnSummary {
	h.called <- struct{}{}
	<-h.release
	return []ReturnSummary{{Trigger: ReturnOnHost, Profile: "late"}}
}

// M1: a Host spawn waits at most the budget plus the grace for the return; past
// that it starts without a summary and the return's result is dropped.
func TestSpawnHostReturnIsAHardLimit(t *testing.T) {
	l := cloneLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeHost}}
	hooks := &slowRepoHooks{fakeCloneHooks: &fakeCloneHooks{}, release: make(chan struct{}), called: make(chan struct{}, 1)}
	defer close(hooks.release)
	m, _ := cloneManager(t, l, hooks)
	m.hostBudget, m.hostGrace = 50*time.Millisecond, 100*time.Millisecond
	start := time.Now()
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Spawn took %v with a hung return; want about budget+grace", d)
	}
	select {
	case <-hooks.called:
	default:
		t.Fatal("ReturnForRepo was never called")
	}
	if got := s.Info().HostReturn; got != nil {
		t.Fatalf("HostReturn = %+v, want nil when the return outlived its limit", got)
	}
}
