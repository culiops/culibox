package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/iotest"
	"time"

	"github.com/culiops/culibox/internal/gitview"
)

// fakeGit records every Target it is handed and answers with canned results.
type fakeGit struct {
	mu       sync.Mutex
	statuses []gitview.Target
	fresh    []bool // per Status/StatusFresh call: was it fresh
	diffs    []fakeDiffCall
	bases    []gitview.Target
	baseRefs []gitview.Target
	numstats []fakeNumstatCall
	shows    []fakeShowCall
	// numGate, when set, holds every Numstat until it is closed: a slow diff.
	numGate chan struct{}
	st      gitview.Status
	diff    gitview.Diff
	base    gitview.BaseInfo
	ns      gitview.Numstat
	blob    gitview.Blob
	raw     []byte // what ShowRaw returns beside blob
}

type fakeShowCall struct {
	T        gitview.Target
	Rev, Rel string
	Raw      bool
}

type fakeNumstatCall struct {
	T     gitview.Target
	Sc    gitview.Scope
	Fresh bool
}

type fakeDiffCall struct {
	T         gitview.Target
	Rel       string
	Untracked bool
}

func (f *fakeGit) Status(ctx context.Context, t gitview.Target) gitview.Status {
	return f.status(t, false)
}

func (f *fakeGit) StatusShared(ctx context.Context, t gitview.Target) gitview.Status {
	return f.status(t, false)
}

func (f *fakeGit) StatusFresh(ctx context.Context, t gitview.Target) gitview.Status {
	return f.status(t, true)
}

func (f *fakeGit) status(t gitview.Target, fresh bool) gitview.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, t)
	f.fresh = append(f.fresh, fresh)
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

func (f *fakeGit) Base(_ context.Context, t gitview.Target) gitview.BaseInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bases = append(f.bases, t)
	if f.base.State == "" {
		return gitview.BaseInfo{State: "ok", Branch: "feat", BaseRef: "refs/remotes/origin/main", BaseName: "origin/main", MergeBase: strings.Repeat("a", 40), Ahead: true}
	}
	return f.base
}

// BaseRef answers from the same canned base (its ref part), recorded apart from Base.
func (f *fakeGit) BaseRef(ctx context.Context, t gitview.Target) gitview.BaseInfo {
	f.mu.Lock()
	f.baseRefs = append(f.baseRefs, t)
	f.mu.Unlock()
	f.mu.Lock()
	bi := f.base
	f.mu.Unlock()
	if bi.State == "" {
		return gitview.BaseInfo{State: "ok", BaseRef: "refs/remotes/origin/main", BaseName: "origin/main"}
	}
	return gitview.BaseInfo{State: bi.State, Reason: bi.Reason, BaseRef: bi.BaseRef, BaseName: bi.BaseName}
}

func (f *fakeGit) Numstat(_ context.Context, t gitview.Target, sc gitview.Scope) gitview.Numstat {
	return f.numstat(t, sc, false)
}

func (f *fakeGit) NumstatFresh(_ context.Context, t gitview.Target, sc gitview.Scope) gitview.Numstat {
	return f.numstat(t, sc, true)
}

func (f *fakeGit) numstat(t gitview.Target, sc gitview.Scope, fresh bool) gitview.Numstat {
	f.mu.Lock()
	f.numstats = append(f.numstats, fakeNumstatCall{t, sc, fresh})
	ns, gate := f.ns, f.numGate
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if ns.State == "" {
		return gitview.Numstat{State: "ok", Entries: []gitview.NumEntry{}}
	}
	// A copy: the handler appends to and sets fields of what it gets.
	ns.Entries = append([]gitview.NumEntry{}, ns.Entries...)
	return ns
}

func (f *fakeGit) Show(_ context.Context, t gitview.Target, rev, rel string) gitview.Blob {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shows = append(f.shows, fakeShowCall{t, rev, rel, false})
	if f.blob.State == "" {
		return gitview.Blob{State: "ok", Content: "package main\n"}
	}
	return f.blob
}

func (f *fakeGit) ShowRaw(_ context.Context, t gitview.Target, rev, rel string) ([]byte, gitview.Blob) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shows = append(f.shows, fakeShowCall{t, rev, rel, true})
	if f.blob.State == "" {
		return f.raw, gitview.Blob{State: "ok"}
	}
	return f.raw, f.blob
}

// statusCalls is how many Status calls have been made so far.
func (f *fakeGit) statusCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.statuses)
}

func (f *fakeGit) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.statuses) + len(f.diffs) + len(f.bases) + len(f.baseRefs) + len(f.numstats) + len(f.shows)
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

func TestGitStatusFreshFlagSelectsFreshRun(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	b, _ := json.Marshal(map[string]any{"path": repo, "fresh": true})
	decodeStatus(t, gitPost(s.handleGitStatus, "/api/git/status", string(b)))
	decodeStatus(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(repo)))
	if len(g.fresh) != 2 || !g.fresh[0] || g.fresh[1] {
		t.Fatalf("fresh flags = %v, want [true false]", g.fresh)
	}
}

func TestGitDiffUsesPlainStatus(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	mustWrite(t, filepath.Join(repo, "a.txt"))
	decodeDiff(t, gitPost(s.handleGitDiff, "/api/git/diff", pathBody(filepath.Join(repo, "a.txt"))))
	if len(g.fresh) != 1 || g.fresh[0] {
		t.Fatalf("diff's status call fresh flags = %v, want [false] (reuse the tree's result)", g.fresh)
	}
}

func TestGitMissingImageReasonIsFixed(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	mustWrite(t, filepath.Join(repo, "a.txt"))
	g.st = gitview.Status{State: "unavailable", Reason: gitview.ReasonNoImage, Entries: []gitview.Entry{}}
	st := decodeStatus(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(repo)))
	if st.State != "unavailable" || st.Reason != msgGitNoImage {
		t.Fatalf("status = %+v, want %q", st, msgGitNoImage)
	}
	d := decodeDiff(t, gitPost(s.handleGitDiff, "/api/git/diff", pathBody(filepath.Join(repo, "a.txt"))))
	if d.State != "unavailable" || d.Reason != msgGitNoImage {
		t.Fatalf("diff after status = %+v", d)
	}
	g.st = gitview.Status{State: "ok", Entries: []gitview.Entry{}}
	g.diff = gitview.Diff{State: "unavailable", Reason: gitview.ReasonNoImage}
	d = decodeDiff(t, gitPost(s.handleGitDiff, "/api/git/diff", pathBody(filepath.Join(repo, "a.txt"))))
	if d.State != "unavailable" || d.Reason != msgGitNoImage {
		t.Fatalf("diff = %+v, want %q", d, msgGitNoImage)
	}
}

func TestGitDiffTooManyEmptyIsUnknownNotUnchanged(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	mustWrite(t, filepath.Join(repo, "new.txt"))
	g.st = gitview.Status{State: "toomany", Entries: []gitview.Entry{}}
	g.diff = gitview.Diff{State: "ok", Kind: "unchanged", Diff: ""}
	d := decodeDiff(t, gitPost(s.handleGitDiff, "/api/git/diff", pathBody(filepath.Join(repo, "new.txt"))))
	if d.State != "unavailable" || d.Reason != msgGitTooMany {
		t.Fatalf("diff = %+v, want unavailable %q", d, msgGitTooMany)
	}
	// A tracked file's HEAD diff is real and is still shown.
	g.diff = gitview.Diff{State: "ok", Kind: "text", Diff: "@@ -1 +1 @@\n-a\n+b\n"}
	d = decodeDiff(t, gitPost(s.handleGitDiff, "/api/git/diff", pathBody(filepath.Join(repo, "new.txt"))))
	if d.State != "ok" || d.Kind != "text" || d.Reason != "" {
		t.Fatalf("tracked diff under toomany = %+v", d)
	}
	// With a complete status an empty diff stays "unchanged".
	g.st = gitview.Status{State: "ok", Entries: []gitview.Entry{}}
	g.diff = gitview.Diff{State: "ok", Kind: "unchanged"}
	d = decodeDiff(t, gitPost(s.handleGitDiff, "/api/git/diff", pathBody(filepath.Join(repo, "new.txt"))))
	if d.State != "ok" || d.Kind != "unchanged" {
		t.Fatalf("ok-status empty diff = %+v", d)
	}
}

func decodeBase(t *testing.T, rr *httptest.ResponseRecorder) gitview.BaseInfo {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rr.Code, rr.Body)
	}
	var bi gitview.BaseInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &bi); err != nil {
		t.Fatalf("decode %s: %v", rr.Body, err)
	}
	return bi
}

func TestGitBaseHandlerOK(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	mustMkdirAll(t, filepath.Join(repo, "sub"))
	g.base = gitview.BaseInfo{State: "ok", Branch: "feat/x", BaseRef: "refs/remotes/origin/main", BaseName: "origin/main", MergeBase: strings.Repeat("b", 40), Ahead: true}
	// A baseRef (or anything else) in the body is not read: the server derives it.
	body := `{"path":` + strconv.Quote(filepath.Join(repo, "sub")) + `,"baseRef":"-x","scope":"branch"}`
	rr := gitPost(s.handleGitBase, "/api/git/base", body)
	bi := decodeBase(t, rr)
	if bi != g.base {
		t.Fatalf("base = %+v, want %+v", bi, g.base)
	}
	want := gitview.Target{Top: repo, Common: filepath.Join(repo, ".git")}
	if len(g.bases) != 1 || g.bases[0] != want || len(g.statuses) != 0 || len(g.diffs) != 0 {
		t.Fatalf("calls: bases=%+v statuses=%+v diffs=%+v, want one Base of %+v", g.bases, g.statuses, g.diffs, want)
	}
	for _, key := range []string{`"state":"ok"`, `"branch":"feat/x"`, `"baseRef":"refs/remotes/origin/main"`, `"baseName":"origin/main"`, `"mergeBase":"` + strings.Repeat("b", 40) + `"`, `"ahead":true`} {
		if !strings.Contains(rr.Body.String(), key) {
			t.Errorf("reply %s lacks %s", rr.Body, key)
		}
	}
	// Boxed: the contained root is the only mount, as for status.
	bs, _, ws := boxedServer(t)
	bg := &fakeGit{}
	bs.Git = bg
	mainRepo(t, ws)
	decodeBase(t, gitPost(bs.handleGitBase, "/api/git/base", pathBody(ws)))
	if w := (gitview.Target{Root: ws, Top: ws, Common: filepath.Join(ws, ".git")}); len(bg.bases) != 1 || bg.bases[0] != w {
		t.Fatalf("boxed targets = %+v, want %+v", bg.bases, w)
	}
}

func TestGitBaseRefusedOutside(t *testing.T) {
	s, h, g := hostGitServer(t)
	other := t.TempDir() // a sibling of $HOME, not inside it
	mustMkdirAll(t, filepath.Join(other, ".git", "worktrees", "w"))
	wt := filepath.Join(h, "wt")
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(other, ".git", "worktrees", "w")+"\n")
	bi := decodeBase(t, gitPost(s.handleGitBase, "/api/git/base", pathBody(wt)))
	if bi.State != "unavailable" || bi.Reason != msgGitOutside {
		t.Fatalf("base = %+v, want unavailable %q", bi, msgGitOutside)
	}
	if n := g.calls(); n != 0 {
		t.Fatalf("runner called %d times for a refused target", n)
	}
	// A repository above the viewable tree, and a mount path docker cannot take.
	outside := t.TempDir()
	mainRepo(t, outside)
	if rr := gitPost(s.handleGitBase, "/api/git/base", pathBody(outside)); rr.Code != http.StatusForbidden {
		t.Fatalf("outside home: code = %d, want 403", rr.Code)
	}
	unsafe := filepath.Join(h, "a,readonly=false")
	mainRepo(t, unsafe)
	if bi := decodeBase(t, gitPost(s.handleGitBase, "/api/git/base", pathBody(unsafe))); bi.State != "unavailable" || bi.Reason != msgGitNotAvailable {
		t.Fatalf("unsafe path: %+v", bi)
	}
	if n := g.calls(); n != 0 {
		t.Fatalf("runner called %d times for a refused target", n)
	}
}

func TestGitBaseStatesAndReasons(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	post := func() gitview.BaseInfo {
		return decodeBase(t, gitPost(s.handleGitBase, "/api/git/base", pathBody(repo)))
	}
	// The runner's reason can carry git's stderr: only fixed messages reach the client.
	for raw, want := range map[string]string{
		"fatal: /secret/path: bad object deadbeef": msgGitNotAvailable,
		"git timed out": msgGitTimeout,
		"timed out waiting for a free git container": msgGitTimeout,
		gitview.ReasonNoImage:                        msgGitNoImage,
	} {
		g.base = gitview.BaseInfo{State: "unavailable", Reason: raw, Branch: "feat"}
		if bi := post(); bi.State != "unavailable" || bi.Reason != want || bi.Branch != "feat" {
			t.Fatalf("raw %q: base = %+v, want reason %q", raw, bi, want)
		}
	}
	g.base = gitview.BaseInfo{State: "nobase", Reason: "git said /secret/path", Branch: "feat"}
	if bi := post(); bi.State != "nobase" || bi.Reason != msgGitNoBase || bi.BaseRef != "" || bi.Branch != "feat" {
		t.Fatalf("nobase: %+v", bi)
	}
	g.base = gitview.BaseInfo{State: "notrepo"}
	if bi := post(); bi.State != "notrepo" || bi.Reason != "" {
		t.Fatalf("notrepo: %+v", bi)
	}
	// ok carries no reason.
	g.base = gitview.BaseInfo{State: "ok", Reason: "stray", Branch: "feat", BaseRef: "refs/heads/main", BaseName: "main", MergeBase: strings.Repeat("c", 40)}
	if bi := post(); bi.State != "ok" || bi.Reason != "" {
		t.Fatalf("ok: %+v", bi)
	}
}

func TestGitBaseNotRepoNeverRunsGit(t *testing.T) {
	s, h, g := hostGitServer(t)
	dir := filepath.Join(h, "plain")
	mustMkdirAll(t, dir)
	if bi := decodeBase(t, gitPost(s.handleGitBase, "/api/git/base", pathBody(dir))); bi.State != "notrepo" {
		t.Fatalf("base = %+v", bi)
	}
	if g.calls() != 0 {
		t.Fatal("runner called for a non-repo")
	}
}

func TestGitBaseNilRunner(t *testing.T) {
	s, h, _ := hostGitServer(t)
	s.Git = nil
	mainRepo(t, filepath.Join(h, "repo"))
	bi := decodeBase(t, gitPost(s.handleGitBase, "/api/git/base", pathBody(filepath.Join(h, "repo"))))
	if bi.State != "unavailable" || bi.Reason != msgGitNoRuntime {
		t.Fatalf("base = %+v", bi)
	}
}

func TestGitBaseGuards(t *testing.T) {
	s, h, g := hostGitServer(t)
	mainRepo(t, filepath.Join(h, "repo"))
	get := httptest.NewRecorder()
	s.handleGitBase(get, httptest.NewRequest(http.MethodGet, "/api/git/base?path="+h, nil))
	if get.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET code = %d, want 405", get.Code)
	}
	for _, body := range []string{"", "{", `{"path":"rel/dir"}`, `{"path":""}`} {
		if rr := gitPost(s.handleGitBase, "/api/git/base", body); rr.Code != http.StatusBadRequest {
			t.Fatalf("body %.40q: code = %d, want 400 (%s)", body, rr.Code, rr.Body)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/git/base", strings.NewReader(pathBody(filepath.Join(h, "repo"))))
	req.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()
	s.handleGitBase(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-origin code = %d, want 403", rr.Code)
	}
	if g.calls() != 0 {
		t.Fatal("runner called on a refused request")
	}
}

func decodeNumstat(t *testing.T, rr *httptest.ResponseRecorder) gitview.Numstat {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rr.Code, rr.Body)
	}
	var ns gitview.Numstat
	if err := json.Unmarshal(rr.Body.Bytes(), &ns); err != nil {
		t.Fatalf("decode %s: %v", rr.Body, err)
	}
	if ns.Entries == nil {
		t.Fatalf("entries must be [] not null: %s", rr.Body)
	}
	return ns
}

func numstatBody(p, scope string) string {
	b, _ := json.Marshal(map[string]string{"path": p, "scope": scope})
	return string(b)
}

func postNumstat(s *Server, p, scope string) *httptest.ResponseRecorder {
	return gitPost(s.handleGitNumstat, "/api/git/numstat", numstatBody(p, scope))
}

func TestGitNumstatHandlerOK(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	mustMkdirAll(t, filepath.Join(repo, "sub"))
	g.ns = gitview.Numstat{State: "ok", Reason: "stray", Entries: []gitview.NumEntry{{P: "a.go", X: "M", A: 3, D: 1}, {P: "img.png", X: "M", Bin: true}}}
	rr := postNumstat(s, filepath.Join(repo, "sub"), "head")
	ns := decodeNumstat(t, rr)
	want := []gitview.NumEntry{{P: "a.go", X: "M", A: 3, D: 1}, {P: "img.png", X: "M", Bin: true}}
	if ns.State != "ok" || ns.Reason != "" || ns.RootRel != "sub" || !slices.Equal(ns.Entries, want) {
		t.Fatalf("numstat = %+v, want ok under sub with %+v", ns, want)
	}
	// The wire shape the client reads.
	for _, key := range []string{`"state":"ok"`, `"rootRel":"sub"`, `{"p":"a.go","x":"M","a":3,"d":1}`, `{"p":"img.png","x":"M","a":0,"d":0,"bin":true}`} {
		if !strings.Contains(rr.Body.String(), key) {
			t.Errorf("reply %s lacks %s", rr.Body, key)
		}
	}
	tg := gitview.Target{Top: repo, Common: filepath.Join(repo, ".git")}
	if len(g.numstats) != 1 || g.numstats[0] != (fakeNumstatCall{tg, gitview.Scope{}, false}) || len(g.bases) != 0 || len(g.diffs) != 0 {
		t.Fatalf("calls: numstats=%+v bases=%+v diffs=%+v, want one head-scope Numstat of %+v", g.numstats, g.bases, g.diffs, tg)
	}
}

func TestGitNumstatBranchScopeUsesTheServersBase(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	g.base = gitview.BaseInfo{State: "ok", Branch: "feat", BaseRef: "refs/remotes/origin/develop", BaseName: "origin/develop", MergeBase: strings.Repeat("b", 40), Ahead: true}
	// A ref in the body is not read: the base is the one BaseRef derived.
	body := `{"path":` + strconv.Quote(repo) + `,"scope":"branch","baseRef":"-x","base":"refs/heads/evil"}`
	decodeNumstat(t, gitPost(s.handleGitNumstat, "/api/git/numstat", body))
	tg := gitview.Target{Top: repo, Common: filepath.Join(repo, ".git")}
	want := fakeNumstatCall{tg, gitview.Scope{Branch: true, BaseRef: "refs/remotes/origin/develop"}, false}
	// The ref alone is looked up (BaseRef): a Branch numstat never waits for a status or a merge-base run.
	if len(g.numstats) != 1 || g.numstats[0] != want || len(g.baseRefs) != 1 || g.baseRefs[0] != tg || len(g.bases) != 0 {
		t.Fatalf("calls: numstats=%+v baseRefs=%+v bases=%+v, want one BaseRef, no Base and %+v", g.numstats, g.baseRefs, g.bases, want)
	}
}

func TestGitNumstatBranchScopeWithoutABase(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	for name, c := range map[string]struct {
		base   gitview.BaseInfo
		state  string
		reason string
	}{
		"no base":     {gitview.BaseInfo{State: "nobase", Reason: "git said /secret/path"}, "unavailable", msgGitNoBase},
		"unavailable": {gitview.BaseInfo{State: "unavailable", Reason: "fatal: /secret/path: bad object"}, "unavailable", msgGitNotAvailable},
		"timeout":     {gitview.BaseInfo{State: "unavailable", Reason: "git timed out"}, "unavailable", msgGitTimeout},
		"not a repo":  {gitview.BaseInfo{State: "notrepo"}, "notrepo", ""},
	} {
		g.base = c.base
		n := len(g.numstats)
		ns := decodeNumstat(t, postNumstat(s, repo, "branch"))
		if ns.State != c.state || ns.Reason != c.reason || len(ns.Entries) != 0 {
			t.Errorf("%s: numstat = %+v, want %s %q", name, ns, c.state, c.reason)
		}
		if len(g.numstats) != n {
			t.Errorf("%s: Numstat ran with no base", name)
		}
	}
}

func TestGitNumstatScopeIsHeadOrBranch(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	// Anything else is refused, the absent scope included: no client field names a ref.
	for _, scope := range []string{"", "HEAD", "Head", "BRANCH", " branch", "branch\n", "refs/heads/main", "-x", "main", "origin/main", "merge-base"} {
		if rr := postNumstat(s, repo, scope); rr.Code != http.StatusBadRequest {
			t.Errorf("scope %q: code = %d, want 400 (%s)", scope, rr.Code, rr.Body)
		}
	}
	for _, body := range []string{`{"path":` + strconv.Quote(repo) + `}`, `{"path":` + strconv.Quote(repo) + `,"scope":1}`, `{"path":` + strconv.Quote(repo) + `,"scope":null}`, `{"path":` + strconv.Quote(repo) + `,"scope":["branch"]}`} {
		if rr := gitPost(s.handleGitNumstat, "/api/git/numstat", body); rr.Code != http.StatusBadRequest {
			t.Errorf("body %s: code = %d, want 400 (%s)", body, rr.Code, rr.Body)
		}
	}
	if g.calls() != 0 {
		t.Fatalf("runner called %d times for a bad scope", g.calls())
	}
	for _, scope := range []string{"head", "branch"} {
		if ns := decodeNumstat(t, postNumstat(s, repo, scope)); ns.State != "ok" {
			t.Errorf("scope %q: %+v", scope, ns)
		}
	}
}

func TestGitNumstatRefusedOutside(t *testing.T) {
	s, h, g := hostGitServer(t)
	other := t.TempDir() // a sibling of $HOME, not inside it
	mustMkdirAll(t, filepath.Join(other, ".git", "worktrees", "w"))
	wt := filepath.Join(h, "wt")
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(other, ".git", "worktrees", "w")+"\n")
	for _, scope := range []string{"head", "branch"} {
		ns := decodeNumstat(t, postNumstat(s, wt, scope))
		if ns.State != "unavailable" || ns.Reason != msgGitOutside {
			t.Fatalf("%s: numstat = %+v, want unavailable %q", scope, ns, msgGitOutside)
		}
	}
	// A repository above the viewable tree, and a mount path docker cannot take.
	outside := t.TempDir()
	mainRepo(t, outside)
	if rr := postNumstat(s, outside, "head"); rr.Code != http.StatusForbidden {
		t.Fatalf("outside home: code = %d, want 403", rr.Code)
	}
	unsafe := filepath.Join(h, "a,readonly=false")
	mainRepo(t, unsafe)
	if ns := decodeNumstat(t, postNumstat(s, unsafe, "head")); ns.State != "unavailable" || ns.Reason != msgGitNotAvailable {
		t.Fatalf("unsafe path: %+v", ns)
	}
	if n := g.calls(); n != 0 {
		t.Fatalf("runner called %d times for a refused target", n)
	}
}

func TestGitNumstatBoxedOK(t *testing.T) {
	s, _, ws := boxedServer(t)
	g := &fakeGit{}
	s.Git = g
	mainRepo(t, ws)
	mustMkdirAll(t, filepath.Join(ws, "sub"))
	ns := decodeNumstat(t, postNumstat(s, filepath.Join(ws, "sub"), "head"))
	// Boxed: the contained root is the only mount (Target.Root).
	want := gitview.Target{Root: ws, Top: ws, Common: filepath.Join(ws, ".git")}
	if ns.State != "ok" || ns.RootRel != "sub" || len(g.numstats) != 1 || g.numstats[0].T != want {
		t.Fatalf("numstat = %+v targets = %+v", ns, g.numstats)
	}
}

func TestGitNumstatGuards(t *testing.T) {
	s, h, g := hostGitServer(t)
	mainRepo(t, filepath.Join(h, "repo"))
	get := httptest.NewRecorder()
	s.handleGitNumstat(get, httptest.NewRequest(http.MethodGet, "/api/git/numstat?path="+h, nil))
	if get.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET code = %d, want 405", get.Code)
	}
	for _, body := range []string{"", "{", `{"path":"rel/dir","scope":"head"}`, `{"path":"","scope":"head"}`, `{"path":"/` + strings.Repeat("a", 9000) + `","scope":"head"}`} {
		if rr := gitPost(s.handleGitNumstat, "/api/git/numstat", body); rr.Code != http.StatusBadRequest {
			t.Fatalf("body %.40q: code = %d, want 400 (%s)", body, rr.Code, rr.Body)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/git/numstat", strings.NewReader(numstatBody(filepath.Join(h, "repo"), "head")))
	req.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()
	s.handleGitNumstat(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-origin code = %d, want 403", rr.Code)
	}
	if g.calls() != 0 {
		t.Fatal("runner called on a refused request")
	}
}

func TestGitNumstatNilRunnerAndNotRepo(t *testing.T) {
	s, h, g := hostGitServer(t)
	plain := filepath.Join(h, "plain")
	mustMkdirAll(t, plain)
	if ns := decodeNumstat(t, postNumstat(s, plain, "head")); ns.State != "notrepo" {
		t.Fatalf("not a repo: %+v", ns)
	}
	if g.calls() != 0 {
		t.Fatal("runner called for a non-repo")
	}
	s.Git = nil
	mainRepo(t, filepath.Join(h, "repo"))
	ns := decodeNumstat(t, postNumstat(s, filepath.Join(h, "repo"), "head"))
	if ns.State != "unavailable" || ns.Reason != msgGitNoRuntime {
		t.Fatalf("nil runner: %+v", ns)
	}
}

func TestGitNumstatStatesAndReasons(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	mustWrite(t, filepath.Join(repo, "new.txt"), "a\n")
	g.st = gitview.Status{State: "ok", Entries: []gitview.Entry{{P: "new.txt", X: "U"}}}
	post := func() gitview.Numstat { return decodeNumstat(t, postNumstat(s, repo, "head")) }
	// The runner's reason can carry git's stderr: only fixed messages reach the client.
	for raw, want := range map[string]string{
		"fatal: /secret/path: bad object deadbeef": msgGitNotAvailable,
		"git timed out": msgGitTimeout,
		"timed out waiting for a free git container": msgGitTimeout,
		gitview.ReasonNoImage:                        msgGitNoImage,
	} {
		g.ns = gitview.Numstat{State: "unavailable", Reason: raw, Entries: []gitview.NumEntry{}}
		if ns := post(); ns.State != "unavailable" || ns.Reason != want || len(ns.Entries) != 0 {
			t.Fatalf("raw %q: numstat = %+v, want reason %q", raw, ns, want)
		}
	}
	// Without a count from git there is no point counting lines: no untracked entries.
	g.ns = gitview.Numstat{State: "toomany", Entries: []gitview.NumEntry{}}
	if ns := post(); ns.State != "toomany" || ns.Reason != "" || len(ns.Entries) != 0 {
		t.Fatalf("toomany: %+v", ns)
	}
	g.ns = gitview.Numstat{State: "notrepo", Entries: []gitview.NumEntry{}}
	if ns := post(); ns.State != "notrepo" || len(ns.Entries) != 0 {
		t.Fatalf("notrepo: %+v", ns)
	}
}

// untrackedRepo is a repo in a host server's $HOME whose status lists the given
// paths as untracked and whose numstat has one tracked change.
func untrackedRepo(t *testing.T, paths ...string) (s *Server, repo string, g *fakeGit) {
	t.Helper()
	s, h, g := hostGitServer(t)
	repo = filepath.Join(h, "repo")
	mainRepo(t, repo)
	es := []gitview.Entry{}
	for _, p := range paths {
		es = append(es, gitview.Entry{P: p, X: "U"})
	}
	g.st = gitview.Status{State: "ok", Entries: es}
	g.ns = gitview.Numstat{State: "ok", Entries: []gitview.NumEntry{{P: "tracked.go", X: "M", A: 1, D: 1}}}
	return s, repo, g
}

func TestGitNumstatCountsUntracked(t *testing.T) {
	s, repo, g := untrackedRepo(t, "three.txt", "nonl.txt", "empty.txt", "bin.dat", "late-nul.txt", "big.txt", "toobig.txt", "dir/", "sub/deep.txt", "gone.txt", "crlf.txt", "blank.txt")
	w := func(name string, b []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("three.txt", []byte("a\nb\nc\n"))
	w("nonl.txt", []byte("a\nb\nlast")) // the last line has no newline: still a line
	w("empty.txt", nil)
	w("bin.dat", append([]byte("PNG\x00"), []byte("a\nb\n")...))
	// A NUL after the first 8 KiB does not make a file binary.
	late := append(bytes.Repeat([]byte("x\n"), 4096), 0, '\n') // NUL at byte 8192
	w("late-nul.txt", late)
	w("big.txt", bytes.Repeat([]byte("l\n"), 1<<20)) // exactly 2 MiB
	w("toobig.txt", append(bytes.Repeat([]byte("l\n"), 1<<20), '\n'))
	w("sub/deep.txt", []byte("one\n"))
	w("crlf.txt", []byte("a\r\nb\r\n"))
	w("blank.txt", []byte("\n\n\n"))
	mustMkdirAll(t, filepath.Join(repo, "dir"))
	w("dir/inside.txt", []byte("x\n")) // under a collapsed untracked folder: no entry for it

	ns := decodeNumstat(t, postNumstat(s, repo, "head"))
	want := []gitview.NumEntry{
		{P: "tracked.go", X: "M", A: 1, D: 1}, // git's, first and untouched
		{P: "three.txt", X: "U", A: 3},
		{P: "nonl.txt", X: "U", A: 3},
		{P: "empty.txt", X: "U"},
		{P: "bin.dat", X: "U", Bin: true},
		{P: "late-nul.txt", X: "U", A: 4097},
		{P: "big.txt", X: "U", A: 1 << 20},
		// toobig.txt (over 2 MiB), dir/ (a folder) and gone.txt (not on disk) have no entry.
		{P: "sub/deep.txt", X: "U", A: 1},
		{P: "crlf.txt", X: "U", A: 2},
		{P: "blank.txt", X: "U", A: 3},
	}
	if ns.State != "ok" || !slices.Equal(ns.Entries, want) {
		t.Fatalf("numstat = %+v\nwant %+v", ns, want)
	}
	if len(g.statuses) != 1 || len(g.numstats) != 1 {
		t.Fatalf("calls: statuses=%d numstats=%d, want one each", len(g.statuses), len(g.numstats))
	}
	// Branch scope counts the same untracked files: they are new against the base too.
	ns = decodeNumstat(t, postNumstat(s, repo, "branch"))
	if ns.State != "ok" || !slices.Equal(ns.Entries, want) {
		t.Fatalf("branch numstat = %+v", ns)
	}
	if !strings.Contains(fmt.Sprint(ns.Entries), "{bin.dat U 0 0 true}") {
		t.Fatalf("binary entry: %+v", ns.Entries)
	}
}

func TestGitNumstatUntrackedOnlyTakesWhatStatusSaysIsUntracked(t *testing.T) {
	s, repo, g := untrackedRepo(t)
	mustWrite(t, filepath.Join(repo, "modified.go"), "a\nb\n")
	mustWrite(t, filepath.Join(repo, "dup.go"), "a\nb\n")
	mustWrite(t, filepath.Join(repo, "new.go"), "a\n")
	// Only U entries are counted; M, A, D and C come from git. A path numstat
	// already has (the two runs are seconds apart) is not listed twice.
	g.st.Entries = []gitview.Entry{{P: "modified.go", X: "M"}, {P: "added.go", X: "A"}, {P: "gone.go", X: "D"}, {P: "conflict.go", X: "C"}, {P: "dup.go", X: "U"}, {P: "new.go", X: "U"}}
	g.ns.Entries = []gitview.NumEntry{{P: "dup.go", X: "A", A: 2}}
	ns := decodeNumstat(t, postNumstat(s, repo, "head"))
	want := []gitview.NumEntry{{P: "dup.go", X: "A", A: 2}, {P: "new.go", X: "U", A: 1}}
	if !slices.Equal(ns.Entries, want) {
		t.Fatalf("entries = %+v, want %+v", ns.Entries, want)
	}
	// A status that is not ok has no untracked list: git's entries stand alone.
	g.st = gitview.Status{State: "toomany", Entries: []gitview.Entry{}}
	if ns := decodeNumstat(t, postNumstat(s, repo, "head")); ns.State != "ok" || !slices.Equal(ns.Entries, []gitview.NumEntry{{P: "dup.go", X: "A", A: 2}}) {
		t.Fatalf("toomany status: %+v", ns)
	}
	g.st = gitview.Status{State: "unavailable", Reason: "boom /secret", Entries: []gitview.Entry{}}
	if ns := decodeNumstat(t, postNumstat(s, repo, "head")); ns.State != "ok" || len(ns.Entries) != 1 || ns.Reason != "" {
		t.Fatalf("unavailable status: %+v", ns)
	}
}

func TestGitNumstatCountsAtMost200Untracked(t *testing.T) {
	var paths []string
	for i := 0; i < 250; i++ {
		paths = append(paths, fmt.Sprintf("f%03d.txt", i))
	}
	s, repo, _ := untrackedRepo(t, paths...)
	for _, p := range paths {
		mustWrite(t, filepath.Join(repo, p), "a\n")
	}
	ns := decodeNumstat(t, postNumstat(s, repo, "head"))
	// The tracked entry and the first 200 untracked ones, in status order.
	if len(ns.Entries) != 201 || ns.Entries[1].P != "f000.txt" || ns.Entries[200].P != "f199.txt" {
		t.Fatalf("%d entries, first %+v last %+v", len(ns.Entries), ns.Entries[1], ns.Entries[len(ns.Entries)-1])
	}
	// The cap is on files looked at, not on counts that came out: a missing file
	// uses a slot and so makes no room for a later one. A folder is not a file.
	paths = append([]string{"dir/", "missing.txt"}, paths...)
	s, repo, _ = untrackedRepo(t, paths...)
	for _, p := range paths[2:] {
		mustWrite(t, filepath.Join(repo, p), "a\n")
	}
	ns = decodeNumstat(t, postNumstat(s, repo, "head"))
	if len(ns.Entries) != 1+199 || ns.Entries[len(ns.Entries)-1].P != "f198.txt" {
		t.Fatalf("with a folder and a missing file: %d entries, last %+v", len(ns.Entries), ns.Entries[len(ns.Entries)-1])
	}
}

func TestGitNumstatUntrackedNeverLeavesTheTree(t *testing.T) {
	s, repo, _ := untrackedRepo(t, "../outside.txt", "/etc/hostname", "a/../../outside.txt", "link-out.txt", "link-in.txt", "linkdir/file.txt", "fifo", "ok.txt")
	h := filepath.Dir(repo)
	mustWrite(t, filepath.Join(h, "outside.txt"), "secret\n")
	mustWrite(t, filepath.Join(repo, "real.txt"), "a\nb\n")
	mustWrite(t, filepath.Join(repo, "ok.txt"), "a\n")
	outer := t.TempDir() // beside $HOME, outside every served tree
	mustWrite(t, filepath.Join(outer, "host.txt"), "x\ny\nz\n")
	mustSymlink(t, filepath.Join(outer, "host.txt"), filepath.Join(repo, "link-out.txt"))
	// A link to a file in the tree is the link's own text for git (one line), not its target's lines.
	mustSymlink(t, "real.txt", filepath.Join(repo, "link-in.txt"))
	mustMkdirAll(t, filepath.Join(repo, "realdir"))
	mustWrite(t, filepath.Join(repo, "realdir", "file.txt"), "a\nb\n")
	mustSymlink(t, filepath.Join(repo, "realdir"), filepath.Join(repo, "linkdir"))
	if err := syscall.Mkfifo(filepath.Join(repo, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	ns := decodeNumstat(t, postNumstat(s, repo, "head"))
	want := []gitview.NumEntry{{P: "tracked.go", X: "M", A: 1, D: 1}, {P: "ok.txt", X: "U", A: 1}}
	if !slices.Equal(ns.Entries, want) {
		t.Fatalf("entries = %+v, want only %+v", ns.Entries, want)
	}
}

func TestGitNumstatUntrackedBoxed(t *testing.T) {
	s, home, ws := boxedServer(t)
	g := &fakeGit{}
	s.Git = g
	mainRepo(t, ws)
	for p, c := range map[string]string{"new.txt": "a\nb\n", "real.txt": "a\nb\nc\n"} {
		mustWrite(t, filepath.Join(ws, p), c)
	}
	mustWrite(t, filepath.Join(home, "host-secret.txt"), "1\n2\n3\n4\n")
	mustSymlink(t, filepath.Join(home, "host-secret.txt"), filepath.Join(ws, "escape.txt"))
	mustSymlink(t, "real.txt", filepath.Join(ws, "inner-link.txt"))
	mustSymlink(t, "../host-secret.txt", filepath.Join(ws, "up-link.txt"))
	g.st = gitview.Status{State: "ok", Entries: []gitview.Entry{{P: "new.txt", X: "U"}, {P: "escape.txt", X: "U"}, {P: "inner-link.txt", X: "U"}, {P: "up-link.txt", X: "U"}, {P: "real.txt", X: "U"}}}
	ns := decodeNumstat(t, postNumstat(s, ws, "head"))
	// Through os.Root: a box-planted link, out of the workspace or within it, counts nothing.
	want := []gitview.NumEntry{{P: "new.txt", X: "U", A: 2}, {P: "real.txt", X: "U", A: 3}}
	if ns.State != "ok" || !slices.Equal(ns.Entries, want) {
		t.Fatalf("numstat = %+v, want %+v", ns, want)
	}
}

func TestCountLines(t *testing.T) {
	type res struct {
		lines   int
		bin, ok bool
	}
	nulAt := func(n int) []byte { b := bytes.Repeat([]byte("a"), n+1); b[n] = 0; return b }
	cases := []struct {
		name string
		in   []byte
		want res
	}{
		{"empty", nil, res{0, false, true}},
		{"one line", []byte("a\n"), res{1, false, true}},
		{"no final newline", []byte("a\nb"), res{2, false, true}},
		{"only a newline", []byte("\n"), res{1, false, true}},
		{"no newline at all", []byte("abc"), res{1, false, true}},
		{"NUL in the first byte", []byte{0}, res{0, true, true}},
		{"NUL at the last sniffed byte", nulAt(countSniffBytes - 1), res{0, true, true}},
		{"NUL one past the sniff window", nulAt(countSniffBytes), res{1, false, true}},
		{"exactly the cap", bytes.Repeat([]byte("l\n"), maxCountBytes/2), res{maxCountBytes / 2, false, true}},
		{"one byte over the cap", bytes.Repeat([]byte("l"), maxCountBytes+1), res{0, false, false}},
	}
	for _, c := range cases {
		// Whole, and one byte per Read: the sniff window spans many reads.
		for name, r := range map[string]io.Reader{
			"whole":  io.LimitReader(bytes.NewReader(c.in), maxCountBytes+1),
			"1-byte": io.LimitReader(iotest.OneByteReader(bytes.NewReader(c.in)), maxCountBytes+1),
			"half":   io.LimitReader(iotest.HalfReader(bytes.NewReader(c.in)), maxCountBytes+1),
		} {
			if c.name == "exactly the cap" && name == "1-byte" {
				continue // 2 million one-byte reads: nothing more to learn
			}
			lines, bin, ok := countLines(r)
			if got := (res{lines, bin, ok}); got != c.want {
				t.Errorf("%s (%s): got %+v, want %+v", c.name, name, got, c.want)
			}
		}
	}
	// A file that grew past the cap after its size was read is caught by the limit.
	if _, _, ok := countLines(io.LimitReader(zeros{}, maxCountBytes+1)); ok {
		t.Error("an endless reader was counted")
	}
	// A read error is no count.
	if _, _, ok := countLines(io.MultiReader(strings.NewReader("a\n"), iotest.ErrReader(io.ErrClosedPipe))); ok {
		t.Error("a read error was counted")
	}
}

// zeros is an endless reader of "x\n".
type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = "x\n"[i%2]
	}
	return len(p), nil
}

func TestGitNumstatFreshReachesTheRunner(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	tg := gitview.Target{Top: repo, Common: filepath.Join(repo, ".git")}
	post := func(scope string, fresh any) gitview.Numstat {
		b, _ := json.Marshal(map[string]any{"path": repo, "scope": scope, "fresh": fresh})
		return decodeNumstat(t, gitPost(s.handleGitNumstat, "/api/git/numstat", string(b)))
	}
	post("head", true)
	post("head", false)
	post("branch", true)
	post("branch", false)
	base := gitview.Scope{Branch: true, BaseRef: "refs/remotes/origin/main"}
	want := []fakeNumstatCall{{tg, gitview.Scope{}, true}, {tg, gitview.Scope{}, false}, {tg, base, true}, {tg, base, false}}
	if !slices.Equal(g.numstats, want) {
		t.Fatalf("numstat calls = %+v\nwant %+v", g.numstats, want)
	}
	// The body field is read as status reads it: absent is not fresh.
	g.numstats = nil
	decodeNumstat(t, gitPost(s.handleGitNumstat, "/api/git/numstat", numstatBody(repo, "head")))
	if len(g.numstats) != 1 || g.numstats[0].Fresh {
		t.Fatalf("numstat without fresh = %+v", g.numstats)
	}
	// Only numstat is fresh: the status the untracked counts come from, and the
	// base, are the reused ones (the client refreshes status on its own).
	for i, f := range g.fresh {
		if f {
			t.Fatalf("Status call %d of a fresh numstat request was StatusFresh", i)
		}
	}
	if len(g.baseRefs) != 2 {
		t.Fatalf("BaseRef ran %d times for two branch requests, want 2", len(g.baseRefs))
	}
}

func TestGitNumstatReadsStatusAlongsideNotAfterANumstat(t *testing.T) {
	// A slow diff outlasts Status's 2 s reuse window, so a Status read after it
	// would be a second cold container run in the same request. Status is read
	// at the start, in parallel: it is already made while numstat is still going.
	s, repo, g := untrackedRepo(t, "new.txt")
	mustWrite(t, filepath.Join(repo, "new.txt"), "a\nb\n")
	gate := make(chan struct{})
	g.mu.Lock()
	g.numGate = gate
	g.mu.Unlock()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- postNumstat(s, repo, "head") }()
	deadline := time.Now().Add(5 * time.Second)
	for g.statusCalls() == 0 {
		if time.Now().After(deadline) {
			close(gate)
			t.Fatal("Status was not requested while numstat was still running")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("the request finished before the diff did")
	default:
	}
	close(gate)
	ns := decodeNumstat(t, <-done)
	want := []gitview.NumEntry{{P: "tracked.go", X: "M", A: 1, D: 1}, {P: "new.txt", X: "U", A: 2}}
	if ns.State != "ok" || !slices.Equal(ns.Entries, want) {
		t.Fatalf("numstat = %+v, want %+v", ns, want)
	}
	if n := g.statusCalls(); n != 1 {
		t.Fatalf("Status ran %d times for one request, want 1", n)
	}
}

func decodeBlob(t *testing.T, rr *httptest.ResponseRecorder) gitview.Blob {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rr.Code, rr.Body)
	}
	var b gitview.Blob
	if err := json.Unmarshal(rr.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode %s: %v", rr.Body, err)
	}
	return b
}

func showBody(p, scope string, raw bool) string {
	b, _ := json.Marshal(map[string]any{"path": p, "scope": scope, "raw": raw})
	return string(b)
}

func postShow(s *Server, p, scope string) *httptest.ResponseRecorder {
	return gitPost(s.handleGitShow, "/api/git/show", showBody(p, scope, false))
}

func postShowRaw(s *Server, p, scope string) *httptest.ResponseRecorder {
	return gitPost(s.handleGitShow, "/api/git/show", showBody(p, scope, true))
}

// showRepo is a repo in a host server's $HOME with the given files on disk.
func showRepo(t *testing.T, files ...string) (s *Server, repo string, g *fakeGit) {
	t.Helper()
	s, h, g := hostGitServer(t)
	repo = filepath.Join(h, "repo")
	mainRepo(t, repo)
	for _, f := range files {
		mustWrite(t, filepath.Join(repo, f), "on disk\n")
	}
	return s, repo, g
}

func TestGitShowHandlerOK(t *testing.T) {
	s, repo, g := showRepo(t, "sub/a.go")
	g.blob = gitview.Blob{State: "ok", Reason: "stray", Content: "package a\n"}
	rr := postShow(s, filepath.Join(repo, "sub", "a.go"), "head")
	b := decodeBlob(t, rr)
	if b.State != "ok" || b.Reason != "" || b.Content != "package a\n" || b.Missing || b.Truncated {
		t.Fatalf("show = %+v", b)
	}
	// The wire shape the client reads.
	for _, key := range []string{`"state":"ok"`, `"content":"package a\n"`, `"truncated":false`} {
		if !strings.Contains(rr.Body.String(), key) {
			t.Errorf("reply %s lacks %s", rr.Body, key)
		}
	}
	tg := gitview.Target{Top: repo, Common: filepath.Join(repo, ".git")}
	if len(g.shows) != 1 || g.shows[0] != (fakeShowCall{tg, "HEAD", "sub/a.go", false}) || len(g.bases) != 0 || len(g.statuses) != 0 || len(g.diffs) != 0 {
		t.Fatalf("calls: shows=%+v bases=%+v, want one HEAD Show of sub/a.go in %+v", g.shows, g.bases, tg)
	}

	// A path HEAD does not have, and a cut blob, are what the runner said.
	g.blob = gitview.Blob{State: "ok", Missing: true}
	if b := decodeBlob(t, postShow(s, filepath.Join(repo, "sub", "a.go"), "head")); b.State != "ok" || !b.Missing || b.Content != "" {
		t.Fatalf("missing: %+v", b)
	}
	if rr := postShow(s, filepath.Join(repo, "sub", "a.go"), "head"); !strings.Contains(rr.Body.String(), `"missing":true`) {
		t.Fatalf("missing is not on the wire: %s", rr.Body)
	}
	g.blob = gitview.Blob{State: "ok", Content: "xx", Truncated: true}
	if b := decodeBlob(t, postShow(s, filepath.Join(repo, "sub", "a.go"), "head")); !b.Truncated || b.Content != "xx" {
		t.Fatalf("truncated: %+v", b)
	}
}

func TestGitShowBranchScopeUsesMergeBase(t *testing.T) {
	s, repo, g := showRepo(t, "a.go")
	mb := strings.Repeat("b", 40)
	g.base = gitview.BaseInfo{State: "ok", Branch: "feat", BaseRef: "refs/remotes/origin/develop", BaseName: "origin/develop", MergeBase: mb, Ahead: true}
	// A rev in the body is not read: the rev is the one Base derived.
	body := `{"path":` + strconv.Quote(filepath.Join(repo, "a.go")) + `,"scope":"branch","rev":"-x","baseRef":"refs/heads/evil","base":"HEAD~1"}`
	decodeBlob(t, gitPost(s.handleGitShow, "/api/git/show", body))
	tg := gitview.Target{Top: repo, Common: filepath.Join(repo, ".git")}
	if len(g.shows) != 1 || g.shows[0] != (fakeShowCall{tg, mb, "a.go", false}) || len(g.bases) != 1 || g.bases[0] != tg {
		t.Fatalf("calls: shows=%+v bases=%+v, want one Base and a Show at %s", g.shows, g.bases, mb)
	}
	// "head" never asks for a base.
	g.bases, g.shows = nil, nil
	decodeBlob(t, postShow(s, filepath.Join(repo, "a.go"), "head"))
	if len(g.bases) != 0 || len(g.shows) != 1 || g.shows[0].Rev != "HEAD" {
		t.Fatalf("head: shows=%+v bases=%+v", g.shows, g.bases)
	}
}

func TestGitShowNoBaseUnavailable(t *testing.T) {
	s, repo, g := showRepo(t, "a.go", "img.png")
	for name, c := range map[string]struct {
		base   gitview.BaseInfo
		state  string
		reason string
	}{
		"no base":     {gitview.BaseInfo{State: "nobase", Reason: "git said /secret/path"}, "unavailable", msgGitNoBase},
		"unavailable": {gitview.BaseInfo{State: "unavailable", Reason: "fatal: /secret/path: bad object"}, "unavailable", msgGitNotAvailable},
		"timeout":     {gitview.BaseInfo{State: "unavailable", Reason: "git timed out"}, "unavailable", msgGitTimeout},
		"not a repo":  {gitview.BaseInfo{State: "notrepo"}, "notrepo", ""},
	} {
		g.base = c.base
		for _, rr := range []*httptest.ResponseRecorder{postShow(s, filepath.Join(repo, "a.go"), "branch"), postShowRaw(s, filepath.Join(repo, "img.png"), "branch")} {
			b := decodeBlob(t, rr)
			if b.State != c.state || b.Reason != c.reason || b.Content != "" || b.Missing {
				t.Errorf("%s: show = %+v, want %s %q", name, b, c.state, c.reason)
			}
		}
	}
	if len(g.shows) != 0 {
		t.Fatalf("Show ran with no base: %+v", g.shows)
	}
}

func TestGitShowScopeIsHeadOrBranch(t *testing.T) {
	s, repo, g := showRepo(t, "a.go")
	p := filepath.Join(repo, "a.go")
	for _, scope := range []string{"", "HEAD", "Head", "BRANCH", " branch", "branch\n", "refs/heads/main", "-x", "main", "origin/main", "merge-base"} {
		if rr := postShow(s, p, scope); rr.Code != http.StatusBadRequest {
			t.Errorf("scope %q: code = %d, want 400 (%s)", scope, rr.Code, rr.Body)
		}
	}
	for _, body := range []string{`{"path":` + strconv.Quote(p) + `}`, `{"path":` + strconv.Quote(p) + `,"scope":1}`, `{"path":` + strconv.Quote(p) + `,"scope":null}`, `{"path":` + strconv.Quote(p) + `,"scope":["branch"]}`, `{"path":` + strconv.Quote(p) + `,"scope":"head","raw":"yes"}`} {
		if rr := gitPost(s.handleGitShow, "/api/git/show", body); rr.Code != http.StatusBadRequest {
			t.Errorf("body %s: code = %d, want 400 (%s)", body, rr.Code, rr.Body)
		}
	}
	if g.calls() != 0 {
		t.Fatalf("runner called %d times for a bad scope", g.calls())
	}
	for _, scope := range []string{"head", "branch"} {
		if b := decodeBlob(t, postShow(s, p, scope)); b.State != "ok" {
			t.Errorf("scope %q: %+v", scope, b)
		}
	}
}

func TestGitShowRawImageHeaders(t *testing.T) {
	s, repo, g := showRepo(t, "img.png", "logo.SVG", "pic.jpeg")
	png := []byte("\x89PNG\r\n\x1a\n\x00\xff\xfe not utf-8")
	g.raw = png
	for file, ctype := range map[string]string{"img.png": "image/png", "logo.SVG": "image/svg+xml", "pic.jpeg": "image/jpeg"} {
		g.shows = nil
		rr := postShowRaw(s, filepath.Join(repo, file), "head")
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: code = %d (%s)", file, rr.Code, rr.Body)
		}
		h := rr.Header()
		if h.Get("Content-Type") != ctype {
			t.Errorf("%s: Content-Type = %q, want %q", file, h.Get("Content-Type"), ctype)
		}
		// The two headers the file preview sets: an SVG cannot run script, and nothing is sniffed.
		if h.Get("Content-Security-Policy") != "sandbox" {
			t.Errorf("%s: Content-Security-Policy = %q, want sandbox", file, h.Get("Content-Security-Policy"))
		}
		if h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q, want nosniff", file, h.Get("X-Content-Type-Options"))
		}
		if h.Get("Content-Length") != strconv.Itoa(len(png)) || !bytes.Equal(rr.Body.Bytes(), png) {
			t.Errorf("%s: body %q (length %q), want the blob as it is", file, rr.Body.Bytes(), h.Get("Content-Length"))
		}
		tg := gitview.Target{Top: repo, Common: filepath.Join(repo, ".git")}
		if len(g.shows) != 1 || g.shows[0] != (fakeShowCall{tg, "HEAD", file, true}) {
			t.Errorf("%s: calls = %+v", file, g.shows)
		}
	}
	// At the merge base, the rev is the server's, as for text.
	mb := strings.Repeat("c", 40)
	g.base = gitview.BaseInfo{State: "ok", BaseRef: "refs/heads/main", BaseName: "main", MergeBase: mb}
	g.shows = nil
	if rr := postShowRaw(s, filepath.Join(repo, "img.png"), "branch"); rr.Code != http.StatusOK || !bytes.Equal(rr.Body.Bytes(), png) {
		t.Fatalf("branch raw: %d %q", rr.Code, rr.Body)
	}
	if len(g.shows) != 1 || g.shows[0].Rev != mb || !g.shows[0].Raw {
		t.Fatalf("branch raw calls = %+v", g.shows)
	}
}

func TestGitShowRawOtherOutcomesAreJSON(t *testing.T) {
	s, repo, g := showRepo(t, "img.png")
	p := filepath.Join(repo, "img.png")
	// A new image has no HEAD side, and git failing is a fixed message: both
	// are JSON, so a client tells them from the image by Content-Type.
	g.blob = gitview.Blob{State: "ok", Missing: true}
	rr := postShowRaw(s, p, "head")
	if b := decodeBlob(t, rr); b.State != "ok" || !b.Missing || rr.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("missing: %+v %q", b, rr.Header().Get("Content-Type"))
	}
	g.blob = gitview.Blob{State: "unavailable", Reason: "fatal: /secret/path: bad object"}
	rr = postShowRaw(s, p, "head")
	if b := decodeBlob(t, rr); b.State != "unavailable" || b.Reason != msgGitNotAvailable || rr.Header().Get("Content-Security-Policy") != "" {
		t.Fatalf("unavailable: %+v %v", b, rr.Header())
	}
	// A blob past the cap is cut, and a cut image is not served as the file.
	g.blob = gitview.Blob{State: "ok", Truncated: true}
	g.raw = []byte("\x89PNG")
	if rr := postShowRaw(s, p, "head"); rr.Code != http.StatusRequestEntityTooLarge || bytes.Contains(rr.Body.Bytes(), []byte("PNG")) {
		t.Fatalf("truncated raw: code = %d (%s), want 413", rr.Code, rr.Body)
	}
}

func TestGitShowRawRefusesNonImage(t *testing.T) {
	s, repo, g := showRepo(t, "a.go", "noext", "page.html", "x.png.txt", "real.go")
	// A link named like an image that points at source is read by its real name.
	if err := os.Symlink(filepath.Join(repo, "real.go"), filepath.Join(repo, "link.png")); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"a.go", "noext", "page.html", "x.png.txt", "link.png"} {
		if rr := postShowRaw(s, filepath.Join(repo, f), "head"); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: code = %d, want 400 (%s)", f, rr.Code, rr.Body)
		}
	}
	if g.calls() != 0 {
		t.Fatalf("git ran for a non-image raw request: %+v", g.shows)
	}
	// The same files are fine as text.
	if b := decodeBlob(t, postShow(s, filepath.Join(repo, "page.html"), "head")); b.State != "ok" {
		t.Fatalf("text of a non-image: %+v", b)
	}
}

func TestGitShowRefusedOutside(t *testing.T) {
	s, h, g := hostGitServer(t)
	other := t.TempDir() // a sibling of $HOME, not inside it
	mustMkdirAll(t, filepath.Join(other, ".git", "worktrees", "w"))
	wt := filepath.Join(h, "wt")
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(other, ".git", "worktrees", "w")+"\n")
	mustWrite(t, filepath.Join(wt, "a.go"), "x\n")
	for _, scope := range []string{"head", "branch"} {
		for _, rr := range []*httptest.ResponseRecorder{postShow(s, filepath.Join(wt, "a.go"), scope), postShowRaw(s, filepath.Join(wt, "a.go"), scope)} {
			if b := decodeBlob(t, rr); b.State != "unavailable" || b.Reason != msgGitOutside {
				t.Fatalf("%s: show = %+v, want unavailable %q", scope, b, msgGitOutside)
			}
		}
	}
	// A repository above the viewable tree, and a mount path docker cannot take.
	outside := t.TempDir()
	mainRepo(t, outside)
	mustWrite(t, filepath.Join(outside, "a.go"), "x\n")
	if rr := postShow(s, filepath.Join(outside, "a.go"), "head"); rr.Code != http.StatusForbidden {
		t.Fatalf("outside home: code = %d, want 403", rr.Code)
	}
	unsafe := filepath.Join(h, "a,readonly=false")
	mainRepo(t, unsafe)
	mustWrite(t, filepath.Join(unsafe, "a.go"), "x\n")
	if b := decodeBlob(t, postShow(s, filepath.Join(unsafe, "a.go"), "head")); b.State != "unavailable" || b.Reason != msgGitNotAvailable {
		t.Fatalf("unsafe path: %+v", b)
	}
	// A file link inside the checkout that leaves the viewable tree is refused by the fs policy.
	secret := filepath.Join(t.TempDir(), "secret.go")
	mustWrite(t, secret, "x\n")
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	if err := os.Symlink(secret, filepath.Join(repo, "leak.go")); err != nil {
		t.Fatal(err)
	}
	if rr := postShow(s, filepath.Join(repo, "leak.go"), "head"); rr.Code != http.StatusForbidden {
		t.Fatalf("link out of the tree: code = %d (%s), want 403", rr.Code, rr.Body)
	}
	if n := g.calls(); n != 0 {
		t.Fatalf("runner called %d times for a refused target", n)
	}
}

func TestGitShowNotAFile(t *testing.T) {
	s, repo, g := showRepo(t, "d/a.go")
	for _, p := range []string{filepath.Join(repo, "d"), repo} {
		if rr := postShow(s, p, "head"); rr.Code != http.StatusBadRequest {
			t.Fatalf("%s: code = %d, want 400", p, rr.Code)
		}
	}
	// A file whose directory is not there has nothing to resolve: a 404.
	if rr := postShow(s, filepath.Join(repo, "nodir", "gone.go"), "head"); rr.Code != http.StatusNotFound {
		t.Fatalf("missing: code = %d, want 404", rr.Code)
	}
	if g.calls() != 0 {
		t.Fatal("runner called")
	}
}

func TestGitShowBoxedOK(t *testing.T) {
	s, _, ws := boxedServer(t)
	g := &fakeGit{}
	s.Git = g
	mainRepo(t, ws)
	mustWrite(t, filepath.Join(ws, "sub", "a.go"), "x\n")
	if b := decodeBlob(t, postShow(s, filepath.Join(ws, "sub", "a.go"), "head")); b.State != "ok" {
		t.Fatalf("show = %+v", b)
	}
	// Boxed: the contained root is the only mount (Target.Root).
	want := fakeShowCall{gitview.Target{Root: ws, Top: ws, Common: filepath.Join(ws, ".git")}, "HEAD", "sub/a.go", false}
	if len(g.shows) != 1 || g.shows[0] != want {
		t.Fatalf("calls = %+v, want %+v", g.shows, want)
	}
}

func TestGitShowGuards(t *testing.T) {
	s, h, g := hostGitServer(t)
	mainRepo(t, filepath.Join(h, "repo"))
	get := httptest.NewRecorder()
	s.handleGitShow(get, httptest.NewRequest(http.MethodGet, "/api/git/show?path="+h, nil))
	if get.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET code = %d, want 405", get.Code)
	}
	for _, body := range []string{"", "{", `{"path":"rel/a.go","scope":"head"}`, `{"path":"","scope":"head"}`, `{"path":"/` + strings.Repeat("a", 9000) + `","scope":"head"}`} {
		if rr := gitPost(s.handleGitShow, "/api/git/show", body); rr.Code != http.StatusBadRequest {
			t.Fatalf("body %.40q: code = %d, want 400 (%s)", body, rr.Code, rr.Body)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/git/show", strings.NewReader(showBody(filepath.Join(h, "repo", "a.go"), "head", false)))
	req.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()
	s.handleGitShow(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-origin code = %d, want 403", rr.Code)
	}
	if g.calls() != 0 {
		t.Fatal("runner called on a refused request")
	}
}

func TestGitShowNilRunnerAndNotRepo(t *testing.T) {
	s, h, g := hostGitServer(t)
	plain := filepath.Join(h, "plain")
	mustWrite(t, filepath.Join(plain, "a.go"), "x\n")
	if b := decodeBlob(t, postShow(s, filepath.Join(plain, "a.go"), "head")); b.State != "notrepo" {
		t.Fatalf("not a repo: %+v", b)
	}
	if g.calls() != 0 {
		t.Fatal("runner called for a non-repo")
	}
	s.Git = nil
	mainRepo(t, filepath.Join(h, "repo"))
	for _, rr := range []*httptest.ResponseRecorder{postShow(s, filepath.Join(h, "repo", "a.go"), "head"), postShowRaw(s, filepath.Join(h, "repo", "a.png"), "head")} {
		if b := decodeBlob(t, rr); b.State != "unavailable" || b.Reason != msgGitNoRuntime {
			t.Fatalf("nil runner: %+v", b)
		}
	}
}

func TestGitShowStatesAndReasons(t *testing.T) {
	s, repo, g := showRepo(t, "a.go")
	p := filepath.Join(repo, "a.go")
	// The runner's reason can carry git's stderr: only fixed messages reach the client.
	for raw, want := range map[string]string{
		"fatal: /secret/path: bad object deadbeef": msgGitNotAvailable,
		"git timed out": msgGitTimeout,
		"timed out waiting for a free git container": msgGitTimeout,
		gitview.ReasonNoImage:                        msgGitNoImage,
	} {
		g.blob = gitview.Blob{State: "unavailable", Reason: raw}
		if b := decodeBlob(t, postShow(s, p, "head")); b.State != "unavailable" || b.Reason != want || b.Content != "" {
			t.Fatalf("raw %q: show = %+v, want reason %q", raw, b, want)
		}
	}
	g.blob = gitview.Blob{State: "notrepo"}
	if b := decodeBlob(t, postShow(s, p, "head")); b.State != "notrepo" {
		t.Fatalf("notrepo: %+v", b)
	}
}

// gitFileRel is the one path check of diff and show: what it refuses, both refuse.
func TestGitFileRel(t *testing.T) {
	_, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	mustWrite(t, filepath.Join(repo, "sub", "a b.go"), "x\n")
	mustMkdirAll(t, filepath.Join(repo, "d"))
	if err := os.Symlink(filepath.Join(repo, "sub", "a b.go"), filepath.Join(repo, "link.go")); err != nil {
		t.Fatal(err)
	}
	tg := gitview.Target{Top: repo, Common: filepath.Join(repo, ".git")}
	if rel, err := gitFileRel(tg, filepath.Join(repo, "sub", "a b.go")); err != nil || rel != "sub/a b.go" {
		t.Fatalf("file: rel = %q, err = %v", rel, err)
	}
	for name, c := range map[string]struct {
		abs  string
		want func(error) bool
	}{
		"directory":       {filepath.Join(repo, "d"), func(e error) bool { return errors.Is(e, errGitNotFile) }},
		"the checkout":    {repo, func(e error) bool { return errors.Is(e, errGitNotFile) }},
		"link":            {filepath.Join(repo, "link.go"), func(e error) bool { return errors.Is(e, errGitNotFile) }},
		"gone":            {filepath.Join(repo, "gone.go"), func(e error) bool { return errors.Is(e, os.ErrNotExist) }},
		"outside the top": {filepath.Join(h, "other.go"), func(e error) bool { return errors.Is(e, errGitOutside) }},
	} {
		if name == "outside the top" {
			mustWrite(t, c.abs, "x\n")
		}
		if rel, err := gitFileRel(tg, c.abs); rel != "" || !c.want(err) {
			t.Errorf("%s: rel = %q, err = %v", name, rel, err)
		}
	}
}

// A deleted file's Changes row asks git show for the base text of a path that is
// no longer on disk. Only POST /api/git/show accepts such a vanished leaf (see
// gitTargetShow); every refusal below must leave the runner untouched.

func TestGitShowDeletedTrackedFile(t *testing.T) {
	s, repo, g := showRepo(t, "sub/keep.go")
	g.blob = gitview.Blob{State: "ok", Content: "package gone\n"}
	gone := filepath.Join(repo, "sub", "gone.go")
	for _, scope := range []string{"head", "branch"} {
		g.base = gitview.BaseInfo{State: "ok", MergeBase: "abc123", BaseRef: "origin/main"}
		g.shows, g.bases = nil, nil
		b := decodeBlob(t, postShow(s, gone, scope))
		if b.State != "ok" || b.Missing || b.Content != "package gone\n" {
			t.Fatalf("%s: show = %+v", scope, b)
		}
		rev := "HEAD"
		if scope == "branch" {
			rev = "abc123"
		}
		tg := gitview.Target{Top: repo, Common: filepath.Join(repo, ".git")}
		if len(g.shows) != 1 || g.shows[0] != (fakeShowCall{tg, rev, "sub/gone.go", false}) {
			t.Fatalf("%s: shows = %+v, want one %s Show of sub/gone.go", scope, g.shows, rev)
		}
	}
	// A path the base does not have either is the runner's "missing".
	g.blob = gitview.Blob{State: "ok", Missing: true}
	if b := decodeBlob(t, postShow(s, gone, "head")); b.State != "ok" || !b.Missing {
		t.Fatalf("missing: %+v", b)
	}
	// The raw (image) form takes the same vanished path.
	g.blob = gitview.Blob{State: "ok"}
	g.raw = []byte("PNG")
	g.shows = nil
	rr := postShowRaw(s, filepath.Join(repo, "sub", "gone.png"), "head")
	if rr.Code != http.StatusOK || rr.Body.String() != "PNG" {
		t.Fatalf("raw: code %d body %q", rr.Code, rr.Body)
	}
	if len(g.shows) != 1 || g.shows[0].Rel != "sub/gone.png" || !g.shows[0].Raw {
		t.Fatalf("raw shows = %+v", g.shows)
	}
}

func TestGitShowDeletedFileBoxed(t *testing.T) {
	s, _, ws := boxedServer(t)
	g := &fakeGit{}
	s.Git = g
	mainRepo(t, ws)
	mustMkdirAll(t, filepath.Join(ws, "sub"))
	if b := decodeBlob(t, postShow(s, filepath.Join(ws, "sub", "gone.go"), "head")); b.State != "ok" {
		t.Fatalf("show = %+v", b)
	}
	want := fakeShowCall{gitview.Target{Root: ws, Top: ws, Common: filepath.Join(ws, ".git")}, "HEAD", "sub/gone.go", false}
	if len(g.shows) != 1 || g.shows[0] != want {
		t.Fatalf("calls = %+v, want %+v", g.shows, want)
	}
}

func TestGitShowVanishedLeafRefusals(t *testing.T) {
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	mustWrite(t, filepath.Join(repo, "a.go"), "x\n")
	outside := t.TempDir() // a sibling of $HOME, not inside it
	mustMkdirAll(t, filepath.Join(outside, "d"))
	// A parent that is a link to a directory outside the viewable tree.
	mustSymlink(t, filepath.Join(outside, "d"), filepath.Join(repo, "out"))
	// A leaf that is a dangling link (to a missing file, and to one outside).
	mustSymlink(t, filepath.Join(repo, "nowhere.go"), filepath.Join(repo, "dangling.go"))
	mustSymlink(t, filepath.Join(outside, "nowhere.go"), filepath.Join(repo, "dangling-out.go"))

	cases := []struct {
		name string
		path string
		code int
	}{
		{"symlinked parent outside the tree", filepath.Join(repo, "out", "gone.go"), http.StatusForbidden},
		{"dangling link leaf", filepath.Join(repo, "dangling.go"), http.StatusNotFound},
		{"dangling link leaf to outside", filepath.Join(repo, "dangling-out.go"), http.StatusNotFound},
		{"missing parent", filepath.Join(repo, "nodir", "gone.go"), http.StatusNotFound},
		{"parent is a file", filepath.Join(repo, "a.go", "gone.go"), http.StatusNotFound},
		{"parent is a dangling link", filepath.Join(repo, "dangling.go", "gone.go"), http.StatusNotFound},
		{"outside home", filepath.Join(outside, "gone.go"), http.StatusForbidden},
	}
	for _, c := range cases {
		for _, scope := range []string{"head", "branch"} {
			if rr := postShow(s, c.path, scope); rr.Code != c.code {
				t.Errorf("%s (%s): code = %d (%s), want %d", c.name, scope, rr.Code, rr.Body, c.code)
			}
		}
	}
	if n := g.calls(); n != 0 {
		t.Fatalf("runner called %d times for a refused target: %+v", n, g.shows)
	}
}

func TestGitShowVanishedLeafDenied(t *testing.T) {
	s, h, ws := boxedServer(t)
	g := &fakeGit{}
	s.Git = g
	mainRepo(t, ws)
	// A vanished file in the denied tree (the data dir), and one reached by a
	// link from the workspace into it.
	mainRepo(t, filepath.Join(h, "data"))
	mustSymlink(t, filepath.Join(h, "data"), filepath.Join(ws, "datalink"))
	if rr := postShow(s, filepath.Join(h, "data", "gone.go"), "head"); rr.Code != http.StatusForbidden {
		t.Errorf("in the denied tree: code = %d (%s), want 403", rr.Code, rr.Body)
	}
	// Through a box-planted link the strict walk leaves resolution to EvalSymlinks,
	// which lands outside the contained root: refused as "outside".
	if b := decodeBlob(t, postShow(s, filepath.Join(ws, "datalink", "gone.go"), "head")); b.State != "unavailable" || b.Reason != msgGitOutside {
		t.Errorf("through a link to the denied tree: %+v, want unavailable %q", b, msgGitOutside)
	}
	if g.calls() != 0 {
		t.Fatal("runner called")
	}
}

func TestGitShowVanishedLeafBoxedEscapes(t *testing.T) {
	s, h, ws := boxedServer(t)
	g := &fakeGit{}
	s.Git = g
	mainRepo(t, ws)
	hostDir := filepath.Join(h, "hostdir")
	mustMkdirAll(t, hostDir)
	// A box-planted link out of the contained root, used as a parent.
	mustSymlink(t, hostDir, filepath.Join(ws, "out"))
	// A dangling link leaf inside the box.
	mustSymlink(t, filepath.Join(ws, "nowhere"), filepath.Join(ws, "dangling.go"))
	mustWrite(t, filepath.Join(ws, "a.go"), "x\n")

	if b := decodeBlob(t, postShow(s, filepath.Join(ws, "out", "gone.go"), "head")); b.State != "unavailable" || b.Reason != msgGitOutside {
		t.Errorf("link parent out of the root: %+v, want unavailable %q", b, msgGitOutside)
	}
	for _, p := range []string{filepath.Join(ws, "dangling.go"), filepath.Join(ws, "nodir", "gone.go")} {
		if rr := postShow(s, p, "head"); rr.Code != http.StatusNotFound {
			t.Errorf("%s: code = %d (%s), want 404", p, rr.Code, rr.Body)
		}
	}
	// A file as the parent: EvalSymlinks says "not a directory", an "outside" as before.
	if b := decodeBlob(t, postShow(s, filepath.Join(ws, "a.go", "gone.go"), "head")); b.State != "unavailable" || b.Reason != msgGitOutside {
		t.Errorf("file as parent: %+v, want unavailable %q", b, msgGitOutside)
	}
	if g.calls() != 0 {
		t.Fatalf("runner called: %+v", g.shows)
	}
}

// A link parent that stays inside the tree is followed (as for an existing
// file), and git is asked about the resolved path.
func TestGitShowVanishedLeafUnderInTreeLink(t *testing.T) {
	s, repo, g := showRepo(t, "real/keep.go")
	mustSymlink(t, filepath.Join(repo, "real"), filepath.Join(repo, "alias"))
	if b := decodeBlob(t, postShow(s, filepath.Join(repo, "alias", "gone.go"), "head")); b.State != "ok" {
		t.Fatalf("show = %+v", b)
	}
	if len(g.shows) != 1 || g.shows[0].Rel != "real/gone.go" {
		t.Fatalf("shows = %+v, want rel real/gone.go", g.shows)
	}
}

// A path that names a directory (also through "." and ".." which the resolver
// cleans away) is not a vanished leaf.
func TestGitShowVanishedLeafNotDotNames(t *testing.T) {
	s, repo, g := showRepo(t, "sub/keep.go")
	for _, p := range []string{filepath.Join(repo, "sub", "gone", ".."), repo + "/sub/gone/../.", filepath.Join(repo, "sub") + "/."} {
		if rr := postShow(s, p, "head"); rr.Code != http.StatusBadRequest && rr.Code != http.StatusNotFound {
			t.Errorf("%s: code = %d (%s), want 400/404", p, rr.Code, rr.Body)
		}
	}
	if g.calls() != 0 {
		t.Fatalf("runner called: %+v", g.shows)
	}
}

// Only show takes a vanished leaf: diff, numstat, base and status refuse it as before.
func TestGitVanishedLeafOnlyForShow(t *testing.T) {
	s, repo, g := showRepo(t, "sub/keep.go")
	gone := filepath.Join(repo, "sub", "gone.go")
	for name, h := range map[string]http.HandlerFunc{
		"diff": s.handleGitDiff, "status": s.handleGitStatus, "base": s.handleGitBase, "numstat": s.handleGitNumstat,
	} {
		b, _ := json.Marshal(map[string]any{"path": gone, "scope": "head"})
		if rr := gitPost(h, "/api/git/"+name, string(b)); rr.Code != http.StatusNotFound {
			t.Errorf("%s: code = %d (%s), want 404", name, rr.Code, rr.Body)
		}
	}
	if g.calls() != 0 {
		t.Fatalf("runner called for a vanished leaf outside show")
	}
}

// gitTargetShow is gitTarget for every path that exists.
func TestGitTargetShowExistingSameAsGitTarget(t *testing.T) {
	s, repo, _ := showRepo(t, "sub/a.go")
	for _, p := range []string{filepath.Join(repo, "sub", "a.go"), filepath.Join(repo, "sub"), repo} {
		t1, a1, e1 := s.gitTarget(p)
		t2, a2, e2 := s.gitTargetShow(p)
		if t1 != t2 || a1 != a2 || e1 != e2 {
			t.Errorf("%s: gitTarget = %+v %q %v, gitTargetShow = %+v %q %v", p, t1, a1, e1, t2, a2, e2)
		}
	}
}
