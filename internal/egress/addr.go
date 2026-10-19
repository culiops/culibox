// Package egress is the egress proxy for every boxed Linux session: the only
// way out of its network. It accepts CONNECT host:443 only, authenticates each
// tunnel to a live boxed session, matches the host against the session's AWS
// allowlist (a session without any binding runs in open mode and skips only
// that match), dials only public addresses that are not the host's own, and
// audits every tunnel.
package egress

import (
	"net"
	"net/netip"
)

// refused lists ranges a tunnel may never dial. The netip predicates in
// PublicAddrs already cover loopback, RFC 1918 / fc00::/7, link-local,
// multicast and unspecified; these are the rest: ranges the predicates miss,
// and ranges that smuggle an IPv4 address inside an IPv6 one (NAT64, 6to4,
// Teredo, IPv4-compatible, SIIT), which would reach a private v4 target through
// a translator that may sit on the path.
var refused = mustPrefixes(
	// IPv4
	"0.0.0.0/8",       // "this network"
	"100.64.0.0/10",   // CGNAT, includes the tailnet
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // TEST-NET-1
	"192.88.99.0/24",  // 6to4 relay anycast
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // TEST-NET-2
	"203.0.113.0/24",  // TEST-NET-3
	"240.0.0.0/4",     // reserved, includes the broadcast address
	// IPv6
	"::/96",           // IPv4-compatible, includes :: and ::1
	"::ffff:0:0:0/96", // SIIT translated
	"64:ff9b::/96",    // NAT64
	"64:ff9b:1::/48",  // local-use NAT64
	"100::/64",        // discard-only
	"2001::/32",       // Teredo
	"2001:2::/48",     // benchmarking
	"2001:db8::/32",   // documentation
	"2002::/16",       // 6to4
	"3fff::/20",       // documentation
	"fec0::/10",       // deprecated site-local
)

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// PublicAddrs returns the addresses of addrs a tunnel may dial, in input order.
// Each is unmapped (::ffff:a.b.c.d becomes a.b.c.d) and stripped of any zone
// first, so an IPv4-mapped loopback cannot hide behind an IPv6 spelling and a
// zone cannot defeat the prefix checks (Prefix.Contains is false for a zoned
// address). Anything that is not a global unicast address, or falls in refused
// or inside boxNet, is dropped. An invalid boxNet matches nothing.
func PublicAddrs(addrs []netip.Addr, boxNet netip.Prefix) []netip.Addr {
	if boxNet.IsValid() && boxNet.Addr().Is4In6() && boxNet.Bits() >= 96 {
		boxNet = netip.PrefixFrom(boxNet.Addr().Unmap(), boxNet.Bits()-96)
	}
	var out []netip.Addr
	for _, a := range addrs {
		if !a.IsValid() {
			continue
		}
		a = a.WithZone("").Unmap()
		if !usable(a, boxNet) {
			continue
		}
		out = append(out, a)
	}
	return out
}

func usable(a netip.Addr, boxNet netip.Prefix) bool {
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsMulticast() || a.IsUnspecified() || !a.IsGlobalUnicast() {
		return false
	}
	for _, p := range refused {
		if p.Contains(a) {
			return false
		}
	}
	return !(boxNet.IsValid() && boxNet.Contains(a))
}

// WithoutAddrs returns addrs minus every address in drop, keeping order. Both
// sides are compared after WithZone("").Unmap(), so ::ffff:a.b.c.d equals
// a.b.c.d and a zone never hides a match. The addresses returned are as given.
func WithoutAddrs(addrs, drop []netip.Addr) []netip.Addr {
	if len(drop) == 0 {
		return addrs
	}
	set := make(map[netip.Addr]struct{}, len(drop))
	for _, d := range drop {
		set[d.WithZone("").Unmap()] = struct{}{}
	}
	var out []netip.Addr
	for _, a := range addrs {
		if _, hit := set[a.WithZone("").Unmap()]; !hit {
			out = append(out, a)
		}
	}
	return out
}

// HostAddrs is every unicast address of every interface of this host, unmapped
// and without zone. The proxy never dials these: a box may not reach the
// daemon, a sibling service or the docker API through its own public name.
func HostAddrs() ([]netip.Addr, error) {
	as, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, a := range as {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if addr, ok := netip.AddrFromSlice(ip); ok {
			out = append(out, addr.WithZone("").Unmap())
		}
	}
	return out, nil
}
