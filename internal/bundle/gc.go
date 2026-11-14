package bundle

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
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

// GCRemoved is one bundle (with its imports) or plugin snapshot a pass removed. The package reports rather than logs,
// like box.ReclaimState, so every decision is tested without a daemon.
type GCRemoved struct {
	Name  string
	Age   time.Duration
	Bytes int64
}

// GCReport is what a pass did. Removed and Swept are real removals; the counts
// after them are bundles and snapshots it looked at and kept.
type GCReport struct {
	Removed []GCRemoved
	Swept   int // leftovers of an earlier pass that were removed
	// InUse is old ones a container mounts. Current is old ones the source still
	// hashes or fingerprints to. TooYoung is unmounted ones newer than GCMinAge.
	InUse, Current, TooYoung int
	// Held is condemned dirs kept because a container names them.
	Held int
	// SnapshotsUnsure is old or condemned snapshots kept because the host plugin
	// set could not be read, so the current ones are unknown.
	SnapshotsUnsure int
}

// gcPair is what lives and dies together: a bundle dir and its imports dir, or a
// plugin snapshot alone.
type gcPair struct {
	key      string
	names    []string // those of the pair that exist: bundle first, so its lock goes first
	youngest time.Time
}

// gcScan is what the bundle root holds, from reading it and nothing else.
type gcScan struct {
	candidates []gcPair // old enough, not current
	leftovers  []string // condemned names from an earlier pass
	current    int
	tooYoung   int
	unsure     int
}

// GC removes the bundle dirs, imports dirs and plugin snapshots no container
// mounts and no spawn has taken for GCMinAge, and reports what it did. Only a real
// directory under the exact name of one of those is ever considered; session-mcp
// and anything else in the root are not its business.
//
// It removes nothing when it cannot be sure: the current hash unknown or docker
// unreadable, in any way, ends the pass before anything is touched. A host plugin
// set that cannot be read leaves the current snapshots unknown, so that pass
// removes no snapshot and says so.
//
// No bundle dir names a snapshot, in its lock or anywhere else: it holds only the
// verdict that plugins are mounted, and every Ensure picks the snapshots afresh
// from the host's fingerprint, republishing one that is gone. So a kept bundle
// holds no snapshot alive, and only containers and the current fingerprint do.
//
// A pair is removed only when all of these hold, in order:
//
//  1. the source does not hash or fingerprint to it now,
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
	snapshots, snapshotsErr := m.currentSnapshots()
	if snapshotsErr == nil {
		maps.Copy(current, snapshots)
	}
	scan, err := m.scanGC(current, snapshotsErr == nil, time.Now())
	if errors.Is(err, fs.ErrNotExist) {
		return rep, nil // no bundle was ever built
	}
	if err != nil {
		return rep, fmt.Errorf("bundle: gc: %w", err)
	}
	rep.Current, rep.TooYoung, rep.SnapshotsUnsure = scan.current, scan.tooYoung, scan.unsure
	var errs []error
	if snapshotsErr != nil && scan.unsure > 0 {
		errs = append(errs, fmt.Errorf("bundle: gc: host plugin set unknown, removing no snapshot: %w", snapshotsErr))
	}
	if len(scan.candidates) == 0 && len(scan.leftovers) == 0 {
		return rep, errors.Join(errs...)
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

	for _, name := range scan.leftovers {
		if ctx.Err() != nil {
			break
		}
		if refs[keyOf(name)] {
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
		if refs[p.key] {
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
		condemned = append(condemned, GCRemoved{Name: p.key, Age: age, Bytes: size})
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
		if refs[p.key] {
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

// currentSnapshots is the snapshot names the host plugin set fingerprints to now,
// read without publishing anything. A set the verdict refuses or that cannot be
// walked is an error rather than "none": it may be a passing state, and the next
// spawn would then want what was removed. No plugins at all is a real none.
func (m *Manager) currentSnapshots() (map[string]bool, error) {
	pdir, fi, verdict, ok := pluginGate(m.opts.Source)
	if !ok {
		if len(verdict.drops) > 0 {
			return nil, fmt.Errorf("%s %s", verdict.drops[0].Name, verdict.drops[0].Reason)
		}
		return nil, nil
	}
	src, states, err := fingerprintSlots(pdir, fi, m.opts.Root)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	out := map[string]bool{}
	for _, st := range states {
		out[filepath.Base(st.final)] = true
	}
	return out, nil
}

// scanGC reads the root and sorts what it finds. It writes nothing. An entry that
// cannot be read, or a pair with a half that is not a real directory, is kept.
// With snapshotsKnown false no snapshot is a candidate or a leftover to sweep.
func (m *Manager) scanGC(current map[string]bool, snapshotsKnown bool, now time.Time) (gcScan, error) {
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
			if keyOf(rest) == "" || strings.HasPrefix(rest, gcPrefix) || !e.Type().IsDir() {
				continue
			}
			if isSnapshotName(rest) && !snapshotsKnown {
				scan.unsure++
				continue
			}
			scan.leftovers = append(scan.leftovers, name)
			continue
		}
		hash := keyOf(name)
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
			p = &gcPair{key: hash}
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
		case isSnapshotName(hash) && !snapshotsKnown:
			scan.unsure++
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
		if err := renameWritable(root, name, gcPrefix+name); err != nil {
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

// renameWritable renames the directory name to to, inside root. macOS refuses to
// rename a directory that has no owner-write permission (EACCES; measured on
// macos-15, #293), where Linux does not, and a bundle copied from a read-only tree
// has such a top directory. So on EACCES only, and only for a real directory
// (Lstat, never a link), the owner-write bit is added and the rename retried; the
// mode is put back if it still fails. Linux, and any rename that works at first,
// never change a mode. Everything goes through root, so nothing outside it is
// touched, and removeTree makes the tree writable anyway before deleting it.
func renameWritable(root *os.Root, name, to string) error {
	err := root.Rename(name, to)
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		return err
	}
	fi, lerr := root.Lstat(name)
	if lerr != nil || !fi.IsDir() || fi.Mode().Perm()&0o200 != 0 {
		return err
	}
	if cerr := root.Chmod(name, fi.Mode().Perm()|0o200); cerr != nil {
		return err
	}
	if rerr := root.Rename(name, to); rerr != nil {
		_ = root.Chmod(name, fi.Mode().Perm())
		return rerr
	}
	return nil
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

// referenced is the set of keys some source is the dir of, or lies inside it: the
// layout mounts files in the bundle, import files one at a time and a snapshot's
// entries. Paths are compared whole, because another instance's root holds the
// same names.
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
			if h := keyOf(strings.TrimPrefix(first, gcPrefix)); h != "" {
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

// keyOf returns the key a bundle, imports or snapshot dir name belongs to, marked
// or not: the hash for a bundle and its imports, the name for a snapshot. It
// returns "" when the name is none of those. Exact on purpose: only what this
// package generates is ever deleted.
func keyOf(name string) string {
	name = strings.TrimPrefix(name, gcPrefix)
	if isSnapshotName(name) {
		return name
	}
	h := strings.TrimSuffix(name, importsSuffix)
	if !isHash(h) {
		return ""
	}
	return h
}

// isSnapshotName matches only what slotState.name produces: plugins-<slot>-<token>.
func isSnapshotName(name string) bool {
	for _, sl := range pluginSlots {
		if rest, ok := strings.CutPrefix(name, "plugins-"+sl.name+"-"); ok {
			return isHash(rest)
		}
	}
	return false
}

func isHash(s string) bool {
	return len(s) == hashLen && strings.Trim(s, "0123456789abcdef") == ""
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
