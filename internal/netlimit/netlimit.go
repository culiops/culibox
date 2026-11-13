// Package netlimit caps how many connections a listener has open at once.
package netlimit

import (
	"errors"
	"net"
	"sync"
)

// Listener returns a listener that accepts at most n connections at a time;
// a slot is freed when the accepted connection is closed. Accept waits for a
// slot before it takes the next connection off the kernel's queue. (A local
// 30-line copy of golang.org/x/net/netutil.LimitListener, which is not a
// dependency of this module.)
func Listener(l net.Listener, n int) net.Listener {
	return ListenerWithNotice(l, n, nil)
}

// ListenerWithNotice is Listener that also calls notice, at most once for the
// life of the listener, the first time Accept finds every slot taken. Past that
// point a client waits in the kernel's queue and sees nothing, so the owner has
// to say it somewhere. notice runs on the accept goroutine: it must not block.
// A nil notice is Listener.
func ListenerWithNotice(l net.Listener, n int, notice func()) net.Listener {
	return &limitedListener{Listener: l, sem: make(chan struct{}, n), done: make(chan struct{}), notice: notice}
}

type limitedListener struct {
	net.Listener
	sem        chan struct{}
	done       chan struct{}
	closeOnce  sync.Once
	notice     func()
	noticeOnce sync.Once
}

func (l *limitedListener) Accept() (net.Conn, error) {
	select {
	case l.sem <- struct{}{}:
	default:
		if l.notice != nil {
			l.noticeOnce.Do(l.notice)
		}
		select {
		case l.sem <- struct{}{}:
		case <-l.done:
			return nil, net.ErrClosed
		}
	}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	return &limitedConn{Conn: c, release: func() { <-l.sem }}, nil
}

func (l *limitedListener) Close() error {
	err := l.Listener.Close()
	l.closeOnce.Do(func() { close(l.done) })
	return err
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

// CloseWrite forwards the half-close: the tunnel splice reaches the client conn
// through the hijacked (wrapped) value, and a client that has finished sending
// must not be cut off because the wrapper hid the method.
func (c *limitedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return errors.New("netlimit: connection does not support CloseWrite")
}
