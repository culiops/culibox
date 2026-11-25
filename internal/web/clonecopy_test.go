package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/clone"
	"github.com/culiops/culibox/internal/config"
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
		"suggested": filepath.FromSlash("/h/box-clones/work"), "worktree": false, "inside": true, "lfs": true, "git_home": false,
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

// A restart refused with needs_copy suggests the restarted session's profile
// name, not the "(restart <id>)" log label.
func TestRestartNeedsCopySuggestsTheProfile(t *testing.T) {
	svc := &mockSvc{restartErr: &box.NeedsCopyError{Repo: "/h/code/app"}, getOK: true, getAgent: "work"}
	a := &API{Sessions: svc, Home: "/h"}
	rec := httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/claude-3/restart", nil))
	var b map[string]any
	json.Unmarshal(rec.Body.Bytes(), &b)
	if rec.Code != http.StatusBadRequest || b["code"] != "needs_copy" || b["suggested"] != filepath.FromSlash("/h/box-clones/work") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func estimatePOST(s *Server, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.handleCloneEstimate(rec, httptest.NewRequest(http.MethodPost, "/api/clone/estimate", strings.NewReader(body)))
	return rec
}

func TestEstimateHandlerReturnsSnakeCaseJSON(t *testing.T) {
	var gotOf, gotDir string
	s := &Server{CloneEstimate: func(_ context.Context, of, dir string) (clone.Estimate, error) {
		gotOf, gotDir = of, dir
		return clone.Estimate{GitBytes: 1, TreeBytes: 2, FreeBytes: 3, Enough: true, Partial: true, LFS: true}, nil
	}}
	rec := estimatePOST(s, `{"of":"/h/repo","dir":"/h/box-clones/work"}`)
	if rec.Code != http.StatusOK || gotOf != "/h/repo" || gotDir != "/h/box-clones/work" {
		t.Fatalf("%d %s (of=%q dir=%q)", rec.Code, rec.Body, gotOf, gotDir)
	}
	var b map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"git_bytes": 1.0, "tree_bytes": 2.0, "free_bytes": 3.0, "enough": true, "partial": true, "lfs": true}
	if len(b) != len(want) {
		t.Fatalf("body %s, want exactly %v", rec.Body, want)
	}
	for k, v := range want {
		if b[k] != v {
			t.Errorf("%s = %v, want %v", k, b[k], v)
		}
	}
}

func TestEstimateHandlerValidatesPaths(t *testing.T) {
	called := false
	s := &Server{CloneEstimate: func(context.Context, string, string) (clone.Estimate, error) {
		called = true
		return clone.Estimate{}, nil
	}}
	for _, body := range []string{
		"", "{", "[]", `{"of":"/h/repo"}`, `{"dir":"/h/copy"}`, `{"of":"repo","dir":"/h/copy"}`, `{"of":"/h/repo","dir":"copy"}`,
		`{"of":"/h/repo/../x","dir":"/h/copy"}`, `{"of":"/h/repo","dir":"/h//copy"}`, `{"of":"/h/repo/","dir":"/h/copy"}`,
		`{"of":"/h/re\npo","dir":"/h/copy"}`, `{"of":"/h/repo","dir":"/h/co\u0000py"}`,
		`{"of":"/h/repo","dir":"/h/copy","free_only":true,"git_bytes":-1}`,
		`{"of":"/h/repo","dir":"/h/copy","free_only":true,"tree_bytes":1125899906842625}`,
	} {
		if rec := estimatePOST(s, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%q: code %d, want 400", body, rec.Code)
		}
	}
	if called {
		t.Fatal("the estimate ran for an invalid request")
	}
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		s.handleCloneEstimate(rec, httptest.NewRequest(m, "/api/clone/estimate?of=/a&dir=/b", nil))
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost || called {
			t.Errorf("%s: code %d Allow %q called %v, want 405, POST and not called", m, rec.Code, rec.Header().Get("Allow"), called)
		}
	}
}

// A location edit asks for the free space alone: the repo is not estimated
// again, and the sizes sent back are judged with the same rule (#371 C4).
func TestEstimateHandlerFreeOnly(t *testing.T) {
	called := false
	s := &Server{CloneEstimate: func(context.Context, string, string) (clone.Estimate, error) {
		called = true
		return clone.Estimate{}, nil
	}}
	dir := filepath.Join(t.TempDir(), "box-clones", "work")
	rec := estimatePOST(s, fmt.Sprintf(`{"of":"/h/repo","dir":%q,"free_only":true,"git_bytes":1000,"tree_bytes":2000}`, dir))
	if rec.Code != http.StatusOK || called {
		t.Fatalf("%d %s called=%v; want 200 and no repo estimate", rec.Code, rec.Body, called)
	}
	var e clone.Estimate
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	want, err := clone.FreeFor(dir, 1000, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if e.GitBytes != 1000 || e.TreeBytes != 2000 || e.FreeBytes <= 0 || e.Enough != want.Enough || e.Partial || e.LFS {
		t.Fatalf("free-only estimate %+v, want the sizes back, the free space and FreeFor's verdict %+v", e, want)
	}
}

// The estimate does work on the host, so it is a POST behind csrfGuard: a
// cross-site cookie POST never reaches it, and a cross-site GET navigation
// (which SameSite=Lax would send the cookie with) is 405 (#371 S3).
func TestEstimateCrossSiteRefusedThroughHandlerChain(t *testing.T) {
	called := 0
	s := &Server{AllowedHosts: []string{"example.com"}, API: &API{Sessions: &mockSvc{}}, Terminal: http.NotFoundHandler(),
		Profiles: NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {}),
		CloneEstimate: func(context.Context, string, string) (clone.Estimate, error) {
			called++
			return clone.Estimate{}, nil
		}}
	ck := issueCookie(t, s)
	h := s.Handler()
	do := func(method, target string, hdr [2]string) int {
		r := httptest.NewRequest(method, target, strings.NewReader(`{"of":"/h/repo","dir":"/h/copy"}`))
		r.AddCookie(ck)
		if hdr[0] != "" {
			r.Header.Set(hdr[0], hdr[1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	if got := do("POST", "/api/clone/estimate", [2]string{"Sec-Fetch-Site", "cross-site"}); got != http.StatusForbidden {
		t.Errorf("cross-site cookie POST: got %d, want 403", got)
	}
	if got := do("POST", "/api/clone/estimate", [2]string{"Origin", "http://evil.example"}); got != http.StatusForbidden {
		t.Errorf("foreign-origin cookie POST: got %d, want 403", got)
	}
	if got := do("GET", "/api/clone/estimate?of=/h/repo&dir=/h/copy", [2]string{"Sec-Fetch-Site", "cross-site"}); got != http.StatusMethodNotAllowed {
		t.Errorf("cross-site GET: got %d, want 405", got)
	}
	if called != 0 {
		t.Fatalf("the estimate ran %d times for refused requests", called)
	}
	if got := do("POST", "/api/clone/estimate", [2]string{"Sec-Fetch-Site", "same-origin"}); got != http.StatusOK {
		t.Errorf("same-origin POST: got %d, want 200", got)
	}
	if called != 1 {
		t.Fatalf("the estimate ran %d times, want 1", called)
	}
}

func TestEstimateHandlerRefusesBoxOwned(t *testing.T) {
	s := &Server{CloneEstimate: func(_ context.Context, of, _ string) (clone.Estimate, error) {
		return clone.Estimate{}, fmt.Errorf("%w: clone_of %s is box-owned", box.ErrUnboxableWorkspace, of)
	}}
	rec := estimatePOST(s, `{"of":"/h/box-clones/work","dir":"/h/box-clones/other"}`)
	var b map[string]string
	json.Unmarshal(rec.Body.Bytes(), &b)
	if rec.Code != http.StatusBadRequest || !strings.Contains(b["error"], "box-owned") {
		t.Fatalf("%d %s, want 400 naming the box-owned refusal", rec.Code, rec.Body)
	}
}

func TestEstimateHandlerOtherErrors(t *testing.T) {
	for _, c := range []struct {
		err  error
		code int
	}{
		{fmt.Errorf("%w: clone mode is unavailable", box.ErrBoxUnavailable), http.StatusServiceUnavailable},
		{errors.New("git exploded at /secret/path"), http.StatusInternalServerError},
	} {
		s := &Server{CloneEstimate: func(context.Context, string, string) (clone.Estimate, error) { return clone.Estimate{}, c.err }}
		rec := estimatePOST(s, `{"of":"/h/repo","dir":"/h/copy"}`)
		if rec.Code != c.code {
			t.Errorf("%v: code %d, want %d", c.err, rec.Code, c.code)
		}
		if c.code == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "/secret/path") {
			t.Errorf("a generic failure leaked its detail: %s", rec.Body)
		}
	}
}

// The route sits behind the same auth as the profile API, and exists only when
// the server has an estimate.
func TestEstimateRouteIsProtectedAndOptional(t *testing.T) {
	s, tok := testServerWithAuth(t)
	s.CloneEstimate = func(context.Context, string, string) (clone.Estimate, error) {
		return clone.Estimate{Enough: true}, nil
	}
	h := s.Handler()
	const url, body = "/api/clone/estimate", `{"of":"/h/repo","dir":"/h/copy"}`

	req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Host = "example.com"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusSeeOther && rec.Code != http.StatusFound {
		t.Errorf("no credential: code %d, want a refusal", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Host = "example.com"
	req.Header.Set("Authorization", "Bearer "+tok)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enough":true`) {
		t.Errorf("bearer: %d %s", rec.Code, rec.Body)
	}

	// A server without the hook has no such route: the catch-all answers, not the handler.
	s.CloneEstimate = nil
	req = httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Host = "example.com"
	req.Header.Set("Authorization", "Bearer "+tok)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "git_bytes") {
		t.Errorf("no CloneEstimate but the estimate route answered: %s", rec.Body)
	}
}

// The server decides whether a refused folder sits in the repo at the home directory, on resolved paths: a home that is a symlink
// still matches its target, and a repo elsewhere does not (#371).
func TestGitIsHomeResolvesSymlinks(t *testing.T) {
	real := realTemp(t)
	link := filepath.Join(realTemp(t), "home-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	other := realTemp(t)
	for _, c := range []struct {
		name, home, repo string
		want             bool
	}{
		{"same", real, real, true},
		{"home is a symlink to the repo", link, real, true},
		{"repo is a symlink to home", real, link, true},
		{"trailing slash", real + "/", real, true},
		{"another repo", real, other, false},
		{"a repo under home", real, filepath.Join(real, "code"), false},
		{"no home", "", real, false},
		{"no repo", real, "", false},
	} {
		if got := gitIsHome(c.home, c.repo); got != c.want {
			t.Errorf("%s: gitIsHome(%q, %q) = %v, want %v", c.name, c.home, c.repo, got, c.want)
		}
	}
}

func TestWriteSpawnErrNeedsCopyFlagsTheHomeRepo(t *testing.T) {
	real := realTemp(t)
	link := filepath.Join(realTemp(t), "home-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	writeSpawnErr(rec, "work", link, &box.NeedsCopyError{Repo: real, Inside: true})
	var b map[string]any
	if e := json.Unmarshal(rec.Body.Bytes(), &b); e != nil {
		t.Fatal(e)
	}
	if b["code"] != "needs_copy" || b["git_home"] != true {
		t.Fatalf("a symlinked home: %s", rec.Body)
	}
}
