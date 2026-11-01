// Package proxyline wraps a net.Listener so that every accepted connection
// must begin with a strict PROXY protocol v1 (TCP4) header, and reports the
// header's source as RemoteAddr.
//
// It exists for macOS, where boxes reach host services through a relay in the
// colima VM and therefore arrive from 127.0.0.1. The relay prepends the
// header; the host listener trusts it only from a loopback peer and only when
// the caller's allow function accepts the claimed source. Every failure
// closes the connection (fail closed).
package proxyline

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrHeader is returned (possibly wrapped) when the PROXY header is missing,
// malformed, from a disallowed source, or sent by a non-loopback peer.
var ErrHeader = errors.New("proxyline: missing or invalid PROXY header")

// maxLine is the longest legal PROXY v1 line including the trailing CRLF
// (107 bytes per the protocol specification).
const maxLine = 107

// ParseV1 parses a single PROXY v1 TCP4 line, which must include its trailing
// "\r\n". Anything other than the canonical form
// "PROXY TCP4 <src-ip> <dst-ip> <src-port> <dst-port>\r\n" is rejected.
func ParseV1(line []byte) (src, dst netip.AddrPort, err error) {
	bad := func(why string) (netip.AddrPort, netip.AddrPort, error) {
		return netip.AddrPort{}, netip.AddrPort{}, fmt.Errorf("%w: %s", ErrHeader, why)
	}
	if len(line) > maxLine {
		return bad("line too long")
	}
	s := string(line)
	if !strings.HasSuffix(s, "\r\n") {
		return bad("missing CRLF")
	}
	f := strings.Split(s[:len(s)-2], " ")
	if len(f) != 6 {
		return bad("wrong field count")
	}
	if f[0] != "PROXY" || f[1] != "TCP4" {
		return bad("not PROXY TCP4")
	}
	var addrs [2]netip.Addr
	for i, a := range f[2:4] {
		ip, perr := netip.ParseAddr(a)
		if perr != nil || !ip.Is4() || ip.String() != a {
			return bad("bad address")
		}
		addrs[i] = ip
	}
	var ports [2]uint16
	for i, p := range f[4:6] {
		n, perr := strconv.ParseUint(p, 10, 16)
		if perr != nil || n < 1 || strconv.FormatUint(n, 10) != p {
			return bad("bad port")
		}
		ports[i] = uint16(n)
	}
	return netip.AddrPortFrom(addrs[0], ports[0]), netip.AddrPortFrom(addrs[1], ports[1]), nil
}

// Wrap returns a listener whose accepted connections require a PROXY v1
// header. The header is read lazily in the connection's own goroutine (on the
// first Read or RemoteAddr), never inside Accept, so a silent client cannot
// stall the accept loop. allow is called with the header's source address;
// a nil allow rejects everything. timeout bounds the header read.
func Wrap(l net.Listener, allow func(netip.Addr) bool, timeout time.Duration) net.Listener {
	return &listener{Listener: l, allow: allow, timeout: timeout}
}

type listener struct {
	net.Listener
	allow   func(netip.Addr) bool
	timeout time.Duration
}

func (l *listener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &conn{Conn: c, allow: l.allow, timeout: l.timeout}, nil
}

type conn struct {
	net.Conn
	allow   func(netip.Addr) bool
	timeout time.Duration

	once sync.Once
	br   *bufio.Reader
	addr net.Addr // header source once validated; nil otherwise
	err  error    // sticky header failure

	mu       sync.Mutex // guards the caller's read deadline across the header phase
	ready    bool
	deadline time.Time
}

// handshake reads and validates the header exactly once. If anything goes
// wrong, including a panic in the allow callback, the connection ends up
// closed with a sticky ErrHeader.
func (c *conn) handshake() {
	c.once.Do(func() {
		ok := false
		defer func() {
			if !ok {
				if c.err == nil {
					c.err = ErrHeader
				}
				c.Conn.Close()
			}
		}()
		if err := c.readHeader(); err != nil {
			c.err = err
			return
		}
		ok = true
		c.mu.Lock()
		c.ready = true
		// Restore the caller's deadline (zero clears ours), under the lock so
		// a concurrent SetReadDeadline cannot be overwritten by a stale value.
		c.Conn.SetReadDeadline(c.deadline)
		c.mu.Unlock()
	})
}

func (c *conn) readHeader() error {
	if !peerIsLoopback(c.Conn.RemoteAddr()) {
		return fmt.Errorf("%w: peer %v is not loopback", ErrHeader, c.Conn.RemoteAddr())
	}
	if err := c.Conn.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
		return fmt.Errorf("%w: %w", ErrHeader, err)
	}
	c.br = bufio.NewReaderSize(c.Conn, 4096)
	line := make([]byte, 0, maxLine)
	for {
		b, err := c.br.ReadByte()
		if err != nil {
			return fmt.Errorf("%w: %w", ErrHeader, err)
		}
		line = append(line, b)
		if b == '\n' {
			break
		}
		if len(line) == maxLine {
			return fmt.Errorf("%w: no terminator within %d bytes", ErrHeader, maxLine)
		}
	}
	src, _, err := ParseV1(line)
	if err != nil {
		return err
	}
	if c.allow == nil || !c.allow(src.Addr()) {
		return fmt.Errorf("%w: source %v not allowed", ErrHeader, src.Addr())
	}
	c.addr = net.TCPAddrFromAddrPort(src)
	return nil
}

func peerIsLoopback(a net.Addr) bool {
	if a == nil {
		return false
	}
	ap, err := netip.ParseAddrPort(a.String())
	if err != nil {
		return false
	}
	return ap.Addr().Unmap().IsLoopback()
}

func (c *conn) Read(p []byte) (int, error) {
	c.handshake()
	if c.err != nil {
		return 0, c.err
	}
	return c.br.Read(p)
}

// Write is gated by the same handshake as Read: nothing is ever written to a
// peer whose header has not been validated.
func (c *conn) Write(p []byte) (int, error) {
	c.handshake()
	if c.err != nil {
		return 0, c.err
	}
	return c.Conn.Write(p)
}

// CloseWrite forwards the half-close (the egress tunnel splice type-asserts
// for it) once the header has been validated.
func (c *conn) CloseWrite() error {
	c.handshake()
	if c.err != nil {
		return c.err
	}
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return errors.New("proxyline: connection does not support CloseWrite")
}

// CloseRead forwards the read half-close if the inner conn supports it.
func (c *conn) CloseRead() error {
	if cr, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return cr.CloseRead()
	}
	return errors.New("proxyline: connection does not support CloseRead")
}

// RemoteAddr returns the header's source after a successful handshake and the
// real peer address otherwise. It triggers the handshake if not yet done.
func (c *conn) RemoteAddr() net.Addr {
	c.handshake()
	if c.err == nil && c.addr != nil {
		return c.addr
	}
	return c.Conn.RemoteAddr()
}

// SetReadDeadline records the caller's deadline; it is applied immediately
// only once the header phase (which owns the deadline) is over.
func (c *conn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deadline = t
	if !c.ready {
		return nil
	}
	return c.Conn.SetReadDeadline(t)
}

func (c *conn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.Conn.SetWriteDeadline(t)
}
