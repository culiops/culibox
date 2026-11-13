package box

import (
	"path/filepath"
	"strings"
)

// VMShare is one folder the box VM mounts. Path is symlink-resolved.
type VMShare struct {
	Path     string
	Writable bool
}

// ShareView is what the share checks read: the folders the running VM has
// applied (its written colima.yaml: the only ones docker can see) and the folders
// the user listed (applied or not yet). Fold compares paths case-insensitively
// (APFS). Home is the user's home, shown as "~" in refusals.
type ShareView struct {
	Applied, Listed []VMShare
	Home            string
	Fold            bool
}

// SharesFunc reads the live ShareView. Options.Shares nil = no VM, every path
// visible (Linux).
type SharesFunc func() (ShareView, error)

// ErrVMRestarting is the refusal while the box VM restarts to apply its folder
// list. It is a ErrBoxUnavailable.
var ErrVMRestarting error = vmRestartingError{}

type vmRestartingError struct{}

func (vmRestartingError) Error() string {
	return "the box VM is restarting to apply its folder list; try again in about 20 s"
}
func (vmRestartingError) Unwrap() error { return ErrBoxUnavailable }

// NotSharedError is the refusal of a path the box VM cannot see (or cannot
// write). It names the folder and the fix.
type NotSharedError struct {
	Dir      string
	Pending  bool // listed by the user, not applied to the VM yet
	ReadOnly bool // shared read-only, but a workspace needs read & write
	// NeedWrite: the box writes the folder (a workspace, a clone copy). False
	// for what it only reads (clone_of, a context path): the hint then offers
	// a read-only share.
	NeedWrite bool
	Home      string
}

func (e *NotSharedError) Unwrap() error { return ErrUnboxableWorkspace }

func (e *NotSharedError) Error() string {
	d := tildePath(e.Dir, e.Home)
	switch {
	case e.ReadOnly:
		return d + " is shared read-only with the box VM, but a workspace needs read & write. " +
			"Switch it in Settings › VM folders, or run `culibox box share add " + d + "`"
	case e.Pending:
		return d + " was added to the box VM folders but not applied yet: apply it in Settings › VM folders " +
			"(this restarts the box VM), or run `culibox box share apply`"
	}
	ro := ""
	if !e.NeedWrite {
		ro = " -ro"
	}
	return d + " is not shared with the box VM, so a box can’t use it. " +
		"Add it in Settings › VM folders, or run `culibox box share add " + d + ro + "`"
}

// tildePath shows p with a leading home directory as "~".
func tildePath(p, home string) string {
	if home == "" {
		return p
	}
	home = filepath.Clean(home)
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

// Check returns nil when the VM can use abs (symlink-resolved), writing it when
// needWrite, or the *NotSharedError that says why not.
func (v ShareView) Check(abs string, needWrite bool) error {
	readOnly := false
	for _, s := range v.Applied {
		if !pathWithin(abs, s.Path, v.Fold) {
			continue
		}
		if s.Writable || !needWrite {
			return nil
		}
		readOnly = true
	}
	if readOnly {
		return &NotSharedError{Dir: abs, ReadOnly: true, NeedWrite: true, Home: v.Home}
	}
	for _, s := range v.Listed {
		if pathWithin(abs, s.Path, v.Fold) {
			return &NotSharedError{Dir: abs, Pending: true, NeedWrite: needWrite, Home: v.Home}
		}
	}
	return &NotSharedError{Dir: abs, NeedWrite: needWrite, Home: v.Home}
}

// view reads the share view; nil = no VM, no check.
func (o Options) view() (*ShareView, error) {
	if o.Shares == nil {
		return nil, nil
	}
	v, err := o.Shares()
	if err != nil {
		return nil, err
	}
	return &v, nil
}
