package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".avif", ".bmp", ".ico"} {
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

// metaRead is GET /api/fs/read?meta=1 for p.
func metaRead(s *Server, p string) *httptest.ResponseRecorder {
	return fsGet(s.handleFsRead, "/api/fs/read", p, "meta=1")
}

func TestFsReadMetaHasVersionNoContent(t *testing.T) {
	home := t.TempDir()
	// Multi-byte text, and a file at exactly the cap: the version is the one of
	// the same bytes the full read hashes, whatever the file.
	files := map[string]string{
		"a.txt":  "hello\n",
		"u.txt":  "h\u00e9llo \u20ac\n",
		"cap.go": strings.Repeat("x", maxTextBytes),
		"e.txt":  "",
	}
	s := &Server{Home: home}
	for name, content := range files {
		p := filepath.Join(home, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		rr := metaRead(s, p)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: meta status = %d (%s)", name, rr.Code, rr.Body)
		}
		var meta, full fsRead
		if err := json.Unmarshal(rr.Body.Bytes(), &meta); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(fsGet(s.handleFsRead, "/api/fs/read", p).Body.Bytes(), &full); err != nil {
			t.Fatal(err)
		}
		if meta.Kind != "text" || meta.Size != int64(len(content)) || meta.Version != hashText([]byte(content)) {
			t.Errorf("%s: meta = %+v, want text of %d bytes with the sha256 of the content", name, meta, len(content))
		}
		if meta.Version != full.Version || meta.Size != full.Size || meta.Kind != full.Kind {
			t.Errorf("%s: meta %+v and full read %+v disagree on kind, size or version", name, meta, full)
		}
		// No content, whatever the file holds: the poll costs a hash, not a payload.
		if meta.Content != "" || strings.Contains(rr.Body.String(), `"content"`) {
			t.Errorf("%s: meta carries content: %.80s", name, rr.Body)
		}
	}
	// The wire shape the client reads: kind, size, mtime, version, utf8, and readonly only when true.
	when := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	os.Chtimes(filepath.Join(home, "a.txt"), when, when)
	rr := metaRead(s, filepath.Join(home, "a.txt"))
	want := fmt.Sprintf(`{"kind":"text","size":6,"mtime":%d,"version":"`, when.UnixMilli()) + hashText([]byte("hello\n")) + `","utf8":true}`
	if strings.TrimSpace(rr.Body.String()) != want {
		t.Errorf("wire = %s, want %s", rr.Body, want)
	}
	// A change of one byte moves the version.
	os.WriteFile(filepath.Join(home, "a.txt"), []byte("hello!\n"), 0o644)
	var after fsRead
	json.Unmarshal(metaRead(s, filepath.Join(home, "a.txt")).Body.Bytes(), &after)
	if after.Version == hashText([]byte("hello\n")) || after.Version != hashText([]byte("hello!\n")) {
		t.Errorf("version after an edit = %q", after.Version)
	}
}

// A text file that is not valid UTF-8 (Latin-1, CP1252) is still text (no NUL byte), but the JSON body
// cannot carry its bytes: every invalid one reaches the client as U+FFFD, and saving that text back would
// replace them. Both reads say so with utf8:false, so the editor can lock the file. A valid file, a BOM
// and an empty file are UTF-8.
func TestFsReadSaysWhetherTextIsUTF8(t *testing.T) {
	home := t.TempDir()
	files := map[string]struct {
		bytes string
		valid bool
	}{
		"latin1.txt": {"caf\xe9 = 1\n", false},
		"cut.txt":    {"ok \xe2\x82", false}, // a multi-byte sequence cut short
		"utf8.txt":   {"caf\u00e9 \u20ac = 1\n", true},
		"bom.txt":    {"\xef\xbb\xbfhello\n", true},
		"empty.txt":  {"", true},
		"ascii.txt":  {"hello\n", true},
	}
	s := &Server{Home: home}
	for name, c := range files {
		p := filepath.Join(home, name)
		if err := os.WriteFile(p, []byte(c.bytes), 0o644); err != nil {
			t.Fatal(err)
		}
		for what, rr := range map[string]*httptest.ResponseRecorder{
			"full": fsGet(s.handleFsRead, "/api/fs/read", p),
			"meta": metaRead(s, p),
		} {
			var got struct {
				Kind string `json:"kind"`
				UTF8 *bool  `json:"utf8"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatalf("%s %s: %v", name, what, err)
			}
			if got.Kind != "text" || got.UTF8 == nil || *got.UTF8 != c.valid {
				t.Errorf("%s %s: kind %q utf8 %v, want text and utf8=%v (%s)", name, what, got.Kind, got.UTF8, c.valid, rr.Body)
			}
		}
		// The version is of the raw bytes in both, and does not depend on how they decode.
		var full fsRead
		json.Unmarshal(fsGet(s.handleFsRead, "/api/fs/read", p).Body.Bytes(), &full)
		if full.Version != hashText([]byte(c.bytes)) {
			t.Errorf("%s: version = %q, want the sha256 of the raw bytes", name, full.Version)
		}
	}
	// Only text says it: an image or a binary file has no encoding to speak of.
	bin := filepath.Join(home, "b.bin")
	os.WriteFile(bin, []byte("a\x00b\xe9"), 0o644)
	if rr := fsGet(s.handleFsRead, "/api/fs/read", bin); strings.Contains(rr.Body.String(), "utf8") {
		t.Errorf("binary read carries utf8: %s", rr.Body)
	}
}

func TestFsReadMetaKeepsTheReadCaps(t *testing.T) {
	home := t.TempDir()
	s := &Server{Home: home}
	big := filepath.Join(home, "big.txt")
	os.WriteFile(big, bytes.Repeat([]byte("a"), maxTextBytes+1), 0o644)
	// The same refusal as the full read: a file the editor cannot open has no version to poll.
	if rr := metaRead(s, big); rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the cap: code = %d, want 413", rr.Code)
	}
	if rr := metaRead(s, home); rr.Code != http.StatusBadRequest {
		t.Fatalf("directory: code = %d, want 400", rr.Code)
	}
	if rr := metaRead(s, filepath.Join(home, "gone.txt")); rr.Code != http.StatusNotFound {
		t.Fatalf("missing: code = %d, want 404", rr.Code)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(outside, []byte("x"), 0o644)
	if rr := metaRead(s, outside); rr.Code != http.StatusForbidden {
		t.Fatalf("outside home: code = %d, want 403", rr.Code)
	}
	// meta is a metadata read, never a download: it does not fetch the bytes either.
	txt := filepath.Join(home, "a.txt")
	os.WriteFile(txt, []byte("secret body"), 0o644)
	rr := fsGet(s.handleFsRead, "/api/fs/read", txt, "meta=1", "download=1")
	if rr.Header().Get("Content-Disposition") != "" || strings.Contains(rr.Body.String(), "secret body") {
		t.Fatalf("meta with download streamed the file: %v %s", rr.Header(), rr.Body)
	}
	// Only meta=1 is meta.
	for _, v := range []string{"0", "", "true", "yes"} {
		var fr fsRead
		json.Unmarshal(fsGet(s.handleFsRead, "/api/fs/read", txt, "meta="+v).Body.Bytes(), &fr)
		if fr.Content != "secret body" {
			t.Errorf("meta=%q: content = %q, want the full read", v, fr.Content)
		}
	}
}

func TestFsReadMetaImageNoStream(t *testing.T) {
	home := t.TempDir()
	png := []byte("\x89PNG\r\n\x1a\nrest")
	os.WriteFile(filepath.Join(home, "img.png"), png, 0o644)
	os.WriteFile(filepath.Join(home, "logo.svg"), []byte("<svg><script>alert(1)</script></svg>"), 0o644)
	os.WriteFile(filepath.Join(home, "b.bin"), []byte{0x00, 0x01, 0x02, 0x03}, 0o644)
	s := &Server{Home: home}
	for _, c := range []struct {
		name, kind string
		size       int64
	}{{"img.png", "image", int64(len(png))}, {"logo.svg", "image", 36}, {"b.bin", "binary", 4}} {
		rr := metaRead(s, filepath.Join(home, c.name))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: code = %d (%s)", c.name, rr.Code, rr.Body)
		}
		var fr fsRead
		if err := json.Unmarshal(rr.Body.Bytes(), &fr); err != nil {
			t.Fatalf("%s: not JSON: %v (%q)", c.name, err, rr.Body.Bytes())
		}
		if fr.Kind != c.kind || fr.Size != c.size || fr.Content != "" || fr.Version != "" {
			t.Errorf("%s: meta = %+v, want %s of %d bytes and no content", c.name, fr, c.kind, c.size)
		}
		// Not the image: no image type, none of its bytes, none of its headers.
		if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("%s: Content-Type = %q, want JSON", c.name, ct)
		}
		if bytes.Contains(rr.Body.Bytes(), []byte("PNG")) || strings.Contains(rr.Body.String(), "script") {
			t.Errorf("%s: meta streamed the file: %q", c.name, rr.Body.Bytes())
		}
	}
	// The binary reply is the full read's, size included.
	var full, meta fsRead
	json.Unmarshal(fsGet(s.handleFsRead, "/api/fs/read", filepath.Join(home, "b.bin")).Body.Bytes(), &full)
	json.Unmarshal(metaRead(s, filepath.Join(home, "b.bin")).Body.Bytes(), &meta)
	if full != meta {
		t.Errorf("binary: meta %+v, full %+v", meta, full)
	}
}

// A read-only file (the scratch folder, a box file) says so in its metadata too:
// the editor opens it without Save.
func TestFsReadMetaCarriesReadOnly(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, "a.txt")
	os.WriteFile(p, []byte("x\n"), 0o644)
	for _, ro := range []bool{true, false} {
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		info, _ := f.Stat()
		rr := httptest.NewRecorder()
		serveFsFile(rr, httptest.NewRequest(http.MethodGet, "/x?meta=1", nil), f, info, "a.txt", ro)
		f.Close()
		var fr fsRead
		json.Unmarshal(rr.Body.Bytes(), &fr)
		if fr.ReadOnly != ro || fr.Kind != "text" || fr.Content != "" || fr.Version != hashText([]byte("x\n")) {
			t.Errorf("readOnly %v: meta = %+v", ro, fr)
		}
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

func TestRawExt(t *testing.T) {
	want := map[string]string{
		".mp4": "video/mp4", ".webm": "video/webm", ".mp3": "audio/mpeg",
		".wav": "audio/wav", ".ogg": "audio/ogg", ".pdf": "application/pdf",
	}
	if len(rawExt) != len(want) {
		t.Fatalf("rawExt = %v, want exactly %v", rawExt, want)
	}
	for ext, ctype := range want {
		if rawExt[ext] != ctype {
			t.Errorf("rawExt[%s] = %q, want %q", ext, rawExt[ext], ctype)
		}
	}
}

func TestFsReadRawServesMediaAndPDF(t *testing.T) {
	home := t.TempDir()
	s := &Server{Home: home}
	body := []byte("0123456789abcdef\x00\x01")
	for name, ctype := range map[string]string{
		"a.mp4": "video/mp4", "a.webm": "video/webm", "a.mp3": "audio/mpeg",
		"a.wav": "audio/wav", "a.ogg": "audio/ogg", "a.pdf": "application/pdf",
		"UP.MP4": "video/mp4", // the extension is matched lowercase
	} {
		p := filepath.Join(home, name)
		os.WriteFile(p, body, 0o644)
		rr := fsGet(s.handleFsRead, "/api/fs/read", p, "raw=1")
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: code = %d (%s)", name, rr.Code, rr.Body)
		}
		h := rr.Header()
		if h.Get("Content-Type") != ctype {
			t.Errorf("%s: Content-Type = %q, want %q", name, h.Get("Content-Type"), ctype)
		}
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != "sandbox" ||
			h.Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: hardening headers = %v", name, h)
		}
		cd := h.Get("Content-Disposition")
		if ctype == "application/pdf" {
			if !strings.HasPrefix(cd, "inline;") || !strings.Contains(cd, `filename="a.pdf"`) ||
				!strings.Contains(cd, "filename*=UTF-8''a.pdf") {
				t.Errorf("%s: Content-Disposition = %q, want inline with the quoted name", name, cd)
			}
		} else if cd != "" {
			t.Errorf("%s: Content-Disposition = %q, want none", name, cd)
		}
		if !bytes.Equal(rr.Body.Bytes(), body) {
			t.Errorf("%s: body differs", name)
		}
	}
}

func TestFsReadRawPDFNameIsQuoted(t *testing.T) {
	home := t.TempDir()
	s := &Server{Home: home}
	p := filepath.Join(home, `a"b.pdf`)
	os.WriteFile(p, []byte("%PDF-1.4"), 0o644)
	cd := fsGet(s.handleFsRead, "/api/fs/read", p, "raw=1").Header().Get("Content-Disposition")
	if cd != `inline; filename="a\"b.pdf"; filename*=UTF-8''a%22b.pdf` {
		t.Errorf("Content-Disposition = %q", cd)
	}
}

func TestFsReadRawRangeSeeks(t *testing.T) {
	home := t.TempDir()
	s := &Server{Home: home}
	p := filepath.Join(home, "a.mp4")
	os.WriteFile(p, []byte("0123456789"), 0o644)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/fs/read?raw=1&path="+url.QueryEscape(p), nil)
	req.Header.Set("Range", "bytes=2-5")
	s.handleFsRead(rr, req)
	if rr.Code != http.StatusPartialContent || rr.Body.String() != "2345" {
		t.Fatalf("range: code = %d body = %q, want 206 and 2345", rr.Code, rr.Body)
	}
	if rr.Header().Get("Content-Range") != "bytes 2-5/10" || rr.Header().Get("Content-Security-Policy") != "sandbox" {
		t.Errorf("range headers = %v", rr.Header())
	}
}

func TestFsReadRawIsIgnoredOffTheAllowlist(t *testing.T) {
	home := t.TempDir()
	s := &Server{Home: home}
	// Never active content raw: html and friends stay on today's JSON path.
	for name, c := range map[string]struct {
		body []byte
		kind string
	}{
		"a.html":  {[]byte("<script>alert(1)</script>"), "text"},
		"a.txt":   {[]byte("plain"), "text"},
		"a.xhtml": {[]byte("<html/>"), "text"},
		"a.swf":   {[]byte{0, 1, 2}, "binary"},
		"a.bin":   {[]byte{0, 1, 2}, "binary"},
	} {
		p := filepath.Join(home, name)
		os.WriteFile(p, c.body, 0o644)
		rr := fsGet(s.handleFsRead, "/api/fs/read", p, "raw=1")
		var fr fsRead
		if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &fr) != nil || fr.Kind != c.kind {
			t.Errorf("%s: code = %d body = %q, want %s JSON", name, rr.Code, rr.Body, c.kind)
		}
		if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("%s: Content-Type = %q, want JSON", name, ct)
		}
	}
}

func TestFsReadMediaWithoutRawStaysJSON(t *testing.T) {
	home := t.TempDir()
	s := &Server{Home: home}
	for _, name := range []string{"a.mp4", "a.pdf", "a.mp3"} {
		p := filepath.Join(home, name)
		os.WriteFile(p, []byte{0x00, 0x01, 0x02, 0x03}, 0o644)
		for _, extra := range [][]string{nil, {"raw=0"}, {"raw=true"}} {
			rr := fsGet(s.handleFsRead, "/api/fs/read", p, extra...)
			var fr fsRead
			if json.Unmarshal(rr.Body.Bytes(), &fr) != nil || fr.Kind != "binary" || fr.Size != 4 {
				t.Errorf("%s %v: body = %q, want binary JSON", name, extra, rr.Body)
			}
		}
	}
}

func TestFsReadRawLosesToDownloadAndMeta(t *testing.T) {
	home := t.TempDir()
	s := &Server{Home: home}
	p := filepath.Join(home, "a.mp4")
	os.WriteFile(p, []byte{0x00, 0x01, 0x02, 0x03}, 0o644)
	rr := fsGet(s.handleFsRead, "/api/fs/read", p, "raw=1", "download=1")
	if rr.Header().Get("Content-Type") != "application/octet-stream" ||
		!strings.HasPrefix(rr.Header().Get("Content-Disposition"), "attachment;") {
		t.Errorf("raw+download: headers = %v, want the download", rr.Header())
	}
	rr = fsGet(s.handleFsRead, "/api/fs/read", p, "raw=1", "meta=1")
	var fr fsRead
	if json.Unmarshal(rr.Body.Bytes(), &fr) != nil || fr.Kind != "binary" || fr.Size != 4 {
		t.Errorf("raw+meta: body = %q, want binary JSON", rr.Body)
	}
}

func TestFsReadRawKeepsContainment(t *testing.T) {
	s, home, ws := boxedServer(t)
	creds := filepath.Join(home, ".claude", "secret.pdf")
	mustWrite(t, creds, "TOPSECRET")
	mustSymlink(t, creds, filepath.Join(ws, "doc.pdf"))
	rr := fsGet(s.handleFsRead, "/api/fs/read", filepath.Join(ws, "doc.pdf"), "raw=1")
	if rr.Code != http.StatusForbidden || strings.Contains(rr.Body.String(), "TOPSECRET") {
		t.Fatalf("raw through an escaping link: code = %d body = %s", rr.Code, rr.Body)
	}
}

func TestFsReadReportsMtime(t *testing.T) {
	home := t.TempDir()
	s := &Server{Home: home}
	when := time.Date(2026, 3, 4, 5, 6, 7, 123_000_000, time.UTC)
	wantMs := when.UnixMilli()
	files := map[string][]byte{
		"a.txt": []byte("hi"), "b.bin": {0, 1, 2}, "img.png": []byte("\x89PNG\r\n\x1a\nrest"),
	}
	for name, b := range files {
		p := filepath.Join(home, name)
		os.WriteFile(p, b, 0o644)
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name  string
		extra []string
	}{
		{"a.txt", nil}, {"a.txt", []string{"meta=1"}},
		{"b.bin", nil}, {"b.bin", []string{"meta=1"}},
		{"img.png", []string{"meta=1"}},
	} {
		rr := fsGet(s.handleFsRead, "/api/fs/read", filepath.Join(home, c.name), c.extra...)
		var fr fsRead
		if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &fr) != nil {
			t.Fatalf("%s %v: code = %d body = %q", c.name, c.extra, rr.Code, rr.Body)
		}
		if fr.Mtime != wantMs {
			t.Errorf("%s %v: mtime = %d, want %d", c.name, c.extra, fr.Mtime, wantMs)
		}
	}
}

func TestFsRead413ReportsSizeAndMtime(t *testing.T) {
	home := t.TempDir()
	s := &Server{Home: home}
	p := filepath.Join(home, "big.txt")
	os.WriteFile(p, bytes.Repeat([]byte("a"), maxTextBytes+10), 0o644)
	when := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	os.Chtimes(p, when, when)
	rr := fsGet(s.handleFsRead, "/api/fs/read", p)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code = %d, want 413", rr.Code)
	}
	var got struct {
		Error string `json:"error"`
		Size  int64  `json:"size"`
		Mtime int64  `json:"mtime"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("body not JSON: %q", rr.Body)
	}
	if got.Error != "file too large to preview" || got.Size != maxTextBytes+10 || got.Mtime != when.UnixMilli() {
		t.Errorf("413 body = %+v", got)
	}
}
