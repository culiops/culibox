package broker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/boxaddr"
	"github.com/culiops/culibox/internal/boxvm"
)

func TestListenLinuxUnixSocket(t *testing.T) {
	root := shortDir(t)
	ep, err := Listen("linux", root, 9071)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Listener.Close()
	defer ep.Boxes.Close()
	// The shared socket is still there: boxes from before #73 mount its dir,
	// and it answers them with a respawn message instead of a credential.
	sock := filepath.Join(root, "broker", "sock", "broker.sock")
	fi, err := os.Stat(sock)
	if err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket %v %v", fi, err)
	}
	if d, _ := os.Stat(filepath.Dir(sock)); d.Mode().Perm() != 0o700 {
		t.Fatalf("socket dir perm %v", d.Mode().Perm())
	}
	if _, ok := ep.Listener.(*sharedListener); !ok {
		t.Fatalf("the shared listener is a %T, want *sharedListener", ep.Listener)
	}
	if ep.Boxes == nil {
		t.Fatal("no per-box sockets on Linux")
	}
	if ep.BoxURL != "unix:///opt/agentbox-broker/s" {
		t.Fatalf("box URL %q", ep.BoxURL)
	}
	ep.Listener.Close()
	ep2, err := Listen("linux", root, 9071) // a stale socket from a crash is replaced
	if err != nil {
		t.Fatal(err)
	}
	ep2.Listener.Close()
}

// 84 + 24 = 108: the per-box socket path would not fit in sun_path. Startup
// refuses (fail closed, clear error) instead of listening on a shared socket
// only: a bound session would then get no credentials at all, silently.
func TestListenLinuxRefusesAStateRootTooLongForPerBoxSockets(t *testing.T) {
	root := rootOfLen(t, 84)
	ep, err := Listen("linux", root, 9071)
	if err == nil {
		ep.Listener.Close()
		t.Fatal("a state root that pushes the per-box socket path over the unix limit was accepted")
	}
	if !strings.Contains(err.Error(), "107") {
		t.Fatalf("the error must name the limit: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "broker", "sock", "broker.sock")); err == nil {
		t.Fatal("a shared socket was left listening")
	}
	// One byte shorter fits.
	ok, err := Listen("linux", rootOfLen(t, 83), 9071)
	if err != nil {
		t.Fatalf("an 83 byte state root must work: %v", err)
	}
	ok.Listener.Close()
}

// Boxes created before the per-box sockets mount the shared broker/sock dir at
// /opt/agentbox-sock. A new box mounts its own dir at a different in-box path:
// box.reuseMounts compares in-box destinations only, so a changed destination
// is what stops a pre-upgrade box from being reused silently for a new bound
// spawn (see box.TestBoxFromBeforePerBoxSocketsIsNeverReusedSilently).
func TestSockBoxDirIsNotThePreBindingDir(t *testing.T) {
	if SockBoxDir == "/opt/agentbox-sock" {
		t.Fatal("SockBoxDir is the pre-#73 path: an old box would be reused silently")
	}
	if SockBoxDir != "/opt/agentbox-broker" {
		t.Fatalf("SockBoxDir = %q", SockBoxDir)
	}
}

func TestListenLinuxServesOverTheSocket(t *testing.T) {
	ep, err := Listen("linux", shortDir(t), 9071)
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
	root, elsewhere := shortDir(t), t.TempDir()
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

	root2 := shortDir(t)
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
	root := shortDir(t)
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
	if ep.Boxes != nil {
		t.Fatalf("darwin has no per-box sockets: %+v", ep)
	}
	// Boxes sit on the internal egress network; the relay on its gateway
	// forwards the broker port to the Mac's loopback listener.
	_, port, _ := net.SplitHostPort(ep.Listener.Addr().String())
	if want := fmt.Sprintf("http://%s:%s", boxvm.EgressGateway, port); ep.BoxURL != want {
		t.Fatalf("box URL %q, want %q", ep.BoxURL, want)
	}
}

// darwinBroker listens like the Mac does and serves a handler that echoes the
// peer address it saw.
func darwinBroker(t *testing.T) string {
	t.Helper()
	ep, err := Listen("darwin", t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(r.RemoteAddr)) })}
	go srv.Serve(ep.Listener)
	t.Cleanup(func() { srv.Close(); ep.Listener.Close() })
	return ep.Listener.Addr().String()
}

func rawGet(t *testing.T, addr, prefix string) (string, error) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(c, "%sGET / HTTP/1.1\r\nHost: b\r\nConnection: close\r\n\r\n", prefix)
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: "GET"})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

// On macOS the broker is reachable only through the relay: a PROXY line naming
// an address inside the box subnet (not the gateway) is required, and it
// becomes the request's RemoteAddr.
func TestListenDarwinRequiresHeader(t *testing.T) {
	addr := darwinBroker(t)
	body, err := rawGet(t, addr, "PROXY TCP4 172.29.72.9 172.29.72.1 40000 9071\r\n")
	if err != nil || body != "172.29.72.9:40000" {
		t.Fatalf("with header: body %q err %v", body, err)
	}
	for name, prefix := range map[string]string{
		"no header":   "",
		"gateway":     "PROXY TCP4 172.29.72.1 172.29.72.1 40000 9071\r\n",
		"outside":     "PROXY TCP4 10.1.2.3 172.29.72.1 40000 9071\r\n",
		"loopback":    "PROXY TCP4 127.0.0.1 172.29.72.1 40000 9071\r\n",
		"garbage":     "PROXY nonsense\r\n",
		"unspecified": "PROXY TCP4 0.0.0.0 172.29.72.1 40000 9071\r\n",
	} {
		if body, err := rawGet(t, addr, prefix); err == nil {
			t.Errorf("%s: served %q", name, body)
		}
	}
}

// rawPost is rawGet for a POST with a bearer token: status and body, or the
// error when the connection was closed unanswered.
func rawPost(t *testing.T, addr, prefix, path, tok, body string) (int, string, error) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(c, "%sPOST %s HTTP/1.1\r\nHost: b\r\nAuthorization: Bearer %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		prefix, path, tok, len(body), body)
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: "POST"})
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), nil
}

// The darwin listener holds at most n raw connections, silent ones included
// (the cap sits under the PROXY wrapper, so a connection that never sends its
// header still holds its slot until the header timeout).
func TestListenDarwinCapsConnections(t *testing.T) {
	ep, err := listenDarwin(0, 2)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(r.RemoteAddr)) })}
	go srv.Serve(ep.Listener)
	t.Cleanup(func() { srv.Close(); ep.Listener.Close() })
	addr := ep.Listener.Addr().String()

	var idle []net.Conn
	for i := 0; i < 2; i++ {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		idle = append(idle, c)
		t.Cleanup(func() { c.Close() })
	}
	third, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	third.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprint(third, "PROXY TCP4 172.29.72.9 172.29.72.1 40000 9071\r\nGET / HTTP/1.1\r\nHost: b\r\nConnection: close\r\n\r\n")
	got := make(chan string, 1)
	go func() {
		resp, err := http.ReadResponse(bufio.NewReader(third), &http.Request{Method: "GET"})
		if err != nil {
			got <- "error: " + err.Error()
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		got <- string(b)
	}()
	select {
	case r := <-got:
		t.Fatalf("a third connection was served while two were held open: %q", r)
	case <-time.After(300 * time.Millisecond):
	}
	idle[0].Close() // a slot frees
	select {
	case r := <-got:
		if r != "172.29.72.9:40000" {
			t.Fatalf("third connection got %q", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("closing a connection did not let the waiting one in")
	}
}

func TestMaxDarwinConnsIsBounded(t *testing.T) {
	if maxDarwinConns <= 0 || maxDarwinConns > 512 {
		t.Fatalf("maxDarwinConns = %d", maxDarwinConns)
	}
}

// macOS end to end over the real listener: the relay's PROXY source decides the
// box. The box's own address gets the value; any other address, a bound box's or
// an unbound one's, is refused as denied_box; no PROXY line is closed unanswered
// and writes no row.
func TestDarwinTokenBoundToItsBoxAddress(t *testing.T) {
	v := staticSession(grafanaEntry())
	v.Box = ownBox
	b, _, rec, _ := staticSetup(t, map[string]SessionView{"s": v})
	lookup := func(_ context.Context, box string) (netip.Addr, error) {
		if box == ownBox {
			return netip.MustParseAddr("172.29.72.9"), nil
		}
		return netip.Addr{}, errors.New("no such box")
	}
	b.SetBoxSource(boxaddr.New(context.Background(), lookup, time.Now))
	ep, err := listenDarwin(0, maxDarwinConns)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: b.Handler(), BaseContext: BaseContext, ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ep.Listener)
	t.Cleanup(func() { srv.Close(); ep.Listener.Close() })
	addr := ep.Listener.Addr().String()
	tok := tokA("s")

	code, body, err := rawPost(t, addr, "PROXY TCP4 172.29.72.9 172.29.72.1 40000 9071\r\n", "/v1/static/token", tok, grafanaBody)
	if err != nil || code != 200 || !strings.Contains(body, staticSecret) {
		t.Fatalf("own address: %d %q %v", code, body, err)
	}
	code, body, err = rawPost(t, addr, "PROXY TCP4 172.29.72.10 172.29.72.1 40000 9071\r\n", "/v1/static/token", tok, grafanaBody)
	if err != nil || code != 403 || strings.Contains(body, staticSecret) {
		t.Fatalf("another box's address: %d %q %v", code, body, err)
	}
	rec.mu.Lock()
	rows := append([]Row(nil), rec.rows...)
	rec.mu.Unlock()
	if len(rows) != 2 || rows[0].Outcome != OutcomeFetched || rows[1].Outcome != OutcomeDeniedBox || rows[1].SessionID != "s" {
		t.Fatalf("rows = %+v, want fetched then denied_box", rows)
	}
	if _, _, err := rawPost(t, addr, "", "/v1/static/token", tok, grafanaBody); err == nil {
		t.Fatal("a connection with no PROXY line was answered")
	}
	rec.mu.Lock()
	n := len(rec.rows)
	rec.mu.Unlock()
	if n != 2 {
		t.Fatalf("a connection with no PROXY line wrote a row: %d rows", n)
	}
}
