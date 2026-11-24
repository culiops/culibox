package broker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hostSock is the socket path of Host session id under root.
func hostSockPath(root, id string) string {
	return filepath.Join(root, "broker", "h", HostDirName(id), "s")
}

func startedHostSockets(t *testing.T, root, goos string) (*HostSockets, *serveLog) {
	t.Helper()
	s := newHostSockets(root, goos)
	log := &serveLog{}
	s.Start(log.serve)
	t.Cleanup(s.Close)
	return s, log
}

func TestHostSocketAcquireBindsUnderStateRoot(t *testing.T) {
	root := shortDir(t)
	s, log := startedHostSockets(t, root, "linux")
	dir, err := s.Acquire("s1")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "broker", "h", HostDirName("s1")); dir != want {
		t.Fatalf("dir = %s, want %s", dir, want)
	}
	if len(HostDirName("s1")) != 12 {
		t.Fatalf("HostDirName = %q, want 12 hex", HostDirName("s1"))
	}
	fi, err := os.Stat(filepath.Join(dir, "s"))
	if err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket %v %v", fi, err)
	}
	if di, err := os.Stat(dir); err != nil || di.Mode().Perm() != 0o700 {
		t.Fatalf("dir %v %v", di, err)
	}
	if log.count() != 1 {
		t.Fatalf("serve called %d times, want 1", log.count())
	}
}

func TestHostListenerNamesItsSession(t *testing.T) {
	root := shortDir(t)
	s, _ := startedHostSockets(t, root, "linux")
	dir, err := s.Acquire("s1")
	if err != nil {
		t.Fatal(err)
	}
	// serveLog's handler echoes "host:<session>" for a request that came in on a
	// host listener.
	body, err := getOver(filepath.Join(dir, "s"))
	if err != nil || body != "host:s1" {
		t.Fatalf("GET over the host socket = %q, %v; want host:s1", body, err)
	}
	if b, h, shared := presenterOf(BaseContext(&hostListener{sid: "s1"})); b != "" || h != "s1" || shared {
		t.Fatalf("presenterOf = %q %q %v, want host s1", b, h, shared)
	}
	if b, h, shared := presenterOf(withHost(t.Context(), "s2")); b != "" || h != "s2" || shared {
		t.Fatalf("presenterOf = %q %q %v, want host s2", b, h, shared)
	}
}

func TestHostSocketsAreIndependent(t *testing.T) {
	root := shortDir(t)
	s, _ := startedHostSockets(t, root, "linux")
	if _, err := s.Acquire("s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire("s2"); err != nil {
		t.Fatal(err)
	}
	s.Release("s1")
	if _, err := os.Lstat(hostSockPath(root, "s1")); !os.IsNotExist(err) {
		t.Fatalf("s1's socket must be gone: %v", err)
	}
	if _, err := getOver(hostSockPath(root, "s2")); err != nil {
		t.Fatalf("s2 must still answer: %v", err)
	}
}

func TestHostSocketRefCount(t *testing.T) {
	root := shortDir(t)
	s, log := startedHostSockets(t, root, "linux")
	if _, err := s.Acquire("s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire("s1"); err != nil {
		t.Fatal(err)
	}
	if log.count() != 1 {
		t.Fatalf("a second Acquire must reuse the listener, serve called %d times", log.count())
	}
	s.Release("s1")
	if _, err := getOver(hostSockPath(root, "s1")); err != nil {
		t.Fatalf("still referenced, must answer: %v", err)
	}
	s.Release("s1")
	if _, err := os.Lstat(hostSockPath(root, "s1")); !os.IsNotExist(err) {
		t.Fatalf("last Release must close the socket: %v", err)
	}
	// The directory stays.
	if _, err := os.Stat(filepath.Dir(hostSockPath(root, "s1"))); err != nil {
		t.Fatalf("the directory must stay: %v", err)
	}
}

// The path is <root> + len("/broker/h/") + 12 + len("/s") = root + 24.
func TestHostSocketPathCapPerOS(t *testing.T) {
	root := rootOfLen(t, 105-24)
	sd, _ := startedHostSockets(t, root, "darwin")
	_, err := sd.Acquire("s1")
	if err == nil {
		t.Fatal("a 105 byte path was accepted for darwin")
	}
	if !strings.Contains(err.Error(), "at most 103") || !strings.Contains(err.Error(), hostSockPath(root, "s1")) {
		t.Fatalf("the error must name the limit and the path: %v", err)
	}
	sl, _ := startedHostSockets(t, root, "linux")
	if _, err := sl.Acquire("s1"); err != nil {
		t.Fatalf("a 105 byte path must work on linux: %v", err)
	}
}

func TestHostSocketRefusesPlantedSymlink(t *testing.T) {
	root := shortDir(t)
	elsewhere := shortDir(t)
	if err := os.Mkdir(filepath.Join(root, "broker"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(root, "broker", "h")); err != nil {
		t.Fatal(err)
	}
	s, log := startedHostSockets(t, root, "linux")
	if _, err := s.Acquire("s1"); err == nil {
		t.Fatal("a symlinked broker/h was followed")
	}
	if log.count() != 0 {
		t.Fatal("serve called for a refused directory")
	}
	if ents, _ := os.ReadDir(elsewhere); len(ents) != 0 {
		t.Fatalf("something was created through the symlink: %v", ents)
	}
}

func TestHostSocketAcquireBeforeStartFails(t *testing.T) {
	s := newHostSockets(shortDir(t), "linux")
	if _, err := s.Acquire("s1"); err == nil {
		t.Fatal("Acquire before Start succeeded")
	}
}
