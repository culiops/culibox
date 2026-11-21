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
	// preferIndexBudget bounds a NEW prefer-root index's first build. The
	// client aborts a palette fetch after PAL_FETCH_MS (4s); the very first
	// search after boot can trigger the home index's first build (up to
	// indexBudget) AND a brand-new prefer index's first build back to back,
	// synchronously, in the same request — two indexBudget-sized builds sum to
	// exactly the client's timeout. A separate, smaller budget for the prefer
	// index keeps that worst case under it.
	preferIndexBudget = 1 * time.Second
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
	// loses whole projects — reported via dirsTruncated, but lost all the
	// same — in an order that depends on lexical DFS.
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

	// maxPreferIndexes bounds the per-directory indexes behind ?prefer=. Each
	// holds up to maxIndexPaths entries; an operator moves between a handful of
	// repos, not dozens.
	maxPreferIndexes = 4
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
// (see the per-segment loop at the top of emit), so a multi-segment key such as
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
	root string
	// relPrefix is root's slash path relative to $HOME ("" for the home index
	// itself), so skipRelPaths — which are home-relative — still apply to an
	// index rooted deeper, e.g. at ~/.claude.
	relPrefix string
	// noRelSkip turns skipRelPaths off: they are $HOME-relative, so an index
	// rooted at an added workspace root (or a folder under one) never applies them.
	noRelSkip      bool
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
	builds        int // walks started by runBuild (tests count them)
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

// claimBuild marks a build as in flight and reports whether the caller owns it
// (false: one is already running). Claiming under the lock, BEFORE the goroutine
// is spawned, is what keeps two requests from both starting a walk.
func (ix *fileIndex) claimBuild() bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.building {
		return false
	}
	ix.building = true
	return true
}

// startBuild is claimBuild for an index that has never built: it also fails once
// a build has landed, checked under the same lock, so a request that saw "not
// ready" an instant before the build finished cannot start a second walk.
func (ix *fileIndex) startBuild() bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.building || !ix.built.IsZero() {
		return false
	}
	ix.building = true
	return true
}

// refresh walks the tree and replaces the cache, unless a build is already in
// flight. Unlike snapshot's first call it does NOT hold the lock across the
// walk, so ready() stays answerable while it runs. Run it in a goroutine.
func (ix *fileIndex) refresh() {
	if ix.claimBuild() {
		ix.runBuild()
	}
}

// runBuild is the walk of a build the caller has claimed (claimBuild).
func (ix *fileIndex) runBuild() {
	// Clearing `building` is deferred so a panic in the walk cannot wedge it on.
	defer func() {
		ix.mu.Lock()
		ix.building = false
		ix.mu.Unlock()
	}()
	ix.mu.Lock()
	b := ix.bounds()
	ix.builds++
	ix.mu.Unlock()
	entries, truncated, dirs, dirsTruncated := walkTree(b)
	ix.mu.Lock()
	ix.entries, ix.truncated, ix.dirs, ix.dirsTruncated, ix.built = entries, truncated, dirs, dirsTruncated, time.Now()
	ix.mu.Unlock()
}

// ready reports whether a build has completed at least once.
func (ix *fileIndex) ready() bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return !ix.built.IsZero()
}

// bounds snapshots the limits for a walk. Caller holds mu, so the background
// rebuild never reads a field another goroutine is writing.
func (ix *fileIndex) bounds() walkBounds {
	return walkBounds{ix.root, ix.maxPaths, ix.maxPerRoot, ix.maxDirs, ix.maxPerRootDirs, ix.maxDepth, ix.budget, ix.relPrefix, ix.noRelSkip}
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
	relPrefix      string
	noRelSkip      bool
}

// walkTree collects files AND directories under root in one pass, each with its
// own bounds and its own truncation flag. One walk, two result sets: the walk
// already visits every directory, so recording them costs no extra I/O — but
// descending past the file fairness prune does make the walk itself longer.
//
// # Truncation-flag ownership map
//
// filesTrunc and dirsTrunc are written from SEVEN statements (eight flag
// assignments — the deadline writes both in one statement) spread across TWO
// lexical scopes, the emit closure and the WalkDir callback. No site can see
// the others. Every one carries a `[trunc-writer]` tag, so
//
//	grep -n 'trunc-writer' internal/web/fssearch.go
//
// settles any ownership question on one screen instead of re-deriving control
// flow through four early returns. The sites, in source order:
//
//  1. emit — this top-level's per-root DIRECTORY cap is spent → dirsTrunc.
//     A candidate reached emit and was refused.
//  2. emit — the global directory cap (len(dirs) >= maxDirs) → dirsTrunc.
//     Same shape: a refused candidate.
//  3. walk — the deadline expired → filesTrunc AND dirsTrunc, together.
//     Returns filepath.SkipAll, and is the only site that ends the whole walk,
//     so both sets really are cut short mid-tree.
//  4. walk, directory branch — this top-level's per-root FILE budget is spent
//     → filesTrunc. Deliberately not dirsTrunc: the branch is reached for
//     every directory under that top-level, including ones the project rules
//     would never have offered.
//  5. walk, directory branch — the conjunction prune (this top-level's file
//     AND directory budgets both spent) → dirsTrunc, and fs.SkipDir on that
//     ONE top-level. Site 4's condition is a precondition of this one, so by
//     the time it fires filesTrunc is already set: the prune flags both too,
//     but only for the subtree it skips, not for the walk.
//  6. walk, file branch — the global file cap (len(files) >= maxPaths) →
//     filesTrunc. Recording stops; traversal continues, so the directory set
//     is unaffected.
//  7. walk, file branch — this top-level's per-root file cap → filesTrunc.
//     Same: recording stops, traversal does not.
//
// Facts that follow, stated once here where they can actually be checked: the
// deadline (3) is the only site that ends the whole walk; directory candidates
// are destroyed in four places, emit's two refusals plus — outside emit's
// sight — the deadline and the prune, which skip subtrees whose markers are
// therefore never observed; no site in the file branch ever writes dirsTrunc,
// and emit never writes filesTrunc.
//
// RULE: that inventory — which sites write which flag, which end the walk,
// which set both — is global, and it is stated ONCE, here. No `only` / `every`
// / `never` claim about a truncation flag may appear at a write site. A local
// comment says what its own site does and why ("this branch does not set
// dirsTrunc, because merely visiting a directory says nothing"), which stays
// true regardless of what the other six sites do, and points here for the rest.
// A global claim parked at a local site cannot be checked from there: four
// comments in this file went stale exactly that way, each true when written and
// falsified by a later edit somewhere else.
func walkTree(b walkBounds) (files []indexEntry, filesTrunc bool, dirs []indexEntry, dirsTrunc bool) {
	deadline := time.Now().Add(b.budget)
	files = make([]indexEntry, 0, 1024)
	dirs = make([]indexEntry, 0, 256)
	perRoot := make(map[string]int)
	perRootDirs := make(map[string]int)
	seenDirs := make(map[string]bool)
	n := 0

	// emit records one directory (slash-separated, relative to root) and owns
	// every bound applied to a candidate that REACHES it. Candidates are also
	// destroyed before they ever get here — see the ownership map on walkTree.
	// Both admission rules funnel
	// through it, so the two call sites cannot drift on the bounds — and,
	// crucially, so the dedupe set is shared. The common collision is rule 1 × rule 2: every
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
			dirsTrunc = true // [trunc-writer] site 1 — per-root directory cap
			return
		}
		if len(dirs) >= b.maxDirs {
			dirsTrunc = true // [trunc-writer] site 2 — global directory cap
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
			filesTrunc, dirsTrunc = true, true // [trunc-writer] site 3 — deadline
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
			if skipDirs[d.Name()] || (!b.noRelSkip && skipRelPaths[path.Join(b.relPrefix, slash)]) || len(parts) >= b.maxDepth {
				return fs.SkipDir
			}
			// A CONJUNCTION, not the old single test: skip the subtree only once
			// BOTH budgets for this top-level are spent. Pruning on the file
			// budget alone would take the directories with it, and the subtrees
			// that spend their file budget are the big repos — the ones most
			// worth searching.
			//
			// The per-visit test below flags filesTrunc but NOT dirsTrunc,
			// because merely VISITING a directory under a top-level whose
			// directory budget is spent says nothing: this branch is reached
			// for every directory down there, including ones the project rules
			// would never have offered, so flagging per visit would report the
			// set truncated because of a folder that was never a candidate.
			//
			// The conjunction below is a different claim — see there, and the
			// ownership map on walkTree.
			fileBudgetSpent := perRoot[top] >= b.maxPerRoot
			dirBudgetSpent := perRootDirs[top] >= b.maxPerRootDirs
			if fileBudgetSpent {
				filesTrunc = true // [trunc-writer] site 4 — per-root file budget spent
			}
			if fileBudgetSpent && dirBudgetSpent {
				// This subtree is pruned UNVISITED, so any marker inside it is
				// never observed and emit is never called for that candidate at
				// all — a refusal flag cannot report what it never received.
				// That is why the flag is set HERE rather than left to emit:
				// directories genuinely disappear at this line, outside emit's
				// sight. A truncated list presented as complete is the failure
				// this file exists to prevent, and it is strictly worse than
				// the opposite error. For how this site relates to the other
				// six, see the ownership map on walkTree.
				dirsTrunc = true // [trunc-writer] site 5 — conjunction prune
				return fs.SkipDir
			}
			// Rule 1: a direct, NON-HIDDEN child of $HOME is offered without
			// needing a marker — the non-hidden depth-1 children listWorkspaces
			// offers, minus symlinks, which WalkDir does not follow (a symlinked
			// ~/foo -> /elsewhere has d.IsDir() false and lands in the file
			// branch below, while listWorkspaces resolves and offers it) — plus
			// .config by name, which is hidden but is where a user's own
			// configuration lives. Rule 2 deliberately overrides the hidden filter for
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
			// A full file index stops RECORDING FILES; it does not end the walk.
			// This branch used to return filepath.SkipAll, which ends the WalkDir
			// call outright and took the DIRECTORY walk down with it — so it also
			// had to set dirsTrunc, because every directory lexically after the
			// abort really was gone. That re-coupled two result sets given
			// separate budgets and separate flags precisely so one could not
			// starve the other, and it was not theoretical: on a real home the
			// walk died around ~/my-assistant and the picker lost tech-advisor, a
			// genuine project root, over the 247 files past the ceiling.
			//
			// Past the ceiling the walk keeps traversing, so the remaining cost
			// is bounded by indexBudget, not by maxPaths. That is never worse
			// for the directory set than the old SkipAll, which cut it at the
			// same point or earlier. Returning nil leaves the directory set
			// complete, so dirsTrunc must NOT be set here. Which sites DO end
			// the walk, and which set both flags, is the ownership map's job —
			// see walkTree. That inventory cannot be checked from this line,
			// which is precisely how the sentence that used to sit here went
			// stale.
			//
			// The test is self-latching: files never shrinks, so once it reaches
			// maxPaths this branch returns for every later file. A separate
			// "filesFull" flag would be a second source of truth for the same
			// fact.
			filesTrunc = true // [trunc-writer] site 6 — global file cap
			return nil
		}
		if perRoot[top] >= b.maxPerRoot {
			filesTrunc = true // [trunc-writer] site 7 — per-root file cap
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
	Path string `json:"path"`
	// Rel is relative to Root, or to $HOME when Root is empty.
	Rel string `json:"rel"`
	// Root is the added workspace root holding the hit; empty for a $HOME hit.
	Root  string `json:"root,omitempty"`
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

// rootIndexes returns the built indexes of the given added roots (never home,
// never a dropped root: the caller passes the per-request policy's list). A
// missing index is created and built in the background (refresh, one build per
// index: it is started only when none is in flight and nothing has built yet;
// later staleness goes through snapshot's stale-while-rebuild), so a slow disk
// can never stall the palette. Entries for roots no longer listed are dropped.
// The result is shorter than roots while any root is still building.
func (s *Server) rootIndexes(roots []string) []*fileIndex {
	s.rootIdxMu.Lock()
	defer s.rootIdxMu.Unlock()
	if s.rootIdx == nil {
		s.rootIdx = map[string]*fileIndex{}
	}
	want := make(map[string]bool, len(roots))
	for _, r := range roots {
		want[r] = true
	}
	for r := range s.rootIdx {
		if !want[r] {
			delete(s.rootIdx, r)
		}
	}
	out := make([]*fileIndex, 0, len(roots))
	for _, r := range roots {
		ix, ok := s.rootIdx[r]
		if !ok {
			ix = newFileIndex(r)
			ix.noRelSkip = true
			s.rootIdx[r] = ix
		}
		if ix.ready() {
			out = append(out, ix)
			continue
		}
		// The build is claimed here, under the index lock, before the goroutine
		// exists: a request arriving meanwhile sees it in flight and spawns nothing.
		if ix.startBuild() {
			go ix.runBuild()
		}
	}
	return out
}

// handleFsSearch serves GET /api/fs/search?q=&limit=&kind=&prefer= — fuzzy-matched
// files under $HOME and every added workspace root, best first. `partial` is set
// while an added root is still building its first index. `truncated` reports that the underlying index hit
// one of its bounds, so a short result set is never mistaken for "no such file".
// prefer=<abs dir inside a served root> lists that subtree's matches first (see
// resolvePrefer).
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
	// ONE policy snapshot per request: the prefer root and every hit below are
	// judged against the same view of the boxed trees, and $HOME is resolved once.
	pol, err := s.fsPolicy()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "home unresolvable")
		return
	}
	entries, truncated := s.searchIndex().snapshot(kind)
	lq := strings.ToLower(q)
	// Added roots: every index that has built is searched; one still building
	// makes the answer partial instead of making the palette wait for it.
	var extra []string
	if len(pol.roots) > 1 {
		extra = pol.roots[1:]
	}
	ready := s.rootIndexes(extra)
	partial := len(ready) < len(extra)
	// prefer=<abs dir>: walk that subtree first (its own index, so a root walk
	// that ran out of budget before reaching it cannot hide it), and list its
	// hits before everything else. Files only: the directory picker is unchanged.
	// The folder may sit under an added root: its hits are then rooted there,
	// never joined onto $HOME.
	var hits []searchHit
	preferRel, preferRoot := "", ""
	if kind == kindFiles {
		if pabs, prel, proot, ok := resolvePrefer(pol, r.URL.Query().Get("prefer")); ok {
			preferRel, preferRoot = prel, proot
			// skipRelPaths are $HOME-relative: off under an added root.
			ix := s.preferIndexIn(pabs, filepath.ToSlash(prel), proot != pol.roots[0])
			pentries, ptrunc := ix.snapshot(kindFiles)
			truncated = truncated || ptrunc
			hits = scoreEntries(pentries, lq, prel)
			if proot != pol.roots[0] {
				for i := range hits {
					hits[i].Root = proot
				}
			}
		}
	}
	rest := make([]searchHit, 0, 64)
	for _, h := range scoreEntries(entries, lq, "") {
		// The home index holds the preferred subtree too; list each file once.
		if preferRel != "" && preferRoot == pol.roots[0] && strings.HasPrefix(h.Rel, preferRel+string(filepath.Separator)) {
			continue
		}
		rest = append(rest, h)
	}
	for _, ix := range ready {
		ents, trunc := ix.snapshot(kind)
		truncated = truncated || trunc
		for _, h := range scoreEntries(ents, lq, "") {
			h.Root = ix.root
			rest = append(rest, h)
		}
	}
	if len(ready) > 0 {
		// One ranking across home and every root; stable, so home leads a tie.
		sort.SliceStable(rest, func(i, j int) bool { return rest[i].Score > rest[j].Score })
	}
	hits = append(hits, rest...)

	// Resolve and containment-check only what will actually be returned: a link
	// that leaves its root, or that a box planted inside a boxed workspace to
	// reach a host file, must never be surfaced — exactly as /api/fs/list and
	// /api/fs/read already guarantee. The same resolver decides here, so search
	// cannot name a path the other endpoints would refuse. Nested roots index the
	// same file twice; the path is the identity, so each is listed once.
	out := make([]searchHit, 0, limit)
	seen := make(map[string]bool, limit)
	for _, h := range hits {
		if len(out) >= limit {
			break
		}
		base := s.Home
		if h.Root != "" {
			base = h.Root
		}
		abs := filepath.Join(base, h.Rel)
		if seen[abs] {
			continue
		}
		// Nested roots: the file belongs to the deepest root holding it, whichever
		// index found it. Re-attribute rather than drop, so a query that only the
		// outer index matches ("api" for api/main.go) still gets the right root.
		if h.Root != "" {
			if r, ok := pol.rootOf(abs); ok && r != h.Root {
				if rel, err := filepath.Rel(r, abs); err == nil {
					h.Root, h.Rel = r, rel
					if r == pol.roots[0] {
						h.Root = ""
					}
				}
			}
		}
		t, err := pol.resolve(abs)
		if err != nil {
			continue
		}
		info, err := statTarget(t)
		if err != nil {
			continue
		}
		// The index says what it recorded; the stat says what is on disk NOW.
		// They disagree whenever something was created, deleted or replaced
		// since the last rebuild, which is exactly what the TTL allows.
		if info.IsDir() != (kind == kindDirs) {
			continue
		}
		seen[abs] = true
		h.Path = abs
		out = append(out, h)
	}
	resp := map[string]any{"results": out, "truncated": truncated}
	if partial {
		resp["partial"] = true
	}
	writeJSON(w, http.StatusOK, resp)
}

// scoreEntries fuzzy-scores an index against lq and returns the hits best
// first. prefix (home-relative) is joined onto each entry's rel for an index
// rooted below $HOME; the score is computed on the index's own rel, so a
// preferred repo's files are not penalised for the length of its path.
func scoreEntries(entries []indexEntry, lq, prefix string) []searchHit {
	hits := make([]searchHit, 0, 64)
	for _, e := range entries {
		score, ok := fuzzyScoreLower(e.lower, e.baseStart, lq)
		if !ok {
			continue
		}
		rel := e.rel
		if prefix != "" {
			rel = filepath.Join(prefix, e.rel)
		}
		hits = append(hits, searchHit{Rel: rel, Score: score})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Rel < hits[j].Rel
	})
	return hits
}

// statTarget stats a resolved target the way its kind demands. Every target is
// opened through its Root: for a strict (boxed) target Rel is unresolved, and
// os.Root refuses any link in it that leaves the workspace, so a box-planted link
// cannot make the stat — or the kind check built on it — touch a host file.
func statTarget(t fsTarget) (os.FileInfo, error) {
	root, err := os.OpenRoot(t.Root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Stat(t.Rel)
}

// resolvePrefer turns ?prefer=<abs dir> into the directory to walk first and
// its path relative to the served root holding it, which is returned too (home
// or an added root: the handler roots the hits there). Anything
// unusable — relative, missing, not a directory, outside every root once
// symlinks resolve, denied, or a root itself —
// is ignored (ok=false), never an error: preferring is a ranking hint, not a
// scope. It resolves through pol, so a prefer root inside a boxed workspace is
// held to the same os.Root containment as the hits it will produce.
func resolvePrefer(pol fsPolicy, raw string) (abs, rel, root string, ok bool) {
	if raw == "" {
		return "", "", "", false
	}
	t, err := pol.resolve(raw)
	if err != nil {
		return "", "", "", false
	}
	fi, err := statTarget(t)
	if err != nil || !fi.IsDir() {
		return "", "", "", false
	}
	resolved := t.Abs
	root, inRoot := pol.rootOf(resolved)
	if !inRoot {
		return "", "", "", false
	}
	rel, err = filepath.Rel(root, resolved)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", "", "", false
	}
	// A prefer root that is itself a machine-generated/credential directory
	// (skipDirs) or sits under a skipped home-relative path (skipRelPaths) is
	// ignored the same way an unusable value is: the walk would refuse to
	// index it anyway (it IS the excluded subtree, not merely a path that
	// crosses one), so preferring it would silently search nothing.
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, p := range parts {
		if skipDirs[p] {
			return "", "", "", false
		}
	}
	// skipRelPaths are $HOME-relative, so they only apply under home.
	if root == pol.roots[0] {
		for i := range parts {
			if skipRelPaths[strings.Join(parts[:i+1], "/")] {
				return "", "", "", false
			}
		}
	}
	return resolved, rel, root, true
}

// preferIndex returns the index rooted at root (created on first use) and marks
// it most recently used, evicting the oldest past maxPreferIndexes.
func (s *Server) preferIndex(root, relPrefix string) *fileIndex {
	return s.preferIndexIn(root, relPrefix, false)
}

// preferIndexIn is preferIndex with the index's skipRelPaths switched off for a
// folder under an added root. The flag is fixed at creation, before any walk.
func (s *Server) preferIndexIn(root, relPrefix string, noRelSkip bool) *fileIndex {
	s.prefMu.Lock()
	defer s.prefMu.Unlock()
	if s.prefIdx == nil {
		s.prefIdx = map[string]*fileIndex{}
	}
	ix, ok := s.prefIdx[root]
	if !ok {
		ix = newFileIndex(root)
		ix.relPrefix = relPrefix
		ix.noRelSkip = noRelSkip
		ix.budget = preferIndexBudget
		s.prefIdx[root] = ix
	}
	for i, r := range s.prefOrder {
		if r == root {
			s.prefOrder = append(s.prefOrder[:i], s.prefOrder[i+1:]...)
			break
		}
	}
	s.prefOrder = append(s.prefOrder, root)
	for len(s.prefOrder) > maxPreferIndexes {
		delete(s.prefIdx, s.prefOrder[0])
		s.prefOrder = s.prefOrder[1:]
	}
	return ix
}
