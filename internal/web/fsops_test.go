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
		Entries int    `json:"entries"`
		Files   int    `json:"files"`
		Bytes   int64  `json:"bytes"`
		Capped  string `json:"capped"`
	}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Entries != 2 || got.Files != 2 || got.Bytes != 8 {
		t.Fatalf("counts = %d entries / %d files / %d bytes, want 2 / 2 / 8", got.Entries, got.Files, got.Bytes)
	}
	if got.Capped != "" {
		t.Errorf("capped = %q, want empty", got.Capped)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("directory was deleted without recursive")
	}

	rec = httptest.NewRecorder()
	b, _ = json.Marshal(map[string]any{"path": dir, "recursive": true, "entries": 2})
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
	b, _ := json.Marshal(map[string]any{"path": dir, "recursive": true, "entries": 1}) // saw 1, now 5
	s.handleFsDelete(rec, opReq(t, "/api/fs/delete", string(b)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d (%s)", rec.Code, rec.Body)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("directory deleted despite growing past the confirmed count")
	}
}

// The regression that made the confirmation dialog useless: measureTree counts
// regular files only, so a tree of directories and symlinks reported "0 files,
// 0 bytes" — a message byte-identical to an empty folder's, for something
// RemoveAll would wipe out.
func TestDelete_DirectoryOfOnlySubdirsAndSymlinksIsNotSilent(t *testing.T) {
	s, home := opSrv(t)
	trap := filepath.Join(home, "trap")
	os.MkdirAll(filepath.Join(trap, "sub1", "deeper"), 0o755)
	os.Mkdir(filepath.Join(trap, "sub2"), 0o755)
	os.Symlink("/etc/hosts", filepath.Join(trap, "link1"))
	os.Symlink("/etc/hosts", filepath.Join(trap, "link2"))

	rec := httptest.NewRecorder()
	b, _ := json.Marshal(map[string]any{"path": trap})
	s.handleFsDelete(rec, opReq(t, "/api/fs/delete", string(b)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d (%s)", rec.Code, rec.Body)
	}
	var got struct {
		Entries int `json:"entries"`
		Files   int `json:"files"`
	}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Entries != 5 { // sub1, sub1/deeper, sub2, link1, link2
		t.Fatalf("entries = %d, want 5 — a non-empty tree must never report as empty", got.Entries)
	}
	if got.Files != 0 {
		t.Errorf("files = %d, want 0 (none of them is a regular file)", got.Files)
	}
	if _, err := os.Stat(trap); err != nil {
		t.Fatal("directory deleted without recursive")
	}
}

// The measurement must describe the tree that will actually be removed, which
// means walking inside the root rather than the caller's path.
func TestDelete_MeasuresInsideTheRootNotTheRequestPath(t *testing.T) {
	_, home := opSrv(t)
	outside := t.TempDir()
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		os.WriteFile(filepath.Join(outside, n), []byte("0123456789"), 0o644)
	}
	real := filepath.Join(home, "dir")
	os.Mkdir(real, 0o755)
	os.WriteFile(filepath.Join(real, "only"), []byte("x"), 0o644)

	root, rhome, err := openHomeRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	rel, _ := filepath.Rel(rhome, real)
	m := measureForDelete(root, rel)
	if m.Entries != 1 || m.Files != 1 {
		t.Fatalf("in-root measurement = %+v, want 1 entry / 1 file", m)
	}
	_ = outside
}

func TestDelete_ProtectedDirReportsItsContentsToo(t *testing.T) {
	s, home := opSrv(t)
	git := filepath.Join(home, ".git")
	os.Mkdir(git, 0o755)
	os.WriteFile(filepath.Join(git, "HEAD"), []byte("ref"), 0o644)
	os.WriteFile(filepath.Join(git, "config"), []byte("[core]"), 0o644)

	rec := httptest.NewRecorder()
	b, _ := json.Marshal(map[string]any{"path": git})
	s.handleFsDelete(rec, opReq(t, "/api/fs/delete", string(b)))
	var got struct {
		ConfirmName string `json:"confirm_name"`
		Entries     int    `json:"entries"`
	}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got.ConfirmName != ".git" || got.Entries != 2 {
		t.Fatalf("protected refusal = %+v, want .git with 2 entries — the most protected folder must not be the least informative", got)
	}
}

func TestDelete_HomeItselfIsRefused(t *testing.T) {
	s, home := opSrv(t)
	for _, p := range []string{home, home + "/", home + "/."} {
		rec := httptest.NewRecorder()
		b, _ := json.Marshal(map[string]any{"path": p, "recursive": true})
		s.handleFsDelete(rec, opReq(t, "/api/fs/delete", string(b)))
		if rec.Code == http.StatusNoContent {
			t.Fatalf("%q: home itself was accepted for deletion", p)
		}
		if _, err := os.Stat(home); err != nil {
			t.Fatalf("%q: home is gone", p)
		}
	}
}

func TestRename_ProtectedDirNeedsTypedName(t *testing.T) {
	s, home := opSrv(t)
	ssh := filepath.Join(home, ".ssh")
	os.Mkdir(ssh, 0o755)
	os.WriteFile(filepath.Join(ssh, "id_key"), []byte("secret"), 0o600)

	rec := httptest.NewRecorder()
	b, _ := json.Marshal(map[string]string{"path": ssh, "name": "zz"})
	s.handleFsRename(rec, opReq(t, "/api/fs/rename", string(b)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d — rename must not be a way around the delete gate", rec.Code)
	}
	if _, err := os.Stat(ssh); err != nil {
		t.Fatal(".ssh was renamed without the typed confirmation")
	}
	rec = httptest.NewRecorder()
	b, _ = json.Marshal(map[string]string{"path": ssh, "name": "zz", "confirm_name": ".ssh"})
	s.handleFsRename(rec, opReq(t, "/api/fs/rename", string(b)))
	if rec.Code != http.StatusOK {
		t.Fatalf("typed name: want 200, got %d (%s)", rec.Code, rec.Body)
	}
}

// The four symlink shapes, for rename as well as delete: the old design turned
// the outside and dangling ones into an unrenameable 403/404.
func TestRename_EverySymlinkShapeRenamesTheLink(t *testing.T) {
	s, home := opSrv(t)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(home, "real.txt"), []byte("keep"), 0o644)
	os.Mkdir(filepath.Join(home, "realdir"), 0o755)
	os.Symlink(filepath.Join(home, "real.txt"), filepath.Join(home, "to-file"))
	os.Symlink(filepath.Join(home, "realdir"), filepath.Join(home, "to-dir"))
	os.Symlink(outside, filepath.Join(home, "to-outside"))
	os.Symlink(filepath.Join(home, "gone"), filepath.Join(home, "dangling"))

	for _, link := range []string{"to-file", "to-dir", "to-outside", "dangling"} {
		rec := httptest.NewRecorder()
		b, _ := json.Marshal(map[string]string{"path": filepath.Join(home, link), "name": link + "-x"})
		s.handleFsRename(rec, opReq(t, "/api/fs/rename", string(b)))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: want 200, got %d (%s)", link, rec.Code, rec.Body)
		}
		if _, err := os.Lstat(filepath.Join(home, link+"-x")); err != nil {
			t.Errorf("%s: link not renamed", link)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "real.txt")); err != nil {
		t.Error("file target renamed")
	}
	if _, err := os.Stat(filepath.Join(home, "realdir")); err != nil {
		t.Error("directory target renamed")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("target outside home renamed")
	}
}

// Every handler must refuse a parent swapped for an escaping symlink after the
// root was opened — the containment property, asserted through the HANDLER.
func TestOps_RefuseAParentSymlinkedOutOfHomeAfterResolution(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(s *Server, rec *httptest.ResponseRecorder, r *http.Request)
		body func(target string) string
		path string
	}{
		{"mkdir", func(s *Server, rec *httptest.ResponseRecorder, r *http.Request) { s.handleFsMkdir(rec, r) },
			func(target string) string { return `{"dir":"` + target + `","name":"escaped"}` }, "/api/fs/mkdir"},
		{"rename", func(s *Server, rec *httptest.ResponseRecorder, r *http.Request) { s.handleFsRename(rec, r) },
			func(target string) string { return `{"path":"` + target + `/victim","name":"escaped"}` }, "/api/fs/rename"},
		{"delete", func(s *Server, rec *httptest.ResponseRecorder, r *http.Request) { s.handleFsDelete(rec, r) },
			func(target string) string { return `{"path":"` + target + `/victim","recursive":true}` }, "/api/fs/delete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, home := opSrv(t)
			outside := t.TempDir()
			os.WriteFile(filepath.Join(outside, "victim"), []byte("keep"), 0o644)
			workdir := filepath.Join(home, "workdir")
			os.Mkdir(workdir, 0o755)
			os.Remove(workdir)
			os.Symlink(outside, workdir) // the parent now points outside $HOME

			rec := httptest.NewRecorder()
			tc.call(s, rec, opReq(t, tc.path, tc.body(workdir)))
			if rec.Code == http.StatusOK || rec.Code == http.StatusNoContent {
				t.Fatalf("%s reached outside home: %d (%s)", tc.name, rec.Code, rec.Body)
			}
			if _, err := os.Stat(filepath.Join(outside, "victim")); err != nil {
				t.Fatalf("%s touched a file outside home", tc.name)
			}
			if _, err := os.Stat(filepath.Join(outside, "escaped")); err == nil {
				t.Fatalf("%s created something outside home", tc.name)
			}
		})
	}
}

func TestOps_GuardsApplyToEveryHandler(t *testing.T) {
	s, home := opSrv(t)
	big := strings.Repeat("x", maxOpBody+1024)
	for _, tc := range []struct {
		name string
		call func(rec *httptest.ResponseRecorder, r *http.Request)
	}{
		{"mkdir", func(rec *httptest.ResponseRecorder, r *http.Request) { s.handleFsMkdir(rec, r) }},
		{"rename", func(rec *httptest.ResponseRecorder, r *http.Request) { s.handleFsRename(rec, r) }},
		{"delete", func(rec *httptest.ResponseRecorder, r *http.Request) { s.handleFsDelete(rec, r) }},
	} {
		rec := httptest.NewRecorder()
		tc.call(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s GET: want 405, got %d", tc.name, rec.Code)
		}
		rec = httptest.NewRecorder()
		cross := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"path":"`+home+`/a","dir":"`+home+`","name":"a"}`))
		cross.Header.Set("Sec-Fetch-Site", "cross-site")
		tc.call(rec, cross)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s cross-site: want 403, got %d", tc.name, rec.Code)
		}
		rec = httptest.NewRecorder()
		tc.call(rec, opReq(t, "/x", `{"name":"`+big+`"}`))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s oversized body: want 400, got %d", tc.name, rec.Code)
		}
	}
}

func TestOps_ParentThatIsAFileIsABadRequestNotAServerError(t *testing.T) {
	s, home := opSrv(t)
	os.WriteFile(filepath.Join(home, "afile"), []byte("x"), 0o644)
	for _, tc := range []struct {
		name string
		call func(rec *httptest.ResponseRecorder, r *http.Request)
		body string
	}{
		{"mkdir", func(rec *httptest.ResponseRecorder, r *http.Request) { s.handleFsMkdir(rec, r) },
			`{"dir":"` + home + `/afile","name":"x"}`},
		{"rename", func(rec *httptest.ResponseRecorder, r *http.Request) { s.handleFsRename(rec, r) },
			`{"path":"` + home + `/afile/x","name":"y"}`},
		{"delete", func(rec *httptest.ResponseRecorder, r *http.Request) { s.handleFsDelete(rec, r) },
			`{"path":"` + home + `/afile/x"}`},
	} {
		rec := httptest.NewRecorder()
		tc.call(rec, opReq(t, "/x", tc.body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d (%s)", tc.name, rec.Code, rec.Body)
		}
	}
}
