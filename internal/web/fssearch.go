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
// cached as a flat list of relative paths and refreshed in the background once
// it goes stale. The staleness is deliberate: the palette opens files that
// already exist, while the file tree stays the live, authoritative browser.
const (
	indexTTL      = 60 * time.Second
	maxIndexPaths = 60000
	maxIndexDepth = 12
	indexBudget   = 2 * time.Second

	searchDefaultLimit = 50
	searchMaxLimit     = 200
)

// skipDirs are never descended into. Dot-prefixed directories are NOT skipped
// wholesale: this is a console for running coding agents, and ~/.claude,
// .claude/skills and .config hold exactly the files a user reaches for. Only
// machine-generated caches and credential stores are excluded.
var skipDirs = map[string]bool{
	"node_modules":  true,
	"dist":          true,
	"build":         true,
	"target":        true,
	"__pycache__":   true,
	".git":          true,
	".cache":        true,
	".venv":         true,
	".next":         true,
	".terraform":    true,
	".mypy_cache":   true,
	".pytest_cache": true,
	".gradle":       true,
	".tox":          true,
	".svn":          true,
	".hg":           true,
	// Credential stores: reachable by deliberate navigation in the file tree,
	// but never surfaced by typing three letters into a palette.
	".ssh":   true,
	".gnupg": true,
}

// indexEntry caches the lowercased path and basename offset alongside the path.
// Lowercasing 60k paths on every keystroke was the dominant cost otherwise.
type indexEntry struct {
	rel       string
	lower     string
	baseStart int
}

// fileIndex is a TTL-cached, bounded list of files under root. The bounds are
// fields rather than constants so tests can shrink them.
type fileIndex struct {
	root     string
	mu       sync.Mutex
	ttl      time.Duration
	maxPaths int
	maxDepth int
	budget   time.Duration

	entries   []indexEntry
	truncated bool
	built     time.Time
	building  bool
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

// setTTL is the race-free way to change the refresh interval (the background
// rebuild reads it under the same lock).
func (ix *fileIndex) setTTL(d time.Duration) {
	ix.mu.Lock()
	ix.ttl = d
	ix.mu.Unlock()
}

// snapshot returns the cached entries. A stale cache is served AS IS and
// refreshed in the background: holding the lock across a walk would stall the
// palette for up to the build budget once every TTL, and an aborted browser
// request does not cancel server-side work, so the stall would queue up behind
// itself. Only the very first call — when there is nothing to serve — blocks.
//
// The returned slice is replaced wholesale by a rebuild, never mutated, so
// callers may read it after releasing the lock.
func (ix *fileIndex) snapshot() ([]indexEntry, bool) {
	ix.mu.Lock()
	stale := ix.built.IsZero() || time.Since(ix.built) > ix.ttl
	first := ix.built.IsZero()

	if stale && !ix.building {
		ix.building = true
		root, maxPaths, maxDepth, budget := ix.root, ix.maxPaths, ix.maxDepth, ix.budget
		rebuild := func() {
			entries, truncated := walkTree(root, maxPaths, maxDepth, budget)
			ix.mu.Lock()
			ix.entries, ix.truncated, ix.built, ix.building = entries, truncated, time.Now(), false
			ix.mu.Unlock()
		}
		if first {
			ix.mu.Unlock()
			rebuild() // nothing to serve yet — this one call waits
			ix.mu.Lock()
		} else {
			go rebuild()
		}
	}
	entries, truncated := ix.entries, ix.truncated
	ix.mu.Unlock()
	return entries, truncated
}

// walkTree collects files under root, honouring every bound. Returns the entries
// and whether a bound cut the walk short.
func walkTree(root string, maxPaths, maxDepth int, budget time.Duration) ([]indexEntry, bool) {
	deadline := time.Now().Add(budget)
	entries := make([]indexEntry, 0, 1024)
	truncated := false
	n := 0

	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable directory must not abort the whole walk.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == root {
			return nil
		}
		// Check the clock every so often rather than on every entry.
		if n++; n%512 == 0 && time.Now().After(deadline) {
			truncated = true
			return filepath.SkipAll
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return nil
		}
		depth := len(strings.Split(rel, string(os.PathSeparator)))

		if d.IsDir() {
			if skipDirs[d.Name()] || depth >= maxDepth {
				return fs.SkipDir
			}
			return nil
		}
		if depth > maxDepth {
			return nil
		}
		// Symlinks are indexed so the containment check at result time is real;
		// WalkDir itself never follows them, so the walk cannot escape root.
		if !d.Type().IsRegular() && d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		if len(entries) >= maxPaths {
			truncated = true
			return filepath.SkipAll
		}
		slash := filepath.ToSlash(rel)
		lower := strings.ToLower(slash)
		entries = append(entries, indexEntry{rel: rel, lower: lower, baseStart: baseStartOf(lower)})
		return nil
	})
	return entries, truncated
}

// baseStartOf returns the byte offset of the basename within a slash-separated path.
func baseStartOf(p string) int {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return i + 1
	}
	return 0
}

// fuzzyScore reports whether query matches rel and, if so, how good the match is
// (higher is better). Convenience wrapper over fuzzyScoreLower for callers that
// have not precomputed the lowercased form.
func fuzzyScore(rel, query string) (int, bool) {
	l := strings.ToLower(filepath.ToSlash(rel))
	return fuzzyScoreLower(l, baseStartOf(l), strings.ToLower(query))
}

// fuzzyScoreLower scores an already-lowercased path against an already-lowercased
// query. Three tiers, best first:
//
//  1. the query appears contiguously in the BASENAME — scored from that
//     occurrence, with a bonus when it starts the basename;
//  2. the query appears contiguously anywhere in the path;
//  3. the query matches as a greedy left-to-right subsequence, penalised by
//     every character skipped between matches.
//
// Anchoring tier 1 on the basename occurrence matters: a purely greedy walk
// would bind the query's first character to a letter in a parent directory
// ("internal/app.js" for "app"), losing the basename bonus and charging gap
// penalties for the spelling of a folder the user never typed.
//
// Comparison is byte-wise. For non-ASCII paths a subsequence may align on
// partial runes, which can only ever over-match — acceptable in a fuzzy finder.
func fuzzyScoreLower(lrel string, baseStart int, lq string) (int, bool) {
	if lq == "" {
		return 0, false
	}
	base := lrel[baseStart:]

	if i := strings.Index(base, lq); i >= 0 {
		score := 300 - len(lrel)
		if i == 0 {
			score += 60
		}
		return score, true
	}
	if strings.Contains(lrel, lq) {
		return 200 - len(lrel), true
	}

	first, prev, gaps, qi := -1, -1, 0, 0
	for i := 0; i < len(lrel) && qi < len(lq); i++ {
		if lrel[i] != lq[qi] {
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
	if qi < len(lq) {
		return 0, false
	}
	score := 100 - len(lrel) - 2*gaps
	if first == baseStart {
		score += 60
	}
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
// bounds, so a short result set is never mistaken for "no such file".
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

	entries, truncated := s.searchIndex().snapshot()
	lq := strings.ToLower(q)
	hits := make([]searchHit, 0, limit)
	for _, e := range entries {
		score, ok := fuzzyScoreLower(e.lower, e.baseStart, lq)
		if !ok {
			continue
		}
		hits = append(hits, searchHit{Rel: e.rel, Score: score})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Rel < hits[j].Rel
	})

	// Resolve and containment-check only what will actually be returned: a
	// symlink inside $HOME pointing outside it must never be surfaced, exactly
	// as /api/fs/list and /api/fs/read already guarantee. $HOME is resolved once
	// rather than per hit.
	rhome, err := filepath.EvalSymlinks(s.Home)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "home unresolvable")
		return
	}
	out := make([]searchHit, 0, limit)
	for _, h := range hits {
		if len(out) >= limit {
			break
		}
		abs := filepath.Join(s.Home, h.Rel)
		resolved, err := containedUnder(rhome, abs)
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
