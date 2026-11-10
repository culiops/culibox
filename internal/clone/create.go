package clone

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
)

// maxGitConfig bounds the .git/config read when checking an existing clone.
const maxGitConfig = 64 << 10

// createMu serializes clone creation in this process: two sessions of one
// profile spawning together must not race each other into the rename.
var createMu sync.Mutex

// originURL is the origin URL a clone of of is created with, and the only one
// an existing clone is accepted with. Measured (A2a Task 1, V1): plain of
// works in a box, so there is no "/.git" suffix. The form lives here only.
func originURL(of string) string { return of }

// EnsureClone makes sure c.Dir is a clone of c.Of, creating it on the host when
// absent. An existing dir is only inspected, with no git exec: it may belong to
// a box by now, so nothing here reads its .git through git.
//
// Creation is atomic: git clones into a fresh temp dir beside c.Dir, and the
// result is renamed into place, so git never writes through a path a box could
// have planted a link at, and a failed clone leaves nothing behind.
//
// An existing, empty, real directory at c.Dir counts as "not created yet": on
// macOS the box VM may share the copy dir before it exists, and Lima then
// creates the missing mount source as an empty directory. The VM keeps sharing
// that directory (inode) until a restart, so a rename onto it would leave the
// VM looking at the old, empty one. The clone is therefore made the same way,
// and its top-level entries are moved INTO the existing directory, which keeps
// its inode.
func EnsureClone(ctx context.Context, g Git, c config.Clone, home string) (created bool, err error) {
	if err := checkParents(c); err != nil {
		return false, err
	}
	if exists, err := checkExisting(c); exists || err != nil {
		return false, err
	}
	createMu.Lock()
	defer createMu.Unlock()
	if err := checkParents(c); err != nil { // again, right before anything is created
		return false, err
	}
	if exists, err := checkExisting(c); exists || err != nil { // lost a race to another spawn
		return false, err
	}
	fail := func(err error) (bool, error) {
		return false, fmt.Errorf("%w: creating the clone %s from %s: %w", box.ErrBoxUnavailable, c.Dir, c.Of, err)
	}
	parent := filepath.Dir(c.Dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fail(err)
	}
	tmp, err := os.MkdirTemp(parent, ".agent-box-clone-*")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(tmp)
	repo := filepath.Join(tmp, "repo")

	if _, err := g.Run(ctx, "", CleanEnv(), "clone", "--no-hardlinks", "--template=",
		"-c", "core.hooksPath=/dev/null", "--no-recurse-submodules", "--", c.Of, repo); err != nil {
		return fail(err)
	}
	if err := copyIdentity(ctx, g, c.Of, repo, home); err != nil {
		return fail(err)
	}
	if isEmptyRealDir(c.Dir) {
		if err := fillInPlace(repo, c.Dir); err != nil {
			if errors.Is(err, errFillNotEmpty) { // something appeared meanwhile
				_, cerr := checkExisting(c)
				if cerr == nil {
					return false, nil // it is the clone we wanted
				}
				return false, cerr
			}
			return fail(err)
		}
		return true, nil
	}
	if err := os.Rename(repo, c.Dir); err != nil {
		// Something appeared at c.Dir since the check (another process): accept
		// it if it is the clone we wanted, refuse it if not.
		if exists, cerr := checkExisting(c); exists {
			return false, cerr
		}
		return fail(err)
	}
	return true, nil
}

// copyIdentity copies user.name and user.email into the new clone: from of's
// repo-local config, else the host's global config, else nothing.
func copyIdentity(ctx context.Context, g Git, of, repo, home string) error {
	type kv struct{ key, val string }
	var found []kv
	for _, key := range []string{"user.name", "user.email"} {
		val, ok, err := configGet(ctx, g, of, CleanEnv(), "--local", key)
		if err != nil {
			return err
		}
		if !ok {
			val, ok, err = configGet(ctx, g, of, globalEnv(home), "--global", key)
			if err != nil {
				return err
			}
		}
		if ok {
			found = append(found, kv{key, val})
		}
	}
	for _, f := range found {
		// "--" so a value that starts with "-" is data, not an option.
		if _, err := g.Run(ctx, repo, CleanEnv(), "config", "--", f.key, f.val); err != nil {
			return err
		}
	}
	return nil
}

// globalEnv is CleanEnv pointed at the user's real global git config.
func globalEnv(home string) []string {
	var env []string
	for _, kv := range CleanEnv() {
		if !strings.HasPrefix(kv, "GIT_CONFIG_GLOBAL=") && !strings.HasPrefix(kv, "HOME=") {
			env = append(env, kv)
		}
	}
	return append(env, "HOME="+home)
}

// configGet reads one key from the named scope; exit 1 means unset, an empty
// value counts as unset.
func configGet(ctx context.Context, g Git, dir string, env []string, scope, key string) (val string, found bool, err error) {
	out, err := g.Run(ctx, dir, env, hardened("config", scope, "--get", key)...)
	if err != nil {
		if code, ok := exitCode(err); ok && code == 1 {
			return "", false, nil
		}
		return "", false, err
	}
	val = strings.TrimRight(string(out), "\r\n")
	return val, val != "", nil
}

// HostOriginURL is the host repo's own remote.origin.url ("" when unset). It
// reads `of`, the user's repo, never a box clone: a box owns its clone's .git,
// and git run there would honour whatever the box planted. The read goes
// through hardened(), so a hook or fsmonitor in `of` cannot run either.
func HostOriginURL(ctx context.Context, g Git, of string) (string, error) {
	url, _, err := configGet(ctx, g, of, CleanEnv(), "--local", "remote.origin.url")
	return url, err
}

// checkParents refuses a c.Dir whose parent chain holds a symlink. MkdirAll,
// MkdirTemp and docker's mount all follow links, so a link planted by another
// box that can write above the clone would make the host create the clone, and
// a box later mount it read-write, wherever the link points. Every component
// that exists is Lstat'd from the root down; absent ones are the ones agent-box
// is about to create itself. A check, not a lock: defence in depth.
func checkParents(c config.Clone) error {
	cur := string(filepath.Separator)
	for _, part := range strings.Split(filepath.Dir(c.Dir), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		st, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: cannot inspect %s above the clone: %v", box.ErrUnboxableWorkspace, cur, err)
		}
		if st.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s is a symlink; agent-box creates clones only under real directories (use the resolved path in default_cwd)",
				box.ErrUnboxableWorkspace, cur)
		}
	}
	return nil
}

var errFillNotEmpty = errors.New("directory is no longer empty")

// isEmptyRealDir reports whether path is a real directory (not a symlink) with
// no entries at all.
func isEmptyRealDir(path string) bool {
	st, err := os.Lstat(path)
	if err != nil || !st.IsDir() || st.Mode()&fs.ModeSymlink != 0 {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	names, err := f.Readdirnames(1)
	return len(names) == 0 && errors.Is(err, io.EOF)
}

// fillInPlace moves src's top-level entries into the existing empty dir,
// keeping dir's inode, then removes src. Emptiness is re-checked right before
// the first move (errFillNotEmpty). If a move fails, the entries already moved
// are renamed back into src (which the caller removes), so dir is left empty;
// nothing here deletes a path under a clone dir.
func fillInPlace(src, dir string) error {
	if !isEmptyRealDir(dir) {
		return errFillNotEmpty
	}
	ents, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	var moved []string
	for _, e := range ents {
		if err := os.Rename(filepath.Join(src, e.Name()), filepath.Join(dir, e.Name())); err != nil {
			for _, m := range moved {
				_ = os.Rename(filepath.Join(dir, m), filepath.Join(src, m))
			}
			return err
		}
		moved = append(moved, e.Name())
	}
	return os.Remove(src)
}

// checkExisting inspects c.Dir without git. exists is false when c.Dir is
// absent or is an empty real directory (not created yet, see EnsureClone); any
// other existing dir that is not the wanted clone is an ErrUnboxableWorkspace.
func checkExisting(c config.Clone) (exists bool, err error) {
	st, lerr := os.Lstat(c.Dir)
	if errors.Is(lerr, fs.ErrNotExist) || isEmptyRealDir(c.Dir) {
		return false, nil
	}
	refuse := func(why string) (bool, error) {
		return true, fmt.Errorf("%w: %s exists but is not a clone of %s (%s); move it away and agent-box will create the clone",
			box.ErrUnboxableWorkspace, c.Dir, c.Of, why)
	}
	if lerr != nil {
		return refuse(lerr.Error())
	}
	if st.Mode()&fs.ModeSymlink != 0 {
		return refuse("it is a symlink")
	}
	if !st.IsDir() {
		return refuse("it is not a directory")
	}
	root, err := os.OpenRoot(c.Dir)
	if err != nil {
		return refuse(err.Error())
	}
	defer root.Close()
	gst, err := root.Lstat(".git")
	if err != nil {
		return refuse("no .git directory")
	}
	if !gst.IsDir() { // a symlink or a gitdir: file
		return refuse(".git is not a plain directory")
	}
	data, err := readBoxFile(root, ".git/config", maxGitConfig)
	if err != nil {
		return refuse(".git/config is not a regular file of at most 64 KiB")
	}
	urls, ok := box.GitConfigValues(data, "remote.origin", "url")
	if !ok {
		return refuse("its .git/config does not parse")
	}
	if len(urls) != 1 {
		return refuse(fmt.Sprintf("origin has %d urls, want exactly one", len(urls)))
	}
	if urls[0] != originURL(c.Of) {
		return refuse(fmt.Sprintf("its origin is %q", urls[0])) // %q: .git/config is box-written text
	}
	return true, nil
}
