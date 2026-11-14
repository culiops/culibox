package box

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOverlaps(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"/w/a", "/w/a", true},
		{"/w/a/", "/w/a", true},
		{"/w/a/./x/..", "/w/a", true},
		{"/w", "/w/a", true},
		{"/w/a/b/c", "/w/a", true},
		{"/w/ab", "/w/a", false},
		{"/w/a", "/w/ab", false},
		{"/w/a", "/w/b", false},
		{"/", "/w/a", true},
	}
	for _, c := range cases {
		if got := Overlaps(c.a, c.b); got != c.want {
			t.Errorf("Overlaps(%q,%q)=%v", c.a, c.b, got)
		}
		if got := Overlaps(c.b, c.a); got != c.want {
			t.Errorf("Overlaps(%q,%q)=%v (not symmetric)", c.b, c.a, got)
		}
	}
}

func boundMgr(t *testing.T) (*Manager, *fakeClient) {
	m, f := newTestManagerWithFake(t)
	m.SetEgress(Egress{Network: "agentbox-egress-TEST", Verify: okVerify})
	return m, f
}

func wrapIn(m *Manager, dir, sid string, exclusive bool) (string, error) {
	k := BoxKey{Binding: "abc123abc123"}
	if exclusive {
		k.Session = sid
	}
	_, _, name, err := m.Wrap(context.Background(), dir, sid, k, []string{"sh"}, nil, nil)
	return name, err
}

// mustLastExclusive is the name of the last box the fake started with the
// exclusive label.
func (m *Manager) mustLastExclusive(t *testing.T) string {
	t.Helper()
	f := m.opts.Client.(*fakeClient)
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.runArgs) - 1; i >= 0; i-- {
		args := f.runArgs[i]
		if !containsPair(args, "--label", LabelExclusive+"=1") {
			continue
		}
		for j := 0; j+1 < len(args); j++ {
			if args[j] == "--name" {
				return args[j+1]
			}
		}
	}
	t.Fatal("no exclusive box was started")
	return ""
}

func (f *fakeClient) wasStopped(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return contains(f.stopped, name)
}

// Carry from the Task 6 review: the Session key reaches runArgs as exclusive.
func TestWrapWithSessionKeyLabelsTheBoxExclusive(t *testing.T) {
	m, f := boundMgr(t)
	name, err := wrapIn(m, t.TempDir(), "s1", true)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	args := f.runArgs[len(f.runArgs)-1]
	f.mu.Unlock()
	if !containsPair(args, "--label", LabelExclusive+"=1") || !containsPair(args, "--name", name) {
		t.Fatalf("an exclusive Wrap must label its box exclusive: %q", args)
	}
	plain, err := wrapIn(m, t.TempDir(), "s2", false)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	args = f.runArgs[len(f.runArgs)-1]
	f.mu.Unlock()
	if containsPair(args, "--label", LabelExclusive+"=1") || !containsPair(args, "--name", plain) {
		t.Fatalf("a plain Wrap must not label its box exclusive: %q", args)
	}
}

func TestExclusiveSpawnRefusedNextToALiveBox(t *testing.T) {
	m, _ := boundMgr(t)
	dir := t.TempDir()
	if _, err := wrapIn(m, dir, "plain-1", false); err != nil {
		t.Fatal(err)
	}
	_, err := wrapIn(m, filepath.Join(dir), "elev-1", true)
	if !errors.Is(err, ErrWorkspaceHeld) || !strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), "separate folder or worktree") {
		t.Fatalf("want ErrWorkspaceHeld naming the path and the remedy, got %v", err)
	}
}

func TestAnySpawnRefusedNextToAnExclusiveBox(t *testing.T) {
	m, _ := boundMgr(t)
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapIn(m, dir, "elev-1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapIn(m, sub, "plain-1", false); !errors.Is(err, ErrWorkspaceHeld) { // descendant
		t.Fatalf("descendant: %v", err)
	}
	if _, _, _, err := m.Wrap(context.Background(), dir, "u-1", BoxKey{}, []string{"sh"}, nil, nil); !errors.Is(err, ErrWorkspaceHeld) { // unbound too
		t.Fatalf("unbound: %v", err)
	}
	other := t.TempDir()
	if _, err := wrapIn(m, other, "plain-2", false); err != nil { // an unrelated folder is fine
		t.Fatalf("unrelated folder: %v", err)
	}
}

func TestSymlinkedSpellingIsTheSameWorkspace(t *testing.T) {
	m, _ := boundMgr(t)
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "ln")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapIn(m, dir, "elev-1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapIn(m, link, "plain-1", false); !errors.Is(err, ErrWorkspaceHeld) {
		t.Fatalf("a symlink to an exclusive folder must be refused: %v", err)
	}
}

func TestIdleSameInstanceBoxIsRemovedFirstBothWays(t *testing.T) {
	m, f := boundMgr(t)
	dir := t.TempDir()
	name, err := wrapIn(m, dir, "plain-1", false)
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(name) // idle now, waiting on its timer
	if _, err := wrapIn(m, dir, "elev-1", true); err != nil {
		t.Fatalf("an idle same-instance box must be removed, not block: %v", err)
	}
	if !f.wasStopped(name) {
		t.Fatalf("idle box %s not removed: %v", name, f.stopped)
	}
	m.mu.Lock()
	_, armed := m.timers[name]
	m.mu.Unlock()
	if armed {
		t.Fatal("the removed idle box kept its idle timer")
	}
	// The other direction (spec correction 6): an idle exclusive box.
	ex := m.mustLastExclusive(t) // helper: the exclusive box's name, read from f.labels
	m.mu.Lock()
	m.active[ex] = 0 // as if adopted after a restart and its session ended; timer pending
	m.mu.Unlock()
	if _, err := wrapIn(m, dir, "plain-2", false); err != nil {
		t.Fatalf("an idle exclusive box must be removed, not block: %v", err)
	}
	if !f.wasStopped(ex) {
		t.Fatalf("idle exclusive box %s not removed", ex)
	}
	m.mu.Lock()
	marked := m.exclusive[ex]
	m.mu.Unlock()
	if marked {
		t.Fatal("the removed exclusive box is still marked exclusive")
	}
}

// Spec correction 6: a removal failure refuses rather than sharing the folder.
func TestIdleBoxRemovalFailureRefuses(t *testing.T) {
	m, f := boundMgr(t)
	dir := t.TempDir()
	name, err := wrapIn(m, dir, "plain-1", false)
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(name)
	f.mu.Lock()
	f.stopFails = 1
	f.mu.Unlock()
	if _, err := wrapIn(m, dir, "elev-1", true); !errors.Is(err, ErrWorkspaceHeld) {
		t.Fatalf("a box that could not be removed must still block: %v", err)
	}
}

func TestOtherInstanceBoxesAreSeen(t *testing.T) {
	m, f := boundMgr(t)
	dir := t.TempDir()
	real, _ := filepath.EvalSymlinks(dir)
	f.others = []BoxInfo{{Name: "ab-box-prod1", Workspace: real, State: "running", Instance: "PROD", Claims: true}}
	if _, err := wrapIn(m, dir, "elev-1", true); !errors.Is(err, ErrWorkspaceHeld) {
		t.Fatalf("an exclusive spawn next to another instance's box: %v", err)
	}
	if contains(f.stopped, "ab-box-prod1") {
		t.Fatal("another instance's box must never be removed")
	}
	f.others[0].Exclusive = true
	if _, _, _, err := m.Wrap(context.Background(), dir, "u-1", BoxKey{}, []string{"sh"}, nil, nil); !errors.Is(err, ErrWorkspaceHeld) {
		t.Fatalf("a plain spawn next to another instance's exclusive box: %v", err)
	}
	f.others[0].State = "exited"
	if _, err := wrapIn(m, dir, "elev-2", true); err != nil {
		t.Fatalf("a stopped box holds nothing: %v", err)
	}
}

// Carry from the Task 6 review: rows can come from foreign containers, and a
// row the parser could not split cleanly has no trustworthy workspace, so it
// counts as a conflict for every spawn rather than being skipped.
func TestUnreadableRowRefusesEverySpawn(t *testing.T) {
	m, f := boundMgr(t)
	f.others = []BoxInfo{{Name: "ab-box-odd", State: "running", Workspace: "/elsewhere", Unreadable: true}}
	if _, _, _, err := m.Wrap(context.Background(), t.TempDir(), "u-1", BoxKey{}, []string{"sh"}, nil, nil); !errors.Is(err, ErrWorkspaceHeld) {
		t.Fatalf("a plain spawn next to an unreadable running row: %v", err)
	}
	if _, err := wrapIn(m, t.TempDir(), "elev-1", true); !errors.Is(err, ErrWorkspaceHeld) {
		t.Fatalf("an exclusive spawn next to an unreadable running row: %v", err)
	}
	if contains(f.stopped, "ab-box-odd") {
		t.Fatal("an unreadable row must never be removed")
	}
	f.others[0].State = "exited"
	if _, err := wrapIn(m, t.TempDir(), "elev-2", true); err != nil {
		t.Fatalf("a stopped unreadable row holds nothing: %v", err)
	}
}

// A running box with no workspace label could mount anything: an exclusive
// spawn treats it as overlapping. A plain spawn only cares about exclusive ones,
// and that label is readable.
func TestRunningBoxWithoutWorkspaceLabel(t *testing.T) {
	m, f := boundMgr(t)
	f.others = []BoxInfo{{Name: "ab-box-nolabel", State: "running", Instance: "PROD", Claims: true}}
	if _, err := wrapIn(m, t.TempDir(), "elev-1", true); !errors.Is(err, ErrWorkspaceHeld) {
		t.Fatalf("an exclusive spawn next to a box with no workspace label: %v", err)
	}
	if _, err := wrapIn(m, t.TempDir(), "plain-1", false); err != nil {
		t.Fatalf("a plain spawn next to a non-exclusive box: %v", err)
	}
	f.others[0].Exclusive = true
	if _, err := wrapIn(m, t.TempDir(), "plain-2", false); !errors.Is(err, ErrWorkspaceHeld) {
		t.Fatalf("a plain spawn next to an exclusive box with no workspace label: %v", err)
	}
}

// A paused or restarting box keeps its folder and resumes into it, so it holds
// the workspace like a running one; a relative workspace label on a container
// agent-box did not write is as unreadable as an empty one.
func TestPausedBoxAndRelativeLabelHoldTheWorkspace(t *testing.T) {
	m, f := boundMgr(t)
	dir := t.TempDir()
	real, _ := filepath.EvalSymlinks(dir)
	for _, state := range []string{"paused", "restarting"} {
		f.others = []BoxInfo{{Name: "ab-box-p", State: state, Workspace: real, Instance: "PROD", Claims: true}}
		if _, err := wrapIn(m, dir, "elev-"+state, true); !errors.Is(err, ErrWorkspaceHeld) {
			t.Fatalf("an exclusive spawn next to a %s box on the same folder: %v", state, err)
		}
	}
	f.others = []BoxInfo{{Name: "ab-box-rel", State: "running", Workspace: "w/proj", Instance: "PROD", Claims: true}}
	if _, err := wrapIn(m, t.TempDir(), "elev-rel", true); !errors.Is(err, ErrWorkspaceHeld) {
		t.Fatalf("an exclusive spawn next to a box with a relative workspace label: %v", err)
	}
	f.others = []BoxInfo{{Name: "ab-box-x", State: "exited", Workspace: real, Instance: "PROD", Claims: true},
		{Name: "ab-box-y", State: "created", Workspace: "rel", Instance: "PROD"}}
	if _, err := wrapIn(m, dir, "elev-ok", true); err != nil {
		t.Fatalf("exited and created boxes hold nothing: %v", err)
	}
}

// The check and the docker run sit under one opMu hold, so two exclusive
// spawns into one folder at once cannot both pass.
func TestConcurrentExclusiveSpawnsOneWins(t *testing.T) {
	m, _ := boundMgr(t)
	dir := t.TempDir()
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = wrapIn(m, dir, "elev-"+string(rune('a'+i)), true)
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrWorkspaceHeld):
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d exclusive spawns into one folder succeeded, want 1", ok)
	}
}

func TestListingFailureRefusesEveryBoxedSpawn(t *testing.T) {
	m, f := boundMgr(t)
	f.listErr = errors.New("daemon unreachable")
	if _, _, _, err := m.Wrap(context.Background(), t.TempDir(), "u-1", BoxKey{}, []string{"sh"}, nil, nil); !errors.Is(err, ErrBoxUnavailable) {
		t.Fatalf("want ErrBoxUnavailable, got %v", err)
	}
	if f.runs() != 0 {
		t.Fatal("docker run happened after a failed workspace check")
	}
}

func TestExclusiveBoxIsRemovedWhenItsSessionEnds(t *testing.T) {
	m, f := boundMgr(t) // IdleTTL is an hour in this manager
	name, err := wrapIn(m, t.TempDir(), "elev-1", true)
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(name)
	waitFor(t, 2*time.Second, func() bool { return f.wasStopped(name) })
	waitFor(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return !m.exclusive[name]
	})
}

func TestPlainBoxStillWaitsForItsIdleTTL(t *testing.T) {
	m, f := boundMgr(t)
	name, err := wrapIn(m, t.TempDir(), "plain-1", false)
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(name)
	time.Sleep(50 * time.Millisecond)
	if f.wasStopped(name) {
		t.Fatal("a plain box was removed at release instead of after its idle TTL")
	}
}

func TestMarkExclusiveAppliesToAnAdoptedBox(t *testing.T) {
	m, f := boundMgr(t)
	m.AdoptBox("ab-box-adopted")
	m.MarkExclusive("ab-box-adopted")
	m.ReleaseBox("ab-box-adopted")
	waitFor(t, 2*time.Second, func() bool { return f.wasStopped("ab-box-adopted") })
}

func TestGrantBlocker(t *testing.T) {
	m, f := boundMgr(t)
	dir := t.TempDir()
	real, _ := filepath.EvalSymlinks(dir)
	self, err := wrapIn(m, dir, "elev-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if why, err := m.GrantBlocker(context.Background(), dir, self); err != nil || why != "" {
		t.Fatalf("own box only: %q %v", why, err)
	}
	f.others = []BoxInfo{{Name: "ab-box-old", Workspace: real + "/sub", State: "running", Instance: "PROD"}}
	why, _ := m.GrantBlocker(context.Background(), dir, self)
	if !strings.Contains(why, "older CuliBox") || !strings.Contains(why, "PROD") {
		t.Fatalf("a claims-less box: %q", why)
	}
	f.others[0].Claims = true
	why, _ = m.GrantBlocker(context.Background(), dir, self)
	if why == "" || strings.Contains(why, "older CuliBox") {
		t.Fatalf("any other overlapping box blocks a grant: %q", why)
	}
	f.others[0].Workspace = "/somewhere/else"
	if why, err := m.GrantBlocker(context.Background(), dir, self); err != nil || why != "" {
		t.Fatalf("a box over an unrelated folder: %q %v", why, err)
	}
	f.others[0] = BoxInfo{Name: "ab-box-odd", State: "running", Workspace: "/somewhere/else", Unreadable: true}
	if why, _ := m.GrantBlocker(context.Background(), dir, self); why == "" {
		t.Fatal("an unreadable running row must block a grant")
	}
	f.others[0] = BoxInfo{Name: "ab-box-nolabel", State: "running", Instance: "PROD", Claims: true}
	if why, _ := m.GrantBlocker(context.Background(), dir, self); why == "" {
		t.Fatal("a running box with no workspace label must block a grant")
	}
	f.others[0] = BoxInfo{Name: "ab-box-rel", State: "running", Workspace: "w/proj", Instance: "PROD", Claims: true}
	if why, _ := m.GrantBlocker(context.Background(), dir, self); why == "" {
		t.Fatal("a box with a relative workspace label must block a grant")
	}
	f.others[0] = BoxInfo{Name: "ab-box-paused", State: "paused", Workspace: real, Instance: "PROD", Claims: true}
	if why, _ := m.GrantBlocker(context.Background(), dir, self); why == "" {
		t.Fatal("a paused box over the folder must block a grant")
	}
	f.others[0].State = "exited"
	if why, _ := m.GrantBlocker(context.Background(), dir, self); why != "" {
		t.Fatalf("an exited box holds nothing: %q", why)
	}
	f.listErr = errors.New("down")
	if _, err := m.GrantBlocker(context.Background(), dir, self); err == nil {
		t.Fatal("a listing error must be an error (the caller refuses)")
	}
}
