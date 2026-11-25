package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

// The quick shell beside a clone session runs in a box in the clone's directory,
// with no clone setup of its own. A cwd that resolves to a known private copy is
// not refused with needs_copy, and Wrap is given the copy (BoxKey.CopyDir). A git
// folder that is not a positive match is refused (#371).
func TestBoxShellInAPrivateCopyIsNotRefused(t *testing.T) {
	repo, _ := wsRepo(t, "copy") // the clone dir is a git repo
	copyDir, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	l := cloneLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}}

	hooks := &fakeCloneHooks{copyDir: copyDir}
	m, boxes := cloneManager(t, l, hooks)
	var recorded [][2]string
	m.SetBoxedRecorder(func(cwd, cp string) error { recorded = append(recorded, [2]string{cwd, cp}); return nil })
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "sh", Runtime: config.RuntimeBox, Cwd: repo, ParentID: "clone-1"})
	if err != nil {
		t.Fatalf("a shell in a private copy: %v", err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if boxes.lastKey.CopyDir != copyDir || boxes.lastKey.Clone != nil {
		t.Fatalf("key = %+v, want CopyDir %s and no Clone", boxes.lastKey, copyDir)
	}
	if len(recorded) != 1 || recorded[0][1] != copyDir {
		t.Fatalf("the recorder was given %v, want the copy %s", recorded, copyDir)
	}

	// Its restart: a shell session has no CloneOf, so it spawns in place again.
	lx := cloneLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "exit 0"}, runtime: config.RuntimeBox}}
	mx, _ := cloneManager(t, lx, &fakeCloneHooks{copyDir: copyDir})
	old := exitedSession(t, mx, launcher.SpawnSpec{Agent: "sh", Runtime: config.RuntimeBox, Cwd: repo, ParentID: "clone-1"})
	ns, err := mx.Restart(old.ID)
	if err != nil {
		t.Fatalf("restart of a shell in a private copy: %v", err)
	}
	t.Cleanup(func() { _ = mx.Kill(ns.ID) })

	// A git folder that is no known copy is a host-owned repo: refused.
	m2, _ := cloneManager(t, l, &fakeCloneHooks{})
	_, err = m2.Spawn(launcher.SpawnSpec{Agent: "sh", Runtime: config.RuntimeBox, Cwd: repo, ParentID: "clone-1"})
	var nc *box.NeedsCopyError
	if !errors.As(err, &nc) {
		t.Fatalf("err = %v, want *box.NeedsCopyError for a repo that is no known copy", err)
	}
}

// A box plants <clone>/src -> a host repo. The folder is asked about as it
// resolves (the host repo), so it is no copy: refused, not boxed in place with
// the refusal skipped.
func TestBoxShellThroughAPlantedLinkIsRefused(t *testing.T) {
	cloneDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hostRepo, _ := wsRepo(t, "hostrepo")
	hostReal, _ := filepath.EvalSymlinks(hostRepo)
	link := filepath.Join(cloneDir, "src")
	if err := os.Symlink(hostReal, link); err != nil {
		t.Fatal(err)
	}
	l := cloneLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}}
	hooks := &fakeCloneHooks{copyDir: cloneDir}
	m, boxes := cloneManager(t, l, hooks)
	_, err = m.Spawn(launcher.SpawnSpec{Agent: "sh", Runtime: config.RuntimeBox, Cwd: link, ParentID: "clone-1"})
	var nc *box.NeedsCopyError
	if !errors.As(err, &nc) || nc.Repo != hostReal {
		t.Fatalf("err = %v, want *box.NeedsCopyError for %s", err, hostReal)
	}
	if got := hooks.copyDirAsked; len(got) != 1 || got[0] != hostReal {
		t.Fatalf("CopyDirOf was asked about %v, want the resolved %s", got, hostReal)
	}
	if boxes.prepares != 0 {
		t.Fatalf("Prepare ran for a refused spawn: %d", boxes.prepares)
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
