package session

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

// swapLauncher is a cloneLauncher whose profile can change after spawn.
type swapLauncher struct {
	cloneLauncher
	mu  sync.Mutex
	cur *config.Clone
}

func (l *swapLauncher) CloneFor(agent string) (*config.Clone, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if agent != "clone" || l.cur == nil {
		return nil, nil
	}
	c := *l.cur
	return &c, nil
}

func (l *swapLauncher) set(c *config.Clone) {
	l.mu.Lock()
	l.cur = c
	l.mu.Unlock()
}

func newSwapLauncher(t *testing.T, argv []string) (*swapLauncher, string) {
	t.Helper()
	cl, dir := testCloneLauncher(t, config.RuntimeBox)
	if argv != nil {
		cl.argv = argv
	}
	return &swapLauncher{cloneLauncher: cl, cur: cl.clone}, dir
}

func swapManager(t *testing.T, l *swapLauncher, hooks CloneHooks) (*Manager, *fakeBoxes) {
	t.Helper()
	m := NewManager(l, nil, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	m.SetClone(hooks)
	return m, boxes
}

func waitReturns(t *testing.T, h *fakeCloneHooks, want int) []returnCall {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := h.returnCalls()
		if len(got) >= want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("ReturnWork called %d times, want %d", len(got), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitInfoReturn(t *testing.T, s *Session) *ReturnSummary {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if r := s.Info().Return; r != nil {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatal("Info().Return never set")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCloneSpawnPassesReturnBudget(t *testing.T) {
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	hooks := &fakeCloneHooks{}
	m, _ := cloneManager(t, l, hooks)
	start := time.Now()
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if got := hooks.refreshes[0].ReturnBudget; got != cloneSpawnReturnBudget {
		t.Fatalf("ReturnBudget = %v, want %v", got, cloneSpawnReturnBudget)
	}
	if need := start.Add(cloneRefreshTimeout + cloneSpawnReturnBudget); hooks.deadlines[0].Before(need) {
		t.Fatalf("Refresh deadline %v is before refresh timeout + return budget (%v)", hooks.deadlines[0], need)
	}
}

func TestExitReturnRunsOnAgentExit(t *testing.T) {
	l, dir := newSwapLauncher(t, []string{"sh", "-c", "exit 0"})
	hooks := &fakeCloneHooks{retSum: ReturnSummary{Home: []string{"a"}}}
	m, _ := swapManager(t, l, hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	calls := waitReturns(t, hooks, 1)
	if len(calls) != 1 || calls[0].trigger != ReturnOnExit {
		t.Fatalf("calls = %+v, want one exit call", calls)
	}
	if calls[0].clone.Dir != dir || calls[0].clone.Of != "/home/u/repo" {
		t.Fatalf("clone passed = %+v, want the profile's current clone", calls[0].clone)
	}
	r := waitInfoReturn(t, s)
	if r.Trigger != ReturnOnExit || len(r.Home) != 1 || r.Home[0] != "a" {
		t.Fatalf("Info().Return = %+v", r)
	}
}

func TestKillBringsWorkHome(t *testing.T) {
	l, dir := newSwapLauncher(t, nil)
	hooks := &fakeCloneHooks{}
	m, _ := swapManager(t, l, hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Kill(s.ID); err != nil {
		t.Fatal(err)
	}
	calls := waitReturns(t, hooks, 1)
	if len(calls) != 1 || calls[0].trigger != ReturnOnExit {
		t.Fatalf("calls = %+v, want one exit call", calls)
	}
	if calls[0].live {
		t.Fatal("the killed session counted itself as live, so a kill would never bring work home")
	}
}

// The exiting session is excluded by identity, not by status: Kill finalizes
// before the status turns exited, so a scan by status alone sees it live.
func TestCloneLiveExcludesTheExitingSession(t *testing.T) {
	l, dir := newSwapLauncher(t, nil)
	m, _ := swapManager(t, l, &fakeCloneHooks{})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if !m.cloneLive(dir, nil)() {
		t.Fatal("a live boxed session on the clone must count")
	}
	if m.cloneLive(dir, s)() {
		t.Fatal("the excepted session must not count itself")
	}
	if m.cloneLive(t.TempDir(), nil)() {
		t.Fatal("a session elsewhere must not count")
	}
}

func TestExitReturnSkipsWhileAnotherSessionLives(t *testing.T) {
	l, dir := newSwapLauncher(t, nil)
	hooks := &fakeCloneHooks{}
	m, _ := swapManager(t, l, hooks)
	s1, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s2.ID) })
	if err := m.Kill(s1.ID); err != nil {
		t.Fatal(err)
	}
	calls := waitReturns(t, hooks, 1)
	if len(calls) != 1 || !calls[0].live {
		t.Fatalf("calls = %+v, want one call that saw the other session live", calls)
	}
}

func TestExitReturnIgnoresAProfileChangedSinceSpawn(t *testing.T) {
	l, dir := newSwapLauncher(t, nil)
	hooks := &fakeCloneHooks{}
	m, _ := swapManager(t, l, hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	for name, cur := range map[string]*config.Clone{
		"another dir": {Of: "/home/u/repo", Dir: t.TempDir()},
		"another of":  {Of: "/home/u/other", Dir: dir},
		"no clone":    nil,
	} {
		l.set(cur)
		m.returnWork(s, ReturnOnExit) // synchronous: a miss would be a call by now
		if n := len(hooks.returnCalls()); n != 0 {
			t.Fatalf("%s: ReturnWork called %d times, want 0", name, n)
		}
	}
	if s.Info().Return != nil {
		t.Fatal("a skipped exit return must not set Info().Return")
	}
	l.set(&config.Clone{Of: "/home/u/repo", Dir: dir + "/"}) // same dir, another spelling
	m.returnWork(s, ReturnOnExit)
	if n := len(hooks.returnCalls()); n != 1 {
		t.Fatalf("unchanged profile (unclean spelling): %d calls, want 1", n)
	}
}

func TestDetachDoesNotReturn(t *testing.T) {
	be := newFakeBackend()
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	hooks := &fakeCloneHooks{}
	m := NewManager(l, be, 1<<16, time.Hour)
	m.SetBoxes(&fakeBoxes{})
	m.SetClone(hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	be.crashAttach(s.ID) // attach dies, agent stays alive: detached
	waitStatus(t, s, statusDetached, 2*time.Second)
	time.Sleep(200 * time.Millisecond)
	if n := len(hooks.returnCalls()); n != 0 {
		t.Fatalf("detached session returned %d times; its agent is still alive", n)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
}

func TestNonCloneSessionNeverReturns(t *testing.T) {
	l, _ := testCloneLauncher(t, config.RuntimeBox)
	hooks := &fakeCloneHooks{}
	m, _ := cloneManager(t, l, hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Kill(s.ID); err != nil {
		t.Fatal(err)
	}
	m.returnWork(s, ReturnOnExit) // synchronous
	time.Sleep(100 * time.Millisecond)
	if n := len(hooks.returnCalls()); n != 0 {
		t.Fatalf("non-clone session returned %d times", n)
	}
}

func TestBringHome(t *testing.T) {
	l, dir := newSwapLauncher(t, nil)
	hooks := &fakeCloneHooks{
		retEnter: make(chan struct{}, 4),
		retHold:  make(chan struct{}),
		retSum:   ReturnSummary{Home: []string{"feat"}},
	}
	m, _ := swapManager(t, l, hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })

	if err := m.BringHome("nope"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("unknown id: err = %v, want ErrNoSession", err)
	}
	if err := m.BringHome(s.ID); err != nil {
		t.Fatalf("BringHome = %v, want nil at once", err)
	}
	<-hooks.retEnter
	if err := m.BringHome(s.ID); !errors.Is(err, ErrReturning) {
		t.Fatalf("second call while running: err = %v, want ErrReturning", err)
	}
	close(hooks.retHold)
	r := waitInfoReturn(t, s)
	if r.Trigger != ReturnNow || len(r.Home) != 1 {
		t.Fatalf("Info().Return = %+v", r)
	}
	calls := hooks.returnCalls()
	if len(calls) != 1 || calls[0].trigger != ReturnNow {
		t.Fatalf("calls = %+v, want one now call", calls)
	}
	// The flag clears once the return is done.
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := m.BringHome(s.ID)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrReturning) || time.Now().After(deadline) {
			t.Fatalf("after the return finished: err = %v, want nil", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBringHomeNonCloneSession(t *testing.T) {
	l, _ := testCloneLauncher(t, config.RuntimeBox)
	hooks := &fakeCloneHooks{}
	m, _ := cloneManager(t, l, hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if err := m.BringHome(s.ID); !errors.Is(err, ErrNotClone) {
		t.Fatalf("err = %v, want ErrNotClone", err)
	}
}

func TestBringHomeAfterProfileChangeSaysUnavailable(t *testing.T) {
	l, dir := newSwapLauncher(t, nil)
	hooks := &fakeCloneHooks{}
	m, _ := swapManager(t, l, hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	l.set(nil)
	if err := m.BringHome(s.ID); err != nil {
		t.Fatal(err)
	}
	r := waitInfoReturn(t, s)
	if r.Trigger != ReturnNow || r.Skipped != "unavailable" {
		t.Fatalf("Info().Return = %+v, want a now/unavailable summary", r)
	}
	if n := len(hooks.returnCalls()); n != 0 {
		t.Fatalf("ReturnWork called %d times for a profile that no longer names the clone", n)
	}
}

func TestCloneInfoCopyCopiesReturn(t *testing.T) {
	orig := &CloneInfo{Return: &ReturnSummary{
		Home:     []string{"a"},
		NotHome:  []NotHomeBranch{{Branch: "b", Reason: "x"}},
		Warnings: []string{"w"},
	}}
	cp := orig.copy()
	cp.Return.Home[0] = "changed"
	cp.Return.NotHome[0].Branch = "changed"
	cp.Return.Warnings[0] = "changed"
	if orig.Return.Home[0] != "a" || orig.Return.NotHome[0].Branch != "b" || orig.Return.Warnings[0] != "w" {
		t.Fatalf("mutating the copy changed the original: %+v", orig.Return)
	}
	if (&CloneInfo{}).copy().Return != nil {
		t.Fatal("a CloneInfo without Return must copy to one without")
	}
}
