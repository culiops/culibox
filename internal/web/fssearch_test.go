package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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
func TestIndexCollectsDirectories(t *testing.T) {
	home := t.TempDir()
	mustMkdirAll(t, filepath.Join(home, "repo", "internal", "web"))
	mustWrite(t, filepath.Join(home, "repo", "internal", "web", "a.go"), "package web")

	ix := newFileIndex(home)
	dirs, _ := ix.snapshot(kindDirs)
	got := rels(dirs)
	for _, want := range []string{"repo", "repo/internal", "repo/internal/web"} {
		if !slices.Contains(got, want) {
			t.Fatalf("directory %q missing from %v", want, got)
		}
	}
	// The file set must not have gained directories.
	files, _ := ix.snapshot(kindFiles)
	if slices.Contains(rels(files), "repo/internal") {
		t.Fatalf("a directory leaked into the file set: %v", rels(files))
	}
}

// vendor/ is pruned from RECORDING only. Adding it to skipDirs would delete
// every file under it from the palette, which is a regression, not a benefit.
func TestVendorIsNotRecordedButItsFilesAre(t *testing.T) {
	home := t.TempDir()
	mustMkdirAll(t, filepath.Join(home, "app", "vendor", "lib"))
	mustWrite(t, filepath.Join(home, "app", "vendor", "lib", "x.php"), "<?php")

	ix := newFileIndex(home)
	dirs, _ := ix.snapshot(kindDirs)
	for _, d := range rels(dirs) {
		if d == "app/vendor" || d == "app/vendor/lib" {
			t.Fatalf("vendor directory was recorded: %v", rels(dirs))
		}
	}
	files, _ := ix.snapshot(kindFiles)
	if !slices.Contains(rels(files), "app/vendor/lib/x.php") {
		t.Fatalf("a file under vendor must still be findable: %v", rels(files))
	}
}

// The fairness prune is a CONJUNCTION. A subtree that has spent its file budget
// must keep yielding directories, or the biggest repos — exactly the ones worth
// searching — go missing.
func TestFileBudgetExhaustionStillYieldsDirectories(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < 12; i++ {
		d := filepath.Join(home, "big", fmt.Sprintf("d%02d", i))
		mustMkdirAll(t, d)
		mustWrite(t, filepath.Join(d, "f.txt"), "x")
	}
	ix := newFileIndex(home)
	ix.maxPerRoot = 3 // file budget for "big" is spent almost immediately
	ix.maxPerRootDirs = 100

	dirs, _ := ix.snapshot(kindDirs)
	if n := len(rels(dirs)); n < 12 {
		t.Fatalf("file-budget exhaustion pruned the directory set: got %d, want >= 12 (%v)", n, rels(dirs))
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

// Hitting the global file ceiling returns filepath.SkipAll, which ends the
// WalkDir call outright — the directory walk dies with it, mid-tree. Flagging
// only filesTrunc there would leave dirsTrunc false while directories
// lexically after the abort point are silently missing: a truncated list
// presented as complete.
func TestMaxPathsAbortAlsoTruncatesDirectories(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < 6; i++ {
		d := filepath.Join(home, fmt.Sprintf("a%02d", i))
		mustMkdirAll(t, d)
		mustWrite(t, filepath.Join(d, "f.txt"), "x")
	}
	ix := newFileIndex(home)
	ix.maxPaths = 2 // hit well before the walk would otherwise finish

	_, filesTrunc := ix.snapshot(kindFiles)
	if !filesTrunc {
		t.Fatal("hitting maxPaths must report the FILE set truncated")
	}
	dirs, dirsTrunc := ix.snapshot(kindDirs)
	if !dirsTrunc {
		t.Fatalf("hitting maxPaths must also report the DIRECTORY set truncated (got dirs=%v)", rels(dirs))
	}
	if len(dirs) >= 6 {
		t.Fatalf("test setup did not actually lose any directories to the abort: got %v", rels(dirs))
	}
}
