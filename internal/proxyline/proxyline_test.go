package proxyline

import (
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

const goodHeader = "PROXY TCP4 172.29.72.5 172.29.72.1 40000 9072\r\n"

func allowAll(netip.Addr) bool { return true }

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

// listen starts a wrapped loopback listener.
func listen(t *testing.T, allow func(netip.Addr) bool, timeout time.Duration) net.Listener {
	t.Helper()
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := Wrap(raw, allow, timeout)
	t.Cleanup(func() { l.Close() })
	return l
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
	if _, err := cl.Write([]byte(goodHeader + "hello")); err != nil {
		t.Fatal(err)
	}
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
	if _, err := cl.Write(append([]byte(goodHeader), payload...)); err != nil {
		t.Fatal(err)
	}
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
	l := listen(t, allowAll, 5*time.Second)
	cl := dial(t, l)
	if _, err := cl.Write([]byte("CONNECT x:443 HTTP/1.1\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
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
}

func TestWrapRejectsDisallowedSource(t *testing.T) {
	l := listen(t, func(netip.Addr) bool { return false }, 5*time.Second)
	cl := dial(t, l)
	if _, err := cl.Write([]byte(goodHeader + "x")); err != nil {
		t.Fatal(err)
	}
	sc := accept(t, l)
	if _, err := sc.Read(make([]byte, 1)); !errors.Is(err, ErrHeader) {
		t.Fatalf("Read err = %v, want ErrHeader", err)
	}
}

func TestWrapAllowReceivesHeaderSource(t *testing.T) {
	var seen netip.Addr
	l := listen(t, func(a netip.Addr) bool { seen = a; return true }, 5*time.Second)
	cl := dial(t, l)
	cl.Write([]byte(goodHeader))
	sc := accept(t, l)
	_ = sc.RemoteAddr() // triggers the header read
	if seen.String() != "172.29.72.5" {
		t.Fatalf("allow saw %v", seen)
	}
}

func TestWrapNilAllowFailsClosed(t *testing.T) {
	l := listen(t, nil, 5*time.Second)
	cl := dial(t, l)
	cl.Write([]byte(goodHeader))
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
	_ = dial(t, l) // A: silent
	b := dial(t, l)
	if _, err := b.Write([]byte(goodHeader)); err != nil {
		t.Fatal(err)
	}
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

// fakeConn reports a non-loopback peer address.
type fakeConn struct {
	net.Conn
	remote net.Addr
}

func (f fakeConn) RemoteAddr() net.Addr { return f.remote }

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
	go func() { cs.Write([]byte(goodHeader + "x")) }()
	fl := &fakeListener{conn: fakeConn{Conn: ss, remote: &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 1234}}}
	l := Wrap(fl, allowAll, 5*time.Second)
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
}

func TestWrapAddrAndCloseDelegate(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := Wrap(raw, allowAll, time.Second)
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
	cl.Write([]byte(goodHeader)) // header only, then silence
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
	cl.Write([]byte(goodHeader))
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
	go func() { cs.Write([]byte(goodHeader)) }()
	l := Wrap(&fakeListener{conn: fakeConn{Conn: ss, remote: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9}}}, allowAll, time.Second)
	c, _ := l.Accept()
	defer c.Close()
	if err := c.(interface{ CloseWrite() error }).CloseWrite(); err == nil {
		t.Fatal("want error from inner conn without CloseWrite")
	}
}

func TestWrapAllowPanicFailsClosed(t *testing.T) {
	l := listen(t, func(netip.Addr) bool { panic("boom") }, 5*time.Second)
	cl := dial(t, l)
	cl.Write([]byte(goodHeader))
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
