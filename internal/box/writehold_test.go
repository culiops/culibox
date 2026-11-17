package box

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// holdSet is a write-hold hook over a set of held session ids: what the broker
// answers for a session with a write window. It also records what it was asked.
type holdSet struct {
	mu      sync.Mutex
	held    map[string]string // session id -> why
	adopted map[string]time.Time
}

func newHoldSet(m *Manager) *holdSet {
	h := &holdSet{held: map[string]string{}, adopted: map[string]time.Time{}}
	m.SetWriteHold(h.hold)
	return h
}

func (h *holdSet) hold(sid string, adoptedAt time.Time) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.adopted[sid] = adoptedAt
	return h.held[sid]
}

func (h *holdSet) set(sid, why string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if why == "" {
		delete(h.held, sid)
		return
	}
	h.held[sid] = why
}

// #325: an elevatable session keeps its own box, but until it holds a write
// window, other spawns may use its folder ("+ Shell" first of all).
func TestSpawnBesideAnExclusiveBoxWithoutAWindow(t *testing.T) {
	m, _ := boundMgr(t)
	newHoldSet(m)
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapIn(m, dir, "elev-1", true); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.Wrap(context.Background(), dir, "sh-1", BoxKey{}, []string{"sh"}, nil, nil); err != nil {
		t.Fatalf("a quick shell beside an elevatable session with no window: %v", err)
	}
	if _, err := wrapIn(m, sub, "plain-1", false); err != nil {
		t.Fatalf("a bound spawn in a subfolder: %v", err)
	}
	if _, err := wrapIn(m, dir, "elev-2", true); err != nil {
		t.Fatalf("a second elevatable session in the folder: %v", err)
	}
}

// The other order: an elevatable session may start (or resume) in a folder a
// shell already uses. The grant is what checks the folder.
func TestExclusiveSpawnBesideALivePlainBox(t *testing.T) {
	m, _ := boundMgr(t)
	newHoldSet(m)
	dir := t.TempDir()
	plain, err := wrapIn(m, dir, "plain-1", false)
	if err != nil {
		t.Fatal(err)
	}
	self, err := wrapIn(m, dir, "elev-1", true)
	if err != nil {
		t.Fatalf("an elevatable spawn beside a live shell: %v", err)
	}
	gb, err := m.ClaimForGrant(context.Background(), dir, self)
	if err != nil || gb.Box != plain || gb.Why == "" {
		t.Fatalf("the grant must name the shell's box %s: %+v %v", plain, gb, err)
	}
}

// A spawn into the folder while a window is open (or a grant is being made, or
// write credentials from a dropped window are still valid) is refused, with the
// reason the hook gives; once the hook stops holding, spawns work again.
func TestSpawnRefusedWhileAWriteWindowIsOpen(t *testing.T) {
	m, f := boundMgr(t)
	h := newHoldSet(m)
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "ln")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapIn(m, dir, "elev-1", true); err != nil {
		t.Fatal(err)
	}
	why := `session "deploy" (elev-1) has a write window open until 15:04`
	h.set("elev-1", why)
	runs := f.runs()
	for _, spawn := range []struct {
		name string
		do   func() error
	}{
		{"quick shell", func() error {
			_, _, _, err := m.Wrap(context.Background(), dir, "sh-1", BoxKey{}, []string{"sh"}, nil, nil)
			return err
		}},
		{"subfolder", func() error { _, err := wrapIn(m, sub, "plain-1", false); return err }},
		{"symlinked spelling", func() error { _, err := wrapIn(m, link, "plain-2", false); return err }},
		{"another elevatable", func() error { _, err := wrapIn(m, dir, "elev-2", true); return err }},
	} {
		err := spawn.do()
		if !errors.Is(err, ErrWorkspaceHeld) || !strings.Contains(err.Error(), why) ||
			!strings.Contains(err.Error(), "write window") || strings.Contains(err.Error(), "AWS-write-capable") {
			t.Fatalf("%s: want a refusal naming the open window, got %v", spawn.name, err)
		}
	}
	if f.runs() != runs {
		t.Fatal("a box was started while the window was open")
	}
	if _, err := wrapIn(m, t.TempDir(), "plain-3", false); err != nil {
		t.Fatalf("an unrelated folder is fine: %v", err)
	}
	h.set("elev-1", "") // the window ended
	if _, err := wrapIn(m, sub, "plain-4", false); err != nil {
		t.Fatalf("spawns work again once the window ends: %v", err)
	}
}

// Without the hook (no broker) this process cannot tell whether a window is
// open, and an exclusive box it does not know the owner of is the same: both
// fail closed, as before #325.
func TestUnknownWriteStateFailsClosed(t *testing.T) {
	m, _ := boundMgr(t)
	dir := t.TempDir()
	if _, err := wrapIn(m, dir, "elev-1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapIn(m, dir, "plain-1", false); !errors.Is(err, ErrWorkspaceHeld) {
		t.Fatalf("no hook: %v", err)
	}
	newHoldSet(m)
	ex := m.mustLastExclusive(t)
	m.mu.Lock()
	delete(m.owner, ex)
	m.mu.Unlock()
	if _, err := wrapIn(m, dir, "plain-2", false); !errors.Is(err, ErrWorkspaceHeld) {
		t.Fatalf("an exclusive box with no known session: %v", err)
	}
}

// Another instance's exclusive box keeps refusing every spawn: this process
// cannot see that instance's windows.
func TestOtherInstanceExclusiveBoxStillRefuses(t *testing.T) {
	m, f := boundMgr(t)
	newHoldSet(m)
	dir := t.TempDir()
	real, _ := filepath.EvalSymlinks(dir)
	f.others = []BoxInfo{{Name: "ab-box-prod1", Workspace: real, State: "running", Instance: "PROD", Claims: true, Exclusive: true}}
	_, _, _, err := m.Wrap(context.Background(), dir, "u-1", BoxKey{}, []string{"sh"}, nil, nil)
	if !errors.Is(err, ErrWorkspaceHeld) || !strings.Contains(err.Error(), "PROD") {
		t.Fatalf("want a refusal naming the other instance, got %v", err)
	}
	f.others[0].Exclusive = false
	if _, err := wrapIn(m, dir, "elev-1", true); !errors.Is(err, ErrWorkspaceHeld) {
		t.Fatalf("an elevatable spawn beside another instance's box: %v", err)
	}
}

// A box adopted after a restart may hold write credentials the broker no longer
// knows about: the hook is told when it was adopted, and a fresh box is not.
func TestWriteHoldIsToldAboutAdoption(t *testing.T) {
	m, f := boundMgr(t)
	h := newHoldSet(m)
	dir := t.TempDir()
	real, _ := filepath.EvalSymlinks(dir)
	if _, err := wrapIn(m, dir, "elev-1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapIn(m, dir, "plain-1", false); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	fresh, asked := h.adopted["elev-1"]
	h.mu.Unlock()
	if !asked || !fresh.IsZero() {
		t.Fatalf("a box this process started is not adopted: %v %v", fresh, asked)
	}
	f.mu.Lock()
	f.running["ab-box-adopted"] = true
	f.labels["ab-box-adopted"] = map[string]string{LabelWorkspace: real, LabelInstance: "TEST", LabelExclusive: "1", LabelClaims: "1"}
	f.mu.Unlock()
	m.AdoptBox("ab-box-adopted")
	m.AdoptExclusive("ab-box-adopted", "old-1")
	h.set("old-1", `session "old" (old-1) was running before CuliBox restarted`)
	if _, err := wrapIn(m, dir, "plain-2", false); !errors.Is(err, ErrWorkspaceHeld) || !strings.Contains(err.Error(), "before CuliBox restarted") {
		t.Fatalf("an adopted box the hook holds: %v", err)
	}
	h.mu.Lock()
	at := h.adopted["old-1"]
	h.mu.Unlock()
	if at.IsZero() {
		t.Fatal("the hook was not told the box was adopted")
	}
	// Removed with its session like any exclusive box, and forgotten.
	m.ReleaseBox("ab-box-adopted")
	waitFor(t, 2*time.Second, func() bool { return f.wasStopped("ab-box-adopted") })
	waitFor(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, o := m.owner["ab-box-adopted"]
		_, a := m.adoptedAt["ab-box-adopted"]
		return !o && !a
	})
}

// The grant panel reads without side effects: an idle box of this instance is
// not shown as blocking (the grant removes it), and nothing is removed.
// ClaimForGrant removes it.
func TestIdleBoxIsRemovedByTheGrantNotThePanel(t *testing.T) {
	m, f := boundMgr(t)
	newHoldSet(m)
	dir := t.TempDir()
	self, err := wrapIn(m, dir, "elev-1", true)
	if err != nil {
		t.Fatal(err)
	}
	sh, _, err := wrapPlain(m, dir, "sh-1")
	if err != nil {
		t.Fatal(err)
	}
	if gb, err := m.GrantBlocker(context.Background(), dir, self); err != nil || gb.Box != sh {
		t.Fatalf("a live shell blocks the panel: %+v %v", gb, err)
	}
	m.ReleaseBox(sh) // the shell ended: its box idles for the TTL
	if gb, err := m.GrantBlocker(context.Background(), dir, self); err != nil || gb.Why != "" {
		t.Fatalf("an idle box must not block the panel: %+v %v", gb, err)
	}
	if f.wasStopped(sh) {
		t.Fatal("the panel removed a box")
	}
	if gb, err := m.ClaimForGrant(context.Background(), dir, self); err != nil || gb.Why != "" {
		t.Fatalf("the grant removes an idle box: %+v %v", gb, err)
	}
	if !f.wasStopped(sh) {
		t.Fatal("the idle box was not removed")
	}
	// One that cannot be removed blocks.
	sh2, _, err := wrapPlain(m, dir, "sh-2")
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(sh2)
	f.mu.Lock()
	f.stopFails = 1
	f.mu.Unlock()
	if gb, err := m.ClaimForGrant(context.Background(), dir, self); err != nil || gb.Why == "" {
		t.Fatalf("an idle box that could not be removed must block: %+v %v", gb, err)
	}
}

func wrapPlain(m *Manager, dir, sid string) (string, []string, error) {
	argv, _, name, err := m.Wrap(context.Background(), dir, sid, BoxKey{}, []string{"sh"}, nil, nil)
	return name, argv, err
}

// The race (#325): a spawn and a grant into one folder at once must not both
// succeed. A spawn that passed its check before the grant's hold was visible
// is still starting its box under opMu; ClaimForGrant waits for it and then
// sees the box.
func TestClaimForGrantWaitsForASpawnInFlight(t *testing.T) {
	m, f := boundMgr(t)
	h := newHoldSet(m)
	dir := t.TempDir()
	self, err := wrapIn(m, dir, "elev-1", true)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.runHook = func(args []string) (string, bool, error) {
		if args[0] == "run" {
			once.Do(func() { close(entered); <-release })
		}
		return "", false, nil
	}
	spawned := make(chan error, 1)
	var shell string
	go func() {
		var err error
		shell, _, err = wrapPlain(m, dir, "sh-1")
		spawned <- err
	}()
	<-entered // the spawn passed its check and is in `docker run`
	h.set("elev-1", `session "deploy" (elev-1) is opening a write window`)
	claimed := make(chan GrantBlock, 1)
	go func() {
		gb, err := m.ClaimForGrant(context.Background(), dir, self)
		if err != nil {
			t.Error(err)
		}
		claimed <- gb
	}()
	select {
	case gb := <-claimed:
		t.Fatalf("the grant's check ran while a spawn was starting its box: %+v", gb)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-spawned; err != nil {
		t.Fatal(err)
	}
	if gb := <-claimed; gb.Box != shell {
		t.Fatalf("the grant must see the shell's box %s: %+v", shell, gb)
	}
}

// The same race, run many times both ways: whatever the interleaving, a grant
// that passed (hold set first, ClaimForGrant clear) never has a spawn that
// succeeded next to it.
func TestSpawnAndGrantNeverBothSucceed(t *testing.T) {
	for i := 0; i < 50; i++ {
		m, f := boundMgr(t)
		h := newHoldSet(m)
		dir := t.TempDir()
		self, err := wrapIn(m, dir, "elev-1", true)
		if err != nil {
			t.Fatal(err)
		}
		f.runHook = func([]string) (string, bool, error) { time.Sleep(time.Millisecond); return "", false, nil }
		var spawnedOK atomic.Int32
		var granted atomic.Bool
		var wg sync.WaitGroup
		for j := 0; j < 4; j++ {
			wg.Add(1)
			go func(j int) {
				defer wg.Done()
				if _, _, err := wrapPlain(m, dir, "sh-"+string(rune('a'+j))); err == nil {
					spawnedOK.Add(1)
				} else if !errors.Is(err, ErrWorkspaceHeld) {
					t.Error(err)
				}
			}(j)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.set("elev-1", "opening a write window") // the broker's hold, before its check
			gb, err := m.ClaimForGrant(context.Background(), dir, self)
			if err != nil {
				t.Error(err)
				return
			}
			if gb.Why == "" {
				granted.Store(true)
				return
			}
			h.set("elev-1", "") // refused: the hold goes
		}()
		wg.Wait()
		if granted.Load() && spawnedOK.Load() > 0 {
			t.Fatalf("round %d: a grant passed and %d spawn(s) succeeded into the folder", i, spawnedOK.Load())
		}
	}
}

// #333: an exclusive box is removed as soon as its session ends (exit, Kill,
// Restart), but write credentials it was given may stay valid: the folder stays
// closed to other boxes while the hook still holds that session, and opens once
// it does not. Restart's replacement is a new elevatable session in the folder.
func TestEndedSessionKeepsItsFolderHeld(t *testing.T) {
	for _, next := range []struct {
		name string
		do   func(m *Manager, dir string) error
	}{
		{"kill then a shell", func(m *Manager, dir string) error { _, _, err := wrapPlain(m, dir, "sh-1"); return err }},
		{"restart's replacement", func(m *Manager, dir string) error { _, err := wrapIn(m, dir, "elev-2", true); return err }},
	} {
		t.Run(next.name, func(t *testing.T) {
			m, f := boundMgr(t)
			h := newHoldSet(m)
			dir := t.TempDir()
			sub := filepath.Join(dir, "sub")
			if err := os.Mkdir(sub, 0o700); err != nil {
				t.Fatal(err)
			}
			name, err := wrapIn(m, dir, "elev-1", true)
			if err != nil {
				t.Fatal(err)
			}
			why := `session elev-1 dropped its write window, but the write credentials it was given stay valid until 15:04`
			h.set("elev-1", why)
			m.ReleaseBox(name) // the session ended: its box goes at once
			waitFor(t, 2*time.Second, func() bool { return f.wasStopped(name) })
			waitFor(t, 2*time.Second, func() bool {
				m.mu.Lock()
				defer m.mu.Unlock()
				return !m.exclusive[name]
			})
			runs := f.runs()
			err = next.do(m, dir)
			if !errors.Is(err, ErrWorkspaceHeld) || !strings.Contains(err.Error(), why) {
				t.Fatalf("want a refusal naming the ended session's credentials, got %v", err)
			}
			if _, err := wrapIn(m, sub, "plain-1", false); !errors.Is(err, ErrWorkspaceHeld) {
				t.Fatalf("a subfolder: %v", err)
			}
			if f.runs() != runs {
				t.Fatal("a box was started in the held folder")
			}
			if _, err := wrapIn(m, t.TempDir(), "plain-2", false); err != nil {
				t.Fatalf("an unrelated folder is fine: %v", err)
			}
			h.set("elev-1", "") // the credentials expired
			if err := next.do(m, dir); err != nil {
				t.Fatalf("the folder opens once the hold ends: %v", err)
			}
			m.mu.Lock()
			n := len(m.ended)
			m.mu.Unlock()
			if n != 0 {
				t.Fatalf("an ended hold was kept: %d", n)
			}
		})
	}
}

// A session that holds nothing when its box goes leaves nothing behind.
func TestEndedSessionWithoutAHoldLeavesNothing(t *testing.T) {
	m, f := boundMgr(t)
	newHoldSet(m)
	dir := t.TempDir()
	name, err := wrapIn(m, dir, "elev-1", true)
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(name)
	waitFor(t, 2*time.Second, func() bool { return f.wasStopped(name) })
	waitFor(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return !m.exclusive[name] && len(m.ended) == 0
	})
	if _, _, err := wrapPlain(m, dir, "sh-1"); err != nil {
		t.Fatal(err)
	}
}

// An ended hold is pruned when the next box goes, not only by a spawn into its
// folder: it cannot pile up.
func TestEndedHoldsArePrunedOnTheNextRemoval(t *testing.T) {
	m, f := boundMgr(t)
	h := newHoldSet(m)
	a, err := wrapIn(m, t.TempDir(), "elev-1", true)
	if err != nil {
		t.Fatal(err)
	}
	h.set("elev-1", "held")
	m.ReleaseBox(a)
	waitFor(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.ended) == 1
	})
	h.set("elev-1", "")
	b, err := wrapIn(m, t.TempDir(), "elev-2", true)
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(b)
	waitFor(t, 2*time.Second, func() bool { return f.wasStopped(b) })
	waitFor(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.ended) == 0
	})
}

// A box adopted after a restart had no workspace recorded by this process: the
// hold its session leaves when the box goes covers the folder its label names.
func TestAdoptedBoxLeavesItsFolderHeld(t *testing.T) {
	m, f := boundMgr(t)
	h := newHoldSet(m)
	dir := t.TempDir()
	real, _ := filepath.EvalSymlinks(dir)
	f.mu.Lock()
	f.running["ab-box-adopted"] = true
	f.labels["ab-box-adopted"] = map[string]string{LabelWorkspace: real, LabelInstance: "TEST", LabelExclusive: "1", LabelClaims: "1"}
	f.mu.Unlock()
	m.AdoptBox("ab-box-adopted")
	m.AdoptExclusive("ab-box-adopted", "old-1")
	h.set("old-1", `session old-1 was running when CuliBox restarted`)
	m.ReleaseBox("ab-box-adopted")
	waitFor(t, 2*time.Second, func() bool { return f.wasStopped("ab-box-adopted") })
	waitFor(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return !m.exclusive["ab-box-adopted"]
	})
	if _, _, err := wrapPlain(m, dir, "sh-1"); !errors.Is(err, ErrWorkspaceHeld) || !strings.Contains(err.Error(), "restarted") {
		t.Fatalf("the adopted box's folder: %v", err)
	}
	h.mu.Lock()
	at := h.adopted["old-1"]
	h.mu.Unlock()
	if at.IsZero() {
		t.Fatal("the hook was not told the ended box had been adopted")
	}
	if _, _, err := wrapPlain(m, t.TempDir(), "sh-2"); err != nil {
		t.Fatalf("another folder: %v", err)
	}
}

// A grant waiting on a slow box operation gives up with its context.
func TestClaimForGrantHonoursItsContext(t *testing.T) {
	m, _ := boundMgr(t)
	dir := t.TempDir()
	m.opMu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := m.ClaimForGrant(ctx, dir, "ab-box-self")
	m.opMu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the context's error, got %v", err)
	}
	// The abandoned wait gives the lock back: a later spawn is not stuck.
	done := make(chan error, 1)
	go func() { _, err := wrapIn(m, dir, "plain-1", false); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("opMu was not given back after an abandoned grant check")
	}
}
