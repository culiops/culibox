package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func mustWrite(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
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

func TestFileIndexSkipsNoiseDirs(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, "keep.go"))
	mustWrite(t, filepath.Join(home, "proj", "main.go"))
	mustWrite(t, filepath.Join(home, ".git", "config"))
	mustWrite(t, filepath.Join(home, ".hidden", "secret.txt"))
	mustWrite(t, filepath.Join(home, "proj", "node_modules", "left-pad", "index.js"))
	mustWrite(t, filepath.Join(home, "proj", "vendor", "dep.go"))
	mustWrite(t, filepath.Join(home, "proj", ".env"))

	ix := newFileIndex(home)
	paths, _ := ix.snapshot()
	joined := strings.Join(paths, "\n")

	for _, want := range []string{"keep.go", filepath.Join("proj", "main.go")} {
		if !contains(paths, want) {
			t.Fatalf("expected %q in index, got:\n%s", want, joined)
		}
	}
	for _, bad := range []string{".git", ".hidden", "node_modules", "vendor", ".env"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("index must not contain %q, got:\n%s", bad, joined)
		}
	}
}

func TestFileIndexRespectsMaxDepth(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, "a", "shallow.txt"))
	mustWrite(t, filepath.Join(home, "a", "b", "c", "deep.txt"))

	ix := newFileIndex(home)
	ix.maxDepth = 2 // at most 2 path elements: "a/shallow.txt" in, "a/b/c/deep.txt" out
	paths, _ := ix.snapshot()

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
	paths, truncated := ix.snapshot()

	if len(paths) != 5 {
		t.Fatalf("want 5 paths, got %d", len(paths))
	}
	if !truncated {
		t.Fatal("hitting maxPaths must report truncated")
	}
}

func TestFileIndexCachesUntilTTL(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, "one.txt"))

	ix := newFileIndex(home)
	if paths, _ := ix.snapshot(); len(paths) != 1 {
		t.Fatalf("want 1 path, got %v", paths)
	}
	mustWrite(t, filepath.Join(home, "two.txt"))

	// Inside the TTL the new file must NOT appear (that is the documented trade).
	if paths, _ := ix.snapshot(); len(paths) != 1 {
		t.Fatalf("cached snapshot should still hold 1 path, got %v", paths)
	}
	// Expiring the TTL rebuilds.
	ix.ttl = time.Nanosecond
	time.Sleep(2 * time.Millisecond)
	if paths, _ := ix.snapshot(); len(paths) != 2 {
		t.Fatalf("expired snapshot should rebuild to 2 paths, got %v", paths)
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

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
