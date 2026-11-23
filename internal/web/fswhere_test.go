package web

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
)

type whereAns struct {
	Path string `json:"path"`
	Top  string `json:"top"`
}

func whereGet(t *testing.T, s *Server, p string) (int, whereAns) {
	t.Helper()
	rr := fsGet(s.handleFsWhere, "/api/fs/where", p)
	var a whereAns
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &a); err != nil {
			t.Fatalf("decode %s: %v", rr.Body, err)
		}
	}
	return rr.Code, a
}

// /api/fs/where says where a path really is (its resolved spelling, as /api/fs/list's "path" does for a folder) and the
// top of the git checkout holding it, when the fs policy serves that top in the same tree. No git runs.
func TestFsWhere(t *testing.T) {
	s, home, ws := boxedServer(t)
	repo := filepath.Join(home, "repo")
	mainRepo(t, repo)
	f := filepath.Join(repo, "src", "a.go")
	mustWrite(t, f, "x")
	mustSymlink(t, "repo/src", filepath.Join(home, "lnk"))
	plain := filepath.Join(home, "plain", "b.txt")
	mustWrite(t, plain, "x")

	if code, a := whereGet(t, s, f); code != 200 || a.Path != f || a.Top != repo {
		t.Errorf("file in a repo: %d %+v, want path %s top %s", code, a, f, repo)
	}
	if code, a := whereGet(t, s, filepath.Join(repo, "src")); code != 200 || a.Path != filepath.Join(repo, "src") || a.Top != repo {
		t.Errorf("folder in a repo: %d %+v", code, a)
	}
	if code, a := whereGet(t, s, filepath.Join(home, "lnk", "a.go")); code != 200 || a.Path != f || a.Top != repo {
		t.Errorf("a link spelling: %d %+v, want the resolved path %s", code, a, f)
	}
	if code, a := whereGet(t, s, plain); code != 200 || a.Path != plain || a.Top != "" {
		t.Errorf("no repo: %d %+v, want no top", code, a)
	}
	if code, _ := whereGet(t, s, filepath.Join(t.TempDir(), "x")); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("outside the roots: %d, want a refusal", code)
	}
	if code, _ := whereGet(t, s, filepath.Join(home, "data", "cookie.secret")); code != http.StatusForbidden {
		t.Errorf("denied tree: %d, want 403", code)
	}
	if code, _ := whereGet(t, s, filepath.Join(home, "nope.txt")); code != http.StatusNotFound {
		t.Errorf("missing: %d, want 404", code)
	}
	if code, _ := whereGet(t, s, "rel/x"); code != http.StatusBadRequest {
		t.Errorf("relative: %d, want 400", code)
	}
	// A boxed workspace's repo: its top is the workspace's own, through the same tree.
	mainRepo(t, ws)
	mustWrite(t, filepath.Join(ws, "w.txt"), "x")
	if code, a := whereGet(t, s, filepath.Join(ws, "w.txt")); code != 200 || a.Top != ws {
		t.Errorf("boxed workspace repo: %d %+v, want top %s", code, a, ws)
	}
}

// A .git above every served root (home itself inside a repository) is never a top: that folder is not one the tree
// may be rooted at.
func TestFsWhereTopAboveTheRoots(t *testing.T) {
	up, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mainRepo(t, up)
	home := filepath.Join(up, "home")
	mustWrite(t, filepath.Join(home, "f.txt"), "x")
	s := &Server{Home: home}
	if code, a := whereGet(t, s, filepath.Join(home, "f.txt")); code != 200 || a.Top != "" {
		t.Errorf("repo above home: %d %+v, want no top", code, a)
	}
}

func TestFsWhereMethod(t *testing.T) {
	s, home, _ := boxedServer(t)
	rr := gitPost(s.handleFsWhere, "/api/fs/where?path="+home, "")
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d, want 405", rr.Code)
	}
}
