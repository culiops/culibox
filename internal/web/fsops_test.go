package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// opReq builds a same-origin JSON POST the mutation handlers accept.
func opReq(t *testing.T, path, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	return r
}

func opSrv(t *testing.T) (*Server, string) {
	t.Helper()
	home := t.TempDir()
	// t.TempDir under macOS/Linux can sit behind a symlink (/tmp -> /private/tmp);
	// resolve it so the test's own path comparisons match the handler's.
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{Home: resolved}, resolved
}

func TestMkdir_CreatesDirectory(t *testing.T) {
	s, home := opSrv(t)
	rec := httptest.NewRecorder()
	b, _ := json.Marshal(map[string]string{"dir": home, "name": "notes"})
	s.handleFsMkdir(rec, opReq(t, "/api/fs/mkdir", string(b)))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body)
	}
	info, err := os.Stat(filepath.Join(home, "notes"))
	if err != nil || !info.IsDir() {
		t.Fatalf("directory not created: %v", err)
	}
	var got struct {
		Path string `json:"path"`
	}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Path != filepath.Join(home, "notes") {
		t.Errorf("path = %q, want %q", got.Path, filepath.Join(home, "notes"))
	}
}

func TestMkdir_RejectsBadNames(t *testing.T) {
	s, home := opSrv(t)
	for _, name := range []string{"../evil", "sub/evil", "", ".", ".."} {
		rec := httptest.NewRecorder()
		b, _ := json.Marshal(map[string]string{"dir": home, "name": name})
		s.handleFsMkdir(rec, opReq(t, "/api/fs/mkdir", string(b)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("name %q: want 400, got %d", name, rec.Code)
		}
	}
}

func TestMkdir_ExistingNameConflicts(t *testing.T) {
	s, home := opSrv(t)
	os.Mkdir(filepath.Join(home, "notes"), 0o755)
	rec := httptest.NewRecorder()
	b, _ := json.Marshal(map[string]string{"dir": home, "name": "notes"})
	s.handleFsMkdir(rec, opReq(t, "/api/fs/mkdir", string(b)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rec.Code)
	}
}

func TestMkdir_OutsideHomeForbidden(t *testing.T) {
	s, _ := opSrv(t)
	rec := httptest.NewRecorder()
	s.handleFsMkdir(rec, opReq(t, "/api/fs/mkdir", `{"dir":"/tmp","name":"x"}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d (%s)", rec.Code, rec.Body)
	}
}

func TestMkdir_MethodAndOriginGuards(t *testing.T) {
	s, home := opSrv(t)
	rec := httptest.NewRecorder()
	s.handleFsMkdir(rec, httptest.NewRequest(http.MethodGet, "/api/fs/mkdir", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: want 405, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	b, _ := json.Marshal(map[string]string{"dir": home, "name": "x"})
	cross := httptest.NewRequest(http.MethodPost, "/api/fs/mkdir", strings.NewReader(string(b)))
	cross.Header.Set("Sec-Fetch-Site", "cross-site")
	s.handleFsMkdir(rec, cross)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site: want 403, got %d", rec.Code)
	}
}

// The parent is swapped for a symlink AFTER the Root is opened. Only the
// post-open swap tests containment; a pre-open one is caught by containedDir.
func TestMkdir_RefusesSymlinkedParentSwappedAfterOpen(t *testing.T) {
	_, home := opSrv(t)
	outside := t.TempDir()
	real := filepath.Join(home, "workdir")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	root, rhome, err := openHomeRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	rel, _ := filepath.Rel(rhome, real)
	os.Remove(real)
	if err := os.Symlink(outside, real); err != nil { // workdir now points outside $HOME
		t.Fatal(err)
	}
	if err := root.Mkdir(filepath.Join(rel, "escaped"), 0o755); err == nil {
		t.Fatal("os.Root allowed a mkdir through a symlink pointing outside home")
	}
	if _, err := os.Stat(filepath.Join(outside, "escaped")); err == nil {
		t.Fatal("directory was created outside home")
	}
}

func TestRename_FileAndDirectory(t *testing.T) {
	s, home := opSrv(t)
	os.WriteFile(filepath.Join(home, "a.txt"), []byte("x"), 0o644)
	os.Mkdir(filepath.Join(home, "olddir"), 0o755)
	for _, tc := range []struct{ from, to string }{{"a.txt", "b.txt"}, {"olddir", "newdir"}} {
		rec := httptest.NewRecorder()
		b, _ := json.Marshal(map[string]string{"path": filepath.Join(home, tc.from), "name": tc.to})
		s.handleFsRename(rec, opReq(t, "/api/fs/rename", string(b)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d (%s)", tc.from, rec.Code, rec.Body)
		}
		if _, err := os.Lstat(filepath.Join(home, tc.to)); err != nil {
			t.Errorf("%s not renamed: %v", tc.from, err)
		}
	}
}

func TestRename_NeverOverwrites(t *testing.T) {
	s, home := opSrv(t)
	os.WriteFile(filepath.Join(home, "a.txt"), []byte("keep"), 0o644)
	os.WriteFile(filepath.Join(home, "b.txt"), []byte("victim"), 0o644)
	rec := httptest.NewRecorder()
	b, _ := json.Marshal(map[string]string{"path": filepath.Join(home, "a.txt"), "name": "b.txt"})
	s.handleFsRename(rec, opReq(t, "/api/fs/rename", string(b)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rec.Code)
	}
	got, _ := os.ReadFile(filepath.Join(home, "b.txt"))
	if string(got) != "victim" {
		t.Fatal("destination was overwritten")
	}
}

func TestRename_SameNameIsANoOp(t *testing.T) {
	s, home := opSrv(t)
	os.WriteFile(filepath.Join(home, "a.txt"), []byte("x"), 0o644)
	rec := httptest.NewRecorder()
	b, _ := json.Marshal(map[string]string{"path": filepath.Join(home, "a.txt"), "name": "a.txt"})
	s.handleFsRename(rec, opReq(t, "/api/fs/rename", string(b)))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(home, "a.txt")); err != nil {
		t.Fatal("file disappeared")
	}
}

// containedPath would resolve the link and rename its TARGET; resolveParent must not.
func TestRename_SymlinkRenamesTheLinkNotItsTarget(t *testing.T) {
	s, home := opSrv(t)
	os.WriteFile(filepath.Join(home, "real.txt"), []byte("target"), 0o644)
	os.Symlink(filepath.Join(home, "real.txt"), filepath.Join(home, "lnk.txt"))
	rec := httptest.NewRecorder()
	b, _ := json.Marshal(map[string]string{"path": filepath.Join(home, "lnk.txt"), "name": "moved.txt"})
	s.handleFsRename(rec, opReq(t, "/api/fs/rename", string(b)))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body)
	}
	if _, err := os.Lstat(filepath.Join(home, "real.txt")); err != nil {
		t.Fatal("the symlink's target was renamed instead of the link")
	}
	if _, err := os.Lstat(filepath.Join(home, "moved.txt")); err != nil {
		t.Fatal("the link was not renamed")
	}
}

func TestRename_RejectsPathInName(t *testing.T) {
	s, home := opSrv(t)
	os.WriteFile(filepath.Join(home, "a.txt"), []byte("x"), 0o644)
	os.Mkdir(filepath.Join(home, "sub"), 0o755)
	for _, name := range []string{"../evil", "sub/evil"} {
		rec := httptest.NewRecorder()
		b, _ := json.Marshal(map[string]string{"path": filepath.Join(home, "a.txt"), "name": name})
		s.handleFsRename(rec, opReq(t, "/api/fs/rename", string(b)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("name %q: want 400, got %d", name, rec.Code)
		}
	}
}

func TestDelete_FileAndEmptyDir(t *testing.T) {
	s, home := opSrv(t)
	os.WriteFile(filepath.Join(home, "a.txt"), []byte("x"), 0o644)
	os.Mkdir(filepath.Join(home, "empty"), 0o755)
	for _, n := range []string{"a.txt", "empty"} {
		rec := httptest.NewRecorder()
		b, _ := json.Marshal(map[string]any{"path": filepath.Join(home, n)})
		s.handleFsDelete(rec, opReq(t, "/api/fs/delete", string(b)))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s: want 204, got %d (%s)", n, rec.Code, rec.Body)
		}
		if _, err := os.Lstat(filepath.Join(home, n)); !os.IsNotExist(err) {
			t.Errorf("%s still exists", n)
		}
	}
}

func TestDelete_NonEmptyDirNeedsRecursiveAndReportsCounts(t *testing.T) {
	s, home := opSrv(t)
	dir := filepath.Join(home, "full")
	os.Mkdir(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "one"), []byte("12345"), 0o644)
	os.WriteFile(filepath.Join(dir, "two"), []byte("123"), 0o644)

	rec := httptest.NewRecorder()
	b, _ := json.Marshal(map[string]any{"path": dir})
	s.handleFsDelete(rec, opReq(t, "/api/fs/delete", string(b)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d (%s)", rec.Code, rec.Body)
	}
	var got struct {
		Files int   `json:"files"`
		Bytes int64 `json:"bytes"`
	}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Files != 2 || got.Bytes != 8 {
		t.Fatalf("counts = %d files / %d bytes, want 2 / 8", got.Files, got.Bytes)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("directory was deleted without recursive")
	}

	rec = httptest.NewRecorder()
	b, _ = json.Marshal(map[string]any{"path": dir, "recursive": true, "files": 2})
	s.handleFsDelete(rec, opReq(t, "/api/fs/delete", string(b)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("recursive: want 204, got %d (%s)", rec.Code, rec.Body)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("directory survived a recursive delete")
	}
}

// The defect the spec review caught, four ways: each link must go, each target must survive.
func TestDelete_SymlinkRemovesLinkNotTarget(t *testing.T) {
	s, home := opSrv(t)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(home, "real.txt"), []byte("keep"), 0o644)
	os.Mkdir(filepath.Join(home, "realdir"), 0o755)
	os.WriteFile(filepath.Join(home, "realdir", "inside"), []byte("keep"), 0o644)
	os.Symlink(filepath.Join(home, "real.txt"), filepath.Join(home, "to-file"))
	os.Symlink(filepath.Join(home, "realdir"), filepath.Join(home, "to-dir"))
	os.Symlink(outside, filepath.Join(home, "to-outside"))
	os.Symlink(filepath.Join(home, "gone"), filepath.Join(home, "dangling"))

	for _, link := range []string{"to-file", "to-dir", "to-outside", "dangling"} {
		rec := httptest.NewRecorder()
		b, _ := json.Marshal(map[string]any{"path": filepath.Join(home, link)})
		s.handleFsDelete(rec, opReq(t, "/api/fs/delete", string(b)))
		if rec.Code != http.StatusNoContent {
			t.Errorf("%s: want 204, got %d (%s)", link, rec.Code, rec.Body)
		}
		if _, err := os.Lstat(filepath.Join(home, link)); !os.IsNotExist(err) {
			t.Errorf("%s: link still there", link)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "real.txt")); err != nil {
		t.Error("file target destroyed")
	}
	if _, err := os.Stat(filepath.Join(home, "realdir", "inside")); err != nil {
		t.Error("directory target destroyed")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("target outside home destroyed")
	}
}

func TestDelete_ProtectedDirNeedsTypedName(t *testing.T) {
	s, home := opSrv(t)
	git := filepath.Join(home, ".git")
	os.Mkdir(git, 0o755)
	os.WriteFile(filepath.Join(git, "HEAD"), []byte("ref"), 0o644)

	rec := httptest.NewRecorder()
	b, _ := json.Marshal(map[string]any{"path": git, "recursive": true})
	s.handleFsDelete(rec, opReq(t, "/api/fs/delete", string(b)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rec.Code)
	}
	var got struct {
		ConfirmName string `json:"confirm_name"`
	}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got.ConfirmName != ".git" {
		t.Fatalf("confirm_name = %q", got.ConfirmName)
	}

	rec = httptest.NewRecorder()
	b, _ = json.Marshal(map[string]any{"path": git, "recursive": true, "confirm_name": "wrong"})
	s.handleFsDelete(rec, opReq(t, "/api/fs/delete", string(b)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("wrong name: want 409, got %d", rec.Code)
	}
	if _, err := os.Stat(git); err != nil {
		t.Fatal(".git deleted without the typed confirmation")
	}

	rec = httptest.NewRecorder()
	b, _ = json.Marshal(map[string]any{"path": git, "recursive": true, "confirm_name": ".git"})
	s.handleFsDelete(rec, opReq(t, "/api/fs/delete", string(b)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("typed name: want 204, got %d (%s)", rec.Code, rec.Body)
	}
}

func TestDelete_OutsideHomeAndMissing(t *testing.T) {
	s, home := opSrv(t)
	rec := httptest.NewRecorder()
	s.handleFsDelete(rec, opReq(t, "/api/fs/delete", `{"path":"/etc/hosts"}`))
	if rec.Code != http.StatusForbidden {
		t.Errorf("outside home: want 403, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	b, _ := json.Marshal(map[string]any{"path": filepath.Join(home, "nope")})
	s.handleFsDelete(rec, opReq(t, "/api/fs/delete", string(b)))
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing: want 404, got %d", rec.Code)
	}
}

func TestDelete_RefusesWhenTheTreeGrewSinceConfirmation(t *testing.T) {
	s, home := opSrv(t)
	dir := filepath.Join(home, "growing")
	os.Mkdir(dir, 0o755)
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644)
	}
	rec := httptest.NewRecorder()
	b, _ := json.Marshal(map[string]any{"path": dir, "recursive": true, "files": 1}) // saw 1, now 5
	s.handleFsDelete(rec, opReq(t, "/api/fs/delete", string(b)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d (%s)", rec.Code, rec.Body)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("directory deleted despite growing past the confirmed count")
	}
}
