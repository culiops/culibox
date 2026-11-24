// Package boximagecheck tells an instance which box image it runs and whether the
// image CI built from this binary's Dockerfile is a different one. It describes;
// it never pulls, tags or edits anything.
package boximagecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	boximage "github.com/culiops/culibox/docker/culibox"
	"github.com/culiops/culibox/internal/boxregistry"
	"github.com/culiops/culibox/internal/datacompat"
)

// CacheFile is the check's answer, under the data root. Only CuliBox writes it.
const CacheFile = "image-check.json"

const (
	cacheVersion = 1
	// maxCacheSize is far more than a cache holds (an index is a few KB); a bigger
	// file is not ours and is treated as unreadable.
	maxCacheSize = 1 << 20
	// maxScanField bounds what a scan's number and count may be: the title the
	// count comes from holds at most six digits.
	maxScanField = 999999
	// checkEvery is how long an answer, whatever it was, stands.
	checkEvery = 24 * time.Hour
)

// Scan is the weekly vulnerability scan's open issue: its number and the count of
// fixable HIGH or CRITICAL findings its title states. Nothing else of the issue is kept.
type Scan struct {
	Number int `json:"number"`
	Count  int `json:"count"`
}

// Cache is what the daily check learned, shared by the server and doctor
// (<dataDir>/image-check.json). Hash is the Dockerfile hash of the binary that
// asked: an answer for another Dockerfile is not this binary's answer.
type Cache struct {
	Version     int                `json:"version"`
	CheckedAt   time.Time          `json:"checked_at"`
	Hash        string             `json:"hash"`
	Registry    *boxregistry.Index `json:"registry,omitempty"`
	RegistryErr string             `json:"registry_err,omitempty"` // a short class, never a raw body
	Scan        *Scan              `json:"scan,omitempty"`
	ScanErr     string             `json:"scan_err,omitempty"`
}

// LoadCache reads the cache. A missing, unreadable or unparseable file is (nil, nil):
// the check is simply due and writes a fresh one. A file from a newer CuliBox is
// (nil, datacompat.ErrNewer) and must be left as it is. What comes back from disk is
// checked before anyone shows it: a registry ref that is not a digest of the
// published repository, or a scan number or count outside what a title can state,
// is dropped.
func LoadCache(dataDir string) (*Cache, error) {
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return nil, nil
	}
	defer root.Close()
	f, err := root.Open(CacheFile)
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxCacheSize+1))
	if err != nil || len(b) > maxCacheSize {
		return nil, nil
	}
	// Probe the version before the typed decode: a newer CuliBox may have changed
	// the other fields' types, and that must read as "newer", not as garbage.
	var probe struct{ Version int }
	if json.Unmarshal(b, &probe) == nil && probe.Version > cacheVersion {
		return nil, datacompat.Newer(filepath.Join(dataDir, CacheFile), fmt.Sprintf("version %d, this build understands %d", probe.Version, cacheVersion))
	}
	var c Cache
	if err := json.Unmarshal(b, &c); err != nil || c.Version != cacheVersion {
		return nil, nil
	}
	if c.Registry != nil && !boximage.ValidPublished(c.Registry.Ref) {
		c.Registry = nil
	}
	if c.Registry != nil {
		for arch, p := range c.Registry.Platforms {
			c.Registry.Platforms[arch] = p.Sanitized()
		}
	}
	// Doctor and the UI print the error classes, so a text that is not one of ours
	// never gets that far.
	c.RegistryErr = knownClass(c.RegistryErr, errUnexpected, errUnreachable, errNotFound, errUnexpected)
	c.ScanErr = knownClass(c.ScanErr, errScan, errScan)
	if s := c.Scan; s != nil && (s.Number < 0 || s.Number > maxScanField || s.Count < 0 || s.Count > maxScanField) {
		c.Scan = nil
	}
	return &c, nil
}

// knownClass is s when it is "" or one of classes, else fallback.
func knownClass(s, fallback string, classes ...string) string {
	if s == "" || slices.Contains(classes, s) {
		return s
	}
	return fallback
}

// SaveCache writes the cache atomically, through os.Root with an O_EXCL temp file
// so a symlink planted in the data dir is never followed (same pattern as linktrust).
func SaveCache(dataDir string, c *Cache) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return err
	}
	defer root.Close()
	tmp := fmt.Sprintf("%s.tmp.%d", CacheFile, os.Getpid())
	_ = root.Remove(tmp) // leftover from a crashed write by this pid
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		_ = root.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = root.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	if err := root.Rename(tmp, CacheFile); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	return nil
}

// Due reports whether the check should run: there is no cache, it answers for
// another Dockerfile (the binary changed), or it is more than a day old. One
// attempt a day whatever its outcome; retryFailed (the CLI doctor, which a user
// runs to confirm a fix) also runs again over a failed registry check. A cache
// stamped in the future is due: it would otherwise park the check until then.
func Due(c *Cache, now time.Time, retryFailed bool) bool {
	switch {
	case c == nil, c.Hash != boximage.Hash():
		return true
	case now.Before(c.CheckedAt), now.Sub(c.CheckedAt) > checkEvery:
		return true
	}
	return retryFailed && c.RegistryErr != ""
}

// RunIfDue runs the check when this instance may ask the network and the cache is
// due, and saves the answer. Otherwise it returns the cache as loaded (nil when
// there is none). A cache from a newer CuliBox is (nil, err) and is never written.
// A check cut short by a cancelled context returns its answer with context.Canceled
// and writes nothing. A failed save still returns the fresh answer, with the error; the cache on disk
// is then still the old one, so the check is due again on the very next call.
// Callers that call repeatedly (the server's hourly tick) must rate-limit
// themselves, for instance by remembering the time of their last attempt in memory.
func RunIfDue(ctx context.Context, k Checker, dataDir string, in Inputs, retryFailed bool) (*Cache, error) {
	c, err := LoadCache(dataDir)
	if err != nil {
		return nil, err
	}
	if !NeedsNetwork(in) || !Due(c, k.now(), retryFailed) {
		return c, nil
	}
	c = k.Check(ctx)
	// A cancelled context is shutdown, not a registry that failed: saving the
	// "unreachable" it produced would hold the check off for a day. A deadline that
	// ran out is a real failure and is saved.
	if errors.Is(ctx.Err(), context.Canceled) {
		return c, ctx.Err()
	}
	return c, SaveCache(dataDir, c)
}
