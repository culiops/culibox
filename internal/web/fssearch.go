package web

import (
	"io/fs"
	"net/http"
	"os"
	"path"
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

	// Directory bounds are separate from the file bounds on purpose: sharing one
	// budget would let the files starve the directories. The directory index
	// records PROJECT ROOTS, not every folder (see markerFiles), so the set
	// these bound is small — measured on a real home directory, 91 directories
	// against ~60 000 files.
	//
	// Both caps are therefore RETAINED BUT VESTIGIAL. Do NOT lower them to
	// "fit" the new size. Lowering makes dirBudgetSpent reachable, and a spent
	// directory budget contributes to the fairness prune's fs.SkipDir below —
	// which stops the walk VISITING markers deeper in that subtree and so
	// silently loses whole projects, in an order that depends on lexical DFS.
	// That order-dependent project loss is the exact failure this design exists
	// to eliminate.
	maxIndexDirs = 20000
	// maxPerRootDirs is the directory twin of maxPerRootPaths, and carries the
	// same warning: headroom, not a binding limit. It exists for the reason
	// maxPerRootPaths does — the walk is a lexical DFS, so without a per-root
	// cap one huge subtree that sorts first can crowd out everything after it.
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

// markerFiles name a directory as a project root: a directory that DIRECTLY
// contains one of these is recorded in the directory index whatever its depth
// and whether or not it is hidden. The picker answers "which project am I in";
// the file TREE goes deeper, so `<repo>/internal` is reached by picking the
// repo and clicking down, not by searching for it. Measured on a real home
// directory, this takes the offered set from 10 049 directories to 91.
//
// composer.json is deliberately absent: it accounts for zero directories here —
// every Composer project also carries .git, and the rest sit inside a vendor/,
// already suppressed by skipDirRecord.
var markerFiles = map[string]bool{
	".git":           true,
	"go.mod":         true,
	"package.json":   true,
	"Cargo.toml":     true,
	"pyproject.toml": true,
}

// skipDirRecord names a directory whose ENTIRE SUBTREE is never worth
// offering as a workspace — the match suppresses recording for it and every
// directory nested inside it, not just the named directory itself — but whose
// FILES must stay findable. It gates recording only; the walk still descends.
// Putting these in skipDirs instead would delete every file under a Laravel
// vendor/ from the command palette — or, for "sdk", 14 854 Go stdlib files.
//
// EVERY KEY IS A SINGLE PATH ELEMENT. The match runs one segment at a time
// (see the underSkipRecord loop in emit), so a multi-segment key such as
// "code-server/config/extensions" can never fire — it is dead config that
// reads as a working rule.
//
// The counts below are directories removed AFTER the project rule, measured on
// a real home directory. Measure before adding another key.
var skipDirRecord = map[string]bool{
	"vendor": true,
	// The retired code-server install (2026-07-26). 26 rows — 20% of the
	// result set, and the whole of what `q=static` used to return.
	"code-server": true,
	// Go SDK trees: the toolchain ships 21 go.mod files, so without this the
	// project rule reintroduces the stdlib paths that made `q=internal`
	// useless. A future <repo>/sdk/ package becomes unpickable; zero
	// collateral today, and accepted over skipRelPaths, which would have
	// deleted the stdlib files from the palette as well. 22 rows.
	"sdk": true,
	// Shell/tmux plugin checkouts — each plugin is its own git clone. 9 rows.
	".zinit": true,
	".tmux":  true,
	// OpenNext build output; the bundles carry their own package.json. 7 rows.
	".open-next": true,
	// Worktrees are alternate checkouts of a repo already listed. 6 rows.
	".worktrees": true,
	"worktrees":  true,
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
	seenDirs := make(map[string]bool)
	n := 0

	// emit records one directory (slash-separated, relative to root) and owns
	// EVERY bound the directory set has. Both admission rules funnel through it,
	// so the two call sites cannot drift on the bounds — and, crucially, so the
	// dedupe set is shared. The common collision is rule 1 × rule 2: every
	// top-level repo is admitted once as a non-hidden depth-1 child and again
	// when its .git is visited (~30 directories on a real home). Two separate
	// sets would list each of them twice.
	emit := func(slashRel string) {
		if seenDirs[slashRel] {
			return
		}
		parts := strings.Split(slashRel, "/")
		// skipDirRecord is keyed on a single path element, so a directory
		// nested under a skip-recorded ancestor (app/vendor/lib under "vendor")
		// is checked by walking every segment, not just the last — otherwise
		// only the named directory itself would be suppressed and its subtree
		// would leak back in one level down.
		for _, part := range parts {
			if skipDirRecord[part] {
				return
			}
		}
		if len(parts) >= b.maxDepth {
			return
		}
		top := parts[0]
		if perRootDirs[top] >= b.maxPerRootDirs {
			dirsTrunc = true
			return
		}
		if len(dirs) >= b.maxDirs {
			dirsTrunc = true
			return
		}
		seenDirs[slashRel] = true
		perRootDirs[top]++
		lower := strings.ToLower(slashRel)
		dirs = append(dirs, indexEntry{rel: filepath.FromSlash(slashRel), lower: lower, baseStart: baseStartOf(lower)})
	}

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

		// Rule 2: a marker names its PARENT a project root. This sits above the
		// d.IsDir() split deliberately — .git is in skipDirs, so below the
		// fs.SkipDir return in that branch the single most important marker
		// would never be observed at all. The same one position is also above
		// the depth return and above the per-root FILE cap, either of which
		// would otherwise swallow a marker in a big or deep subtree. One
		// insertion, not three constraints.
		//
		// Emitting the PARENT rather than deciding a directory's project-ness
		// when the directory itself is visited is forced by WalkDir's order,
		// which fully descends a subtree before reaching a later sibling:
		// repo → repo/aaa → repo/aaa/deep → repo/go.mod → repo/zzz. Deciding at
		// visit time is impossible; deferring the decision to the end of the
		// subtree loses the repo whenever a budget abort fires mid-subtree,
		// worst for the 41% of the set that are package.json-only, since "p"
		// sorts late.
		if markerFiles[d.Name()] {
			// A marker in $HOME itself would emit ".", which is the root, not a
			// project inside it.
			if parent := path.Dir(slash); parent != "." {
				emit(parent)
			}
		}

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
			// Rule 1: a direct, NON-HIDDEN child of $HOME is offered without
			// needing a marker, mirroring listWorkspaces — plus .config by
			// name, which is hidden but is where a user's own configuration
			// lives. Rule 2 deliberately overrides this hidden filter for
			// everything else: a hidden directory carrying a marker is
			// recorded, which is how ~/.claude (its own git checkout) arrives.
			if len(parts) == 1 && (!strings.HasPrefix(slash, ".") || slash == ".config") {
				emit(slash)
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
			// SkipAll ends the WalkDir call outright — the directory walk dies
			// with it. Flagging only filesTrunc here would leave dirsTrunc false
			// while every directory lexically after this point is silently gone.
			filesTrunc, dirsTrunc = true, true
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

	// Anything that is not exactly "dir" means files. The command palette sends
	// no kind at all and must keep getting byte-identical results.
	kind := kindFiles
	if r.URL.Query().Get("kind") == "dir" {
		kind = kindDirs
	}
	entries, truncated := s.searchIndex().snapshot(kind)
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
		info, err := os.Stat(resolved)
		if err != nil {
			continue
		}
		// The index says what it recorded; the stat says what is on disk NOW.
		// They disagree whenever something was created, deleted or replaced
		// since the last rebuild, which is exactly what the TTL allows.
		if info.IsDir() != (kind == kindDirs) {
			continue
		}
		h.Path = abs
		out = append(out, h)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out, "truncated": truncated})
}
