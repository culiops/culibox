// Package gitrepo finds a git checkout and its linked worktrees by reading the
// filesystem only. It never runs git: the agent owns these files under a box
// mount, so every read is bounded and refuses symlinks and FIFOs.
package gitrepo

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// Repo is one checkout: Top is the directory holding .git (a directory or a
// file), Common the repository's common git dir, Linked true when the checkout
// is a linked worktree.
type Repo struct {
	Top, Common string
	Linked      bool
}

// Locate walks up from dir to the nearest .git. A .git directory is a main
// checkout. A .git file names a gitdir: when that gitdir's PARENT is named
// worktrees the checkout is a linked worktree of the repository two levels up
// (a submodule of a worktree — …/worktrees/wt/modules/sub — merely contains the
// word); otherwise the gitdir is itself the common dir (a submodule). ok=false:
// no .git found, or one that is neither a directory nor a readable regular file.
func Locate(dir string) (Repo, bool) {
	for d := dir; ; {
		g := filepath.Join(d, ".git")
		if fi, err := os.Lstat(g); err == nil {
			if fi.IsDir() {
				return Repo{Top: d, Common: g}, true
			}
			gitdir, ok := ReadGitFile(g)
			if !ok || gitdir == "" {
				return Repo{}, false
			}
			if !filepath.IsAbs(gitdir) {
				gitdir = filepath.Join(d, gitdir)
			}
			gitdir = filepath.Clean(gitdir)
			if filepath.Base(filepath.Dir(gitdir)) == "worktrees" {
				return Repo{Top: d, Common: filepath.Dir(filepath.Dir(gitdir)), Linked: true}, true
			}
			return Repo{Top: d, Common: gitdir}, true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return Repo{}, false
		}
		d = parent
	}
}

// CheckoutOf reports the repository's common git dir for the checkout holding
// dir, plus the checkout root when that checkout is a linked worktree (empty for
// a main checkout or a submodule).
func CheckoutOf(dir string) (common, wtRoot string, ok bool) {
	r, ok := Locate(dir)
	if !ok {
		return "", "", false
	}
	if r.Linked {
		return r.Common, r.Top, true
	}
	return r.Common, "", true
}

// LinkedWorktrees lists the live linked worktrees of the repository whose common
// git dir is common, as absolute dirs root/.claude/worktrees/<name>, sorted by
// name. At most max directory entries are READ (the directory is opened and read
// with ReadDir(max), in directory order, so a directory of millions of entries
// costs max of them, not all); the entries read are then sorted. An entry counts only if it is
// a real directory (Lstat: a symlink never is), its .git is a regular file naming
// a gitdir directly under common/worktrees, and that gitdir exists.
func LinkedWorktrees(root, common string, max int) []string {
	if max <= 0 {
		return nil
	}
	base := filepath.Join(root, ".claude", "worktrees")
	d, err := os.Open(base)
	if err != nil {
		return nil
	}
	defer d.Close()
	// A short read or an error mid-read still leaves the entries read so far.
	ents, _ := d.ReadDir(max)
	wtParent := filepath.Join(common, "worktrees")
	var out []string
	for _, e := range ents {
		dir := filepath.Join(base, e.Name())
		if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
			continue
		}
		gitdir, ok := ReadGitFile(filepath.Join(dir, ".git"))
		if !ok || gitdir == "" {
			continue
		}
		if !filepath.IsAbs(gitdir) {
			gitdir = filepath.Join(dir, gitdir)
		}
		gitdir = filepath.Clean(gitdir)
		if filepath.Dir(gitdir) != wtParent {
			continue
		}
		if fi, err := os.Stat(gitdir); err != nil || !fi.IsDir() {
			continue
		}
		out = append(out, dir)
	}
	sort.Strings(out)
	return out
}

// ReadGitFile returns the gitdir a .git FILE points at; ok=false for anything
// that is not a regular file of the form "gitdir: <path>".
func ReadGitFile(p string) (string, bool) {
	f, ok := OpenRegular(p)
	if !ok {
		return "", false
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 4096))
	gitdir, found := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
	return strings.TrimSpace(gitdir), found
}

// OpenRegular opens p only if it is a regular file, deciding on the opened fd
// rather than a prior Lstat: under a box mount the agent owns these files and can
// swap one for a FIFO between the check and the open, and a plain open of a FIFO
// blocks with no timeout (one pinned thread per poll). O_NONBLOCK makes that open
// return at once; O_NOFOLLOW refuses a symlink.
func OpenRegular(p string) (*os.File, bool) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, false
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, false
	}
	return f, true
}
