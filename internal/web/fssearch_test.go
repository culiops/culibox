package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// mustWrite writes p with the given content, defaulting to "x" when the
// caller doesn't care what's inside the file.
func mustWrite(t *testing.T, p string, content ...string) {
	t.Helper()
	body := "x"
	if len(content) > 0 {
		body = content[0]
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustMkdirAll(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestFuzzyScoreMatching(t *testing.T) {
	if _, ok := fuzzyScore("web/app.js", ""); ok {
		t.Fatal("empty query must not match")
	}
	if _, ok := fuzzyScore("web/app.js", "zzz"); ok {
		t.Fatal("absent characters must not match")
	}
	if _, ok := fuzzyScore("web/app.js", "wpj"); !ok {
		t.Fatal("scattered subsequence should match")
	}
	if _, ok := fuzzyScore("web/APP.js", "app"); !ok {
		t.Fatal("match must be case-insensitive")
	}
	// A subsequence that runs out of characters must not match.
	if _, ok := fuzzyScore("ab", "aba"); ok {
		t.Fatal("query longer than available subsequence must not match")
	}
}

func TestFuzzyScoreRanking(t *testing.T) {
	rank := func(query string, paths ...string) []string {
		type sc struct {
			p string
			s int
		}
		var out []sc
		for _, p := range paths {
			if s, ok := fuzzyScore(p, query); ok {
				out = append(out, sc{p, s})
			}
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].s != out[j].s {
				return out[i].s > out[j].s
			}
			return out[i].p < out[j].p
		})
		var names []string
		for _, o := range out {
			names = append(names, o.p)
		}
		return names
	}

	// A contiguous basename hit beats a scattered subsequence in a longer path.
	got := rank("app", "x/app.js", "a/p/p/lication/notes.md")
	if len(got) != 2 || got[0] != "x/app.js" {
		t.Fatalf("basename hit should rank first, got %v", got)
	}

	// Basename-start beats basename-middle.
	got = rank("app", "x/app.js", "x/myapp.js")
	if len(got) != 2 || got[0] != "x/app.js" {
		t.Fatalf("basename-prefix should rank first, got %v", got)
	}

	// Shorter path wins an otherwise-equal match.
	got = rank("app", "app.js", "deep/nested/dir/app.js")
	if len(got) != 2 || got[0] != "app.js" {
		t.Fatalf("shorter path should rank first, got %v", got)
	}

	// A basename hit beats the same substring appearing only in a directory name.
	got = rank("app", "src/app.js", "app/zzzzzz.js")
	if len(got) != 2 || got[0] != "src/app.js" {
		t.Fatalf("basename hit should beat dirname hit, got %v", got)
	}
}

// rels flattens an index snapshot to its relative paths.
func rels(entries []indexEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.rel)
	}
	return out
}

func TestFileIndexSkipsGeneratedDirsButKeepsDotfiles(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, "keep.go"))
	mustWrite(t, filepath.Join(home, "proj", "main.go"))
	mustWrite(t, filepath.Join(home, ".git", "config"))
	mustWrite(t, filepath.Join(home, ".cache", "junk.bin"))
	mustWrite(t, filepath.Join(home, ".ssh", "id_rsa"))
	mustWrite(t, filepath.Join(home, "proj", "node_modules", "left-pad", "index.js"))
	// This console runs coding agents: ~/.claude and dotfiles are the files a
	// user actually reaches for, so they must be findable.
	mustWrite(t, filepath.Join(home, ".claude", "settings.json"))
	mustWrite(t, filepath.Join(home, "proj", ".gitignore"))
	mustWrite(t, filepath.Join(home, "proj", "vendor", "dep.go"))

	paths := rels(mustSnapshot(t, newFileIndex(home)))
	joined := strings.Join(paths, "\n")

	for _, want := range []string{
		"keep.go",
		filepath.Join("proj", "main.go"),
		filepath.Join(".claude", "settings.json"),
		filepath.Join("proj", ".gitignore"),
		filepath.Join("proj", "vendor", "dep.go"),
	} {
		if !contains(paths, want) {
			t.Fatalf("expected %q in index, got:\n%s", want, joined)
		}
	}
	for _, bad := range []string{".git" + string(filepath.Separator), ".cache", ".ssh", "node_modules"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("index must not contain %q, got:\n%s", bad, joined)
		}
	}
}

func TestFileIndexCapsEachTopLevelSubtree(t *testing.T) {
	home := t.TempDir()
	// A machine-generated tree that would otherwise consume the whole budget
	// before the walk ever reaches the user's own directories. The walk is a
	// lexical DFS, so "aaa-huge" is visited first.
	for i := 0; i < 40; i++ {
		mustWrite(t, filepath.Join(home, "aaa-huge", "pkg", "f"+strconv.Itoa(i)+".txt"))
	}
	mustWrite(t, filepath.Join(home, "zzz-repo", "main.go"))
	mustWrite(t, filepath.Join(home, "zzz-repo", "README.md"))

	ix := newFileIndex(home)
	ix.maxPaths = 50
	ix.maxPerRoot = 5
	paths := rels(mustSnapshot(t, ix))

	huge := 0
	for _, p := range paths {
		if strings.HasPrefix(p, "aaa-huge") {
			huge++
		}
	}
	if huge > 5 {
		t.Fatalf("one subtree took %d slots, cap is 5: %v", huge, paths)
	}
	// The point of the cap: the user's own files still make it in.
	for _, want := range []string{
		filepath.Join("zzz-repo", "main.go"),
		filepath.Join("zzz-repo", "README.md"),
	} {
		if !contains(paths, want) {
			t.Fatalf("expected %q to survive the cap, got %v", want, paths)
		}
	}
}

func TestFileIndexSkipsMachineManagedTrees(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, ".local", "share", "thing.db"))
	mustWrite(t, filepath.Join(home, ".claude", "projects", "log.jsonl"))
	mustWrite(t, filepath.Join(home, ".claude", "plugins", "cache", "dep.js"))
	mustWrite(t, filepath.Join(home, "go", "pkg", "mod", "dep.go"))
	// Kept: the user's own agent configuration lives right beside those.
	mustWrite(t, filepath.Join(home, ".claude", "settings.json"))
	mustWrite(t, filepath.Join(home, ".claude", "skills", "brain", "SKILL.md"))
	mustWrite(t, filepath.Join(home, "go", "bin", "tool"))

	paths := rels(mustSnapshot(t, newFileIndex(home)))
	joined := strings.Join(paths, "\n")

	for _, bad := range []string{".local", "projects", "plugins", filepath.Join("pkg", "mod")} {
		if strings.Contains(joined, bad) {
			t.Fatalf("machine-managed tree %q must be skipped, got:\n%s", bad, joined)
		}
	}
	for _, want := range []string{
		filepath.Join(".claude", "settings.json"),
		filepath.Join(".claude", "skills", "brain", "SKILL.md"),
		filepath.Join("go", "bin", "tool"),
	} {
		if !contains(paths, want) {
			t.Fatalf("expected %q in index, got:\n%s", want, joined)
		}
	}
}

func TestFileIndexFirstBuildBlocksConcurrentCallers(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < 20; i++ {
		mustWrite(t, filepath.Join(home, "d"+strconv.Itoa(i), "f.txt"))
	}
	ix := newFileIndex(home)

	// Every caller that arrives during the very first walk must wait for real
	// results. Answering them with an empty index and truncated=false would read
	// as "no such file" — the palette would show nothing while the user types.
	var wg sync.WaitGroup
	counts := make([]int, 8)
	for i := range counts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			entries, _ := ix.snapshot(kindFiles)
			counts[i] = len(entries)
		}(i)
	}
	wg.Wait()
	for i, n := range counts {
		if n != 20 {
			t.Fatalf("caller %d saw %d entries, want 20 — an empty index leaked to a concurrent first caller", i, n)
		}
	}
}

func TestFileIndexSkipsCredentialStores(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, ".aws", "credentials"))
	mustWrite(t, filepath.Join(home, ".kube", "config"))
	mustWrite(t, filepath.Join(home, ".config", "rclone", "rclone.conf"))
	mustWrite(t, filepath.Join(home, ".config", "gh", "hosts.yml"))
	mustWrite(t, filepath.Join(home, ".netrc"))
	mustWrite(t, filepath.Join(home, "proj", ".npmrc"))
	// Not a secret store: the rest of .config stays searchable.
	mustWrite(t, filepath.Join(home, ".config", "nvim", "init.lua"))

	paths := rels(mustSnapshot(t, newFileIndex(home)))
	joined := strings.Join(paths, "\n")

	for _, bad := range []string{"credentials", "kube", "rclone", "hosts.yml", ".netrc", ".npmrc"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("credential store %q must not be indexed, got:\n%s", bad, joined)
		}
	}
	if !contains(paths, filepath.Join(".config", "nvim", "init.lua")) {
		t.Fatalf("the rest of .config must stay searchable, got:\n%s", joined)
	}
}

func TestFileIndexRespectsMaxDepth(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, "a", "shallow.txt"))
	mustWrite(t, filepath.Join(home, "a", "b", "c", "deep.txt"))

	ix := newFileIndex(home)
	ix.maxDepth = 2 // at most 2 path elements: "a/shallow.txt" in, "a/b/c/deep.txt" out
	paths := rels(mustSnapshot(t, ix))

	if !contains(paths, filepath.Join("a", "shallow.txt")) {
		t.Fatalf("shallow file missing: %v", paths)
	}
	if contains(paths, filepath.Join("a", "b", "c", "deep.txt")) {
		t.Fatalf("deep file should have been skipped: %v", paths)
	}
}

func TestFileIndexTruncatesAtMaxPaths(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < 12; i++ {
		mustWrite(t, filepath.Join(home, "f"+string(rune('a'+i))+".txt"))
	}
	ix := newFileIndex(home)
	ix.maxPaths = 5
	entries, truncated := ix.snapshot(kindFiles)

	if len(entries) != 5 {
		t.Fatalf("want 5 paths, got %d", len(entries))
	}
	if !truncated {
		t.Fatal("hitting maxPaths must report truncated")
	}
}

// mustSnapshot returns the first snapshot, which always builds synchronously.
func mustSnapshot(t *testing.T, ix *fileIndex) []indexEntry {
	t.Helper()
	entries, _ := ix.snapshot(kindFiles)
	return entries
}

func TestFileIndexServesStaleAndRefreshesInBackground(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, "one.txt"))

	ix := newFileIndex(home)
	// The very first call has nothing to serve, so it builds synchronously.
	if entries := mustSnapshot(t, ix); len(entries) != 1 {
		t.Fatalf("want 1 path, got %v", rels(entries))
	}
	mustWrite(t, filepath.Join(home, "two.txt"))

	// Inside the TTL the new file must NOT appear (that is the documented trade).
	if entries, _ := ix.snapshot(kindFiles); len(entries) != 1 {
		t.Fatalf("cached snapshot should still hold 1 path, got %v", rels(entries))
	}

	// Once stale, the request is still answered immediately from the old cache —
	// never blocked on the walk — and a background rebuild lands shortly after.
	ix.setTTL(time.Nanosecond)
	if entries, _ := ix.snapshot(kindFiles); len(entries) != 1 {
		t.Fatalf("stale snapshot must be served as is, got %v", rels(entries))
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		entries, _ := ix.snapshot(kindFiles)
		if len(entries) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background rebuild never landed, got %v", rels(entries))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type searchResp struct {
	Results []struct {
		Path  string `json:"path"`
		Rel   string `json:"rel"`
		Score int    `json:"score"`
	} `json:"results"`
	Truncated bool `json:"truncated"`
}

func doSearch(t *testing.T, s *Server, query string) (*httptest.ResponseRecorder, searchResp) {
	t.Helper()
	rr := httptest.NewRecorder()
	s.handleFsSearch(rr, httptest.NewRequest("GET", "/api/fs/search?"+query, nil))
	var out searchResp
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("bad JSON: %v (%s)", err, rr.Body)
		}
	}
	return rr, out
}

func TestHandleFsSearchRequiresQuery(t *testing.T) {
	s := &Server{Home: t.TempDir()}
	if rr, _ := doSearch(t, s, "q="); rr.Code != http.StatusBadRequest {
		t.Fatalf("empty q: want 400, got %d", rr.Code)
	}
	if rr, _ := doSearch(t, s, ""); rr.Code != http.StatusBadRequest {
		t.Fatalf("missing q: want 400, got %d", rr.Code)
	}
	if rr, _ := doSearch(t, s, "q=%20%20"); rr.Code != http.StatusBadRequest {
		t.Fatalf("blank q: want 400, got %d", rr.Code)
	}
}

func TestHandleFsSearchRanksAndReturnsAbsolutePaths(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, "proj", "app.js"))
	mustWrite(t, filepath.Join(home, "other", "deep", "wrapper", "application-notes.md"))
	mustWrite(t, filepath.Join(home, "unrelated.txt"))
	s := &Server{Home: home}

	rr, out := doSearch(t, s, "q=app.js")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rr.Code, rr.Body)
	}
	if len(out.Results) == 0 {
		t.Fatal("expected at least one result")
	}
	first := out.Results[0]
	if first.Rel != filepath.Join("proj", "app.js") {
		t.Fatalf("want proj/app.js first, got %+v", out.Results)
	}
	if first.Path != filepath.Join(home, "proj", "app.js") {
		t.Fatalf("path must be absolute and joined to home, got %q", first.Path)
	}
	for _, r := range out.Results {
		if strings.Contains(r.Rel, "unrelated") {
			t.Fatalf("non-matching file leaked into results: %+v", out.Results)
		}
	}
}

func TestHandleFsSearchLimit(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < 10; i++ {
		mustWrite(t, filepath.Join(home, "match"+string(rune('a'+i))+".txt"))
	}
	s := &Server{Home: home}

	if _, out := doSearch(t, s, "q=match&limit=3"); len(out.Results) != 3 {
		t.Fatalf("limit=3: want 3 results, got %d", len(out.Results))
	}
	// Over-large limits are clamped, not honoured verbatim.
	if _, out := doSearch(t, s, "q=match&limit=99999"); len(out.Results) != 10 {
		t.Fatalf("huge limit: want all 10 results, got %d", len(out.Results))
	}
	// A garbage limit falls back to the default rather than returning nothing.
	if _, out := doSearch(t, s, "q=match&limit=abc"); len(out.Results) != 10 {
		t.Fatalf("bad limit: want default behaviour, got %d", len(out.Results))
	}
}

func TestHandleFsSearchExcludesSymlinkEscape(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "escape-target.txt")
	if err := os.WriteFile(secret, []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "escape-target.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skip("symlink not supported")
	}
	mustWrite(t, filepath.Join(home, "escape-local.txt"))
	s := &Server{Home: home}

	_, out := doSearch(t, s, "q=escape")
	for _, r := range out.Results {
		if strings.Contains(r.Rel, "escape-target") {
			t.Fatalf("symlink escaping home must not be returned: %+v", out.Results)
		}
	}
	if len(out.Results) != 1 || !strings.Contains(out.Results[0].Rel, "escape-local") {
		t.Fatalf("want only the in-home file, got %+v", out.Results)
	}
}

func TestSearchKindDirReturnsOnlyDirectories(t *testing.T) {
	home := t.TempDir()
	mustMkdirAll(t, filepath.Join(home, "static"))
	mustWrite(t, filepath.Join(home, "static", "static.js"), "x")

	s := &Server{Home: home}
	_, resp := doSearch(t, s, "q=static&kind=dir")
	if len(resp.Results) == 0 {
		t.Fatal("kind=dir found no directories")
	}
	for _, r := range resp.Results {
		if r.Rel == "static/static.js" {
			t.Fatalf("a FILE was returned for kind=dir: %+v", resp.Results)
		}
	}
}

// The palette consumes this endpoint. An absent or unrecognised kind must be
// byte-identical to today, or the palette silently starts listing folders.
func TestSearchWithoutKindIsUnchanged(t *testing.T) {
	home := t.TempDir()
	mustMkdirAll(t, filepath.Join(home, "static"))
	mustWrite(t, filepath.Join(home, "static", "static.js"), "x")

	s := &Server{Home: home}
	_, plain := doSearch(t, s, "q=static")
	_, bogus := doSearch(t, s, "q=static&kind=banana")
	if !reflect.DeepEqual(plain.Results, bogus.Results) {
		t.Fatalf("an unrecognised kind changed the result set: %+v vs %+v", plain.Results, bogus.Results)
	}
	for _, r := range plain.Results {
		if r.Rel == "static" {
			t.Fatalf("a DIRECTORY leaked into the default result set: %+v", plain.Results)
		}
	}
}

// indexDirs returns the directory set as slash-separated paths, which is how
// every assertion below and the HTTP layer read them.
func indexDirs(t *testing.T, ix *fileIndex) []string {
	t.Helper()
	dirs, _ := ix.snapshot(kindDirs)
	out := rels(dirs)
	for i, p := range out {
		out[i] = filepath.ToSlash(p)
	}
	return out
}

// countOf reports how many times needle appears — the dedupe assertions need
// "exactly once", which slices.Contains cannot express.
func countOf(hay []string, needle string) int {
	n := 0
	for _, h := range hay {
		if h == needle {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// The project rule: a directory is offered as a workspace when it is a
// non-hidden depth-1 child of $HOME (plus .config), or when it DIRECTLY
// contains a marker file. skipDirRecord beats both.
// ---------------------------------------------------------------------------

// Every marker names its parent, at any depth, hidden or not. The fixture sits
// at depth 3 so rule 1 cannot account for the result.
func TestProjectMarkersRecordTheirParent(t *testing.T) {
	for _, marker := range []string{".git", "go.mod", "package.json", "Cargo.toml", "pyproject.toml"} {
		t.Run(marker, func(t *testing.T) {
			home := t.TempDir()
			mustWrite(t, filepath.Join(home, "top", "nest", "proj", marker), "{}")
			got := indexDirs(t, newFileIndex(home))
			if !slices.Contains(got, "top/nest/proj") {
				t.Fatalf("%q did not name its parent a project: %v", marker, got)
			}
			// The directory between $HOME's child and the project carries no
			// marker, so it is not offered.
			if slices.Contains(got, "top/nest") {
				t.Fatalf("a markerless directory was recorded: %v", got)
			}
		})
	}
}

// .git is in skipDirs, so a marker test placed inside the d.IsDir() branch —
// below its fs.SkipDir — would never observe the single most important marker.
// Here .git is a real DIRECTORY, which is the shape that regresses.
func TestProjectGitDirIsObservedDespiteSkipDirs(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, "top", "nest", "repo", ".git", "config"), "[core]")

	got := indexDirs(t, newFileIndex(home))
	if !slices.Contains(got, "top/nest/repo") {
		t.Fatalf("a .git DIRECTORY must name its parent a project: %v", got)
	}
	if slices.Contains(got, "top/nest/repo/.git") {
		t.Fatalf(".git is the marker, not the project: %v", got)
	}
}

// Rule 1 mirrors listWorkspaces: the direct, non-hidden children of $HOME are
// offered with no marker at all. The tree here is deliberately MARKERLESS —
// on a real home ~/.claude carries its own .git and rule 2 admits it, so a test
// asserting "hidden is never offered" against a real home would be false.
func TestProjectDepth1ChildrenNeedNoMarker(t *testing.T) {
	home := t.TempDir()
	mustMkdirAll(t, filepath.Join(home, "notes", "deeper"))
	mustMkdirAll(t, filepath.Join(home, ".hidden", "inner"))
	mustMkdirAll(t, filepath.Join(home, ".config", "nvim"))

	got := indexDirs(t, newFileIndex(home))
	if !slices.Contains(got, "notes") {
		t.Fatalf("a non-hidden depth-1 child must be offered without a marker: %v", got)
	}
	// .config is the one hidden name on the allowlist: it is where a user's own
	// configuration lives, and it carries no marker of its own.
	if !slices.Contains(got, ".config") {
		t.Fatalf(".config must be admitted by name: %v", got)
	}
	for _, bad := range []string{".hidden", ".hidden/inner", "notes/deeper", ".config/nvim"} {
		if slices.Contains(got, bad) {
			t.Fatalf("markerless directory %q must not be offered: %v", bad, got)
		}
	}
}

// One directory, several markers — and the rule-1 × rule-2 collision, which is
// the common case: EVERY top-level repo is admitted once as a depth-1 child and
// again when its .git is visited (~30 of them on a real home). One shared
// dedupe set, not one per rule.
func TestProjectRecordsEachDirectoryOnce(t *testing.T) {
	home := t.TempDir()
	// Three markers, one directory, below depth 1 so only rule 2 applies.
	deep := filepath.Join(home, "top", "poly")
	mustWrite(t, filepath.Join(deep, "go.mod"), "module poly")
	mustWrite(t, filepath.Join(deep, "package.json"), "{}")
	mustWrite(t, filepath.Join(deep, ".git", "config"), "[core]")
	// A depth-1 repo: rule 1 and rule 2 both admit it.
	mustWrite(t, filepath.Join(home, "repo", ".git", "config"), "[core]")
	mustWrite(t, filepath.Join(home, "repo", "go.mod"), "module repo")

	got := indexDirs(t, newFileIndex(home))
	if n := countOf(got, "top/poly"); n != 1 {
		t.Fatalf("three markers recorded the directory %d times: %v", n, got)
	}
	if n := countOf(got, "repo"); n != 1 {
		t.Fatalf("rule 1 × rule 2 recorded the directory %d times: %v", n, got)
	}
}

// skipDirRecord beats BOTH rules, over the whole subtree — while the files
// underneath stay in the palette, which is the entire reason these names are
// not in skipDirs.
func TestProjectSkipDirRecordBeatsBothRules(t *testing.T) {
	home := t.TempDir()
	// A depth-1 child, so rule 1 would offer it but for the skip.
	mustWrite(t, filepath.Join(home, "sdk", "go1.26", "src", "internal", "go.mod"), "module std")
	mustWrite(t, filepath.Join(home, "code-server", "extensions", "ms-x", "package.json"), "{}")
	mustMkdirAll(t, filepath.Join(home, "worktrees", "feat-a"))
	mustWrite(t, filepath.Join(home, "worktrees", "feat-a", "go.mod"), "module w")
	// And below depth 1, where only rule 2 could fire.
	mustWrite(t, filepath.Join(home, "app", ".open-next", "server", "package.json"), "{}")
	mustWrite(t, filepath.Join(home, ".zinit", "plugins", "p", ".git", "config"), "[core]")
	mustWrite(t, filepath.Join(home, ".tmux", "plugins", "q", ".git", "config"), "[core]")
	mustWrite(t, filepath.Join(home, "repo", ".worktrees", "wt", "go.mod"), "module wt")

	ix := newFileIndex(home)
	got := indexDirs(t, ix)
	for _, bad := range []string{
		"sdk", "sdk/go1.26/src/internal",
		"code-server", "code-server/extensions/ms-x",
		"worktrees", "worktrees/feat-a",
		"app/.open-next/server",
		".zinit/plugins/p", ".tmux/plugins/q",
		"repo/.worktrees/wt",
	} {
		if slices.Contains(got, bad) {
			t.Fatalf("skipDirRecord did not suppress %q: %v", bad, got)
		}
	}
	// The parents that are not themselves skipped stay offered.
	for _, want := range []string{"app", "repo"} {
		if !slices.Contains(got, want) {
			t.Fatalf("%q must still be offered: %v", want, got)
		}
	}
	// The file index is untouched by skipDirRecord — that is the whole point.
	files := rels(mustSnapshot(t, ix))
	for _, want := range []string{
		filepath.Join("sdk", "go1.26", "src", "internal", "go.mod"),
		filepath.Join("code-server", "extensions", "ms-x", "package.json"),
	} {
		if !slices.Contains(files, want) {
			t.Fatalf("file %q must stay in the palette: %v", want, files)
		}
	}
}

// A marker in $HOME itself names $HOME — which is the index root, not a project
// inside it. Emitting "." would put a bare dot in the picker.
func TestProjectMarkerInHomeItselfIsNotRecorded(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, "go.mod"), "module home")
	mustWrite(t, filepath.Join(home, ".git", "config"), "[core]")
	mustMkdirAll(t, filepath.Join(home, "proj"))

	got := indexDirs(t, newFileIndex(home))
	for _, bad := range []string{".", "", ".git"} {
		if slices.Contains(got, bad) {
			t.Fatalf("a marker in $HOME emitted %q: %v", bad, got)
		}
	}
	if !slices.Contains(got, "proj") {
		t.Fatalf("the rest of the walk must be unaffected: %v", got)
	}
}

// maxDirs bounds rule-2 emissions too, and sets dirsTrunc when it bites — the
// bound lives in emit, so it cannot be bypassed by the rule that admitted the
// directory.
func TestProjectMaxDirsBoundsMarkerEmissions(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < 12; i++ {
		mustWrite(t, filepath.Join(home, "big", fmt.Sprintf("p%02d", i), "go.mod"), "module p")
	}
	ix := newFileIndex(home)
	ix.maxDirs = 4

	dirs, dirsTrunc := ix.snapshot(kindDirs)
	if len(dirs) != 4 {
		t.Fatalf("maxDirs did not bound the marker rule: got %d dirs (%v)", len(dirs), rels(dirs))
	}
	if !dirsTrunc {
		t.Fatal("hitting maxDirs must report the DIRECTORY set truncated")
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// The directory set is collected by the SAME walk as the files, with its own
// bounds and its own truncation flag. These pin the three things that separate
// it from "just index directories too".
//
// The marker sits at DEPTH 2 on purpose. The obvious fixture home/repo/.git
// proves nothing: "repo" is a non-hidden depth-1 child, so rule 1 records it
// whether or not the marker rule works at all.
func TestIndexCollectsDirectories(t *testing.T) {
	home := t.TempDir()
	mustMkdirAll(t, filepath.Join(home, "top", "repo", ".git"))
	mustMkdirAll(t, filepath.Join(home, "top", "repo", "sub"))
	mustWrite(t, filepath.Join(home, "top", "repo", "sub", "a.go"), "package web")

	ix := newFileIndex(home)
	got := indexDirs(t, ix)
	for _, want := range []string{"top", "top/repo"} {
		if !slices.Contains(got, want) {
			t.Fatalf("directory %q missing from %v", want, got)
		}
	}
	// The tree goes deeper than the picker: a markerless child of a project is
	// reached by clicking down, not by searching for it.
	if slices.Contains(got, "top/repo/sub") {
		t.Fatalf("a markerless directory was recorded: %v", got)
	}
	// The file set must not have gained directories.
	files, _ := ix.snapshot(kindFiles)
	if slices.Contains(rels(files), filepath.Join("top", "repo", "sub")) {
		t.Fatalf("a directory leaked into the file set: %v", rels(files))
	}
}

// vendor/ is pruned from RECORDING only. Adding it to skipDirs would delete
// every file under it from the palette, which is a regression, not a benefit.
//
// The marker inside vendor/ is what keeps this test honest: without it,
// app/vendor/lib is unrecorded merely because nothing there is a project, and
// the test would pass with skipDirRecord deleted entirely. package.json is used
// rather than composer.json — the natural choice for a vendor/ — because
// composer.json was dropped from the marker set, which would make the fixture
// vacuous for a new reason.
func TestVendorIsNotRecordedButItsFilesAre(t *testing.T) {
	home := t.TempDir()
	mustMkdirAll(t, filepath.Join(home, "app", "vendor", "lib"))
	mustWrite(t, filepath.Join(home, "app", "vendor", "lib", "x.php"), "<?php")
	mustWrite(t, filepath.Join(home, "app", "vendor", "lib", "package.json"), "{}")

	ix := newFileIndex(home)
	got := indexDirs(t, ix)
	for _, d := range got {
		if d == "app/vendor" || d == "app/vendor/lib" {
			t.Fatalf("vendor directory was recorded: %v", got)
		}
	}
	files, _ := ix.snapshot(kindFiles)
	for _, want := range []string{
		filepath.Join("app", "vendor", "lib", "x.php"),
		filepath.Join("app", "vendor", "lib", "package.json"),
	} {
		if !slices.Contains(rels(files), want) {
			t.Fatalf("a file under vendor must still be findable: %v", rels(files))
		}
	}
}

// The fairness prune is a CONJUNCTION. A subtree that has spent its file budget
// must keep yielding directories, or the biggest repos — exactly the ones worth
// searching — go missing.
//
// With maxPerRoot = 3 the marker files are themselves past the per-root FILE
// cap, so this also proves the marker check sits ABOVE that cap in the walk: if
// it sat below, every project after the third file in "big" would be lost.
func TestFileBudgetExhaustionStillYieldsDirectories(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < 12; i++ {
		d := filepath.Join(home, "big", fmt.Sprintf("d%02d", i))
		mustMkdirAll(t, d)
		mustWrite(t, filepath.Join(d, "go.mod"), "module d")
		mustWrite(t, filepath.Join(d, "f.txt"), "x")
	}
	ix := newFileIndex(home)
	ix.maxPerRoot = 3 // file budget for "big" is spent almost immediately
	ix.maxPerRootDirs = 100

	got := indexDirs(t, ix)
	if len(got) < 12 {
		t.Fatalf("file-budget exhaustion pruned the directory set: got %d, want >= 12 (%v)", len(got), got)
	}
	for i := 0; i < 12; i++ {
		want := fmt.Sprintf("big/d%02d", i)
		if !slices.Contains(got, want) {
			t.Fatalf("project %q was lost to the spent file budget: %v", want, got)
		}
	}
}

// Two caps, two flags. One shared bool would make the palette claim FILES are
// missing when only the directory cap tripped.
func TestTruncationFlagsAreIndependent(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < 30; i++ {
		d := filepath.Join(home, fmt.Sprintf("d%02d", i))
		mustMkdirAll(t, d)
	}
	mustWrite(t, filepath.Join(home, "one.txt"), "x")

	ix := newFileIndex(home)
	ix.maxDirs = 5 // only the directory cap is tight

	if _, dirsTrunc := ix.snapshot(kindDirs); !dirsTrunc {
		t.Fatal("hitting maxDirs must report the DIRECTORY set truncated")
	}
	if _, filesTrunc := ix.snapshot(kindFiles); filesTrunc {
		t.Fatal("the directory cap must not mark the FILE set truncated")
	}
}

// The two result sets have independent budgets and independent truncation flags
// so that one cannot starve the other. Ending the walk with filepath.SkipAll at
// the file ceiling re-coupled them: on a real home the walk died at ~my-assistant
// and the picker lost tech-advisor — a genuine project root — for the sake of
// 247 files it had no room for anyway.
//
// So the file ceiling now stops RECORDING FILES and lets the walk continue. The
// directory set is complete, and dirsTruncated must say so.
func TestFileCapDoesNotTruncateDirectories(t *testing.T) {
	home := t.TempDir()
	// Sorts first, and spends the whole file budget before anything else is
	// reached — the lexical-DFS shape that made this a real defect.
	for i := 0; i < 10; i++ {
		mustWrite(t, filepath.Join(home, "aaa-noise", fmt.Sprintf("f%02d.txt", i)), "x")
	}
	// Projects lexically AFTER the cap is hit. Under SkipAll they were never
	// visited at all, so no marker of theirs was ever observed.
	mustWrite(t, filepath.Join(home, "zzz-outer", "zzz-one", "go.mod"), "module one")
	mustWrite(t, filepath.Join(home, "zzz-outer", "zzz-two", ".git", "config"), "[core]")

	ix := newFileIndex(home)
	ix.maxPaths = 3

	files, filesTrunc := ix.snapshot(kindFiles)
	if len(files) != 3 {
		t.Fatalf("the file cap must still bound the file set: got %d (%v)", len(files), rels(files))
	}
	if !filesTrunc {
		t.Fatal("hitting maxPaths must still report the FILE set truncated")
	}

	dirs, dirsTrunc := ix.snapshot(kindDirs)
	got := indexDirs(t, ix)
	for _, want := range []string{"zzz-outer", "zzz-outer/zzz-one", "zzz-outer/zzz-two"} {
		if !slices.Contains(got, want) {
			t.Fatalf("the file cap killed the directory walk: %q missing from %v", want, got)
		}
	}
	if dirsTrunc {
		t.Fatalf("the FILE cap must not report the DIRECTORY set truncated (got dirs=%v)", rels(dirs))
	}
}

// emit is the ONLY writer of dirsTrunc for a dropped candidate — the fairness
// prune deliberately no longer flags it, because that branch fires for every
// directory under a spent top-level, including ones the project rules would
// never have offered. This pins the writer that remains.
func TestPerRootDirCapReportsTruncation(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < 8; i++ {
		mustWrite(t, filepath.Join(home, "big", fmt.Sprintf("p%02d", i), "go.mod"), "module p")
	}
	ix := newFileIndex(home)
	ix.maxPerRootDirs = 3 // "big" itself plus two of its projects

	dirs, dirsTrunc := ix.snapshot(kindDirs)
	if len(dirs) != 3 {
		t.Fatalf("the per-root directory cap must bound the set: got %d (%v)", len(dirs), rels(dirs))
	}
	if !dirsTrunc {
		t.Fatal("dropping a real candidate to the per-root cap must report the DIRECTORY set truncated")
	}
}

// The other half of the same flag, and the one emit CANNOT cover. When both
// budgets for a top-level are spent the conjunction prunes the subtree
// UNVISITED, so a marker inside it is never observed and emit is never called
// for that candidate at all — a refusal flag cannot report what it never
// received.
//
// The sibling above leaves maxPerRoot at its default, so the conjunction never
// fires there and it exercises emit's refusal path only. That gap is exactly how
// this false negative slipped through review.
func TestPrunedSubtreeReportsTruncation(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < 8; i++ {
		mustWrite(t, filepath.Join(home, "big", fmt.Sprintf("p%02d", i), "go.mod"), "module p")
	}
	ix := newFileIndex(home)
	ix.maxPerRootDirs = 3
	ix.maxPerRoot = 1 // both budgets spend, so the conjunction prunes

	dirs, dirsTrunc := ix.snapshot(kindDirs)
	if len(dirs) >= 9 {
		t.Fatalf("setup did not actually prune anything: %v", rels(dirs))
	}
	if !dirsTrunc {
		t.Fatalf("a project pruned UNVISITED must report the DIRECTORY set truncated (got dirs=%v)", rels(dirs))
	}
}

// The deadline is the other kind of stop, and the distinction is the whole
// point: budget exhaustion ends the walk outright, so both sets really ARE cut
// short mid-tree and both flags must say so. A truncated list presented as
// complete is the failure this pins.
func TestDeadlineTruncatesBothSets(t *testing.T) {
	home := t.TempDir()
	// The clock is only checked every 512 entries, so the fixture must exceed
	// that or the deadline is never observed.
	for i := 0; i < 600; i++ {
		mustWrite(t, filepath.Join(home, "d", fmt.Sprintf("f%03d.txt", i)), "x")
	}
	ix := newFileIndex(home)
	ix.budget = -time.Second // already expired when the walk starts

	_, filesTrunc := ix.snapshot(kindFiles)
	_, dirsTrunc := ix.snapshot(kindDirs)
	if !filesTrunc || !dirsTrunc {
		t.Fatalf("budget exhaustion must truncate BOTH sets: filesTrunc=%v dirsTrunc=%v", filesTrunc, dirsTrunc)
	}
}

// dpSkipWorkspaceNames in static/dirpicker.js is a hand-maintained literal copy
// of skipDirRecord's keys — the picker filters its empty-state workspace list
// with it, because /api/workspaces is deliberately unfiltered (it also feeds the
// New-session form). Nothing at runtime compares the two, and one drift
// direction is completely silent: a key added to the JS set that Go does not
// have makes a real workspace vanish from the empty-state list with no error,
// no empty result, and nothing in a log. The other direction is only slightly
// louder — a key added to Go alone leaves the picker offering a directory that
// its own fuzzy search can never find, the self-contradiction the JS set exists
// to prevent.
//
// So the comparison is done here, by reading the source text. That is in idiom
// for this repo: scripts/lint.sh already analyses static/*.js as text. Failing
// in BOTH directions is the point; a one-way superset check would pass through
// the silent case.
func TestDirPickerSkipSetMatchesSkipDirRecord(t *testing.T) {
	const jsPath = "static/dirpicker.js"
	src, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("read %s: %v", jsPath, err)
	}

	// Anchored on the declaration, not on a bare list of strings, so a rename or
	// a reformat fails here rather than silently comparing nothing.
	decl := regexp.MustCompile(`dpSkipWorkspaceNames\s*=\s*new Set\(\[([^\]]*)\]`)
	m := decl.FindSubmatch(src)
	if m == nil {
		t.Fatalf("could not find the `dpSkipWorkspaceNames = new Set([...])` literal in %s — "+
			"if it was renamed or restructured, update this test; it is the only thing "+
			"holding that set and skipDirRecord together", jsPath)
	}

	js := map[string]bool{}
	for _, q := range regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(string(m[1]), -1) {
		js[q[1]] = true
	}
	if len(js) == 0 {
		t.Fatalf("%s: dpSkipWorkspaceNames parsed as empty; the literal must use double-quoted strings", jsPath)
	}

	var missingInJS, missingInGo []string
	for k := range skipDirRecord {
		if !js[k] {
			missingInJS = append(missingInJS, k)
		}
	}
	for k := range js {
		if !skipDirRecord[k] {
			missingInGo = append(missingInGo, k)
		}
	}
	sort.Strings(missingInJS)
	sort.Strings(missingInGo)

	if len(missingInJS) > 0 {
		t.Errorf("%s is missing %v — skipDirRecord (fssearch.go) has these keys and the picker does not, "+
			"so the empty-state list offers directories the fuzzy search can never find; add them to dpSkipWorkspaceNames",
			jsPath, missingInJS)
	}
	if len(missingInGo) > 0 {
		t.Errorf("skipDirRecord (fssearch.go) is missing %v — %s filters these out with no counterpart in Go, "+
			"so a real workspace disappears from the picker's empty state with no signal at all; "+
			"either add them to skipDirRecord or drop them from dpSkipWorkspaceNames",
			missingInGo, jsPath)
	}
}

func hitRels(out searchResp) []string {
	r := make([]string, 0, len(out.Results))
	for _, h := range out.Results {
		r = append(r, h.Rel)
	}
	return r
}

func TestSearchPreferRanksTheSubtreeFirst(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, "aaa", "peek.go"))                         // shorter path: wins on score alone
	mustWrite(t, filepath.Join(home, "proj", "internal", "session", "peek.go")) // the focused session's repo
	s := &Server{Home: home}
	_, plain := doSearch(t, s, "q=peek")
	if len(plain.Results) < 2 || plain.Results[0].Rel != filepath.Join("aaa", "peek.go") {
		t.Fatalf("baseline changed: %v", hitRels(plain))
	}
	_, out := doSearch(t, s, "q=peek&prefer="+url.QueryEscape(filepath.Join(home, "proj")))
	want := filepath.Join("proj", "internal", "session", "peek.go")
	if len(out.Results) != 2 || out.Results[0].Rel != want || out.Results[0].Path != filepath.Join(home, want) {
		t.Fatalf("prefer did not rank its subtree first (or listed a file twice): %+v", out.Results)
	}
}

func TestSearchPreferWalksItsOwnSubtreeWhenTheHomeIndexIsTruncated(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, "aaa", "filler.txt"))
	mustWrite(t, filepath.Join(home, "zzz", "target-file.go"))
	s := &Server{Home: home}
	s.idx = newFileIndex(home)
	s.idx.maxPaths = 1 // the home walk fills up on aaa/ and never records zzz/
	_, plain := doSearch(t, s, "q=target")
	if len(plain.Results) != 0 || !plain.Truncated {
		t.Fatalf("setup: want a truncated miss, got %+v", plain)
	}
	_, out := doSearch(t, s, "q=target&prefer="+url.QueryEscape(filepath.Join(home, "zzz")))
	if len(out.Results) != 1 || out.Results[0].Rel != filepath.Join("zzz", "target-file.go") {
		t.Fatalf("prefer must walk its own subtree: %+v", out.Results)
	}
}

func TestSearchPreferIgnoresUnusableValues(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, filepath.Join(home, "proj", "notes.md"))
	mustWrite(t, filepath.Join(home, "other", "notes-2.md"))
	mustWrite(t, filepath.Join(outside, "notes-outside.md"))
	if err := os.Symlink(outside, filepath.Join(home, "link-out")); err != nil {
		t.Skip("symlink not supported")
	}
	s := &Server{Home: home}
	_, plain := doSearch(t, s, "q=notes")
	for _, p := range []string{
		"proj",                                  // relative
		home,                                    // $HOME itself: nothing to prefer
		filepath.Join(home, "missing"),          // does not exist
		filepath.Join(home, "proj", "notes.md"), // a file
		outside,                                 // outside home
		filepath.Join(home, "link-out"),         // a symlink out of home
		"/",
	} {
		rr, out := doSearch(t, s, "q=notes&prefer="+url.QueryEscape(p))
		if rr.Code != http.StatusOK {
			t.Fatalf("prefer=%q: code %d, want 200 (ignored, never an error)", p, rr.Code)
		}
		if !reflect.DeepEqual(hitRels(out), hitRels(plain)) {
			t.Errorf("prefer=%q changed the results: %v vs %v", p, hitRels(out), hitRels(plain))
		}
		for _, r := range out.Results {
			if strings.Contains(r.Rel, "outside") {
				t.Fatalf("prefer=%q escaped home: %v", p, hitRels(out))
			}
		}
	}
}

func TestSearchPreferKeepsSkipRules(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, ".claude", "settings.json"))
	mustWrite(t, filepath.Join(home, ".claude", "projects", "slug", "transcript.json"))
	s := &Server{Home: home}
	_, out := doSearch(t, s, "q=json&prefer="+url.QueryEscape(filepath.Join(home, ".claude")))
	for _, r := range out.Results {
		if strings.Contains(r.Rel, "projects") {
			t.Fatalf("skipRelPaths lost under a prefer root: %v", hitRels(out))
		}
	}
	if len(out.Results) != 1 {
		t.Fatalf("want settings.json only, got %v", hitRels(out))
	}
}

func TestSearchPreferIsIgnoredForDirectories(t *testing.T) {
	home := t.TempDir()
	mustMkdirAll(t, filepath.Join(home, "zproj", ".git"))
	mustMkdirAll(t, filepath.Join(home, "aproj", ".git"))
	s := &Server{Home: home}
	_, plain := doSearch(t, s, "q=proj&kind=dir")
	_, out := doSearch(t, s, "q=proj&kind=dir&prefer="+url.QueryEscape(filepath.Join(home, "zproj")))
	if !reflect.DeepEqual(hitRels(out), hitRels(plain)) {
		t.Fatalf("the directory picker must not change: %v vs %v", hitRels(out), hitRels(plain))
	}
}

func TestPreferIndexEvictsTheLeastRecentlyUsed(t *testing.T) {
	s := &Server{Home: t.TempDir()}
	for _, r := range []string{"/a", "/b", "/c", "/d"} {
		s.preferIndex(r, "")
	}
	s.preferIndex("/a", "") // touch /a: /b is now the oldest
	s.preferIndex("/e", "")
	if len(s.prefIdx) != maxPreferIndexes {
		t.Fatalf("%d indexes kept, want %d", len(s.prefIdx), maxPreferIndexes)
	}
	if _, ok := s.prefIdx["/b"]; ok {
		t.Fatal("the least recently used index was not evicted")
	}
	if _, ok := s.prefIdx["/a"]; !ok {
		t.Fatal("a recently used index was evicted")
	}
}

// resolvePrefer must reject (ignore silently, not error) a prefer root that
// is itself a skipped directory or sits under a skipped home-relative path —
// a directory a user would never deliberately search first, and one the
// walk itself would refuse to index anyway (Minor pulled into the control-
// actions final fix wave).
func TestResolvePreferRejectsSkippedRoots(t *testing.T) {
	home := t.TempDir()
	mustMkdirAll(t, filepath.Join(home, ".ssh"))
	mustMkdirAll(t, filepath.Join(home, ".aws"))
	mustMkdirAll(t, filepath.Join(home, ".claude", "projects", "slug"))
	mustMkdirAll(t, filepath.Join(home, "proj", "node_modules", "leftpad"))
	mustMkdirAll(t, filepath.Join(home, "proj", "src"))

	for _, rel := range []string{
		".ssh",                      // skipDirs, at the root
		".aws",                      // skipDirs, at the root
		"proj/node_modules",         // skipDirs, nested
		"proj/node_modules/leftpad", // skipDirs, nested deeper still
		".claude/projects",          // skipRelPaths, exact
		".claude/projects/slug",     // skipRelPaths, a prefix of it
	} {
		if _, _, ok := resolvePrefer(home, filepath.Join(home, rel)); ok {
			t.Errorf("prefer=%q: resolved ok=true, want it silently ignored", rel)
		}
	}

	// A sibling that shares no path with a skip rule must still resolve.
	if _, rel, ok := resolvePrefer(home, filepath.Join(home, "proj", "src")); !ok || rel != filepath.Join("proj", "src") {
		t.Fatalf("resolvePrefer(proj/src) = rel=%q ok=%v, want it to resolve normally", rel, ok)
	}
}

// A brand-new prefer index must build under its own, smaller budget: the
// first palette search after boot can build the home index (up to
// indexBudget) and a brand-new prefer index back to back in one request, and
// two indexBudget-sized builds would sum to the client's PAL_FETCH_MS abort.
func TestPreferIndexUsesItsOwnBudget(t *testing.T) {
	s := &Server{Home: t.TempDir()}
	ix := s.preferIndex(filepath.Join(s.Home, "proj"), "proj")
	if ix.budget != preferIndexBudget {
		t.Fatalf("preferIndex budget = %v, want preferIndexBudget (%v)", ix.budget, preferIndexBudget)
	}
	if preferIndexBudget >= indexBudget {
		t.Fatalf("preferIndexBudget (%v) must be smaller than the home index's indexBudget (%v)", preferIndexBudget, indexBudget)
	}
	// The home index itself keeps the larger budget.
	home := newFileIndex(s.Home)
	if home.budget != indexBudget {
		t.Fatalf("home index budget = %v, want indexBudget (%v)", home.budget, indexBudget)
	}
}
