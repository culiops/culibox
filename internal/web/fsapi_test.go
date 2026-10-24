package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestIsBinary(t *testing.T) {
	if isBinary([]byte("plain text\nline")) {
		t.Fatal("text misclassified as binary")
	}
	if !isBinary([]byte{0x89, 0x50, 0x00, 0x01}) {
		t.Fatal("NUL-containing bytes should be binary")
	}
	if isBinary([]byte("")) {
		t.Fatal("empty should not be binary")
	}
}

func TestImageExt(t *testing.T) {
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg"} {
		if imageExt[ext] == "" {
			t.Fatalf("missing MIME for %s", ext)
		}
	}
}

func TestHandleFsList(t *testing.T) {
	home := t.TempDir()
	mustMkdir(t, filepath.Join(home, "zdir"))
	mustMkdir(t, filepath.Join(home, "adir"))
	if err := os.WriteFile(filepath.Join(home, "afile"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{Home: home}

	rr := httptest.NewRecorder()
	s.handleFsList(rr, httptest.NewRequest("GET", "/api/fs/list?path="+home, nil))
	if rr.Code != 200 {
		t.Fatalf("want 200, got %d (%s)", rr.Code, rr.Body)
	}
	var got struct {
		Entries []fsEntry `json:"entries"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 3 || !got.Entries[0].IsDir || got.Entries[0].Name != "adir" ||
		got.Entries[2].IsDir || got.Entries[2].Name != "afile" {
		t.Fatalf("bad ordering: %+v", got.Entries)
	}

	rr = httptest.NewRecorder()
	s.handleFsList(rr, httptest.NewRequest("GET", "/api/fs/list?path="+filepath.Join(home, "afile"), nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("file target: want 400, got %d", rr.Code)
	}
	other := t.TempDir()
	rr = httptest.NewRecorder()
	s.handleFsList(rr, httptest.NewRequest("GET", "/api/fs/list?path="+other, nil))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("outside home: want 403, got %d", rr.Code)
	}
}

func TestHandleFsRead(t *testing.T) {
	home := t.TempDir()
	txt := filepath.Join(home, "a.go")
	if err := os.WriteFile(txt, []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, "b.bin")
	if err := os.WriteFile(bin, []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(home, "big.txt")
	// non-NUL text (all-zero bytes would classify as binary before the size check)
	if err := os.WriteFile(big, bytes.Repeat([]byte("a"), (2<<20)+10), 0o644); err != nil {
		t.Fatal(err)
	}
	png := filepath.Join(home, "img.png")
	if err := os.WriteFile(png, []byte("\x89PNG\r\n\x1a\nrest"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{Home: home}
	read := func(p string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		s.handleFsRead(rr, httptest.NewRequest("GET", "/api/fs/read?path="+p, nil))
		return rr
	}

	rr := read(txt)
	var fr fsRead
	if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &fr) != nil ||
		fr.Kind != "text" || fr.Lang != "go" || fr.Content != "package main" {
		t.Fatalf("text read bad: code=%d body=%s", rr.Code, rr.Body)
	}
	rr = read(bin)
	fr = fsRead{}
	json.Unmarshal(rr.Body.Bytes(), &fr)
	if rr.Code != 200 || fr.Kind != "binary" || fr.Content != "" {
		t.Fatalf("binary read bad: code=%d body=%s", rr.Code, rr.Body)
	}
	if rr := read(big); rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("big file: want 413, got %d", rr.Code)
	}
	rr = read(png)
	if rr.Code != 200 || rr.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("png: want 200 image/png, got %d %q", rr.Code, rr.Header().Get("Content-Type"))
	}
	// ensure io import is used (guards against accidental removal)
	_ = io.Discard
}

func TestReadDownloadServesBinaryAndLargeFiles(t *testing.T) {
	home := t.TempDir()
	s := &Server{Home: home}

	bin := filepath.Join(home, "blob.bin")
	os.WriteFile(bin, []byte{0x00, 0x01, 0x02, 0x00}, 0o644)
	big := filepath.Join(home, "big.txt")
	os.WriteFile(big, bytes.Repeat([]byte("x"), maxTextBytes+10), 0o644)

	for _, p := range []string{bin, big} {
		w := httptest.NewRecorder()
		s.handleFsRead(w, httptest.NewRequest(http.MethodGet, "/api/fs/read?download=1&path="+p, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: code = %d, want 200", p, w.Code)
		}
		if cd := w.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") {
			t.Errorf("%s: Content-Disposition = %q", p, cd)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/octet-stream" {
			t.Errorf("%s: Content-Type = %q", p, ct)
		}
		// nosniff is what stops the browser from second-guessing that
		// Content-Type and rendering, say, an HTML file inline from $HOME.
		if ns := w.Header().Get("X-Content-Type-Options"); ns != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q, want nosniff", p, ns)
		}
	}

	// Without download=1 the same two files are still refused exactly as before.
	w := httptest.NewRecorder()
	s.handleFsRead(w, httptest.NewRequest(http.MethodGet, "/api/fs/read?path="+big, nil))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("no download flag, big file: code = %d, want 413", w.Code)
	}
}

func TestReadDownloadQuotesFilename(t *testing.T) {
	home := t.TempDir()
	s := &Server{Home: home}
	p := filepath.Join(home, `we"ird name.txt`)
	os.WriteFile(p, []byte("x"), 0o644)
	w := httptest.NewRecorder()
	s.handleFsRead(w, httptest.NewRequest(http.MethodGet,
		"/api/fs/read?download=1&path="+url.QueryEscape(p), nil))
	cd := w.Header().Get("Content-Disposition")
	// The quote must survive as an ESCAPED quote inside the quoted-string, not
	// as a bare one that would terminate the parameter early and let the rest of
	// the filename be read as header syntax.
	if !strings.Contains(cd, `filename="we\"ird name.txt"`) {
		t.Fatalf("quote not escaped in header: %q", cd)
	}
}

func TestReadDownloadStillContained(t *testing.T) {
	home := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(outside, []byte("x"), 0o644)
	s := &Server{Home: home}
	w := httptest.NewRecorder()
	s.handleFsRead(w, httptest.NewRequest(http.MethodGet, "/api/fs/read?download=1&path="+outside, nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", w.Code)
	}
}

func TestFsReadReturnsVersionForText(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{Home: home}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/fs/read?path="+url.QueryEscape(filepath.Join(home, "a.txt")), nil)
	s.handleFsRead(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got fsRead
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("hello\n"))
	want := hex.EncodeToString(sum[:])
	if got.Version != want {
		t.Fatalf("version = %q, want %q", got.Version, want)
	}
}

// boxedServer is a Server whose BoxTrees name ws as a boxed workspace and
// home/data as a denied tree. home is symlink-resolved so expected paths match
// what the resolver reports.
func boxedServer(t *testing.T) (s *Server, home, ws string) {
	t.Helper()
	h, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws = filepath.Join(h, "ws")
	data := filepath.Join(h, "data")
	mustMkdirAll(t, ws)
	mustMkdirAll(t, data)
	mustWrite(t, filepath.Join(data, "cookie.secret"), "cookie")
	s = &Server{Home: h, MaxArchiveBytes: 1 << 20, MaxArchiveFiles: 100}
	s.BoxTrees = func() BoxTrees { return BoxTrees{Denied: []string{data}, Contained: []string{ws}} }
	return s, h, ws
}

func fsGet(h http.HandlerFunc, route, p string, extra ...string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	q := "?path=" + url.QueryEscape(p)
	for _, e := range extra {
		q += "&" + e
	}
	h(rr, httptest.NewRequest(http.MethodGet, route+q, nil))
	return rr
}

func TestFsReadBoxedEscapeRefused(t *testing.T) {
	s, home, ws := boxedServer(t)
	creds := filepath.Join(home, ".claude", ".credentials.json")
	mustWrite(t, creds, "TOPSECRET")
	mustSymlink(t, creds, filepath.Join(ws, "notes.txt"))

	for _, extra := range [][]string{nil, {"download=1"}} {
		rr := fsGet(s.handleFsRead, "/api/fs/read", filepath.Join(ws, "notes.txt"), extra...)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("extra=%v: code = %d, want 403 (%s)", extra, rr.Code, rr.Body)
		}
		if strings.Contains(rr.Body.String(), "TOPSECRET") {
			t.Fatalf("extra=%v: secret leaked: %s", extra, rr.Body)
		}
	}
}

func TestFsReadBoxedDirLinkRefused(t *testing.T) {
	s, home, ws := boxedServer(t)
	mustWrite(t, filepath.Join(home, ".claude", ".credentials.json"), "TOPSECRET")
	mustSymlink(t, home, filepath.Join(ws, "cfg"))

	rr := fsGet(s.handleFsRead, "/api/fs/read", filepath.Join(ws, "cfg", ".claude", ".credentials.json"))
	if rr.Code != http.StatusForbidden || strings.Contains(rr.Body.String(), "TOPSECRET") {
		t.Fatalf("read through dir link: code = %d body = %s, want 403", rr.Code, rr.Body)
	}
	rr = fsGet(s.handleFsList, "/api/fs/list", filepath.Join(ws, "cfg"))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("list of dir link: code = %d, want 403 (%s)", rr.Code, rr.Body)
	}
}

func TestFsReadBoxedDanglingAndInTreeLink(t *testing.T) {
	s, _, ws := boxedServer(t)
	mustWrite(t, filepath.Join(ws, "real.txt"), "real content")
	// Relative targets: os.Root resolves a link only if it stays beneath the root
	// by relative steps, so an absolute link is refused even when it points back
	// into the tree (see TestFsReadBoxedAbsoluteInTreeLinkRefused).
	mustSymlink(t, "missing", filepath.Join(ws, "d"))
	mustSymlink(t, "real.txt", filepath.Join(ws, "l"))

	if rr := fsGet(s.handleFsRead, "/api/fs/read", filepath.Join(ws, "d")); rr.Code != http.StatusNotFound {
		t.Fatalf("dangling link: code = %d, want 404 (%s)", rr.Code, rr.Body)
	}
	rr := fsGet(s.handleFsRead, "/api/fs/read", filepath.Join(ws, "l"))
	var fr fsRead
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &fr) != nil || fr.Content != "real content" {
		t.Fatalf("in-tree link: code = %d body = %s, want 200 with content", rr.Code, rr.Body)
	}
}

// A FIFO opened for reading blocks until a writer shows up, so a plain open of
// one is a request that never returns. It must be refused, host path or boxed.
func TestFsReadFifoRefused(t *testing.T) {
	s, home, ws := boxedServer(t)
	mustMkdirAll(t, filepath.Join(home, "proj"))
	fifos := []string{filepath.Join(ws, "p"), filepath.Join(home, "proj", "p")}
	for _, f := range fifos {
		if err := syscall.Mkfifo(f, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range fifos {
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- fsGet(s.handleFsRead, "/api/fs/read", f) }()
		select {
		case rr := <-done:
			if rr.Code != http.StatusForbidden {
				t.Fatalf("%s: code = %d, want 403 (%s)", f, rr.Code, rr.Body)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: read of a FIFO hung", f)
		}
	}
}

func TestFsListBoxedRootItself(t *testing.T) {
	s, _, ws := boxedServer(t)
	mustMkdirAll(t, filepath.Join(ws, "sub"))
	rr := fsGet(s.handleFsList, "/api/fs/list", ws)
	var got struct {
		Path    string    `json:"path"`
		Entries []fsEntry `json:"entries"`
	}
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &got) != nil {
		t.Fatalf("code = %d body = %s, want 200", rr.Code, rr.Body)
	}
	if got.Path != ws {
		t.Errorf("path = %q, want %q", got.Path, ws)
	}
	if len(got.Entries) != 1 || got.Entries[0].Name != "sub" || !got.Entries[0].IsDir {
		t.Errorf("entries = %+v, want [sub]", got.Entries)
	}
}

func TestFsDataDirDenied(t *testing.T) {
	s, home, _ := boxedServer(t)
	data := filepath.Join(home, "data")
	if rr := fsGet(s.handleFsList, "/api/fs/list", data); rr.Code != http.StatusForbidden {
		t.Errorf("list data dir: code = %d, want 403", rr.Code)
	}
	rr := fsGet(s.handleFsRead, "/api/fs/read", filepath.Join(data, "cookie.secret"))
	if rr.Code != http.StatusForbidden || strings.Contains(rr.Body.String(), "cookie") {
		t.Errorf("read cookie.secret: code = %d body = %s, want 403", rr.Code, rr.Body)
	}
}

// Pins the cost of os.Root containment: an ABSOLUTE link is refused even when its
// target is inside the same workspace. Safe direction, documented rather than
// worked around (resolving it ourselves would reintroduce the race os.Root closes).
func TestFsReadBoxedAbsoluteInTreeLinkRefused(t *testing.T) {
	s, _, ws := boxedServer(t)
	mustWrite(t, filepath.Join(ws, "real.txt"), "real content")
	mustSymlink(t, filepath.Join(ws, "real.txt"), filepath.Join(ws, "abs"))
	if rr := fsGet(s.handleFsRead, "/api/fs/read", filepath.Join(ws, "abs")); rr.Code != http.StatusForbidden {
		t.Fatalf("absolute in-tree link: code = %d, want 403 (%s)", rr.Code, rr.Body)
	}
}
