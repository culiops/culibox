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
	// maxPerRootPaths stops any single top-level subtree from consuming the
	// whole budget. The walk is a lexical DFS, so without this the directories
	// that happen to sort first decide what is findable: on a real home dir
	// ~/.local alone holds 100k files and the index filled up before reaching a
	// single one of the user's repos.
	maxPerRootPaths = 10000

	// Directory bounds are separate from the file bounds on purpose. Measured on
	// a real home directory: 16 566 directories at depth 12, against ~60 000
	// files. Sharing one budget would let the files starve the directories.
	maxIndexDirs = 20000
	// maxPerRootDirs is the directory twin of maxPerRootPaths, and it binds: one
	// repo on the author's box holds 7 741 directories. Without it a global cap
	// degrades alphabetically, because the walk is a lexical DFS — the exact
	// unfairness maxPerRootPaths exists to prevent.
	maxPerRootDirs = 4000

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
	// Package and tool caches — machine-managed, and enormous.
	".local":         true,
	".npm":           true,
	".nvm":           true,
	".cargo":         true,
	".rustup":        true,
	".gem":           true,
	".pyenv":         true,
	".rbenv":         true,
	".vscode-server": true,
	// Credential stores. This is NOT a security boundary — /api/fs/list and
	// /api/fs/read already reach all of $HOME behind the same basic auth, and no
	// blocklist could be exhaustive. It only keeps secrets from surfacing by
	// accident when someone types three letters into a palette; deliberate
	// navigation in the file tree still finds them.
	".ssh":   true,
	".gnupg": true,
	".aws":   true,
	".kube":  true,
}

// skipFiles are excluded by filename anywhere in the tree, for the same
// accident-reduction reason as the credential directories above.
var skipFiles = map[string]bool{
	".netrc":           true,
	".git-credentials": true,
	".npmrc":           true,
	".pypirc":          true,
}

// skipRelPaths are skipped by their position, not their name — the same name
// elsewhere is a perfectly good directory to search. Paths are slash-separated
// and relative to the index root.
var skipRelPaths = map[string]bool{
	// Claude Code's own machine-managed state. ~/.claude/{skills,context,
	// settings.json,CLAUDE.md} are exactly what a user wants to open, so the
	// parent must stay indexed.
	".claude/projects":        true,
	".claude/file-history":    true,
	".claude/security":        true,
	".claude/plugins":         true,
	".claude/paste-cache":     true,
	".claude/shell-snapshots": true,
	"go/pkg":                  true, // module cache; go/bin stays
	".config/gh":              true, // credential stores under an otherwise
	".config/rclone":          true, // useful, indexed directory
}

// searchKind selects which of the index's two result sets a caller wants.
type searchKind int

const (
	kindFiles searchKind = iota
	kindDirs
)

// skipDirRecord names directories that are never worth offering as a workspace
// but whose FILES must stay findable. It gates recording only — the walk still
// descends. Putting these in skipDirs instead would delete every file under a
// Laravel vendor/ from the command palette.
var skipDirRecord = map[string]bool{
	"vendor": true,
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
	root           string
	mu             sync.Mutex
	ttl            time.Duration
	maxPaths       int
	maxPerRoot     int
	maxDirs        int
	maxPerRootDirs int
	maxDepth       int
	budget         time.Duration

	entries       []indexEntry
	truncated     bool
	dirs          []indexEntry
	dirsTruncated bool
	built         time.Time
	building      bool
}

func newFileIndex(root string) *fileIndex {
	return &fileIndex{
		root:           root,
		ttl:            indexTTL,
		maxPaths:       maxIndexPaths,
		maxPerRoot:     maxPerRootPaths,
		maxDirs:        maxIndexDirs,
		maxPerRootDirs: maxPerRootDirs,
		maxDepth:       maxIndexDepth,
		budget:         indexBudget,
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
func (ix *fileIndex) snapshot(k searchKind) ([]indexEntry, bool) {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	if ix.built.IsZero() {
		// Nothing to serve. Build with the lock HELD so every concurrent caller
		// waits for real results: releasing it here would let the requests that
		// arrive during the first walk answer with an empty index and
		// truncated=false, which reads as "no such file" — exactly the confusion
		// that field exists to prevent. Costs one stall per process, on the very
		// first search. The deferred Unlock also covers a panic in the walk.
		ix.entries, ix.truncated, ix.dirs, ix.dirsTruncated = walkTree(ix.bounds())
		ix.built = time.Now()
		if k == kindDirs {
			return ix.dirs, ix.dirsTruncated
		}
		return ix.entries, ix.truncated
	}

	if time.Since(ix.built) > ix.ttl && !ix.building {
		ix.building = true
		b := ix.bounds()
		go func() {
			// Clearing `building` is deferred so a panic in the walk cannot wedge
			// the flag on and leave the index frozen for the process's lifetime.
			defer func() {
				ix.mu.Lock()
				ix.building = false
				ix.mu.Unlock()
			}()
			entries, truncated, dirs, dirsTruncated := walkTree(b)
			ix.mu.Lock()
			ix.entries, ix.truncated, ix.dirs, ix.dirsTruncated, ix.built = entries, truncated, dirs, dirsTruncated, time.Now()
			ix.mu.Unlock()
		}()
	}
	if k == kindDirs {
		return ix.dirs, ix.dirsTruncated
	}
	return ix.entries, ix.truncated
}

// bounds snapshots the limits for a walk. Caller holds mu, so the background
// rebuild never reads a field another goroutine is writing.
func (ix *fileIndex) bounds() walkBounds {
	return walkBounds{ix.root, ix.maxPaths, ix.maxPerRoot, ix.maxDirs, ix.maxPerRootDirs, ix.maxDepth, ix.budget}
}

// walkBounds is every limit a walk honours, captured under the index lock so the
// background rebuild never reads a field that a test (or a future setter) is
// concurrently writing.
type walkBounds struct {
	root           string
	maxPaths       int
	maxPerRoot     int
	maxDirs        int
	maxPerRootDirs int
	maxDepth       int
	budget         time.Duration
}

// walkTree collects files AND directories under root in one pass, each with its
// own bounds and its own truncation flag. One walk, two result sets: the walk
// already visits every directory, so recording them costs no extra I/O — but
// descending past the file fairness prune does make the walk itself longer.
func walkTree(b walkBounds) (files []indexEntry, filesTrunc bool, dirs []indexEntry, dirsTrunc bool) {
	deadline := time.Now().Add(b.budget)
	files = make([]indexEntry, 0, 1024)
	dirs = make([]indexEntry, 0, 256)
	perRoot := make(map[string]int)
	perRootDirs := make(map[string]int)
	n := 0

	filepath.WalkDir(b.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable directory must not abort the whole walk.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == b.root {
			return nil
		}
		// Check the clock every so often rather than on every entry.
		if n++; n%512 == 0 && time.Now().After(deadline) {
			filesTrunc, dirsTrunc = true, true
			return filepath.SkipAll
		}
		rel, relErr := filepath.Rel(b.root, p)
		if relErr != nil {
			return nil
		}
		slash := filepath.ToSlash(rel)
		parts := strings.Split(slash, "/")
		top := parts[0]

		if d.IsDir() {
			if skipDirs[d.Name()] || skipRelPaths[slash] || len(parts) >= b.maxDepth {
				return fs.SkipDir
			}
			// A CONJUNCTION, not the old single test: skip the subtree only once
			// BOTH budgets for this top-level are spent. Pruning on the file
			// budget alone would take the directories with it, and the subtrees
			// that spend their file budget are the big repos — the ones most
			// worth searching.
			fileBudgetSpent := perRoot[top] >= b.maxPerRoot
			dirBudgetSpent := perRootDirs[top] >= b.maxPerRootDirs
			if fileBudgetSpent {
				filesTrunc = true
			}
			if dirBudgetSpent {
				dirsTrunc = true
			}
			if fileBudgetSpent && dirBudgetSpent {
				return fs.SkipDir
			}
			// skipDirRecord is keyed on a single path element, so a directory
			// nested under a skip-recorded ancestor (app/vendor/lib under
			// "vendor") is checked by walking every segment, not just d.Name() —
			// otherwise only the named directory itself would be suppressed and
			// its subtree would leak back in one level down.
			underSkipRecord := false
			for _, part := range parts {
				if skipDirRecord[part] {
					underSkipRecord = true
					break
				}
			}
			if !dirBudgetSpent && !underSkipRecord {
				if len(dirs) >= b.maxDirs {
					dirsTrunc = true
				} else {
					perRootDirs[top]++
					lower := strings.ToLower(slash)
					dirs = append(dirs, indexEntry{rel: rel, lower: lower, baseStart: baseStartOf(lower)})
				}
			}
			return nil
		}
		if len(parts) > b.maxDepth || skipFiles[d.Name()] {
			return nil
		}
		// Symlinks are indexed so the containment check at result time is real;
		// WalkDir itself never follows them, so the walk cannot escape root.
		if !d.Type().IsRegular() && d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		if len(files) >= b.maxPaths {
			filesTrunc = true
			return filepath.SkipAll
		}
		if perRoot[top] >= b.maxPerRoot {
			filesTrunc = true
			return nil
		}
		perRoot[top]++
		lower := strings.ToLower(slash)
		files = append(files, indexEntry{rel: rel, lower: lower, baseStart: baseStartOf(lower)})
		return nil
	})
	return
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

	entries, truncated := s.searchIndex().snapshot(kindFiles)
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
