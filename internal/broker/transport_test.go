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
	"github.com/culiops/culibox/internal/proxyline"
)

func TestListenLinuxUnixSocket(t *testing.T) {
	root := shortDir(t)
	ep, err := Listen("linux", root, 9071, proxyline.Key{})
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
	ep2, err := Listen("linux", root, 9071, proxyline.Key{}) // a stale socket from a crash is replaced
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
	ep, err := Listen("linux", root, 9071, proxyline.Key{})
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
	ok, err := Listen("linux", rootOfLen(t, 83), 9071, proxyline.Key{})
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
	ep, err := Listen("linux", shortDir(t), 9071, proxyline.Key{})
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
	if ep, err := Listen("linux", root, 9071, proxyline.Key{}); err == nil {
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
	if ep, err := Listen("linux", root2, 9071, proxyline.Key{}); err == nil {
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
	ep, err := Listen("linux", root, 9071, proxyline.Key{})
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Listener.Close()
	if d, _ := os.Stat(filepath.Join(root, "broker", "sock")); d.Mode().Perm() != 0o700 {
		t.Fatalf("socket dir perm %v", d.Mode().Perm())
	}
}

// testRelayKey is the relay key every darwin test listener wants; the relay
// proves it before its PROXY line is read. wrongRelayKey is another one.
var (
	testRelayKey  = mustRelayKey(0x31)
	wrongRelayKey = mustRelayKey(0x32)
)

func mustRelayKey(seed byte) proxyline.Key {
	var raw [32]byte
	for i := range raw {
		raw[i] = seed + byte(i)
	}
	k, err := proxyline.KeyFromBytes(raw[:])
	if err != nil {
		panic(err)
	}
	return k
}

// proxyLine is the PROXY line the relay writes for a box at src.
func proxyLine(src string) string {
	return "PROXY TCP4 " + src + " 172.29.72.1 40000 9071\r\n"
}

// v1KeyLine is the static key line the relay wrote before #118.
func v1KeyLine(k proxyline.Key) string { return "AGENTBOX-RELAY " + k.Hex() + "\r\n" }

// relayHandshake runs the relay's half of the handshake on c against the
// listener at addr (the relay dials the port it forwards to, so the port is
// addr's).
func relayHandshake(c net.Conn, addr string, k proxyline.Key) error {
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		return err
	}
	return proxyline.RelayHandshake(c, k, ap.Port())
}

// rawConn dials addr and sends nothing. It has a deadline, so a listener that
// never answers fails the test instead of hanging it.
func rawConn(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(5 * time.Second))
	return c
}

// relayConn dials addr and runs the relay's handshake under k, failing the
// test if it does not complete. The PROXY line and the request follow.
func relayConn(t *testing.T, addr string, k proxyline.Key) net.Conn {
	t.Helper()
	c := rawConn(t, addr)
	if err := relayHandshake(c, addr, k); err != nil {
		t.Fatalf("relay handshake: %v", err)
	}
	return c
}

// wrongKeyConn dials addr and runs the handshake under wrongRelayKey, which
// must fail: the listener's proof does not verify under it, so the relay stops
// before it proves anything. The caller sends its bytes anyway.
func wrongKeyConn(t *testing.T, addr string) net.Conn {
	t.Helper()
	c := rawConn(t, addr)
	if err := relayHandshake(c, addr, wrongRelayKey); err == nil {
		t.Fatal("a relay on the wrong key passed the listener's proof")
	}
	return c
}

func TestListenDarwinLoopbackTCP(t *testing.T) {
	ep, err := Listen("darwin", t.TempDir(), 0, testRelayKey) // 0 = any free port, for the test only
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
	ep, err := Listen("darwin", t.TempDir(), 0, testRelayKey)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(r.RemoteAddr)) })}
	go srv.Serve(ep.Listener)
	t.Cleanup(func() { srv.Close(); ep.Listener.Close() })
	return ep.Listener.Addr().String()
}

// rawGet is one GET on c after head (the PROXY line, when the test sends one):
// the body, or the error when the connection was closed unanswered. It closes c.
func rawGet(t *testing.T, c net.Conn, head string) (string, error) {
	t.Helper()
	defer c.Close()
	fmt.Fprintf(c, "%sGET / HTTP/1.1\r\nHost: b\r\nConnection: close\r\n\r\n", head)
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
	body, err := rawGet(t, relayConn(t, addr, testRelayKey), proxyLine("172.29.72.9"))
	if err != nil || body != "172.29.72.9:40000" {
		t.Fatalf("with header: body %q err %v", body, err)
	}
	for name, head := range map[string]string{
		"key but no header": "",
		"gateway":           proxyLine("172.29.72.1"),
		"outside":           proxyLine("10.1.2.3"),
		"loopback":          proxyLine("127.0.0.1"),
		"garbage":           "PROXY nonsense\r\n",
		"unspecified":       proxyLine("0.0.0.0"),
	} {
		if body, err := rawGet(t, relayConn(t, addr, testRelayKey), head); err == nil {
			t.Errorf("%s: served %q", name, body)
		}
	}
}

// rawPost is rawGet for a POST with a bearer token: status and body, or the
// error when the connection was closed unanswered. It closes c.
func rawPost(t *testing.T, c net.Conn, head, path, tok, body string) (int, string, error) {
	t.Helper()
	defer c.Close()
	fmt.Fprintf(c, "%sPOST %s HTTP/1.1\r\nHost: b\r\nAuthorization: Bearer %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		head, path, tok, len(body), body)
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
	ep, err := listenDarwin(0, testRelayKey, 2, 100)
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
	got := make(chan string, 1)
	go func() {
		// The listener has not accepted this connection, so even the relay's
		// handshake waits for a slot: it runs here, with the wait for the reply.
		if err := relayHandshake(third, addr, testRelayKey); err != nil {
			got <- "error: " + err.Error()
			return
		}
		fmt.Fprint(third, proxyLine("172.29.72.9")+"GET / HTTP/1.1\r\nHost: b\r\nConnection: close\r\n\r\n")
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

// darwinBoundBroker serves a static-token broker over the real darwin
// listener, with session "s" bound to ownBox at 172.29.72.9. It returns the
// address to dial and the audit recorder.
func darwinBoundBroker(t *testing.T) (string, *memRec) {
	t.Helper()
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
	ep, err := listenDarwin(0, testRelayKey, maxDarwinConns, maxDarwinPerSource)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: b.Handler(), BaseContext: BaseContext, ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ep.Listener)
	t.Cleanup(func() { srv.Close(); ep.Listener.Close() })
	return ep.Listener.Addr().String(), rec
}

// macOS end to end over the real listener: the relay's PROXY source decides the
// box. The box's own address gets the value; any other address, a bound box's or
// an unbound one's, is refused as denied_box; no PROXY line is closed unanswered
// and writes no row.
func TestDarwinTokenBoundToItsBoxAddress(t *testing.T) {
	addr, rec := darwinBoundBroker(t)
	tok := tokA("s")

	code, body, err := rawPost(t, relayConn(t, addr, testRelayKey), proxyLine("172.29.72.9"), "/v1/static/token", tok, grafanaBody)
	if err != nil || code != 200 || !strings.Contains(body, staticSecret) {
		t.Fatalf("own address: %d %q %v", code, body, err)
	}
	code, body, err = rawPost(t, relayConn(t, addr, testRelayKey), proxyLine("172.29.72.10"), "/v1/static/token", tok, grafanaBody)
	if err != nil || code != 403 || strings.Contains(body, staticSecret) {
		t.Fatalf("another box's address: %d %q %v", code, body, err)
	}
	rec.mu.Lock()
	rows := append([]Row(nil), rec.rows...)
	rec.mu.Unlock()
	if len(rows) != 2 || rows[0].Outcome != OutcomeFetched || rows[1].Outcome != OutcomeDeniedBox || rows[1].SessionID != "s" {
		t.Fatalf("rows = %+v, want fetched then denied_box", rows)
	}
	if _, _, err := rawPost(t, relayConn(t, addr, testRelayKey), "", "/v1/static/token", tok, grafanaBody); err == nil {
		t.Fatal("a connection with the key and no PROXY line was answered")
	}
	rec.mu.Lock()
	n := len(rec.rows)
	rec.mu.Unlock()
	if n != 2 {
		t.Fatalf("a connection with no PROXY line wrote a row: %d rows", n)
	}
}

// #114, #118: the PROXY line is believed only after the relay proved the key. A
// process on the Mac that dials the loopback port and names the bound box's own
// address, with a valid token, gets no answer without the key, with the v1 key
// line the relay sent before #118, or with another key, writes no audit row, and
// the same request behind the right key is served.
func TestListenDarwinNeedsTheRelayKey(t *testing.T) {
	addr, rec := darwinBoundBroker(t)
	tok := tokA("s")
	own := proxyLine("172.29.72.9")

	for name, open := range map[string]func() (net.Conn, string){
		"no key":      func() (net.Conn, string) { return rawConn(t, addr), own },
		"v1 key line": func() (net.Conn, string) { return rawConn(t, addr), v1KeyLine(testRelayKey) + own },
		"wrong key":   func() (net.Conn, string) { return wrongKeyConn(t, addr), own },
	} {
		c, head := open()
		if code, body, err := rawPost(t, c, head, "/v1/static/token", tok, grafanaBody); err == nil {
			t.Errorf("%s: answered %d %q", name, code, body)
		}
	}
	rec.mu.Lock()
	n := len(rec.rows)
	rec.mu.Unlock()
	if n != 0 {
		t.Fatalf("a refused connection wrote %d audit rows", n)
	}

	code, body, err := rawPost(t, relayConn(t, addr, testRelayKey), own, "/v1/static/token", tok, grafanaBody)
	if err != nil || code != 200 || !strings.Contains(body, staticSecret) {
		t.Fatalf("right key: %d %q %v", code, body, err)
	}
	rec.mu.Lock()
	rows := append([]Row(nil), rec.rows...)
	rec.mu.Unlock()
	if len(rows) != 1 || rows[0].Outcome != OutcomeFetched {
		t.Fatalf("rows = %+v, want exactly one fetched row", rows)
	}
}

// With no relay key the Mac listener does not open at all (fail closed and loud:
// the alternative is trusting the PROXY line alone).
func TestListenDarwinRefusesAZeroKey(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()
	for name, open := range map[string]func() (Endpoint, error){
		"listenDarwin": func() (Endpoint, error) { return listenDarwin(port, proxyline.Key{}, 2, 2) },
		"Listen":       func() (Endpoint, error) { return Listen("darwin", t.TempDir(), port, proxyline.Key{}) },
	} {
		ep, err := open()
		if err == nil {
			ep.Listener.Close()
			t.Fatalf("%s: a zero key opened a listener", name)
		}
		if !strings.Contains(err.Error(), "relay key") {
			t.Errorf("%s: error %q does not name the relay key", name, err)
		}
		if c, derr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second); derr == nil {
			c.Close()
			t.Errorf("%s: something is listening on %d", name, port)
		}
	}
}

func TestMaxDarwinPerSourceIsBounded(t *testing.T) {
	if maxDarwinPerSource <= 0 || maxDarwinPerSource >= maxDarwinConns {
		t.Fatalf("maxDarwinPerSource = %d, maxDarwinConns = %d: one source must not be able to take them all", maxDarwinPerSource, maxDarwinConns)
	}
}

// keepAliveGet is one served request on a conn that stays open behind the relay
// handshake and a PROXY line for src, like a box's client holding its
// connection idle.
func keepAliveGet(t *testing.T, addr, src string) (net.Conn, string, error) {
	t.Helper()
	c := relayConn(t, addr, testRelayKey)
	c.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintf(c, "%sGET / HTTP/1.1\r\nHost: b\r\n\r\n", proxyLine(src))
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: "GET"})
	if err != nil {
		c.Close()
		return nil, "", err
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return c, string(b), nil
}

// One box, bound or not, must not be able to take the whole connection budget
// with idle connections and stall every other box's credential calls: a source
// holds at most its share, another source still gets in, and a slot comes back
// when its holder closes.
func TestListenDarwinOneSourceCannotCrowdOutAnother(t *testing.T) {
	ep, err := listenDarwin(0, testRelayKey, 256, 3)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(r.RemoteAddr)) })}
	go srv.Serve(ep.Listener)
	t.Cleanup(func() { srv.Close(); ep.Listener.Close() })
	addr := ep.Listener.Addr().String()

	var held []net.Conn
	for i := 0; i < 3; i++ {
		c, body, err := keepAliveGet(t, addr, "172.29.72.9")
		if err != nil || body != "172.29.72.9:40000" {
			t.Fatalf("conn %d from the source: %q %v", i, body, err)
		}
		held = append(held, c)
		t.Cleanup(func() { c.Close() })
	}
	if c, body, err := keepAliveGet(t, addr, "172.29.72.9"); err == nil {
		c.Close()
		t.Fatalf("a fourth connection from a source capped at three was served: %q", body)
	}
	other, body, err := keepAliveGet(t, addr, "172.29.72.10")
	if err != nil || body != "172.29.72.10:40000" {
		t.Fatalf("another source was crowded out: %q %v", body, err)
	}
	other.Close()

	held[0].Close() // its holder is gone; the server notices and frees the slot
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, body, err := keepAliveGet(t, addr, "172.29.72.9")
		if err == nil {
			c.Close()
			if body != "172.29.72.9:40000" {
				t.Fatalf("body %q", body)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("closing a connection never freed its source's slot: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
