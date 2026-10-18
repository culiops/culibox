package egress

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

const (
	// boxRefreshInterval: a box's address is looked up again on a mismatch at
	// most this often, so a box presenting another box's token cannot make the
	// proxy call docker once per CONNECT.
	boxRefreshInterval = 10 * time.Second
	boxLookupTimeout   = 5 * time.Second
)

// boxAddrEntry is one box's address on the egress network as last looked up.
type boxAddrEntry struct {
	addr    netip.Addr    // invalid: the lookup failed or the box has none
	checked time.Time     // when the last lookup started
	ready   chan struct{} // non-nil while that lookup runs; closed when it ends
}

// BoxAddr is the address of container box on the named docker network, from
// docker inspect. A box that is not on that network, has no usable address, or
// cannot be inspected is an error: the proxy then refuses its CONNECTs.
func BoxAddr(ctx context.Context, d Docker, box, network string) (netip.Addr, error) {
	if box == "" || strings.HasPrefix(box, "-") {
		return netip.Addr{}, fmt.Errorf("egress: unusable box name %q", box)
	}
	out, err := run(ctx, d, "inspect", "--type", "container", "--format", "{{json .NetworkSettings.Networks}}", box)
	if err != nil {
		return netip.Addr{}, err
	}
	var nets map[string]struct{ IPAddress string }
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &nets); err != nil {
		return netip.Addr{}, fmt.Errorf("egress: box %s: unreadable inspect output: %w", box, err)
	}
	n, ok := nets[network]
	if !ok {
		return netip.Addr{}, fmt.Errorf("egress: box %s is not on %s", box, network)
	}
	a, err := netip.ParseAddr(n.IPAddress)
	if err != nil || a.Unmap().IsUnspecified() {
		return netip.Addr{}, fmt.Errorf("egress: box %s has no address on %s", box, network)
	}
	return a.WithZone("").Unmap(), nil
}

// setBoxAddr installs the box-address lookup. Service calls it with docker
// inspect against its network unless the proxy was built with one.
func (p *Proxy) setBoxAddr(f func(ctx context.Context, box string) (netip.Addr, error)) {
	p.mu.Lock()
	if p.boxAddr == nil {
		p.boxAddr = f
	}
	p.mu.Unlock()
}

// boxSourceOK: src is the current address of box on the egress network. A
// cached match answers at once, without docker. A miss or a mismatch looks the
// box up again, at most once per boxRefreshInterval per box; concurrent callers
// wait for that one lookup instead of starting their own. Every failure
// (no lookup installed, a lookup error, a box with no address) is false.
func (p *Proxy) boxSourceOK(ctx context.Context, box string, src netip.Addr) bool {
	if box == "" || !src.IsValid() {
		return false
	}
	src = src.WithZone("").Unmap()
	looked := false
	for {
		p.mu.Lock()
		lookup := p.boxAddr
		e := p.boxAddrs[box]
		if e != nil && e.ready != nil {
			ch := e.ready
			p.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return false
			}
		}
		if e != nil && e.addr.IsValid() && e.addr == src {
			p.mu.Unlock()
			return true
		}
		now := p.o.Now()
		if looked || lookup == nil || (e != nil && now.Sub(e.checked) < boxRefreshInterval) {
			p.mu.Unlock()
			return false
		}
		ne := &boxAddrEntry{checked: now, ready: make(chan struct{})}
		p.boxAddrs[box] = ne
		p.mu.Unlock()

		// The proxy's own context, not the request's: a caller that hangs up
		// must not leave a failed entry that refuses the box's own CONNECTs.
		lctx, cancel := context.WithTimeout(p.ctx, boxLookupTimeout)
		a, err := lookup(lctx, box)
		cancel()
		p.mu.Lock()
		if err == nil && a.IsValid() {
			ne.addr = a.WithZone("").Unmap()
		}
		close(ne.ready)
		ne.ready = nil
		p.mu.Unlock()
		looked = true
	}
}

// boxSourceAllowed applies boxSourceOK when the source check is on (Linux,
// where each box has its own address on the egress network). Without it every
// connection arrives from the same forwarded address and there is nothing to
// bind to.
func (p *Proxy) boxSourceAllowed(ctx context.Context, box, remote string) bool {
	p.mu.Lock()
	check := p.checkSource
	p.mu.Unlock()
	if !check {
		return true
	}
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		return false
	}
	return p.boxSourceOK(ctx, box, ap.Addr())
}

// dropBoxLocked forgets box's cached address.
func (p *Proxy) dropBoxLocked(box string) {
	if box != "" {
		delete(p.boxAddrs, box)
	}
}
