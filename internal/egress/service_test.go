package egress

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/sesstoken"
)

// addrListener is a listener that never accepts but reports the address it was asked for.
type addrListener struct {
	*chanListener
	addr net.Addr
}

func (l *addrListener) Addr() net.Addr { return l.addr }

type listenRec struct {
	mu    sync.Mutex
	addrs []string
	lns   []*addrListener
	err   error
	// report, when set, is the address the listener claims instead of the asked one.
	report string
}

func (r *listenRec) listen(network, addr string) (net.Listener, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addrs = append(r.addrs, addr)
	if r.err != nil {
		return nil, r.err
	}
	a := addr
	if r.report != "" {
		a = r.report
	}
	ap := netip.MustParseAddrPort(a)
	l := &addrListener{chanListener: newChanListener(), addr: net.TCPAddrFromAddrPort(ap)}
	r.lns = append(r.lns, l)
	return l, nil
}

func (r *listenRec) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.addrs)
}

func testProxy() *Proxy {
	return New(Options{Secret: []byte("0123456789abcdef0123456789abcdef"), Audit: &memRec{},
		Lookup: func(string) (SessionView, bool) { return SessionView{}, false }})
}

func linuxService(f Docker, lr *listenRec) *Service {
	return NewService(ServiceOptions{Proxy: testProxy(), Docker: f, GOOS: "linux", Instance: "DEV", Port: 9072,
		Want: Want{Internal: true}, Listen: lr.listen})
}

func TestReadyIsIdempotentAndListensOnce(t *testing.T) {
	f, lr := newFakeDocker(), &listenRec{}
	s := linuxService(f, lr)
	defer s.Close(context.Background())
	e1, err := s.Ready(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	e2, err := s.Ready(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if lr.count() != 1 || len(f.createCalls()) != 1 {
		t.Fatalf("listens=%d creates=%d", lr.count(), len(f.createCalls()))
	}
	if e1 != e2 || e1.Port != 9072 || e1.ProxyHost != "172.21.0.1" || e1.Net.Name != "agentbox-egress-DEV" || !e1.Net.Internal {
		t.Fatalf("endpoint: %+v / %+v", e1, e2)
	}
	if s.Network() != "agentbox-egress-DEV" || s.Proxy() == nil {
		t.Fatal("accessors")
	}
}

func TestReadyLinuxBindsGatewayOnlyWithSourceCheck(t *testing.T) {
	f, lr := newFakeDocker(), &listenRec{}
	s := linuxService(f, lr)
	defer s.Close(context.Background())
	if _, err := s.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(lr.addrs) != 1 || lr.addrs[0] != "172.21.0.1:9072" {
		t.Fatalf("listen addrs: %v", lr.addrs)
	}
	p := s.Proxy()
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.checkSource || p.gw != netip.MustParseAddr("172.21.0.1") || p.subnet != netip.MustParsePrefix("172.21.0.0/16") {
		t.Fatalf("SetNetwork not applied: check=%v gw=%v subnet=%v", p.checkSource, p.gw, p.subnet)
	}
}

func TestReadyDarwinBindsLoopbackWithSourceCheck(t *testing.T) {
	f, lr := newFakeDocker(), &listenRec{}
	s := NewService(ServiceOptions{Proxy: testProxy(), Docker: f, GOOS: "darwin", Instance: "DEV", Port: 9072,
		Want: Want{Subnet: "172.29.72.0/24"}, ProxyHost: "host.lima.internal", NoProxy: "host.lima.internal", Listen: lr.listen, RelayKey: testRelayKey})
	defer s.Close(context.Background())
	ep, err := s.Ready(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(lr.addrs) != 1 || lr.addrs[0] != "127.0.0.1:9072" {
		t.Fatalf("listen addrs: %v", lr.addrs)
	}
	if ep.ProxyHost != "host.lima.internal" || ep.Net.Internal || ep.Port != 9072 {
		t.Fatalf("endpoint: %+v", ep)
	}
	p := s.Proxy()
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.checkSource || p.subnet != netip.MustParsePrefix("172.29.72.0/24") || p.gw != netip.MustParseAddr("172.29.72.1") {
		t.Fatalf("darwin SetNetwork: check=%v subnet=%v gw=%v", p.checkSource, p.subnet, p.gw)
	}
}

// #114: on macOS the PROXY line is trusted only behind the relay key, so a
// service with no key must not listen at all (and must say why).
func TestReadyDarwinRefusesWithoutARelayKey(t *testing.T) {
	f, lr := newFakeDocker(), &listenRec{}
	s := NewService(ServiceOptions{Proxy: testProxy(), Docker: f, GOOS: "darwin", Instance: "DEV", Port: 9072,
		Want: Want{Subnet: "172.29.72.0/24"}, Listen: lr.listen})
	defer s.Close(context.Background())
	_, err := s.Ready(context.Background())
	if err == nil || !strings.Contains(err.Error(), "relay key") {
		t.Fatalf("Ready without a relay key: err = %v, want one naming the relay key", err)
	}
	if lr.count() != 0 {
		t.Fatalf("listens = %d, want 0: the refusal must come before Listen", lr.count())
	}
}

// Linux has no relay, so no key: nothing changes there.
func TestReadyLinuxNeedsNoRelayKey(t *testing.T) {
	f, lr := newFakeDocker(), &listenRec{}
	s := linuxService(f, lr)
	defer s.Close(context.Background())
	if _, err := s.Ready(context.Background()); err != nil {
		t.Fatalf("Ready on linux with a zero key: %v", err)
	}
	if lr.count() != 1 {
		t.Fatalf("listens = %d, want 1", lr.count())
	}
}

func TestReadyReportsTheRealPortWhenAskedForZero(t *testing.T) {
	f := newFakeDocker()
	s := NewService(ServiceOptions{Proxy: testProxy(), Docker: f, GOOS: "darwin", Instance: "DEV", Port: 0,
		Want: Want{Subnet: "172.29.72.0/24"}, Listen: net.Listen, RelayKey: testRelayKey})
	defer s.Close(context.Background())
	ep, err := s.Ready(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ep.Port == 0 {
		t.Fatal("Endpoint.Port is the requested 0, not the bound port")
	}
}

func TestReadyFailsClosedOnListenError(t *testing.T) {
	f, lr := newFakeDocker(), &listenRec{err: fmt.Errorf("address already in use")}
	s := linuxService(f, lr)
	defer s.Close(context.Background())
	if _, err := s.Ready(context.Background()); err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("err: %v", err)
	}
	lr.mu.Lock()
	lr.err = nil
	lr.mu.Unlock()
	if _, err := s.Ready(context.Background()); err != nil {
		t.Fatalf("a later Ready must retry: %v", err)
	}
	if lr.count() != 2 {
		t.Fatalf("listens: %d", lr.count())
	}
}

func TestReadyRefusesListenerBoundWiderThanAsked(t *testing.T) {
	for _, report := range []string{"0.0.0.0:9072", "[::]:9072", "93.184.216.34:9072", "127.0.0.1:9072"} {
		f, lr := newFakeDocker(), &listenRec{report: report}
		s := linuxService(f, lr)
		if _, err := s.Ready(context.Background()); err == nil {
			t.Errorf("accepted a listener that reports %s", report)
		}
		if len(lr.lns) != 1 {
			t.Fatalf("%s: listeners: %d", report, len(lr.lns))
		}
		select {
		case <-lr.lns[0].done:
		default:
			t.Errorf("%s: the wrong listener was left open", report)
		}
		s.Close(context.Background())
	}
}

func TestReadyRefusesOpenNetworkBeforeListening(t *testing.T) {
	f, lr := newFakeDocker(), &listenRec{}
	f.nets["agentbox-egress-DEV"] = inspectJSON("agentbox-egress-DEV", false, [2]string{"172.21.0.0/16", "172.21.0.1"})
	s := linuxService(f, lr)
	defer s.Close(context.Background())
	if _, err := s.Ready(context.Background()); err == nil || !strings.Contains(err.Error(), "docker network rm") {
		t.Fatalf("err: %v", err)
	}
	if lr.count() != 0 {
		t.Fatal("listened on an open network")
	}
}

func TestReadyRefusesWhenTheGatewayMovedUnderALiveListener(t *testing.T) {
	f, lr := newFakeDocker(), &listenRec{}
	s := linuxService(f, lr)
	defer s.Close(context.Background())
	if _, err := s.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.nets["agentbox-egress-DEV"] = inspectJSON("agentbox-egress-DEV", true, [2]string{"172.30.0.0/16", "172.30.0.1"})
	f.mu.Unlock()
	if _, err := s.Ready(context.Background()); err == nil {
		t.Fatal("Ready kept serving after the network was recreated elsewhere")
	}
}

func TestReadyConcurrentCallsListenOnce(t *testing.T) {
	f, lr := newFakeDocker(), &listenRec{}
	s := linuxService(f, lr)
	defer s.Close(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Ready(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if lr.count() != 1 || len(f.createCalls()) != 1 {
		t.Fatalf("listens=%d creates=%d", lr.count(), len(f.createCalls()))
	}
}

func TestExists(t *testing.T) {
	f, lr := newFakeDocker(), &listenRec{}
	s := linuxService(f, lr)
	defer s.Close(context.Background())
	if s.Exists(context.Background()) {
		t.Fatal("exists before create")
	}
	if _, err := s.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !s.Exists(context.Background()) {
		t.Fatal("missing after Ready")
	}
	if len(f.createCalls()) != 1 {
		t.Fatal("Exists must not create")
	}
}

func TestCloseStopsListenerAndRefusesLaterReady(t *testing.T) {
	f, lr := newFakeDocker(), &listenRec{}
	s := linuxService(f, lr)
	if _, err := s.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Close(context.Background())
	s.Close(context.Background()) // idempotent
	select {
	case <-lr.lns[0].done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close left the listener open")
	}
	if _, err := s.Ready(context.Background()); err == nil {
		t.Fatal("Ready after Close")
	}
	if lr.count() != 1 {
		t.Fatal("re-listened after Close")
	}
}

// End to end on loopback (darwin shape): the proxy serves through the limit
// listener (which counts raw connections, before the PROXY line is read), and a flood of idle connections cannot take every slot for good.
func TestServiceListenerLimitsConnections(t *testing.T) {
	f := newFakeDocker()
	s := NewService(ServiceOptions{Proxy: testProxy(), Docker: f, GOOS: "darwin", Instance: "DEV", Port: 0,
		Want: Want{Subnet: "172.29.72.0/24"}, Listen: net.Listen, RelayKey: testRelayKey})
	s.maxConns = 2
	defer s.Close(context.Background())
	ep, err := s.Ready(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", ep.Port)
	c1, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	c2, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	// c1 and c2 stay idle (no request): they hold their slots until closed. The
	// kernel queue is FIFO, so the daemon takes them before c3.
	var served atomic.Bool
	c3, err := net.Dial("tcp", addr) // the kernel completes the handshake; the daemon must not accept it yet
	if err != nil {
		t.Fatal(err)
	}
	defer c3.Close()
	c3.SetDeadline(time.Now().Add(10 * time.Second))
	go func() {
		// The daemon has not accepted c3, so even the relay's handshake waits for
		// a slot: it runs here, with the wait for the reply.
		if err := relayHandshake(c3, addr, testRelayKey); err != nil {
			return
		}
		fmt.Fprint(c3, "PROXY TCP4 172.29.72.5 172.29.72.1 40000 9072\r\nCONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(c3), nil)
		if err == nil && resp.StatusCode == 407 {
			served.Store(true)
		}
	}()
	time.Sleep(300 * time.Millisecond)
	if served.Load() {
		t.Fatal("a third connection was served while two were held open")
	}
	c1.Close()
	deadline := time.Now().Add(3 * time.Second)
	for !served.Load() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !served.Load() {
		t.Fatal("closing a connection did not let the waiting one in")
	}
}

func TestDefaultMaxConnsIsTheNamedConstant(t *testing.T) {
	s := NewService(ServiceOptions{Proxy: testProxy(), Docker: newFakeDocker(), GOOS: "linux"})
	if s.maxConns != MaxProxyConns {
		t.Fatalf("maxConns = %d", s.maxConns)
	}
}

func TestDefaultMaxPerSourceIsTheNamedConstant(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		s := NewService(ServiceOptions{Proxy: testProxy(), Docker: newFakeDocker(), GOOS: goos})
		if s.maxPerSource != MaxProxyConnsPerSource {
			t.Fatalf("%s: maxPerSource = %d", goos, s.maxPerSource)
		}
	}
}

// srcConn is a pipe end that reports a chosen peer address, as a Linux box's
// connection reports its own address on the egress network.
type srcConn struct {
	net.Conn
	addr net.Addr
}

func (c srcConn) RemoteAddr() net.Addr { return c.addr }

// Linux shape: the cap is keyed on the connection's own peer address. One box
// holding its whole share leaves the others served, and a closed connection
// frees a slot.
func TestServiceLinuxOneSourceCannotCrowdOutAnother(t *testing.T) {
	f, lr := newFakeDocker(), &listenRec{}
	s := linuxService(f, lr)
	s.maxPerSource = 3
	defer s.Close(context.Background())
	if _, err := s.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	ln := lr.lns[0]
	// open hands the proxy the server end of a pipe that claims to come from
	// src, and returns the client end.
	open := func(src string) net.Conn {
		client, server := net.Pipe()
		ln.ch <- srcConn{Conn: server, addr: net.TCPAddrFromAddrPort(netip.MustParseAddrPort(src))}
		t.Cleanup(func() { client.Close() })
		return client
	}
	const bareConnect = "CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\n\r\n"
	// send writes a bare CONNECT on c (in its own goroutine: a pipe write
	// blocks until the server reads, and a refused conn never does) and gives
	// the reply 2 s.
	send := func(c net.Conn) *bufio.Reader {
		c.SetDeadline(time.Now().Add(2 * time.Second))
		go c.Write([]byte(bareConnect))
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
		// Silent: it holds a slot until closed, without sending a byte.
		held = append(held, open(fmt.Sprintf("172.21.0.5:%d", 40000+i)))
	}
	time.Sleep(200 * time.Millisecond) // the server admits them on their own goroutines

	b, err := io.ReadAll(send(open("172.21.0.5:40003")))
	if len(b) != 0 {
		t.Fatalf("a fourth connection from one source was served: %q", b)
	}
	if err != nil && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("fourth connection: %v, want EOF or a closed pipe", err)
	}

	if code := status(open("172.21.0.6:40000")); code != 407 {
		t.Fatalf("another source: status %d, want 407", code)
	}

	held[0].Close()
	deadline := time.Now().Add(3 * time.Second)
	for status(open("172.21.0.5:40004")) != 407 {
		if time.Now().After(deadline) {
			t.Fatal("closing a held connection never freed a slot for its source")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func envMap(t *testing.T, env []string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, kv := range env {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			t.Fatalf("malformed env entry %q", kv)
		}
		if _, dup := m[kv[:i]]; dup {
			t.Fatalf("%s set twice in %q", kv[:i], env)
		}
		m[kv[:i]] = kv[i+1:]
	}
	return m
}

func TestSpawnEnvURLAndStrip(t *testing.T) {
	f, lr := newFakeDocker(), &listenRec{}
	s := linuxService(f, lr)
	defer s.Close(context.Background())
	nonce := sesstoken.NewNonce()
	env, err := s.SpawnEnv(context.Background(), "claude-1", nonce)
	if err != nil {
		t.Fatal(err)
	}
	m := envMap(t, env)
	u, err := url.Parse(m["HTTPS_PROXY"])
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "http" || u.Host != "172.21.0.1:9072" || u.User.Username() != "claude-1" {
		t.Fatalf("HTTPS_PROXY = %q", m["HTTPS_PROXY"])
	}
	pass, _ := u.User.Password()
	sid, n, ok := sesstoken.Parse(sesstoken.DomainEgress, []byte("0123456789abcdef0123456789abcdef"), pass)
	if !ok || sid != "claude-1" || n != nonce {
		t.Fatalf("password does not parse under DomainEgress: ok=%v sid=%q nonce=%q", ok, sid, n)
	}
	if _, _, ok := sesstoken.Parse(sesstoken.DomainBroker, []byte("0123456789abcdef0123456789abcdef"), pass); ok {
		t.Fatal("the egress password parses as a broker token")
	}
	if m["https_proxy"] != m["HTTPS_PROXY"] {
		t.Fatalf("https_proxy %q != HTTPS_PROXY %q", m["https_proxy"], m["HTTPS_PROXY"])
	}
	for _, n := range []string{"HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy", "ALL_PROXY"} {
		if v, ok := m[n]; ok {
			t.Fatalf("Linux env sets %s=%q", n, v)
		}
	}
	// Spec 7.1: both shrink the allowlist without breaking the workload.
	if m["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] != "1" || m["CHECKPOINT_DISABLE"] != "1" {
		t.Fatalf("measured env missing: %v", env)
	}
	// Every name set is stripped first, so a spawn sets each exactly once.
	strip := s.StripEnv()
	for name := range m {
		if !slices.Contains(strip, name) {
			t.Fatalf("StripEnv lacks %s (set by SpawnEnv)", name)
		}
	}
	for _, want := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy", "NODE_USE_ENV_PROXY"} {
		if !slices.Contains(strip, want) {
			t.Fatalf("StripEnv lacks %s: %v", want, strip)
		}
	}
	// The returned slices are the caller's own.
	strip[0] = "X"
	if s.StripEnv()[0] == "X" {
		t.Fatal("StripEnv aliases internal state")
	}
}

func TestSpawnEnvDarwinHostAndNoProxy(t *testing.T) {
	f, lr := newFakeDocker(), &listenRec{}
	s := NewService(ServiceOptions{Proxy: testProxy(), Docker: f, GOOS: "darwin", Instance: "DEV", Port: 9072,
		Want: Want{Subnet: "172.29.72.0/24"}, ProxyHost: "host.lima.internal", NoProxy: "host.lima.internal",
		ExtraEnv: []string{"FOO=bar", "CHECKPOINT_DISABLE=1"}, Listen: lr.listen, RelayKey: testRelayKey})
	defer s.Close(context.Background())
	env, err := s.SpawnEnv(context.Background(), "s1", sesstoken.NewNonce())
	if err != nil {
		t.Fatal(err)
	}
	m := envMap(t, env) // fails on a duplicate: ExtraEnv must not double the measured names
	if !strings.HasSuffix(m["HTTPS_PROXY"], "@host.lima.internal:9072") {
		t.Fatalf("HTTPS_PROXY = %q", m["HTTPS_PROXY"])
	}
	if m["NO_PROXY"] != "host.lima.internal" || m["no_proxy"] != "host.lima.internal" || m["FOO"] != "bar" {
		t.Fatalf("env = %v", env)
	}
	if !slices.Contains(s.StripEnv(), "FOO") {
		t.Fatal("ExtraEnv names are not stripped")
	}
	if _, ok := m["HTTP_PROXY"]; ok {
		t.Fatal("darwin sets HTTP_PROXY")
	}
}

// Fail closed: a service that cannot become ready gives no env at all.
func TestSpawnEnvFailsWhenNotReady(t *testing.T) {
	f, lr := newFakeDocker(), &listenRec{err: errors.New("address in use")}
	s := linuxService(f, lr)
	defer s.Close(context.Background())
	env, err := s.SpawnEnv(context.Background(), "s1", sesstoken.NewNonce())
	if err == nil || env != nil {
		t.Fatalf("env=%v err=%v, want an error and no env", env, err)
	}
}

func TestServiceForgetReachesTheProxy(t *testing.T) {
	f, lr := newFakeDocker(), &listenRec{}
	s := linuxService(f, lr)
	defer s.Close(context.Background())
	p := s.Proxy()
	p.mu.Lock()
	p.stats["s1"] = &Stats{}
	p.mu.Unlock()
	s.Forget("s1")
	if _, ok := p.Stats("s1"); ok {
		t.Fatal("Forget left the session's counters at the proxy")
	}
}

func TestReadyRefreshesHostAddrs(t *testing.T) {
	var calls, dials atomic.Int32
	hostAddr := netip.MustParseAddr("93.184.216.34")
	px := New(Options{Secret: []byte("0123456789abcdef0123456789abcdef"), Audit: &memRec{},
		Lookup: func(string) (SessionView, bool) {
			return SessionView{Nonce: nonceA, Box: "b", Boxed: true, Live: true}, true
		},
		Resolve: func(context.Context, string) ([]netip.Addr, error) { return []netip.Addr{hostAddr}, nil },
		Dial:    func(context.Context, netip.AddrPort) (net.Conn, error) { dials.Add(1); return nil, errors.New("no") }})
	f, lr := newFakeDocker(), &listenRec{}
	f.boxes = map[string]string{"b": "172.21.0.5"}
	s := NewService(ServiceOptions{Proxy: px, Docker: f, GOOS: "linux", Instance: "DEV", Port: 9072,
		Want: Want{Internal: true}, Listen: lr.listen,
		HostAddrs: func() ([]netip.Addr, error) { calls.Add(1); return []netip.Addr{hostAddr}, nil }})
	defer s.Close(context.Background())
	for i := 1; i <= 2; i++ {
		if _, err := s.Ready(context.Background()); err != nil {
			t.Fatal(err)
		}
		if int(calls.Load()) != i {
			t.Fatalf("after %d Ready calls HostAddrs ran %d times", i, calls.Load())
		}
	}
	if lr.count() != 1 {
		t.Fatalf("listens: %d", lr.count())
	}
	// Drive the handler directly: the fake listener never accepts.
	req := httptest.NewRequest(http.MethodConnect, "example.com:443", nil)
	req.RequestURI = "example.com:443"
	req.RemoteAddr = "172.21.0.5:40000"
	req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("s1:"+px.Token("s1", nonceA))))
	rec := httptest.NewRecorder()
	px.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || dials.Load() != 0 {
		t.Fatalf("status %d, dials %d: a name resolving to the host must be refused", rec.Code, dials.Load())
	}
}

func TestReadyFailsClosedWhenHostAddrsUnreadable(t *testing.T) {
	for name, fn := range map[string]func() ([]netip.Addr, error){
		"error": func() ([]netip.Addr, error) { return nil, errors.New("netlink down") },
		"empty": func() ([]netip.Addr, error) { return nil, nil },
	} {
		t.Run(name, func(t *testing.T) {
			f, lr := newFakeDocker(), &listenRec{}
			s := NewService(ServiceOptions{Proxy: testProxy(), Docker: f, GOOS: "linux", Instance: "DEV", Port: 9072,
				Want: Want{Internal: true}, Listen: lr.listen, HostAddrs: fn})
			defer s.Close(context.Background())
			if _, err := s.Ready(context.Background()); err == nil {
				t.Fatal("Ready succeeded without the host's addresses")
			}
			if lr.count() != 0 {
				t.Fatal("listened without the host's addresses")
			}
		})
	}
}

// The service gives the proxy its box addresses from docker: a CONNECT is held
// to the address of its session's box on this instance's egress network.
func TestServiceBindsTokensToTheirBoxThroughDocker(t *testing.T) {
	var dials atomic.Int32
	px := New(Options{Secret: []byte("0123456789abcdef0123456789abcdef"), Audit: &memRec{},
		Lookup: func(string) (SessionView, bool) {
			return SessionView{Nonce: nonceA, Box: "b", Boxed: true, Live: true}, true
		},
		Resolve: func(context.Context, string) ([]netip.Addr, error) { return []netip.Addr{publicAddr}, nil },
		Dial:    func(context.Context, netip.AddrPort) (net.Conn, error) { dials.Add(1); return nil, errors.New("no") }})
	f, lr := newFakeDocker(), &listenRec{}
	f.boxes = map[string]string{"b": "172.21.0.5"}
	s := NewService(ServiceOptions{Proxy: px, Docker: f, GOOS: "linux", Instance: "DEV", Port: 9072,
		Want: Want{Internal: true}, Listen: lr.listen,
		HostAddrs: func() ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil }})
	defer s.Close(context.Background())
	if _, err := s.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	try := func(remote string) int {
		req := httptest.NewRequest(http.MethodConnect, "example.com:443", nil)
		req.RequestURI = "example.com:443"
		req.RemoteAddr = remote
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("s1:"+px.Token("s1", nonceA))))
		rec := httptest.NewRecorder()
		px.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := try("172.21.0.6:40000"); code != http.StatusProxyAuthRequired {
		t.Fatalf("another box's address: status %d, want 407", code)
	}
	if code := try("172.21.0.5:40000"); code != http.StatusBadGateway || dials.Load() == 0 {
		t.Fatalf("its own box: status %d dials %d, want the dial attempted (502)", code, dials.Load())
	}
	var inspects int
	f.mu.Lock()
	for _, c := range f.calls {
		if len(c) > 0 && c[0] == "inspect" {
			inspects++
			if c[len(c)-1] != "b" {
				t.Errorf("inspected %q", c)
			}
		}
	}
	f.mu.Unlock()
	if inspects != 1 {
		t.Fatalf("container inspects = %d, want 1", inspects)
	}
}

func TestServiceWarnsOnCIDROverlap(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	f, lr := newFakeDocker(), &listenRec{}
	// The fake network is 172.21.0.0/16: the first cidr contains it, the second is elsewhere.
	s := NewService(ServiceOptions{Proxy: testProxy(), Docker: f, GOOS: "linux", Instance: "DEV", Port: 9072,
		Want: Want{Internal: true}, Listen: lr.listen,
		StaticTokens: []config.StaticToken{
			{Name: "corp", Egress: []config.TokenEgress{
				{Host: "wide.corp.example", Private: true, CIDR: "172.16.0.0/12"},
				{Host: "narrow.corp.example", Private: true, CIDR: "10.20.0.0/16"},
				{Host: "public.example.com"},
			}}}})
	defer s.Close(context.Background())
	if _, err := s.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ready(context.Background()); err != nil { // idempotent: no second warning
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "overlaps the box subnet") || !strings.Contains(out, `"corp"`) || !strings.Contains(out, "172.16.0.0/12") || !strings.Contains(out, "172.21.0.0/16") {
		t.Fatalf("log = %q", out)
	}
	if strings.Contains(out, "10.20.0.0/16") || strings.Count(out, "overlaps the box subnet") != 1 {
		t.Fatalf("log = %q", out)
	}
}

func TestReadyRunsAfterNetworkOnEveryCall(t *testing.T) {
	f, lr := newFakeDocker(), &listenRec{}
	var got []Net
	s := NewService(ServiceOptions{Proxy: testProxy(), Docker: f, GOOS: "linux", Instance: "DEV", Port: 9072,
		Want: Want{Internal: true}, Listen: lr.listen,
		AfterNetwork: func(_ context.Context, n Net) error { got = append(got, n); return nil }})
	defer s.Close(context.Background())
	for i := 0; i < 2; i++ {
		if _, err := s.Ready(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 2 {
		t.Fatalf("hook calls: %d", len(got))
	}
	for _, n := range got {
		if n.Name != "agentbox-egress-DEV" || n.Gateway != netip.MustParseAddr("172.21.0.1") || n.Subnet != netip.MustParsePrefix("172.21.0.0/16") {
			t.Fatalf("hook net: %+v", n)
		}
	}
}

func TestReadyFailsWhenAfterNetworkFails(t *testing.T) {
	boom := errors.New("rules missing")
	// First call fails: nothing may listen.
	f, lr := newFakeDocker(), &listenRec{}
	s := NewService(ServiceOptions{Proxy: testProxy(), Docker: f, GOOS: "linux", Instance: "DEV", Port: 9072,
		Want: Want{Internal: true}, Listen: lr.listen,
		AfterNetwork: func(context.Context, Net) error { return boom }})
	defer s.Close(context.Background())
	if _, err := s.Ready(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err: %v", err)
	}
	if lr.count() != 0 {
		t.Fatal("listened although the hook failed")
	}

	// A later call, after a successful one, still fails with the hook's error.
	f2, lr2 := newFakeDocker(), &listenRec{}
	fail := false
	s2 := NewService(ServiceOptions{Proxy: testProxy(), Docker: f2, GOOS: "linux", Instance: "DEV", Port: 9072,
		Want: Want{Internal: true}, Listen: lr2.listen,
		AfterNetwork: func(context.Context, Net) error {
			if fail {
				return boom
			}
			return nil
		}})
	defer s2.Close(context.Background())
	if _, err := s2.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	fail = true
	if _, err := s2.Ready(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("later err: %v", err)
	}
}

func TestReadySkipsAfterNetworkWhenTheNetworkFails(t *testing.T) {
	f, lr := newFakeDocker(), &listenRec{}
	f.nets["agentbox-egress-DEV"] = inspectJSON("agentbox-egress-DEV", false, [2]string{"172.21.0.0/16", "172.21.0.1"})
	called := false
	s := NewService(ServiceOptions{Proxy: testProxy(), Docker: f, GOOS: "linux", Instance: "DEV", Port: 9072,
		Want: Want{Internal: true}, Listen: lr.listen,
		AfterNetwork: func(context.Context, Net) error { called = true; return nil }})
	defer s.Close(context.Background())
	if _, err := s.Ready(context.Background()); err == nil {
		t.Fatal("expected the network error")
	}
	if called {
		t.Fatal("hook ran against a refused network")
	}
}

// D9 (#118): when another process holds the loopback port on macOS, Ready says
// so (it gets nothing from the boxes; here is how to find it), and nothing new
// listens. The caller's behaviour is unchanged: the error still comes back and
// a later Ready retries.
func TestReadyDarwinNamesAProcessOnItsPort(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	port := held.Addr().(*net.TCPAddr).Port
	s := NewService(ServiceOptions{Proxy: testProxy(), Docker: newFakeDocker(), GOOS: "darwin", Instance: "DEV", Port: port,
		Want: Want{Subnet: "172.29.72.0/24"}, Listen: net.Listen, RelayKey: testRelayKey})
	defer s.Close(context.Background())
	_, err = s.Ready(context.Background())
	if err == nil {
		t.Fatal("Ready listened on a port another process holds")
	}
	for _, want := range []string{
		fmt.Sprintf("another process holds 127.0.0.1:%d", port),
		fmt.Sprintf("sudo lsof -nP -iTCP:%d -sTCP:LISTEN", port),
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Errorf("error lost its cause: %v", err)
	}
	s.mu.Lock()
	listening := s.ln != nil
	s.mu.Unlock()
	if listening {
		t.Error("a failed bind left a listener behind")
	}
}

// D11 (#118), changed deliberately for #131: Linux still gets no macOS text (there is no
// relay and no key proof on the Linux bridge), but an address-in-use error on the
// egress port now says so: a fixed message names the port, once in the log and in
// the returned error, because another account that took the gateway's address
// while CuliBox was down can act as the proxy. The cause stays wrapped and the
// error still begins as it did, so callers and the retry on the next Ready are
// unchanged.
func TestReadyLinuxNamesAPortConflict(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	bind := &net.OpError{Op: "listen", Err: os.NewSyscallError("bind", syscall.EADDRINUSE)}
	f, lr := newFakeDocker(), &listenRec{err: bind}
	s := linuxService(f, lr)
	defer s.Close(context.Background())
	_, err := s.Ready(context.Background())
	if err == nil {
		t.Fatal("Ready succeeded although Listen failed")
	}
	if strings.Contains(err.Error(), "another process holds") {
		t.Errorf("linux error carries the macOS explanation: %v", err)
	}
	if !errors.Is(err, syscall.EADDRINUSE) || !strings.HasPrefix(err.Error(), "egress: listen 172.") {
		t.Errorf("linux error lost its start or its cause: %v", err)
	}
	want := LinuxBindConflict(9072)
	if !strings.Contains(want, "9072") {
		t.Fatalf("the fixed message does not name the port: %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error lacks the fixed message %q: %v", want, err)
	}
	if !strings.Contains(want, "new fenced sessions are refused") {
		t.Errorf("the message does not say what happens to the boxes: %q", want)
	}
	var bc *BindConflictError
	if !errors.As(err, &bc) || bc.Port != 9072 {
		t.Errorf("error is not a BindConflictError: %v", err)
	}
	if got := strings.Count(buf.String(), want); got != 1 {
		t.Errorf("log has the fixed message %d times, want once: %q", got, buf.String())
	}
	// Retried while the port stays taken: the error says it again, the log does not.
	if _, err := s.Ready(context.Background()); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("second Ready: %v", err)
	}
	if got := strings.Count(buf.String(), want); got != 1 {
		t.Errorf("log repeats the message on a retry: %d times", got)
	}
}

// Any other Linux listen error keeps its text: only address-in-use means the
// port is held by someone else.
func TestReadyLinuxOtherListenErrorHasNoConflictText(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	denied := &net.OpError{Op: "listen", Err: os.NewSyscallError("bind", syscall.EACCES)}
	f, lr := newFakeDocker(), &listenRec{err: denied}
	s := linuxService(f, lr)
	defer s.Close(context.Background())
	_, err := s.Ready(context.Background())
	if err == nil || !errors.Is(err, syscall.EACCES) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), LinuxBindConflict(9072)) || strings.Contains(buf.String(), "already in use") {
		t.Errorf("a non-conflict error got the conflict text: %v / %q", err, buf.String())
	}
}

// The message is logged again for a new episode: once Listen works, a later
// conflict is a new event.
func TestReadyLinuxConflictLogsAgainAfterASuccess(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	bind := &net.OpError{Op: "listen", Err: os.NewSyscallError("bind", syscall.EADDRINUSE)}
	f, lr := newFakeDocker(), &listenRec{err: bind}
	s := linuxService(f, lr)
	defer s.Close(context.Background())
	if _, err := s.Ready(context.Background()); err == nil {
		t.Fatal("want a conflict")
	}
	lr.mu.Lock()
	lr.err = nil
	lr.mu.Unlock()
	if _, err := s.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.ln = nil // as if the accept loop had died; Close still closes the real one once
	s.mu.Unlock()
	lr.mu.Lock()
	lr.err = bind
	lr.mu.Unlock()
	if _, err := s.Ready(context.Background()); err == nil {
		t.Fatal("want a conflict")
	}
	if got := strings.Count(buf.String(), LinuxBindConflict(9072)); got != 2 {
		t.Errorf("log has the message %d times, want 2 (one per episode)", got)
	}
}

// sweepRec records SweepRelay calls and how many docker calls had run before
// each one.
type sweepRec struct {
	f      *fakeDocker
	err    error
	calls  int
	before []int
}

func (r *sweepRec) sweep(context.Context) error {
	r.f.mu.Lock()
	r.before = append(r.before, len(r.f.calls))
	r.f.mu.Unlock()
	r.calls++
	return r.err
}

func darwinSweepService(f *fakeDocker, lr *listenRec, sw *sweepRec) *Service {
	return NewService(ServiceOptions{Proxy: testProxy(), Docker: f, GOOS: "darwin", Instance: "DEV", Port: 9072,
		Want: Want{Subnet: "172.29.72.0/24"}, Listen: lr.listen, RelayKey: testRelayKey, SweepRelay: sw.sweep})
}

// #118: a darwin Ready that is not listening yet sweeps a relay that is not
// current before it ensures the network. A Ready that fails at the network never
// reaches AfterNetwork (the relay's own checks), so without the sweep a relay
// from an earlier CuliBox would keep forwarding the boxes' egress to a port this
// one does not hold. Ready still returns the network's error.
func TestReadyDarwinSweepsTheRelayBeforeTheNetwork(t *testing.T) {
	f, lr := newFakeDocker(), &listenRec{}
	f.nets["agentbox-egress-DEV"] = inspectJSON("agentbox-egress-DEV", false, [2]string{"172.30.0.0/16", "172.30.0.1"})
	sw := &sweepRec{f: f}
	after := false
	s := darwinSweepService(f, lr, sw)
	s.o.AfterNetwork = func(context.Context, Net) error { after = true; return nil }
	defer s.Close(context.Background())
	_, err := s.Ready(context.Background())
	if err == nil || !strings.Contains(err.Error(), "has subnet 172.30.0.0/16") {
		t.Fatalf("err = %v, want the network's refusal", err)
	}
	if sw.calls != 1 || sw.before[0] != 0 {
		t.Fatalf("sweeps = %d (docker calls before each: %v), want one before the network is looked at", sw.calls, sw.before)
	}
	if after || lr.count() != 0 {
		t.Fatalf("Ready went on past a refused network: AfterNetwork=%v listens=%d", after, lr.count())
	}
}

// A sweep that fails (docker could not inspect or remove the relay) does not
// block Ready: it is logged with a fixed text and the wrapped error, and the
// relay's own checks in AfterNetwork still decide.
func TestReadyDarwinSweepFailureIsLoggedAndReadyGoesOn(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	f, lr := newFakeDocker(), &listenRec{}
	sw := &sweepRec{f: f, err: errors.New("inspecting the relay: Cannot connect to the Docker daemon")}
	s := darwinSweepService(f, lr, sw)
	defer s.Close(context.Background())
	if _, err := s.Ready(context.Background()); err != nil {
		t.Fatalf("a failed sweep blocked Ready: %v", err)
	}
	if lr.count() != 1 {
		t.Fatalf("listens = %d, want 1", lr.count())
	}
	if got := buf.String(); !strings.Contains(got, "egress: could not remove a relay that is not current") ||
		!strings.Contains(got, "Cannot connect to the Docker daemon") {
		t.Fatalf("the sweep failure is not logged: %q", got)
	}
}

// Once listening, Ready does not sweep: the steady state costs nothing, and
// AfterNetwork still runs on every call.
func TestReadyDarwinSkipsTheSweepWhileListening(t *testing.T) {
	f, lr := newFakeDocker(), &listenRec{}
	sw := &sweepRec{f: f}
	s := darwinSweepService(f, lr, sw)
	defer s.Close(context.Background())
	for i := 0; i < 3; i++ {
		if _, err := s.Ready(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if sw.calls != 1 {
		t.Fatalf("sweeps = %d over three Ready calls, want 1 (only before the first listen)", sw.calls)
	}
}

// Linux has no relay: Ready never sweeps, even with a hook set.
func TestReadyLinuxNeverSweeps(t *testing.T) {
	f, lr := newFakeDocker(), &listenRec{}
	sw := &sweepRec{f: f}
	s := NewService(ServiceOptions{Proxy: testProxy(), Docker: f, GOOS: "linux", Instance: "DEV", Port: 9072,
		Want: Want{Internal: true}, Listen: lr.listen, SweepRelay: sw.sweep})
	defer s.Close(context.Background())
	if _, err := s.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sw.calls != 0 {
		t.Fatalf("linux swept a relay %d time(s)", sw.calls)
	}
}
