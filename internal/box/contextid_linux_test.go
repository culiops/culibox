//go:build linux

package box

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func lstatID(t *testing.T, p string) FileID {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Lstat(p, &st); err != nil {
		t.Fatal(err)
	}
	return FileID{Dev: uint64(st.Dev), Ino: uint64(st.Ino)}
}

// The identity is the checked leaf's.
func TestCheckContextPathIDIsTheLeafs(t *testing.T) {
	d := ctxTmp(t)
	dir := filepath.Join(d, "docs")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id, err := CheckContextPathID(dir, "/h", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if id == nil || *id != lstatID(t, dir) {
		t.Fatalf("id = %+v, want %+v", id, lstatID(t, dir))
	}
}

// A parent flipped to a symlink after its own step of the walk must not move
// the walk: the identity stays the original leaf's, so docker mounting the
// link's target later fails the in-box comparison. A plain Lstat of the full
// path at that moment would follow the link and record the target instead.
func TestCheckContextPathIDParentSwappedMidWalk(t *testing.T) {
	d := ctxTmp(t)
	real := filepath.Join(d, "real")
	other := filepath.Join(d, "other")
	for _, p := range []string{filepath.Join(real, "x"), filepath.Join(other, "x")} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	orig := lstatID(t, filepath.Join(real, "x"))
	target := lstatID(t, filepath.Join(other, "x"))
	swapped := false
	ctxWalkHook = func(dir string) {
		if dir != real || swapped {
			return
		}
		swapped = true
		if err := os.Rename(real, filepath.Join(d, "moved")); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(other, real); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { ctxWalkHook = nil })
	id, err := CheckContextPathID(filepath.Join(real, "x"), "/h", nil, nil)
	if !swapped {
		t.Fatal("the hook never ran: the walk did not visit the parent")
	}
	if err != nil {
		return // refusing is also safe
	}
	if id == nil || *id == target || *id != orig {
		t.Fatalf("id = %+v, want the original leaf %+v (the link target is %+v)", id, orig, target)
	}
}

// A parent that is already a symlink when the identity walk reaches it is
// refused, even if the component walk before it saw a directory.
func TestCheckContextPathIDRefusesSymlinkedParentAtWalk(t *testing.T) {
	d := ctxTmp(t)
	other := filepath.Join(d, "other")
	if err := os.MkdirAll(filepath.Join(other, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d, "link")
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if _, err := leafID(filepath.Join(link, "x")); err == nil {
		t.Fatal("the identity walk followed a symlinked parent")
	}
	if _, err := leafID(link); err == nil {
		t.Fatal("the identity walk accepted a symlink leaf")
	}
}

// The workspace identity is the directory's own.
func TestWorkspaceIDIsTheDirectorys(t *testing.T) {
	d := ctxTmp(t)
	dir := filepath.Join(d, "ws")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id, err := workspaceID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if id == nil || *id != lstatID(t, dir) {
		t.Fatalf("id = %+v, want %+v", id, lstatID(t, dir))
	}
}

// A resolved workspace path holds no symlink, so one met at any component, the
// leaf included, means the path changed after it was resolved.
func TestWorkspaceIDRefusesASymlinkAnywhere(t *testing.T) {
	d := ctxTmp(t)
	other := filepath.Join(d, "other")
	if err := os.MkdirAll(filepath.Join(other, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d, "link")
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]string{"parent": filepath.Join(link, "x"), "leaf": link} {
		id, err := workspaceID(p)
		if err == nil || id != nil {
			t.Fatalf("%s link: workspaceID(%q) = %+v, %v; want a refusal", name, p, id, err)
		}
		if !errors.Is(err, ErrWorkspaceMountChanged) || !errors.Is(err, ErrBoxUnavailable) {
			t.Fatalf("%s link: err = %v, want ErrWorkspaceMountChanged and ErrBoxUnavailable", name, err)
		}
	}
}

// A file is not a workspace: that is the user's to fix, not a race.
func TestWorkspaceIDRefusesAFile(t *testing.T) {
	d := ctxTmp(t)
	f := filepath.Join(d, "f")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := workspaceID(f)
	if err == nil || id != nil {
		t.Fatalf("workspaceID(file) = %+v, %v; want a refusal", id, err)
	}
	if !errors.Is(err, ErrUnboxableWorkspace) || errors.Is(err, ErrWorkspaceMountChanged) {
		t.Fatalf("err = %v, want ErrUnboxableWorkspace and not ErrWorkspaceMountChanged", err)
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("err = %q, want it to say \"not a directory\"", err)
	}
}

// The walk is the same one the context paths use: a parent flipped to a symlink
// after its own step must not move it. The identity is the original
// directory's, or the walk refuses; it is never the link target's.
func TestWorkspaceIDParentSwappedMidWalk(t *testing.T) {
	d := ctxTmp(t)
	real := filepath.Join(d, "real")
	other := filepath.Join(d, "other")
	for _, p := range []string{filepath.Join(real, "x"), filepath.Join(other, "x")} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	orig := lstatID(t, filepath.Join(real, "x"))
	target := lstatID(t, filepath.Join(other, "x"))
	swapped := false
	ctxWalkHook = func(dir string) {
		if dir != real || swapped {
			return
		}
		swapped = true
		if err := os.Rename(real, filepath.Join(d, "moved")); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(other, real); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { ctxWalkHook = nil })
	id, err := workspaceID(filepath.Join(real, "x"))
	if !swapped {
		t.Fatal("the hook never ran: the walk did not visit the parent")
	}
	if err != nil {
		return // refusing is also safe
	}
	if id == nil || *id == target || *id != orig {
		t.Fatalf("id = %+v, want the original directory %+v (the link target is %+v)", id, orig, target)
	}
}

// fileInfoID reads the same identity an Lstat of the path gives.
func TestFileInfoIDIsTheLstatIdentity(t *testing.T) {
	d := ctxTmp(t)
	fi, err := os.Lstat(d)
	if err != nil {
		t.Fatal(err)
	}
	id := fileInfoID(fi)
	if id == nil || *id != lstatID(t, d) {
		t.Fatalf("fileInfoID = %+v, want %+v", id, lstatID(t, d))
	}
}
