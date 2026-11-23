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
// under .claude/worktrees, registered in .git/worktrees as git does, returning their dirs.
func altRepo(t *testing.T, top string, names ...string) []string {
	t.Helper()
	mainRepo(t, top)
	var out []string
	for _, n := range names {
		gd := filepath.Join(top, ".git", "worktrees", n)
		mustMkdirAll(t, gd)
		wt := filepath.Join(top, ".claude", "worktrees", n)
		mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+gd+"\n")
		mustWrite(t, filepath.Join(gd, "gitdir"), filepath.Join(wt, ".git")+"\n") // git's registry: the pointer back
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

func TestAltBaseInWorktreeFindsMainOnly(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	wts := altRepo(t, repo, "a")
	f := filepath.Join(repo, "docs", "superpowers", "plan.md") // gitignored: main only
	mustWrite(t, f)
	got := altGet(t, s, wts[0], "docs/superpowers/plan.md")
	if len(got) != 1 || got[0].Path != f || got[0].Worktree != "" || got[0].Mtime == 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestAltBaseInWorktreeSubdirAppliesPrefix(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	wts := altRepo(t, repo, "a")
	mustMkdirAll(t, filepath.Join(wts[0], "internal"))
	f := filepath.Join(repo, "internal", "bundle", "gc.go")
	mustWrite(t, f)
	got := altGet(t, s, filepath.Join(wts[0], "internal"), "bundle/gc.go")
	if len(got) != 1 || got[0].Path != f || got[0].Worktree != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestAltBaseInWorktreeMainAndSibling(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	wts := altRepo(t, repo, "a", "b")
	now := time.Now()
	mf, bf, af := filepath.Join(repo, "f.txt"), filepath.Join(wts[1], "f.txt"), filepath.Join(wts[0], "f.txt")
	for f, d := range map[string]time.Duration{mf: -2 * time.Hour, bf: -1 * time.Hour, af: 0} {
		mustWrite(t, f)
		os.Chtimes(f, now.Add(d), now.Add(d))
	}
	got := altGet(t, s, wts[0], "f.txt")
	// a is the base's own checkout and is never listed.
	if len(got) != 2 || got[0].Path != bf || got[0].Worktree != "b" || got[1].Path != mf || got[1].Worktree != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestAltBaseInMainNeverListsMain(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	altRepo(t, repo, "a")
	mustWrite(t, filepath.Join(repo, "f.txt"))
	if got := altGet(t, s, repo, "f.txt"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestAltBaseInOutOfTreeWorktreeFindsMain(t *testing.T) {
	// A linked worktree that lives beside the repo, not under .claude/worktrees.
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	altRepo(t, repo)
	gd := filepath.Join(repo, ".git", "worktrees", "x")
	mustMkdirAll(t, gd)
	wt := filepath.Join(h, "wt")
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+gd+"\n")
	f := filepath.Join(repo, "f.txt")
	mustWrite(t, f)
	got := altGet(t, s, wt, "f.txt")
	if len(got) != 1 || got[0].Path != f || got[0].Worktree != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestAltBoxedBaseInWorktreeFindsMain(t *testing.T) {
	s, _, ws := boxedServer(t)
	wts := altRepo(t, ws, "a")
	f := filepath.Join(ws, "docs", "p.md")
	mustWrite(t, f)
	got := altGet(t, s, wts[0], "docs/p.md")
	if len(got) != 1 || got[0].Path != f || got[0].Worktree != "" {
		t.Fatalf("got %+v", got)
	}
}

// A worktree beside the repository (git worktree add ../x), not under .claude/worktrees, is a checkout too.
func TestFsAlternatesSiblingWorktree(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	altRepo(t, repo)
	gd := filepath.Join(repo, ".git", "worktrees", "side")
	wt := filepath.Join(h, "repo-side")
	mustWrite(t, filepath.Join(gd, "gitdir"), filepath.Join(wt, ".git")+"\n")
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+gd+"\n")
	f := filepath.Join(wt, "f.txt")
	mustWrite(t, f)
	got := altGet(t, s, repo, "f.txt")
	if len(got) != 1 || got[0].Path != f || got[0].Worktree != "repo-side" {
		t.Fatalf("got %+v", got)
	}
}

// A registered worktree outside every served root is never offered (and nothing in the answer says it exists).
func TestFsAlternatesWorktreeOutsideRoots(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	altRepo(t, repo)
	out, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gd := filepath.Join(repo, ".git", "worktrees", "far")
	wt := filepath.Join(out, "far")
	mustWrite(t, filepath.Join(gd, "gitdir"), filepath.Join(wt, ".git")+"\n")
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+gd+"\n")
	mustWrite(t, filepath.Join(wt, "f.txt"))
	rr := altReq(s, repo, "f.txt")
	if rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != `{"candidates":[]}` {
		t.Fatalf("code %d body %s", rr.Code, rr.Body)
	}
}

func TestAltRejectsDotDot(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	altRepo(t, repo, "a")
	for _, rel := range []string{"../../etc/passwd", "a/../../x", "/etc/passwd", "", "."} {
		rr := altReq(s, repo, rel)
		if rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != `{"candidates":[]}` {
			t.Fatalf("rel %q: code %d body %s", rel, rr.Code, rr.Body)
		}
	}
	// A real file reachable only by climbing out of the checkout is not served.
	r2 := filepath.Join(h, "r2")
	altRepo(t, r2, "w")
	mustWrite(t, filepath.Join(r2, ".claude", "secret.txt"))
	if got := altGet(t, s, r2, "../secret.txt"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
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
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- altReq(s, repo, "p") }()
	select {
	case rr := <-done:
		if rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != `{"candidates":[]}` {
			t.Fatalf("code %d body %s", rr.Code, rr.Body)
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
	for _, c := range [][2]string{{"", "f"}, {"relative/dir", "f"}} {
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

// #364: a worktree dir named in git's registry is judged by path alone before anything stats it. The roots here do not
// exist, so a check that resolved or stat'ed the path could not say yes to any of them.
func TestServesByPathNeedsNoFilesystem(t *testing.T) {
	const b = "/nonexistent-364"
	p := fsPolicy{roots: []string{b + "/home/u", b + "/srv"}, scratch: map[fileKey]string{{dev: 1, ino: 2}: b + "/tmp/claude-1"}}
	host := fsTarget{Root: b + "/home/u", Abs: b + "/home/u/repo"}
	boxed := fsTarget{Root: b + "/home/u/ws", Abs: b + "/home/u/ws", Strict: true}
	for _, c := range []struct {
		req  fsTarget
		dir  string
		want bool
	}{
		{host, b + "/home/u/repo-wt", true},
		{host, b + "/home/u", true},
		{host, b + "/srv/x/.git", true},
		{host, b + "/tmp/claude-1/wt", true},
		{host, "/net/host/share/wt", false},
		{host, b + "/home/user/wt", false}, // whole components
		{host, b + "/home/u/../../etc", false},
		{host, "rel/wt", false},
		{boxed, b + "/home/u/ws/.claude/worktrees/a", true},
		{boxed, b + "/home/u/ws", true},
		{boxed, b + "/home/u/repo-wt", false}, // served for a host request, not in the boxed request's tree
		{boxed, b + "/home/u/wsx", false},
	} {
		if got := p.servesByPath(c.req, c.dir); got != c.want {
			t.Errorf("servesByPath(strict=%v, %q) = %v, want %v", c.req.Strict, c.dir, got, c.want)
		}
	}
}

// recordEval notes every path the web package's git and alternates code hands to EvalSymlinks, for the test's life.
func recordEval(t *testing.T) *[]string {
	t.Helper()
	var seen []string
	old := evalSymlinks
	evalSymlinks = func(p string) (string, error) { seen = append(seen, p); return old(p) }
	t.Cleanup(func() { evalSymlinks = old })
	return &seen
}

// noneUnder fails the test for every recorded path that is dir or below it.
func noneUnder(t *testing.T, what string, seen []string, dir string) {
	t.Helper()
	for _, p := range seen {
		if p == dir || strings.HasPrefix(p, dir+"/") {
			t.Errorf("%s touched %s: a path outside the served roots that a .git pointer chose", what, p)
		}
	}
}

// outsidePointer makes a checkout under home whose .git FILE points at a worktree gitdir of a repository outside every
// served root (the agent writes that file), returning the checkout and the outside repository.
func outsidePointer(t *testing.T, home string) (wt, outside string) {
	t.Helper()
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gd := filepath.Join(outside, ".git", "worktrees", "x")
	mustMkdirAll(t, gd)
	mustWrite(t, filepath.Join(outside, "f.txt"))
	wt = filepath.Join(home, "wt")
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+gd+"\n")
	mustWrite(t, filepath.Join(wt, "f.txt"))
	return wt, outside
}

// #364 review: the common dir and main checkout Locate derives from a .git file's text are judged by path before
// anything resolves them, on the dock's status poll and on the alternates lookup.
func TestGitPointerOutsideRootsNeverTouched(t *testing.T) {
	s, h, _ := hostGitServer(t)
	wt, outside := outsidePointer(t, h)
	seen := recordEval(t)
	if st := decodeStatus(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(wt))); st.State == "ok" {
		t.Errorf("status of a checkout whose git dir is outside the roots = %+v, want refused", st)
	}
	noneUnder(t, "status", *seen, outside)
	*seen = nil
	if got := altGet(t, s, wt, "f.txt"); len(got) != 0 {
		t.Errorf("alternates = %+v, want none", got)
	}
	noneUnder(t, "alternates", *seen, outside)
}

// The alternates lookup hands gitrepo.Worktrees its path gate: without it, a registered worktree outside every served
// root would reach this package's own checks (and be resolved) at all.
func TestAltWorktreeGateIsWired(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	altRepo(t, repo)
	mustWrite(t, filepath.Join(repo, "f.txt"))
	out, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gd := filepath.Join(repo, ".git", "worktrees", "far")
	far := filepath.Join(out, "far")
	mustWrite(t, filepath.Join(gd, "gitdir"), filepath.Join(far, ".git")+"\n")
	mustWrite(t, filepath.Join(far, ".git"), "gitdir: "+gd+"\n")
	mustWrite(t, filepath.Join(far, "f.txt"))
	seen := recordEval(t)
	if got := altGet(t, s, filepath.Join(h, "repo"), "f.txt"); len(got) != 0 {
		t.Errorf("alternates = %+v, want none", got)
	}
	noneUnder(t, "alternates", *seen, out)
}
