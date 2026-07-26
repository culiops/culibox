package web

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// tarNames reads a gzipped tar body and returns its entry names, sorted.
func tarNames(t *testing.T, body []byte) []string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name)
	}
	sort.Strings(names)
	return names
}

func TestArchiveContainsFilesAndSkipsSymlinks(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "out")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644)
	os.WriteFile(filepath.Join(root, "sub", "b.txt"), []byte("beta"), 0o644)
	outside := filepath.Join(t.TempDir(), "victim.txt")
	os.WriteFile(outside, []byte("secret"), 0o644)
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Skip("symlink not supported")
	}

	s := &Server{Home: home, MaxArchiveBytes: 1 << 20, MaxArchiveFiles: 100}
	w := httptest.NewRecorder()
	s.handleFsArchive(w, httptest.NewRequest(http.MethodGet, "/api/fs/archive?path="+root, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", w.Code, w.Body)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "out.tar.gz") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	names := tarNames(t, w.Body.Bytes())
	joined := strings.Join(names, ",")
	for _, want := range []string{"out/a.txt", "out/sub/b.txt"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %v", want, names)
		}
	}
	// The symlink must be absent entirely — not followed, not recorded.
	if strings.Contains(joined, "link.txt") {
		t.Errorf("symlink was archived: %v", names)
	}
	if strings.Contains(joined, "secret") {
		t.Errorf("symlink target leaked into the archive: %v", names)
	}
}

// The cap must be enforced BEFORE the gzip stream starts: once bytes are on the
// wire the status is already 200, and aborting mid-stream hands back a truncated
// .tar.gz that looks like a successful download.
func TestArchiveRefusesOverCapBeforeStreaming(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "big")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "a.bin"), make([]byte, 500), 0o644)

	s := &Server{Home: home, MaxArchiveBytes: 100, MaxArchiveFiles: 100}
	w := httptest.NewRecorder()
	s.handleFsArchive(w, httptest.NewRequest(http.MethodGet, "/api/fs/archive?path="+root, nil))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code = %d, want 413", w.Code)
	}
	if strings.HasPrefix(w.Body.String(), "\x1f\x8b") {
		t.Fatal("gzip stream started before the cap was enforced")
	}
}

func TestArchiveRefusesTooManyFiles(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "many")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		os.WriteFile(filepath.Join(root, "f"+strconv.Itoa(i)), []byte("x"), 0o644)
	}
	s := &Server{Home: home, MaxArchiveBytes: 1 << 20, MaxArchiveFiles: 3}
	w := httptest.NewRecorder()
	s.handleFsArchive(w, httptest.NewRequest(http.MethodGet, "/api/fs/archive?path="+root, nil))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code = %d, want 413", w.Code)
	}
}

func TestArchiveRejectsNonDirAndEscape(t *testing.T) {
	home := t.TempDir()
	f := filepath.Join(home, "file.txt")
	os.WriteFile(f, []byte("x"), 0o644)
	outside := t.TempDir()
	s := &Server{Home: home, MaxArchiveBytes: 1 << 20, MaxArchiveFiles: 100}

	for _, c := range []struct {
		path string
		want int
	}{
		{f, http.StatusBadRequest},
		{outside, http.StatusForbidden},
		{filepath.Join(home, "nope"), http.StatusNotFound},
	} {
		w := httptest.NewRecorder()
		s.handleFsArchive(w, httptest.NewRequest(http.MethodGet, "/api/fs/archive?path="+c.path, nil))
		if w.Code != c.want {
			t.Errorf("%s: code = %d, want %d", c.path, w.Code, c.want)
		}
	}
}

// A symlinked DIRECTORY must not be descended into either — WalkDir does not
// follow symlinks, but this pins the behavior so a future rewrite cannot
// silently start following them.
func TestArchiveDoesNotDescendSymlinkedDir(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "out")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	secretDir := t.TempDir()
	os.WriteFile(filepath.Join(secretDir, "leak.txt"), []byte("secret"), 0o644)
	if err := os.Symlink(secretDir, filepath.Join(root, "elsewhere")); err != nil {
		t.Skip("symlink not supported")
	}
	os.WriteFile(filepath.Join(root, "own.txt"), []byte("mine"), 0o644)

	s := &Server{Home: home, MaxArchiveBytes: 1 << 20, MaxArchiveFiles: 100}
	w := httptest.NewRecorder()
	s.handleFsArchive(w, httptest.NewRequest(http.MethodGet, "/api/fs/archive?path="+root, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	joined := strings.Join(tarNames(t, w.Body.Bytes()), ",")
	if strings.Contains(joined, "leak.txt") || strings.Contains(joined, "elsewhere") {
		t.Fatalf("descended a symlinked directory: %s", joined)
	}
	if !strings.Contains(joined, "own.txt") {
		t.Fatalf("real file missing: %s", joined)
	}
}
