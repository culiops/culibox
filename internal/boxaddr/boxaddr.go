// Package boxaddr remembers each box's address on the egress network, so a
// connection's source address can be matched to the box it claims to come from.
// The egress proxy and the credential broker share one Cache.
package boxaddr

import (
	"context"
	"net/netip"
	"sync"
	"time"
)

const (
	// RefreshInterval: a box's address is looked up again on a mismatch at
	// most this often, so a box presenting another box's token cannot make the
	// daemon call docker once per request.
	RefreshInterval = 10 * time.Second
	// LookupTimeout bounds one lookup.
	LookupTimeout = 5 * time.Second
)

// Lookup is a box's current address on the egress network.
type Lookup func(ctx context.Context, box string) (netip.Addr, error)

// entry is one box's address as last looked up.
type entry struct {
	addr    netip.Addr    // invalid: the lookup failed or the box has none
	checked time.Time     // when the last lookup started
	ready   chan struct{} // non-nil while that lookup runs; closed when it ends
}

// Cache is a box → address cache with a bounded, shared refresh. Every failure
// answers "not the box's address". It is safe for concurrent use.
type Cache struct {
	mu      sync.Mutex
	lookup  Lookup
	now     func() time.Time
	base    context.Context // lookups run under this, never under a caller's context
	timeout time.Duration   // LookupTimeout; tests shrink it
	entries map[string]*entry
}

// New returns a Cache. lookup may be nil (SetLookup installs one later; until
// then every check is refused); now nil means time.Now. Lookups run under base.
func New(base context.Context, lookup Lookup, now func() time.Time) *Cache {
	if now == nil {
		now = time.Now
	}
	return &Cache{lookup: lookup, now: now, base: base, timeout: LookupTimeout, entries: map[string]*entry{}}
}

// SetLookup installs f only when the cache has none yet: a lookup chosen at
// construction is never replaced.
func (c *Cache) SetLookup(f Lookup) {
	c.mu.Lock()
	if c.lookup == nil {
		c.lookup = f
	}
	c.mu.Unlock()
}

// SourceOK: src is the current address of box on the egress network. A cached
// match answers at once, without docker. A miss or a mismatch looks the box up
// again, at most once per RefreshInterval per box; concurrent callers wait for
// that one lookup instead of starting their own. Every failure (no lookup
// installed, a lookup error, a box with no address) is false.
func (c *Cache) SourceOK(ctx context.Context, box string, src netip.Addr) bool {
	if box == "" || !src.IsValid() {
		return false
	}
	src = src.WithZone("").Unmap()
	looked := false
	for {
		c.mu.Lock()
		lookup := c.lookup
		e := c.entries[box]
		if e != nil && e.ready != nil {
			ch := e.ready
			c.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return false
			}
		}
		if e != nil && e.addr.IsValid() && e.addr == src {
			c.mu.Unlock()
			return true
		}
		now := c.now()
		if looked || lookup == nil || (e != nil && now.Sub(e.checked) < RefreshInterval) {
			c.mu.Unlock()
			return false
		}
		ne := &entry{checked: now, ready: make(chan struct{})}
		c.entries[box] = ne
		c.mu.Unlock()

		// The cache's own context, not the request's: a caller that hangs up
		// must not leave a failed entry that refuses the box's own requests.
		lctx, cancel := context.WithTimeout(c.base, c.timeout)
		a, err := lookup(lctx, box)
		cancel()
		c.mu.Lock()
		if err == nil && a.IsValid() {
			ne.addr = a.WithZone("").Unmap()
		}
		close(ne.ready)
		ne.ready = nil
		c.mu.Unlock()
		looked = true
	}
}

// Drop forgets box's cached address; the next check looks it up afresh.
func (c *Cache) Drop(box string) {
	if box == "" {
		return
	}
	c.mu.Lock()
	delete(c.entries, box)
	c.mu.Unlock()
}
