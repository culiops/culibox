package broker

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/boxvm"
)

func TestListenLinuxUnixSocket(t *testing.T) {
	root := t.TempDir()
	ep, err := Listen("linux", root, 9071)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Listener.Close()
	sock := filepath.Join(root, "broker", "sock", "broker.sock")
	fi, err := os.Stat(sock)
	if err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket %v %v", fi, err)
	}
	if d, _ := os.Stat(filepath.Dir(sock)); d.Mode().Perm() != 0o700 {
		t.Fatalf("socket dir perm %v", d.Mode().Perm())
	}
	if ep.BoxURL != "unix:///opt/agentbox-sock/broker.sock" || len(ep.Mounts) != 1 ||
		ep.Mounts[0].Host != filepath.Dir(sock) || ep.Mounts[0].Box != SockBoxDir {
		t.Fatalf("endpoint %+v", ep)
	}
	ep.Listener.Close()
	ep2, err := Listen("linux", root, 9071) // a stale socket from a crash is replaced
	if err != nil {
		t.Fatal(err)
	}
	ep2.Listener.Close()
}

func TestListenLinuxServesOverTheSocket(t *testing.T) {
	ep, err := Listen("linux", t.TempDir(), 9071)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Listener.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })}
	go srv.Serve(ep.Listener)
	defer srv.Close()
	c := &http.Client{Transport: &http.Transport{DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
		return net.Dial("unix", ep.Listener.Addr().String())
	}}}
	resp, err := c.Get("http://broker/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestListenLinuxRefusesAPlantedSymlink(t *testing.T) {
	root, elsewhere := t.TempDir(), t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(root, "broker")); err != nil {
		t.Fatal(err)
	}
	if ep, err := Listen("linux", root, 9071); err == nil {
		ep.Listener.Close()
		t.Fatal("a symlinked broker dir must be refused")
	}
	if ents, _ := os.ReadDir(elsewhere); len(ents) != 0 {
		t.Fatalf("something was created through the symlink: %v", ents)
	}

	root2 := t.TempDir()
	if err := os.Mkdir(filepath.Join(root2, "broker"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(root2, "broker", "sock")); err != nil {
		t.Fatal(err)
	}
	if ep, err := Listen("linux", root2, 9071); err == nil {
		ep.Listener.Close()
		t.Fatal("a symlinked sock dir must be refused")
	}
	if ents, _ := os.ReadDir(elsewhere); len(ents) != 0 {
		t.Fatalf("something was created through the symlink: %v", ents)
	}
}

func TestListenLinuxTightensALooseDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "broker", "sock"), 0o755); err != nil {
		t.Fatal(err)
	}
	ep, err := Listen("linux", root, 9071)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Listener.Close()
	if d, _ := os.Stat(filepath.Join(root, "broker", "sock")); d.Mode().Perm() != 0o700 {
		t.Fatalf("socket dir perm %v", d.Mode().Perm())
	}
}

func TestListenDarwinLoopbackTCP(t *testing.T) {
	ep, err := Listen("darwin", t.TempDir(), 0) // 0 = any free port, for the test only
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Listener.Close()
	if host := ep.Listener.Addr().String(); !strings.HasPrefix(host, "127.0.0.1:") {
		t.Fatalf("darwin broker must bind loopback only, got %s", host)
	}
	if len(ep.Mounts) != 0 {
		t.Fatalf("endpoint %+v", ep)
	}
	// Boxes sit on the internal egress network; the relay on its gateway
	// forwards the broker port to the Mac's loopback listener.
	_, port, _ := net.SplitHostPort(ep.Listener.Addr().String())
	if want := fmt.Sprintf("http://%s:%s", boxvm.EgressGateway, port); ep.BoxURL != want {
		t.Fatalf("box URL %q, want %q", ep.BoxURL, want)
	}
}
