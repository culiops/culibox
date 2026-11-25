package session

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

// The quick shell beside a clone session runs in a box in the clone's directory,
// with no clone setup of its own. That directory is box-owned, so it is already a
// private copy: no needs_copy, and Wrap is told (BoxKey.InCopy). Outside a
// box-owned directory the same spawn is refused (#371).
func TestBoxShellInAPrivateCopyIsNotRefused(t *testing.T) {
	repo, _ := wsRepo(t, "copy") // the clone dir is a git repo
	l := cloneLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}}

	hooks := &fakeCloneHooks{owned: "/c/copy is inside the clone of a clone-mode profile"}
	m, boxes := cloneManager(t, l, hooks)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "sh", Runtime: config.RuntimeBox, Cwd: repo, ParentID: "clone-1"})
	if err != nil {
		t.Fatalf("a shell in a box-owned directory: %v", err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if !boxes.lastKey.InCopy || boxes.lastKey.Clone != nil {
		t.Fatalf("key = %+v, want InCopy and no Clone", boxes.lastKey)
	}

	// Its restart: a shell session has no CloneOf, so it spawns in place again.
	lx := cloneLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "exit 0"}, runtime: config.RuntimeBox}}
	mx, _ := cloneManager(t, lx, &fakeCloneHooks{owned: hooks.owned})
	old := exitedSession(t, mx, launcher.SpawnSpec{Agent: "sh", Runtime: config.RuntimeBox, Cwd: repo, ParentID: "clone-1"})
	ns, err := mx.Restart(old.ID)
	if err != nil {
		t.Fatalf("restart of a shell in a private copy: %v", err)
	}
	t.Cleanup(func() { _ = mx.Kill(ns.ID) })

	// A directory the clone hooks do not own is a host-owned repo: refused.
	m2, _ := cloneManager(t, l, &fakeCloneHooks{})
	_, err = m2.Spawn(launcher.SpawnSpec{Agent: "sh", Runtime: config.RuntimeBox, Cwd: repo, ParentID: "clone-1"})
	var nc *box.NeedsCopyError
	if !errors.As(err, &nc) {
		t.Fatalf("err = %v, want *box.NeedsCopyError for a repo that is not box-owned", err)
	}
}

// clone:false on a clone profile, in a box, at its clone_of repo: the repo is a
// host-owned git repo, so the spawn is refused with needs_copy (it used to box
// the repo in place).
func TestCloneFalseBoxAtCloneOfIsRefused(t *testing.T) {
	repo, _ := wsRepo(t, "orig")
	l, _ := testCloneLauncher(t, config.RuntimeBox)
	l.clone.Of = repo
	m, boxes := cloneManager(t, l, &fakeCloneHooks{})
	_, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Runtime: config.RuntimeBox, Clone: boolPtr(false), Cwd: repo})
	var nc *box.NeedsCopyError
	if !errors.As(err, &nc) || nc.Repo != repo {
		t.Fatalf("err = %v, want *box.NeedsCopyError for %s", err, repo)
	}
	if boxes.prepares != 0 {
		t.Fatalf("Prepare ran for a refused spawn: %d", boxes.prepares)
	}
}

// A restart of a session that was boxed in place in what is now a git repo is
// refused the same way: it never reaches the box layer.
func TestRestartOfAnInPlaceGitSessionIsRefused(t *testing.T) {
	repo, _ := wsRepo(t, "r")
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "exit 0"})
	old := &Session{ID: "bash-1", Agent: "bash", name: "b", Cwd: repo, Runtime: config.RuntimeBox, status: statusExited}
	m.mu.Lock()
	m.sessions[old.ID] = old
	m.mu.Unlock()
	_, err := m.Restart(old.ID)
	var nc *box.NeedsCopyError
	if !errors.As(err, &nc) || nc.Repo != repo {
		t.Fatalf("Restart err = %v, want *box.NeedsCopyError for %s", err, filepath.Base(repo))
	}
	if boxes.prepares != 0 {
		t.Fatalf("Prepare ran for a refused restart: %d", boxes.prepares)
	}
}
