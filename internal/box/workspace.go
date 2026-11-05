package box

import (
	"errors"
)

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
