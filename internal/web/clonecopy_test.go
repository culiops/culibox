package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/box"
)

func TestSuggestCopyDir(t *testing.T) {
	cases := []struct{ name, home, profile, repo, want string }{
		{"profile name", "/h", "work", "/h/code/app", "/h/box-clones/work"},
		{"dots, dashes and underscores", "/h", "my_app-2.x", "/h/app", "/h/box-clones/my_app-2.x"},
		{"profile with a space uses the repo", "/h", "my app", "/h/code/app", "/h/box-clones/app"},
		{"empty profile uses the repo", "/h", "", "/h/code/app", "/h/box-clones/app"},
		{"restart label uses the repo", "/h", "(restart abc)", "/h/code/app", "/h/box-clones/app"},
		{"path in the profile uses the repo", "/h", "../evil", "/h/code/app", "/h/box-clones/app"},
		{"slash in the profile uses the repo", "/h", "a/b", "/h/code/app", "/h/box-clones/app"},
		{"dot-leading profile uses the repo", "/h", ".hidden", "/h/code/app", "/h/box-clones/app"},
		{"dotdot profile uses the repo", "/h", "..", "/h/code/app", "/h/box-clones/app"},
		{"non-ascii profile uses the repo", "/h", "dự-án", "/h/code/app", "/h/box-clones/app"},
		{"unusable both", "/h", "a b", "/h/code/my app", "/h/box-clones/copy"},
		{"repo is a dot dir", "/h", "", "/h/.dotfiles", "/h/box-clones/copy"},
		{"repo is the root", "/h", "", "/", "/h/box-clones/copy"},
		{"no repo", "/h", "", "", "/h/box-clones/copy"},
	}
	for _, c := range cases {
		if got := suggestCopyDir(c.home, c.profile, c.repo); got != filepath.FromSlash(c.want) {
			t.Errorf("%s: suggestCopyDir(%q, %q, %q) = %q, want %q", c.name, c.home, c.profile, c.repo, got, c.want)
		}
	}
}

func TestWriteSpawnErrNeedsCopy(t *testing.T) {
	rec := httptest.NewRecorder()
	err := &box.NeedsCopyError{Repo: "/h/code/app", Inside: true, LFS: true}
	writeSpawnErr(rec, "work", "/h", err)
	var b map[string]any
	if e := json.Unmarshal(rec.Body.Bytes(), &b); e != nil {
		t.Fatal(e)
	}
	want := map[string]any{
		"error": err.Error(), "code": "needs_copy", "repo": "/h/code/app",
		"suggested": filepath.FromSlash("/h/box-clones/work"), "worktree": false, "inside": true, "lfs": true,
	}
	if rec.Code != http.StatusBadRequest || len(b) != len(want) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for k, v := range want {
		if b[k] != v {
			t.Errorf("%s = %v, want %v (body %s)", k, b[k], v, rec.Body)
		}
	}
}

// Wrapped, and through the spawn handler: the API's home reaches the suggestion,
// and the refusal is not the generic ErrUnboxableWorkspace body.
func TestSpawnNeedsCopyThroughTheHandler(t *testing.T) {
	svc := &mockSvc{spawnErr: &box.NeedsCopyError{Repo: "/h/app", Worktree: true}}
	a := &API{Sessions: svc, Home: "/h"}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"agent":"my app","cwd":"/h/app"}`))
	a.handleSessions(rec, req)
	var b map[string]any
	json.Unmarshal(rec.Body.Bytes(), &b)
	if rec.Code != http.StatusBadRequest || b["code"] != "needs_copy" || b["worktree"] != true ||
		b["suggested"] != filepath.FromSlash("/h/box-clones/app") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
