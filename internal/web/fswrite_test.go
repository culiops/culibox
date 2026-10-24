package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

// A group/other-writable original (0664) is the case 0640 cannot catch: under a
// standard umask (022), 0640 already lacks the bits umask would strip, so a
// save that silently loses them still reports mode 0640 as "unchanged". This
// pins the process umask deterministically so the test fails without an
// explicit Chmod in writeTemp (OpenFile's create mode is subject to umask;
// os.File.Chmod is not).
func TestFsWriteSuccessPreservesGroupWritableModeUnderUmask(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)

	home := t.TempDir()
	p := filepath.Join(home, "c.conf")
	if err := os.WriteFile(p, []byte("old\n"), 0o664); err != nil {
		t.Fatal(err)
	}
	// os.WriteFile's own create is likewise subject to umask, so force the
	// exact starting mode with an explicit Chmod (unaffected by umask) rather
	// than trusting the WriteFile call to have produced 0664.
	if err := os.Chmod(p, 0o664); err != nil {
		t.Fatal(err)
	}
	base := hashText([]byte("old\n"))
	s := &Server{Home: home}
	body := fmt.Sprintf(`{"path":%q,"content":"new\n","base_version":%q}`, p, base)
	rr := writeReq(t, s, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o664 {
		t.Fatalf("mode = %o, want 664 — the umask must not strip the original's group/other write bits", info.Mode().Perm())
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

func TestWriteBoxedDirLinkRefused(t *testing.T) {
	s, home, ws := boxedLinked(t)
	body := fmt.Sprintf(`{"path":%q,"content":"pwned","overwrite":true}`, filepath.Join(ws, "cfg", ".bashrc"))
	rr := writeReq(t, s, body)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403 (%s)", rr.Code, rr.Body)
	}
	mustContent(t, filepath.Join(home, ".bashrc"), "host")
	ents, _ := os.ReadDir(home)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".agentbox-tmp-") {
			t.Fatalf("temp file left in the host tree: %s", e.Name())
		}
	}
}

func TestWriteInsideBoxedWorkspaceWorks(t *testing.T) {
	s, _, ws := boxedServer(t)
	p := filepath.Join(ws, "n.txt")
	mustWrite(t, p, "old")
	rr := writeReq(t, s, fmt.Sprintf(`{"path":%q,"content":"new","base_version":%q}`, p, hashText([]byte("old"))))
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d (%s)", rr.Code, rr.Body)
	}
	mustContent(t, p, "new")
}

// A box can leave a FIFO where a file is expected; reading it for the optimistic
// lock must refuse instead of blocking for a writer.
func TestWriteLockReadRefusesFifo(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o644); err != nil {
		t.Skip("mkfifo not supported")
	}
	root := mustRoot(t, dir)
	done := make(chan error, 1)
	go func() {
		_, err := readRootFile(root, "pipe")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errNotRegular) {
			t.Fatalf("err = %v, want errNotRegular", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readRootFile blocked on a FIFO")
	}
}
