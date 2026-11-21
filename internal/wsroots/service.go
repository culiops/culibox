package wsroots

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// MaxRoots bounds the list: it bounds the index cost, and nobody picks 32
// data folders by hand.
const MaxRoots = 32

// touchEvery is the least gap between two last_used writes for one root.
const touchEvery = time.Hour

// Errors Add and Remove return. ErrCorrupt (store.go) is the other one.
var (
	// ErrCovered: home or an existing root already reaches the folder.
	ErrCovered = errors.New("that folder is already reachable")
	// ErrCap: the list is full (MaxRoots).
	ErrCap = errors.New("too many workspace roots")
	// ErrNotDir: the path is missing, not absolute, or not a directory.
	ErrNotDir = errors.New("not a folder")
	// ErrNotFound: Remove found no such root.
	ErrNotFound = errors.New("no such workspace root")
)

// RefusedError is a folder that can never be a root; Reason is plain words
// naming only a class of folder (see Refusal).
type RefusedError struct{ Reason string }

func (e *RefusedError) Error() string { return e.Reason }

// AuditFunc records a change before it is saved. action is "created" or
// "deleted"; a non-nil error stops the change (fail closed).
type AuditFunc func(action, path, actor string) error

// Service is the audited root list. Every read and write re-reads the file
// when another process changed it, so a hand edit is seen without a restart.
// A damaged file is never overwritten: every change then returns ErrCorrupt.
type Service struct {
	dataDir string
	home    string // symlinks resolved when possible
	audit   AuditFunc
	now     func() time.Time

	mu      sync.Mutex
	loaded  bool
	list    []Root
	corrupt error
	file    fileStamp // workspace-roots.json as last read or written; zero = none
}

// NewService returns a Service over dataDir. home is the user's home; it is
// resolved once (EvalSymlinks, else Clean) because home always covers.
// audit and now may be nil.
func NewService(dataDir, home string, audit AuditFunc, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	if home != "" {
		if real, err := filepath.EvalSymlinks(home); err == nil {
			home = real
		} else {
			home = filepath.Clean(home)
		}
	}
	return &Service{dataDir: dataDir, home: home, audit: audit, now: now}
}

// fileStamp identifies one version of workspace-roots.json.
type fileStamp struct {
	mtime time.Time
	size  int64
	ino   uint64
	ok    bool
}

func statFile(dataDir string) fileStamp {
	fi, err := os.Lstat(filepath.Join(dataDir, FileName))
	if err != nil {
		return fileStamp{}
	}
	st := fileStamp{mtime: fi.ModTime(), size: fi.Size(), ok: true}
	if sys, ok := fi.Sys().(*syscall.Stat_t); ok {
		st.ino = uint64(sys.Ino)
	}
	return st
}

// reload re-reads the file on first use and whenever its mtime, size or inode
// differ from what this service last read or wrote. The caller holds s.mu.
// A missing file is an empty list; a damaged one makes the service refuse
// writes until it changes.
func (s *Service) reload() {
	now := statFile(s.dataDir)
	if s.loaded && now == s.file {
		return
	}
	list, _, err := Load(s.dataDir)
	switch {
	case errors.Is(err, ErrCorrupt):
		s.list, s.corrupt = nil, err
	case err != nil:
		// Unreadable for now: keep what we had, and read again next time.
		if !s.loaded {
			s.list, s.corrupt, s.loaded = nil, nil, true
		}
		return
	default:
		s.list, s.corrupt = list, nil
	}
	s.loaded, s.file = true, now
}

// List is a copy of the saved roots (home is not in it). A damaged file
// returns nil, ErrCorrupt.
func (s *Service) List() ([]Root, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reload()
	if s.corrupt != nil {
		return nil, s.corrupt
	}
	return append([]Root{}, s.list...), nil
}

// Paths is the added root paths only (home is not included); empty on error.
func (s *Service) Paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reload()
	out := make([]string, 0, len(s.list))
	for _, r := range s.list {
		out = append(out, r.Path)
	}
	return out
}

// Covering is the longest of home and the roots that is abs or contains it,
// or "". On a damaged file home alone covers.
func (s *Service) Covering(abs string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reload()
	return s.coveringLocked(abs)
}

func (s *Service) coveringLocked(abs string) string {
	abs = filepath.Clean(abs)
	best := ""
	consider := func(p string) {
		p = filepath.Clean(p)
		if within(abs, p) && len(p) > len(best) {
			best = p
		}
	}
	if s.home != "" {
		consider(s.home)
	}
	for _, r := range s.list {
		consider(r.Path)
	}
	return best
}

// Add makes abs a root. It resolves symlinks and requires a directory, then
// refuses, in order: home itself (ErrCovered), a forbidden folder
// (*RefusedError), a folder home or a root already reaches (ErrCovered), a
// full list (ErrCap). The audit is recorded before the save; if it fails,
// nothing is saved.
func (s *Service) Add(abs, actor string) (Root, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reload()
	if s.corrupt != nil {
		return Root{}, s.corrupt
	}
	if !filepath.IsAbs(abs) {
		return Root{}, fmt.Errorf("%w: %q is not an absolute path", ErrNotDir, abs)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return Root{}, fmt.Errorf("%w: %v", ErrNotDir, err)
	}
	fi, err := os.Stat(real)
	if err != nil {
		return Root{}, fmt.Errorf("%w: %v", ErrNotDir, err)
	}
	if !fi.IsDir() {
		return Root{}, fmt.Errorf("%w: %s", ErrNotDir, real)
	}
	real = filepath.Clean(real)
	// Home first: the refusal rule calls home "your credential folders",
	// but home is already reachable, so the honest answer is "covered".
	if s.home != "" && real == s.home {
		return Root{}, ErrCovered
	}
	if reason := Refusal(real, Env{Home: s.home, DataDir: s.dataDir}); reason != "" {
		return Root{}, &RefusedError{Reason: reason}
	}
	if s.coveringLocked(real) != "" {
		return Root{}, ErrCovered
	}
	if len(s.list) >= MaxRoots {
		return Root{}, ErrCap
	}
	root := Root{Path: real, Added: s.now(), AddedBy: actor}
	next := append(append(make([]Root, 0, len(s.list)+1), s.list...), root)
	if err := s.commitLocked("created", real, actor, next); err != nil {
		return Root{}, err
	}
	return root, nil
}

// Remove drops the root at path (as stored, or a spelling that resolves to
// it). The audit is recorded before the save.
func (s *Service) Remove(path, actor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reload()
	if s.corrupt != nil {
		return s.corrupt
	}
	idx := s.indexLocked(filepath.Clean(path))
	if idx < 0 {
		if real, err := filepath.EvalSymlinks(path); err == nil {
			idx = s.indexLocked(filepath.Clean(real))
		}
	}
	if idx < 0 {
		return ErrNotFound
	}
	next := make([]Root, 0, len(s.list)-1)
	next = append(next, s.list[:idx]...)
	next = append(next, s.list[idx+1:]...)
	return s.commitLocked("deleted", s.list[idx].Path, actor, next)
}

func (s *Service) indexLocked(path string) int {
	for i, r := range s.list {
		if r.Path == path {
			return i
		}
	}
	return -1
}

// Touch records that a session started under abs: it sets last_used on the
// covering root, at most once per hour, and never for home. It is not audited
// and a failed write is ignored (last_used is only a hint).
func (s *Service) Touch(abs string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reload()
	if s.corrupt != nil {
		return
	}
	cover := s.coveringLocked(abs)
	idx := s.indexLocked(cover)
	if cover == "" || idx < 0 {
		return // not covered, or only home covers it
	}
	now := s.now()
	if last := s.list[idx].LastUsed; !last.IsZero() && now.Sub(last) < touchEvery {
		return
	}
	next := append([]Root{}, s.list...)
	next[idx].LastUsed = now
	if err := Save(s.dataDir, next); err != nil {
		return
	}
	s.list, s.file = next, statFile(s.dataDir)
}

// commitLocked audits, then saves next, then adopts it. A failed audit or
// save leaves the saved list as it was. The caller holds s.mu.
func (s *Service) commitLocked(action, path, actor string, next []Root) error {
	if s.audit != nil {
		if err := s.audit(action, path, actor); err != nil {
			return err
		}
	}
	if err := Save(s.dataDir, next); err != nil {
		return err
	}
	s.list, s.file = next, statFile(s.dataDir)
	return nil
}
