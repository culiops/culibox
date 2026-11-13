package box

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// workspace is a workspace directory resolved once. The protected check, the box
// name, the in-place guards' root, the mount and the label all take Path, never
// a spelling of the directory looked up again, so a path component that changes
// after the resolution cannot send any one of them somewhere the others did not
// look.
type workspace struct {
	// Given is the cwd as the caller passed it. Refusals name it, since it is
	// the path the user chose.
	Given string
	// Path is absolute, clean and symlink-resolved.
	Path string
	// ID is the identity of Path read on the host by a walk that follows no
	// symlink (workspaceID). Nil when the host records none (macOS: the box VM
	// reads it).
	ID *FileID
	// HostID is the host's own reading of Path's identity, on every OS: ID where
	// the host records one (Linux), else an Lstat of Path (macOS, hostWorkspaceID).
	// A Mac inode number is not the one the box sees, so HostID is never compared
	// with anything read in the box; it is only compared with another host
	// reading: the folder the in-place guards are derived in (inPlaceMountsAt).
	HostID *FileID
}

// resolvePath is cwd made absolute and symlink-resolved: the one spelling a
// workspace is checked, named and mounted by. A `..` after a symlink leads where
// the kernel takes it (the link target's parent): the path is resolved as it
// stands, never cleaned first, which filepath.Abs does. A relative cwd hangs off
// the resolved working directory (the process's own spelling of it may go through
// a link). An error is the text BoxName has always returned for a path that does
// not resolve.
func resolvePath(cwd string) (string, error) {
	p := cwd
	if !filepath.IsAbs(p) {
		wd, err := os.Getwd()
		if err == nil {
			wd, err = filepath.EvalSymlinks(wd)
		}
		if err != nil {
			return "", fmt.Errorf("box: absolute path for %q: %w", cwd, err)
		}
		p = wd + string(filepath.Separator) + cwd // not filepath.Join: it would clean a `..` lexically
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("box: resolving workspace %q: %w", cwd, err)
	}
	return real, nil
}

// resolveWorkspace resolves cwd once (resolvePath) and records the identity of
// the result (workspaceAt). Wrap runs the two steps itself, so that it can word
// a resolution that failed differently from an identity that could not be read.
func resolveWorkspace(cwd string) (workspace, error) {
	real, err := resolvePath(cwd)
	if err != nil {
		return workspace{}, err
	}
	return workspaceAt(cwd, real, workspaceID)
}

// mount is the workspace as an identity-checked mount: the folder bound at its
// own path, carrying the identity that was recorded for it. With vm set the
// identity is the box VM's to read (Options.VMIDs is the kernel view the box
// shows), so none is carried.
func (ws workspace) mount(vm bool) Mount {
	mt := Mount{Host: ws.Path, Box: ws.Path, ID: ws.ID, CheckID: true, kind: mountWorkspace}
	if vm {
		mt.ID = nil
	}
	return mt
}

// workspaceAt is the workspace for cwd that resolvePath resolved to real: real
// with the identity of the directory it names. idOf reads it by a walk that
// follows no symlink (workspaceID; nil, nil where the host records none). A
// failure is a changed path (ErrWorkspaceMountChanged) or a leaf that is not a
// directory, never a path that does not resolve. Where idOf gives no identity the
// host's own reading is still taken (hostWorkspaceID), so every OS is held to the
// folder it resolved.
func workspaceAt(cwd, real string, idOf func(string) (*FileID, error)) (workspace, error) {
	id, err := idOf(real)
	if err != nil {
		return workspace{}, err
	}
	host := id
	if host == nil {
		if host, err = hostWorkspaceID(real); err != nil {
			return workspace{}, err
		}
	}
	return workspace{Given: cwd, Path: real, ID: id, HostID: host}, nil
}

// hostWorkspaceID is the host's identity reading of the workspace directory p
// (already resolved) where the descriptor walk of workspaceID is not available
// (macOS): a read that follows no symlink in any component (hostNoFollowStat),
// with the same refusals for a leaf that has become a symlink or vanished, and
// for one that is not a directory. A link anywhere above it, swapped in since the
// resolution that precedes this, is refused too: an Lstat would follow it.
func hostWorkspaceID(p string) (*FileID, error) {
	st, err := hostNoFollowStat(p)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, wsChanged(p, "its path is now a symlink or has a symlink in it")
		}
		return nil, wsChanged(p, fmt.Sprintf("it cannot be read (%v)", err))
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
	case unix.S_IFLNK:
		return nil, wsChanged(p, "its path is now a symlink")
	default:
		return nil, fmt.Errorf("%w: %s: it is not a directory", ErrUnboxableWorkspace, p)
	}
	return &FileID{Dev: uint64(st.Dev), Ino: uint64(st.Ino)}, nil
}

// hostFileID is the identity the host's stat reports for fi, on any OS, nil when
// it carries none. Unlike fileInfoID it is not nil on macOS, so it is only for
// comparing two readings made on the host (see workspace.HostID).
func hostFileID(fi fs.FileInfo) *FileID {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return &FileID{Dev: uint64(st.Dev), Ino: uint64(st.Ino)}
}

// wrapResolvedHook, when set (tests only), runs in Wrap right after
// resolveWorkspace, with the result.
var wrapResolvedHook func(ws workspace)

// ErrWorkspaceMountChanged marks a spawn refused because the workspace, seen
// from where it is mounted, is not the directory that was resolved and checked:
// a path component changed between the check and the mount. It also reports
// ErrBoxUnavailable (see wsChanged), so a caller that shows ErrBoxUnavailable
// as is shows this too.
var ErrWorkspaceMountChanged = errors.New("workspace mount is not the checked path")

// wsMountError is the ErrWorkspaceMountChanged refusal. It is not an
// ErrUnboxableWorkspace: the folder is fine and the start raced, so a retry may
// succeed.
type wsMountError struct {
	msg string
	// also are further errors it is, for a refusal that covers more than the
	// workspace (identityFailed with context mounts in the same batch).
	also []error
}

func (e *wsMountError) Error() string { return e.msg }
func (e *wsMountError) Unwrap() []error {
	return append([]error{ErrBoxUnavailable, ErrWorkspaceMountChanged}, e.also...)
}

// wsChanged is the refusal for a workspace p that is not the checked path, with
// why saying how. Its text is "workspace <p>: <why>; refusing".
func wsChanged(p, why string) error {
	return &wsMountError{msg: "workspace " + p + ": " + why + "; refusing"}
}
