// Package store persists user-created profiles to a JSON file (behind a small
// interface so it can be swapped for SQLite later). Config-file profiles are
// separate, read-only seeds; this store holds only runtime-created/edited ones.
package store

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/datacompat"
)

// FormatVersion is the profiles.json format this build reads and writes (#291).
//
// RULE: any new profile field that restricts, scopes or contains something
// (containment, egress, a credential reference, a deny list) MUST bump this
// number in the same change. A build that reads a higher version loads no
// profile and refuses writes (it fails closed), so an older build can never run
// a profile while dropping a restriction it does not know. A benign field needs
// no bump. A file with no version is version 1. Version 1 is written as the
// plain JSON list every release so far wrote, so a rollback to a build from
// before this rule still reads it; a later version is written as
// {"version": N, "profiles": [...]}, which such a build refuses (fail closed).
// See docs/UPGRADING.md. It is a var only so a test can raise it.
var FormatVersion = 1

// fileDoc is the object form of profiles.json, used from version 2 on.
type fileDoc struct {
	Version  int                  `json:"version"`
	Profiles []config.AgentConfig `json:"profiles"`
}

// decode reads either form. A version higher than FormatVersion is
// datacompat.Newer; a version that is not a whole number from 1 is an error.
func decode(path string, b []byte) ([]config.AgentConfig, error) {
	var list []config.AgentConfig
	if err := json.Unmarshal(b, &list); err == nil {
		return list, nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return nil, fmt.Errorf("store: parse %s: %w", path, err)
	}
	raw, ok := top["version"]
	if !ok {
		return nil, fmt.Errorf("store: parse %s: not a list of profiles and has no version", path)
	}
	v, err := strconv.Atoi(string(raw))
	if err != nil || v < 1 {
		return nil, fmt.Errorf("store: parse %s: version %s is not a whole number from 1", path, raw)
	}
	if v > FormatVersion {
		// Nothing else is read: no profile of a newer format is usable here.
		return nil, datacompat.Newer(path, fmt.Sprintf("format version %d, this build knows %d", v, FormatVersion))
	}
	var doc fileDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("store: parse %s: %w", path, err)
	}
	return doc.Profiles, nil
}

// ErrNotFound is returned by Delete/Get for an unknown profile.
var ErrNotFound = errors.New("store: profile not found")

// ErrChangedOnDisk is returned by Save and Delete when the profiles file no
// longer holds the bytes this process last read or wrote: someone edited,
// replaced or removed it, or it appeared after Open found none. The store
// refuses to write over it (that would silently discard the change) and leaves
// its in-memory state as it was; reloading means restarting.
var ErrChangedOnDisk = errors.New("store: the profiles file changed on disk after it was loaded")

// ErrUnreadable is returned by Save and Delete on a store OpenOrReadOnly made
// read-only: the profiles file has content this build cannot parse, and a write
// would replace it with what this build knows. The file is left as it is.
var ErrUnreadable = errors.New("store: the profiles file has content this build cannot read")

// Store is a JSON-file-backed profile store, safe for concurrent use.
type Store struct {
	path string
	mu   sync.Mutex
	m    map[string]config.AgentConfig

	// disk is the sha256 of the file as this process last read or wrote it;
	// onDisk is false when there was no file. checkDiskLocked compares them
	// with the file as it is now.
	disk   [sha256.Size]byte
	onDisk bool

	// unreadable is why the file did not parse; set only by OpenOrReadOnly. A
	// store with it set is empty and refuses every write.
	unreadable error
}

// Open loads the store from path (a missing file is an empty store).
func Open(path string) (*Store, error) {
	s, parseErr, err := OpenOrReadOnly(path)
	if err != nil {
		return nil, err
	}
	if parseErr != nil {
		return nil, parseErr
	}
	return s, nil
}

// OpenOrReadOnly is Open for serve: a file that does not parse gives an empty
// store that refuses Save and Delete (ErrUnreadable) and leaves the file alone,
// with the parse error as parseErr and a nil err. Any other read error is err,
// as for Open. The read-only callers (doctor, clone, box share, bug report) keep
// Open, which fails on a parse error.
func OpenOrReadOnly(path string) (s *Store, parseErr error, err error) {
	s = &Store{path: path, m: map[string]config.AgentConfig{}}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil, nil
		}
		return nil, nil, err
	}
	list, err := decode(path, b)
	if err != nil {
		parseErr = err
		s.unreadable = parseErr
		return s, parseErr, nil
	}
	for _, p := range list {
		s.m[p.Name] = p
	}
	s.disk, s.onDisk = sha256.Sum256(b), true
	return s, nil, nil
}

// List returns the stored profiles, sorted by name.
func (s *Store) List() []config.AgentConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sortedLocked()
}

// Get returns a stored profile by name.
func (s *Store) Get(name string) (config.AgentConfig, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.m[name]
	return p, ok
}

// Save creates or updates a profile and persists atomically. It returns
// ErrUnreadable on a read-only store and ErrChangedOnDisk, changing nothing, when the file was changed behind the
// store's back. A write that fails leaves the store as it was: the previous
// profile (or none) is put back, so Get does not serve a value that is not on
// disk and the next successful write does not persist it.
func (s *Store) Save(p config.AgentConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unreadable != nil {
		return fmt.Errorf("%w: %v", ErrUnreadable, s.unreadable)
	}
	if err := s.checkDiskLocked(); err != nil {
		return err
	}
	prev, had := s.m[p.Name]
	s.m[p.Name] = p
	if err := s.flushLocked(); err != nil {
		if had {
			s.m[p.Name] = prev
		} else {
			delete(s.m, p.Name)
		}
		return err
	}
	return nil
}

// Delete removes a profile and persists atomically. Like Save it returns
// ErrChangedOnDisk, changing nothing, when the file was changed behind the
// store's back, and a write that fails puts the profile back.
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unreadable != nil {
		return fmt.Errorf("%w: %v", ErrUnreadable, s.unreadable)
	}
	prev, ok := s.m[name]
	if !ok {
		return ErrNotFound
	}
	if err := s.checkDiskLocked(); err != nil {
		return err
	}
	delete(s.m, name)
	if err := s.flushLocked(); err != nil {
		s.m[name] = prev
		return err
	}
	return nil
}

// checkDiskLocked compares the file as it is now with what this process last
// read or wrote (the content, not the mtime: an identical rewrite is not a
// change). nil means they match, or the file is absent and was absent at Open;
// ErrChangedOnDisk means the bytes differ, the file appeared or it vanished.
func (s *Store) checkDiskLocked() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("store: read %s: %w", s.path, err)
		}
		if s.onDisk {
			return ErrChangedOnDisk
		}
		return nil
	}
	if !s.onDisk || sha256.Sum256(b) != s.disk {
		return ErrChangedOnDisk
	}
	return nil
}

func (s *Store) sortedLocked() []config.AgentConfig {
	out := make([]config.AgentConfig, 0, len(s.m))
	for _, p := range s.m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Store) flushLocked() error {
	var doc any = s.sortedLocked()
	if FormatVersion > 1 {
		doc = fileDoc{Version: FormatVersion, Profiles: s.sortedLocked()}
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil { // durable before the rename
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil { // atomic replace
		return err
	}
	// what checkDiskLocked expects to find on the next write
	s.disk, s.onDisk = sha256.Sum256(b), true
	if d, err := os.Open(filepath.Dir(s.path)); err == nil { // make the rename durable
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
