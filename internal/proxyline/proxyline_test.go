package proxyline

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const goodHeader = "PROXY TCP4 172.29.72.5 172.29.72.1 40000 9072\r\n"

func allowAll(netip.Addr) bool { return true }

// testKey is the relay key every test listener wants; otherKey is a wrong one.
var (
	testKey  = keyOf(0x11)
	otherKey = keyOf(0x22)
)

// relayWrite plays the relay on c in the background: RelayHandshake under
// testKey, then h (the PROXY line and whatever follows it) in one write. The
// port is the one c dialed; a pipe has none, so it falls back to 1, the port
// fakeConn reports as its own. The channel carries the first error, or nil. c
// gets a deadline first, so a listener that never answers fails the test
// instead of hanging it.
func relayWrite(t *testing.T, c net.Conn, h string) <-chan error {
	t.Helper()
	port := uint16(1)
	if ap, err := netip.ParseAddrPort(c.RemoteAddr().String()); err == nil {
		port = ap.Port()
	}
	c.SetDeadline(time.Now().Add(10 * time.Second))
	out := make(chan error, 1)
	go func() {
		if err := RelayHandshake(c, testKey, port); err != nil {
			out <- err
			return
		}
		var err error
		if h != "" {
			_, err = io.WriteString(c, h)
		}
		out <- err
	}()
	return out
}

func TestParseV1Valid(t *testing.T) {
	src, dst, err := ParseV1([]byte(goodHeader))
	if err != nil {
		t.Fatalf("ParseV1: %v", err)
	}
	if src.String() != "172.29.72.5:40000" {
		t.Errorf("src = %s", src)
	}
	if dst.String() != "172.29.72.1:9072" {
		t.Errorf("dst = %s", dst)
	}
}

func TestParseV1Rejects(t *testing.T) {
	// 107 bytes is the maximum; build a 108-byte but otherwise well-formed
	// line by padding the destination port field is not possible, so pad
	// with a trailing run that still splits into six fields.
	long := "PROXY TCP4 172.29.72.5 172.29.72.1 40000 " + strings.Repeat("0", 108-len("PROXY TCP4 172.29.72.5 172.29.72.1 40000 ")-2) + "\r\n"
	if len(long) != 108 {
		t.Fatalf("test bug: long line is %d bytes", len(long))
	}
	cases := map[string]string{
		"missing CRLF":    "PROXY TCP4 172.29.72.5 172.29.72.1 40000 9072",
		"LF only":         "PROXY TCP4 172.29.72.5 172.29.72.1 40000 9072\n",
		"TCP6":            "PROXY TCP6 172.29.72.5 172.29.72.1 40000 9072\r\n",
		"UNKNOWN":         "PROXY UNKNOWN\r\n",
		"leading zeros":   "PROXY TCP4 172.029.72.5 172.29.72.1 40000 9072\r\n",
		"port zero":       "PROXY TCP4 172.29.72.5 172.29.72.1 0 9072\r\n",
		"dst port zero":   "PROXY TCP4 172.29.72.5 172.29.72.1 40000 0\r\n",
		"port 65536":      "PROXY TCP4 172.29.72.5 172.29.72.1 65536 9072\r\n",
		"port leading 0":  "PROXY TCP4 172.29.72.5 172.29.72.1 040000 9072\r\n",
		"extra field":     "PROXY TCP4 172.29.72.5 172.29.72.1 40000 9072 x\r\n",
		"double space":    "PROXY TCP4  172.29.72.5 172.29.72.1 40000 9072\r\n",
		"108 bytes":       long,
		"IPv6 address":    "PROXY TCP4 ::1 172.29.72.1 40000 9072\r\n",
		"IPv4-in-IPv6":    "PROXY TCP4 ::ffff:172.29.72.5 172.29.72.1 40000 9072\r\n",
		"lowercase proxy": "proxy TCP4 172.29.72.5 172.29.72.1 40000 9072\r\n",
		"empty":           "",
		"embedded CR":     "PROXY TCP4 172.29.72.5 172.29.72.1 40000 9\r72\r\n",
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := ParseV1([]byte(line))
			if !errors.Is(err, ErrHeader) {
				t.Fatalf("err = %v, want ErrHeader", err)
			}
		})
	}
}

func TestParseV1AcceptsMaxLen(t *testing.T) {
	// The longest legal TCP4 line is well under 107; just confirm a
	// maximal-width line parses.
	line := "PROXY TCP4 255.255.255.255 255.255.255.255 65535 65535\r\n"
	if len(line) > maxLine {
		t.Fatalf("test bug: %d > %d", len(line), maxLine)
	}
	if _, _, err := ParseV1([]byte(line)); err != nil {
		t.Fatal(err)
	}
}

// listen starts a wrapped loopback listener that wants testKey.
func listen(t *testing.T, allow func(netip.Addr) bool, timeout time.Duration) net.Listener {
	t.Helper()
	return listenOpts(t, Options{Key: testKey, Allow: allow, Timeout: timeout})
}

// listenOpts is listen with the whole Options.
func listenOpts(t *testing.T, o Options) net.Listener {
	t.Helper()
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := Wrap(raw, o)
	t.Cleanup(func() { l.Close() })
	return l
}

// refusals counts KeyRefused calls.
type refusals struct{ n atomic.Int32 }

func (r *refusals) opts(key Key, timeout time.Duration) Options {
	return Options{Key: key, Allow: allowAll, Timeout: timeout, KeyRefused: func() { r.n.Add(1) }}
}

func dial(t *testing.T, l net.Listener) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func accept(t *testing.T, l net.Listener) net.Conn {
	t.Helper()
	c, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestWrapSetsRemoteAddrFromHeader(t *testing.T) {
	l := listen(t, allowAll, 5*time.Second)
	cl := dial(t, l)
	relayWrite(t, cl, goodHeader+"hello")
	sc := accept(t, l)
	if got := sc.RemoteAddr().String(); got != "172.29.72.5:40000" {
		t.Fatalf("RemoteAddr = %s", got)
	}
	if _, ok := sc.RemoteAddr().(*net.TCPAddr); !ok {
		t.Fatalf("RemoteAddr type = %T, want *net.TCPAddr", sc.RemoteAddr())
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(sc, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello" {
		t.Fatalf("payload = %q", buf)
	}
}

func TestWrapDeliversBytesAfterHeader(t *testing.T) {
	l := listen(t, allowAll, 5*time.Second)
	cl := dial(t, l)
	payload := make([]byte, 4096)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	relayWrite(t, cl, goodHeader+string(payload))
	sc := accept(t, l)
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(sc, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatal("payload mismatch")
	}
}

func TestWrapRejectsMissingHeader(t *testing.T) {
	// The relay proved the key and the PROXY line is missing: a header refusal,
	// not a key refusal, so KeyRefused must stay silent.
	var r refusals
	l := listenOpts(t, r.opts(testKey, 5*time.Second))
	cl := dial(t, l)
	relayWrite(t, cl, "CONNECT x:443 HTTP/1.1\r\n\r\n")
	sc := accept(t, l)
	_, err := sc.Read(make([]byte, 16))
	if !errors.Is(err, ErrHeader) {
		t.Fatalf("Read err = %v, want ErrHeader", err)
	}
	// Failed header: RemoteAddr is the real (loopback) peer, not an attacker-chosen one.
	if ap, perr := netip.ParseAddrPort(sc.RemoteAddr().String()); perr != nil || !ap.Addr().IsLoopback() {
		t.Fatalf("RemoteAddr after failure = %v", sc.RemoteAddr())
	}
	// Subsequent reads keep failing.
	if _, err := sc.Read(make([]byte, 1)); !errors.Is(err, ErrHeader) {
		t.Fatalf("second Read err = %v, want ErrHeader", err)
	}
	cl.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := cl.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("client read err = %v, want EOF", err)
	}
	if got := r.n.Load(); got != 0 {
		t.Fatalf("KeyRefused called %d times for a connection that proved the key", got)
	}
}

func TestWrapRejectsDisallowedSource(t *testing.T) {
	// The relay proved the key and the source is not allowed: not a key refusal.
	var r refusals
	o := r.opts(testKey, 5*time.Second)
	o.Allow = func(netip.Addr) bool { return false }
	l := listenOpts(t, o)
	cl := dial(t, l)
	relayWrite(t, cl, goodHeader+"x")
	sc := accept(t, l)
	if _, err := sc.Read(make([]byte, 1)); !errors.Is(err, ErrHeader) {
		t.Fatalf("Read err = %v, want ErrHeader", err)
	}
	if got := r.n.Load(); got != 0 {
		t.Fatalf("KeyRefused called %d times for a connection that proved the key", got)
	}
}

func TestWrapAllowReceivesHeaderSource(t *testing.T) {
	var seen netip.Addr
	l := listen(t, func(a netip.Addr) bool { seen = a; return true }, 5*time.Second)
	cl := dial(t, l)
	relayWrite(t, cl, goodHeader)
	sc := accept(t, l)
	_ = sc.RemoteAddr() // triggers the header read
	if seen.String() != "172.29.72.5" {
		t.Fatalf("allow saw %v", seen)
	}
}

func TestWrapNilAllowFailsClosed(t *testing.T) {
	l := listen(t, nil, 5*time.Second)
	cl := dial(t, l)
	relayWrite(t, cl, goodHeader)
	sc := accept(t, l)
	if _, err := sc.Read(make([]byte, 1)); !errors.Is(err, ErrHeader) {
		t.Fatalf("Read err = %v, want ErrHeader", err)
	}
}

func TestWrapTimeout(t *testing.T) {
	l := listen(t, allowAll, 100*time.Millisecond)
	_ = dial(t, l) // connects, sends nothing
	sc := accept(t, l)
	done := make(chan error, 1)
	go func() {
		_, err := sc.Read(make([]byte, 1))
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrHeader) {
			t.Fatalf("err = %v, want ErrHeader", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Read did not fail within 1s")
	}
}

func TestWrapSlowClientDoesNotBlockAccept(t *testing.T) {
	l := listen(t, allowAll, 5*time.Second)
	a := dial(t, l) // A: a good hello, then nothing
	if _, err := io.WriteString(a, goodHello(nonceOf(0x31))); err != nil {
		t.Fatal(err)
	}
	b := dial(t, l)
	relayWrite(t, b, goodHeader)
	start := time.Now()
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 2)
	go func() {
		for i := 0; i < 2; i++ {
			c, err := l.Accept()
			ch <- res{c, err}
		}
	}()
	var conns []net.Conn
	for i := 0; i < 2; i++ {
		select {
		case r := <-ch:
			if r.err != nil {
				t.Fatal(r.err)
			}
			conns = append(conns, r.c)
			t.Cleanup(func() { r.c.Close() })
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("Accept #%d blocked (elapsed %v)", i+1, time.Since(start))
		}
	}
	// Accept order is connect order: conns[1] is B, whose header is valid.
	if got := conns[1].RemoteAddr().String(); got != "172.29.72.5:40000" {
		t.Fatalf("B RemoteAddr = %s", got)
	}
}

// fakeConn reports chosen peer and local addresses. A nil local is what a
// conn with no address of its own reports.
type fakeConn struct {
	net.Conn
	remote net.Addr
	local  net.Addr
}

func (f fakeConn) RemoteAddr() net.Addr { return f.remote }
func (f fakeConn) LocalAddr() net.Addr  { return f.local }

// pipeLocal is the local address the pipe tests give their fakeConn: the port
// relayWrite falls back to.
var pipeLocal = &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}

type fakeListener struct {
	conn net.Conn
	done bool
}

func (f *fakeListener) Accept() (net.Conn, error) {
	if f.done {
		return nil, net.ErrClosed
	}
	f.done = true
	return f.conn, nil
}
func (f *fakeListener) Close() error   { return nil }
func (f *fakeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1} }

func TestWrapNonLoopbackPeerRefused(t *testing.T) {
	cs, ss := net.Pipe()
	defer cs.Close()
	// The peer says hello and then listens: whatever the listener wrote would
	// reach it.
	go cs.Write([]byte(goodHello(nonceOf(0x31))))
	fl := &fakeListener{conn: fakeConn{Conn: ss, remote: &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 1234}, local: pipeLocal}}
	var refused refusals
	l := Wrap(fl, refused.opts(testKey, 5*time.Second))
	c, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, ErrHeader) {
		t.Fatalf("Read err = %v, want ErrHeader", err)
	}
	if got := c.RemoteAddr().String(); got != "10.0.0.1:1234" {
		t.Fatalf("RemoteAddr = %s, want the real peer", got)
	}
	// The peer was refused before the key was looked at, and heard nothing.
	if refused.n.Load() != 0 {
		t.Fatalf("KeyRefused called %d times for a non-loopback peer", refused.n.Load())
	}
	cs.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, err := cs.Read(make([]byte, 16)); n != 0 || err != io.EOF {
		t.Fatalf("the non-loopback peer received %d bytes (err %v), want none and EOF", n, err)
	}
}

func TestWrapAddrAndCloseDelegate(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := Wrap(raw, Options{Key: testKey, Allow: allowAll, Timeout: time.Second})
	if l.Addr().String() != raw.Addr().String() {
		t.Fatal("Addr not delegated")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Accept(); err == nil {
		t.Fatal("Accept after Close succeeded")
	}
}

func TestWrapKeepsCallerReadDeadline(t *testing.T) {
	l := listen(t, allowAll, 5*time.Second)
	cl := dial(t, l)
	relayWrite(t, cl, goodHeader) // handshake and header only, then silence
	sc := accept(t, l)
	if err := sc.SetReadDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := sc.Read(make([]byte, 1))
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("err = %v, want timeout", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("caller deadline not honoured")
	}
}

func TestWrapWriteBeforeHeaderFails(t *testing.T) {
	l := listen(t, allowAll, 100*time.Millisecond)
	cl := dial(t, l) // sends no header
	sc := accept(t, l)
	n, err := sc.Write([]byte("secret"))
	if !errors.Is(err, ErrHeader) || n != 0 {
		t.Fatalf("Write = %d, %v; want 0, ErrHeader", n, err)
	}
	cl.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, rerr := io.ReadAll(cl)
	if rerr != nil || len(got) != 0 {
		t.Fatalf("client received %q, err %v; want nothing", got, rerr)
	}
}

func TestWrapForwardsCloseWrite(t *testing.T) {
	l := listen(t, allowAll, 5*time.Second)
	cl := dial(t, l)
	relayWrite(t, cl, goodHeader)
	sc := accept(t, l)
	cw, ok := sc.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("conn does not expose CloseWrite")
	}
	if err := cw.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	cl.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := cl.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("client read err = %v, want EOF", err)
	}
	// Server can still read what the client sends after the half-close.
	if _, err := cl.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(sc, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("server read %q, %v", buf, err)
	}
}

func TestWrapCloseWriteUnsupportedInner(t *testing.T) {
	cs, ss := net.Pipe()
	defer cs.Close()
	relayWrite(t, cs, goodHeader)
	l := Wrap(&fakeListener{conn: fakeConn{Conn: ss, remote: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9}, local: pipeLocal}}, Options{Key: testKey, Allow: allowAll, Timeout: time.Second})
	c, _ := l.Accept()
	defer c.Close()
	if err := c.(interface{ CloseWrite() error }).CloseWrite(); err == nil {
		t.Fatal("want error from inner conn without CloseWrite")
	}
}

func TestWrapAllowPanicFailsClosed(t *testing.T) {
	l := listen(t, func(netip.Addr) bool { panic("boom") }, 5*time.Second)
	cl := dial(t, l)
	relayWrite(t, cl, goodHeader)
	sc := accept(t, l)
	func() {
		defer func() { recover() }()
		sc.Read(make([]byte, 1))
	}()
	if _, err := sc.Read(make([]byte, 1)); !errors.Is(err, ErrHeader) {
		t.Fatalf("Read after panic = %v, want ErrHeader", err)
	}
	if _, err := sc.Write([]byte("x")); !errors.Is(err, ErrHeader) {
		t.Fatalf("Write after panic = %v, want ErrHeader", err)
	}
	cl.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := cl.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("client err = %v, want EOF (conn closed)", err)
	}
}

func TestWrapTimeoutKeepsNetErrorTimeout(t *testing.T) {
	l := listen(t, allowAll, 100*time.Millisecond)
	_ = dial(t, l)
	sc := accept(t, l)
	_, err := sc.Read(make([]byte, 1))
	var ne net.Error
	if !errors.Is(err, ErrHeader) || !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("err = %v, want ErrHeader that is also a net.Error timeout", err)
	}
}

// seesNothing fails unless the client's side of a refused connection is closed
// without a single byte having come back (a reset counts as closed: the server
// may close with unread input).
func seesNothing(t *testing.T, cl net.Conn) {
	t.Helper()
	if got := readUntilClosed(t, cl); len(got) != 0 {
		t.Fatalf("a refused connection got %d bytes back: %q", len(got), got)
	}
}

// readUntilClosed returns every byte the listener sends until it closes its
// end; a reset ends the read as an EOF does, since a listener that closes with
// unread input may send one.
func readUntilClosed(t *testing.T, cl net.Conn) []byte {
	t.Helper()
	cl.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := io.ReadAll(cl)
	if err != nil && !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("client read err = %v, want EOF or a reset", err)
	}
	return got
}

// readRefused reads sc and requires the handshake's ErrHeader. The read has a
// deadline of its own (held until the handshake is over), so a listener that
// wrongly accepts fails the test instead of hanging it.
func readRefused(t *testing.T, sc net.Conn) error {
	t.Helper()
	sc.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, err := sc.Read(make([]byte, 16))
	if !errors.Is(err, ErrHeader) {
		t.Fatalf("Read err = %v, want ErrHeader", err)
	}
	return err
}

func TestInSubnetNotGateway(t *testing.T) {
	sub := netip.MustParsePrefix("172.29.72.0/24")
	gw := netip.MustParseAddr("172.29.72.1")
	cases := []struct {
		name   string
		subnet netip.Prefix
		gw     netip.Addr
		addr   string
		want   bool
	}{
		{"box address", sub, gw, "172.29.72.5", true},
		{"gateway", sub, gw, "172.29.72.1", false},
		{"outside subnet", sub, gw, "172.29.73.5", false},
		{"loopback", sub, gw, "127.0.0.1", false},
		{"v4-mapped box address", sub, gw, "::ffff:172.29.72.5", true},
		{"v4-mapped gateway", sub, gw, "::ffff:172.29.72.1", false},
		{"v4-mapped outside", sub, gw, "::ffff:10.0.0.1", false},
		{"invalid address", sub, gw, "", false},
		{"zero-bit subnet", netip.MustParsePrefix("0.0.0.0/0"), gw, "172.29.72.5", false},
		{"invalid subnet", netip.Prefix{}, gw, "172.29.72.5", false},
		{"invalid gateway", sub, netip.Addr{}, "172.29.72.5", false},
		{"unspecified gateway", sub, netip.MustParseAddr("0.0.0.0"), "172.29.72.5", false},
		{"gateway outside subnet", sub, netip.MustParseAddr("10.0.0.1"), "172.29.72.5", false},
		{"v4-mapped gateway form is unmapped", sub, netip.MustParseAddr("::ffff:172.29.72.1"), "172.29.72.1", false},
	}
	for _, c := range cases {
		var a netip.Addr
		if c.addr != "" {
			a = netip.MustParseAddr(c.addr)
		}
		if got := InSubnetNotGateway(c.subnet, c.gw)(a); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// TestExplainBindError: a bind that lost the port to another process says so
// (and that the process gets nothing from the boxes, and how to find it); any
// other error passes through untouched.
func TestExplainBindError(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	port := held.Addr().(*net.TCPAddr).Port
	_, bindErr := net.Listen("tcp", held.Addr().String())
	if !errors.Is(bindErr, syscall.EADDRINUSE) {
		t.Fatalf("setup: second bind gave %v, want EADDRINUSE", bindErr)
	}
	got := ExplainBindError(port, bindErr)
	if !errors.Is(got, syscall.EADDRINUSE) {
		t.Errorf("explained error lost the cause: %v", got)
	}
	for _, want := range []string{
		fmt.Sprintf("another process holds 127.0.0.1:%d", port),
		"gets nothing from the boxes",
		fmt.Sprintf("sudo lsof -nP -iTCP:%d -sTCP:LISTEN", port),
		bindErr.Error(),
	} {
		if !strings.Contains(got.Error(), want) {
			t.Errorf("explained error lacks %q: %v", want, got)
		}
	}
	if got := ExplainBindError(1, io.EOF); got != io.EOF {
		t.Errorf("a non-bind error changed: %v", got)
	}
	if got := ExplainBindError(1, nil); got != nil {
		t.Errorf("nil became %v", got)
	}
}
