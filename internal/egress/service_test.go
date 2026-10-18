package egress

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/sesstoken"
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

func TestReadyDarwinBindsLoopbackOnlyWithoutSourceCheck(t *testing.T) {
	f, lr := newFakeDocker(), &listenRec{}
	s := NewService(ServiceOptions{Proxy: testProxy(), Docker: f, GOOS: "darwin", Instance: "DEV", Port: 9072,
		Want: Want{Subnet: "172.29.72.0/24"}, ProxyHost: "host.lima.internal", NoProxy: "host.lima.internal", Listen: lr.listen})
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
	if p.checkSource || p.subnet != netip.MustParsePrefix("172.29.72.0/24") {
		t.Fatalf("darwin SetNetwork: check=%v subnet=%v", p.checkSource, p.subnet)
	}
}

func TestReadyReportsTheRealPortWhenAskedForZero(t *testing.T) {
	f := newFakeDocker()
	s := NewService(ServiceOptions{Proxy: testProxy(), Docker: f, GOOS: "darwin", Instance: "DEV", Port: 0,
		Want: Want{Subnet: "172.29.72.0/24"}, Listen: net.Listen})
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
// listener, and a flood of idle connections cannot take every slot for good.
func TestServiceListenerLimitsConnections(t *testing.T) {
	f := newFakeDocker()
	s := NewService(ServiceOptions{Proxy: testProxy(), Docker: f, GOOS: "darwin", Instance: "DEV", Port: 0,
		Want: Want{Subnet: "172.29.72.0/24"}, Listen: net.Listen})
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
	go func() {
		fmt.Fprintf(c3, "CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\n\r\n")
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
		ExtraEnv: []string{"FOO=bar", "CHECKPOINT_DISABLE=1"}, Listen: lr.listen})
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
