package box

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The refusal for a workspace that is not the checked path is both
// ErrBoxUnavailable (shown to the user as is) and ErrWorkspaceMountChanged
// (what the tests and callers tell it apart by), and its text is fixed.
func TestWsChangedIsBothErrorsAndReadsAsFixedText(t *testing.T) {
	err := wsChanged("/w/p", "it is now a symlink")
	if !errors.Is(err, ErrBoxUnavailable) || !errors.Is(err, ErrWorkspaceMountChanged) {
		t.Fatalf("err = %v, want ErrBoxUnavailable and ErrWorkspaceMountChanged", err)
	}
	if errors.Is(err, ErrUnboxableWorkspace) {
		t.Fatalf("err = %v must not be ErrUnboxableWorkspace: the folder is fine, the start raced", err)
	}
	if want := "workspace /w/p: it is now a symlink; refusing"; err.Error() != want {
		t.Fatalf("err = %q, want %q", err, want)
	}
}

func hostIDPtr(t *testing.T, p string) *FileID {
	t.Helper()
	id, err := hostID(p)
	if err != nil {
		t.Fatal(err)
	}
	return &id
}

// hostWorkspaceID is the host's own reading where the walk that follows no
// symlink is not available (macOS): the directory's identity, and the same
// refusals for a leaf that became a link, vanished or is not a directory.
func TestHostWorkspaceID(t *testing.T) {
	d := ctxTmp(t)
	dir, other := filepath.Join(d, "dir"), filepath.Join(d, "other")
	mkdirs(t, dir, other)
	file := filepath.Join(d, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d, "link")
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	got, err := hostWorkspaceID(dir)
	if err != nil || got == nil || *got != *hostIDPtr(t, dir) {
		t.Fatalf("hostWorkspaceID(dir) = %v, %v; want the directory's identity", got, err)
	}
	if *got == *hostIDPtr(t, other) {
		t.Fatal("test setup: two directories share an identity")
	}
	for name, p := range map[string]string{"a leaf that is now a symlink": link, "a leaf that is gone": filepath.Join(d, "gone")} {
		if _, err := hostWorkspaceID(p); !errors.Is(err, ErrWorkspaceMountChanged) || !errors.Is(err, ErrBoxUnavailable) {
			t.Fatalf("%s: err = %v, want ErrWorkspaceMountChanged and ErrBoxUnavailable", name, err)
		}
	}
	if _, err := hostWorkspaceID(file); !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("err = %v, want ErrUnboxableWorkspace saying it is not a directory", err)
	}
}

// Where the host records no walk identity (workspaceID gives none: macOS), the
// workspace still carries the host's own reading, for the one comparison that is
// between two host readings: the guards' root. ID stays nil there, since the
// identity compared with what the box shows is the VM's.
func TestWorkspaceAtKeepsAHostIdentityWhenThereIsNoWalkIdentity(t *testing.T) {
	d := ctxTmp(t)
	mkdirs(t, filepath.Join(d, "ws"))
	ws := filepath.Join(d, "ws")
	none := func(string) (*FileID, error) { return nil, nil }
	got, err := workspaceAt(ws, ws, none)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != nil {
		t.Fatalf("ID = %+v, want nil: the host's inode is not the box's", got.ID)
	}
	if got.HostID == nil || *got.HostID != *hostIDPtr(t, ws) {
		t.Fatalf("HostID = %+v, want the directory's %+v", got.HostID, hostIDPtr(t, ws))
	}
	if got.Path != ws || got.Given != ws {
		t.Fatalf("workspace = %+v", got)
	}

	// Where the walk gives one (Linux), the host identity is that one.
	id := &FileID{Dev: 7, Ino: 9}
	got, err = workspaceAt(ws, ws, func(string) (*FileID, error) { return id, nil })
	if err != nil || got.ID != id || got.HostID != id {
		t.Fatalf("workspaceAt = %+v, %v; want ID and HostID both %+v", got, err, id)
	}

	// A leaf that is now a link is refused there too, not mounted.
	link := filepath.Join(d, "link")
	if err := os.Symlink(ws, link); err != nil {
		t.Fatal(err)
	}
	if _, err := workspaceAt(link, link, none); !errors.Is(err, ErrWorkspaceMountChanged) {
		t.Fatalf("err = %v, want ErrWorkspaceMountChanged for a leaf that is a symlink", err)
	}
}

// The folder the guards are derived in is compared with the host reading of the
// workspace, whether or not the walk recorded an ID: a workspace value with only
// a HostID (macOS) is held to it, and the walk ID (Linux, equal to HostID) is
// not what is compared.
func TestInPlaceMountsAtComparesTheHostIdentity(t *testing.T) {
	d := ctxTmp(t)
	opened, checked := filepath.Join(d, "opened"), filepath.Join(d, "checked")
	mkdirs(t, opened, checked)

	_, err := inPlaceMountsAt(workspace{Given: opened, Path: opened, HostID: hostIDPtr(t, checked)})
	if !errors.Is(err, ErrWorkspaceMountChanged) || !strings.Contains(err.Error(), "the folder opened to derive its guards is not the one that was checked") {
		t.Fatalf("err = %v, want ErrWorkspaceMountChanged for a root that is not the checked folder", err)
	}
	if _, serr := os.Lstat(filepath.Join(opened, ".claude")); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf(".claude was created in the unchecked folder (Lstat: %v)", serr)
	}

	got, err := inPlaceMountsAt(workspace{Given: opened, Path: opened, HostID: hostIDPtr(t, opened)})
	if err != nil || len(got) != 1 {
		t.Fatalf("inPlaceMountsAt = %+v, %v; want the .claude guard for the matching root", got, err)
	}

	// ID is the one compared with what the box shows; a root that matches HostID
	// is accepted whatever ID holds.
	third := filepath.Join(d, "third")
	mkdirs(t, third)
	if _, err := inPlaceMountsAt(workspace{Given: third, Path: third, ID: hostIDPtr(t, checked), HostID: hostIDPtr(t, third)}); err != nil {
		t.Fatalf("err = %v, want the root accepted: HostID is the one compared with it", err)
	}
}

// The host's reading must not follow a link in any folder above the workspace
// (the Lstat it replaced did): a parent that is a link is a path that changed
// since it was resolved, however the link leads.
func TestHostWorkspaceIDRefusesALinkInAParent(t *testing.T) {
	d := ctxTmp(t)
	real := filepath.Join(d, "a", "b")
	mkdirs(t, real)
	link := filepath.Join(d, "link")
	if err := os.Symlink(filepath.Join(d, "a"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := hostWorkspaceID(real); err != nil {
		t.Fatalf("the real path: %v", err)
	}
	_, err := hostWorkspaceID(filepath.Join(link, "b"))
	if !errors.Is(err, ErrWorkspaceMountChanged) || !errors.Is(err, ErrBoxUnavailable) {
		t.Fatalf("err = %v, want ErrWorkspaceMountChanged and ErrBoxUnavailable for a parent that is a symlink", err)
	}
}
