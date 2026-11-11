package egress

import (
	"net/netip"
	"slices"
	"testing"
)

func TestPublicAddrs(t *testing.T) {
	box := netip.MustParsePrefix("172.21.0.0/16")
	in := []string{"93.184.215.14", "127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "100.64.20.30",
		"169.254.169.254", "0.0.0.0", "0.1.2.3", "224.0.0.1", "255.255.255.255", "172.21.0.1", "198.18.0.1",
		"::1", "fc00::1", "fd7a:115c:a1e0::1", "fe80::1", "::ffff:127.0.0.1", "::ffff:93.184.215.14",
		"64:ff9b::7f00:1", "2002:7f00:1::1", "2001:0:1::1", "2606:4700::1111"}
	var addrs []netip.Addr
	for _, s := range in {
		addrs = append(addrs, netip.MustParseAddr(s))
	}
	got := PublicAddrs(addrs, box)
	want := []netip.Addr{netip.MustParseAddr("93.184.215.14"), netip.MustParseAddr("93.184.215.14"), netip.MustParseAddr("2606:4700::1111")}
	if !slices.Equal(got, want) {
		t.Fatalf("PublicAddrs = %v, want %v", got, want)
	}
	if len(PublicAddrs(nil, box)) != 0 || len(PublicAddrs(addrs[:1], netip.Prefix{})) != 1 {
		t.Fatal("nil input / zero prefix")
	}
}

// Beyond the plan's minimum list: reserved and documentation ranges, other
// embeddings of IPv4 in IPv6, zones and the zero Addr must never be dialled.
func TestPublicAddrsStricter(t *testing.T) {
	refused := []string{
		"240.0.0.1", "192.0.0.8", "192.0.2.1", "198.51.100.7", "203.0.113.9", "192.88.99.1", "198.19.255.255",
		"100.64.0.1", "100.127.255.254", "172.31.255.255", "169.254.0.1",
		"::", "::2", "::7f00:1", "::ffff:0:7f00:1", "::ffff:10.0.0.1", "::ffff:169.254.169.254",
		"64:ff9b:1::1", "64:ff9b::a00:1", "100::1", "2001:db8::1", "fec0::1", "ff02::1", "ff00::1",
		"fe80::1%eth0", "2002::1", "2001::1", "2001:2::1",
	}
	box := netip.MustParsePrefix("172.21.0.0/16")
	for _, s := range refused {
		a, err := netip.ParseAddr(s)
		if err != nil {
			t.Fatalf("bad test addr %q: %v", s, err)
		}
		if got := PublicAddrs([]netip.Addr{a}, box); len(got) != 0 {
			t.Errorf("%s was not refused: %v", s, got)
		}
	}
	if got := PublicAddrs([]netip.Addr{{}}, box); len(got) != 0 {
		t.Errorf("zero Addr was kept: %v", got)
	}
	// Box subnets are refused wherever they sit, in both families and as a
	// 4-in-6 prefix; an address just outside is kept.
	if got := PublicAddrs([]netip.Addr{netip.MustParseAddr("93.184.215.14")}, netip.MustParsePrefix("93.184.0.0/16")); len(got) != 0 {
		t.Errorf("box subnet not refused: %v", got)
	}
	if got := PublicAddrs([]netip.Addr{netip.MustParseAddr("2606:4700::1111")}, netip.MustParsePrefix("2606:4700::/32")); len(got) != 0 {
		t.Errorf("v6 box subnet not refused: %v", got)
	}
	if got := PublicAddrs([]netip.Addr{netip.MustParseAddr("93.184.215.14")}, netip.MustParsePrefix("::ffff:93.184.0.0/112")); len(got) != 0 {
		t.Errorf("4-in-6 box subnet not refused: %v", got)
	}
	if got := PublicAddrs([]netip.Addr{netip.MustParseAddr("93.185.0.1")}, netip.MustParsePrefix("93.184.0.0/16")); len(got) != 1 {
		t.Errorf("address outside the box subnet dropped: %v", got)
	}
	// Zones are stripped from what is kept.
	if got := PublicAddrs([]netip.Addr{netip.MustParseAddr("2606:4700::1111%eth0")}, box); len(got) != 1 || got[0].Zone() != "" {
		t.Errorf("zone not stripped: %v", got)
	}
}

func TestWithoutAddrs(t *testing.T) {
	a := func(ss ...string) []netip.Addr {
		var out []netip.Addr
		for _, s := range ss {
			out = append(out, netip.MustParseAddr(s))
		}
		return out
	}
	cases := []struct {
		name           string
		in, drop, want []netip.Addr
	}{
		{"mapped dropped by plain", a("::ffff:93.184.216.34", "8.8.8.8"), a("93.184.216.34"), a("8.8.8.8")},
		{"plain dropped by mapped", a("93.184.216.34", "8.8.8.8"), a("::ffff:93.184.216.34"), a("8.8.8.8")},
		{"zoned dropped by plain", a("fe80::1%eth0", "2606:4700::1111"), a("fe80::1"), a("2606:4700::1111")},
		{"plain dropped by zoned", a("fe80::1", "2606:4700::1111"), a("fe80::1%eth0"), a("2606:4700::1111")},
		{"order kept", a("9.9.9.9", "93.184.216.34", "8.8.8.8", "1.1.1.1"), a("93.184.216.34"), a("9.9.9.9", "8.8.8.8", "1.1.1.1")},
		{"nothing to drop", a("9.9.9.9", "8.8.8.8"), nil, a("9.9.9.9", "8.8.8.8")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := WithoutAddrs(tc.in, tc.drop)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("WithoutAddrs = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHostAddrsListsInterfaceAddrsUnmapped(t *testing.T) {
	got, err := HostAddrs()
	if err != nil {
		t.Skipf("no interface addresses here: %v", err)
	}
	for _, a := range got {
		if !a.IsValid() || a.Is4In6() || a.Zone() != "" {
			t.Fatalf("address %v is not normalised", a)
		}
	}
}

func TestPrivateAddrs(t *testing.T) {
	cidr := netip.MustParsePrefix("10.20.0.0/16")
	box := netip.MustParsePrefix("172.18.0.0/16")
	parse := func(ss ...string) []netip.Addr {
		var out []netip.Addr
		for _, s := range ss {
			out = append(out, netip.MustParseAddr(s))
		}
		return out
	}
	for _, s := range []string{"10.20.1.5", "::ffff:10.20.1.5"} {
		got := PrivateAddrs(parse(s), cidr, box)
		if want := parse("10.20.1.5"); !slices.Equal(got, want) {
			t.Errorf("%s: got %v, want %v", s, got, want)
		}
	}
	for _, s := range []string{"10.21.0.1", "127.0.0.1", "169.254.1.1", "172.18.0.5", "100.64.1.1", "8.8.8.8", "fe80::1%eth0"} {
		if got := PrivateAddrs(parse(s), cidr, box); len(got) != 0 {
			t.Errorf("%s was kept: %v", s, got)
		}
	}
	// The cidr never overrides the box subnet, loopback or link-local, even
	// when it contains them (the box subnet is unknown at config time).
	if got := PrivateAddrs(parse("172.18.0.5"), netip.MustParsePrefix("172.16.0.0/12"), box); len(got) != 0 {
		t.Errorf("box subnet address kept through an overlapping cidr: %v", got)
	}
	if got := PrivateAddrs(parse("127.0.0.1"), netip.MustParsePrefix("127.0.0.0/8"), box); len(got) != 0 {
		t.Errorf("loopback kept: %v", got)
	}
	if got := PrivateAddrs(parse("fe80::1", "fd00::5"), netip.MustParsePrefix("fc00::/7"), box); !slices.Equal(got, parse("fd00::5")) {
		t.Errorf("v6: %v", got)
	}
	// An invalid box subnet matches nothing; an invalid cidr keeps nothing.
	if got := PrivateAddrs(parse("10.20.1.5"), cidr, netip.Prefix{}); len(got) != 1 {
		t.Errorf("zero box prefix: %v", got)
	}
	if got := PrivateAddrs(parse("10.20.1.5"), netip.Prefix{}, box); len(got) != 0 {
		t.Errorf("zero cidr: %v", got)
	}
	if got := PrivateAddrs([]netip.Addr{{}}, cidr, box); len(got) != 0 {
		t.Errorf("zero Addr kept: %v", got)
	}
}
