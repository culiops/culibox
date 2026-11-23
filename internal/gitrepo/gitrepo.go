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

// lstat, openFile and evalSymlinks are os.Lstat, os.OpenFile and filepath.EvalSymlinks, variables so a test can see
// every path the package touches.
var (
	lstat        = os.Lstat
	openFile     = os.OpenFile
	evalSymlinks = filepath.EvalSymlinks
)

// Repo is one checkout: Top is the directory holding .git (a directory or a
// file), Common the repository's common git dir, Linked true when the checkout
// is a linked worktree. Gitdir is the directory a .git FILE names (empty for a
// .git directory): a linked worktree's own gitdir, <Common>/worktrees/<name>.
type Repo struct {
	Top, Common, Gitdir string
	Linked              bool
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
		if fi, err := lstat(g); err == nil {
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
				return Repo{Top: d, Common: filepath.Dir(filepath.Dir(gitdir)), Gitdir: gitdir, Linked: true}, true
			}
			return Repo{Top: d, Common: gitdir, Gitdir: gitdir}, true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return Repo{}, false
		}
		d = parent
	}
}

// Normalize is the one spelling of a path that workspace grouping compares:
// filepath.Clean plus EvalSymlinks, falling back to Clean alone when the path
// cannot be resolved (it does not exist, or a component is unreadable). "" stays "".
func Normalize(p string) string {
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	if r, err := evalSymlinks(p); err == nil {
		return r
	}
	return p
}

// WorkspaceRoot is the repository dir that dir belongs to, normalised, so every
// checkout of one repository reports the same string: the checkout's Top, or for
// a linked worktree of a normal repository (Common is <main>/.git) that main
// checkout. A worktree of a bare repository has no main checkout, so its own Top
// stands. "" when dir is not inside a repository. Like Locate it only reads the
// filesystem. The .git file is written by the box in an in-place session, so the
// main checkout is trusted only when the worktree's own gitdir the file names
// exists as a real directory; a forged pointer to some other repository falls
// back to the checkout's own Top.
//
// That .git file can be box-written wherever the host calls this (#364): at spawn and at load for a session's cwd
// (session.workspaceRootOf), which can lie in an in-place box's workspace, and for any typed folder in the workspace
// check API (web handleWorkspaceCheck). So the gitdir it names is never stat'ed on its say-so: the main checkout it
// implies must be this checkout's folder, an ancestor of it, or a sibling of it (git worktree add ../x), judged by path
// text alone (mainNear), and the walk to the gitdir then refuses any link (linkFreeDir). A pointer anywhere else is
// not touched, and the checkout stands as its own workspace.
func WorkspaceRoot(dir string) string {
	if dir == "" {
		return ""
	}
	// Walk the physical path: a dir reached through a symlink must not be
	// searched along the link's own parents.
	r, ok := Locate(Normalize(dir))
	if !ok {
		return ""
	}
	if r.Linked && filepath.Base(r.Common) == ".git" {
		// Judged by its text first: the main checkout the pointer implies must hold this checkout or sit beside it.
		// Only then is the gitdir walked, following no link, so the walk stays in or beside the checkout's folders.
		if main := filepath.Dir(r.Common); mainNear(main, r.Top) && linkFreeDir(r.Gitdir) {
			return main
		}
	}
	return Normalize(r.Top)
}

// mainNear reports, by path text alone, whether main (a main checkout a linked worktree's .git file implies) holds the
// checkout top or sits beside it, so that reading it only looks in the checkout's own folder or its ancestors. top is
// resolved and clean; main is clean.
func mainNear(main, top string) bool {
	if !filepath.IsAbs(main) {
		return false
	}
	sep := string(filepath.Separator)
	return main == top || strings.HasPrefix(top, strings.TrimSuffix(main, sep)+sep) ||
		filepath.Dir(main) == filepath.Dir(top)
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

// maxGitdirBytes bounds a pointer file read: a path longer than this is no checkout git wrote.
const maxGitdirBytes = 4096

// Worktrees lists the live linked worktrees of the repository whose common git dir is common, wherever they live
// (beside the repository, under .worktrees or .claude/worktrees), as their checkout dirs, sorted. It reads git's own
// registry, <common>/worktrees/<name>/gitdir, as text: never a git process. Every file there is written by whoever owns
// the repository, so each entry must hold up both ways before it counts:
//   - <common>/worktrees is a real directory (not a link), read with ReadDir(max): at most max entries are READ;
//   - <name> is a real directory, its gitdir a regular file (opened O_NOFOLLOW|O_NONBLOCK, judged on the fd) of at most
//     maxGitdirBytes, holding a path ending in /.git (a relative one is read from <common>/worktrees/<name>, as git does);
//   - the checkout dir that path names is a real directory, and no component of the path to it is a link;
//   - that checkout's own .git file points back at <common>/worktrees/<name>.
//
// Before the checkout dir that path names (or its .git) is touched at all, allow must accept both: a check by path
// alone, with no filesystem access, of whether the caller could serve them (a nil allow serves nothing). The dir is
// then reached one component at a time with Lstat, so no link on the way is followed.
//
// A pointer to any other place (/etc, another repository) fails the last check. The caller still decides whether a
// checkout is one it may show (the fs policy); this only says which ones git has.
func Worktrees(common string, max int, allow func(dir string) bool) []string {
	if max <= 0 {
		return nil
	}
	common = filepath.Clean(common)
	reg := filepath.Join(common, "worktrees")
	if fi, err := lstat(reg); err != nil || !fi.IsDir() {
		return nil
	}
	d, err := openFile(reg, os.O_RDONLY, 0)
	if err != nil {
		return nil
	}
	defer d.Close()
	// A short read or an error mid-read still leaves the entries read so far.
	ents, _ := d.ReadDir(max)
	var out []string
	for _, e := range ents {
		gd := filepath.Join(reg, e.Name())
		if fi, err := lstat(gd); err != nil || !fi.IsDir() {
			continue
		}
		dotgit, ok := readPointer(filepath.Join(gd, "gitdir"))
		if !ok {
			continue
		}
		if !filepath.IsAbs(dotgit) {
			dotgit = filepath.Join(gd, dotgit)
		}
		dotgit = filepath.Clean(dotgit)
		if filepath.Base(dotgit) != ".git" {
			continue
		}
		wt := filepath.Dir(dotgit)
		// The path is text the repository's owner wrote: the caller judges it before anything touches it, and the
		// walk to it never follows a link, so neither a pointer outside what the caller serves nor one through a link
		// out of it makes the host stat, open or mount anything there.
		if allow == nil || !allow(wt) || !linkFreeDir(wt) {
			continue
		}
		back, ok := ReadGitFile(dotgit)
		if !ok || back == "" {
			continue
		}
		if !filepath.IsAbs(back) {
			back = filepath.Join(wt, back)
		}
		if filepath.Clean(back) != gd {
			continue
		}
		out = append(out, wt)
	}
	sort.Strings(out)
	return out
}

// linkFreeDir reports whether the absolute, clean path p is a directory reached with no link anywhere on the way: each
// component is Lstat'ed in turn from "/", so the walk stops at the first link or non-directory instead of following it.
func linkFreeDir(p string) bool {
	if !filepath.IsAbs(p) {
		return false
	}
	cur := string(filepath.Separator)
	for _, c := range strings.Split(p, string(filepath.Separator)) {
		if c == "" {
			continue
		}
		cur = filepath.Join(cur, c)
		if fi, err := lstat(cur); err != nil || !fi.IsDir() {
			return false
		}
	}
	return true
}

// readPointer reads a one-line pointer file (gitdir), trimmed; ok=false for anything but a regular file of at most
// maxGitdirBytes with something in it.
func readPointer(p string) (string, bool) {
	f, ok := OpenRegular(p)
	if !ok {
		return "", false
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, maxGitdirBytes+1))
	if len(b) > maxGitdirBytes {
		return "", false
	}
	v := strings.TrimSpace(string(b))
	return v, v != ""
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
	f, err := openFile(p, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
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
