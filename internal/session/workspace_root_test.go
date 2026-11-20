package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/gitrepo"
	"github.com/culiops/culibox/internal/launcher"
)

// wsRepo makes <temp>/<name>/.git and returns the repo dir with a subdir in it.
func wsRepo(t *testing.T, name string) (repo, sub string) {
	t.Helper()
	repo = filepath.Join(t.TempDir(), name)
	sub = filepath.Join(repo, "sub")
	for _, d := range []string{filepath.Join(repo, ".git"), sub} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return repo, sub
}

func TestSpawnSetsWorkspaceRoot(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sleep", "30"}, bin: "/usr/bin/bash"}
	m, path := newCtlManager(t, l)
	repo, sub := wsRepo(t, "repo")
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Cwd: sub})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	want := gitrepo.Normalize(repo)
	if got := s.Info().WorkspaceRoot; got != want {
		t.Fatalf("Info().WorkspaceRoot = %q, want %q", got, want)
	}
	var listed *Info
	for _, in := range m.List() {
		if in.ID == s.ID {
			in := in
			listed = &in
		}
	}
	if listed == nil || listed.WorkspaceRoot != want {
		t.Fatalf("List() entry = %+v, want WorkspaceRoot %q", listed, want)
	}
	if md := sidecarEntry(t, path, s.ID); md.WorkspaceRoot != want {
		t.Fatalf("sidecar WorkspaceRoot = %q, want %q", md.WorkspaceRoot, want)
	}
}

func TestSpawnOutsideARepoHasNoWorkspaceRoot(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sleep", "30"}, bin: "/usr/bin/bash"}
	m, _ := newCtlManager(t, l)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if got := s.Info().WorkspaceRoot; got != "" {
		t.Fatalf("WorkspaceRoot = %q, want empty", got)
	}
}

// A clone session belongs to the repo it was cloned from, not to its clone dir.
func TestSpawnCloneWorkspaceRootIsCloneOf(t *testing.T) {
	l, dir := testCloneLauncher(t, config.RuntimeBox)
	// The clone dir is a repo of its own; the group is still the origin.
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	m, _ := cloneManager(t, l, &fakeCloneHooks{})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Runtime: config.RuntimeBox, Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	want := gitrepo.Normalize(l.clone.Of)
	if info := s.Info(); info.CloneOf == "" || info.WorkspaceRoot != want {
		t.Fatalf("CloneOf = %q WorkspaceRoot = %q, want %q", info.CloneOf, info.WorkspaceRoot, want)
	}
}

// clone:false on a clone profile makes an in-place session: it has no CloneOf,
// so its workspace comes from its cwd.
func TestSpawnCloneFalseWorkspaceRootIsTheCwdRepo(t *testing.T) {
	l, _ := testCloneLauncher(t, config.RuntimeBox)
	repo, sub := wsRepo(t, "inplace")
	m, _ := cloneManager(t, l, &fakeCloneHooks{})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Runtime: config.RuntimeBox, Clone: boolPtr(false), Cwd: sub})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if info := s.Info(); info.CloneOf != "" || info.WorkspaceRoot != gitrepo.Normalize(repo) {
		t.Fatalf("CloneOf = %q WorkspaceRoot = %q, want the cwd's repo %q", info.CloneOf, info.WorkspaceRoot, gitrepo.Normalize(repo))
	}
}

// A companion shell groups with its agent, even when its own cwd is elsewhere
// (the agent's clone dir).
func TestSpawnShellInheritsParentWorkspaceRoot(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sleep", "30"}, bin: "/usr/bin/bash"}
	m, _ := newCtlManager(t, l)
	repoA, subA := wsRepo(t, "a")
	_, subB := wsRepo(t, "b")
	parent, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Cwd: subA})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(parent.ID) })
	shell, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Cwd: subB, ParentID: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(shell.ID) })
	if got, want := shell.Info().WorkspaceRoot, gitrepo.Normalize(repoA); got != want {
		t.Fatalf("shell WorkspaceRoot = %q, want its parent's %q", got, want)
	}
}

// A parent that is not live (never existed, or ended) leaves the shell to its
// own cwd.
func TestSpawnShellOfUnknownParentUsesItsCwd(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sleep", "30"}, bin: "/usr/bin/bash"}
	m, _ := newCtlManager(t, l)
	repo, sub := wsRepo(t, "r")
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Cwd: sub, ParentID: "nobody-1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if got, want := s.Info().WorkspaceRoot, gitrepo.Normalize(repo); got != want {
		t.Fatalf("WorkspaceRoot = %q, want %q", got, want)
	}
}

func TestReconcileComputesMissingWorkspaceRoot(t *testing.T) {
	repo, sub := wsRepo(t, "repo")
	_, sub2 := wsRepo(t, "other")
	cloneOf := filepath.Join(t.TempDir(), "origin")
	fb := newFakeBackend()
	for _, id := range []string{"bash-1", "bash-2", "bash-3", "bash-4"} {
		fb.seedLive(id)
	}
	now := time.Now()
	path := writeSidecar(t, t.TempDir(), map[string]sessionMeta{
		// From before the field: computed from the cwd.
		"bash-1": {Agent: "bash", Cwd: sub, Created: now},
		// A clone session: computed from clone_of, not its cwd.
		"bash-2": {Agent: "bash", Cwd: sub2, CloneOf: cloneOf, Created: now},
		// A recorded value is kept as it was spawned, never recomputed.
		"bash-3": {Agent: "bash", Cwd: sub, WorkspaceRoot: "/recorded", Created: now},
		// A shell whose parent is loaded groups with it.
		"bash-4": {Agent: "bash", Cwd: sub2, ParentID: "bash-1", Created: now},
	})
	m := newReconcileManager(t, fb, path)
	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"bash-1", "bash-2", "bash-3", "bash-4"} {
		id := id
		t.Cleanup(func() { _ = m.Kill(id) })
	}
	want := map[string]string{
		"bash-1": gitrepo.Normalize(repo),
		"bash-2": gitrepo.Normalize(cloneOf),
		"bash-3": "/recorded",
		"bash-4": gitrepo.Normalize(repo),
	}
	for id, w := range want {
		s, ok := m.Get(id)
		if !ok {
			t.Fatalf("%s not reattached", id)
		}
		if got := s.Info().WorkspaceRoot; got != w {
			t.Errorf("%s WorkspaceRoot = %q, want %q", id, got, w)
		}
	}
}

// One repo reached two ways is one group: a clone_of spelled through a symlink
// and an in-place session in the real directory report the same string.
func TestWorkspaceRootOneRepoThroughASymlink(t *testing.T) {
	repo, sub := wsRepo(t, "repo")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}

	l, dir := testCloneLauncher(t, config.RuntimeBox)
	c := *l.clone
	c.Of = link
	l.clone = &c
	m, _ := cloneManager(t, l, &fakeCloneHooks{})
	cs, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Runtime: config.RuntimeBox, Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(cs.ID) })

	pl := &ctlLauncher{argv: []string{"sleep", "30"}, bin: "/usr/bin/bash"}
	pm, _ := newCtlManager(t, pl)
	ps, err := pm.Spawn(launcher.SpawnSpec{Agent: "bash", Cwd: sub})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pm.Kill(ps.ID) })

	if a, b := cs.Info().WorkspaceRoot, ps.Info().WorkspaceRoot; a == "" || a != b {
		t.Fatalf("clone via symlink %q, in place via real dir %q: want one string", a, b)
	}
}
