package web

import (
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// scratchServer is a Server with a home and a separate 0700 scratch root. The
// scratch root sits directly in /tmp with a short name so a unix socket path
// inside it stays under sun_path's ~107 bytes whatever GOTMPDIR is.
func scratchServer(t *testing.T) (s *Server, home, scratch string) {
	t.Helper()
	h, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sc, err := os.MkdirTemp("/tmp", "sk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sc) })
	if sc, err = filepath.EvalSymlinks(sc); err != nil {
		t.Fatal(err)
	}
	mustChmod(t, sc, 0o700)
	return &Server{Home: h, ScratchDirs: []string{sc}}, h, sc
}

func wantRefusal(t *testing.T, code int, msg string, gotCode int, gotMsg string) {
	t.Helper()
	if gotCode != code || gotMsg != msg {
		t.Fatalf("got %d %q, want %d %q", gotCode, gotMsg, code, msg)
	}
}

func TestFsErr_OutsideHome(t *testing.T) {
	s, _, _ := scratchServer(t)
	rec := fsGet(s.handleFsRead, "/api/fs/read", "/etc/hostname")
	wantRefusal(t, 403, msgOutside, rec.Code, errBody(t, rec))
}

func TestFsErr_DeniedTree(t *testing.T) {
	s, home, _ := boxedServer(t)
	rec := fsGet(s.handleFsRead, "/api/fs/read", filepath.Join(home, "data", "cookie.secret"))
	wantRefusal(t, 403, msgDenied, rec.Code, errBody(t, rec))
}

func TestFsErr_LinkEscapesWorkspace(t *testing.T) {
	s, _, ws := boxedServer(t)
	mustSymlink(t, "/etc/hostname", filepath.Join(ws, "evil"))
	rec := fsGet(s.handleFsRead, "/api/fs/read", filepath.Join(ws, "evil"))
	wantRefusal(t, 403, msgEscape, rec.Code, errBody(t, rec))
}

func TestFsErr_PermissionMidWalkIs403(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores mode bits")
	}
	s, home, _ := scratchServer(t)
	locked := filepath.Join(home, "locked")
	mustWrite(t, filepath.Join(locked, "f"))
	mustChmod(t, locked, 0)
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	rec := fsGet(s.handleFsRead, "/api/fs/read", filepath.Join(locked, "f"))
	wantRefusal(t, 403, msgPerm, rec.Code, errBody(t, rec))
}

func TestFsErr_PermissionAtOpen(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores mode bits")
	}
	s, home, _ := scratchServer(t)
	f := filepath.Join(home, "f")
	mustWrite(t, f)
	mustChmod(t, f, 0)
	rec := fsGet(s.handleFsRead, "/api/fs/read", f)
	wantRefusal(t, 403, msgPerm, rec.Code, errBody(t, rec))
}

func TestFsErr_FifoNotRegular(t *testing.T) {
	s, _, scratch := scratchServer(t)
	p := filepath.Join(scratch, "pipe")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var code int
	var msg string
	go func() {
		rec := fsGet(s.handleFsRead, "/api/fs/read", p)
		code, msg = rec.Code, errBody(t, rec)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reading a FIFO hung")
	}
	wantRefusal(t, 403, msgNotReg, code, msg)
}

func TestFsErr_SocketNotRegular(t *testing.T) {
	s, _, scratch := scratchServer(t)
	p := filepath.Join(scratch, "s.sock")
	l, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	rec := fsGet(s.handleFsRead, "/api/fs/read", p)
	wantRefusal(t, 403, msgNotReg, rec.Code, errBody(t, rec))
}

func TestFsErr_ProtectedFolderRenameAndDelete(t *testing.T) {
	s, _, ws := boxedServer(t)
	rec := httptest.NewRecorder()
	s.handleFsRename(rec, opReq(t, "/api/fs/rename", opJSON(map[string]string{"path": ws, "name": "ws2"})))
	wantRefusal(t, 403, msgProtected, rec.Code, errBody(t, rec))
	rec = httptest.NewRecorder()
	s.handleFsDelete(rec, opReq(t, "/api/fs/delete", opJSON(map[string]any{"path": ws, "recursive": true, "entries": 1})))
	wantRefusal(t, 403, msgProtected, rec.Code, errBody(t, rec))
}

func TestFsErr_PermissionOnMutationSaysChange(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores mode bits")
	}
	s, home, _ := scratchServer(t)
	locked := filepath.Join(home, "locked")
	mustMkdirAll(t, filepath.Join(locked, "sub"))
	mustChmod(t, locked, 0)
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	rec := httptest.NewRecorder()
	s.handleFsMkdir(rec, opReq(t, "/api/fs/mkdir", opJSON(map[string]string{"dir": filepath.Join(locked, "sub"), "name": "n"})))
	wantRefusal(t, 403, msgPermWrite, rec.Code, errBody(t, rec))
}

func TestFsRead_ScratchTextCarriesReadonly(t *testing.T) {
	s, home, scratch := scratchServer(t)
	mustWrite(t, filepath.Join(scratch, "notes.md"), "hi")
	mustWrite(t, filepath.Join(home, "a.md"), "hi")
	read := func(p string) map[string]any {
		rec := fsGet(s.handleFsRead, "/api/fs/read", p)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", p, rec.Code, rec.Body)
		}
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if m := read(filepath.Join(scratch, "notes.md")); m["readonly"] != true || m["content"] != "hi" {
		t.Fatalf("scratch read = %v", m)
	}
	if m := read(filepath.Join(home, "a.md")); m["readonly"] != nil {
		t.Fatalf("home read carries readonly: %v", m)
	}
}

func TestFsList_ScratchListsReadOnly(t *testing.T) {
	s, _, scratch := scratchServer(t)
	mustWrite(t, filepath.Join(scratch, "a", "b.log"))
	rec := fsGet(s.handleFsList, "/api/fs/list", filepath.Join(scratch, "a"))
	if rec.Code != 200 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
}
