package abduco

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
}

// DefaultLookup looks for this process's abduco.
func DefaultLookup() Lookup {
	return Lookup{Executable: os.Executable, LookPath: exec.LookPath, Uid: os.Getuid()}
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
			why, ok := bundledOK(cand, l.Uid)
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
		return r, fmt.Errorf("abduco not found %s or on PATH", where)
	}
	r.Path, r.Source = p, SourcePath
	return r, nil
}

// bundledOK says whether the abduco next to culibox may run. A culibox run from a
// sticky shared directory such as /tmp would otherwise run whatever abduco another
// local user put there: a file, a hard link to a root binary, or a symlink they can
// retarget later. So the folder must be writable by us or root alone, and so must
// the folder a symlink points into; the file itself must be ours (or root's) and
// writable by no one else. why is "" when there is no such file at all.
func bundledOK(path string, uid int) (why string, ok bool) {
	li, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		return err.Error(), false
	}
	if why := safeDir(filepath.Dir(path), uid); why != "" {
		return why, false
	}
	target := path
	if li.Mode()&os.ModeSymlink != 0 {
		if target, err = filepath.EvalSymlinks(path); err != nil {
			return err.Error(), false
		}
		if why := safeDir(filepath.Dir(target), uid); why != "" {
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
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Sprintf("writable by group or others (mode %o)", fi.Mode().Perm()), false
	}
	if why := ownedBy(fi, uid); why != "" {
		return why, false
	}
	return "", true
}

// safeDir is "" when only uid or root can add, remove or replace files in dir.
func safeDir(dir string, uid int) string {
	fi, err := os.Stat(dir)
	if err != nil {
		return "folder: " + err.Error()
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Sprintf("folder %s is writable by group or others (mode %o)", dir, fi.Mode().Perm())
	}
	if why := ownedBy(fi, uid); why != "" {
		return "folder " + dir + " is " + why
	}
	return ""
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
