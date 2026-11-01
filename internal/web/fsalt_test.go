package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type altCand struct {
	Path     string `json:"path"`
	Worktree string `json:"worktree"`
	Mtime    int64  `json:"mtime"`
}

func altGet(t *testing.T, s *Server, base, rel string) []altCand {
	t.Helper()
	rr := altReq(s, base, rel)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rr.Code, rr.Body)
	}
	if strings.Contains(rr.Body.String(), "null") {
		t.Fatalf("body has null: %s", rr.Body)
	}
	var out struct {
		Candidates []altCand `json:"candidates"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || out.Candidates == nil {
		t.Fatalf("body = %s (%v)", rr.Body, err)
	}
	return out.Candidates
}

func altReq(s *Server, base, rel string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	q := "?base=" + url.QueryEscape(base) + "&rel=" + url.QueryEscape(rel)
	s.handleFsAlternates(rr, httptest.NewRequest(http.MethodGet, "/api/fs/alternates"+q, nil))
	return rr
}

// altRepo makes a main checkout at top with live linked worktrees named names
// under .claude/worktrees, returning their dirs.
func altRepo(t *testing.T, top string, names ...string) []string {
	t.Helper()
	mainRepo(t, top)
	var out []string
	for _, n := range names {
		gd := filepath.Join(top, ".git", "worktrees", n)
		mustMkdirAll(t, gd)
		wt := filepath.Join(top, ".claude", "worktrees", n)
		mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+gd+"\n")
		out = append(out, wt)
	}
	return out
}

func TestAltNone(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	altRepo(t, repo, "a")
	if got := altGet(t, s, repo, "x/y.go"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestAltOne(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	wts := altRepo(t, repo, "a")
	f := filepath.Join(wts[0], "x", "y.go")
	mustWrite(t, f)
	got := altGet(t, s, repo, "x/y.go")
	if len(got) != 1 || got[0].Path != f || got[0].Worktree != "a" || got[0].Mtime == 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestAltFromSubdirBase(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	wts := altRepo(t, repo, "a")
	mustMkdirAll(t, filepath.Join(repo, "internal"))
	f := filepath.Join(wts[0], "internal", "bundle", "gc.go")
	mustWrite(t, f)
	got := altGet(t, s, filepath.Join(repo, "internal"), "bundle/gc.go")
	if len(got) != 1 || got[0].Path != f {
		t.Fatalf("got %+v", got)
	}
}

func TestAltMany(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	wts := altRepo(t, repo, "a", "b", "c")
	now := time.Now()
	for i, d := range []time.Duration{-3 * time.Hour, -1 * time.Hour, -2 * time.Hour} {
		f := filepath.Join(wts[i], "f.txt")
		mustWrite(t, f)
		if err := os.Chtimes(f, now.Add(d), now.Add(d)); err != nil {
			t.Fatal(err)
		}
	}
	got := altGet(t, s, repo, "f.txt")
	if len(got) != 3 || got[0].Worktree != "b" || got[1].Worktree != "c" || got[2].Worktree != "a" {
		t.Fatalf("got %+v", got)
	}
}

func TestAltTieBreaksByPath(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	wts := altRepo(t, repo, "b", "a")
	at := time.Now().Add(-time.Hour)
	for _, wt := range wts {
		f := filepath.Join(wt, "f.txt")
		mustWrite(t, f)
		os.Chtimes(f, at, at)
	}
	got := altGet(t, s, repo, "f.txt")
	if len(got) != 2 || got[0].Worktree != "a" {
		t.Fatalf("got %+v", got)
	}
}

func TestAltBaseInsideWorktree(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	wts := altRepo(t, repo, "a", "b")
	mustWrite(t, filepath.Join(wts[1], "f.txt"))
	mustWrite(t, filepath.Join(wts[0], "f.txt"))
	if got := altGet(t, s, wts[0], "f.txt"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestAltRejectsDotDot(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	altRepo(t, repo, "a")
	for _, rel := range []string{"../../etc/passwd", "a/../../x", "/etc/passwd", ""} {
		if rel == "" || rel[0] == '/' {
			if rr := altReq(s, repo, rel); rr.Code != http.StatusBadRequest && rr.Code != http.StatusOK {
				t.Fatalf("rel %q: code %d", rel, rr.Code)
			}
			continue
		}
		if got := altGet(t, s, repo, rel); len(got) != 0 {
			t.Fatalf("rel %q: got %+v", rel, got)
		}
	}
	// A real file reachable only by climbing out of the worktree is not served.
	wts := altRepo(t, filepath.Join(h, "r2"), "w")
	mustWrite(t, filepath.Join(h, "r2", ".claude", "secret.txt"))
	if got := altGet(t, s, filepath.Join(h, "r2"), "../secret.txt"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
	_ = wts
}

func TestAltSkipsDirCandidate(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	wts := altRepo(t, repo, "a")
	mustMkdirAll(t, filepath.Join(wts[0], "x", "y.go"))
	if got := altGet(t, s, repo, "x/y.go"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestAltSkipsFifoWithoutBlocking(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	wts := altRepo(t, repo, "a")
	if err := syscall.Mkfifo(filepath.Join(wts[0], "p"), 0o644); err != nil {
		t.Skip(err)
	}
	done := make(chan []altCand, 1)
	go func() { done <- altGet(t, s, repo, "p") }()
	select {
	case got := <-done:
		if len(got) != 0 {
			t.Fatalf("got %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked on a FIFO")
	}
}

func TestAltSymlinkedWorktreeEntrySkipped(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	altRepo(t, repo)
	real := filepath.Join(h, "elsewhere")
	mustMkdirAll(t, filepath.Join(repo, ".git", "worktrees", "l"))
	mustWrite(t, filepath.Join(real, ".git"), "gitdir: "+filepath.Join(repo, ".git", "worktrees", "l")+"\n")
	mustWrite(t, filepath.Join(real, "f.txt"))
	mustMkdirAll(t, filepath.Join(repo, ".claude", "worktrees"))
	mustSymlink(t, real, filepath.Join(repo, ".claude", "worktrees", "l"))
	if got := altGet(t, s, repo, "f.txt"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestAltSymlinkedWorktreesBaseSkipped(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	real := filepath.Join(h, "store")
	gd := filepath.Join(repo, ".git", "worktrees", "a")
	mustMkdirAll(t, gd)
	mustWrite(t, filepath.Join(real, "a", ".git"), "gitdir: "+gd+"\n")
	mustWrite(t, filepath.Join(real, "a", "f.txt"))
	mustMkdirAll(t, filepath.Join(repo, ".claude"))
	mustSymlink(t, real, filepath.Join(repo, ".claude", "worktrees"))
	if got := altGet(t, s, repo, "f.txt"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestAltGitPointingElsewhereSkipped(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	other := filepath.Join(h, "other")
	altRepo(t, repo)
	altRepo(t, other, "o")
	wt := filepath.Join(repo, ".claude", "worktrees", "o")
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(other, ".git", "worktrees", "o")+"\n")
	mustWrite(t, filepath.Join(wt, "f.txt"))
	if got := altGet(t, s, repo, "f.txt"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestAltSymlinkedFileCandidateSkipped(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	wts := altRepo(t, repo, "a")
	mustWrite(t, filepath.Join(h, "real.txt"))
	mustSymlink(t, filepath.Join(h, "real.txt"), filepath.Join(wts[0], "f.txt"))
	if got := altGet(t, s, repo, "f.txt"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestAltBadParams(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	altRepo(t, repo, "a")
	for _, c := range [][2]string{{"", "f"}, {"relative/dir", "f"}, {repo, ""}} {
		if rr := altReq(s, c[0], c[1]); rr.Code != http.StatusBadRequest {
			t.Fatalf("%v: code %d, want 400", c, rr.Code)
		}
	}
}

func TestAltRefusalsAreEmpty(t *testing.T) {
	s, h, _ := hostGitServer(t)
	// Not a repo, missing base, base outside $HOME: all 200 + [].
	mustMkdirAll(t, filepath.Join(h, "plain"))
	for _, base := range []string{filepath.Join(h, "plain"), filepath.Join(h, "nope"), t.TempDir()} {
		if got := altGet(t, s, base, "f.txt"); len(got) != 0 {
			t.Fatalf("base %s: got %+v", base, got)
		}
	}
	// base is a file.
	mustWrite(t, filepath.Join(h, "file.txt"))
	if got := altGet(t, s, filepath.Join(h, "file.txt"), "f.txt"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestAltSubmoduleIsNotMain(t *testing.T) {
	s, h, _ := hostGitServer(t)
	// A checkout whose .git file names a non-worktrees gitdir is a submodule.
	sub := filepath.Join(h, "sub")
	gd := filepath.Join(h, "super", ".git", "modules", "sub")
	mustMkdirAll(t, gd)
	mustWrite(t, filepath.Join(sub, ".git"), "gitdir: "+gd+"\n")
	if got := altGet(t, s, sub, "f.txt"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestAltBoxedOK(t *testing.T) {
	s, _, ws := boxedServer(t)
	wts := altRepo(t, ws, "a")
	f := filepath.Join(wts[0], "x", "y.go")
	mustWrite(t, f)
	got := altGet(t, s, ws, "x/y.go")
	if len(got) != 1 || got[0].Path != f {
		t.Fatalf("got %+v", got)
	}
}

func TestAltBoxedLinkOutOfRootSkipped(t *testing.T) {
	s, home, ws := boxedServer(t)
	wts := altRepo(t, ws, "a")
	secret := filepath.Join(home, "secret.txt")
	mustWrite(t, secret)
	mustSymlink(t, secret, filepath.Join(wts[0], "f.txt"))
	if got := altGet(t, s, ws, "f.txt"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestAltBoxedRepoAboveContainedRoot(t *testing.T) {
	s, home, ws := boxedServer(t)
	// The repo is the host dir above the contained root; the base inside the
	// root must not reach worktrees outside it.
	altRepo(t, home, "a")
	mustWrite(t, filepath.Join(home, ".claude", "worktrees", "a", "ws", "f.txt"))
	if got := altGet(t, s, ws, "f.txt"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestAltHostRepoBesideBoxedTrees(t *testing.T) {
	s, h, _ := boxedServer(t)
	repo := filepath.Join(h, "repo")
	wts := altRepo(t, repo, "a")
	mustWrite(t, filepath.Join(wts[0], "f.txt"))
	if got := altGet(t, s, repo, "f.txt"); len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
}
