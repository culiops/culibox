package web

import (
	"bytes"
	"encoding/json"
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

// boxfileAPI is an API whose boxed session "bx" has a real out dir; "hs" is a
// host session (BoxOutputDir false). Returns the API and the out dir.
func boxfileAPI(t *testing.T) (*API, string) {
	t.Helper()
	dir := t.TempDir()
	return &API{Sessions: &mockSvc{boxOut: map[string]string{"bx": dir}}}, dir
}

func boxfileGet(a *API, id, path string, extra ...string) *httptest.ResponseRecorder {
	q := url.Values{"path": {path}}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	rec := httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("GET", "/api/sessions/"+id+"/boxfile?"+q.Encode(), nil))
	return rec
}

func writeOut(t *testing.T, dir, name string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func errBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body not JSON error: %q", rec.Body)
	}
	return m["error"]
}

func TestBoxfileServesPNG(t *testing.T) {
	a, dir := boxfileAPI(t)
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00fake")
	writeOut(t, dir, "a.png", png)
	rec := boxfileGet(a, "bx", "/tmp/playwright-mcp/a.png")
	if rec.Code != 200 {
		t.Fatalf("code %d: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type %q", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff")
	}
	if rec.Header().Get("Content-Security-Policy") != "sandbox" {
		t.Fatal("missing CSP sandbox")
	}
	if !bytes.Equal(rec.Body.Bytes(), png) {
		t.Fatalf("body differs")
	}
}

func TestBoxfileServesYmlAsTextJSON(t *testing.T) {
	a, dir := boxfileAPI(t)
	writeOut(t, dir, "page.yml", []byte("- heading: hi\n"))
	rec := boxfileGet(a, "bx", "/tmp/playwright-mcp/page.yml")
	if rec.Code != 200 {
		t.Fatalf("code %d: %s", rec.Code, rec.Body)
	}
	var r fsRead
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Kind != "text" || r.Content != "- heading: hi\n" {
		t.Fatalf("got %+v", r)
	}
}

func TestBoxfileDownload(t *testing.T) {
	a, dir := boxfileAPI(t)
	writeOut(t, dir, "a.png", []byte("bytes"))
	rec := boxfileGet(a, "bx", "/tmp/playwright-mcp/a.png", "download", "1")
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("Content-Type %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, `"a.png"`) {
		t.Fatalf("Content-Disposition %q", cd)
	}
}

func TestBoxfileRefusesOutsideAllowlist(t *testing.T) {
	a, dir := boxfileAPI(t)
	writeOut(t, dir, "a.png", []byte("x"))
	for _, p := range []string{
		"/home/agent/.claude.json",
		"/tmp/x.png",
		"/tmp/playwright-mcp",
		"/tmp/playwright-mcp/",
		"/tmp/playwright-mcpX/a.png",
		"tmp/playwright-mcp/a.png", // relative
		"",
		"/tmp/playwright-mcp/../../home/agent/.claude.json", // traversal: cleaned path is outside
		"/tmp/playwright-mcp/../x.png",
	} {
		rec := boxfileGet(a, "bx", p)
		if rec.Code != 403 {
			t.Fatalf("path %q: code %d, want 403 (%s)", p, rec.Code, rec.Body)
		}
		if e := errBody(t, rec); e != msgBoxOnly {
			t.Fatalf("path %q: error %q", p, e)
		}
	}
}

func TestBoxfileRefusesSymlinkFinal(t *testing.T) {
	a, dir := boxfileAPI(t)
	outside := filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(outside, []byte("SECRET-BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "x.png")); err != nil {
		t.Fatal(err)
	}
	rec := boxfileGet(a, "bx", "/tmp/playwright-mcp/x.png")
	if rec.Code != 404 || strings.Contains(rec.Body.String(), "SECRET-BYTES") {
		t.Fatalf("code %d body %q", rec.Code, rec.Body)
	}
}

func TestBoxfileRefusesSymlinkedSubdir(t *testing.T) {
	a, dir := boxfileAPI(t)
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "a.png"), []byte("SECRET-BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(dir, "sub")); err != nil {
		t.Fatal(err)
	}
	rec := boxfileGet(a, "bx", "/tmp/playwright-mcp/sub/a.png")
	if rec.Code != 404 || strings.Contains(rec.Body.String(), "SECRET-BYTES") {
		t.Fatalf("code %d body %q", rec.Code, rec.Body)
	}
}

func TestBoxfileRefusesFIFO(t *testing.T) {
	a, dir := boxfileAPI(t)
	if err := syscall.Mkfifo(filepath.Join(dir, "f.png"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- boxfileGet(a, "bx", "/tmp/playwright-mcp/f.png") }()
	select {
	case rec := <-done:
		if rec.Code != 404 {
			t.Fatalf("code %d, want 404", rec.Code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request blocked on a FIFO")
	}
}

func TestBoxfileMissingFile404(t *testing.T) {
	a, _ := boxfileAPI(t)
	if rec := boxfileGet(a, "bx", "/tmp/playwright-mcp/nope.png"); rec.Code != 404 {
		t.Fatalf("code %d, want 404", rec.Code)
	}
}

func TestBoxfileHostAndUnknownSession404(t *testing.T) {
	a, dir := boxfileAPI(t)
	writeOut(t, dir, "a.png", []byte("x"))
	for _, id := range []string{"hs", "unknown"} {
		rec := boxfileGet(a, id, "/tmp/playwright-mcp/a.png")
		if rec.Code != 404 {
			t.Fatalf("session %q: code %d, want 404", id, rec.Code)
		}
	}
}

func TestBoxfileMethod(t *testing.T) {
	a, dir := boxfileAPI(t)
	writeOut(t, dir, "a.png", []byte("x"))
	for _, m := range []string{"POST", "PUT", "DELETE"} {
		rec := httptest.NewRecorder()
		a.handleSession(rec, httptest.NewRequest(m, "/api/sessions/bx/boxfile?path=/tmp/playwright-mcp/a.png", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: code %d, want 405", m, rec.Code)
		}
	}
}
