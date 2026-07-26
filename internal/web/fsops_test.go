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
