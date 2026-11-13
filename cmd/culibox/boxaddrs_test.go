package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/boxaddr"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/proxyline"
	"github.com/culiops/culibox/internal/session"
)

// inspectDocker answers `docker inspect` with a fixed network table and
// records the arguments it was given.
type inspectDocker struct {
	out  string
	args []string
}

func (d *inspectDocker) Run(_ context.Context, args []string) (string, error) {
	d.args = args
	return d.out, nil
}

// The box-address lookup asks docker about the box on THIS instance's egress
// network, so another instance's network never vouches for a box.
func TestNewBoxAddrsLooksUpOnTheInstanceNetwork(t *testing.T) {
	d := &inspectDocker{out: `{"agentbox-egress-DEV":{"IPAddress":"172.29.72.9"}}`}
	if c := newBoxAddrs(d, "DEV"); !c.SourceOK(context.Background(), "ab-box-x", netip.MustParseAddr("172.29.72.9")) {
		t.Fatal("the box's own address on the instance network was refused")
	}
	if len(d.args) == 0 || d.args[len(d.args)-1] != "ab-box-x" {
		t.Fatalf("docker was asked %v, want an inspect of ab-box-x", d.args)
	}
	if newBoxAddrs(d, "PROD").SourceOK(context.Background(), "ab-box-x", netip.MustParseAddr("172.29.72.9")) {
		t.Fatal("an address on another instance's network vouched for the box")
	}
}

// The egress proxy and the credential broker share ONE cache: the proxy drops
// a box's entry when its session ends or a probe re-verifies a recreated box,
// and a second cache would never be cleared (a recreated box inheriting a
// stale address could then pass the broker's check).
func TestEgressAndBrokerShareTheLayersBoxAddrCache(t *testing.T) {
	var lookups atomic.Int32
	cache := boxaddr.New(context.Background(), func(context.Context, string) (netip.Addr, error) {
		lookups.Add(1)
		return netip.MustParseAddr("172.29.72.9"), nil
	}, time.Now)
	layer := absentLayer() // its docker is /bin/false: a lookup through it would fail
	layer.addrs = cache
	e, err := startEgress(t.TempDir(), layer, config.Config{}, "linux", noSessions)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Stop(context.Background())

	ip := netip.MustParseAddr("172.29.72.9")
	if !cache.SourceOK(context.Background(), "ab-box-x", ip) || lookups.Load() != 1 {
		t.Fatalf("the cache's own lookup was replaced (lookups %d)", lookups.Load())
	}
	// Minting a probe credential for the box drops its entry from the proxy's
	// cache: it must be the layer's cache, so the next check looks the box up again.
	_, _, done := e.Svc.Proxy().ProbeCredential("ab-box-x")
	defer done()
	if !cache.SourceOK(context.Background(), "ab-box-x", ip) || lookups.Load() != 2 {
		t.Fatalf("the egress proxy does not share the layer's cache (lookups %d, want 2)", lookups.Load())
	}
}

// testRelayKey is a relay key for tests that hand the macOS wiring one by hand.
var testRelayKey = func() proxyline.Key {
	var raw [32]byte
	for i := range raw {
		raw[i] = 0x51 + byte(i)
	}
	k, err := proxyline.KeyFromBytes(raw[:])
	if err != nil {
		panic(err)
	}
	return k
}()

// relayConn dials addr and runs the relay's handshake under testRelayKey, as the
// relay does: the port is the one it dialed, which is addr's. The PROXY line
// and the request follow. The conn has a deadline, so a listener that never
// answers fails the test instead of hanging it.
func relayConn(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(5 * time.Second))
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	if err := proxyline.RelayHandshake(c, testRelayKey, ap.Port()); err != nil {
		t.Fatalf("relay handshake: %v", err)
	}
	return c
}

// rawPostProxy is one POST on c, a relay conn, behind head (a PROXY line): status
// and body, or the error when the connection was closed unanswered. It closes c.
func rawPostProxy(t *testing.T, c net.Conn, head, path, tok, body string) (int, string, error) {
	t.Helper()
	defer c.Close()
	fmt.Fprintf(c, "%sPOST %s HTTP/1.1\r\nHost: b\r\nAuthorization: Bearer %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		head, path, tok, len(body), body)
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: "POST"})
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), nil
}

// On macOS the wiring hands the broker the layer's cache: a bound session's
// token works from its own box's address and from no other. Without a cache
// (nil) every request is refused: no box address to bind to.
func TestStartBrokerDarwinBindsTokensToTheBoxAddress(t *testing.T) {
	valFile := filepath.Join(t.TempDir(), "kept")
	if err := os.WriteFile(valFile, []byte("tok-SECRET-1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	kept := tokenEntry("kept", valFile)
	sessions := map[string]*session.Session{
		"tok-1": {ID: "tok-1", Agent: "claude", Runtime: config.RuntimeBox, Box: wireBox, BrokerNonce: wireNonce, Tokens: []config.StaticToken{kept}},
	}
	get := func(id string) (*session.Session, bool) { s, ok := sessions[id]; return s, ok }
	info := func(string) (session.Info, bool) { return session.Info{}, false }
	lookup := func(_ context.Context, box string) (netip.Addr, error) {
		if box == wireBox {
			return netip.MustParseAddr("172.29.72.9"), nil
		}
		return netip.Addr{}, errors.New("no such box")
	}
	// The relay proves its key (#118), then writes the PROXY line.
	own := "PROXY TCP4 172.29.72.9 172.29.72.1 40000 9071\r\n"
	other := "PROXY TCP4 172.29.72.10 172.29.72.1 40000 9071\r\n"
	const path, body = "/v1/static/token", `{"name":"kept"}`

	dir := shortRoot(t)
	svc, err := startBrokerWithStore(dir, dir, "darwin", 0, get, info, nil, []config.StaticToken{kept}, nil,
		boxaddr.New(context.Background(), lookup, time.Now), testRelayKey)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Stop(context.Background())
	addr := svc.Hooks.EP.Listener.Addr().String()
	tok := svc.Hooks.Token("tok-1", wireNonce)
	if code, b, err := rawPostProxy(t, relayConn(t, addr), own, path, tok, body); err != nil || code != http.StatusOK || !strings.Contains(b, "tok-SECRET-1234") {
		t.Fatalf("own address: %d %q %v", code, b, err)
	}
	if code, b, err := rawPostProxy(t, relayConn(t, addr), other, path, tok, body); err != nil || code != http.StatusForbidden || strings.Contains(b, "tok-SECRET-1234") {
		t.Fatalf("another address: %d %q %v", code, b, err)
	}

	dir2 := shortRoot(t)
	svc2, err := startBrokerWithStore(dir2, dir2, "darwin", 0, get, info, nil, []config.StaticToken{kept}, nil, nil, testRelayKey)
	if err != nil {
		t.Fatal(err)
	}
	defer svc2.Stop(context.Background())
	addr2 := svc2.Hooks.EP.Listener.Addr().String()
	tok2 := svc2.Hooks.Token("tok-1", wireNonce)
	if code, _, err := rawPostProxy(t, relayConn(t, addr2), own, path, tok2, body); err != nil || code != http.StatusForbidden {
		t.Fatalf("no box source: %d %v, want 403", code, err)
	}
}

// #114: on macOS the broker does not open without the relay key; the failure
// wraps errBrokerListen, which the daemon logs (profiles with an AWS binding
// then refuse to spawn) instead of exiting.
func TestStartBrokerDarwinRefusesWithoutARelayKey(t *testing.T) {
	dir := shortRoot(t)
	_, err := startBrokerWithStore(dir, dir, "darwin", 0,
		func(string) (*session.Session, bool) { return nil, false },
		func(string) (session.Info, bool) { return session.Info{}, false }, nil, nil, nil, nil, proxyline.Key{})
	if !errors.Is(err, errBrokerListen) {
		t.Fatalf("err = %v, want errBrokerListen", err)
	}
	if !strings.Contains(err.Error(), "relay key") {
		t.Errorf("err = %v, want one naming the relay key", err)
	}
}
