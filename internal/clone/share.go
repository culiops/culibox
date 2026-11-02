package clone

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
)

// boxProjects is where the box's claude keeps per-project state.
const boxProjects = "/home/agent/.claude/projects"

// errShareMissing marks a share source (or a component of it) that does not
// exist: a refusal at spawn, fine at save.
var errShareMissing = errors.New("share source does not exist")

// shareSource checks one clone_share entry's source under of and reports
// whether it is a directory (else a regular file). Every component of rel is
// Lstat'd through an os.Root on of: none may be a symlink, because docker
// resolves the source as root at mount time, so a link (tracked in the repo, or
// placed by a host tool) would make the mounted path differ from the checked
// one, and MountUnsafe/ProtectedOverlap on the literal path would check the
// wrong thing (spec correction 8).
func shareSource(of, rel string, protected []box.ProtectedDir) (isDir bool, err error) {
	if _, err := config.ParseShare(rel); err != nil {
		return false, fmt.Errorf("%w: %v", box.ErrUnboxableWorkspace, err)
	}
	src := filepath.Join(of, rel)
	refuse := func(why string) (bool, error) {
		return false, fmt.Errorf("%w: clone_share %s: %s", box.ErrUnboxableWorkspace, src, why)
	}
	root, err := os.OpenRoot(of)
	if err != nil {
		return refuse(err.Error())
	}
	defer root.Close()
	var st fs.FileInfo
	parts := strings.Split(rel, "/")
	for i := range parts {
		cur := strings.Join(parts[:i+1], "/")
		st, err = root.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("%w: clone_share %s: %w", box.ErrUnboxableWorkspace, src, errShareMissing)
		}
		if err != nil {
			return refuse(err.Error())
		}
		if st.Mode()&fs.ModeSymlink != 0 {
			return refuse(filepath.Join(of, cur) + " is a symlink; share only real paths")
		}
	}
	if !st.IsDir() && !st.Mode().IsRegular() {
		return refuse("must be a directory or a regular file")
	}
	if why := box.MountUnsafe(src); why != "" {
		return refuse(why)
	}
	if why := box.ProtectedOverlap(src, protected); why != "" {
		return refuse(why)
	}
	return st.IsDir(), nil
}

// mountpoint makes <dir>/<rel> exist as the type of the share's source, so
// docker (root) never creates it. dir is the clone, which the box can write: it
// may have planted a link anywhere on this path, and the host following it
// would create a dir or file outside the clone and point the in-box mount at a
// path the box chose (its home, its .git). So every step goes through an
// os.Root on dir and is Lstat'd first; a symlink refuses the spawn (spec
// correction 8).
func mountpoint(dir, rel string, isDir bool) error {
	planted := func(cur string) error {
		return fmt.Errorf("%w: %s is a symlink (the box may have planted it); remove it and spawn again",
			box.ErrUnboxableWorkspace, filepath.Join(dir, cur))
	}
	fail := func(cur string, err error) error {
		return fmt.Errorf("%w: creating the mountpoint %s: %v", box.ErrUnboxableWorkspace, filepath.Join(dir, cur), err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fail(".", err)
	}
	defer root.Close()
	parts := strings.Split(rel, "/")
	for i := range parts[:len(parts)-1] {
		cur := strings.Join(parts[:i+1], "/")
		st, err := root.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := root.Mkdir(cur, 0o755); err != nil {
				return fail(cur, err)
			}
		case err != nil:
			return fail(cur, err)
		case st.Mode()&fs.ModeSymlink != 0:
			return planted(cur)
		case !st.IsDir():
			return fmt.Errorf("%w: %s is not a directory; the share %s needs one there",
				box.ErrUnboxableWorkspace, filepath.Join(dir, cur), rel)
		}
	}
	st, err := root.Lstat(rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if isDir {
			if err := root.Mkdir(rel, 0o755); err != nil {
				return fail(rel, err)
			}
			return nil
		}
		f, err := root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return fail(rel, err)
		}
		if err := f.Close(); err != nil {
			return fail(rel, err)
		}
		return nil
	case err != nil:
		return fail(rel, err)
	case st.Mode()&fs.ModeSymlink != 0:
		return planted(rel)
	case isDir && !st.IsDir():
		return fmt.Errorf("%w: %s is not a directory, but its share source is; remove it and spawn again",
			box.ErrUnboxableWorkspace, filepath.Join(dir, rel))
	case !isDir && !st.Mode().IsRegular():
		return fmt.Errorf("%w: %s is not a regular file, but its share source is; remove it and spawn again",
			box.ErrUnboxableWorkspace, filepath.Join(dir, rel))
	}
	return nil
}

// MemoryMount is the host project memory of of, read-only at the box claude's
// memory dir for dir (spec §6.3, correction 5): memRoot/projects/<slug(of)>/memory
// when memRoot is absolute and it is a real directory (Lstat). Optional: a convenience the session runs
// without, never a reason to refuse a spawn.
func MemoryMount(memRoot, of, dir string) (box.Mount, bool) {
	if !filepath.IsAbs(memRoot) { // "" or relative would be read against the daemon's cwd
		return box.Mount{}, false
	}
	host := filepath.Join(memRoot, "projects", claudeProjectSlug(of), "memory")
	if st, err := os.Lstat(host); err != nil || !st.IsDir() {
		return box.Mount{}, false
	}
	if box.MountUnsafe(host) != "" {
		return box.Mount{}, false
	}
	return box.Mount{Host: host, Box: boxProjects + "/" + claudeProjectSlug(dir) + "/memory", Optional: true}, true
}

var slugUnsafe = regexp.MustCompile(`[^A-Za-z0-9]`)

// claudeProjectSlug is Claude Code's project-dir name for p, measured (A2a
// Task 1, V3): every character outside [A-Za-z0-9] becomes "-".
func claudeProjectSlug(p string) string {
	return slugUnsafe.ReplaceAllString(p, "-")
}
