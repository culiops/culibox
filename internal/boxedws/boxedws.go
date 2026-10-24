// Package boxedws keeps the persistent, host-written record of every workspace
// directory that has ever been spawned boxed.
//
// A box can write anywhere in its workspace, so a symlink it plants there is
// untrusted for as long as the tree exists, not only while the session runs.
// The web fs API therefore treats every recorded workspace as contained
// (resolution never leaves it). This registry is what makes that protection
// outlive the session: an entry is added when a box is spawned and is never
// removed.
package boxedws

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

const (
	fileName = "boxed-workspaces.json"
	version  = 1
)

type fileFormat struct {
	Version int `json:"version"`
	// Seeded is true once a COMPLETE first-boot seed was written. A file
	// without it (written by a build that predates the flag) reads as false
	// and is re-seeded, which is additive and harmless.
	Seeded     bool     `json:"seeded"`
	Workspaces []string `json:"workspaces"`
}

// Registry is the set of boxed workspaces, persisted in <dataDir>/boxed-workspaces.json.
type Registry struct {
	mu        sync.Mutex
	dataDir   string
	unboxable func(string) (string, bool)
	set       map[string]struct{}
	seeded    bool
}

// Open loads <dataDir>/boxed-workspaces.json. seeded=false when the file is
// absent or no complete seed was ever written to it (the caller seeds; a file
// that exists only because a spawn recorded a workspace is not seeded). A
// present but unreadable or corrupt file, or one
// with a version this build does not know, is an error: the registry is a
// security boundary, so an empty fallback would silently un-contain every
// workspace (fail closed).
// unboxable is box.Manager.Unboxable (reason, true means refuse the entry).
func Open(dataDir string, unboxable func(string) (string, bool)) (r *Registry, seeded bool, err error) {
	if unboxable == nil {
		unboxable = func(string) (string, bool) { return "", false }
	}
	r = &Registry{dataDir: dataDir, unboxable: unboxable, set: map[string]struct{}{}}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, false, err
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return nil, false, err
	}
	defer root.Close()
	b, err := root.ReadFile(fileName)
	if errors.Is(err, fs.ErrNotExist) {
		return r, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("boxedws: read %s: %w", fileName, err)
	}
	var f fileFormat
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, false, fmt.Errorf("boxedws: parse %s: %w", filepath.Join(dataDir, fileName), err)
	}
	if f.Version != version {
		return nil, false, fmt.Errorf("boxedws: %s has version %d, this build understands %d",
			filepath.Join(dataDir, fileName), f.Version, version)
	}
	for _, w := range f.Workspaces {
		r.set[w] = struct{}{}
	}
	r.seeded = f.Seeded
	return r, f.Seeded, nil
}

// List returns a sorted copy of the recorded workspaces.
func (r *Registry) List() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return sortedLocked(r.set)
}

// Add records dir (EvalSymlinks'd, so it must exist). An entry unboxable
// refuses, or one that does not resolve, is logged and skipped (nil error);
// only a write failure is an error.
func (r *Registry) Add(dir string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := cloneSet(r.set)
	if !r.addLocked(next, dir) {
		return nil
	}
	return r.commitLocked(next, r.seeded)
}

// Seed adds every dir and always writes the file, even if empty. complete says
// the caller saw every source (e.g. the docker listing succeeded, or there is
// nothing to list): only then is the registry marked seeded, so an incomplete
// seed is retried on the next boot while still recording what it found. The
// flag never goes from true back to false.
func (r *Registry) Seed(dirs []string, complete bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := cloneSet(r.set)
	for _, d := range dirs {
		r.addLocked(next, d)
	}
	return r.commitLocked(next, r.seeded || complete)
}

// addLocked resolves dir and inserts it into set, reporting whether the set
// changed. Skips are logged: a workspace that cannot be recorded is not
// contained, which the operator should be able to see.
func (r *Registry) addLocked(set map[string]struct{}, dir string) bool {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		log.Printf("boxedws: not recording %q: does not resolve: %v", dir, err)
		return false
	}
	if reason, refused := r.unboxable(resolved); refused {
		log.Printf("boxedws: not recording %q: %s", resolved, reason)
		return false
	}
	if _, ok := set[resolved]; ok {
		return false
	}
	set[resolved] = struct{}{}
	return true
}

// commitLocked writes next and, only once that succeeded, makes it current.
// The write goes through os.Root: the data dir sits next to box-writable
// directories, and a plain write on a joined path would follow a planted
// symlink. The temp file is O_EXCL so a pre-planted name is never reused.
func (r *Registry) commitLocked(next map[string]struct{}, seeded bool) error {
	b, err := json.Marshal(fileFormat{Version: version, Seeded: seeded, Workspaces: sortedLocked(next)})
	if err != nil {
		return err
	}
	b = append(b, '\n')
	root, err := os.OpenRoot(r.dataDir)
	if err != nil {
		return err
	}
	defer root.Close()
	tmp := fmt.Sprintf("%s.tmp.%d", fileName, os.Getpid())
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
	if err := root.Rename(tmp, fileName); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	r.set = next
	r.seeded = seeded
	return nil
}

func cloneSet(s map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(s)+1)
	for k := range s {
		out[k] = struct{}{}
	}
	return out
}

func sortedLocked(s map[string]struct{}) []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
