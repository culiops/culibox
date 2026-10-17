package egress

import (
	"net/netip"
	"slices"
	"testing"
)

func TestPublicAddrs(t *testing.T) {
	box := netip.MustParsePrefix("172.21.0.0/16")
	in := []string{"93.184.215.14", "127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "100.76.118.121",
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
