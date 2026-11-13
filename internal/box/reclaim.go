package box

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// DefaultStateTTL is how long an unused box home survives. A workspace untouched
// for a month has no resume worth keeping, but the login and the resume history in
// its .claude are cheap and worth having until then. The regenerable caches in it
// are not cheap, and go sooner: see DefaultCacheTTL.
const DefaultStateTTL = 30 * 24 * time.Hour

// DefaultCacheTTL is how long a dead box keeps its regenerable caches. A Go
// workspace leaves gigabytes behind (a build cache of 3.6 to 5.5 GiB per home was
// measured, 18 GiB of it in 20 hours on one host), so thirty days of them filled
// a disk. An hour keeps the cache warm for someone who comes straight back, and
// anything later pays one rebuild.
const DefaultCacheTTL = time.Hour

// cachePaths are the only paths pruneOne removes from a home that is past
// DefaultCacheTTL but not yet past DefaultStateTTL: caches a tool rebuilds by
// itself. The list is spelled out, never derived (no "everything under .cache"):
// what a home holds besides these is a resume, a login or a user's file.
//
//   - .cache/go-build and go/pkg/mod: GOCACHE and GOMODCACHE as the box image
//     sets them,
//   - .npm: npm's cache,
//   - .claude/security/agent-sdk-venv: the Python venv the security-guidance
//     plugin builds at session start when it is missing.
var cachePaths = []string{
	".cache/go-build",
	"go/pkg/mod",
	".npm",
	".claude/security/agent-sdk-venv",
}

// Reclaimed describes one box home the sweep removed, or whose caches it pruned.
// The package returns this rather than logging it: every decision here is
// unit-tested without a daemon, which is only true while it imports no logger.
type Reclaimed struct {
	Box string
	Age time.Duration
	// Bytes is what the sweep freed: the whole home, or with Caches the files
	// under the allowlisted paths as measured before the removal, counted down to
	// maxCacheDepth only (an upper bound when a path would not go and the error
	// says so).
	Bytes int64
	// Caches is true when only the allowlisted caches were removed and the home
	// itself stays.
	Caches bool
}

// ReclaimState removes the host homes of boxes that no longer exist and have not
// been used for ttl, and removes only the regenerable caches (cachePaths) of
// those unused for cacheTTL but not yet ttl. A cacheTTL that is not shorter than
// ttl turns the pruning off.
//
// A home is touched only when all four hold, checked in this order because the
// cheap and certain tests come first:
//
//  1. its name is one this package generates,
//  2. no container of this instance still carries that name,
//  3. no live session is counted against it,
//  4. it is older than ttl (removed whole) or cacheTTL (caches only).
//
// Grounds 2 and 3 are checked twice: once cheaply against a snapshot to decide
// whether a candidate is worth walking and stat-ing at all, and again — for
// real, under opMu — immediately before removal. Between the snapshot and the
// removal a spawn can start: `ensure` (box.go) takes opMu to create the
// container and bind-mount this exact directory as its $HOME, and a
// RemoveAll racing that mount would delete files out from under a live
// container. Holding opMu across the whole sweep would block every spawn for
// as long as the slowest directory walk takes, so the lock is scoped to one
// candidate's decide-and-remove instead. dirBytes and cacheBytes run BEFORE
// opMu is taken — they are only a report number, and a slightly stale byte count
// is an acceptable cost to keep the walk off the lock.
//
// A removal that fails is recorded and the sweep continues: one stubborn
// directory — a root-owned leftover from before mountpoints were pre-created —
// must not stop the rest. The failures come back joined so the caller can name
// them. Escalating to remove them is deliberately not done: running a root
// container over host state is the hole this package exists to close.
func (m *Manager) ReclaimState(ctx context.Context, ttl, cacheTTL time.Duration) ([]Reclaimed, error) {
	root := filepath.Join(m.opts.StateRoot, stateDirName)
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // nothing has ever been boxed
	}
	if err != nil {
		return nil, fmt.Errorf("box: reading %q: %w", root, err)
	}

	alive, err := m.opts.Client.ListByLabel(ctx, LabelInstance+"="+m.opts.Instance)
	if err != nil {
		return nil, fmt.Errorf("box: listing boxes: %w", err)
	}
	known := make(map[string]bool, len(alive))
	for _, n := range alive {
		known[n] = true
	}

	var out []Reclaimed
	var errs []error
	for _, e := range entries {
		// The context bounds the file work too, not only the docker calls: a pass
		// that is out of time stops here instead of walking the next home.
		if ctx.Err() != nil {
			break
		}
		name := e.Name()
		if !e.IsDir() || !isBoxName(name) || known[name] {
			continue
		}
		m.mu.Lock()
		live := m.active[name]
		m.mu.Unlock()
		if live > 0 {
			continue
		}
		// min: a cacheTTL that is not shorter than ttl must not postpone the
		// removal of a home that is past ttl.
		age, ok := m.homeAge(name)
		if !ok || age < min(ttl, cacheTTL) {
			continue
		}
		dir := m.stateDir(name)
		var reclaimed *Reclaimed
		var err error
		if age >= ttl {
			reclaimed, err = m.reclaimOne(ctx, name, dir, ttl, dirBytes(dir))
		} else {
			// Nothing to prune is the usual case on every pass after the first,
			// and costs a few lstats instead of taking opMu. Whether there is
			// something is the paths' existence, not the byte count: a tree
			// deeper than the walk goes counts for nothing and still has to go.
			size, found := cacheBytes(ctx, dir)
			if !found || ctx.Err() != nil {
				continue
			}
			reclaimed, err = m.pruneOne(ctx, name, dir, cacheTTL, size)
		}
		// A prune can both free something and fail on another path, so the two
		// are recorded independently.
		if err != nil {
			errs = append(errs, err)
		}
		if reclaimed != nil {
			out = append(out, *reclaimed)
		}
	}
	// Said once, after the loop, because the deadline can pass anywhere in the
	// last home's work and the loop then just ends: without this the pass would
	// return no error, and the caller would read that as a clean one. A prune cut
	// short in pruneOne has said it already.
	if err := ctx.Err(); err != nil && !errors.Is(errors.Join(errs...), err) {
		errs = append(errs, err)
	}
	errs = append(errs, m.pruneOrphanMarkers()...)
	return out, errors.Join(errs...)
}

// stillUnused re-reads, for one candidate, every ground the snapshot in
// ReclaimState established, and reports the home's age and whether the home is
// in fact still unused for at least minAge. The caller holds opMu and keeps
// holding it across whatever it then does to the home. Every ground is re-read
// rather than trusted from the snapshot.
func (m *Manager) stillUnused(ctx context.Context, name string, minAge time.Duration) (time.Duration, bool) {
	m.mu.Lock()
	live := m.active[name]
	m.mu.Unlock()
	if live > 0 {
		return 0, false
	}
	// Closes the hole the initial ListByLabel snapshot cannot: a session may
	// have spawned and already released between the snapshot and here, leaving
	// active back at 0 while the container it created is still up, awaiting
	// its idle timer. When Inspect cannot say for certain, do not delete.
	st, err := m.opts.Client.Inspect(ctx, name)
	if err != nil || st == StateRunning {
		return 0, false
	}
	// The age from the snapshot is just as stale as the other two grounds were.
	// A spawn landing in that window touches the marker, and its container can
	// be gone again by the time Inspect runs — removed by hand, or stopped and
	// removed — leaving a home used seconds ago condemned on a month-old
	// reading. The marker is the record of that use, so read it again here.
	age, ok := m.homeAge(name)
	if !ok || age < minAge {
		return 0, false
	}
	return age, true
}

// reclaimOne re-checks a single candidate under opMu and removes its home if
// it is still, in fact, unused. A nil, nil return means the box came back to life
// since the snapshot and was correctly left alone — not an error.
func (m *Manager) reclaimOne(ctx context.Context, name, dir string, ttl time.Duration, size int64) (*Reclaimed, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	age, ok := m.stillUnused(ctx, name, ttl)
	if !ok {
		return nil, nil
	}

	// The out dir sits beside the home, not in it, so it goes separately. Both
	// are attempted whatever the first returns: a stubborn home must not keep
	// the screenshots alive too.
	outDir := filepath.Dir(OutputDir(m.opts.StateRoot, name))
	var causes []string
	for _, r := range []struct{ what, path string }{{"home", dir}, {"output dir", outDir}} {
		if rerr := os.RemoveAll(r.path); rerr != nil {
			// Each cause carries the path this package built (StateRoot plus a
			// validated box name) so the operator can tell the two apart. What
			// os.RemoveAll names is dropped: it is the file it stopped at, which
			// the box chose, newlines included, on its way into the daemon log.
			causes = append(causes, fmt.Sprintf("%s %q: %s", r.what, r.path, removalCause(rerr)))
		}
	}
	if len(causes) > 0 {
		return nil, fmt.Errorf("box: reclaiming %s: %s", name, strings.Join(causes, "; "))
	}
	_ = os.Remove(m.markerPath(name))
	return &Reclaimed{Box: name, Age: age, Bytes: size}, nil
}

// pruneOne removes the cachePaths of a home that is unused but younger than the
// state TTL, under the same opMu and the same re-checks as reclaimOne. A nil, nil
// return means the box came back to life since the snapshot, or there was nothing
// left to remove. The result and the error can both be set: a path that will not
// go does not stop the others, and what did go is still reported.
//
// Every filesystem call goes through an os.Root on the home, which refuses a
// path that escapes it. That is not enough alone: a link that stays inside the
// root (.x -> .claude) IS followed, so a removal of ".x/sub" deletes
// ".claude/sub". plainPath therefore refuses any path with a symlink in it, and
// RemoveAll then only ever sees real directories, removing a link it meets
// inside the tree as the link it is.
//
// Neither the home's own mtime nor the marker is the prune's to move. Unlinking
// .npm from the home changes the home's mtime, which is the age of a home with no
// marker, so that mtime is put back and a prune never makes a home look used.
func (m *Manager) pruneOne(ctx context.Context, name, dir string, cacheTTL time.Duration, size int64) (*Reclaimed, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	age, ok := m.stillUnused(ctx, name, cacheTTL)
	if !ok {
		return nil, nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("box: pruning caches of %s: %w", name, err)
	}
	defer root.Close()
	home, err := root.Lstat(".")
	if err != nil {
		return nil, fmt.Errorf("box: pruning caches of %s: %w", name, err)
	}

	var errs []error
	removed := false
	for _, p := range cachePaths {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if !plainPath(root, p) {
			continue
		}
		// Most trees go in this one call: the build cache, npm's, the venv, and a
		// module cache written with -modcacherw. Only a read-only one needs the
		// walk that makes its directories writable, and a walk costs far more
		// than the removal, with opMu held.
		err := root.RemoveAll(p)
		if err != nil {
			makeRemovable(ctx, root, p)
			err = root.RemoveAll(p)
		}
		if err != nil {
			// p is from cachePaths. What os.Root's error says it stopped at is not
			// relied on to stay free of names from inside the home.
			errs = append(errs, fmt.Errorf("%s: %s", p, removalCause(err)))
			continue
		}
		removed = true
	}
	if _, err := os.Stat(m.markerPath(name)); err != nil {
		// A zero time leaves the access time as it is; only the mtime is read.
		if err := root.Chtimes(".", time.Time{}, home.ModTime()); err != nil {
			errs = append(errs, fmt.Errorf("restoring the age of the home: %w", err))
		}
	}

	var out *Reclaimed
	if removed {
		out = &Reclaimed{Box: name, Age: age, Bytes: size, Caches: true}
	}
	if len(errs) > 0 {
		return out, fmt.Errorf("box: pruning caches of %s: %w", name, errors.Join(errs...))
	}
	return out, nil
}

// removalCause is the reason a removal failed, without any path. A failed
// RemoveAll names the file it stopped at, and below a box home that name was
// chosen by the box (newlines included), so an error that is logged or shown must
// not carry it: the errno is the part worth reporting, and the caller adds the
// box and the allowlisted path it already knows.
func removalCause(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno.Error()
	}
	switch e := err.(type) {
	case *fs.PathError:
		return removalCause(e.Err)
	case *os.LinkError:
		return removalCause(e.Err)
	case *os.SyscallError:
		return removalCause(e.Err)
	case interface{ Unwrap() []error }:
		var parts []string
		for _, c := range e.Unwrap() {
			parts = append(parts, removalCause(c))
		}
		return strings.Join(parts, "; ")
	case interface{ Unwrap() error }:
		return removalCause(e.Unwrap())
	}
	return err.Error()
}

// plainPath reports whether every component of rel (slash-separated, relative to
// root) exists and is a real directory. Lstat is what makes this a symlink check:
// it stops at a link instead of resolving it, and by the time a component is
// checked everything before it is known not to be one, so nothing here is
// followed on the way down.
func plainPath(root *os.Root, rel string) bool {
	parts := strings.Split(rel, "/")
	for i := range parts {
		fi, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil || !fi.IsDir() {
			return false
		}
	}
	return true
}

// maxCacheDepth is how many directory levels below a cache path the walks read.
// The module and build caches, npm's cache and a venv are a dozen levels deep at
// most. A home is the agent's to fill, though, and a chain of thousands of
// directories (`mkdir a; cd a`, repeated) makes a walk through root.FS() stall:
// os.Root resolves every path component of every open one by one, so the cost
// grows with the square of the depth (measured: 1000 deep 6.7 s, 2000 deep 46 s),
// on a pass that runs before the daemon serves and under opMu. Past the cap a
// directory is not read; RemoveAll still removes it, at far less cost than a
// walk (measured: 0.12 s, 0.28 s and 0.85 s at 1000, 2000 and 4000 deep).
const maxCacheDepth = 64

// walkCache calls visit for every entry under rel, which has passed plainPath,
// down to maxCacheDepth, and stops with ctx's error as soon as ctx is done.
// visit runs on a directory before it is read.
func walkCache(ctx context.Context, root *os.Root, rel string, visit func(p string, d fs.DirEntry)) error {
	top := strings.Count(rel, "/")
	return fs.WalkDir(root.FS(), rel, func(p string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			return nil
		}
		if d.IsDir() && strings.Count(p, "/")-top > maxCacheDepth {
			return fs.SkipDir
		}
		visit(p, d)
		return nil
	})
}

// makeRemovable gives the owner full access to every directory under rel, which
// has passed plainPath. A Go module cache written without -modcacherw is 0555
// directories of 0444 files, and none of it can be unlinked until its
// directories are writable again. The chmod comes BEFORE the walk reads a
// directory, so even one with no read bit can be listed. A chmod that fails is
// ignored: if it mattered, RemoveAll fails right after with the real cause (a
// directory owned by another uid), and that is the error worth reporting.
func makeRemovable(ctx context.Context, root *os.Root, rel string) {
	_ = walkCache(ctx, root, rel, func(p string, d fs.DirEntry) {
		if !d.IsDir() {
			return
		}
		if fi, err := d.Info(); err == nil && fi.Mode().Perm()&0o700 != 0o700 {
			_ = root.Chmod(p, fi.Mode().Perm()|0o700)
		}
	})
}

// cacheBytes sums the regular-file bytes under the cachePaths of dir that
// pruneOne would remove, and reports whether any of those paths exists at all.
// It only reads, so it runs before opMu like dirBytes; it is also what keeps
// every pass after the first cheap: a home with no cache path left returns
// found=false and is never locked. The sum is a report number and stops at
// maxCacheDepth; found does not depend on it.
func cacheBytes(ctx context.Context, dir string) (size int64, found bool) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return 0, false
	}
	defer root.Close()
	for _, p := range cachePaths {
		if !plainPath(root, p) {
			continue
		}
		found = true
		err := walkCache(ctx, root, p, func(_ string, d fs.DirEntry) {
			if !d.Type().IsRegular() {
				return
			}
			if fi, err := d.Info(); err == nil {
				size += fi.Size()
			}
		})
		if err != nil {
			break
		}
	}
	return size, found
}

// isBoxName matches only what boxName produces: the prefix plus 12 hex digits.
// Everything else under boxstate/ — including the .lastused directory and any
// file a human dropped there — is left alone.
func isBoxName(s string) bool {
	if !strings.HasPrefix(s, namePrefix) {
		return false
	}
	h := s[len(namePrefix):]
	if len(h) != 12 {
		return false
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// IsBoxName reports whether s is a name BoxName/Wrap can produce: the prefix plus
// 12 lowercase hex digits. Anything that becomes a path component on the host
// (the broker's per-box socket directory) must pass it first.
func IsBoxName(s string) bool { return isBoxName(s) }

// homeAge reports how long since the box was last used. The marker is the real
// signal; a home without one predates the marker and falls back to its own
// mtime, which is safe only because the caller has already refused every home
// with a live session or a surviving container.
func (m *Manager) homeAge(boxName string) (time.Duration, bool) {
	if fi, err := os.Stat(m.markerPath(boxName)); err == nil {
		return time.Since(fi.ModTime()), true
	}
	fi, err := os.Stat(m.stateDir(boxName))
	if err != nil {
		return 0, false
	}
	return time.Since(fi.ModTime()), true
}

// dirBytes sums the regular-file bytes under dir. This is the one filesystem
// walk the design accepts, and only for a directory already condemned.
func dirBytes(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			total += fi.Size()
		}
		return nil
	})
	return total
}

// pruneOrphanMarkers drops markers whose home is gone, so the directory cannot
// grow on its own after a home is removed by any other means. Only entries
// isBoxName recognizes are considered: this is the second directory this
// package deletes from, and "only delete what this package itself generates"
// has to hold for both.
func (m *Manager) pruneOrphanMarkers() []error {
	dir := filepath.Join(m.opts.StateRoot, stateDirName, lastUsedDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var errs []error
	for _, e := range entries {
		if !isBoxName(e.Name()) {
			continue
		}
		if _, err := os.Stat(m.stateDir(e.Name())); err == nil {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			errs = append(errs, fmt.Errorf("box: removing stale marker %s: %w", e.Name(), err))
		}
	}
	return errs
}
