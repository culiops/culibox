package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeReq builds a same-origin JSON POST to /api/fs/write, mirroring how
// fsops_test.go's opReq satisfies the sameOrigin guard (Sec-Fetch-Site, not
// Origin/Host).
func writeReq(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/fs/write", strings.NewReader(body))
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	s.handleFsWrite(rr, req)
	return rr
}

func TestFsWriteSuccessIsAtomicAndKeepsMode(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, "c.conf")
	if err := os.WriteFile(p, []byte("old\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	base := hashText([]byte("old\n"))
	s := &Server{Home: home}
	body := fmt.Sprintf(`{"path":%q,"content":"new\n","base_version":%q}`, p, base)
	rr := writeReq(t, s, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	got, _ := os.ReadFile(p)
	if string(got) != "new\n" {
		t.Fatalf("content = %q", got)
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 640", info.Mode().Perm())
	}
	var resp struct {
		Version string `json:"version"`
	}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Version != hashText([]byte("new\n")) {
		t.Fatalf("returned version mismatch")
	}
}

func TestFsWriteStaleVersionConflicts(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, "c.conf")
	os.WriteFile(p, []byte("current\n"), 0o644)
	s := &Server{Home: home}
	body := fmt.Sprintf(`{"path":%q,"content":"mine\n","base_version":"deadbeef"}`, p)
	rr := writeReq(t, s, body)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "current\n" {
		t.Fatalf("file was overwritten on conflict: %q", got)
	}
	var resp struct {
		CurrentVersion string `json:"current_version"`
	}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.CurrentVersion != hashText([]byte("current\n")) {
		t.Fatalf("current_version = %q", resp.CurrentVersion)
	}
}

func TestFsWriteOverwriteBypassesLock(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, "c.conf")
	os.WriteFile(p, []byte("current\n"), 0o644)
	s := &Server{Home: home}
	body := fmt.Sprintf(`{"path":%q,"content":"forced\n","base_version":"stale","overwrite":true}`, p)
	rr := writeReq(t, s, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "forced\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestFsWriteMissingFileIs404(t *testing.T) {
	home := t.TempDir()
	s := &Server{Home: home}
	p := filepath.Join(home, "nope.txt")
	body := fmt.Sprintf(`{"path":%q,"content":"x","base_version":""}`, p)
	rr := writeReq(t, s, body)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestFsWriteDirectoryIs400(t *testing.T) {
	home := t.TempDir()
	d := filepath.Join(home, "sub")
	os.Mkdir(d, 0o755)
	s := &Server{Home: home}
	body := fmt.Sprintf(`{"path":%q,"content":"x","base_version":""}`, d)
	rr := writeReq(t, s, body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestFsWriteSymlinkRefusedAndUnchanged(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "real.txt")
	os.WriteFile(target, []byte("real\n"), 0o644)
	link := filepath.Join(home, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlink unsupported")
	}
	s := &Server{Home: home}
	body := fmt.Sprintf(`{"path":%q,"content":"evil\n","base_version":""}`, link)
	rr := writeReq(t, s, body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	// Neither the link nor its target was written.
	got, _ := os.ReadFile(target)
	if string(got) != "real\n" {
		t.Fatalf("target changed: %q", got)
	}
	fi, _ := os.Lstat(link)
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink was replaced by a regular file")
	}
}

func TestFsWriteOverCapIs413(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, "c.conf")
	os.WriteFile(p, []byte("x"), 0o644)
	s := &Server{Home: home}
	big := strings.Repeat("a", (2<<20)+1)
	body := fmt.Sprintf(`{"path":%q,"content":%q,"base_version":%q}`, p, big, hashText([]byte("x")))
	rr := writeReq(t, s, body)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rr.Code)
	}
}

func TestFsWriteCrossOriginForbidden(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, "c.conf")
	os.WriteFile(p, []byte("x"), 0o644)
	s := &Server{Home: home}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/fs/write",
		strings.NewReader(fmt.Sprintf(`{"path":%q,"content":"y","base_version":""}`, p)))
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	s.handleFsWrite(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}
