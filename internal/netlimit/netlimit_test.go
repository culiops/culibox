package netlimit

import (
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// closeWriteConn is a net.Conn that records CloseWrite and Close.
type closeWriteConn struct {
	net.Conn
	cw, closes atomic.Int32
}

func (c *closeWriteConn) CloseWrite() error { c.cw.Add(1); return nil }
func (c *closeWriteConn) Close() error      { c.closes.Add(1); return c.Conn.Close() }

// chanListener hands out conns pushed on ch; Close unblocks Accept.
type chanListener struct {
	ch   chan net.Conn
	done chan struct{}
}

func newChanListener() *chanListener {
	return &chanListener{ch: make(chan net.Conn, 16), done: make(chan struct{})}
}
func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *chanListener) Close() error   { close(l.done); return nil }
func (l *chanListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1} }

func pipeConn() (*closeWriteConn, net.Conn) {
	a, b := net.Pipe()
	return &closeWriteConn{Conn: a}, b
}

func TestListenerCapsConcurrentConns(t *testing.T) {
	inner := newChanListener()
	l := Listener(inner, 2)
	var peers []net.Conn
	for i := 0; i < 3; i++ {
		c, p := pipeConn()
		peers = append(peers, p)
		inner.ch <- c
	}
	c1, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Accept(); err != nil {
		t.Fatal(err)
	}
	third := make(chan net.Conn, 1)
	go func() { c, _ := l.Accept(); third <- c }()
	select {
	case <-third:
		t.Fatal("third Accept returned while two conns were open: the cap is not enforced")
	case <-time.After(100 * time.Millisecond):
	}
	c1.Close()
	c1.Close() // a double Close must release only once
	select {
	case c := <-third:
		if c == nil {
			t.Fatal("third Accept returned nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closing a conn did not free a slot")
	}
	// The double Close above must not have freed a second slot.
	fourth := make(chan struct{}, 1)
	inner.ch <- func() net.Conn { c, p := pipeConn(); peers = append(peers, p); return c }()
	go func() { l.Accept(); fourth <- struct{}{} }()
	select {
	case <-fourth:
		t.Fatal("a double Close released two slots")
	case <-time.After(100 * time.Millisecond):
	}
	l.Close()
	for _, p := range peers {
		p.Close()
	}
}

func TestListenerCloseUnblocksWaitingAccept(t *testing.T) {
	inner := newChanListener()
	l := Listener(inner, 1)
	c, p := pipeConn()
	defer p.Close()
	inner.ch <- c
	if _, err := l.Accept(); err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { _, err := l.Accept(); errc <- err }()
	time.Sleep(50 * time.Millisecond)
	l.Close()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("Accept returned a conn after Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock an Accept waiting for a slot")
	}
}

// The tunnel's half-close goes through the conn http.Server hijacks, which is
// the wrapper; without CloseWrite a client that finished sending would be cut.
func TestListenerConnForwardsCloseWrite(t *testing.T) {
	inner := newChanListener()
	l := Listener(inner, 1)
	c, p := pipeConn()
	defer p.Close()
	inner.ch <- c
	got, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	cw, ok := got.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("limited conn lost CloseWrite")
	}
	if err := cw.CloseWrite(); err != nil || c.cw.Load() != 1 {
		t.Fatalf("CloseWrite not forwarded: err=%v n=%d", err, c.cw.Load())
	}
	if c.closes.Load() != 0 {
		t.Fatal("CloseWrite closed the conn")
	}
}

func TestListenerConnCloseWriteFallsBackToErrorWhenUnsupported(t *testing.T) {
	a, b := net.Pipe() // net.Pipe has no CloseWrite
	defer b.Close()
	inner := newChanListener()
	l := Listener(inner, 1)
	inner.ch <- a
	got, _ := l.Accept()
	if err := got.(interface{ CloseWrite() error }).CloseWrite(); err == nil {
		t.Fatal("CloseWrite on a conn without it must fail so the caller falls back to Close")
	}
}

// addrConn is a conn whose RemoteAddr is chosen by the test, standing for the
// source a PROXY header names. asked counts RemoteAddr calls.
type addrConn struct {
	net.Conn
	addr  string
	asked atomic.Int32
}

func (c *addrConn) RemoteAddr() net.Addr {
	c.asked.Add(1)
	return fixedAddr(c.addr)
}

type fixedAddr string

func (a fixedAddr) Network() string { return "tcp" }
func (a fixedAddr) String() string  { return string(a) }

// pipeFrom is a server-side conn that claims to come from addr, and its peer.
func pipeFrom(addr string) (*addrConn, net.Conn) {
	a, b := net.Pipe()
	return &addrConn{Conn: a, addr: addr}, b
}

// accepted pushes one conn from addr through l and returns it with its peer.
func accepted(t *testing.T, inner *chanListener, l net.Listener, addr string) (net.Conn, net.Conn) {
	t.Helper()
	c, peer := pipeFrom(addr)
	inner.ch <- c
	got, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	return got, peer
}

// admitted reports whether c was let in: a refused conn is closed (its peer
// sees EOF) and its reads fail.
func admitted(c, peer net.Conn) bool {
	c.RemoteAddr() // the first use of a conn is where its source is learned
	peer.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, err := peer.Read(make([]byte, 1))
	return err != io.EOF && !errors.Is(err, io.ErrClosedPipe)
}

func TestPerSourceCapsOneAddressAndLetsAnotherIn(t *testing.T) {
	inner := newChanListener()
	l := PerSource(inner, 2)
	var held []net.Conn
	for i := 0; i < 2; i++ {
		c, peer := accepted(t, inner, l, "172.29.72.9:4000"+string(rune('0'+i)))
		defer peer.Close()
		if !admitted(c, peer) {
			t.Fatalf("conn %d from the source was refused under the cap", i)
		}
		held = append(held, c)
	}
	third, peer3 := accepted(t, inner, l, "172.29.72.9:40009")
	defer peer3.Close()
	if admitted(third, peer3) {
		t.Fatal("a third conn from a source capped at two was let in")
	}
	if _, err := third.Read(make([]byte, 1)); err == nil {
		t.Fatal("a refused conn can still be read")
	}
	other, peerO := accepted(t, inner, l, "172.29.72.10:40000")
	defer peerO.Close()
	if !admitted(other, peerO) {
		t.Fatal("another source was refused because the first is full")
	}
	// Closing one of the first source's conns frees a slot; a double Close
	// frees exactly one.
	held[0].Close()
	held[0].Close()
	fourth, peer4 := accepted(t, inner, l, "172.29.72.9:40010")
	defer peer4.Close()
	if !admitted(fourth, peer4) {
		t.Fatal("closing a conn did not free its source's slot")
	}
	fifth, peer5 := accepted(t, inner, l, "172.29.72.9:40011")
	defer peer5.Close()
	if admitted(fifth, peer5) {
		t.Fatal("a double Close freed two slots")
	}
}

// A conn is counted once, however often it is asked its address or read, and a
// refused conn never holds a slot.
func TestPerSourceCountsAConnOnceAndReleasesARefusedOne(t *testing.T) {
	inner := newChanListener()
	l := PerSource(inner, 1)
	first, peer1 := accepted(t, inner, l, "172.29.72.9:40000")
	defer peer1.Close()
	for i := 0; i < 5; i++ {
		first.RemoteAddr()
	}
	second, peer2 := accepted(t, inner, l, "172.29.72.9:40001")
	defer peer2.Close()
	if admitted(second, peer2) {
		t.Fatal("the first conn was counted more than once, or the cap is not enforced")
	}
	second.Close() // the refused conn: closing it must not free the first conn's slot
	third, peer3 := accepted(t, inner, l, "172.29.72.9:40002")
	defer peer3.Close()
	if admitted(third, peer3) {
		t.Fatal("closing a refused conn freed a slot it never held")
	}
	first.Close()
	fourth, peer4 := accepted(t, inner, l, "172.29.72.9:40003")
	defer peer4.Close()
	if !admitted(fourth, peer4) {
		t.Fatal("the slot was not freed when its holder closed")
	}
}

// A conn closed before it was ever used (a client that connects and leaves)
// never took a slot.
func TestPerSourceConnClosedBeforeUseHoldsNothing(t *testing.T) {
	inner := newChanListener()
	l := PerSource(inner, 1)
	gone, peer := accepted(t, inner, l, "172.29.72.9:40000")
	peer.Close()
	gone.Close()
	gone.RemoteAddr() // a late look must not count a conn that is already closed
	next, peerN := accepted(t, inner, l, "172.29.72.9:40001")
	defer peerN.Close()
	if !admitted(next, peerN) {
		t.Fatal("a conn that closed before use still held its source's slot")
	}
}

// Fail closed: a conn whose source cannot be read is refused, not counted under
// some default.
func TestPerSourceRefusesAnUnreadableSource(t *testing.T) {
	inner := newChanListener()
	l := PerSource(inner, 5)
	c, peer := accepted(t, inner, l, "not an address")
	defer peer.Close()
	if admitted(c, peer) {
		t.Fatal("a conn with no readable source was let in")
	}
}

func TestPerSourceCloseUnblocksAccept(t *testing.T) {
	inner := newChanListener()
	l := PerSource(inner, 1)
	errc := make(chan error, 1)
	go func() { _, err := l.Accept(); errc <- err }()
	time.Sleep(50 * time.Millisecond)
	l.Close()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("Accept returned a conn after Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock Accept")
	}
}

// cwAddrConn is a conn with a chosen source that records CloseWrite.
type cwAddrConn struct {
	*closeWriteConn
	addr string
}

func (c *cwAddrConn) RemoteAddr() net.Addr { return fixedAddr(c.addr) }

// net/http sends a FIN and waits (closeWriteAndWait) before it closes a
// connection after an early error reply (413, 431, 400), so the client reads the
// reply instead of a reset. It finds CloseWrite by type assertion, so a wrapper
// that hides it silently drops the FIN.
func TestPerSourceConnForwardsCloseWrite(t *testing.T) {
	inner := newChanListener()
	l := PerSource(inner, 1)
	c, peer := pipeConn()
	defer peer.Close()
	inner.ch <- &cwAddrConn{closeWriteConn: c, addr: "172.29.72.9:40000"}
	got, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	cw, ok := got.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("the per-source wrapper hides CloseWrite")
	}
	if err := cw.CloseWrite(); err != nil || c.cw.Load() != 1 {
		t.Fatalf("CloseWrite not forwarded: err=%v n=%d", err, c.cw.Load())
	}
	if c.closes.Load() != 0 {
		t.Fatal("CloseWrite closed the conn")
	}

	// A conn that was refused is closed and gets no half-close.
	c2, peer2 := pipeConn()
	defer peer2.Close()
	inner.ch <- &cwAddrConn{closeWriteConn: c2, addr: "172.29.72.9:40001"}
	refused, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	if err := refused.(interface{ CloseWrite() error }).CloseWrite(); err == nil || c2.cw.Load() != 0 {
		t.Fatalf("a refused conn was given a half-close: err=%v n=%d", err, c2.cw.Load())
	}
}

func TestPerSourceConnCloseWriteErrorsWhenUnsupported(t *testing.T) {
	inner := newChanListener()
	l := PerSource(inner, 1)
	a, b := net.Pipe() // no CloseWrite
	defer b.Close()
	inner.ch <- &addrConn{Conn: a, addr: "172.29.72.9:40000"}
	got, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	if err := got.(interface{ CloseWrite() error }).CloseWrite(); err == nil {
		t.Fatal("CloseWrite on a conn without it must fail so the caller falls back to Close")
	}
}
