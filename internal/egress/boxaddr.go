package egress

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
)

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
// inspect against its network unless the proxy was built with one (or with a
// shared cache that already has one).
func (p *Proxy) setBoxAddr(f func(ctx context.Context, box string) (netip.Addr, error)) {
	p.addrs.SetLookup(f)
}

// boxSourceOK: src is the current address of box on the egress network, as the
// shared box-address cache answers it (a cached match needs no docker call; a
// mismatch is looked up again at most once per boxaddr.RefreshInterval).
func (p *Proxy) boxSourceOK(ctx context.Context, box string, src netip.Addr) bool {
	return p.addrs.SourceOK(ctx, box, src)
}

// boxSourceAllowed applies boxSourceOK when the source check is on (both OSes:
// on macOS the relay's PROXY line makes RemoteAddr the box's own address on the
// egress network). Without the check there is no box address to bind to.
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

// dropBoxLocked forgets box's cached address. Callers hold p.mu; the cache has
// its own lock and never calls back into the proxy.
func (p *Proxy) dropBoxLocked(box string) {
	p.addrs.Drop(box)
}
