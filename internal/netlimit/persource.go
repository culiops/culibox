package netlimit

import (
	"errors"
	"net"
	"net/netip"
	"sync"
)

// errSourceRefused is what a refused conn answers every read and write with.
var errSourceRefused = errors.New("netlimit: too many connections from this source")

// PerSource limits how many open connections one source address may hold. A
// conn's source is its RemoteAddr, asked lazily on its first use (RemoteAddr,
// Read or Write), never inside Accept: behind a PROXY-protocol wrapper the real
// source is only known once the header has been read, and that read must stay in
// the connection's own goroutine so a silent client cannot stall the accept
// loop. Put it ABOVE the wrapper and put a total cap (Listener) below it: a
// connection that has not shown its source yet is held by the total cap alone.
//
// A conn is counted once, when its source is first read, and released once, on
// Close. A conn over its source's cap, or whose source cannot be read, is closed
// at once without ever holding a slot, and answers every read and write with an
// error (fail closed). Other sources are unaffected by a full one.
func PerSource(l net.Listener, n int) net.Listener {
	return &perSourceListener{Listener: l, max: n, counts: map[netip.Addr]int{}}
}

type perSourceListener struct {
	net.Listener
	max int

	mu     sync.Mutex
	counts map[netip.Addr]int
}

func (l *perSourceListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &perSourceConn{Conn: c, l: l}, nil
}

type perSourceConn struct {
	net.Conn
	l    *perSourceListener
	once sync.Once

	// Guarded by l.mu.
	src     netip.Addr
	counted bool
	closed  bool

	refused bool // written once under once, read after admit
}

// admit learns the source (this triggers a PROXY wrapper's handshake) and takes
// a slot for it, exactly once.
func (c *perSourceConn) admit() {
	c.once.Do(func() {
		ap, err := netip.ParseAddrPort(c.Conn.RemoteAddr().String())
		if err != nil {
			c.refuse()
			return
		}
		src := ap.Addr().Unmap()
		c.l.mu.Lock()
		switch {
		case c.closed:
			// Closed before it was ever used: it never held a slot, and a late
			// look must not give it one.
		case c.l.counts[src] >= c.l.max:
			c.l.mu.Unlock()
			c.refuse()
			return
		default:
			c.l.counts[src]++
			c.src, c.counted = src, true
		}
		c.l.mu.Unlock()
	})
}

func (c *perSourceConn) refuse() {
	c.refused = true
	_ = c.Conn.Close() // not c.Close: it never held a slot
}

func (c *perSourceConn) RemoteAddr() net.Addr {
	c.admit()
	return c.Conn.RemoteAddr()
}

func (c *perSourceConn) Read(p []byte) (int, error) {
	c.admit()
	if c.refused {
		return 0, errSourceRefused
	}
	return c.Conn.Read(p)
}

func (c *perSourceConn) Write(p []byte) (int, error) {
	c.admit()
	if c.refused {
		return 0, errSourceRefused
	}
	return c.Conn.Write(p)
}

func (c *perSourceConn) Close() error {
	err := c.Conn.Close()
	c.l.mu.Lock()
	if !c.closed {
		c.closed = true
		if c.counted {
			if c.l.counts[c.src]--; c.l.counts[c.src] <= 0 {
				delete(c.l.counts, c.src)
			}
		}
	}
	c.l.mu.Unlock()
	return err
}
