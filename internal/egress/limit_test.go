package egress

import (
	"net"
	"testing"
)

// chanListener hands out conns pushed on ch; Close unblocks Accept. The
// limiter's own tests live in internal/netlimit; the service tests use this
// as a scripted listener.
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

func TestMaxProxyConnsIsBounded(t *testing.T) {
	if MaxProxyConns < defaultGlobal || MaxProxyConns > 4096 {
		t.Fatalf("MaxProxyConns = %d", MaxProxyConns)
	}
}
