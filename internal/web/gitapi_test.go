package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/culiops/agent-box/internal/gitview"
)

// fakeGit records every Target it is handed and answers with canned results.
type fakeGit struct {
	mu       sync.Mutex
	statuses []gitview.Target
	diffs    []fakeDiffCall
	st       gitview.Status
	diff     gitview.Diff
}

type fakeDiffCall struct {
	T         gitview.Target
	Rel       string
	Untracked bool
}

func (f *fakeGit) Status(_ context.Context, t gitview.Target) gitview.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, t)
	st := f.st
	if st.State == "" {
		st = gitview.Status{State: "ok", Entries: []gitview.Entry{}}
	}
	st.Top = t.Top
	return st
}

func (f *fakeGit) Diff(_ context.Context, t gitview.Target, rel string, untracked bool) gitview.Diff {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.diffs = append(f.diffs, fakeDiffCall{t, rel, untracked})
	if f.diff.State == "" {
		return gitview.Diff{State: "ok", Kind: "text", Diff: "@@ x"}
	}
	return f.diff
}

func (f *fakeGit) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.statuses) + len(f.diffs)
}

// hostGitServer is a server with a resolved temp $HOME and no boxed trees.
func hostGitServer(t *testing.T) (*Server, string, *fakeGit) {
	t.Helper()
	h, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := &fakeGit{}
	return &Server{Home: h, Git: g}, h, g
}

func gitPost(h http.HandlerFunc, route, body string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest(http.MethodPost, route, strings.NewReader(body)))
	return rr
}

func pathBody(p string) string {
	b, _ := json.Marshal(map[string]string{"path": p})
	return string(b)
}

func decodeStatus(t *testing.T, rr *httptest.ResponseRecorder) gitview.Status {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rr.Code, rr.Body)
	}
	var st gitview.Status
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode %s: %v", rr.Body, err)
	}
	return st
}

func decodeDiff(t *testing.T, rr *httptest.ResponseRecorder) gitview.Diff {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rr.Code, rr.Body)
	}
	var d gitview.Diff
	if err := json.Unmarshal(rr.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode %s: %v", rr.Body, err)
	}
	return d
}

// wantRefused asserts a fixed "unavailable" answer and that git never ran.
func wantRefused(t *testing.T, rr *httptest.ResponseRecorder, g *fakeGit, reason string) {
	t.Helper()
	st := decodeStatus(t, rr)
	if st.State != "unavailable" || st.Reason != reason {
		t.Fatalf("status = %+v, want unavailable %q", st, reason)
	}
	if st.Entries == nil {
		t.Fatal("entries must be [] not null")
	}
	if n := g.calls(); n != 0 {
		t.Fatalf("runner called %d times for a refused target: %+v %+v", n, g.statuses, g.diffs)
	}
}

func mainRepo(t *testing.T, top string) {
	t.Helper()
	mustMkdirAll(t, filepath.Join(top, ".git"))
}

func TestGitStatusMethod(t *testing.T) {
	s, h, g := hostGitServer(t)
	for _, hf := range []http.HandlerFunc{s.handleGitStatus, s.handleGitDiff} {
		rr := httptest.NewRecorder()
		hf(rr, httptest.NewRequest(http.MethodGet, "/api/git/status?path="+h, nil))
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET code = %d, want 405", rr.Code)
		}
	}
	if g.calls() != 0 {
		t.Fatal("runner called on GET")
	}
}

func TestGitStatusBadBody(t *testing.T) {
	s, _, g := hostGitServer(t)
	for _, body := range []string{"", "{", `{"path":"rel/dir"}`, `{"path":""}`, `{"path":"/` + strings.Repeat("a", 9000) + `"}`} {
		for _, hf := range []http.HandlerFunc{s.handleGitStatus, s.handleGitDiff} {
			if rr := gitPost(hf, "/api/git/x", body); rr.Code != http.StatusBadRequest {
				t.Fatalf("body %.40q: code = %d, want 400 (%s)", body, rr.Code, rr.Body)
			}
		}
	}
	if g.calls() != 0 {
		t.Fatal("runner called on a bad body")
	}
}

func TestGitStatusCrossOriginRefused(t *testing.T) {
	s, h, g := hostGitServer(t)
	mainRepo(t, filepath.Join(h, "repo"))
	req := httptest.NewRequest(http.MethodPost, "/api/git/status", strings.NewReader(pathBody(filepath.Join(h, "repo"))))
	req.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()
	s.handleGitStatus(rr, req)
	if rr.Code != http.StatusForbidden || g.calls() != 0 {
		t.Fatalf("code = %d calls = %d, want 403 and no run", rr.Code, g.calls())
	}
}

func TestGitStatusNilRunner(t *testing.T) {
	s, h, _ := hostGitServer(t)
	s.Git = nil
	mainRepo(t, filepath.Join(h, "repo"))
	st := decodeStatus(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(filepath.Join(h, "repo"))))
	if st.State != "unavailable" || st.Reason != "Git status needs the box runtime (docker)" {
		t.Fatalf("status = %+v", st)
	}
	d := decodeDiff(t, gitPost(s.handleGitDiff, "/api/git/diff", pathBody(filepath.Join(h, "repo", "x"))))
	if d.State != "unavailable" || d.Reason != "Git status needs the box runtime (docker)" {
		t.Fatalf("diff = %+v", d)
	}
}

func TestGitStatusNotRepo(t *testing.T) {
	s, h, g := hostGitServer(t)
	dir := filepath.Join(h, "plain")
	mustMkdirAll(t, dir)
	st := decodeStatus(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(dir)))
	if st.State != "notrepo" || st.Entries == nil {
		t.Fatalf("status = %+v", st)
	}
	if g.calls() != 0 {
		t.Fatal("runner called for a non-repo")
	}
}

func TestGitStatusFsErrors(t *testing.T) {
	s, h, _ := hostGitServer(t)
	if rr := gitPost(s.handleGitStatus, "/api/git/status", pathBody(filepath.Join(h, "missing"))); rr.Code != http.StatusNotFound {
		t.Fatalf("missing: code = %d, want 404", rr.Code)
	}
	outside := t.TempDir()
	mainRepo(t, outside)
	if rr := gitPost(s.handleGitStatus, "/api/git/status", pathBody(outside)); rr.Code != http.StatusForbidden {
		t.Fatalf("outside home: code = %d, want 403", rr.Code)
	}
}

func TestGitStatusSubdirRoot(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	mustMkdirAll(t, filepath.Join(repo, "sub"))
	st := decodeStatus(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(filepath.Join(repo, "sub"))))
	if st.State != "ok" || st.Top != repo {
		t.Fatalf("status = %+v", st)
	}
	want := gitview.Target{Top: repo, Common: filepath.Join(repo, ".git")}
	if len(g.statuses) != 1 || g.statuses[0] != want {
		t.Fatalf("targets = %+v, want %+v", g.statuses, want)
	}
}

func TestGitStatusLinkedWorktreeInsideHome(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	mustMkdirAll(t, filepath.Join(repo, ".git", "worktrees", "w"))
	wt := filepath.Join(h, "wt")
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(repo, ".git", "worktrees", "w")+"\n")
	decodeStatus(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(wt)))
	want := gitview.Target{Top: wt, Common: filepath.Join(repo, ".git")}
	if len(g.statuses) != 1 || g.statuses[0] != want {
		t.Fatalf("targets = %+v, want %+v", g.statuses, want)
	}
}

func TestGitStatusLinkedWorktreeCommonOutsideHome(t *testing.T) {
	s, h, g := hostGitServer(t)
	other := t.TempDir() // a sibling of $HOME, not inside it
	mustMkdirAll(t, filepath.Join(other, ".git", "worktrees", "w"))
	wt := filepath.Join(h, "wt")
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(other, ".git", "worktrees", "w")+"\n")
	wantRefused(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(wt)), g, msgGitOutside)
}

func TestGitStatusRelativeGitdirClimbsOut(t *testing.T) {
	s, h, g := hostGitServer(t)
	wt := filepath.Join(h, "wt")
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: ../../../../../../../../tmp/x/.git/worktrees/w\n")
	wantRefused(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(wt)), g, msgGitOutside)
}

func TestGitStatusSymlinkedCommon(t *testing.T) {
	s, h, g := hostGitServer(t)
	real := filepath.Join(h, "real")
	mainRepo(t, real)
	mustMkdirAll(t, filepath.Join(real, ".git", "worktrees", "w"))
	mustSymlink(t, real, filepath.Join(h, "link"))
	wt := filepath.Join(h, "wt")
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(h, "link", ".git", "worktrees", "w")+"\n")
	wantRefused(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(wt)), g, msgGitOutside)
}

func TestGitStatusRequestThroughHostLinkResolves(t *testing.T) {
	// A host link to a repo is followed before Locate, so the Target is the
	// real checkout, never a path with a link in it.
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	mustSymlink(t, repo, filepath.Join(h, "alias"))
	decodeStatus(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(filepath.Join(h, "alias"))))
	want := gitview.Target{Top: repo, Common: filepath.Join(repo, ".git")}
	if len(g.statuses) != 1 || g.statuses[0] != want {
		t.Fatalf("targets = %+v, want %+v", g.statuses, want)
	}
}

func TestGitStatusTopHoldingDeniedTreeRefused(t *testing.T) {
	// $HOME as a repo would mount the data dir (a denied tree) into the container.
	s, h, _ := boxedServer(t)
	g := &fakeGit{}
	s.Git = g
	mainRepo(t, h)
	mustMkdirAll(t, filepath.Join(h, "proj"))
	wantRefused(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(filepath.Join(h, "proj"))), g, msgGitOutside)
}

func TestGitStatusUnsafeMountPathRefused(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "a,readonly=false")
	mainRepo(t, repo)
	wantRefused(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(repo)), g, msgGitNotAvailable)
}

func TestGitStatusBoxedOK(t *testing.T) {
	s, _, ws := boxedServer(t)
	g := &fakeGit{}
	s.Git = g
	mainRepo(t, ws)
	mustMkdirAll(t, filepath.Join(ws, "sub"))
	st := decodeStatus(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(filepath.Join(ws, "sub"))))
	// Boxed: the contained root is the only mount (Target.Root).
	want := gitview.Target{Root: ws, Top: ws, Common: filepath.Join(ws, ".git")}
	if st.State != "ok" || st.RootRel != "sub" || len(g.statuses) != 1 || g.statuses[0] != want {
		t.Fatalf("status = %+v targets = %+v", st, g.statuses)
	}
}

func TestGitStatusBoxedCommonOutsideContainedRoot(t *testing.T) {
	s, h, ws := boxedServer(t)
	g := &fakeGit{}
	s.Git = g
	// The common dir is under $HOME (it would pass a host check) but outside ws.
	other := filepath.Join(h, "other")
	mainRepo(t, other)
	mustMkdirAll(t, filepath.Join(other, ".git", "worktrees", "w"))
	wt := filepath.Join(ws, "wt")
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(other, ".git", "worktrees", "w")+"\n")
	wantRefused(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(wt)), g, msgGitOutside)
}

func TestGitStatusBoxedLinkToHostRepoRefused(t *testing.T) {
	// A box-planted link from its workspace to a host repo must not get that
	// host repo mounted.
	s, h, ws := boxedServer(t)
	g := &fakeGit{}
	s.Git = g
	hostRepo := filepath.Join(h, "hostrepo")
	mainRepo(t, hostRepo)
	mustSymlink(t, hostRepo, filepath.Join(ws, "escape"))
	wantRefused(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(filepath.Join(ws, "escape"))), g, msgGitOutside)
}

func TestGitStatusBoxedWalkUpLeavesRoot(t *testing.T) {
	// ws has no .git of its own; the nearest one is a host repo above it.
	s, h, ws := boxedServer(t)
	g := &fakeGit{}
	s.Git = g
	// Move the denied data dir out of the picture so only the root rule applies.
	s.BoxTrees = func() BoxTrees { return BoxTrees{Contained: []string{ws}} }
	mainRepo(t, h)
	wantRefused(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(ws)), g, msgGitOutside)
}

func TestGitStatusBoxedSymlinkedCommonInsideRoot(t *testing.T) {
	s, _, ws := boxedServer(t)
	g := &fakeGit{}
	s.Git = g
	real := filepath.Join(ws, "real")
	mainRepo(t, real)
	mustMkdirAll(t, filepath.Join(real, ".git", "worktrees", "w"))
	mustSymlink(t, real, filepath.Join(ws, "link"))
	wt := filepath.Join(ws, "wt")
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(ws, "link", ".git", "worktrees", "w")+"\n")
	wantRefused(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(wt)), g, msgGitOutside)
}

func TestGitStatusRunnerReasonNotForwarded(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	for raw, want := range map[string]string{
		"fatal: /secret/path: bad object deadbeef": msgGitNotAvailable,
		"git timed out": msgGitTimeout,
		"timed out waiting for a free git container": msgGitTimeout,
	} {
		g.st = gitview.Status{State: "unavailable", Reason: raw, Entries: []gitview.Entry{}}
		st := decodeStatus(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(repo)))
		if st.State != "unavailable" || st.Reason != want {
			t.Fatalf("raw %q: status = %+v, want reason %q", raw, st, want)
		}
	}
}

func TestGitDiffUntrackedFlag(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	mustWrite(t, filepath.Join(repo, "new.txt"))
	mustWrite(t, filepath.Join(repo, "old.txt"))
	g.st = gitview.Status{State: "ok", Entries: []gitview.Entry{{P: "new.txt", X: "U"}, {P: "old.txt", X: "M"}}}

	d := decodeDiff(t, gitPost(s.handleGitDiff, "/api/git/diff", pathBody(filepath.Join(repo, "new.txt"))))
	if d.State != "ok" {
		t.Fatalf("diff = %+v", d)
	}
	decodeDiff(t, gitPost(s.handleGitDiff, "/api/git/diff", pathBody(filepath.Join(repo, "old.txt"))))
	want := gitview.Target{Top: repo, Common: filepath.Join(repo, ".git")}
	if len(g.diffs) != 2 ||
		g.diffs[0] != (fakeDiffCall{want, "new.txt", true}) ||
		g.diffs[1] != (fakeDiffCall{want, "old.txt", false}) {
		t.Fatalf("diffs = %+v", g.diffs)
	}
	for _, st := range g.statuses {
		if st != want {
			t.Fatalf("status target = %+v", st)
		}
	}
}

func TestGitDiffUntrackedDir(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	mustWrite(t, filepath.Join(repo, "dir", "a", "b.txt"))
	mustWrite(t, filepath.Join(repo, "dirx", "c.txt"))
	g.st = gitview.Status{State: "ok", Entries: []gitview.Entry{{P: "dir/", X: "U"}}}
	decodeDiff(t, gitPost(s.handleGitDiff, "/api/git/diff", pathBody(filepath.Join(repo, "dir", "a", "b.txt"))))
	decodeDiff(t, gitPost(s.handleGitDiff, "/api/git/diff", pathBody(filepath.Join(repo, "dirx", "c.txt"))))
	if len(g.diffs) != 2 || g.diffs[0].Rel != "dir/a/b.txt" || !g.diffs[0].Untracked ||
		g.diffs[1].Rel != "dirx/c.txt" || g.diffs[1].Untracked {
		t.Fatalf("diffs = %+v", g.diffs)
	}
}

func TestGitDiffNoHeadIsUntracked(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	mustWrite(t, filepath.Join(repo, "a.txt"))
	g.st = gitview.Status{State: "ok", NoHead: true, Entries: []gitview.Entry{}}
	decodeDiff(t, gitPost(s.handleGitDiff, "/api/git/diff", pathBody(filepath.Join(repo, "a.txt"))))
	if len(g.diffs) != 1 || !g.diffs[0].Untracked || g.diffs[0].Rel != "a.txt" {
		t.Fatalf("diffs = %+v", g.diffs)
	}
}

func TestGitDiffLinkedWorktreeFile(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	mustMkdirAll(t, filepath.Join(repo, ".git", "worktrees", "w"))
	wt := filepath.Join(repo, ".claude", "worktrees", "w")
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(repo, ".git", "worktrees", "w")+"\n")
	mustWrite(t, filepath.Join(wt, "src", "f.go"))
	g.st = gitview.Status{State: "ok", Entries: []gitview.Entry{{P: "src/f.go", X: "U"}}}
	decodeDiff(t, gitPost(s.handleGitDiff, "/api/git/diff", pathBody(filepath.Join(wt, "src", "f.go"))))
	want := gitview.Target{Top: wt, Common: filepath.Join(repo, ".git")}
	if len(g.diffs) != 1 || g.diffs[0] != (fakeDiffCall{want, "src/f.go", true}) {
		t.Fatalf("diffs = %+v", g.diffs)
	}
}

func TestGitDiffNotAFile(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	mustMkdirAll(t, filepath.Join(repo, "d"))
	for _, p := range []string{filepath.Join(repo, "d"), repo} {
		if rr := gitPost(s.handleGitDiff, "/api/git/diff", pathBody(p)); rr.Code != http.StatusBadRequest {
			t.Fatalf("%s: code = %d, want 400", p, rr.Code)
		}
	}
	if rr := gitPost(s.handleGitDiff, "/api/git/diff", pathBody(filepath.Join(repo, "gone.txt"))); rr.Code != http.StatusNotFound {
		t.Fatalf("missing: code = %d, want 404", rr.Code)
	}
	if g.calls() != 0 {
		t.Fatal("runner called")
	}
}

func TestGitDiffBoxedFileLinkToHostRefused(t *testing.T) {
	s, h, ws := boxedServer(t)
	g := &fakeGit{}
	s.Git = g
	mainRepo(t, ws)
	hostRepo := filepath.Join(h, "hostrepo")
	mainRepo(t, hostRepo)
	mustWrite(t, filepath.Join(hostRepo, "secret.txt"), "TOPSECRET")
	mustSymlink(t, filepath.Join(hostRepo, "secret.txt"), filepath.Join(ws, "notes.txt"))
	rr := gitPost(s.handleGitDiff, "/api/git/diff", pathBody(filepath.Join(ws, "notes.txt")))
	d := decodeDiff(t, rr)
	if d.State != "unavailable" || d.Reason != msgGitOutside || g.calls() != 0 {
		t.Fatalf("diff = %+v calls = %d", d, g.calls())
	}
}

func TestGitDiffStatusUnavailable(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	mustWrite(t, filepath.Join(repo, "a.txt"))
	g.st = gitview.Status{State: "unavailable", Reason: "fatal: raw stderr", Entries: []gitview.Entry{}}
	d := decodeDiff(t, gitPost(s.handleGitDiff, "/api/git/diff", pathBody(filepath.Join(repo, "a.txt"))))
	if d.State != "unavailable" || d.Reason != msgGitNotAvailable || len(g.diffs) != 0 {
		t.Fatalf("diff = %+v diffs = %+v", d, g.diffs)
	}
}

func TestGitDiffRunnerReasonNotForwarded(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	mustWrite(t, filepath.Join(repo, "a.txt"))
	g.diff = gitview.Diff{State: "unavailable", Reason: "fatal: raw stderr /secret"}
	d := decodeDiff(t, gitPost(s.handleGitDiff, "/api/git/diff", pathBody(filepath.Join(repo, "a.txt"))))
	if d.State != "unavailable" || d.Reason != msgGitNotAvailable {
		t.Fatalf("diff = %+v", d)
	}
}

func TestGitStatusHostCommonInsideBoxedRoot(t *testing.T) {
	// A host checkout whose common dir is box-writable must not be mounted:
	// the request is a host one, so every mount must be outside the boxed trees.
	s, h, ws := boxedServer(t)
	g := &fakeGit{}
	s.Git = g
	mainRepo(t, ws)
	mustMkdirAll(t, filepath.Join(ws, ".git", "worktrees", "w"))
	wt := filepath.Join(h, "wt")
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(ws, ".git", "worktrees", "w")+"\n")
	wantRefused(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(wt)), g, msgGitOutside)
}

func TestGitStatusRootRel(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	mustMkdirAll(t, filepath.Join(repo, "sub", "deeper"))
	mustSymlink(t, filepath.Join(repo, "sub"), filepath.Join(h, "alias"))
	for _, c := range []struct{ req, want string }{
		{repo, "."},
		{filepath.Join(repo, "sub"), "sub"},
		{filepath.Join(repo, "sub", "deeper"), "sub/deeper"},
		// An alias of top/sub: Top stays the resolved checkout, rootRel says
		// where the shown tree sits in it.
		{filepath.Join(h, "alias"), "sub"},
	} {
		st := decodeStatus(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(c.req)))
		if st.Top != repo || st.RootRel != c.want {
			t.Fatalf("%s: top %q rootRel %q, want %q %q", c.req, st.Top, st.RootRel, repo, c.want)
		}
	}
	// The JSON always carries rootRel, even for a refusal.
	rr := gitPost(s.handleGitStatus, "/api/git/status", pathBody(repo))
	if !strings.Contains(rr.Body.String(), `"rootRel":"."`) {
		t.Fatalf("body %s", rr.Body)
	}
	s.Git = nil
	rr = gitPost(s.handleGitStatus, "/api/git/status", pathBody(repo))
	if !strings.Contains(rr.Body.String(), `"rootRel"`) {
		t.Fatalf("rootRel missing from %s", rr.Body)
	}
}

func TestGitTargetRootOnlyForBoxed(t *testing.T) {
	// Host request: Root empty (Top/Common mounts). Boxed request: Root is the
	// contained root, for status and diff alike.
	s, h, ws := boxedServer(t)
	g := &fakeGit{}
	s.Git = g
	s.BoxTrees = func() BoxTrees { return BoxTrees{Contained: []string{ws}} }
	host := filepath.Join(h, "hostrepo")
	mainRepo(t, host)
	mustWrite(t, filepath.Join(host, "a.txt"))
	wt := filepath.Join(ws, "wt")
	mainRepo(t, filepath.Join(ws, "main"))
	mustMkdirAll(t, filepath.Join(ws, "main", ".git", "worktrees", "w"))
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(ws, "main", ".git", "worktrees", "w")+"\n")
	mustWrite(t, filepath.Join(wt, "b.txt"))

	decodeStatus(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(host)))
	decodeStatus(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(wt)))
	decodeDiff(t, gitPost(s.handleGitDiff, "/api/git/diff", pathBody(filepath.Join(host, "a.txt"))))
	decodeDiff(t, gitPost(s.handleGitDiff, "/api/git/diff", pathBody(filepath.Join(wt, "b.txt"))))

	hostT := gitview.Target{Top: host, Common: filepath.Join(host, ".git")}
	boxT := gitview.Target{Root: ws, Top: wt, Common: filepath.Join(ws, "main", ".git")}
	if len(g.statuses) != 4 || g.statuses[0] != hostT || g.statuses[1] != boxT ||
		g.statuses[2] != hostT || g.statuses[3] != boxT {
		t.Fatalf("status targets = %+v", g.statuses)
	}
	if len(g.diffs) != 2 || g.diffs[0].T != hostT || g.diffs[1].T != boxT {
		t.Fatalf("diff targets = %+v", g.diffs)
	}
}
