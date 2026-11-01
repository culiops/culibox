package egress

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// darwinService wires a darwin-shaped Service (real loopback listener, source
// check on, PROXY header required) around the harness's proxy and returns the
// address a relay would dial.
func darwinService(t *testing.T, p *Proxy) string {
	t.Helper()
	s := NewService(ServiceOptions{Proxy: p, Docker: newFakeDocker(), GOOS: "darwin", Instance: "DEV", Port: 0,
		Want: Want{Subnet: "172.29.72.0/24"}, Listen: net.Listen})
	ep, err := s.Ready(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ep.Net.Gateway != netip.MustParseAddr("172.29.72.1") {
		t.Fatalf("gateway %v", ep.Net.Gateway)
	}
	return fmt.Sprintf("127.0.0.1:%d", ep.Port)
}

// A client that sends no PROXY line (anything on the Mac that dials the
// loopback port directly) is closed without an HTTP response.
func TestDarwinProxyNeedsHeader(t *testing.T) {
	h, _, _ := boundHarness(t)
	addr := darwinService(t, h.p)
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(c, "CONNECT extra.example.org:443 HTTP/1.1\r\nHost: extra.example.org:443\r\nProxy-Authorization: %s\r\n\r\n",
		basic("s1", h.tok("s1", nonceA)))
	b, err := io.ReadAll(c)
	if len(b) != 0 {
		t.Fatalf("got a response without a PROXY line: %q (err %v)", b, err)
	}
	if len(h.dials()) != 0 {
		t.Fatal("dialled for a connection with no PROXY line")
	}
}

// A token is bound to its own box on macOS too: the box address comes from the
// relay's PROXY line, not from the (always loopback) TCP peer.
func TestDarwinTokenFromOtherBoxRefused(t *testing.T) {
	h, ips, _ := boundHarness(t)
	ips.set("box-1", "172.29.72.5")
	ips.set("box-3", "172.29.72.6")
	addr := darwinService(t, h.p)

	do := func(src string) int {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		fmt.Fprintf(c, "PROXY TCP4 %s 172.29.72.1 40000 9072\r\n", src)
		fmt.Fprintf(c, "CONNECT extra.example.org:443 HTTP/1.1\r\nHost: extra.example.org:443\r\nProxy-Authorization: %s\r\n\r\n",
			basic("s1", h.tok("s1", nonceA)))
		resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: "CONNECT"})
		if err != nil {
			t.Fatalf("src %s: %v", src, err)
		}
		return resp.StatusCode
	}
	if code := do("172.29.72.6"); code != 407 {
		t.Fatalf("box-1's token from box-3's address: status %d, want 407", code)
	}
	if len(h.dials()) != 0 {
		t.Fatal("dialled for a token presented from another box")
	}
	if code := do("172.29.72.5"); code != 200 {
		t.Fatalf("box-1's token from its own address: status %d, want 200", code)
	}
	if got := strings.Join(addrStrings(h.dials()), ","); got == "" {
		t.Fatal("the matched request never dialled")
	}
}

func addrStrings(a []netip.AddrPort) []string {
	var out []string
	for _, x := range a {
		out = append(out, x.String())
	}
	return out
}
