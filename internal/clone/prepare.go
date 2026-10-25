package clone

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/config"
)

// Env is what Prepare needs from the daemon.
type Env struct {
	Git        Git                // the host git, used only to create the clone
	Home       string             // the host user's home
	Protected  []box.ProtectedDir // dirs no mount may be, contain or (Inside) sit in
	MemoryRoot string             // the bundle source dir (~/.claude); "" = no memory mount
	DataDir    string             // the daemon's data dir, for the base record; "" = no base record
}

// CheckOf refuses a clone_of agent-box must not clone from or mount the .git of
// (spec correction 8): it must be a real directory (not a symlink), not /, the
// home or an ancestor of it, and hold a real .git directory that borrows no
// objects or refs from elsewhere (no commondir, no alternates), whose path is
// safe to mount and overlaps no protected dir.
func CheckOf(of, home string, protected []box.ProtectedDir) error {
	refuse := func(why string) error {
		return fmt.Errorf("%w: clone_of %s %s", box.ErrUnboxableWorkspace, of, why)
	}
	st, err := os.Lstat(of)
	switch {
	case err != nil:
		return refuse("cannot be read: " + err.Error())
	case st.Mode()&fs.ModeSymlink != 0:
		return refuse("is a symlink; use the resolved path")
	case !st.IsDir():
		return refuse("is not a directory")
	}
	if !filepath.IsAbs(home) {
		return refuse(fmt.Sprintf("cannot be checked against the home directory (home %q is not an absolute path)", home))
	}
	if of == "/" {
		return refuse("is the home directory or above it; clone a repo, not the home")
	}
	if above, err := isHomeOrAbove(of, home); err != nil {
		return refuse("cannot be checked against the home directory: " + err.Error())
	} else if above {
		return refuse("is the home directory or above it; clone a repo, not the home")
	}
	root, err := os.OpenRoot(of)
	if err != nil {
		return refuse("cannot be opened: " + err.Error())
	}
	defer root.Close()
	if gst, err := root.Lstat(".git"); err != nil || !gst.IsDir() {
		return refuse("has no .git directory (a .git file, a worktree or a separate git dir cannot be cloned from)")
	}
	for _, p := range []string{".git/commondir", ".git/objects/info/alternates"} {
		if _, err := root.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			return refuse("has " + p + "; a repo that borrows objects or refs from elsewhere cannot be cloned from")
		}
	}
	gitDir := filepath.Join(of, ".git")
	if why := box.MountUnsafe(gitDir); why != "" {
		return refuse("cannot be mounted: " + why)
	}
	if why := box.ProtectedOverlap(gitDir, protected); why != "" {
		return refuse("cannot be mounted: its .git: " + why)
	}
	return nil
}

// isHomeOrAbove reports whether of is home or an ancestor of it, comparing both
// as spelled and resolved. A path that cannot be resolved is an error.
func isHomeOrAbove(of, home string) (bool, error) {
	ofs, err := spellings(of)
	if err != nil {
		return false, err
	}
	homes, err := spellings(home)
	if err != nil {
		return false, err
	}
	for _, o := range ofs {
		for _, h := range homes {
			if under(h, o) {
				return true, nil
			}
		}
	}
	return false, nil
}

// CheckProfile is the save-time check of a clone profile: CheckOf, an existing
// clone dir must be a real directory, and every share whose source exists must
// pass the share-source rules. An absent source or clone dir is fine at save;
// the spawn refuses a missing source and creates the clone.
func CheckProfile(c config.Clone, home string, protected []box.ProtectedDir) error {
	if err := CheckOf(c.Of, home, protected); err != nil {
		return err
	}
	if st, err := os.Lstat(c.Dir); err == nil {
		if st.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s is a symlink; use the resolved path in default_cwd", box.ErrUnboxableWorkspace, c.Dir)
		}
		if !st.IsDir() {
			return fmt.Errorf("%w: %s exists and is not a directory", box.ErrUnboxableWorkspace, c.Dir)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s cannot be read: %v", box.ErrUnboxableWorkspace, c.Dir, err)
	}
	for _, rel := range c.Shares {
		if _, err := shareSource(c.Of, rel, protected); err != nil && !errors.Is(err, errShareMissing) {
			return err
		}
	}
	return nil
}

// Prepare readies a clone-mode spawn on the host, before any box runs: it checks
// clone_of, creates the clone if absent, checks every share source and makes
// its mountpoint in the clone, and returns the box's clone mounts. Every mount
// is a read-only bind (D12); there is no other list. Any error refuses the
// spawn: a required mount is never dropped. withMemory is the caller's call
// (default account only); the memory mount is added only when the dir exists.
// created reports that this call made the clone; a new clone starts a trusted
// base record holding the host's base tip taken before the clone.
func Prepare(ctx context.Context, e Env, c config.Clone, withMemory bool) (cm box.CloneMounts, created bool, err error) {
	if err := CheckOf(c.Of, e.Home, e.Protected); err != nil {
		return box.CloneMounts{}, false, err
	}
	// The tip is read before the clone, so the record never holds a commit the
	// clone could not have had. A failure only means there is no record to start.
	_, tip, _ := BaseBranch(ctx, e.Git, c.Of)
	created, err = EnsureClone(ctx, e.Git, c, e.Home)
	if err != nil {
		return box.CloneMounts{}, false, err
	}
	if created && tip != "" && e.DataDir != "" {
		// A write error is ignored: a missing record already fails closed as unverified.
		_ = ResetBaseRecord(e.DataDir, c.Dir, tip)
	}
	gitDir := filepath.Join(c.Of, ".git")
	ro := []box.Mount{{Host: gitDir, Box: gitDir}}
	for _, rel := range c.Shares {
		isDir, err := shareSource(c.Of, rel, e.Protected)
		if err != nil {
			return box.CloneMounts{}, created, err
		}
		dst := filepath.Join(c.Dir, rel)
		if why := box.MountUnsafe(dst); why != "" {
			return box.CloneMounts{}, created, fmt.Errorf("%w: %s: %s", box.ErrUnboxableWorkspace, dst, why)
		}
		if err := mountpoint(c.Dir, rel, isDir); err != nil {
			return box.CloneMounts{}, created, err
		}
		ro = append(ro, box.Mount{Host: filepath.Join(c.Of, rel), Box: dst})
	}
	if withMemory && e.MemoryRoot != "" {
		if m, ok := MemoryMount(e.MemoryRoot, c.Of, c.Dir); ok {
			ro = append(ro, m)
		}
	}
	return box.CloneMounts{Hash: c.Hash(), RO: ro}, created, nil
}
