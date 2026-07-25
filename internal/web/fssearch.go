package web

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Fuzzy file search for the command palette. Walking $HOME on every keystroke is
// unusable on a home directory holding a dozen repos, so the walk result is
// cached as a flat list of relative paths and refreshed lazily once it goes
// stale. The staleness is deliberate: the palette opens files that already
// exist, while the file tree stays the live, authoritative browser.
const (
	indexTTL      = 60 * time.Second
	maxIndexPaths = 60000
	maxIndexDepth = 12
	indexBudget   = 2 * time.Second

	searchDefaultLimit = 50
	searchMaxLimit     = 200
)

// noiseDirs are never descended into. Directories whose name starts with "."
// are skipped by a separate rule, which is what excludes .git.
var noiseDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	"target":       true,
	"__pycache__":  true,
}

// fileIndex is a TTL-cached, bounded list of file paths relative to root. The
// bounds are fields rather than constants so tests can shrink them.
type fileIndex struct {
	root     string
	ttl      time.Duration
	maxPaths int
	maxDepth int
	budget   time.Duration

	mu        sync.Mutex
	paths     []string
	truncated bool
	built     time.Time
}

func newFileIndex(root string) *fileIndex {
	return &fileIndex{
		root:     root,
		ttl:      indexTTL,
		maxPaths: maxIndexPaths,
		maxDepth: maxIndexDepth,
		budget:   indexBudget,
	}
}

// snapshot returns the cached paths, rebuilding first if the cache is empty or
// older than ttl. The returned slice is never mutated afterwards, so callers may
// read it without holding the lock.
func (ix *fileIndex) snapshot() ([]string, bool) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.built.IsZero() || time.Since(ix.built) > ix.ttl {
		ix.build()
	}
	return ix.paths, ix.truncated
}

// build walks root, honouring every bound. It replaces paths/truncated wholesale
// so a partial walk can never be mixed with a previous one. Caller holds mu.
func (ix *fileIndex) build() {
	deadline := time.Now().Add(ix.budget)
	paths := make([]string, 0, 1024)
	truncated := false
	n := 0

	filepath.WalkDir(ix.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable directory must not abort the whole walk.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == ix.root {
			return nil
		}
		// Check the clock every so often rather than on every entry.
		if n++; n%512 == 0 && time.Now().After(deadline) {
			truncated = true
			return filepath.SkipAll
		}
		name := d.Name()
		rel, relErr := filepath.Rel(ix.root, p)
		if relErr != nil {
			return nil
		}
		depth := len(strings.Split(rel, string(os.PathSeparator)))

		if d.IsDir() {
			if strings.HasPrefix(name, ".") || noiseDirs[name] || depth >= ix.maxDepth {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || depth > ix.maxDepth {
			return nil
		}
		// Symlinks are indexed so the containment check at result time is real;
		// WalkDir itself never follows them, so the walk cannot escape root.
		if !d.Type().IsRegular() && d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		if len(paths) >= ix.maxPaths {
			truncated = true
			return filepath.SkipAll
		}
		paths = append(paths, rel)
		return nil
	})

	ix.paths = paths
	ix.truncated = truncated
	ix.built = time.Now()
}

// fuzzyScore reports whether query matches rel as a case-insensitive subsequence
// and, if so, how good the match is. Higher is better. The weights favour, in
// order: a contiguous hit in the basename, a hit starting the basename, a
// contiguous hit anywhere, short paths, and few gaps between matched characters.
func fuzzyScore(rel, query string) (int, bool) {
	if query == "" {
		return 0, false
	}
	lrel := strings.ToLower(rel)
	lq := strings.ToLower(query)
	rrel := []rune(lrel)
	rq := []rune(lq)

	first, prev, gaps, qi := -1, -1, 0, 0
	for i := 0; i < len(rrel) && qi < len(rq); i++ {
		if rrel[i] != rq[qi] {
			continue
		}
		if first < 0 {
			first = i
		} else if i != prev+1 {
			gaps += i - prev - 1
		}
		prev = i
		qi++
	}
	if qi < len(rq) {
		return 0, false
	}

	// baseStart is a rune index, so it is comparable with `first`.
	baseStart := 0
	if i := strings.LastIndex(lrel, "/"); i >= 0 {
		baseStart = len([]rune(lrel[:i+1]))
	}
	base := string(rrel[baseStart:])

	score := 100
	switch {
	case strings.Contains(base, lq):
		score += 200
	case strings.Contains(lrel, lq):
		score += 100
	}
	if first == baseStart {
		score += 60
	}
	score -= len(rrel)
	score -= 2 * gaps
	return score, true
}

type searchHit struct {
	Path  string `json:"path"`
	Rel   string `json:"rel"`
	Score int    `json:"score"`
}

// searchIndex lazily creates this server's file index.
func (s *Server) searchIndex() *fileIndex {
	s.idxMu.Lock()
	defer s.idxMu.Unlock()
	if s.idx == nil {
		s.idx = newFileIndex(s.Home)
	}
	return s.idx
}

// handleFsSearch serves GET /api/fs/search?q=&limit= — fuzzy-matched files under
// $HOME, best first. `truncated` reports that the underlying index hit one of its
// bounds, so an empty or short result set is never mistaken for "no such file".
func (s *Server) handleFsSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeErr(w, http.StatusBadRequest, "missing q")
		return
	}
	limit := searchDefaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		// A malformed limit falls back to the default rather than failing the
		// request — the palette must degrade, not break, on a bad query string.
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > searchMaxLimit {
		limit = searchMaxLimit
	}

	paths, truncated := s.searchIndex().snapshot()
	hits := make([]searchHit, 0, limit)
	for _, rel := range paths {
		score, ok := fuzzyScore(filepath.ToSlash(rel), q)
		if !ok {
			continue
		}
		hits = append(hits, searchHit{Rel: rel, Score: score})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Rel < hits[j].Rel
	})

	// Resolve and containment-check only what will actually be returned: a
	// symlink inside $HOME pointing outside it must never be surfaced, exactly
	// as /api/fs/list and /api/fs/read already guarantee.
	out := make([]searchHit, 0, limit)
	for _, h := range hits {
		if len(out) >= limit {
			break
		}
		abs := filepath.Join(s.Home, h.Rel)
		resolved, err := containedPath(s.Home, abs)
		if err != nil {
			continue
		}
		if info, err := os.Stat(resolved); err != nil || info.IsDir() {
			continue
		}
		h.Path = abs
		out = append(out, h)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out, "truncated": truncated})
}
