package abduco

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Source says where the abduco binary in use came from.
type Source string

const (
	SourceConfig  Source = "abduco_bin"
	SourceBundled Source = "next to culibox"
	SourcePath    Source = "PATH"
)

// Resolved is the abduco binary serve, doctor and init use.
type Resolved struct {
	Path    string // what gets exec'd
	Source  Source
	Skipped string // why an abduco next to culibox was refused; "" if none was there or it was used
}

// Lookup finds abduco. Its fields are seams for tests; DefaultLookup wires the
// real process.
type Lookup struct {
	Executable func() (string, error)       // os.Executable
	LookPath   func(string) (string, error) // exec.LookPath
	Uid        int                          // os.Getuid()
	// PrivateGid is the user's private group (a group named like the user, as
	// umask 002 systems set up): a folder or file writable by that group alone is
	// still writable by the user alone. 0 or -1: none.
	PrivateGid int
}

// DefaultLookup looks for this process's abduco.
func DefaultLookup() Lookup {
	return Lookup{Executable: os.Executable, LookPath: exec.LookPath, Uid: os.Getuid(), PrivateGid: privateGid()}
}

// privateGid is this process's primary group when it is the user's private group
// (same name as the user), else -1: the strict rule. macOS users have the shared
// group staff, so they get -1; so does a user os/user cannot look up.
func privateGid() int {
	u, err := user.Current()
	if err != nil {
		return -1
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil || g.Name != u.Username {
		return -1
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return -1
	}
	return gid
}

// Resolve picks the abduco binary: an explicit abduco_bin always wins; otherwise
// the abduco in the directory of the resolved culibox executable (a release
// tarball ships one there), then abduco on PATH.
func (l Lookup) Resolve(configured string) (Resolved, error) {
	if configured != "" {
		p, err := l.LookPath(configured)
		if err != nil {
			return Resolved{}, fmt.Errorf("abduco_bin %q not found: %w", configured, err)
		}
		return Resolved{Path: p, Source: SourceConfig}, nil
	}
	var r Resolved
	where := "next to culibox"
	if exe, err := l.Executable(); err == nil {
		if target, err := filepath.EvalSymlinks(exe); err == nil {
			dir := filepath.Dir(target)
			where = fmt.Sprintf("next to culibox (%s)", dir)
			cand := filepath.Join(dir, "abduco")
			why, ok := bundledOK(cand, l.Uid, l.PrivateGid)
			if ok {
				return Resolved{Path: cand, Source: SourceBundled}, nil
			}
			if why != "" {
				r.Skipped = cand + ": " + why
			}
		}
	}
	p, err := l.LookPath("abduco")
	if err != nil {
		if r.Skipped != "" {
			return r, fmt.Errorf("abduco not found on PATH, and ignored %s", r.Skipped)
		}
		return r, fmt.Errorf("abduco not found %s or on PATH", where)
	}
	r.Path, r.Source = p, SourcePath
	return r, nil
}

// bundledOK says whether the abduco next to culibox may run. A culibox run from a
// sticky shared directory such as /tmp would otherwise run whatever abduco another
// local user put there: a file, a hard link to a root binary, or a symlink they can
// retarget later. So the folder must be writable by us or root alone, and so must
// the folder a symlink points into; a symlink is followed one hop only (a link to a
// link could sit in anyone's folder); the file itself must be ours (or root's) and
// writable by no one else. pgid is the user's private group (see Lookup). why is
// "" when there is no such file at all.
func bundledOK(path string, uid, pgid int) (why string, ok bool) {
	li, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		return err.Error(), false
	}
	if why := safeDir(filepath.Dir(path), uid, pgid); why != "" {
		return why, false
	}
	target := path
	if li.Mode()&os.ModeSymlink != 0 {
		dest, err := os.Readlink(path)
		if err != nil {
			return err.Error(), false
		}
		if dotDotAfterName(dest) {
			return "a symlink whose target has `..` after a folder name (" + dest + ")", false
		}
		if !filepath.IsAbs(dest) {
			dest = filepath.Join(filepath.Dir(path), dest)
		}
		if target, err = filepath.EvalSymlinks(path); err != nil {
			return err.Error(), false
		}
		if filepath.Clean(dest) != target {
			return "a symlink chain (" + dest + " is itself a link or under one)", false
		}
		if why := safeDir(filepath.Dir(target), uid, pgid); why != "" {
			return "its link target's " + why, false
		}
	}
	fi, err := os.Stat(target)
	if err != nil {
		return err.Error(), false
	}
	if !fi.Mode().IsRegular() {
		return "not a regular file", false
	}
	if fi.Mode().Perm()&0o111 == 0 {
		return "not executable", false
	}
	if why := writableByOthers(fi, pgid); why != "" {
		return why, false
	}
	if why := ownedBy(fi, uid); why != "" {
		return why, false
	}
	return "", true
}

// dotDotAfterName says whether p has a `..` after some other name. Cleaning such a
// path would hide whether that name is a symlink; leading `..`s are fine, since they
// climb from the link's own folder, which is already resolved.
func dotDotAfterName(p string) bool {
	named := false
	for _, part := range strings.Split(p, string(filepath.Separator)) {
		switch part {
		case "", ".":
		case "..":
			if named {
				return true
			}
		default:
			named = true
		}
	}
	return false
}

// safeDir is "" when only uid or root can add, remove or replace files in dir.
func safeDir(dir string, uid, pgid int) string {
	fi, err := os.Stat(dir)
	if err != nil {
		return "folder: " + err.Error()
	}
	if why := writableByOthers(fi, pgid); why != "" {
		return "folder " + dir + " is " + why
	}
	if why := ownedBy(fi, uid); why != "" {
		return "folder " + dir + " is " + why
	}
	return ""
}

// writableByOthers is "" when no one but the owner can write fi: no other-write
// bit, and no group-write bit unless the group is the user's private group pgid.
func writableByOthers(fi os.FileInfo, pgid int) string {
	perm := fi.Mode().Perm()
	if perm&0o002 == 0 && perm&0o020 == 0 {
		return ""
	}
	if perm&0o002 == 0 && pgid > 0 {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Gid) == pgid {
			return ""
		}
	}
	return fmt.Sprintf("writable by group or others (mode %o)", perm)
}

// ownedBy is "" when fi belongs to uid or root.
func ownedBy(fi os.FileInfo, uid int) string {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "of unknown owner"
	}
	if int(st.Uid) != uid && st.Uid != 0 {
		return fmt.Sprintf("owned by uid %d, not by you or root", st.Uid)
	}
	return ""
}
