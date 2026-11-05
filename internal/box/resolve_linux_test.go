//go:build linux

package box

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveWorkspaceIsTheRealDirectoryAndItsIdentity(t *testing.T) {
	d := ctxTmp(t)
	dir := filepath.Join(d, "dir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := resolveWorkspace(link)
	if err != nil {
		t.Fatal(err)
	}
	if ws.Path != want || ws.Given != link {
		t.Fatalf("ws = %+v, want Path %q and Given %q", ws, want, link)
	}
	if ws.ID == nil || *ws.ID != lstatID(t, dir) {
		t.Fatalf("ws.ID = %+v, want the directory's %+v", ws.ID, lstatID(t, dir))
	}
	if ws.HostID == nil || *ws.HostID != *ws.ID {
		t.Fatalf("ws.HostID = %+v, want ID %+v: where the host records one, it is the host reading", ws.HostID, ws.ID)
	}
}

func TestResolveWorkspaceRefusesAFile(t *testing.T) {
	f := filepath.Join(ctxTmp(t), "f")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := resolveWorkspace(f)
	if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("err = %v, want ErrUnboxableWorkspace saying it is not a directory", err)
	}
}

// The folder opened to derive the guards must be the one that was checked: a
// root that is another directory is refused before anything is read or made in
// it.
func TestInPlaceMountsAtRefusesARootThatIsNotTheResolvedOne(t *testing.T) {
	d := ctxTmp(t)
	opened, checked := filepath.Join(d, "opened"), filepath.Join(d, "checked")
	for _, p := range []string{opened, checked} {
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	id := lstatID(t, checked)
	_, err := inPlaceMountsAt(workspace{Given: opened, Path: opened, HostID: &id})
	if !errors.Is(err, ErrWorkspaceMountChanged) || !errors.Is(err, ErrBoxUnavailable) {
		t.Fatalf("err = %v, want ErrWorkspaceMountChanged and ErrBoxUnavailable", err)
	}
	if !strings.Contains(err.Error(), "the folder opened to derive its guards is not the one that was checked") {
		t.Fatalf("err = %q", err)
	}
	if _, serr := os.Lstat(filepath.Join(opened, ".claude")); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf(".claude was created in the unchecked folder (Lstat: %v)", serr)
	}
}

// The matching root is derived as inPlaceMounts always did, and the recorded
// identity is only a comparison: .claude is made as before.
func TestInPlaceMountsAtAcceptsTheResolvedRoot(t *testing.T) {
	ws := ctxTmp(t)
	id := lstatID(t, ws)
	got, err := inPlaceMountsAt(workspace{Given: ws, Path: ws, HostID: &id})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(ws, ".claude")
	if len(got) != 1 || got[0].Host != want || got[0].Box != want {
		t.Fatalf("mounts = %+v, want one self-bind of %s", got, want)
	}
}

// D9: the caller resolves. protectedOverlapResolved takes the path as given, so
// a link to the state root is not the state root to it, while ProtectedOverlap
// resolves and refuses it.
func TestProtectedOverlapResolvedDoesNotFollowALink(t *testing.T) {
	d := ctxTmp(t)
	state := filepath.Join(d, "state")
	if err := os.Mkdir(state, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d, "link")
	if err := os.Symlink(state, link); err != nil {
		t.Fatal(err)
	}
	dirs := []ProtectedDir{DataDirProtected(state)}
	if r := ProtectedOverlap(link, dirs); !strings.Contains(r, "inside agent-box's data directory") && !strings.Contains(r, "it is agent-box's data directory") {
		t.Fatalf("ProtectedOverlap(link) = %q, want a refusal: it resolves the link", r)
	}
	if r := protectedOverlapResolved(link, dirs); r != "" {
		t.Fatalf("protectedOverlapResolved(link) = %q, want none: it does not resolve", r)
	}
	if r := protectedOverlapResolved(state, dirs); r == "" {
		t.Fatal("protectedOverlapResolved(state) = none, want a refusal for the directory itself")
	}
}

// With the guards on, a parent of the resolved workspace swapped for a symlink
// after the resolution leaves the guards' root opening a different folder. Wrap
// refuses before anything is created in it or started.
func TestWrapRefusesGuardsDerivedInAFolderThatIsNotTheCheckedOne(t *testing.T) {
	f := newFake()
	m, err := New(Options{DockerBin: "/usr/bin/docker", Image: "img", Instance: "TEST", IdleTTL: time.Hour,
		StateRoot: t.TempDir(), Client: f, GuardInPlace: true})
	if err != nil {
		t.Fatal(err)
	}
	root := ctxTmp(t)
	parent, other := filepath.Join(root, "parent"), filepath.Join(root, "other")
	for _, p := range []string{filepath.Join(parent, "ws"), filepath.Join(other, "ws")} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	wrapResolvedHook = func(workspace) {
		if err := os.Rename(parent, filepath.Join(root, "moved")); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(other, parent); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { wrapResolvedHook = nil })

	_, _, _, err = m.Wrap(context.Background(), filepath.Join(parent, "ws"), "s1", BoxKey{}, []string{"claude"}, nil, nil)
	if !errors.Is(err, ErrWorkspaceMountChanged) {
		t.Fatalf("err = %v, want ErrWorkspaceMountChanged", err)
	}
	if _, serr := os.Lstat(filepath.Join(other, "ws", ".claude")); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf(".claude was created in the folder the path now leads to (Lstat: %v)", serr)
	}
	if len(f.runArgs) != 0 {
		t.Fatalf("docker run called %d times for a refused workspace", len(f.runArgs))
	}
}

// A path that changes while its identity is read is ErrWorkspaceMountChanged
// even when, by the time the refusal is worded, the path no longer resolves and
// the place it now leads is agent-box's data directory: the wording fallback
// for a cwd that does not resolve is for the first resolution failing, not for
// a later reading of a path that moved.
func TestWrapNamesAChangedPathNotAnUnboxableOneWhenItAlsoVanishes(t *testing.T) {
	f := newFake()
	state := ctxTmp(t)
	m, err := New(Options{DockerBin: "/usr/bin/docker", Image: "img", Instance: "TEST", IdleTTL: time.Hour,
		StateRoot: state, Client: f})
	if err != nil {
		t.Fatal(err)
	}
	root := ctxTmp(t)
	parent := filepath.Join(root, "parent")
	if err := os.MkdirAll(filepath.Join(parent, "ws"), 0o755); err != nil {
		t.Fatal(err)
	}
	// After the walk opens root, parent is swapped for a link into the state
	// root, which has no ws: the walk fails, and so does every later resolution.
	ctxWalkHook = func(dir string) {
		if dir != root {
			return
		}
		if err := os.Rename(parent, filepath.Join(root, "moved")); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(state, parent); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { ctxWalkHook = nil })

	_, _, _, err = m.Wrap(context.Background(), filepath.Join(parent, "ws"), "s1", BoxKey{}, []string{"claude"}, nil, nil)
	if !errors.Is(err, ErrWorkspaceMountChanged) || !errors.Is(err, ErrBoxUnavailable) {
		t.Fatalf("err = %v, want ErrWorkspaceMountChanged and ErrBoxUnavailable", err)
	}
	if errors.Is(err, ErrUnboxableWorkspace) {
		t.Fatalf("err = %v, a spawn that raced is not an unboxable workspace", err)
	}
	if f.runs() != 0 {
		t.Fatalf("docker run called %d times for a refused workspace", f.runs())
	}
}
