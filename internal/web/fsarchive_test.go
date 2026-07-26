package web

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
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
	// measureTree short-circuits at the cap, so any "measured" size it could
	// report is meaningless — the message must not claim one.
	if strings.Contains(w.Body.String(), "folder is 500") {
		t.Fatalf("body reports a short-circuited running total: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "over the") {
		t.Fatalf("body = %s, want the limit named", w.Body.String())
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
	// Assert the MESSAGE, not just the status: a file COUNT rendered through a
	// byte formatter reads "more than 19.5 KB files", and a status-only test
	// passes with that bug in place.
	if !strings.Contains(w.Body.String(), "more than 3 files") {
		t.Fatalf("body = %s, want a plain file count", w.Body.String())
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

// tarEntriesStrict reads the whole archive and reports the first structural
// error. tarNames stops at any error, so it cannot see a truncated member —
// which is exactly the failure this guards.
func tarEntriesStrict(t *testing.T, body []byte) ([]string, error) {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return names, nil
		}
		if err != nil {
			return names, err
		}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			return names, err
		}
		names = append(names, h.Name)
	}
}

// A file that cannot be opened must be skipped ENTIRELY. Writing its header
// first and discovering the failure afterwards leaves the tar writer expecting
// a body that never arrives: the walk aborts, gzip flushes, and the client gets
// 200 OK with an archive whose last member is unextractable.
func TestArchiveSkipsUnreadableFileWithoutCorruptingTheStream(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "out")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644)
	locked := filepath.Join(root, "locked.bin")
	os.WriteFile(locked, bytes.Repeat([]byte("x"), 2000), 0o000)
	if f, err := os.Open(locked); err == nil {
		f.Close()
		t.Skip("running with rights that ignore file mode (root?)")
	}
	os.WriteFile(filepath.Join(root, "z.txt"), []byte("zulu"), 0o644)

	s := &Server{Home: home, MaxArchiveBytes: 1 << 20, MaxArchiveFiles: 100}
	w := httptest.NewRecorder()
	s.handleFsArchive(w, httptest.NewRequest(http.MethodGet, "/api/fs/archive?path="+root, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	names, err := tarEntriesStrict(t, w.Body.Bytes())
	if err != nil {
		t.Fatalf("archive is structurally broken: %v (entries: %v)", err, names)
	}
	joined := strings.Join(names, ",")
	if strings.Contains(joined, "locked.bin") {
		t.Errorf("unreadable file got an entry: %v", names)
	}
	// The readable files on both sides of it must survive.
	for _, want := range []string{"out/a.txt", "out/z.txt"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %v", want, names)
		}
	}
}

// probe=1 answers with the measurement instead of the archive, so the UI can
// report an over-cap directory. A plain <a download> navigation swallows the
// error body, leaving the user with a failed download and no reason.
func TestArchiveProbeReportsSizeAndRefusal(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "out")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "a.txt"), bytes.Repeat([]byte("x"), 500), 0o644)

	ok := &Server{Home: home, MaxArchiveBytes: 1 << 20, MaxArchiveFiles: 100}
	w := httptest.NewRecorder()
	ok.handleFsArchive(w, httptest.NewRequest(http.MethodGet, "/api/fs/archive?probe=1&path="+root, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("probe code = %d, want 200", w.Code)
	}
	var got struct {
		Bytes int64 `json:"bytes"`
		Files int   `json:"files"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("probe body: %v (%s)", err, w.Body)
	}
	if got.Bytes != 500 || got.Files != 1 {
		t.Fatalf("probe = %+v, want 500 bytes / 1 file", got)
	}
	if strings.HasPrefix(w.Body.String(), "\x1f\x8b") {
		t.Fatal("probe streamed an archive")
	}

	small := &Server{Home: home, MaxArchiveBytes: 100, MaxArchiveFiles: 100}
	w = httptest.NewRecorder()
	small.handleFsArchive(w, httptest.NewRequest(http.MethodGet, "/api/fs/archive?probe=1&path="+root, nil))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-cap probe code = %d, want 413", w.Code)
	}
}
