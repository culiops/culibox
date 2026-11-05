package broker

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
)

const bx = "ab-box-0123456789ab"

// shortDir is a temp dir with a short path: unix socket paths are limited to
// 107 bytes and the test temp dir is long.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "bx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

// rootOfLen is a state root whose path is exactly n bytes long.
func rootOfLen(t *testing.T, n int) string {
	t.Helper()
	base := shortDir(t)
	k := n - len(base) - 1
	if k < 1 {
		t.Fatalf("cannot build a %d byte root under %s", n, base)
	}
	p := filepath.Join(base, strings.Repeat("a", k))
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	if len(p) != n {
		t.Fatalf("root %q is %d bytes, want %d", p, len(p), n)
	}
	return p
}

// perBoxSock is the socket path of box name under root.
func perBoxSock(root, name string) string {
	return filepath.Join(root, "broker", "b", name[len(name)-12:], "s")
}

// serveLog records the listeners handed to serve and serves each with a handler
// that echoes who the request came from.
type serveLog struct {
	mu sync.Mutex
	ls []net.Listener
}

func (s *serveLog) serve(l net.Listener) {
	s.mu.Lock()
	s.ls = append(s.ls, l)
	s.mu.Unlock()
	srv := &http.Server{BaseContext: BaseContext, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, shared := presenterOf(r.Context())
		if shared {
			b = "<shared>"
		}
		io.WriteString(w, b)
	})}
	go srv.Serve(l)
}

func (s *serveLog) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ls)
}

func (s *serveLog) at(i int) net.Listener {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ls[i]
}

// getOver does one GET over the unix socket and returns the body.
func getOver(sock string) (string, error) {
	c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}, Timeout: 5 * time.Second}
	resp, err := c.Get("http://broker/")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func startedBoxSockets(t *testing.T, root string) (*BoxSockets, *serveLog) {
	t.Helper()
	s := newBoxSockets(root)
	log := &serveLog{}
	s.Start(log.serve)
	t.Cleanup(s.Close)
	return s, log
}

func TestAcquireCreatesTheBoxsOwnSocket(t *testing.T) {
	root := shortDir(t)
	s, log := startedBoxSockets(t, root)
	m, err := s.Acquire(bx)
	if err != nil {
		t.Fatal(err)
	}
	if want := (box.Mount{Host: filepath.Join(root, "broker", "b", "0123456789ab"), Box: SockBoxDir}); m != want {
		t.Fatalf("mount = %+v, want %+v", m, want)
	}
	sock := perBoxSock(root, bx)
	if fi, err := os.Stat(sock); err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket %v %v", fi, err)
	}
	for _, d := range []string{filepath.Dir(sock), filepath.Join(root, "broker", "b")} {
		if fi, err := os.Stat(d); err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v, want 0700", d, fi, err)
		}
	}
	if log.count() != 1 {
		t.Fatalf("serve called %d times, want 1", log.count())
	}
	if bl, ok := log.at(0).(*boxListener); !ok || bl.box != bx {
		t.Fatalf("serve got %#v, want a *boxListener for %s", log.at(0), bx)
	}
	if body, err := getOver(sock); err != nil || body != bx {
		t.Fatalf("GET over the box's socket = %q, %v; want the box name", body, err)
	}
}

func TestAcquireIsCountedAndReleaseClosesAtZero(t *testing.T) {
	root := shortDir(t)
	s, log := startedBoxSockets(t, root)
	for i := 0; i < 2; i++ {
		if _, err := s.Acquire(bx); err != nil {
			t.Fatal(err)
		}
	}
	if log.count() != 1 {
		t.Fatalf("serve called %d times for one box, want 1", log.count())
	}
	sock := perBoxSock(root, bx)
	s.Release(bx)
	if body, err := getOver(sock); err != nil || body != bx {
		t.Fatalf("after one of two releases the socket must still serve: %q, %v", body, err)
	}
	s.Release(bx)
	if c, err := net.Dial("unix", sock); err == nil {
		c.Close()
		t.Fatal("the socket still accepts at zero references")
	}
	if _, err := os.Lstat(sock); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("socket file after the last release: %v, want it gone", err)
	}
	if fi, err := os.Stat(filepath.Dir(sock)); err != nil || !fi.IsDir() {
		t.Fatalf("the directory must stay (a running box mounts it): %v", err)
	}
	s.Release(bx) // unknown / zero: no-op
	s.Release("ab-box-ffffffffffff")
}

// A box that is still running mounts the DIRECTORY; a directory that was
// removed and recreated would be invisible through that bind mount.
func TestReacquireReusesTheDirectory(t *testing.T) {
	root := shortDir(t)
	s, _ := startedBoxSockets(t, root)
	if _, err := s.Acquire(bx); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(perBoxSock(root, bx))
	before, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Release(bx)
	if _, err := s.Acquire(bx); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(dir)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("the per-box directory was replaced: %v", err)
	}
	if body, err := getOver(perBoxSock(root, bx)); err != nil || body != bx {
		t.Fatalf("the recreated socket must serve: %q, %v", body, err)
	}
}

func TestAcquireReplacesAStaleSocket(t *testing.T) {
	root := shortDir(t)
	sock := perBoxSock(root, bx)
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close() // leaves the socket file behind, as a crash does
	if _, err := os.Lstat(sock); err != nil {
		t.Fatalf("no stale socket to replace: %v", err)
	}
	s, _ := startedBoxSockets(t, root)
	if _, err := s.Acquire(bx); err != nil {
		t.Fatalf("a stale socket must be replaced: %v", err)
	}
	if body, err := getOver(sock); err != nil || body != bx {
		t.Fatalf("GET = %q, %v", body, err)
	}
}

func TestAcquireRefusesANameThatIsNotABox(t *testing.T) {
	root := shortDir(t)
	s, log := startedBoxSockets(t, root)
	for _, bad := range []string{
		"", "ab-box-0123", "ab-box-0123456789AB", "../ab-box-0123456789ab", "ab-box-0123456789ab/x",
		"ab-box-0123456789ab/../../x", "..", "ab-box-../../../../etc", "/abs",
	} {
		if m, err := s.Acquire(bad); err == nil {
			t.Errorf("Acquire(%q) = %+v, want an error", bad, m)
		}
	}
	if ents, _ := os.ReadDir(filepath.Join(root, "broker", "b")); len(ents) != 0 {
		t.Fatalf("a refused name left %v behind", ents)
	}
	if log.count() != 0 {
		t.Fatalf("serve called %d times for refused names", log.count())
	}
}

func TestAcquireRefusesASymlinkedDir(t *testing.T) {
	root, elsewhere := shortDir(t), shortDir(t)
	if err := os.Mkdir(filepath.Join(root, "broker"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(root, "broker", "b")); err != nil {
		t.Fatal(err)
	}
	s, log := startedBoxSockets(t, root)
	if m, err := s.Acquire(bx); err == nil {
		t.Fatalf("a symlinked broker/b was followed: %+v", m)
	}
	if ents, _ := os.ReadDir(elsewhere); len(ents) != 0 {
		t.Fatalf("something was created through the symlink: %v", ents)
	}

	// The leaf directory itself a symlink.
	root2, elsewhere2 := shortDir(t), shortDir(t)
	if err := os.MkdirAll(filepath.Join(root2, "broker", "b"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere2, filepath.Join(root2, "broker", "b", "0123456789ab")); err != nil {
		t.Fatal(err)
	}
	s2, log2 := startedBoxSockets(t, root2)
	if m, err := s2.Acquire(bx); err == nil {
		t.Fatalf("a symlinked leaf directory was followed: %+v", m)
	}
	if ents, _ := os.ReadDir(elsewhere2); len(ents) != 0 {
		t.Fatalf("something was created through the leaf symlink: %v", ents)
	}
	if log.count() != 0 || log2.count() != 0 {
		t.Fatal("serve called for a refused directory")
	}
}

// 24 = len("/broker/b/") + 12 + len("/s"). Linux's sun_path holds 108 bytes
// including the NUL, and Go refuses a 108 byte path. The per-box layout is kept
// as short as the old shared socket's (len("/broker/sock/broker.sock") is also
// 24), so a state root that worked before still works.
func TestAcquireRefusesAPathOverTheUnixLimit(t *testing.T) {
	over := rootOfLen(t, 108-24)
	s, log := startedBoxSockets(t, over)
	_, err := s.Acquire(bx)
	if err == nil {
		t.Fatal("a 108 byte socket path was accepted")
	}
	if !strings.Contains(err.Error(), "107") {
		t.Fatalf("the error must name the limit: %v", err)
	}
	if log.count() != 0 {
		t.Fatal("serve called for a refused path")
	}
	if _, err := os.Lstat(filepath.Join(over, "broker")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused path must create nothing: %v", err)
	}

	atLimit := rootOfLen(t, 107-24)
	s2, _ := startedBoxSockets(t, atLimit)
	if _, err := s2.Acquire(bx); err != nil {
		t.Fatalf("a 107 byte socket path must work: %v", err)
	}
	if body, err := getOver(perBoxSock(atLimit, bx)); err != nil || body != bx {
		t.Fatalf("GET over the 107 byte path = %q, %v", body, err)
	}
}

func TestAcquireBeforeStartOrAfterCloseFails(t *testing.T) {
	root := shortDir(t)
	s := newBoxSockets(root)
	if _, err := s.Acquire(bx); err == nil {
		t.Fatal("Acquire before Start succeeded")
	}
	log := &serveLog{}
	s.Start(log.serve)
	if _, err := s.Acquire(bx); err != nil {
		t.Fatal(err)
	}
	s.Close()
	sock := perBoxSock(root, bx)
	if c, err := net.Dial("unix", sock); err == nil {
		c.Close()
		t.Fatal("the listener survived Close")
	}
	if _, err := os.Lstat(sock); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("socket file after Close: %v", err)
	}
	if _, err := s.Acquire(bx); err == nil {
		t.Fatal("Acquire after Close succeeded")
	}
}

func TestAcquireReleaseConcurrent(t *testing.T) {
	root := shortDir(t)
	s, _ := startedBoxSockets(t, root)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Acquire(bx); err != nil {
				t.Error(err)
				return
			}
			s.Release(bx)
		}()
	}
	wg.Wait()
	s.mu.Lock()
	n := len(s.socks)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d boxes still hold references", n)
	}
	if _, err := os.Lstat(perBoxSock(root, bx)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("socket file left after every reference was released: %v", err)
	}
}

// Boxes created before this change mount the whole broker/sock directory and
// run as the daemon's uid: nothing per-box may live under it.
func TestPerBoxSocketIsNeverInsideTheSharedDir(t *testing.T) {
	root := shortDir(t)
	s, _ := startedBoxSockets(t, root)
	m, err := s.Acquire(bx)
	if err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(root, "broker", "sock")
	if m.Host == shared || strings.HasPrefix(m.Host, shared+string(filepath.Separator)) {
		t.Fatalf("per-box dir %s is inside the shared dir %s", m.Host, shared)
	}
}

func TestBaseContextNamesThePresenter(t *testing.T) {
	if b, shared := presenterOf(BaseContext(&boxListener{box: bx})); b != bx || shared {
		t.Fatalf("per-box listener: (%q, %v), want (%q, false)", b, shared, bx)
	}
	if b, shared := presenterOf(BaseContext(&sharedListener{})); b != "" || !shared {
		t.Fatalf("shared listener: (%q, %v), want (\"\", true)", b, shared)
	}
	if b, shared := presenterOf(BaseContext(&net.TCPListener{})); b != "" || shared {
		t.Fatalf("plain listener: (%q, %v), want (\"\", false)", b, shared)
	}
	if b, shared := presenterOf(context.Background()); b != "" || shared {
		t.Fatalf("no presenter: (%q, %v)", b, shared)
	}
	if b, shared := presenterOf(withBox(context.Background(), bx)); b != bx || shared {
		t.Fatalf("withBox: (%q, %v)", b, shared)
	}
	if b, shared := presenterOf(withShared(context.Background())); b != "" || !shared {
		t.Fatalf("withShared: (%q, %v)", b, shared)
	}
}

// The presenter is the listener's, not the request's: a request over the
// shared socket never claims a box.
func TestSharedListenerRequestsArePresentedAsShared(t *testing.T) {
	root := shortDir(t)
	ep, err := Listen("linux", root, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Listener.Close()
	log := &serveLog{}
	log.serve(ep.Listener)
	body, err := getOver(filepath.Join(root, "broker", "sock", "broker.sock"))
	if err != nil || body != "<shared>" {
		t.Fatalf("GET over the shared socket = %q, %v; want the shared presenter", body, err)
	}
}
