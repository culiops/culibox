package box

import (
	"errors"
	"fmt"
	"path/filepath"
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
}

// resolvePath is cwd made absolute and symlink-resolved: the one spelling a
// workspace is checked, named and mounted by. An error is the text BoxName has
// always returned for a path that does not resolve.
func resolvePath(cwd string) (string, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("box: absolute path for %q: %w", cwd, err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("box: resolving workspace %q: %w", cwd, err)
	}
	return real, nil
}

// resolveWorkspace resolves cwd once (resolvePath) and records the identity of
// the result (workspaceID). Wrap runs it first and carries the result through.
func resolveWorkspace(cwd string) (workspace, error) {
	real, err := resolvePath(cwd)
	if err != nil {
		return workspace{}, err
	}
	id, err := workspaceID(real)
	if err != nil {
		return workspace{}, err
	}
	return workspace{Given: cwd, Path: real, ID: id}, nil
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
type wsMountError struct{ msg string }

func (e *wsMountError) Error() string { return e.msg }
func (e *wsMountError) Unwrap() []error {
	return []error{ErrBoxUnavailable, ErrWorkspaceMountChanged}
}

// wsChanged is the refusal for a workspace p that is not the checked path, with
// why saying how. Its text is "workspace <p>: <why>; refusing".
func wsChanged(p, why string) error {
	return &wsMountError{"workspace " + p + ": " + why + "; refusing"}
}
