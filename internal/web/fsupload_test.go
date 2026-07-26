package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSafeName(t *testing.T) {
	ok := map[string]string{
		"report.md":            "report.md",
		".hidden":              ".hidden",
		"a/b/c.txt":            "c.txt",    // browsers may send a path
		`C:\Users\me\shot.png`: "shot.png", // Windows-style path from the browser
		"  spaced.txt  ":       "spaced.txt",
	}
	for in, want := range ok {
		got, err := safeName(in)
		if err != nil || got != want {
			t.Errorf("safeName(%q) = %q, %v; want %q, nil", in, got, err, want)
		}
	}
	bad := []string{
		"", ".", "..", "/", "../..",
		"\x01evil", "nul\x00byte",
		"submit\nnow.txt", "carriage\rreturn.txt", // would submit/corrupt the PTY line
		"c\u009bsim.txt", "next\u0085line.txt", // C1 controls: escape sequences once echoed
		"spoof\u202egnp.txt", // bidi override: renders as something it is not
		strings.Repeat("a", 251) + ".txt",
	}
	for _, b := range bad {
		if got, err := safeName(b); err == nil {
			t.Errorf("safeName(%q) = %q, nil; want error", b, got)
		}
	}
}

// mustRoot opens dir as an os.Root for the write-path tests.
func mustRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root
}

func TestCreateExclusiveNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shot.png"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := mustRoot(t, dir)
	f, name, err := createExclusive(root, ".", "shot.png")
	if err != nil {
		t.Fatalf("createExclusive: %v", err)
	}
	f.Close()
	if name != "shot-1.png" {
		t.Fatalf("name = %q, want shot-1.png", name)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "shot.png"))
	if string(got) != "original" {
		t.Fatalf("original file was modified: %q", got)
	}

	f2, name2, err := createExclusive(root, ".", "shot.png")
	if err != nil {
		t.Fatalf("createExclusive #2: %v", err)
	}
	f2.Close()
	if name2 != "shot-2.png" {
		t.Fatalf("name2 = %q, want shot-2.png", name2)
	}
}

// The escape test: a symlink sitting at the target name must never be written
// through. O_EXCL fails on an existing symlink, so the upload sidesteps it.
func TestCreateExclusiveDoesNotFollowSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(outside, []byte("do not touch"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "bait.txt")); err != nil {
		t.Fatal(err)
	}
	f, name, err := createExclusive(mustRoot(t, dir), ".", "bait.txt")
	if err != nil {
		t.Fatalf("createExclusive: %v", err)
	}
	if _, err := f.WriteString("payload"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if name != "bait-1.txt" {
		t.Fatalf("name = %q, want bait-1.txt", name)
	}
	got, _ := os.ReadFile(outside)
	if string(got) != "do not touch" {
		t.Fatalf("wrote through the symlink: %q", got)
	}
}

func TestCreateExclusiveGivesUpAfterManyCollisions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= maxNameAttempts; i++ {
		p := filepath.Join(dir, "x-"+strconv.Itoa(i)+".txt")
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := createExclusive(mustRoot(t, dir), ".", "x.txt"); !errors.Is(err, errTooManyCollisions) {
		t.Fatalf("err = %v, want errTooManyCollisions", err)
	}
}

// The race O_EXCL alone cannot win: the destination directory is resolved and
// validated, then swapped for a symlink pointing outside home before the write.
// O_EXCL only guards the FINAL component — the new filename does not exist, so
// a plain os.OpenFile would happily create it on the other side of the link.
// Writing through an os.Root re-resolves every component with O_NOFOLLOW
// semantics, so the swap cannot be exploited at all.
func TestCreateExclusiveRefusesSymlinkedParent(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	work := filepath.Join(home, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	root := mustRoot(t, home) // opened while work is a real directory

	// The swap an attacker with concurrent write access would perform.
	if err := os.Remove(work); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, work); err != nil {
		t.Skip("symlink not supported")
	}

	if _, _, err := createExclusive(root, "work", "pwned.txt"); err == nil {
		t.Fatal("wrote through a symlinked parent directory")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned.txt")); !os.IsNotExist(err) {
		t.Fatal("file escaped home through the symlinked parent")
	}
}

func TestContainedDir(t *testing.T) {
	home := t.TempDir()
	sub := filepath.Join(home, "work")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(home, "file.txt")
	if err := os.WriteFile(regular, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	gotDir, err := containedDir(home, sub)
	if err != nil {
		t.Fatalf("containedDir: %v", err)
	}
	if !strings.HasPrefix(gotDir, home) {
		t.Fatalf("got %q, want a path under %q", gotDir, home)
	}

	if _, err := containedDir(home, filepath.Dir(home)); !errors.Is(err, errOutsideHome) {
		t.Errorf("outside home: err = %v, want errOutsideHome", err)
	}
	if _, err := containedDir(home, filepath.Join(home, "nope")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing dir: err = %v, want os.ErrNotExist", err)
	}
	if _, err := containedDir(home, regular); !errors.Is(err, errNotDir) {
		t.Errorf("dir that is a regular file: err = %v, want errNotDir", err)
	}
	if _, err := containedDir(home, "relative/path"); !errors.Is(err, errBadPath) {
		t.Errorf("relative path: err = %v, want errBadPath", err)
	}
}

// uploadReq builds a multipart request with the given name→content parts.
func uploadReq(t *testing.T, dir string, parts [][2]string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, p := range parts {
		w, err := mw.CreateFormFile("file", p[0])
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, p[1])
	}
	mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/fs/upload?dir="+dir, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return r
}

type uploadResp struct {
	Uploaded []struct {
		Name        string `json:"name"`
		Path        string `json:"path"`
		Size        int64  `json:"size"`
		RenamedFrom string `json:"renamed_from"`
	} `json:"uploaded"`
	Errors []struct {
		Name  string `json:"name"`
		Error string `json:"error"`
	} `json:"errors"`
}

func TestUploadWritesFiles(t *testing.T) {
	home := t.TempDir()
	s := &Server{Home: home, MaxUploadBytes: 1 << 20}
	w := httptest.NewRecorder()
	s.handleFsUpload(w, uploadReq(t, home, [][2]string{{"a.txt", "alpha"}, {"b.txt", "beta"}}))
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	var got uploadResp
	json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Uploaded) != 2 || len(got.Errors) != 0 {
		t.Fatalf("uploaded=%d errors=%d, want 2/0", len(got.Uploaded), len(got.Errors))
	}
	b, err := os.ReadFile(filepath.Join(home, "a.txt"))
	if err != nil || string(b) != "alpha" {
		t.Fatalf("a.txt = %q, %v", b, err)
	}
}

func TestUploadRenamesOnCollision(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "a.txt"), []byte("original"), 0o644)
	s := &Server{Home: home, MaxUploadBytes: 1 << 20}
	w := httptest.NewRecorder()
	s.handleFsUpload(w, uploadReq(t, home, [][2]string{{"a.txt", "new"}}))
	var got uploadResp
	json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Uploaded) != 1 || got.Uploaded[0].Name != "a-1.txt" || got.Uploaded[0].RenamedFrom != "a.txt" {
		t.Fatalf("got %+v", got.Uploaded)
	}
	b, _ := os.ReadFile(filepath.Join(home, "a.txt"))
	if string(b) != "original" {
		t.Fatalf("original overwritten: %q", b)
	}
}

// A part over the cap fails alone: its partial file is removed and its siblings
// still land. The response is still 200 — the destination was fine.
func TestUploadOverCapFailsThatPartOnly(t *testing.T) {
	home := t.TempDir()
	s := &Server{Home: home, MaxUploadBytes: 4}
	w := httptest.NewRecorder()
	s.handleFsUpload(w, uploadReq(t, home, [][2]string{{"big.txt", "way too long"}, {"ok.txt", "ok"}}))
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	var got uploadResp
	json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Errors) != 1 || got.Errors[0].Name != "big.txt" {
		t.Fatalf("errors = %+v, want one for big.txt", got.Errors)
	}
	if len(got.Uploaded) != 1 || got.Uploaded[0].Name != "ok.txt" {
		t.Fatalf("uploaded = %+v, want ok.txt", got.Uploaded)
	}
	if _, err := os.Stat(filepath.Join(home, "big.txt")); !os.IsNotExist(err) {
		t.Fatal("partial file was left on disk")
	}
}

func TestUploadBadFilenameIsAPartError(t *testing.T) {
	home := t.TempDir()
	s := &Server{Home: home, MaxUploadBytes: 1 << 20}
	w := httptest.NewRecorder()
	s.handleFsUpload(w, uploadReq(t, home, [][2]string{{"..", "x"}, {"fine.txt", "y"}}))
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	var got uploadResp
	json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Errors) != 1 || len(got.Uploaded) != 1 {
		t.Fatalf("got %+v / %+v", got.Errors, got.Uploaded)
	}
}

func TestUploadDestinationErrors(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	file := filepath.Join(home, "f.txt")
	os.WriteFile(file, nil, 0o644)
	s := &Server{Home: home, MaxUploadBytes: 1 << 20}

	cases := []struct {
		dir  string
		want int
	}{
		{outside, http.StatusForbidden},
		{filepath.Join(home, "missing"), http.StatusNotFound},
		{file, http.StatusBadRequest},
		{"relative/path", http.StatusBadRequest},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		s.handleFsUpload(w, uploadReq(t, c.dir, [][2]string{{"a.txt", "x"}}))
		if w.Code != c.want {
			t.Errorf("dir %q: code = %d, want %d", c.dir, w.Code, c.want)
		}
	}
}

func TestUploadRejectsGet(t *testing.T) {
	home := t.TempDir()
	s := &Server{Home: home, MaxUploadBytes: 1 << 20}
	w := httptest.NewRecorder()
	s.handleFsUpload(w, httptest.NewRequest(http.MethodGet, "/api/fs/upload?dir="+home, nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d, want 405", w.Code)
	}
}

// renamed_from must mean "a collision forced a new name" and nothing else. A
// Windows-style path or a name that merely had whitespace trimmed is sanitized,
// not renamed — reporting those as renames makes the collision toast a lie.
func TestUploadDoesNotReportSanitationAsRename(t *testing.T) {
	home := t.TempDir()
	s := &Server{Home: home, MaxUploadBytes: 1 << 20}
	w := httptest.NewRecorder()
	s.handleFsUpload(w, uploadReq(t, home, [][2]string{{`C:\Users\me\shot.png`, "x"}, {"  spaced.txt  ", "y"}}))
	var got uploadResp
	json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Uploaded) != 2 {
		t.Fatalf("uploaded = %+v", got.Uploaded)
	}
	for _, u := range got.Uploaded {
		if u.RenamedFrom != "" {
			t.Errorf("%s reported renamed_from=%q with no collision", u.Name, u.RenamedFrom)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "shot.png")); err != nil {
		t.Errorf("windows path not stripped to a base name: %v", err)
	}
}

// Per-file caps alone leave the file COUNT unbounded, which is a disk-fill in
// slow motion. The part cap stops the loop and says so.
func TestUploadRefusesTooManyParts(t *testing.T) {
	home := t.TempDir()
	s := &Server{Home: home, MaxUploadBytes: 1 << 20}
	parts := make([][2]string, maxUploadParts+5)
	for i := range parts {
		parts[i] = [2]string{"f" + strconv.Itoa(i) + ".txt", "x"}
	}
	w := httptest.NewRecorder()
	s.handleFsUpload(w, uploadReq(t, home, parts))
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	var got uploadResp
	json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Uploaded) != maxUploadParts {
		t.Fatalf("uploaded %d files, want the cap of %d", len(got.Uploaded), maxUploadParts)
	}
	if len(got.Errors) != 1 || !strings.Contains(got.Errors[0].Error, "more than") {
		t.Fatalf("errors = %+v, want one part-cap error", got.Errors)
	}
}
