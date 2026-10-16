package account

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

const (
	accountsDir = "accounts" // <dataDir>/accounts
	binDir      = "bin"      // <dataDir>/bin (embedded statusLine script)
)

// Store persists non-default accounts in accounts.json and derives their dirs.
// Every write under dataDir goes through one os.Root so a planted symlink
// cannot redirect it outside.
type Store struct {
	mu           sync.Mutex
	root         *os.Root // dataDir
	dataDir      string
	jsonRel      string // accounts.json, relative to root
	accountsRoot string // <dataDir>/accounts (absolute, for reporting paths)
	defaultDir   string // the default account's config dir
	byName       map[string]Account
}

// Open loads accounts.json (path must live inside dataDir). A missing file is
// an empty store.
func Open(path, dataDir, defaultDir string) (*Store, error) {
	rel, err := filepath.Rel(dataDir, path)
	if err != nil || !filepath.IsLocal(rel) {
		return nil, fmt.Errorf("account: %s is not inside data dir %s", path, dataDir)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return nil, err
	}
	s := &Store{
		root:         root,
		dataDir:      dataDir,
		jsonRel:      rel,
		accountsRoot: filepath.Join(dataDir, accountsDir),
		defaultDir:   defaultDir,
		byName:       map[string]Account{},
	}
	b, err := root.ReadFile(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		root.Close()
		return nil, err
	}
	var list []Account
	if err := json.Unmarshal(b, &list); err != nil {
		root.Close()
		return nil, fmt.Errorf("account: parse %s: %w", path, err)
	}
	for _, a := range list {
		if err := a.Validate(); err != nil {
			root.Close()
			return nil, fmt.Errorf("account: %s: %w", path, err)
		}
		s.byName[a.Name] = a
	}
	return s, nil
}

// Close releases the dataDir root.
func (s *Store) Close() error { return s.root.Close() }

// Root is the account's own directory; ConfigDir is its claude/ child. The
// nesting keeps .claude.json per-account whether Claude Code writes it inside
// CLAUDE_CONFIG_DIR or beside it.
func (s *Store) Root(name string) string { return filepath.Join(s.accountsRoot, name) }

func (s *Store) resolvedLocked(a Account) Resolved {
	return Resolved{Account: a, ConfigDir: filepath.Join(s.Root(a.Name), "claude")}
}

func (s *Store) List() []Resolved {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Resolved{DefaultResolved(s.defaultDir)}
	names := make([]string, 0, len(s.byName))
	for n := range s.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, s.resolvedLocked(s.byName[n]))
	}
	return out
}

func (s *Store) Resolve(name string) (Resolved, error) {
	if name == "" || name == Default {
		return DefaultResolved(s.defaultDir), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byName[name]
	if !ok {
		return Resolved{}, fmt.Errorf("%w %q", ErrUnknown, name)
	}
	return s.resolvedLocked(a), nil
}

func (s *Store) Create(a Account) (Resolved, error) {
	if err := a.Validate(); err != nil {
		return Resolved{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byName[a.Name]; ok {
		return Resolved{}, fmt.Errorf("%w: %q", ErrExists, a.Name)
	}
	r := s.resolvedLocked(a)
	if err := s.provision(r); err != nil {
		return Resolved{}, err
	}
	s.byName[a.Name] = a
	if err := s.flushLocked(); err != nil {
		delete(s.byName, a.Name)
		return Resolved{}, err
	}
	return r, nil
}

func (s *Store) Update(name, tokenSecret string, inherit bool) (Resolved, error) {
	if name == "" || name == Default {
		return Resolved{}, errors.New("the default account cannot be edited")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byName[name]
	if !ok {
		return Resolved{}, fmt.Errorf("%w %q", ErrUnknown, name)
	}
	prev := a
	a.TokenSecret, a.InheritPersonal = tokenSecret, inherit
	if err := a.Validate(); err != nil {
		return Resolved{}, err
	}
	r := s.resolvedLocked(a)
	if err := s.provision(r); err != nil {
		return Resolved{}, err
	}
	s.byName[name] = a
	if err := s.flushLocked(); err != nil {
		s.byName[name] = prev
		return Resolved{}, err
	}
	return r, nil
}

// Delete unregisters an account. Its directory is left on disk: it holds the
// login and transcripts analytics still reads. The caller reports root.
func (s *Store) Delete(name string) (string, error) {
	if name == "" || name == Default {
		return "", errors.New("the default account cannot be deleted")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byName[name]
	if !ok {
		return "", fmt.Errorf("%w %q", ErrUnknown, name)
	}
	delete(s.byName, name)
	if err := s.flushLocked(); err != nil {
		s.byName[name] = a
		return "", err
	}
	return s.Root(name), nil
}

// flushLocked writes accounts.json atomically (tmp + fsync + rename), all
// through the dataDir root.
func (s *Store) flushLocked() error {
	list := make([]Account, 0, len(s.byName))
	for _, a := range s.byName {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.root, s.jsonRel, b, 0o600)
}

// writeFileAtomic replaces name (relative to root) with b: write a sibling tmp
// file, fsync it, rename over the target, then fsync the directory.
func writeFileAtomic(root *os.Root, name string, b []byte, perm os.FileMode) error {
	tmp := name + ".tmp"
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
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
	if err := root.Rename(tmp, name); err != nil { // atomic replace
		return err
	}
	if d, err := root.Open(filepath.Dir(name)); err == nil { // make the rename durable
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
