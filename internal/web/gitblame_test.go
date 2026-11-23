package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/gitview"
)

func decodeBlame(t *testing.T, rr *httptest.ResponseRecorder) blameReply {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rr.Code, rr.Body)
	}
	var b blameReply
	if err := json.Unmarshal(rr.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode %s: %v", rr.Body, err)
	}
	return b
}

// blameRepo is a host checkout with one tracked file, f.tf.
func blameRepo(t *testing.T) (*Server, string, *fakeGit) {
	t.Helper()
	s, h, g := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	mustWrite(t, filepath.Join(repo, "f.tf"), "a\nb\n")
	g.st = gitview.Status{State: "ok", Head: strings.Repeat("b", 40), Entries: []gitview.Entry{}}
	return s, repo, g
}

func TestGitBlameMethodAndRuntime(t *testing.T) {
	s, repo, _ := blameRepo(t)
	rr := httptest.NewRecorder()
	s.handleGitBlame(rr, httptest.NewRequest(http.MethodGet, "/api/git/blame", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET code = %d, want 405", rr.Code)
	}
	s.Git = nil
	if b := decodeBlame(t, gitPost(s.handleGitBlame, "/api/git/blame", pathBody(filepath.Join(repo, "f.tf")))); b.State != "unavailable" || b.Reason != msgGitNoRuntime {
		t.Fatalf("no runtime: %+v", b)
	}
}

func TestGitBlameOK(t *testing.T) {
	s, repo, g := blameRepo(t)
	b := decodeBlame(t, gitPost(s.handleGitBlame, "/api/git/blame", pathBody(filepath.Join(repo, "f.tf"))))
	if b.State != "ok" || b.Version != hashText([]byte("a\nb\n")) || len(b.Lines) != 1 || b.Remote != nil {
		t.Fatalf("reply %+v", b)
	}
	if len(g.blames) != 1 {
		t.Fatalf("%d blame calls", len(g.blames))
	}
	c := g.blames[0]
	if c.Rel != "f.tf" || c.T.Top != repo || c.K.Head != strings.Repeat("b", 40) || c.K.Version != b.Version || c.K.IgnoreRevs || !c.Still {
		t.Fatalf("call %+v", c)
	}
}

func TestGitBlameIgnoreRevsFile(t *testing.T) {
	s, repo, g := blameRepo(t)
	f := filepath.Join(repo, "f.tf")
	mustWrite(t, filepath.Join(repo, ".git-blame-ignore-revs"), strings.Repeat("c", 40)+"\n")
	gitPost(s.handleGitBlame, "/api/git/blame", pathBody(f))
	if !g.blames[0].K.IgnoreRevs {
		t.Fatal("a regular .git-blame-ignore-revs was not passed")
	}
	os.Remove(filepath.Join(repo, ".git-blame-ignore-revs"))
	if err := os.Symlink(f, filepath.Join(repo, ".git-blame-ignore-revs")); err != nil {
		t.Fatal(err)
	}
	gitPost(s.handleGitBlame, "/api/git/blame", pathBody(f))
	if g.blames[1].K.IgnoreRevs {
		t.Fatal("a linked .git-blame-ignore-revs was passed")
	}
}

func TestGitBlameNoRunsNeeded(t *testing.T) {
	s, repo, g := blameRepo(t)
	mustWrite(t, filepath.Join(repo, "new.txt"), "x\n")
	g.st.Entries = []gitview.Entry{{P: "new.txt", X: "U"}}
	if b := decodeBlame(t, gitPost(s.handleGitBlame, "/api/git/blame", pathBody(filepath.Join(repo, "new.txt")))); b.State != "untracked" || b.Version == "" {
		t.Fatalf("untracked: %+v", b)
	}
	g.st = gitview.Status{State: "ok", NoHead: true, Entries: []gitview.Entry{}}
	if b := decodeBlame(t, gitPost(s.handleGitBlame, "/api/git/blame", pathBody(filepath.Join(repo, "f.tf")))); b.State != "untracked" {
		t.Fatalf("no commits: %+v", b)
	}
	g.st = gitview.Status{State: "ok", Head: strings.Repeat("b", 40), Entries: []gitview.Entry{}}
	mustWrite(t, filepath.Join(repo, "big.txt"), strings.Repeat("x", maxTextBytes+1))
	if b := decodeBlame(t, gitPost(s.handleGitBlame, "/api/git/blame", pathBody(filepath.Join(repo, "big.txt")))); b.State != "toobig" {
		t.Fatalf("big: %+v", b)
	}
	if err := os.WriteFile(filepath.Join(repo, "bin"), []byte{0, 1, 2, 0}, 0o644); err != nil {
		t.Fatal(err)
	}
	if b := decodeBlame(t, gitPost(s.handleGitBlame, "/api/git/blame", pathBody(filepath.Join(repo, "bin")))); b.State != "toobig" {
		t.Fatalf("binary: %+v", b)
	}
	if len(g.blames) != 0 {
		t.Fatalf("%d blame calls, want 0", len(g.blames))
	}
}

func TestGitBlameRefusals(t *testing.T) {
	s, repo, g := blameRepo(t)
	h := filepath.Dir(repo)
	mustWrite(t, filepath.Join(h, "plain", "x.txt"), "x\n")
	if b := decodeBlame(t, gitPost(s.handleGitBlame, "/api/git/blame", pathBody(filepath.Join(h, "plain", "x.txt")))); b.State != "notrepo" {
		t.Fatalf("not a repo: %+v", b)
	}
	if rr := gitPost(s.handleGitBlame, "/api/git/blame", pathBody(repo)); rr.Code != http.StatusBadRequest {
		t.Fatalf("directory: %d", rr.Code)
	}
	if rr := gitPost(s.handleGitBlame, "/api/git/blame", pathBody(filepath.Join(repo, "gone.tf"))); rr.Code != http.StatusNotFound {
		t.Fatalf("gone: %d", rr.Code)
	}
	if rr := gitPost(s.handleGitBlame, "/api/git/blame", `{"path":"rel/f.tf"}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("relative: %d", rr.Code)
	}
	g.st = gitview.Status{State: "unavailable", Reason: "fatal: /secret/path", Entries: []gitview.Entry{}}
	if b := decodeBlame(t, gitPost(s.handleGitBlame, "/api/git/blame", pathBody(filepath.Join(repo, "f.tf")))); b.State != "unavailable" || b.Reason != msgGitNotAvailable {
		t.Fatalf("status unavailable: %+v", b)
	}
	if len(g.blames) != 0 {
		t.Fatalf("%d blame calls, want 0", len(g.blames))
	}
}

func TestGitBlameReasonsAreFixed(t *testing.T) {
	s, repo, g := blameRepo(t)
	f := filepath.Join(repo, "f.tf")
	for state, want := range map[string]string{"unavailable": msgGitNotAvailable, "slow": msgBlameSlow, "toobig": "", "notrepo": ""} {
		g.blame = gitview.Blame{State: state, Reason: "fatal: /home/x/secret"}
		b := decodeBlame(t, gitPost(s.handleGitBlame, "/api/git/blame", pathBody(f)))
		if b.State != state || b.Reason != want || bytes.Contains([]byte(b.Reason), []byte("secret")) {
			t.Errorf("%s: %+v", state, b)
		}
	}
	g.blame = gitview.Blame{State: "unavailable", Reason: gitview.ReasonNoImage}
	if b := decodeBlame(t, gitPost(s.handleGitBlame, "/api/git/blame", pathBody(f))); b.Reason != msgGitNoImage {
		t.Errorf("no image: %+v", b)
	}
}

func TestGitBlameFileChangedWhileRead(t *testing.T) {
	s, repo, g := blameRepo(t)
	f := filepath.Join(repo, "f.tf")
	g.blameHook = func() { mustWrite(t, f, "a\nb\nc\n") }
	if b := decodeBlame(t, gitPost(s.handleGitBlame, "/api/git/blame", pathBody(f))); b.State != "changed" || len(b.Lines) != 0 {
		t.Fatalf("reply %+v", b)
	}
	if g.blames[0].Still {
		t.Fatal("still() held for a file that changed")
	}
}

func TestGitBlameRemote(t *testing.T) {
	s, repo, _ := blameRepo(t)
	mustWrite(t, filepath.Join(repo, ".git", "config"), "[remote \"origin\"]\n\turl = https://github.com/example/demo.git\n")
	b := decodeBlame(t, gitPost(s.handleGitBlame, "/api/git/blame", pathBody(filepath.Join(repo, "f.tf"))))
	if b.Remote == nil || b.Remote.Kind != "github" || b.Remote.Base != "https://github.com/example/demo" {
		t.Fatalf("remote %+v", b.Remote)
	}
}

func TestGitBlameGuards(t *testing.T) {
	s, repo, g := blameRepo(t)
	for _, body := range []string{"", "{", `{"path":""}`, `{"path":"/` + strings.Repeat("a", 9000) + `"}`} {
		if rr := gitPost(s.handleGitBlame, "/api/git/blame", body); rr.Code != http.StatusBadRequest {
			t.Fatalf("body %.40q: code = %d, want 400", body, rr.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/git/blame", strings.NewReader(pathBody(filepath.Join(repo, "f.tf"))))
	req.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()
	s.handleGitBlame(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-origin code = %d, want 403", rr.Code)
	}
	if g.calls() != 0 {
		t.Fatal("runner called on a refused request")
	}
}
