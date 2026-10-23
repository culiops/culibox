package box

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
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

// ErrContextMountChanged marks a box refused because a context mount, seen from
// inside the box, is not the path that was checked on the host.
var ErrContextMountChanged = errors.New("context mount is not the checked path")

// ctxMountError is the ErrContextMountChanged refusal: a ctxPathError (so it is
// ErrUnboxableWorkspace too) that also reports ErrContextMountChanged.
type ctxMountError struct{ ctxPathError }

func (e *ctxMountError) Is(target error) bool { return target == ErrContextMountChanged }

func ctxMountChanged(p, extra string) error {
	return &ctxMountError{ctxPathError{"context_paths " + p +
		": the mounted path is not the checked one (it changed while the box started); refusing" + extra}}
}

// FileID is a file's identity on its filesystem. A bind mount shares its
// source's, so the identity of a mount's destination inside the box says
// whether docker mounted the path that was checked.
type FileID struct{ Dev, Ino uint64 }

// ContextMountID is p's identity for Mount.ID, read with Lstat: a symlink is
// its own file, so a path swapped for a link after the check never matches the
// link's target that docker would mount. Nil off Linux, where the in-box check
// is not made: inode numbers seen through the box VM's file share do not match
// the host's.
func ContextMountID(p string) (*FileID, error) {
	if runtime.GOOS != "linux" {
		return nil, nil
	}
	info, err := os.Lstat(p)
	if err != nil {
		return nil, ctxPathRefuse(p, "cannot be read (%v)", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, ctxPathRefuse(p, "its file identity cannot be read")
	}
	return &FileID{Dev: uint64(st.Dev), Ino: uint64(st.Ino)}, nil
}

// checkMountIDs compares, inside the running box name, the identity of every
// extra mount that carries an ID with the one recorded on the host. Docker
// follows symlinks in a bind source, so a path flipped to a link between the
// last host check and `docker run` would mount the link's target; this catches
// it before the agent starts. One `docker exec` for all of them, paths as argv
// (no shell). Mounts without an ID cost nothing. Caller holds opMu.
func (m *Manager) checkMountIDs(ctx context.Context, name string, extraRO []Mount) error {
	var ms []Mount
	for _, mt := range extraRO {
		if mt.ID != nil {
			ms = append(ms, mt)
		}
	}
	if len(ms) == 0 {
		return nil
	}
	args := []string{"exec", name, "stat", "-c", "%d:%i", "--"}
	for _, mt := range ms {
		args = append(args, mt.Box)
	}
	out, err := m.opts.Client.Run(ctx, args)
	extra := ""
	if err != nil {
		extra = fmt.Sprintf(" (in-box check failed: %v)", err)
	}
	lines := strings.Split(out, "\n")
	for i, mt := range ms {
		if err != nil || i >= len(lines) || strings.TrimSpace(lines[i]) != fmt.Sprintf("%d:%d", mt.ID.Dev, mt.ID.Ino) {
			return ctxMountChanged(mt.Host, extra)
		}
	}
	return nil
}

// refuseMountChanged handles a box that failed checkMountIDs. A box this spawn
// created, or one no session of this process uses, is removed: nothing runs in
// it, and it must not be reused with the wrong mount. A box other sessions run
// in is kept (removing it would kill them) and the refusal says how to get it
// recreated; every later spawn into it is checked, and refused, again.
// Caller holds opMu.
func (m *Manager) refuseMountChanged(ctx context.Context, name string, created bool, cerr error) error {
	m.mu.Lock()
	live := m.active[name]
	m.mu.Unlock()
	if !created && live > 0 {
		return fmt.Errorf("%w (box %s still runs %d session(s) and is kept; end them so the box is recreated, then spawn again)",
			cerr, name, live)
	}
	m.unverify(name)
	if err := m.opts.Client.Stop(ctx, name); err != nil {
		cerr = fmt.Errorf("%w (box %s could not be removed: %v; every spawn into it is checked again)", cerr, name, err)
	}
	m.rearmIfIdle(name)
	return cerr
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
		ipc, err := findSocketOrPipe(p)
		if err != nil {
			return err
		}
		if ipc != "" {
			return ctxPathRefuse(p, "contains a unix socket or named pipe (%s); a read-only mount does not stop the box "+
				"from connecting to it or writing into it", ipc)
		}
	}
	return nil
}

// hostSocketDirs are directories under home that hold the sockets or keys of
// host services (the docker, colima and lima daemons, ssh keys and agent).
var hostSocketDirs = []string{".colima", ".lima", ".docker", ".ssh"}

// socketScanMaxEntries bounds findSocketOrPipe's walk. A tree bigger than this
// is refused, not allowed unscanned: a socket anywhere in it would go unseen. A
// var so a test can lower it.
var socketScanMaxEntries = 20000

// findSocketOrPipe returns the first unix socket or named pipe under dir (any
// depth, symlinks not followed), or "". The kernel's read-only check covers
// regular files, directories and symlinks only: a read-only bind mount stops
// neither connect() on a socket nor a write into a FIFO. A socket in a context
// path lets the box drive the host service behind it (a docker socket is root
// on the host), a FIFO lets it feed whatever host process reads it. It fails
// closed: a tree over the entry cap or a directory it cannot read is an error,
// because either could hide one. err is a ready context_paths refusal for p.
func findSocketOrPipe(p string) (found string, err error) {
	visited := 0
	_ = filepath.WalkDir(p, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			sub := path
			if rel, rerr := filepath.Rel(p, path); rerr == nil && rel != "." {
				sub = rel
			}
			err = ctxPathRefuse(p, "cannot read %s to scan for unix sockets and named pipes", sub)
			return fs.SkipAll
		}
		if visited >= socketScanMaxEntries {
			err = ctxPathRefuse(p, "too large to scan for unix sockets and named pipes (over %d entries); list narrower subdirectories", socketScanMaxEntries)
			return fs.SkipAll
		}
		visited++
		if d.Type()&(fs.ModeSocket|fs.ModeNamedPipe) != 0 {
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
	"credentials*", "*.tfvars", ".netrc", ".npmrc", ".git-credentials", "auth.json",
	"*.p12", "*.pfx", "kubeconfig",
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
