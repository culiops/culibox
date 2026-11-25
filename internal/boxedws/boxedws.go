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
	"strings"
	"sync"

	"github.com/culiops/culibox/internal/datacompat"
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
	// GitGuarded says, per recorded workspace, whether a git repo there was
	// ever refused for boxing in place: false means the folder was boxed in
	// place while it was NOT a git repo, so the box may have written a .git
	// there. An absent key is unknown (an entry recorded before the flag
	// existed, or one recorded without a spawn). The map is optional: a file
	// from an older build has none and loads, with every entry unknown.
	GitGuarded map[string]bool `json:"git_guarded,omitempty"`
}

// The statuses InPlaceStatus returns.
const (
	// StatusNone: the path is not inside any recorded workspace.
	StatusNone = "none"
	// StatusNonGit: inside a workspace recorded as boxed in place when it was
	// not a git repo. A .git there may be box-written.
	StatusNonGit = "nongit"
	// StatusUnknown: inside a recorded workspace with no flag.
	StatusUnknown = "unknown"
)

// Registry is the set of boxed workspaces, persisted in <dataDir>/boxed-workspaces.json.
type Registry struct {
	mu        sync.Mutex
	dataDir   string
	unboxable func(string) (string, bool)
	set       map[string]struct{}
	guard     map[string]bool // GitGuarded, only for keys of set
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
	r = &Registry{dataDir: dataDir, unboxable: unboxable, set: map[string]struct{}{}, guard: map[string]bool{}}
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
	// Probe the version before the typed decode: a newer CuliBox may have
	// changed the other fields' types, and that must read as "newer", not as a
	// parse error. Still an error either way (fail closed).
	var probe struct{ Version int }
	if json.Unmarshal(b, &probe) == nil && probe.Version > version {
		return nil, false, datacompat.Newer(filepath.Join(dataDir, fileName), fmt.Sprintf("version %d, this build understands %d", probe.Version, version))
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
	for w, g := range f.GitGuarded {
		if _, ok := r.set[w]; ok {
			r.guard[w] = g
		}
	}
	r.seeded = f.Seeded
	return r, f.Seeded, nil
}

// Path is the record file, for a message that tells the user where to look.
func (r *Registry) Path() string { return filepath.Join(r.dataDir, fileName) }

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
	return r.commitLocked(next, r.guard, r.seeded)
}

// RecordInPlace records ws (EvalSymlinks'd, so it must exist) as boxed in
// place and says whether it was a git-guarded folder. A spawn passes false:
// the folder was boxed in place without git, so whatever .git appears there
// later is the box's. A false is never cleared (a later true or a plain Add
// leaves it); an entry recorded without a flag takes the flag. An entry
// unboxable refuses, or one that does not resolve, is logged and skipped (nil
// error); only a write failure is an error.
func (r *Registry) RecordInPlace(ws string, gitGuarded bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := cloneSet(r.set)
	r.addLocked(next, ws)
	resolved, err := filepath.EvalSymlinks(ws)
	if err != nil {
		return nil // addLocked logged it
	}
	if _, ok := next[resolved]; !ok {
		return nil // refused, logged
	}
	if cur, has := r.guard[resolved]; has && (!cur || cur == gitGuarded) {
		if len(next) == len(r.set) {
			return nil // nothing changes
		}
		return r.commitLocked(next, r.guard, r.seeded)
	}
	guard := cloneGuard(r.guard)
	guard[resolved] = gitGuarded
	return r.commitLocked(next, guard, r.seeded)
}

// InPlaceStatus says what the record knows of p: StatusNone when p is not a
// recorded workspace or inside one, StatusNonGit when it is inside one recorded
// as boxed in place without git, StatusUnknown when it is inside one with no
// flag. p is resolved through symlinks as far as it exists (the record holds
// resolved paths); a nested entry's stricter status wins.
func (r *Registry) InPlaceStatus(p string) string {
	p = resolveBest(p)
	r.mu.Lock()
	defer r.mu.Unlock()
	status := StatusNone
	for w := range r.set {
		if !within(p, w) {
			continue
		}
		g, has := r.guard[w]
		switch {
		case has && !g:
			return StatusNonGit
		case !has:
			status = StatusUnknown
		}
	}
	return status
}

// within reports whether p is dir or below it, by whole path components.
func within(p, dir string) bool {
	if p == dir {
		return true
	}
	return strings.HasPrefix(p, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator))
}

// resolveBest resolves symlinks in the longest existing prefix of p and
// appends the rest, so a path that does not exist yet still compares in the
// form the record keeps. A relative or unresolvable path comes back cleaned.
func resolveBest(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
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
	return r.commitLocked(next, r.guard, r.seeded || complete)
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
func (r *Registry) commitLocked(next map[string]struct{}, guard map[string]bool, seeded bool) error {
	b, err := json.Marshal(fileFormat{Version: version, Seeded: seeded, Workspaces: sortedLocked(next), GitGuarded: guard})
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
	r.guard = guard
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

func cloneGuard(g map[string]bool) map[string]bool {
	out := make(map[string]bool, len(g)+1)
	for k, v := range g {
		out[k] = v
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
