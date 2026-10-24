package box

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

// DefaultStateTTL is how long an unused box home survives. A workspace untouched
// for a month has no resume worth keeping, and each home costs tens of megabytes
// of regenerable npm cache.
const DefaultStateTTL = 30 * 24 * time.Hour

// Reclaimed describes one box home the sweep removed. The package returns this
// rather than logging it: every decision here is unit-tested without a daemon,
// which is only true while it imports no logger.
type Reclaimed struct {
	Box   string
	Age   time.Duration
	Bytes int64
}

// ReclaimState removes the host homes of boxes that no longer exist and have not
// been used for ttl.
//
// A home is removed only when all four hold, checked in this order because the
// cheap and certain tests come first:
//
//  1. its name is one this package generates,
//  2. no container of this instance still carries that name,
//  3. no live session is counted against it,
//  4. it is older than ttl.
//
// Grounds 2 and 3 are checked twice: once cheaply against a snapshot to decide
// whether a candidate is worth walking and stat-ing at all, and again — for
// real, under opMu — immediately before removal. Between the snapshot and the
// removal a spawn can start: `ensure` (box.go) takes opMu to create the
// container and bind-mount this exact directory as its $HOME, and a
// RemoveAll racing that mount would delete files out from under a live
// container. Holding opMu across the whole sweep would block every spawn for
// as long as the slowest directory walk takes, so the lock is scoped to one
// candidate's decide-and-remove instead. dirBytes runs BEFORE opMu is taken —
// it is only a report number, and a slightly stale byte count is an acceptable
// cost to keep the walk off the lock.
//
// A removal that fails is recorded and the sweep continues: one stubborn
// directory — a root-owned leftover from before mountpoints were pre-created —
// must not stop the rest. The failures come back joined so the caller can name
// them. Escalating to remove them is deliberately not done: running a root
// container over host state is the hole this package exists to close.
func (m *Manager) ReclaimState(ctx context.Context, ttl time.Duration) ([]Reclaimed, error) {
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
		if age, ok := m.homeAge(name); !ok || age < ttl {
			continue
		}
		dir := m.stateDir(name)
		size := dirBytes(dir)

		reclaimed, err := m.reclaimOne(ctx, name, dir, ttl, size)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if reclaimed != nil {
			out = append(out, *reclaimed)
		}
	}
	errs = append(errs, m.pruneOrphanMarkers()...)
	return out, errors.Join(errs...)
}

// reclaimOne re-checks a single candidate under opMu and removes its home if
// it is still, in fact, unused. Every ground is re-read here rather than trusted
// from the snapshot. A nil, nil return means the box came back to life since the
// snapshot and was correctly left alone — not an error.
func (m *Manager) reclaimOne(ctx context.Context, name, dir string, ttl time.Duration, size int64) (*Reclaimed, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	live := m.active[name]
	m.mu.Unlock()
	if live > 0 {
		return nil, nil
	}
	// Closes the hole the initial ListByLabel snapshot cannot: a session may
	// have spawned and already released between the snapshot and here, leaving
	// active back at 0 while the container it created is still up, awaiting
	// its idle timer. When Inspect cannot say for certain, do not delete.
	st, err := m.opts.Client.Inspect(ctx, name)
	if err != nil || st == StateRunning {
		return nil, nil
	}
	// The age from the snapshot is just as stale as the other two grounds were.
	// A spawn landing in that window touches the marker, and its container can
	// be gone again by the time Inspect runs — removed by hand, or stopped and
	// removed — leaving a home used seconds ago condemned on a month-old
	// reading. The marker is the record of that use, so read it again here.
	age, ok := m.homeAge(name)
	if !ok || age < ttl {
		return nil, nil
	}

	// The out dir sits beside the home, not in it, so it goes separately. Both
	// are attempted whatever the first returns: a stubborn home must not keep
	// the screenshots alive too.
	err = errors.Join(os.RemoveAll(dir), os.RemoveAll(filepath.Dir(OutputDir(m.opts.StateRoot, name))))
	if err != nil {
		return nil, fmt.Errorf("box: reclaiming %s: %w", name, err)
	}
	_ = os.Remove(m.markerPath(name))
	return &Reclaimed{Box: name, Age: age, Bytes: size}, nil
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
