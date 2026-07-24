// Package store persists user-created profiles to a JSON file (behind a small
// interface so it can be swapped for SQLite later). Config-file profiles are
// separate, read-only seeds; this store holds only runtime-created/edited ones.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/culiops/culiops-agent-console/internal/config"
)

// ErrNotFound is returned by Delete/Get for an unknown profile.
var ErrNotFound = errors.New("store: profile not found")

// Store is a JSON-file-backed profile store, safe for concurrent use.
type Store struct {
	path string
	mu   sync.Mutex
	m    map[string]config.AgentConfig
}

// Open loads the store from path (a missing file is an empty store).
func Open(path string) (*Store, error) {
	s := &Store{path: path, m: map[string]config.AgentConfig{}}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	var list []config.AgentConfig
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("store: parse %s: %w", path, err)
	}
	for _, p := range list {
		s.m[p.Name] = p
	}
	return s, nil
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

// Save creates or updates a profile and persists atomically.
func (s *Store) Save(p config.AgentConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[p.Name] = p
	return s.flushLocked()
}

// Delete removes a profile and persists atomically.
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[name]; !ok {
		return ErrNotFound
	}
	delete(s.m, name)
	return s.flushLocked()
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
	b, err := json.MarshalIndent(s.sortedLocked(), "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path) // atomic replace
}
