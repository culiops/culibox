package clone

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
)

// Env is what Prepare needs from the daemon.
type Env struct {
	Git        Git                // the host git, used only to create the clone
	Home       string             // the host user's home
	Protected  []box.ProtectedDir // dirs no mount may be, contain or (Inside) sit in
	MemoryRoot string             // the bundle source dir (~/.claude); "" = no memory mount
	DataDir    string             // the daemon's data dir, for the base record; "" = no base record
	// Visible reports whether the box VM can read a host path (macOS: the path
	// is under a folder the VM shares). nil = every path is visible. An invisible
	// memory folder is skipped, never a reason to refuse a spawn.
	Visible func(host string) bool
	// Estimate sizes the copy before the first clone is made and refuses one the
	// disk cannot hold (EstimateCopy with the host git). nil = no check.
	Estimate EstimateFunc
}

// memorySkipLogged remembers the memory folders already reported as unshared,
// so a spawn per session does not repeat the line.
var memorySkipLogged sync.Map

// CheckOf refuses a clone_of agent-box must not clone from or mount the .git of
// (spec correction 8): it must be a real directory (not a symlink), not /, the
// home or an ancestor of it, and hold a real .git directory that borrows no
// objects or refs from elsewhere (no commondir, no alternates) and whose
// .git/config git can read (configWhy), whose path is safe to mount and
// overlaps no protected dir. A partial clone passes: it is refused only where a
// box may have written its config (PartialCloneWhy, the daemon's checkOf).
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
	if why := configWhy(root); why != "" {
		return refuse(why)
	}
	gitDir := filepath.Join(of, ".git")
	if why := box.MountUnsafe(gitDir); why != "" {
		return refuse("cannot be mounted: " + why)
	}
	if why := box.ProtectedOverlap(gitDir, box.ReadOnlyUse(protected)); why != "" {
		return refuse("cannot be mounted: its .git: " + why)
	}
	return nil
}

// maxOfConfig bounds the read of clone_of's .git/config: a user repo's config
// grows with its branches and remotes, so the bound is well above a clone's.
const maxOfConfig = 1 << 20

// configWhy says why clone_of's .git/config cannot be used, "" when it can: it
// must be a plain file (read with the box-file discipline: no symlink, FIFO or
// directory, bounded) that git would accept, since nothing after a line git
// rejects can be read for what PartialCloneWhy looks for. An absent config is
// git's defaults.
func configWhy(root *os.Root) string {
	_, why := ofConfig(root, ".git/config")
	return why
}

// ofConfig reads one config file of clone_of under root: its bytes, or why it
// cannot be read as git would ("" and nil data when it is absent).
func ofConfig(root *os.Root, name string) ([]byte, string) {
	data, err := readBoxFile(root, name, maxOfConfig)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, ""
	case err != nil:
		return nil, "has a " + name + " that cannot be read as a plain file (" + err.Error() + "); it must be a regular file of at most 1 MiB"
	}
	if _, _, valid := box.GitConfigPromisor(data); !valid {
		return nil, "has a " + name + " that git would not read; fix it with git config"
	}
	return data, ""
}

// PartialCloneWhy says why the repo of is, or may be, a partial clone, "" when
// it is not: its .git/config (and .git/config.worktree when it turns
// extensions.worktreeConfig on) declares a partial clone or a promisor remote
// (box.GitConfigPromisor), or cannot be read as git would. git then fetches any
// object the repo lacks from that remote, running the commands the config names.
// Every host git call in clone_of that needs no transport forbids them all; the
// two that read clone_of through one, the first copy's git clone and the bundle
// import, rely on GIT_NO_LAZY_FETCH, which an older git ignores. A partial clone
// of the user's own is a working clone_of, so this is not a CheckOf rule: the
// daemon refuses it only for a folder whose .git a box may have written (#371).
// A declaration in a file an [include] pulls in is not seen (a stated limit). A
// repo that does not exist is "": the checks that need it report that.
func PartialCloneWhy(of string) string {
	root, err := os.OpenRoot(of)
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil {
		return "cannot be opened (" + err.Error() + ")"
	}
	defer root.Close()
	files := []string{".git/config"}
	for i := 0; i < len(files); i++ {
		data, why := ofConfig(root, files[i])
		if why != "" {
			return why
		}
		declares, worktree, _ := box.GitConfigPromisor(data)
		if declares {
			return "is a partial clone: its " + files[i] + " declares a promisor remote (extensions.partialClone, or a remote's promisor or partialclonefilter)"
		}
		if worktree && i == 0 {
			files = append(files, ".git/config.worktree")
		}
	}
	return ""
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
	created, err = ensureClone(ctx, e.Git, c, e.Home, e.Estimate)
	if err != nil {
		return box.CloneMounts{}, false, err
	}
	if created && e.DataDir != "" {
		// A write error is ignored: a missing record already fails closed as
		// unverified. With no tip, an older record for this dir must not vouch
		// for the new clone, so it is removed.
		if tip != "" {
			_ = ResetBaseRecord(e.DataDir, c.Dir, tip)
		} else {
			_ = removeBaseRecord(e.DataDir, c.Dir)
		}
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
			if e.Visible == nil || e.Visible(m.Host) {
				ro = append(ro, m)
			} else if _, seen := memorySkipLogged.LoadOrStore(m.Host, true); !seen {
				log.Printf("clone: the clone memory folder %s is not shared with the box VM; boxes start without it (add it in Settings › VM folders)", m.Host)
			}
		}
	}
	return box.CloneMounts{Hash: c.Hash(), RO: ro}, created, nil
}
