package egress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
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

// Darwin shape: the cap is keyed on the PROXY line's source, since the TCP peer
// is always the relay's loopback. One box holding its whole share leaves the
// others served, and a closed connection frees a slot.
func TestServiceDarwinOneSourceCannotCrowdOutAnother(t *testing.T) {
	s := NewService(ServiceOptions{Proxy: testProxy(), Docker: newFakeDocker(), GOOS: "darwin", Instance: "DEV", Port: 0,
		Want: Want{Subnet: "172.29.72.0/24"}, Listen: net.Listen})
	s.maxPerSource = 3
	defer s.Close(context.Background())
	ep, err := s.Ready(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", ep.Port)
	const bareConnect = "CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\n\r\n"
	dial := func(src string, port int) net.Conn {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		fmt.Fprintf(c, "PROXY TCP4 %s 172.29.72.1 %d 9072\r\n", src, port)
		return c
	}
	// send writes a bare CONNECT on c and gives the reply 2 s.
	send := func(c net.Conn) *bufio.Reader {
		c.SetDeadline(time.Now().Add(2 * time.Second))
		fmt.Fprint(c, bareConnect)
		return bufio.NewReader(c)
	}
	// status is the HTTP status of the reply, or 0 when the conn was closed
	// with no response.
	status := func(c net.Conn) int {
		resp, err := http.ReadResponse(send(c), &http.Request{Method: "CONNECT"})
		if err != nil {
			return 0
		}
		return resp.StatusCode
	}

	var held []net.Conn
	for i := 0; i < 3; i++ {
		// Only the PROXY line: the connection stays open and holds its slot.
		held = append(held, dial("172.29.72.9", 40000+i))
	}
	time.Sleep(200 * time.Millisecond) // the server admits them on their own goroutines

	// The server closes it with the CONNECT still unread, which the kernel may
	// report to the client as a reset instead of a plain EOF: either way it is
	// closed and no byte comes back.
	b, err := io.ReadAll(send(dial("172.29.72.9", 40003)))
	if len(b) != 0 {
		t.Fatalf("a fourth connection from one box was served: %q", b)
	}
	if err != nil && !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("fourth connection: %v, want EOF or a reset", err)
	}

	if code := status(dial("172.29.72.10", 40000)); code != 407 {
		t.Fatalf("another box: status %d, want 407", code)
	}

	held[0].Close()
	deadline := time.Now().Add(3 * time.Second)
	for status(dial("172.29.72.9", 40004)) != 407 {
		if time.Now().After(deadline) {
			t.Fatal("closing a held connection never freed a slot for its box")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func addrStrings(a []netip.AddrPort) []string {
	var out []string
	for _, x := range a {
		out = append(out, x.String())
	}
	return out
}
