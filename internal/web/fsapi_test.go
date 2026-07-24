package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestContainedPath(t *testing.T) {
	home := t.TempDir()
	sub := filepath.Join(home, "proj")
	mustMkdir(t, sub)
	inFile := filepath.Join(sub, "a.txt")
	if err := os.WriteFile(inFile, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	outFile := filepath.Join(outside, "secret")
	if err := os.WriteFile(outFile, []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(home, "escape")
	if err := os.Symlink(outFile, escape); err != nil {
		t.Skip("symlink not supported")
	}

	if got, err := containedPath(home, inFile); err != nil || got == "" {
		t.Fatalf("in-home file: got (%q,%v), want ok", got, err)
	}
	if _, err := containedPath(home, "relative/path"); !errors.Is(err, errBadPath) {
		t.Fatalf("relative: want errBadPath, got %v", err)
	}
	if _, err := containedPath(home, filepath.Join(sub, "nope.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing: want ErrNotExist, got %v", err)
	}
	if _, err := containedPath(home, filepath.Join(sub, "..", "..", filepath.Base(outside), "secret")); !errors.Is(err, errOutsideHome) {
		t.Fatalf("traversal: want errOutsideHome, got %v", err)
	}
	if _, err := containedPath(home, escape); !errors.Is(err, errOutsideHome) {
		t.Fatalf("symlink escape: want errOutsideHome, got %v", err)
	}
}

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
