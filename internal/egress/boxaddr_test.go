package egress

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// boxIPs is a fake box-address resolver: box name → address, with a call count.
type boxIPs struct {
	mu    sync.Mutex
	ips   map[string]netip.Addr
	err   error
	calls atomic.Int32
	gate  chan struct{} // non-nil: each lookup waits for it
}

func (b *boxIPs) set(box, ip string) {
	b.mu.Lock()
	b.ips[box] = netip.MustParseAddr(ip)
	b.mu.Unlock()
}

func (b *boxIPs) resolve(_ context.Context, box string) (netip.Addr, error) {
	b.calls.Add(1)
	if b.gate != nil {
		<-b.gate
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return netip.Addr{}, b.err
	}
	a, ok := b.ips[box]
	if !ok {
		return netip.Addr{}, errors.New("no such box")
	}
	return a, nil
}

// fakeClock is a settable Now.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// boundHarness is the Linux setup: the source check is on (loopback stands in
// for the egress network, gateway 127.0.0.2, every test connection comes from
// 127.0.0.1), box-1 holds the bound session s1 and box-3 the open session s3.
func boundHarness(t *testing.T) (*harness, *boxIPs, *fakeClock) {
	t.Helper()
	ips := &boxIPs{ips: map[string]netip.Addr{}}
	ips.set("box-1", "127.0.0.1")
	ips.set("box-3", "127.0.0.3")
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	h := newHarness(t, nil, func(o *Options) {
		o.BoxAddr = ips.resolve
		o.Now = clk.now
	})
	h.setView("s3", SessionView{Nonce: nonceB, Box: "box-3", Boxed: true, Live: true}) // open mode
	h.p.SetNetwork(netip.MustParsePrefix("127.0.0.0/8"), netip.MustParseAddr("127.0.0.2"), true)
	return h, ips, clk
}

// The S1 attack: a bound box presents an open session's token. It must look
// exactly like a stale token: 407, the bad_auth row, no dial.
func TestBoundBoxCannotUseAnOpenSessionsToken(t *testing.T) {
	h, _, _ := boundHarness(t)
	code, _ := h.connect(t, "exfil.example.net:443", "s3", h.tok("s3", nonceB))
	if code != 407 {
		t.Fatalf("open token from the bound box's IP: status %d, want 407", code)
	}
	if len(h.dials()) != 0 {
		t.Fatal("dialled for a token presented from another box")
	}
	r := h.onlyRow(t)
	// Same row as a stale token for s3: the MAC-verified id, no box, bad_auth.
	if r.Outcome != OutcomeBadAuth || r.SessionID != "s3" || r.Box != "" {
		t.Fatalf("row = %+v", r)
	}
	mismatch := h.rows.all()[0]
	h2, _, _ := boundHarness(t)
	h2.connect(t, "exfil.example.net:443", "s3", h2.tok("s3", nonceA)) // wrong nonce
	if stale := h2.onlyRow(t); stale.Outcome != mismatch.Outcome || stale.SessionID != mismatch.SessionID ||
		stale.Box != mismatch.Box || stale.Host != mismatch.Host || stale.Port != mismatch.Port {
		t.Fatalf("mismatch row %+v differs from a stale-token row %+v", mismatch, stale)
	}
}

func TestOpenTokenFromItsOwnBoxIsAllowed(t *testing.T) {
	h, ips, _ := boundHarness(t)
	ips.set("box-3", "127.0.0.1")
	code, c := h.connect(t, "anything.example.net:443", "s3", h.tok("s3", nonceB))
	if code != 200 {
		t.Fatalf("open token from its own box: status %d", code)
	}
	echoRoundTrip(t, c, "ok")
	c.Close()
	// The bound session from its own box keeps its allowlist.
	if code, _ := h.connect(t, "api.anthropic.com:443", "s1", h.tok("s1", nonceA)); code != 200 {
		t.Fatalf("bound token from its own box: status %d", code)
	}
}

func TestMatchedBoxNeverCallsDocker(t *testing.T) {
	h, ips, _ := boundHarness(t)
	for i := 0; i < 5; i++ {
		code, c := h.connect(t, "api.anthropic.com:443", "s1", h.tok("s1", nonceA))
		if code != 200 {
			t.Fatalf("connect %d: status %d", i, code)
		}
		c.Close()
	}
	if n := ips.calls.Load(); n != 1 {
		t.Fatalf("docker lookups = %d, want 1 (the first contact only)", n)
	}
}

func TestBoxIPChangeRefreshesOnceThenAllows(t *testing.T) {
	h, ips, clk := boundHarness(t)
	ips.set("box-1", "127.0.0.9") // the box's old address
	if code, _ := h.connect(t, "api.anthropic.com:443", "s1", h.tok("s1", nonceA)); code != 407 {
		t.Fatalf("source not the box's address: status %d", code)
	}
	if n := ips.calls.Load(); n != 1 {
		t.Fatalf("lookups = %d", n)
	}
	// The box was recreated at 127.0.0.1; once the interval has passed, a
	// mismatch refreshes the entry once and the CONNECT goes through.
	ips.set("box-1", "127.0.0.1")
	clk.advance(boxRefreshInterval + time.Second)
	code, c := h.connect(t, "api.anthropic.com:443", "s1", h.tok("s1", nonceA))
	if code != 200 {
		t.Fatalf("after the box's address changed: status %d", code)
	}
	c.Close()
	if n := ips.calls.Load(); n != 2 {
		t.Fatalf("lookups = %d, want 2", n)
	}
}

func TestRepeatedMismatchesCallDockerOncePerInterval(t *testing.T) {
	h, ips, clk := boundHarness(t)
	tok := h.tok("s3", nonceB)
	for i := 0; i < 6; i++ {
		if code, _ := h.connect(t, "exfil.example.net:443", "s3", tok); code != 407 {
			t.Fatalf("attempt %d: status %d", i, code)
		}
	}
	// Concurrent attempts too.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := h.connect(t, "exfil.example.net:443", "s3", tok)
			if code != 407 {
				t.Errorf("concurrent attempt: status %d", code)
			}
		}()
	}
	wg.Wait()
	if n := ips.calls.Load(); n != 1 {
		t.Fatalf("docker lookups within one interval = %d, want 1", n)
	}
	clk.advance(boxRefreshInterval + time.Second)
	h.connect(t, "exfil.example.net:443", "s3", tok)
	h.connect(t, "exfil.example.net:443", "s3", tok)
	if n := ips.calls.Load(); n != 2 {
		t.Fatalf("after the interval: lookups = %d, want 2", n)
	}
	if len(h.dials()) != 0 {
		t.Fatal("dialled")
	}
}

func TestBoxLookupErrorRefuses(t *testing.T) {
	h, ips, _ := boundHarness(t)
	ips.err = errors.New("docker: no such container")
	if code, _ := h.connect(t, "api.anthropic.com:443", "s1", h.tok("s1", nonceA)); code != 407 {
		t.Fatalf("inspect error: status %d, want 407", code)
	}
	if len(h.dials()) != 0 {
		t.Fatal("dialled")
	}
	if r := h.onlyRow(t); r.Outcome != OutcomeBadAuth || r.SessionID != "s1" {
		t.Fatalf("row = %+v", r)
	}
}

func TestSourceCheckWithoutBoxResolverRefuses(t *testing.T) {
	h := newHarness(t, nil)
	h.p.SetNetwork(netip.MustParsePrefix("127.0.0.0/8"), netip.MustParseAddr("127.0.0.2"), true)
	if code, _ := h.connect(t, "api.anthropic.com:443", "s1", h.tok("s1", nonceA)); code != 407 {
		t.Fatalf("no way to learn box addresses: status %d, want 407", code)
	}
	if len(h.dials()) != 0 {
		t.Fatal("dialled")
	}
}

// Concurrent first contacts from a legitimate box all wait for the one lookup
// and then pass: the singleflight must not refuse them.
func TestConcurrentFirstContactsShareOneLookup(t *testing.T) {
	h, ips, _ := boundHarness(t)
	ips.gate = make(chan struct{})
	tok := h.tok("s1", nonceA)
	var wg sync.WaitGroup
	codes := make(chan int, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, c := h.connect(t, "api.anthropic.com:443", "s1", tok)
			if c != nil {
				c.Close()
			}
			codes <- code
		}()
	}
	waitFor(t, "the lookup to start", func() bool { return ips.calls.Load() >= 1 })
	time.Sleep(50 * time.Millisecond)
	close(ips.gate)
	wg.Wait()
	close(codes)
	for c := range codes {
		if c != 200 {
			t.Fatalf("a concurrent first contact got %d", c)
		}
	}
	if n := ips.calls.Load(); n != 1 {
		t.Fatalf("lookups = %d, want 1", n)
	}
}

func TestProbePrincipalIsBoundToItsBox(t *testing.T) {
	h, _, _ := boundHarness(t)
	// Probe for box-3 (127.0.0.3) used from 127.0.0.1: refused like a bad probe credential.
	user, pass, done := h.p.ProbeCredential("box-3")
	if code, _ := h.connect(t, defaultProbeHost+":443", user, pass); code != 407 {
		t.Fatalf("probe credential from another box: status %d", code)
	}
	if res := done(); res != (ProbeResult{}) {
		t.Fatalf("probe result %+v", res)
	}
	if len(h.dials()) != 0 {
		t.Fatal("dialled")
	}
	// From its own box it works.
	user, pass, done = h.p.ProbeCredential("box-1")
	code, c := h.connect(t, defaultProbeHost+":443", user, pass)
	if code != 200 {
		t.Fatalf("probe credential from its own box: status %d", code)
	}
	c.Close()
	if code, _ := h.connect(t, "example.com:443", user, pass); code != 403 {
		t.Fatalf("probe deny leg: status %d", code)
	}
	waitFor(t, "the probe's allowed count", func() bool { return h.p.probeSeen(user).Allowed == 1 })
	if res := done(); res != (ProbeResult{Allowed: 1, Denied: 1}) {
		t.Fatalf("probe result %+v", res)
	}
}

// A box recreated under the same name is verified with a fresh probe at once:
// minting the probe credential drops the stale entry, even within the interval.
func TestProbeCredentialDropsTheBoxEntry(t *testing.T) {
	h, ips, _ := boundHarness(t)
	ips.set("box-1", "127.0.0.9")
	if code, _ := h.connect(t, "api.anthropic.com:443", "s1", h.tok("s1", nonceA)); code != 407 {
		t.Fatalf("status %d", code)
	}
	ips.set("box-1", "127.0.0.1") // recreated; no clock advance
	user, pass, done := h.p.ProbeCredential("box-1")
	defer done()
	code, c := h.connect(t, defaultProbeHost+":443", user, pass)
	if code != 200 {
		t.Fatalf("probe of a recreated box: status %d", code)
	}
	c.Close()
}

func TestForgetDropsTheBoxEntry(t *testing.T) {
	h, _, _ := boundHarness(t)
	code, c := h.connect(t, "api.anthropic.com:443", "s1", h.tok("s1", nonceA))
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	c.Close()
	h.p.mu.Lock()
	_, cached := h.p.boxAddrs["box-1"]
	h.p.mu.Unlock()
	if !cached {
		t.Fatal("box-1 not cached after a matched CONNECT")
	}
	h.p.Forget("s1")
	h.p.mu.Lock()
	_, cached = h.p.boxAddrs["box-1"]
	_, owner := h.p.sidBox["s1"]
	h.p.mu.Unlock()
	if cached || owner {
		t.Fatal("Forget left the session's box entry behind")
	}
}

// BoxAddr reads the box's address on the named network from docker inspect.
func TestBoxAddrParsesInspect(t *testing.T) {
	const network = "agentbox-egress-DEV"
	cases := []struct {
		name, out string
		err       error
		want      string // "" → error
	}{
		{"ok", `{"agentbox-egress-DEV":{"IPAddress":"172.21.0.5"}}`, nil, "172.21.0.5"},
		{"other network only", `{"bridge":{"IPAddress":"172.17.0.2"}}`, nil, ""},
		{"no address", `{"agentbox-egress-DEV":{"IPAddress":""}}`, nil, ""},
		{"garbage address", `{"agentbox-egress-DEV":{"IPAddress":"x"}}`, nil, ""},
		{"unspecified", `{"agentbox-egress-DEV":{"IPAddress":"0.0.0.0"}}`, nil, ""},
		{"unreadable", `not json`, nil, ""},
		{"docker error", ``, errors.New("No such container"), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &scriptDocker{out: c.out, err: c.err}
			a, err := BoxAddr(context.Background(), d, "agent-box-DEV-x", network)
			if c.want == "" {
				if err == nil {
					t.Fatalf("got %v, want an error", a)
				}
			} else if err != nil || a != netip.MustParseAddr(c.want) {
				t.Fatalf("got %v, %v", a, err)
			}
			if got := strings.Join(d.args, " "); got != "inspect --type container --format {{json .NetworkSettings.Networks}} agent-box-DEV-x" {
				t.Fatalf("argv %q", got)
			}
		})
	}
	// A name docker could read as a flag is never passed.
	d := &scriptDocker{out: `{}`}
	if _, err := BoxAddr(context.Background(), d, "-x", network); err == nil || d.args != nil {
		t.Fatalf("flag-like box name: err %v, argv %q", err, d.args)
	}
	if _, err := BoxAddr(context.Background(), d, "", network); err == nil {
		t.Fatal("empty box name accepted")
	}
}

type scriptDocker struct {
	out  string
	err  error
	args []string
}

func (d *scriptDocker) Run(_ context.Context, args []string) (string, error) {
	d.args = args
	return d.out, d.err
}
