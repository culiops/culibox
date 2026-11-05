package netlimit

import (
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
