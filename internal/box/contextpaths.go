package box

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// A context path is a host directory or file a boxed agent can read, mounted
// read-only at the same path. These checks run on the host before that mount
// is made, at profile save and again at spawn.

// ctxPathError is a context_paths refusal: it wraps ErrUnboxableWorkspace, so a
// caller can classify it, and reads as "context_paths <p>: <why>".
type ctxPathError struct{ msg string }

func (e *ctxPathError) Error() string { return e.msg }
func (e *ctxPathError) Unwrap() error { return ErrUnboxableWorkspace }

func ctxPathRefuse(p, format string, a ...any) error {
	return &ctxPathError{"context_paths " + p + ": " + fmt.Sprintf(format, a...)}
}

// CheckContextPath refuses a context path the box could not mount safely or at
// all. p is absolute and clean (config validation has seen it). No component of
// p may be a symlink: the mount follows links on the host, so a link swapped
// after this check would expose a different path than the one the user chose.
// The leaf must exist and be a directory or a regular file. home is the user's
// home directory, protected the directories a box must not touch, and shared
// the box VM's shared directories (nil when there is no VM).
func CheckContextPath(p, home string, protected []ProtectedDir, shared []string) error {
	if !filepath.IsAbs(p) {
		return ctxPathRefuse(p, "must be an absolute path")
	}
	if filepath.Clean(p) != p {
		return ctxPathRefuse(p, "must be a clean path (no . or .. components, no repeated or trailing slash)")
	}
	cur := string(filepath.Separator)
	var info fs.FileInfo
	for _, part := range strings.Split(strings.Trim(p, string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		var err error
		if info, err = os.Lstat(cur); err != nil {
			if os.IsNotExist(err) {
				return ctxPathRefuse(p, "does not exist")
			}
			return ctxPathRefuse(p, "cannot be read (%v)", err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return ctxPathRefuse(p, "%s is a symlink; list the real path", cur)
		}
	}
	if info == nil { // p is the filesystem root
		var err error
		if info, err = os.Lstat(p); err != nil {
			return ctxPathRefuse(p, "cannot be read (%v)", err)
		}
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return ctxPathRefuse(p, "must be a directory or a regular file")
	}
	if reason := MountUnsafe(p); reason != "" {
		return ctxPathRefuse(p, "%s", reason)
	}
	if home != "" {
		if real, err := filepath.EvalSymlinks(home); err == nil {
			home = real
		}
		if reason := homeReason(p, home); reason != "" {
			return ctxPathRefuse(p, "%s", reason)
		}
		for _, name := range hostSocketDirs {
			d := filepath.Join(home, name)
			if pathWithin(p, d, runtime.GOOS == "darwin") {
				return ctxPathRefuse(p, "inside %s, which holds host sockets or credentials", d)
			}
		}
	}
	if reason := ProtectedOverlap(p, protected); reason != "" {
		return ctxPathRefuse(p, "%s", reason)
	}
	if len(shared) > 0 && !UnderMount(p, shared) {
		return ctxPathRefuse(p, "it is not under the box VM's shared directories (%s), so docker cannot see it; "+
			"share its parent and run `agent-box box setup -recreate`", strings.Join(shared, ", "))
	}
	if info.IsDir() {
		sock, err := findSocket(p)
		if err != nil {
			return err
		}
		if sock != "" {
			return ctxPathRefuse(p, "contains a unix socket (%s); a read-only mount does not stop the box from connecting to it", sock)
		}
	}
	return nil
}

// hostSocketDirs are directories under home that hold the sockets or keys of
// host services (the docker, colima and lima daemons, ssh keys and agent).
var hostSocketDirs = []string{".colima", ".lima", ".docker", ".ssh"}

// socketScanMaxEntries bounds findSocket's walk. A tree bigger than this is
// refused, not allowed unscanned: a socket anywhere in it would go unseen. A
// var so a test can lower it.
var socketScanMaxEntries = 20000

// findSocket returns the first unix socket under dir (any depth, symlinks not
// followed), or "". A read-only bind mount does not stop connect() on a socket,
// so a socket in a context path lets the box drive the host service behind it
// (a docker socket is root on the host). It fails closed: a tree over the entry
// cap or a directory it cannot read is an error, because either could hide a
// socket. err is a ready context_paths refusal for p.
func findSocket(p string) (found string, err error) {
	visited := 0
	_ = filepath.WalkDir(p, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			sub := path
			if rel, rerr := filepath.Rel(p, path); rerr == nil && rel != "." {
				sub = rel
			}
			err = ctxPathRefuse(p, "cannot read %s to scan for unix sockets", sub)
			return fs.SkipAll
		}
		if visited >= socketScanMaxEntries {
			err = ctxPathRefuse(p, "too large to scan for unix sockets (over %d entries); list narrower subdirectories", socketScanMaxEntries)
			return fs.SkipAll
		}
		visited++
		if d.Type()&fs.ModeSocket != 0 {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	return found, err
}

const (
	secretScanMaxEntries = 5000 // files and directories visited
	secretScanMaxDepth   = 2    // a file directly in dir is depth 1
	secretScanMaxResults = 20
)

// SecretFilePatterns are base-name globs that look like a secret or config
// file. internal/config (which imports this package) warns on the same set.
var SecretFilePatterns = []string{
	".env*", "*.pem", "*.key", "config*.json", "id_*", "*.tfstate", "*.tfstate.*",
}

// SecretFiles lists, relative to dir, regular files whose names look like a
// secret, so the user is told before a box can read them. The scan is bounded:
// two levels deep, 5000 entries, 20 results. It skips .git directories and
// symlinks, and an unreadable entry is skipped, not reported.
func SecretFiles(dir string) []string {
	var found []string
	visited := 0
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil || rel == "." {
			return nil
		}
		if visited >= secretScanMaxEntries {
			return fs.SkipAll
		}
		visited++
		depth := strings.Count(rel, string(filepath.Separator)) + 1
		switch {
		case d.Type()&fs.ModeSymlink != 0:
		case d.IsDir():
			if d.Name() == ".git" || depth >= secretScanMaxDepth {
				return fs.SkipDir
			}
		case d.Type().IsRegular() && looksSecret(d.Name()):
			found = append(found, rel)
			if len(found) >= secretScanMaxResults {
				return fs.SkipAll
			}
		}
		return nil
	})
	return found
}

func looksSecret(name string) bool {
	for _, pat := range SecretFilePatterns {
		if ok, _ := filepath.Match(pat, name); ok {
			return true
		}
	}
	return false
}
