package bundle

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GCMinAge is how long an unused bundle survives after a spawn last took it. It
// is what covers a spawn whose container does not exist yet: nothing in docker
// names the dir until then, so Ensure's refreshed date is its only protection.
const GCMinAge = 14 * 24 * time.Hour

const (
	// gcPrefix marks a directory the GC has condemned. Ensure only ever looks at
	// a bare hash, so nothing under this name, whole or half removed, can be
	// handed to a spawn.
	gcPrefix = ".gc-"

	importsSuffix = ".imports"
	hashLen       = 16
)

// MountLister answers which host paths containers mount. An error means the
// answer is unknown, which is never the same as nothing being mounted.
type MountLister interface {
	MountSources(ctx context.Context) ([]string, error)
}

// GCRemoved is one bundle a pass removed. The package reports rather than logs,
// like box.ReclaimState, so every decision is tested without a daemon.
type GCRemoved struct {
	Hash  string
	Age   time.Duration
	Bytes int64
}

// GCReport is what a pass did. Removed and Swept are real removals; the counts
// after them are bundles it looked at and kept.
type GCReport struct {
	Removed []GCRemoved
	Swept   int // leftovers of an earlier pass that were removed
	// InUse is old bundles a container mounts. Current is old bundles the source
	// still hashes to. TooYoung is unmounted bundles newer than GCMinAge.
	InUse, Current, TooYoung int
	// Held is condemned dirs kept because a container names them.
	Held int
}

// gcPair is a bundle dir and its imports dir, which live and die together.
type gcPair struct {
	hash     string
	names    []string // those of the pair that exist: bundle first, so its lock goes first
	youngest time.Time
}

// gcScan is what the bundle root holds, from reading it and nothing else.
type gcScan struct {
	candidates []gcPair // old enough, not current
	leftovers  []string // condemned names from an earlier pass
	current    int
	tooYoung   int
}

// GC removes the bundle dirs and imports dirs no container mounts and no spawn
// has taken for GCMinAge, and reports what it did. Only a real directory under
// the exact name of a bundle or its imports is ever considered; the plugin
// snapshots, session-mcp and anything else in the root are not its business.
//
// It removes nothing when it cannot be sure: the current hash unknown or docker
// unreadable, in any way, ends the pass before anything is touched.
//
// A pair is removed only when all of these hold, in order:
//
//  1. the source does not hash to it now,
//  2. it is older than GCMinAge,
//  3. no container in the first listing mounts it,
//  4. it is still older than GCMinAge, read again with gcMu held, in the same
//     critical section as the rename to the condemned name,
//  5. no container in a second listing, taken after the rename, mounts it.
//
// Step 4 is why gcMu exists: Ensure refreshes the date under it, so a reuse and
// a rename cannot interleave into a spawn holding a path that just went. Step 5
// is final because the rename is. Once the bare name is gone no Ensure can hand
// it out, and any Ensure that handed it out earlier also refreshed the date. The
// lock is held only for one pair's renames, never across a docker call or a
// removal, so a pass adds no latency to a spawn beyond a few renames.
//
// A crash anywhere leaves either the bundle as it was or condemned names, which
// the next pass sweeps. A sweep never renames back: a removal may have started,
// and a partial tree must not get a selectable name. Only the pass that did the
// renaming restores, because it knows it has not started removing.
func (m *Manager) GC(ctx context.Context, mounts MountLister) (GCReport, error) {
	var rep GCReport
	if err := ctx.Err(); err != nil {
		return rep, err
	}
	current, err := m.currentHashes()
	if err != nil {
		return rep, fmt.Errorf("bundle: gc: current hash unknown, removing nothing: %w", err)
	}
	scan, err := m.scanGC(current, time.Now())
	if errors.Is(err, fs.ErrNotExist) {
		return rep, nil // no bundle was ever built
	}
	if err != nil {
		return rep, fmt.Errorf("bundle: gc: %w", err)
	}
	rep.Current, rep.TooYoung = scan.current, scan.tooYoung
	if len(scan.candidates) == 0 && len(scan.leftovers) == 0 {
		return rep, nil
	}
	sources, err := mounts.MountSources(ctx)
	if err != nil {
		return rep, fmt.Errorf("bundle: gc: docker mounts unknown, removing nothing: %w", err)
	}
	root, err := os.OpenRoot(m.opts.Root)
	if err != nil {
		return rep, fmt.Errorf("bundle: gc: %w", err)
	}
	defer root.Close()
	refs := m.referenced(sources)

	var errs []error
	for _, name := range scan.leftovers {
		if ctx.Err() != nil {
			break
		}
		if refs[hashOf(name)] {
			rep.Held++
			continue
		}
		if err := removeTree(root, name); err != nil {
			errs = append(errs, fmt.Errorf("bundle: gc: removing %s: %w", name, err))
			continue
		}
		rep.Swept++
	}

	var condemned []GCRemoved
	var pairs []gcPair
	for _, p := range scan.candidates {
		if refs[p.hash] {
			rep.InUse++
			continue
		}
		if ctx.Err() != nil {
			break
		}
		size := m.pairBytes(p)
		ok, age, err := m.condemn(root, p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !ok {
			rep.TooYoung++
			continue
		}
		condemned = append(condemned, GCRemoved{Hash: p.hash, Age: age, Bytes: size})
		pairs = append(pairs, p)
	}
	if len(pairs) == 0 {
		return rep, errors.Join(errs...)
	}

	sources, err = mounts.MountSources(ctx)
	if err != nil {
		for _, p := range pairs {
			if _, rerr := m.restore(root, p); rerr != nil {
				errs = append(errs, rerr)
			}
		}
		errs = append(errs, fmt.Errorf("bundle: gc: docker mounts unknown after renaming, restored: %w", err))
		return rep, errors.Join(errs...)
	}
	refs = m.referenced(sources)
	for i, p := range pairs {
		if refs[p.hash] {
			rep.InUse++
			back, err := m.restore(root, p)
			if err != nil {
				errs = append(errs, err)
			}
			if !back {
				rep.Held++
			}
			continue
		}
		if ctx.Err() != nil {
			break // still condemned, so the next pass sweeps them
		}
		var failed error
		for _, name := range p.names {
			if err := removeTree(root, gcPrefix+name); err != nil {
				failed = fmt.Errorf("bundle: gc: removing %s: %w", gcPrefix+name, err)
			}
		}
		if failed != nil {
			errs = append(errs, failed) // still condemned, so the next pass retries
			continue
		}
		rep.Removed = append(rep.Removed, condemned[i])
	}
	return rep, errors.Join(errs...)
}

// currentHashes is what the source hashes to now, under both plugin verdicts. The
// verdict comes from pluginSnapshot, which publishes a snapshot dir, and a pass
// must not write, so both hashes are protected: at worst one old dir stays.
func (m *Manager) currentHashes() (map[string]bool, error) {
	mcp := readMCPSource(m.opts.MCPFile)
	imports := m.Imports()
	out := map[string]bool{}
	for _, mounted := range []bool{true, false} {
		h, _, err := m.sourceHash(mounted, mcp, imports)
		if err != nil {
			return nil, err
		}
		out[h] = true
	}
	return out, nil
}

// scanGC reads the root and sorts what it finds. It writes nothing. An entry that
// cannot be read, or a pair with a half that is not a real directory, is kept.
func (m *Manager) scanGC(current map[string]bool, now time.Time) (gcScan, error) {
	var scan gcScan
	entries, err := os.ReadDir(m.opts.Root)
	if err != nil {
		return scan, err
	}
	pairs := map[string]*gcPair{}
	odd := map[string]bool{}
	var order []string
	for _, e := range entries {
		name := e.Name()
		if rest, marked := strings.CutPrefix(name, gcPrefix); marked {
			if hashOf(rest) != "" && !strings.HasPrefix(rest, gcPrefix) && e.Type().IsDir() {
				scan.leftovers = append(scan.leftovers, name)
			}
			continue
		}
		hash := hashOf(name)
		if hash == "" {
			continue
		}
		if !e.Type().IsDir() {
			odd[hash] = true
			continue
		}
		fi, err := e.Info()
		if err != nil {
			odd[hash] = true
			continue
		}
		p := pairs[hash]
		if p == nil {
			p = &gcPair{hash: hash}
			pairs[hash] = p
			order = append(order, hash)
		}
		if name == hash {
			p.names = append([]string{name}, p.names...)
		} else {
			p.names = append(p.names, name)
		}
		if fi.ModTime().After(p.youngest) {
			p.youngest = fi.ModTime()
		}
	}
	for _, hash := range order {
		p := pairs[hash]
		switch {
		case odd[hash]:
		case now.Sub(p.youngest) < GCMinAge:
			scan.tooYoung++
		case current[hash]:
			scan.current++
		default:
			scan.candidates = append(scan.candidates, *p)
		}
	}
	return scan, nil
}

// condemn re-reads the pair's age and renames it, under the lock Ensure takes to
// refresh that age. Bundle first: a bundle without its imports dir is rebuilt,
// but a condemned imports dir beside a live bundle could still be selected when
// its lock lists no imports. ok false with no error means the pair came back to
// use since the scan.
func (m *Manager) condemn(root *os.Root, p gcPair) (ok bool, age time.Duration, err error) {
	m.gcMu.Lock()
	defer m.gcMu.Unlock()
	var youngest time.Time
	for _, name := range p.names {
		fi, err := root.Lstat(name)
		if err != nil || !fi.IsDir() {
			return false, 0, nil
		}
		if fi.ModTime().After(youngest) {
			youngest = fi.ModTime()
		}
	}
	age = time.Since(youngest)
	if age < GCMinAge {
		return false, age, nil
	}
	for i, name := range p.names {
		if err := root.Rename(name, gcPrefix+name); err != nil {
			for _, done := range p.names[:i] {
				if rerr := root.Rename(gcPrefix+done, done); rerr != nil {
					err = errors.Join(err, rerr)
				}
			}
			return false, age, fmt.Errorf("bundle: gc: condemning %s: %w", name, err)
		}
	}
	return true, age, nil
}

// restore takes a condemned pair back, in the pass that condemned it: its removal
// has not begun. A name rebuilt in the meantime wins. The old tree stays condemned
// until no container names it, because a container's bind mount follows the
// inode, not the path. back is false when any half stayed condemned.
func (m *Manager) restore(root *os.Root, p gcPair) (back bool, err error) {
	m.gcMu.Lock()
	defer m.gcMu.Unlock()
	back = true
	for _, name := range p.names {
		if _, serr := root.Lstat(name); serr == nil {
			back = false
			continue
		}
		if rerr := root.Rename(gcPrefix+name, name); rerr != nil {
			back = false
			err = errors.Join(err, fmt.Errorf("bundle: gc: restoring %s: %w", name, rerr))
		}
	}
	return back, err
}

// referenced is the set of hashes some source is the dir of, or lies inside it:
// the layout mounts files in the bundle and import files one at a time. Paths are
// compared whole, because another instance's root holds the same hash names.
// Each side is compared as spelled and with its links resolved, since docker
// keeps the path it was given.
func (m *Manager) referenced(sources []string) map[string]bool {
	roots := []string{filepath.Clean(m.opts.Root)}
	if r, err := filepath.EvalSymlinks(m.opts.Root); err == nil && r != roots[0] {
		roots = append(roots, r)
	}
	refs := map[string]bool{}
	mark := func(source string) {
		for _, r := range roots {
			rest, ok := strings.CutPrefix(source, r+string(filepath.Separator))
			if !ok {
				continue
			}
			first, _, _ := strings.Cut(rest, string(filepath.Separator))
			if h := hashOf(strings.TrimPrefix(first, gcPrefix)); h != "" {
				refs[h] = true
			}
		}
	}
	for _, s := range sources {
		mark(filepath.Clean(s))
		if r, err := filepath.EvalSymlinks(s); err == nil {
			mark(r)
		}
	}
	return refs
}

// hashOf returns the hash a bundle or imports dir name belongs to, or "" when the
// name is neither. Exact on purpose: only what this package generates is ever
// deleted.
func hashOf(name string) string {
	h := strings.TrimPrefix(name, gcPrefix)
	h = strings.TrimSuffix(h, importsSuffix)
	if len(h) != hashLen || strings.Trim(h, "0123456789abcdef") != "" {
		return ""
	}
	return h
}

func (m *Manager) pairBytes(p gcPair) int64 {
	var total int64
	for _, name := range p.names {
		_ = filepath.WalkDir(filepath.Join(m.opts.Root, name), func(_ string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if fi, err := d.Info(); err == nil {
				total += fi.Size()
			}
			return nil
		})
	}
	return total
}

// removeTree deletes one direct child of root that is a real directory. Everything
// goes through os.Root, so no link in the tree is followed out of it. A read-only
// directory would stop the removal half way, so every directory is made writable
// first, parents before their contents so the walk can read them. A chmod that
// fails is not reported on its own: the removal then fails and says why.
func removeTree(root *os.Root, name string) error {
	fi, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("not a directory")
	}
	_ = root.Chmod(name, 0o700)
	tree, err := root.OpenRoot(name)
	if err != nil {
		return err
	}
	defer tree.Close()
	_ = fs.WalkDir(tree.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = tree.Chmod(path, 0o700)
		}
		return nil
	})
	return root.RemoveAll(name)
}
