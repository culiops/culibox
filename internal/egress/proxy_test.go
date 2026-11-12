package egress

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/hostmatch"
	"github.com/culiops/culibox/internal/sesstoken"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

const (
	nonceA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	nonceB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// memRec is an in-memory Recorder.
type memRec struct {
	mu   sync.Mutex
	rows []Row
}

func (m *memRec) Record(r Row) error {
	m.mu.Lock()
	m.rows = append(m.rows, r)
	m.mu.Unlock()
	return nil
}

func (m *memRec) all() []Row {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.rows)
}

// harness: a proxy on 127.0.0.1 whose Resolve answers a public-looking address
// and whose Dial goes to a local upstream server, so no test touches the network.
type harness struct {
	p     *Proxy
	addr  string
	rows  *memRec
	echo  net.Listener
	views map[string]SessionView

	mu      sync.Mutex
	dialled []netip.AddrPort
	dialErr func(netip.AddrPort) error // non-nil error → the dial fails
	up      func(net.Conn)             // upstream behaviour; nil → echo
}

var publicAddr = netip.MustParseAddr("93.184.215.14")

func bound() *config.AWSBinding {
	return &config.AWSBinding{SourceProfile: "p", RoleARN: "arn:aws:iam::123456789012:role/r", EgressAllow: []string{"extra.example.org"}}
}

func newHarness(t *testing.T, resolve func(string) []netip.Addr, mods ...func(*Options)) *harness {
	t.Helper()
	if resolve == nil {
		resolve = func(string) []netip.Addr { return []netip.Addr{publicAddr} }
	}
	h := &harness{rows: &memRec{}, views: map[string]SessionView{
		"s1": {Nonce: nonceA, Box: "box-1", Boxed: true, Live: true, AWS: bound()},
		"s2": {Nonce: nonceB, Box: "box-2", Boxed: true, Live: true, AWS: bound()},
	}}
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.echo = echo
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			h.mu.Lock()
			up := h.up
			h.mu.Unlock()
			go func() {
				defer c.Close()
				if up != nil {
					up(c)
					return
				}
				io.Copy(c, c)
			}()
		}
	}()
	o := Options{
		Secret: testSecret,
		Lookup: func(id string) (SessionView, bool) {
			h.mu.Lock()
			defer h.mu.Unlock()
			v, ok := h.views[id]
			return v, ok
		},
		Audit:   h.rows,
		Resolve: func(_ context.Context, host string) ([]netip.Addr, error) { return resolve(host), nil },
		Dial: func(ctx context.Context, a netip.AddrPort) (net.Conn, error) {
			h.mu.Lock()
			h.dialled = append(h.dialled, a)
			de := h.dialErr
			h.mu.Unlock()
			if de != nil {
				if err := de(a); err != nil {
					return nil, err
				}
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp", echo.Addr().String())
		},
	}
	for _, m := range mods {
		m(&o)
	}
	h.p = New(o)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.addr = l.Addr().String()
	go h.p.Serve(l)
	t.Cleanup(func() {
		h.p.Close()
		echo.Close()
	})
	return h
}

func (h *harness) dials() []netip.AddrPort {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.dialled)
}

func (h *harness) setView(id string, v SessionView) {
	h.mu.Lock()
	h.views[id] = v
	h.mu.Unlock()
}

func basic(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func (h *harness) tok(sid, nonce string) string { return h.p.Token(sid, nonce) }

// bufConn keeps the bytes http.ReadResponse buffered past the response head.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c bufConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// raw sends request bytes as-is and reads one response.
func (h *harness) raw(t *testing.T, req string) (*http.Response, net.Conn) {
	t.Helper()
	c, err := net.DialTimeout("tcp", h.addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, req); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	method := strings.SplitN(req, " ", 2)[0]
	resp, err := http.ReadResponse(br, &http.Request{Method: method})
	if err != nil {
		t.Fatalf("read response to %q: %v", req, err)
	}
	c.SetDeadline(time.Time{})
	return resp, bufConn{c, br}
}

// connectAuth sends one CONNECT with the given Proxy-Authorization value
// ("" = no header) plus any pipelined bytes, and returns the response and the conn.
func (h *harness) connectAuth(t *testing.T, target, auth, pipelined string) (*http.Response, net.Conn) {
	t.Helper()
	req := "CONNECT " + target + " HTTP/1.1\r\n"
	// net/http answers 400 itself for a Host header holding '@' or '/'; leave
	// it out for those targets so the request reaches the handler.
	if !strings.ContainsAny(target, "@/") {
		req += "Host: " + target + "\r\n"
	}
	if auth != "" {
		req += "Proxy-Authorization: " + auth + "\r\n"
	}
	return h.raw(t, req+"\r\n"+pipelined)
}

// connect sends one CONNECT and returns the status code and the open conn.
func (h *harness) connect(t *testing.T, target, user, pass string) (int, net.Conn) {
	t.Helper()
	auth := ""
	if user != "" || pass != "" {
		auth = basic(user, pass)
	}
	resp, c := h.connectAuth(t, target, auth, "")
	return resp.StatusCode, c
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *harness) waitRows(t *testing.T, n int) []Row {
	t.Helper()
	waitFor(t, fmt.Sprintf("%d audit rows", n), func() bool { return len(h.rows.all()) >= n })
	return h.rows.all()
}

// lastRow returns the only row, failing unless exactly one row was recorded.
func (h *harness) onlyRow(t *testing.T) Row {
	t.Helper()
	rows := h.waitRows(t, 1)
	time.Sleep(20 * time.Millisecond)
	if rows = h.rows.all(); len(rows) != 1 {
		t.Fatalf("want 1 row, got %d: %+v", len(rows), rows)
	}
	return rows[0]
}

func echoRoundTrip(t *testing.T, c net.Conn, msg string) {
	t.Helper()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	defer c.SetDeadline(time.Time{})
	if _, err := io.WriteString(c, msg); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("echo: %v", err)
	}
	if string(buf) != msg {
		t.Fatalf("echo = %q, want %q", buf, msg)
	}
}

// assertClosedByServer: after a refusal the proxy closes the connection.
func assertClosedByServer(t *testing.T, c net.Conn) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, err := c.Read(make([]byte, 1)); n != 0 || err == nil {
		t.Fatalf("connection still open after refusal: n=%d err=%v", n, err)
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("connection not closed after refusal")
	}
}

func assertRefusal(t *testing.T, resp *http.Response, code int) {
	t.Helper()
	if resp.StatusCode != code {
		t.Fatalf("status = %d, want %d", resp.StatusCode, code)
	}
	if !resp.Close {
		t.Fatal("refusal must carry Connection: close")
	}
	if code == http.StatusProxyAuthRequired {
		if got := resp.Header.Get("Proxy-Authenticate"); got != `Basic realm="agent-box"` {
			t.Fatalf("Proxy-Authenticate = %q", got)
		}
	}
}

func TestAllowedTunnelSplicesAndAuditsOnClose(t *testing.T) {
	h := newHarness(t, nil)
	code, c := h.connect(t, "api.anthropic.com:443", "s1", h.tok("s1", nonceA))
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	echoRoundTrip(t, c, "ping-pong")
	if rows := h.rows.all(); len(rows) != 0 {
		t.Fatalf("row before close: %+v", rows)
	}
	c.Close()
	r := h.onlyRow(t)
	if r.Outcome != OutcomeAllowed || r.SessionID != "s1" || r.Box != "box-1" || r.Host != "api.anthropic.com" || r.Port != 443 {
		t.Fatalf("row = %+v", r)
	}
	if r.BytesUp != 9 || r.BytesDown != 9 {
		t.Fatalf("bytes up/down = %d/%d, want 9/9", r.BytesUp, r.BytesDown)
	}
	if got := h.dials(); len(got) != 1 || got[0] != netip.AddrPortFrom(publicAddr, 443) {
		t.Fatalf("dialled %v", got)
	}
}

func TestPipelinedBytesAfterConnectAreForwarded(t *testing.T) {
	h := newHarness(t, nil)
	resp, c := h.connectAuth(t, "api.anthropic.com:443", basic("s1", h.tok("s1", nonceA)), "hello")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	c.SetDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 5)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("got %q, %v", buf, err)
	}
}

func TestNonConnectIs405BadMethod(t *testing.T) {
	h := newHarness(t, nil)
	resp, c := h.raw(t, "GET http://x/ HTTP/1.1\r\nHost: x\r\n\r\n")
	assertRefusal(t, resp, 405)
	assertClosedByServer(t, c)
	if r := h.onlyRow(t); r.Outcome != OutcomeBadMethod || r.SessionID != "" || r.Host != "x" {
		t.Fatalf("row = %+v", r)
	}
	if len(h.dials()) != 0 {
		t.Fatal("dialled")
	}
}

func TestOnlyPort443(t *testing.T) {
	for _, target := range []string{"api.anthropic.com:80", "api.anthropic.com:8443",
		"api.anthropic.com:", "api.anthropic.com", "api.anthropic.com:0443", "/x"} {
		t.Run(target, func(t *testing.T) {
			h := newHarness(t, nil)
			resp, c := h.connectAuth(t, target, basic("s1", h.tok("s1", nonceA)), "")
			assertRefusal(t, resp, 405)
			assertClosedByServer(t, c)
			if r := h.onlyRow(t); r.Outcome != OutcomeBadMethod {
				t.Fatalf("row = %+v", r)
			}
			if len(h.dials()) != 0 {
				t.Fatal("dialled")
			}
		})
	}
}

func TestMissingOrGarbledAuthIs407(t *testing.T) {
	cases := map[string]string{
		"none":         "",
		"bearer":       "Bearer x",
		"bad base64":   "Basic !!!not-base64",
		"no colon":     "Basic " + base64.StdEncoding.EncodeToString([]byte("s1")),
		"empty user":   basic("", "x"),
		"scheme only":  "Basic",
		"wrong secret": basic("s1", sesstoken.Mint(sesstoken.DomainEgress, []byte("another-secret-another-secret-00"), "s1", nonceA)),
		"junk token":   basic("s1", "s1.nonce.mac"),
	}
	for name, auth := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, nil)
			resp, c := h.connectAuth(t, "api.anthropic.com:443", auth, "")
			assertRefusal(t, resp, 407)
			assertClosedByServer(t, c)
			if len(h.dials()) != 0 {
				t.Fatal("dialled")
			}
			if auth == "" {
				// Gate M1: git's first, credential-less leg is not an audit event.
				time.Sleep(50 * time.Millisecond)
				if rows := h.rows.all(); len(rows) != 0 {
					t.Fatalf("missing header recorded rows: %+v", rows)
				}
				return
			}
			if r := h.onlyRow(t); r.Outcome != OutcomeBadAuth || r.SessionID != "" || r.Host != "api.anthropic.com" {
				t.Fatalf("row = %+v", r)
			}
		})
	}
}

func TestMissingAuthRecordsNoRow(t *testing.T) {
	h := newHarness(t, nil)
	for i := 0; i < 5; i++ {
		resp, _ := h.connectAuth(t, "github.com:443", "", "")
		assertRefusal(t, resp, 407)
	}
	// A later real refusal is still recorded, with nothing folded into it.
	resp, _ := h.connectAuth(t, "github.com:443", "Bearer x", "")
	assertRefusal(t, resp, 407)
	if r := h.onlyRow(t); r.Outcome != OutcomeBadAuth || r.Suppressed != 0 {
		t.Fatalf("row = %+v", r)
	}
	if st, _ := h.p.Stats("s1"); st != (Stats{}) {
		t.Fatalf("stats = %+v", st)
	}
}

func TestDuplicateAuthHeaderIs407(t *testing.T) {
	h := newHarness(t, nil)
	good := basic("s1", h.tok("s1", nonceA))
	resp, _ := h.raw(t, "CONNECT github.com:443 HTTP/1.1\r\nHost: github.com:443\r\nProxy-Authorization: "+good+
		"\r\nProxy-Authorization: "+good+"\r\n\r\n")
	assertRefusal(t, resp, 407)
	if r := h.onlyRow(t); r.Outcome != OutcomeBadAuth {
		t.Fatalf("row = %+v", r)
	}
}

// git (libcurl CURLAUTH_ANY) sends no credentials until challenged, then
// reconnects (the refusal said Connection: close) and retries with Basic.
func TestGitStyleChallengeThenRetry(t *testing.T) {
	h := newHarness(t, nil)
	resp, c := h.connectAuth(t, "github.com:443", "", "")
	assertRefusal(t, resp, 407)
	if resp.ContentLength != 0 {
		t.Fatalf("407 Content-Length = %d, want 0 (a client must be able to frame it)", resp.ContentLength)
	}
	if resp.ProtoMajor != 1 || resp.ProtoMinor != 1 {
		t.Fatalf("proto %s", resp.Proto)
	}
	assertClosedByServer(t, c)
	resp2, c2 := h.connectAuth(t, "github.com:443", basic("s1", h.tok("s1", nonceA)), "")
	if resp2.StatusCode != 200 {
		t.Fatalf("retry status %d", resp2.StatusCode)
	}
	echoRoundTrip(t, c2, "git-upload-pack")
	c2.Close()
	if r := h.onlyRow(t); r.Outcome != OutcomeAllowed || r.SessionID != "s1" || r.Host != "github.com" {
		t.Fatalf("row = %+v", r)
	}
}

func TestBasicSchemeIsCaseInsensitive(t *testing.T) {
	h := newHarness(t, nil)
	tok := base64.StdEncoding.EncodeToString([]byte("s1:" + h.tok("s1", nonceA)))
	resp, c := h.connectAuth(t, "github.com:443", "bAsIc "+tok, "")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	c.Close()
}

func TestBrokerTokenIsNotAnEgressToken(t *testing.T) {
	h := newHarness(t, nil)
	code, _ := h.connect(t, "api.anthropic.com:443", "s1", sesstoken.Mint(sesstoken.DomainBroker, testSecret, "s1", nonceA))
	if code != 407 {
		t.Fatalf("status %d", code)
	}
	if r := h.onlyRow(t); r.Outcome != OutcomeBadAuth || r.SessionID != "" {
		t.Fatalf("row = %+v", r)
	}
	if len(h.dials()) != 0 {
		t.Fatal("dialled")
	}
}

func TestUserMustEqualTokenSession(t *testing.T) {
	h := newHarness(t, nil)
	code, _ := h.connect(t, "api.anthropic.com:443", "s2", h.tok("s1", nonceA))
	if code != 407 {
		t.Fatalf("status %d", code)
	}
	if r := h.onlyRow(t); r.Outcome != OutcomeBadAuth || r.SessionID != "s1" {
		t.Fatalf("row = %+v (the MAC verified, so the token's session is recorded)", r)
	}
}

func TestDeadUnboxedOrStaleNonceIs407(t *testing.T) {
	views := map[string]SessionView{
		"dead":    {Nonce: nonceA, Box: "b", Boxed: true, Live: false, AWS: bound()},
		"unboxed": {Nonce: nonceA, Box: "b", Boxed: false, Live: true, AWS: bound()},
		"stale":   {Nonce: nonceB, Box: "b", Boxed: true, Live: true, AWS: bound()},
		"nonce0":  {Nonce: "", Box: "b", Boxed: true, Live: true, AWS: bound()},
		"unknown": {},
	}
	for sid, v := range views {
		t.Run(sid, func(t *testing.T) {
			h := newHarness(t, nil)
			if sid != "unknown" {
				h.setView(sid, v)
			}
			code, _ := h.connect(t, "api.anthropic.com:443", sid, h.tok(sid, nonceA))
			if code != 407 {
				t.Fatalf("status %d", code)
			}
			if r := h.onlyRow(t); r.Outcome != OutcomeBadAuth || r.SessionID != sid {
				t.Fatalf("row = %+v", r)
			}
			if len(h.dials()) != 0 {
				t.Fatal("dialled")
			}
		})
	}
}

func TestIPLiteralsAndJunkHostsDenied(t *testing.T) {
	for _, target := range []string{"1.1.1.1:443", "[::1]:443", "127.1:443", "0x7f.0x1:443", "user@a.com:443",
		"localhost:443", ":443", strings.Repeat("a.", 126) + "co:443"} {
		t.Run(target, func(t *testing.T) {
			h := newHarness(t, nil)
			code, _ := h.connect(t, target, "s1", h.tok("s1", nonceA))
			if code != 403 {
				t.Fatalf("status %d", code)
			}
			r := h.onlyRow(t)
			if r.Outcome != OutcomeDenied || r.SessionID != "s1" || r.Box != "box-1" {
				t.Fatalf("row = %+v", r)
			}
			if len(r.Host) > maxHost {
				t.Fatalf("host not bounded: %d bytes", len(r.Host))
			}
			if len(h.dials()) != 0 {
				t.Fatal("dialled")
			}
		})
	}
}

func TestAuditHostDropsUserinfo(t *testing.T) {
	h := newHarness(t, nil)
	auth := "Proxy-Authorization: " + basic("s1", h.tok("s1", nonceA)) + "\r\n\r\n"
	resp, _ := h.raw(t, "CONNECT secret@api.anthropic.com:443 HTTP/1.1\r\n"+auth)
	assertRefusal(t, resp, 403)
	if r := h.onlyRow(t); r.Host != "*@api.anthropic.com" || r.Outcome != OutcomeDenied {
		t.Fatalf("row = %+v", r)
	}
	// user:pass@ makes SplitHostPort fail ("too many colons"): 405, and the raw
	// target recorded for it still loses the userinfo.
	resp, _ = h.raw(t, "CONNECT s1:secret@api.anthropic.com:443 HTTP/1.1\r\n"+auth)
	assertRefusal(t, resp, 405)
	rows := h.waitRows(t, 2)
	if r := rows[1]; r.Outcome != OutcomeBadMethod || strings.Contains(r.Host, "secret") {
		t.Fatalf("row = %+v", r)
	}
}

func TestBadRequestLinesNeverTunnel(t *testing.T) {
	h := newHarness(t, nil)
	auth := basic("s1", h.tok("s1", nonceA))
	for _, req := range []string{
		"CONNECT api.anthropic.com\r\n:443 HTTP/1.1\r\nProxy-Authorization: " + auth + "\r\n\r\n",
		"CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: a\r\nb\r\nProxy-Authorization: " + auth + "\r\n\r\n",
		"CONNECT a%zz.com:443 HTTP/1.1\r\nProxy-Authorization: " + auth + "\r\n\r\n",
		// net/http's own URL parse refuses these before the handler (400, no row).
		"CONNECT api.anthropic.com:443x HTTP/1.1\r\nProxy-Authorization: " + auth + "\r\n\r\n",
		"CONNECT api%2eanthropic.com:443 HTTP/1.1\r\nProxy-Authorization: " + auth + "\r\n\r\n",
	} {
		resp, _ := h.raw(t, req)
		if resp.StatusCode == 200 {
			t.Fatalf("%q tunnelled", req)
		}
	}
	if len(h.dials()) != 0 {
		t.Fatal("dialled")
	}
}

func TestOversizedHeaderRefused(t *testing.T) {
	h := newHarness(t, nil)
	resp, _ := h.raw(t, "CONNECT api.anthropic.com:443 HTTP/1.1\r\nX-Pad: "+strings.Repeat("a", 16<<10)+"\r\n\r\n")
	if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if h.p.srv.ReadHeaderTimeout != 10*time.Second || h.p.srv.MaxHeaderBytes != 8<<10 {
		t.Fatalf("server limits %v / %d", h.p.srv.ReadHeaderTimeout, h.p.srv.MaxHeaderBytes)
	}
}

func TestNormalisedHostsAreAllowed(t *testing.T) {
	for _, target := range []string{"API.Anthropic.COM:443", "api.anthropic.com.:443", "sts.ap-southeast-1.amazonaws.com:443"} {
		t.Run(target, func(t *testing.T) {
			h := newHarness(t, nil)
			code, c := h.connect(t, target, "s1", h.tok("s1", nonceA))
			if code != 200 {
				t.Fatalf("status %d", code)
			}
			c.Close()
			r := h.onlyRow(t)
			if want, _ := hostmatch.Normalize(strings.TrimSuffix(target, ":443")); r.Host != want {
				t.Fatalf("host %q, want %q", r.Host, want)
			}
		})
	}
}

func TestNonAllowlistedHostDenied(t *testing.T) {
	h := newHarness(t, nil)
	code, _ := h.connect(t, "example.com:443", "s1", h.tok("s1", nonceA))
	if code != 403 {
		t.Fatalf("status %d", code)
	}
	if r := h.onlyRow(t); r.Outcome != OutcomeDenied || r.Host != "example.com" || r.SessionID != "s1" {
		t.Fatalf("row = %+v", r)
	}
	if len(h.dials()) != 0 {
		t.Fatal("dialled")
	}
	code, c := h.connect(t, "extra.example.org:443", "s1", h.tok("s1", nonceA))
	if code != 200 {
		t.Fatalf("per-binding host: status %d", code)
	}
	c.Close()
}

func TestRebindingToPrivateIsRefused(t *testing.T) {
	boxNet := netip.MustParsePrefix("8.8.8.0/24") // public-looking, so only the subnet rule refuses it
	for _, a := range []string{"127.0.0.1", "100.64.0.30", "10.0.0.5", "169.254.169.254", "::1", "fc00::1",
		"::ffff:127.0.0.1", "8.8.8.8"} {
		t.Run(a, func(t *testing.T) {
			h := newHarness(t, func(string) []netip.Addr { return []netip.Addr{netip.MustParseAddr(a)} })
			h.p.SetNetwork(boxNet, netip.MustParseAddr("8.8.8.1"), false)
			code, _ := h.connect(t, "x.s3.amazonaws.com:443", "s1", h.tok("s1", nonceA))
			if code != 403 {
				t.Fatalf("status %d", code)
			}
			if r := h.onlyRow(t); r.Outcome != OutcomePrivateAddr || r.Host != "x.s3.amazonaws.com" {
				t.Fatalf("row = %+v", r)
			}
			if len(h.dials()) != 0 {
				t.Fatal("Dial called")
			}
		})
	}
}

func TestMixedAnswerDialsOnlyThePublicAddress(t *testing.T) {
	second := netip.MustParseAddr("93.184.215.15")
	h := newHarness(t, func(string) []netip.Addr {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1"), publicAddr, netip.MustParseAddr("10.1.1.1"), second}
	})
	h.dialErr = func(a netip.AddrPort) error {
		if a.Addr() == publicAddr {
			return errors.New("refused")
		}
		return nil
	}
	code, c := h.connect(t, "api.anthropic.com:443", "s1", h.tok("s1", nonceA))
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	c.Close()
	want := []netip.AddrPort{netip.AddrPortFrom(publicAddr, 443), netip.AddrPortFrom(second, 443)}
	if got := h.dials(); !reflect.DeepEqual(got, want) {
		t.Fatalf("dialled %v, want %v", got, want)
	}
}

func TestDialFailureIs502DialError(t *testing.T) {
	h := newHarness(t, nil)
	h.dialErr = func(netip.AddrPort) error { return errors.New("connection refused") }
	code, _ := h.connect(t, "api.anthropic.com:443", "s1", h.tok("s1", nonceA))
	if code != 502 {
		t.Fatalf("status %d", code)
	}
	if r := h.onlyRow(t); r.Outcome != OutcomeDialError || r.SessionID != "s1" || r.Host != "api.anthropic.com" {
		t.Fatalf("row = %+v", r)
	}
}

func TestResolveFailureIs502DialError(t *testing.T) {
	h := newHarness(t, nil, func(o *Options) {
		o.Resolve = func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("no such host") }
	})
	code, _ := h.connect(t, "api.anthropic.com:443", "s1", h.tok("s1", nonceA))
	if code != 502 {
		t.Fatalf("status %d", code)
	}
	if r := h.onlyRow(t); r.Outcome != OutcomeDialError {
		t.Fatalf("row = %+v", r)
	}
}

func TestDeniedRowsAreThrottledPerSession(t *testing.T) {
	h := newHarness(t, nil)
	for i := 0; i < 50; i++ {
		code, c := h.connect(t, "example.com:443", "s1", h.tok("s1", nonceA))
		if code != 403 {
			t.Fatalf("status %d", code)
		}
		c.Close()
	}
	code, _ := h.connect(t, "example.com:443", "s2", h.tok("s2", nonceB))
	if code != 403 {
		t.Fatalf("status %d", code)
	}
	time.Sleep(20 * time.Millisecond)
	var s1, s2 int
	for _, r := range h.rows.all() {
		switch r.SessionID {
		case "s1":
			s1++
		case "s2":
			s2++
		}
	}
	if s1 < 1 || s1 > 2 || s2 != 1 {
		t.Fatalf("rows s1=%d s2=%d", s1, s2)
	}
	if st, _ := h.p.Stats("s1"); st.Denied != 50 {
		t.Fatalf("stats count every refusal, throttled or not: %+v", st)
	}
}

func TestThrottledCountRidesOnTheNextRow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var mu sync.Mutex
	h := newHarness(t, nil, func(o *Options) {
		o.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	})
	for i := 0; i < 4; i++ {
		h.connect(t, "example.com:443", "s1", h.tok("s1", nonceA))
	}
	mu.Lock()
	now = now.Add(2 * time.Second)
	mu.Unlock()
	h.connect(t, "example.com:443", "s1", h.tok("s1", nonceA))
	rows := h.waitRows(t, 2)
	if len(rows) != 2 || rows[0].Suppressed != 0 || rows[1].Suppressed != 3 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestPerSessionAndGlobalCaps(t *testing.T) {
	h := newHarness(t, nil, func(o *Options) { o.PerSession = 2; o.Global = 3 })
	tok1 := h.tok("s1", nonceA)
	_, a := h.connect(t, "api.anthropic.com:443", "s1", tok1)
	_, b := h.connect(t, "api.anthropic.com:443", "s1", tok1)
	resp, _ := h.connectAuth(t, "api.anthropic.com:443", basic("s1", tok1), "")
	assertRefusal(t, resp, 503)
	// Global: s2 gets the third slot, then the fourth is refused.
	code, c := h.connect(t, "api.anthropic.com:443", "s2", h.tok("s2", nonceB))
	if code != 200 {
		t.Fatalf("s2 status %d", code)
	}
	resp, _ = h.connectAuth(t, "api.anthropic.com:443", basic("s2", h.tok("s2", nonceB)), "")
	assertRefusal(t, resp, 503)
	time.Sleep(20 * time.Millisecond)
	if rows := h.rows.all(); len(rows) != 0 {
		t.Fatalf("cap refusals must not be audited: %+v", rows)
	}
	a.Close()
	waitFor(t, "a slot to free", func() bool {
		code, c := h.connect(t, "api.anthropic.com:443", "s1", tok1)
		if code == 200 {
			c.Close()
			return true
		}
		return false
	})
	b.Close()
	c.Close()
}

func TestIdleTimeoutSpansBothDirections(t *testing.T) {
	h := newHarness(t, nil, func(o *Options) { o.IdleTimeout = 200 * time.Millisecond })
	h.up = func(c net.Conn) {
		// Like a TLS server, treat the client's FIN as the end: if the idle
		// upload direction gave up early, the download is cut here.
		gone := make(chan struct{})
		go func() { io.Copy(io.Discard, c); c.Close(); close(gone) }()
		for i := 0; i < 12; i++ { // 600 ms of a one-way download
			if _, err := c.Write([]byte{'x'}); err != nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		<-gone // then silence until the proxy closes us
	}
	code, c := h.connect(t, "api.anthropic.com:443", "s1", h.tok("s1", nonceA))
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	start := time.Now()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	elapsed := time.Since(start)
	if len(got) != 12 {
		t.Fatalf("got %d bytes before close, want 12 (the download was cut)", len(got))
	}
	if elapsed < 500*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("closed after %v; want ~600ms of traffic + ~200ms idle", elapsed)
	}
	if r := h.onlyRow(t); r.BytesDown != 12 || r.BytesUp != 0 {
		t.Fatalf("row = %+v", r)
	}
}

func TestHalfCloseKeepsTheOtherDirection(t *testing.T) {
	h := newHarness(t, nil)
	h.up = func(c net.Conn) {
		b, _ := io.ReadAll(c) // until the client's half-close arrives
		time.Sleep(50 * time.Millisecond)
		c.Write([]byte("got:" + string(b)))
	}
	code, c := h.connect(t, "api.anthropic.com:443", "s1", h.tok("s1", nonceA))
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	io.WriteString(c, "req")
	c.(bufConn).Conn.(*net.TCPConn).CloseWrite()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "got:req" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func goroutinesAtMost(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := runtime.NumGoroutine()
		if got <= n {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			t.Fatalf("goroutines %d > baseline %d\n%s", got, n, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCloseTearsDownTunnelsWithoutLeaks(t *testing.T) {
	base0 := runtime.NumGoroutine()
	h := newHarness(t, nil)
	base1 := runtime.NumGoroutine()
	tok := h.tok("s1", nonceA)

	// Normal close by the client: everything a tunnel started is gone again.
	for i := 0; i < 3; i++ {
		_, c := h.connect(t, "api.anthropic.com:443", "s1", tok)
		echoRoundTrip(t, c, "x")
		c.Close()
	}
	goroutinesAtMost(t, base1)

	var conns []net.Conn
	for i := 0; i < 5; i++ {
		code, c := h.connect(t, "api.anthropic.com:443", "s1", tok)
		if code != 200 {
			t.Fatalf("status %d", code)
		}
		echoRoundTrip(t, c, "y")
		conns = append(conns, c)
	}
	closed := make(chan error, 1)
	go func() { closed <- h.p.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not return: open tunnels were not torn down")
	}
	for i, c := range conns {
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Fatalf("tunnel %d still open after Close", i)
		} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Fatalf("tunnel %d not closed by Close", i)
		}
		c.Close()
	}
	h.p.mu.Lock()
	open, total := len(h.p.tunnels), h.p.total
	h.p.mu.Unlock()
	if open != 0 || total != 0 {
		t.Fatalf("after Close: %d tunnels registered, %d slots held", open, total)
	}
	if rows := h.waitRows(t, 8); len(rows) != 8 {
		t.Fatalf("every tunnel is audited once, Close included: %d rows", len(rows))
	}
	// New connections are refused once closed.
	if c, err := net.DialTimeout("tcp", h.addr, time.Second); err == nil {
		c.Close()
		t.Fatal("listener still accepting after Close")
	}
	h.echo.Close()
	goroutinesAtMost(t, base0)
}

func TestForgetClosesTheSessionsTunnels(t *testing.T) {
	h := newHarness(t, nil)
	_, c1 := h.connect(t, "api.anthropic.com:443", "s1", h.tok("s1", nonceA))
	_, c2 := h.connect(t, "api.anthropic.com:443", "s2", h.tok("s2", nonceB))
	h.p.Forget("s1")
	c1.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c1.Read(make([]byte, 1)); err == nil {
		t.Fatal("s1 tunnel survived Forget")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("s1 tunnel not closed by Forget")
	}
	echoRoundTrip(t, c2, "still-here")
}

func TestProbeCredentialIsOneShotAndScopedToProbeHost(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	h := newHarness(t, nil, func(o *Options) { o.Now = clock })

	user, pass, done := h.p.ProbeCredential("box-9")
	if user == "" || len(pass) < 32 || strings.Contains(user, pass) {
		t.Fatalf("weak probe credential %q", user)
	}
	// One allowed tunnel to the probe host and one denied CONNECT, the two
	// checks the probe makes with a single credential (Task 10).
	code, c := h.connect(t, "api.anthropic.com:443", user, pass)
	if code != 200 {
		t.Fatalf("probe host: status %d", code)
	}
	c.Close()
	if code, _ := h.connect(t, "api.anthropic.com:443", user, pass); code != 407 {
		t.Fatalf("second allowed use: status %d", code)
	}
	if code, _ := h.connect(t, "example.com:443", user, pass); code != 403 {
		t.Fatalf("deny host: status %d", code)
	}
	if code, _ := h.connect(t, "example.com:443", user, pass); code != 407 {
		t.Fatalf("second denied use: status %d", code)
	}
	waitFor(t, "the probe's allowed count", func() bool { return h.p.probeSeen(user).Allowed == 1 })
	if res := done(); res != (ProbeResult{Allowed: 1, Denied: 1}) {
		t.Fatalf("done() = %+v", res)
	}
	if res := done(); res != (ProbeResult{Allowed: 1, Denied: 1}) {
		t.Fatalf("done() twice = %+v", res)
	}

	// After done(), the credential is gone even for its unused slots.
	u2, p2, done2 := h.p.ProbeCredential("box-9")
	done2()
	if code, _ := h.connect(t, "api.anthropic.com:443", u2, p2); code != 407 {
		t.Fatalf("after done: status %d", code)
	}
	// Scoped to the probe host: everything else on the default list is denied.
	u3, p3, done3 := h.p.ProbeCredential("box-9")
	if code, _ := h.connect(t, "github.com:443", u3, p3); code != 403 {
		t.Fatalf("other host: status %d", code)
	}
	if code, _ := h.connect(t, "api.anthropic.com:443", u3, "wrong"+p3); code != 407 {
		t.Fatalf("wrong password: status %d", code)
	}
	if res := done3(); res != (ProbeResult{Denied: 1}) {
		t.Fatalf("done3() = %+v", res)
	}
	// Expired after 60 s.
	u4, p4, _ := h.p.ProbeCredential("box-9")
	mu.Lock()
	now = now.Add(61 * time.Second)
	mu.Unlock()
	if code, _ := h.connect(t, "api.anthropic.com:443", u4, p4); code != 407 {
		t.Fatalf("expired: status %d", code)
	}
	// A probe user is not a session id, and a session token is not a probe password.
	if code, _ := h.connect(t, "api.anthropic.com:443", u4, h.tok(u4, nonceA)); code != 407 {
		t.Fatalf("token for probe user: status %d", code)
	}
	for _, r := range h.rows.all() {
		if strings.Contains(r.SessionID+r.Box+r.Host, pass) || strings.Contains(r.SessionID+r.Box+r.Host, p3) {
			t.Fatalf("probe password in audit row %+v", r)
		}
	}
}

func TestSourceCheck(t *testing.T) {
	subnet := netip.MustParsePrefix("172.21.0.0/16")
	gw := netip.MustParseAddr("172.21.0.1")
	cases := []struct {
		remote string
		subnet netip.Prefix
		want   bool
	}{
		{"172.21.0.5:40000", subnet, true},
		{"[::ffff:172.21.0.5]:40000", subnet, true},
		{"172.21.0.1:40000", subnet, false},
		{"172.17.0.2:40000", subnet, false},
		{"127.0.0.1:40000", subnet, false},
		{"172.21.0.5:40000", netip.Prefix{}, false},
		{"garbage", subnet, false},
		{"10.0.0.5:40000", netip.MustParsePrefix("0.0.0.0/0"), false},
	}
	for _, c := range cases {
		if got := sourceOK(c.remote, c.subnet, gw); got != c.want {
			t.Errorf("sourceOK(%q, %v) = %v, want %v", c.remote, c.subnet, got, c.want)
		}
	}
	// A gateway that is invalid, unspecified or outside the subnet fails closed.
	for _, g := range []netip.Addr{{}, netip.MustParseAddr("0.0.0.0"), netip.MustParseAddr("::"), netip.MustParseAddr("10.9.9.9")} {
		if sourceOK("172.21.0.5:40000", subnet, g) {
			t.Errorf("gateway %v: a source passed", g)
		}
	}

	h := newHarness(t, nil)
	h.p.SetNetwork(subnet, gw, true)
	code, _ := h.connect(t, "api.anthropic.com:443", "s1", h.tok("s1", nonceA))
	if code != 407 {
		t.Fatalf("connection from outside the egress subnet: status %d", code)
	}
	if r := h.onlyRow(t); r.Outcome != OutcomeBadAuth || r.SessionID != "" {
		t.Fatalf("row = %+v", r)
	}
	if len(h.dials()) != 0 {
		t.Fatal("dialled")
	}
}

func TestDefaultAllowMatchesSpec(t *testing.T) {
	want := []string{"api.anthropic.com", "*.amazonaws.com", "registry.terraform.io", "releases.hashicorp.com", "github.com", "proxy.golang.org", "sum.golang.org", "registry.npmjs.org"}
	if got := DefaultAllow(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultAllow() = %q, want spec §7.1 plus the 2026-11-04 registries %q", got, want)
	}
	for _, p := range DefaultAllow() {
		if err := hostmatch.ValidPattern(p); err != nil {
			t.Errorf("%q: %v", p, err)
		}
	}
}

func TestEffective(t *testing.T) {
	if got := Effective(config.Bindings{}); !reflect.DeepEqual(got, DefaultAllow()) {
		t.Fatalf("Effective(config.Bindings{}) = %q", got)
	}
	b := bound()
	got := Effective(config.Bindings{AWS: b})
	if !reflect.DeepEqual(got, append(DefaultAllow(), "extra.example.org")) {
		t.Fatalf("Effective = %q", got)
	}
	// Nothing a caller holds aliases the policy or the binding.
	got[0], got[len(got)-1] = "mutated", "mutated"
	d := DefaultAllow()
	d[1] = "*.com"
	n := Effective(config.Bindings{})
	n[0] = "mutated"
	if DefaultAllow()[0] != "api.anthropic.com" || DefaultAllow()[1] != "*.amazonaws.com" ||
		Effective(config.Bindings{})[0] != "api.anthropic.com" || b.EgressAllow[0] != "extra.example.org" {
		t.Fatal("a returned allowlist aliases DefaultAllow or the binding")
	}
}

func TestStatsAndForget(t *testing.T) {
	h := newHarness(t, func(host string) []netip.Addr {
		if host == "private.amazonaws.com" {
			return []netip.Addr{netip.MustParseAddr("10.0.0.1")}
		}
		return []netip.Addr{publicAddr}
	})
	if _, ok := h.p.Stats("s1"); ok {
		t.Fatal("stats before any tunnel")
	}
	tok := h.tok("s1", nonceA)
	_, c := h.connect(t, "api.anthropic.com:443", "s1", tok)
	c.Close()
	h.connect(t, "example.com:443", "s1", tok)
	h.connect(t, "private.amazonaws.com:443", "s1", tok)
	h.connect(t, "api.anthropic.com:443", "s1", "garbage") // bad_auth is not attributed
	// The allowed count lands just after the 200 is written (see Probe).
	waitFor(t, "the allowed count", func() bool { st, _ := h.p.Stats("s1"); return st.Allowed == 1 })
	st, ok := h.p.Stats("s1")
	if !ok || st != (Stats{Allowed: 1, Denied: 2}) {
		t.Fatalf("stats = %+v, %v", st, ok)
	}
	h.p.Forget("s1")
	if _, ok := h.p.Stats("s1"); ok {
		t.Fatal("stats survive Forget")
	}
}

func TestTokenIsEgressDomain(t *testing.T) {
	h := newHarness(t, nil)
	sid, n, ok := sesstoken.Parse(sesstoken.DomainEgress, testSecret, h.p.Token("s1", nonceA))
	if !ok || sid != "s1" || n != nonceA {
		t.Fatal("Token does not parse under DomainEgress")
	}
	if _, _, ok := sesstoken.Parse(sesstoken.DomainBroker, testSecret, h.p.Token("s1", nonceA)); ok {
		t.Fatal("egress token verifies as a broker token")
	}
}

// The positive end-to-end path: a remote inside the subnet that is not the
// gateway passes the source check (loopback stands in for the egress network).
func TestSourceCheckPassesInsideTheSubnet(t *testing.T) {
	h := newHarness(t, nil, func(o *Options) {
		// s1's box is at the address every test connection comes from.
		o.BoxAddr = func(context.Context, string) (netip.Addr, error) { return netip.MustParseAddr("127.0.0.1"), nil }
	})
	h.p.SetNetwork(netip.MustParsePrefix("127.0.0.0/8"), netip.MustParseAddr("127.0.0.2"), true)
	code, c := h.connect(t, "api.anthropic.com:443", "s1", h.tok("s1", nonceA))
	if code != 200 {
		t.Fatalf("remote 127.0.0.1 in 127.0.0.0/8, gateway 127.0.0.2: status %d", code)
	}
	echoRoundTrip(t, c, "ok")
	c.Close()
	// The remote is the gateway → refused.
	h.p.SetNetwork(netip.MustParsePrefix("127.0.0.0/8"), netip.MustParseAddr("127.0.0.1"), true)
	if code, _ := h.connect(t, "api.anthropic.com:443", "s1", h.tok("s1", nonceA)); code != 407 {
		t.Fatalf("remote == gateway: status %d", code)
	}
	// checkSource with an invalid or unspecified gateway or subnet refuses everyone.
	for _, n := range []struct {
		subnet netip.Prefix
		gw     netip.Addr
	}{
		{netip.MustParsePrefix("127.0.0.0/8"), netip.Addr{}},
		{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParseAddr("0.0.0.0")},
		{netip.Prefix{}, netip.MustParseAddr("127.0.0.2")},
		{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParseAddr("127.0.0.2")},
	} {
		h.p.SetNetwork(n.subnet, n.gw, true)
		if code, _ := h.connect(t, "api.anthropic.com:443", "s1", h.tok("s1", nonceA)); code != 407 {
			t.Fatalf("SetNetwork(%v, %v, true): status %d, want 407", n.subnet, n.gw, code)
		}
	}
}

// A session that ends while its CONNECT is between auth and the tunnel (here:
// blocked in the dial) must not get a tunnel, and Forget must leave no stats.
func TestForgetDuringDialGetsNoTunnel(t *testing.T) {
	h := newHarness(t, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	h.dialErr = func(netip.AddrPort) error {
		close(entered)
		<-release
		return nil
	}
	type result struct {
		code int
		err  error
	}
	res := make(chan result, 1)
	tok := h.tok("s1", nonceA)
	go func() {
		c, err := net.DialTimeout("tcp", h.addr, 2*time.Second)
		if err != nil {
			res <- result{err: err}
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		io.WriteString(c, "CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\nProxy-Authorization: "+basic("s1", tok)+"\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: "CONNECT"})
		if err != nil {
			res <- result{err: err}
			return
		}
		res <- result{code: resp.StatusCode}
	}()
	<-entered
	v := h.views["s1"]
	v.Live = false
	h.setView("s1", v)
	h.p.Forget("s1")
	close(release)
	r := <-res
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.code != http.StatusProxyAuthRequired {
		t.Fatalf("status %d, want 407: the session ended before its tunnel opened", r.code)
	}
	row := h.onlyRow(t)
	if row.Outcome != OutcomeBadAuth || row.SessionID != "s1" || row.BytesUp != 0 || row.BytesDown != 0 {
		t.Fatalf("row = %+v", row)
	}
	h.p.mu.Lock()
	open := len(h.p.tunnels)
	h.p.mu.Unlock()
	if open != 0 {
		t.Fatalf("%d tunnels registered", open)
	}
	if st, ok := h.p.Stats("s1"); ok {
		t.Fatalf("Forget left a stats entry: %+v", st)
	}
	// A later denial for the forgotten spawn does not re-create it either.
	h.connect(t, "example.com:443", "s1", tok)
	if st, ok := h.p.Stats("s1"); ok {
		t.Fatalf("stats re-created after Forget: %+v", st)
	}
}

// Forget also wins over a denial path that authenticated before it.
func TestForgetThenRespawnCountsAgain(t *testing.T) {
	h := newHarness(t, nil)
	h.connect(t, "example.com:443", "s1", h.tok("s1", nonceA))
	h.p.Forget("s1")
	// The id is reused by a new spawn with a new nonce: it is counted afresh.
	h.setView("s1", SessionView{Nonce: nonceB, Box: "box-1", Boxed: true, Live: true, AWS: bound()})
	_, c := h.connect(t, "api.anthropic.com:443", "s1", h.tok("s1", nonceB))
	c.Close()
	// The allowed count lands just after the 200 is written (see Probe).
	waitFor(t, "the respawn's allowed count", func() bool { st, _ := h.p.Stats("s1"); return st.Allowed == 1 })
	if st, ok := h.p.Stats("s1"); !ok || st != (Stats{Allowed: 1}) {
		t.Fatalf("stats = %+v, %v", st, ok)
	}
}

// failConn's writes always fail, as if the client vanished before the 200.
type failConn struct{ net.Conn }

func (failConn) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

type hijackRecorder struct {
	http.ResponseWriter
	conn net.Conn
}

func (h hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.conn, bufio.NewReadWriter(bufio.NewReader(strings.NewReader("")), bufio.NewWriter(io.Discard)), nil
}

func TestFailed200IsNotAnAllowedTunnel(t *testing.T) {
	h := newHarness(t, nil)
	a, b := net.Pipe()
	defer b.Close()
	req := &http.Request{Method: http.MethodConnect, RequestURI: "api.anthropic.com:443", Host: "api.anthropic.com:443",
		RemoteAddr: "127.0.0.1:5555", Header: http.Header{"Proxy-Authorization": {basic("s1", h.tok("s1", nonceA))}}}
	req = req.WithContext(context.Background())
	h.p.ServeHTTP(hijackRecorder{conn: failConn{a}}, req)
	if st, _ := h.p.Stats("s1"); st.Allowed != 0 {
		t.Fatalf("stats = %+v: a tunnel whose 200 never reached the client was counted", st)
	}
	if r := h.onlyRow(t); r.Outcome != OutcomeDialError || r.BytesUp != 0 || r.BytesDown != 0 {
		t.Fatalf("row = %+v", r)
	}
	// Same for a probe principal.
	user, pass, done := h.p.ProbeCredential("box-9")
	req.Header.Set("Proxy-Authorization", basic(user, pass))
	a2, b2 := net.Pipe()
	defer b2.Close()
	h.p.ServeHTTP(hijackRecorder{conn: failConn{a2}}, req)
	if res := done(); res.Allowed != 0 {
		t.Fatalf("probe result %+v", res)
	}
}

func TestDefaultResolverAsksForTheFQDN(t *testing.T) {
	var asked []string
	orig := lookupNetIP
	lookupNetIP = func(_ context.Context, network, host string) ([]netip.Addr, error) {
		asked = append(asked, network+" "+host)
		return []netip.Addr{publicAddr}, nil
	}
	defer func() { lookupNetIP = orig }()
	h := newHarness(t, nil, func(o *Options) { o.Resolve = nil })
	code, c := h.connect(t, "API.anthropic.com.:443", "s1", h.tok("s1", nonceA))
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	c.Close()
	if !reflect.DeepEqual(asked, []string{"ip api.anthropic.com."}) {
		t.Fatalf("resolver asked %q; a rooted name keeps resolv.conf search domains out", asked)
	}
}

var hostAddr = netip.MustParseAddr("93.184.216.34")

func openView() SessionView {
	return SessionView{Nonce: nonceA, Box: "box-1", Boxed: true, Live: true, AWS: nil}
}

func TestOpenModeAllowsAnyPublicHostname(t *testing.T) {
	addr := netip.MustParseAddr("93.184.216.34")
	h := newHarness(t, func(string) []netip.Addr { return []netip.Addr{addr} })
	h.setView("s1", openView())
	code, c := h.connect(t, "example.com:443", "s1", h.tok("s1", nonceA))
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	echoRoundTrip(t, c, "open")
	c.Close()
	if got, want := h.dials(), []netip.AddrPort{netip.AddrPortFrom(addr, 443)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("dialled %v, want %v", got, want)
	}
	if r := h.onlyRow(t); r.Outcome != OutcomeAllowed || r.Host != "example.com" || r.SessionID != "s1" {
		t.Fatalf("row = %+v", r)
	}
}

func TestOpenModeStillRefuses(t *testing.T) {
	cases := []struct {
		name, target, resolves string
		code                   int
		outcome                Outcome
	}{
		{"ipv4 literal", "1.1.1.1:443", "", 403, OutcomeDenied},
		{"ipv6 literal", "[::1]:443", "", 403, OutcomeDenied},
		{"port 80", "example.com:80", "", 405, OutcomeBadMethod},
		{"rfc1918", "example.com:443", "10.0.0.1", 403, OutcomePrivateAddr},
		{"cgnat", "example.com:443", "100.64.0.7", 403, OutcomePrivateAddr},
		{"loopback", "example.com:443", "127.0.0.1", 403, OutcomePrivateAddr},
		{"link-local", "example.com:443", "169.254.1.1", 403, OutcomePrivateAddr},
		{"v6 loopback", "example.com:443", "::1", 403, OutcomePrivateAddr},
		{"host address", "example.com:443", hostAddr.String(), 403, OutcomePrivateAddr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, func(string) []netip.Addr {
				if tc.resolves == "" {
					return []netip.Addr{publicAddr}
				}
				return []netip.Addr{netip.MustParseAddr(tc.resolves)}
			})
			h.p.SetHostAddrs([]netip.Addr{hostAddr})
			h.setView("s1", openView())
			code, _ := h.connect(t, tc.target, "s1", h.tok("s1", nonceA))
			if code != tc.code {
				t.Fatalf("status %d, want %d", code, tc.code)
			}
			if r := h.onlyRow(t); r.Outcome != tc.outcome {
				t.Fatalf("row = %+v, want outcome %s", r, tc.outcome)
			}
			if len(h.dials()) != 0 {
				t.Fatal("dialled")
			}
		})
	}
}

func TestHostAddrsRefusedInBoundModeToo(t *testing.T) {
	h := newHarness(t, func(string) []netip.Addr { return []netip.Addr{hostAddr} })
	h.p.SetHostAddrs([]netip.Addr{hostAddr})
	code, _ := h.connect(t, "api.anthropic.com:443", "s1", h.tok("s1", nonceA))
	if code != 403 {
		t.Fatalf("status %d", code)
	}
	if r := h.onlyRow(t); r.Outcome != OutcomePrivateAddr {
		t.Fatalf("row = %+v", r)
	}
	if st, _ := h.p.Stats("s1"); st.Denied != 1 {
		t.Fatalf("stats = %+v, want the refusal counted", st)
	}
	code, _ = h.connect(t, "example.com:443", "s1", h.tok("s1", nonceA))
	if code != 403 {
		t.Fatalf("bound is not open: status %d", code)
	}
	// Refused by the allowlist (denied), not by the address check.
	if rows := h.waitRows(t, 2); len(rows) != 2 || rows[1].Outcome != OutcomeDenied || rows[1].Host != "example.com" {
		t.Fatalf("rows = %+v", rows)
	}
	if len(h.dials()) != 0 {
		t.Fatal("dialled")
	}
}

func TestBoundModeUnallowlistedStaysDenied(t *testing.T) {
	h := newHarness(t, nil)
	h.p.SetHostAddrs([]netip.Addr{hostAddr})
	code, _ := h.connect(t, "example.com:443", "s1", h.tok("s1", nonceA))
	if code != 403 {
		t.Fatalf("status %d", code)
	}
	if r := h.onlyRow(t); r.Outcome != OutcomeDenied {
		t.Fatalf("row = %+v", r)
	}
}

func TestMixedAnswerSkipsHostAddrs(t *testing.T) {
	good := netip.MustParseAddr("8.8.8.8")
	h := newHarness(t, func(string) []netip.Addr { return []netip.Addr{hostAddr, good} })
	h.p.SetHostAddrs([]netip.Addr{hostAddr})
	h.setView("s1", openView())
	code, c := h.connect(t, "example.com:443", "s1", h.tok("s1", nonceA))
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	c.Close()
	if got, want := h.dials(), []netip.AddrPort{netip.AddrPortFrom(good, 443)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("dialled %v, want %v", got, want)
	}
}

func TestModeFollowsTheCurrentView(t *testing.T) {
	h := newHarness(t, nil)
	tok := h.tok("s1", nonceA)
	setAWS := func(b *config.AWSBinding) {
		v := h.views["s1"]
		v.AWS = b
		h.setView("s1", v)
	}
	for i, step := range []struct {
		aws  *config.AWSBinding
		want int
	}{{bound(), 403}, {nil, 200}, {bound(), 403}} {
		setAWS(step.aws)
		code, c := h.connect(t, "example.com:443", "s1", tok)
		if code != step.want {
			t.Fatalf("step %d: status %d, want %d", i, code, step.want)
		}
		c.Close()
	}
}

func ghView() SessionView {
	return SessionView{Nonce: nonceA, Box: "box-1", Boxed: true, Live: true,
		GitHub: &config.GitHubBinding{}}
}

func TestProxyGitHubSessionAllowsShard(t *testing.T) {
	const shard = "productionresultssa9.blob.core.windows.net:443"
	h := newHarness(t, nil)
	h.setView("s1", ghView())
	code, c := h.connect(t, shard, "s1", h.tok("s1", nonceA))
	if code != 200 {
		t.Fatalf("github session: status %d", code)
	}
	c.Close()
	if r := h.onlyRow(t); r.Outcome != OutcomeAllowed || r.Host != "productionresultssa9.blob.core.windows.net" {
		t.Fatalf("row = %+v", r)
	}

	// The same host from an AWS-only session is denied and audited.
	h2 := newHarness(t, nil)
	code, _ = h2.connect(t, shard, "s1", h2.tok("s1", nonceA))
	if code != 403 {
		t.Fatalf("aws-only session: status %d, want 403", code)
	}
	if r := h2.onlyRow(t); r.Outcome != OutcomeDenied {
		t.Fatalf("row = %+v", r)
	}
	if len(h2.dials()) != 0 {
		t.Fatal("dialled")
	}
}

// A GitHub-only binding is a bound session: it gets the allowlist, not open
// mode (the HIGH finding: v.AWS == nil used to mean "unbound").
func TestProxyGitHubOnlySessionNotOpen(t *testing.T) {
	h := newHarness(t, nil)
	h.setView("s1", ghView())
	code, _ := h.connect(t, "example.com:443", "s1", h.tok("s1", nonceA))
	if code != 403 {
		t.Fatalf("status %d, want 403", code)
	}
	if r := h.onlyRow(t); r.Outcome != OutcomeDenied || r.Host != "example.com" {
		t.Fatalf("row = %+v", r)
	}
	if len(h.dials()) != 0 {
		t.Fatal("dialled")
	}
}

// tokenView is a session bound to one static-token entry with a private
// exception on sentry.corp.example (port 8443) and a public host.
func tokenView() SessionView {
	return SessionView{Nonce: nonceA, Box: "box-1", Boxed: true, Live: true,
		Tokens: []config.StaticToken{{Name: "sentry", Egress: []config.TokenEgress{
			{Host: "acme.grafana.net"},
			{Host: "sentry.corp.example", Port: 8443, Private: true, CIDR: "10.20.0.0/16"},
		}}}}
}

func TestProxyPrivateExceptionExactHostOnly(t *testing.T) {
	boxAddr := netip.MustParseAddr("172.18.0.9")
	hostAddr := netip.MustParseAddr("203.0.113.77")
	vpnAddr := netip.MustParseAddr("10.20.0.7") // the host's own VPN address, inside tokenView's cidr
	priv := netip.MustParseAddr("10.20.1.5")
	cases := []struct {
		name, target string
		answer       netip.Addr
		view         func() SessionView
		wantCode     int
		wantOutcome  Outcome
	}{
		{"exact host in cidr", "sentry.corp.example:8443", priv, tokenView, 200, OutcomeAllowedPrivate},
		{"wildcard-allowed sibling gets no exception", "x.sentry.corp.example:443", priv, func() SessionView {
			v := tokenView()
			v.AWS = &config.AWSBinding{SourceProfile: "p", RoleARN: "arn:aws:iam::123456789012:role/r", EgressAllow: []string{"*.sentry.corp.example"}}
			return v
		}, 403, OutcomePrivateAddr},
		{"exact host outside cidr", "sentry.corp.example:8443", netip.MustParseAddr("10.99.0.1"), tokenView, 403, OutcomePrivateAddr},
		{"host's own address", "sentry.corp.example:8443", hostAddr, tokenView, 403, OutcomePrivateAddr},
		{"host's own address inside the cidr", "sentry.corp.example:8443", vpnAddr, tokenView, 403, OutcomePrivateAddr},
		{"box subnet address", "sentry.corp.example:8443", boxAddr, tokenView, 403, OutcomePrivateAddr},
		{"entry without a private exception", "acme.grafana.net:443", priv, tokenView, 403, OutcomePrivateAddr},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, func(string) []netip.Addr { return []netip.Addr{c.answer} })
			h.p.SetNetwork(netip.MustParsePrefix("172.18.0.0/16"), netip.MustParseAddr("172.18.0.1"), false)
			h.p.SetHostAddrs([]netip.Addr{hostAddr, vpnAddr})
			h.setView("s1", c.view())
			code, conn := h.connect(t, c.target, "s1", h.tok("s1", nonceA))
			if code != c.wantCode {
				t.Fatalf("status %d, want %d", code, c.wantCode)
			}
			if c.wantCode == 200 {
				echoRoundTrip(t, conn, "private")
				conn.Close()
				if got := h.dials(); len(got) != 1 || got[0].Addr() != c.answer {
					t.Fatalf("dialled %v", got)
				}
			} else if len(h.dials()) != 0 {
				t.Fatalf("dialled %v", h.dials())
			}
			if r := h.onlyRow(t); r.Outcome != c.wantOutcome {
				t.Fatalf("row = %+v, want outcome %s", r, c.wantOutcome)
			}
		})
	}
}

// An answer mixing public and in-cidr addresses: the exception adds the in-cidr
// ones after the public ones. When the public address fails to dial and a
// private one answers, the tunnel is recorded as allowed_private.
func TestProxyPrivateExceptionMixedAnswer(t *testing.T) {
	priv, pub := netip.MustParseAddr("10.20.1.5"), publicAddr
	h := newHarness(t, func(string) []netip.Addr {
		return []netip.Addr{priv, netip.MustParseAddr("10.20.9.9"), netip.MustParseAddr("10.99.0.1"), pub}
	})
	h.setView("s1", tokenView())
	h.mu.Lock()
	h.dialErr = func(a netip.AddrPort) error {
		if a.Addr() == pub {
			return errors.New("unreachable")
		}
		return nil
	}
	h.mu.Unlock()
	code, c := h.connect(t, "sentry.corp.example:8443", "s1", h.tok("s1", nonceA))
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	c.Close()
	want := []netip.AddrPort{netip.AddrPortFrom(pub, 8443), netip.AddrPortFrom(priv, 8443)}
	if got := h.dials(); !slices.Equal(got, want) {
		t.Fatalf("dialled %v, want %v", got, want)
	}
	if r := h.onlyRow(t); r.Outcome != OutcomeAllowedPrivate {
		t.Fatalf("row = %+v", r)
	}
}

func TestProxyDeclaredPort(t *testing.T) {
	cases := []struct {
		name, target string
		view         func() SessionView
		wantCode     int
		wantPort     uint16
	}{
		{"declared port on its host", "sentry.corp.example:8443", tokenView, 200, 8443},
		{"default 443 on a token host", "acme.grafana.net:443", tokenView, 200, 443},
		{"undeclared port on a token host", "acme.grafana.net:8443", tokenView, 405, 0},
		{"another port on the declaring host", "sentry.corp.example:8444", tokenView, 405, 0},
		{"declared host, 443 still fine", "sentry.corp.example:443", tokenView, 200, 443},
		{"declared port on another host", "api.anthropic.com:8443", tokenView, 405, 0},
		{"open mode", "example.com:8443", openView, 405, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, func(host string) []netip.Addr {
				if host == "sentry.corp.example" && c.wantCode == 200 {
					return []netip.Addr{netip.MustParseAddr("10.20.1.5")}
				}
				return []netip.Addr{publicAddr}
			})
			h.setView("s1", c.view())
			code, conn := h.connect(t, c.target, "s1", h.tok("s1", nonceA))
			if code != c.wantCode {
				t.Fatalf("status %d, want %d", code, c.wantCode)
			}
			if c.wantCode == 200 {
				conn.Close()
				if got := h.dials(); len(got) != 1 || got[0].Port() != c.wantPort {
					t.Fatalf("dialled %v, want port %d", got, c.wantPort)
				}
				return
			}
			if len(h.dials()) != 0 {
				t.Fatalf("dialled %v", h.dials())
			}
			r := h.onlyRow(t)
			if r.Outcome != OutcomeBadMethod || r.SessionID != "s1" {
				t.Fatalf("row = %+v, want bad_method with the session id", r)
			}
		})
	}
}

// Probe principals stay 443-only, and a malformed port is refused before auth.
func TestProxyPortEdges(t *testing.T) {
	h := newHarness(t, nil)
	user, pass, done := h.p.ProbeCredential("box-1")
	defer done()
	code, _ := h.connect(t, "api.anthropic.com:8443", user, pass)
	if code != 405 {
		t.Fatalf("probe on 8443: status %d", code)
	}
	for _, target := range []string{"sentry.corp.example:65536", "sentry.corp.example:0", "sentry.corp.example:08443"} {
		h2 := newHarness(t, nil)
		h2.setView("s1", tokenView())
		resp, _ := h2.connectAuth(t, target, basic("s1", h2.tok("s1", nonceA)), "")
		assertRefusal(t, resp, 405)
		if r := h2.onlyRow(t); r.Outcome != OutcomeBadMethod {
			t.Fatalf("%s: row = %+v", target, r)
		}
		if len(h2.dials()) != 0 {
			t.Fatalf("%s: dialled", target)
		}
	}
}

// ghOpenView is a GitHub-bound session whose profile set egress_open.
func ghOpenView() SessionView {
	v := ghView()
	v.EgressOpen = true
	return v
}

func TestProxyEgressOpenBound(t *testing.T) {
	h := newHarness(t, nil)
	h.setView("s1", ghOpenView())
	code, c := h.connect(t, "any.example:443", "s1", h.tok("s1", nonceA))
	if code != 200 {
		t.Fatalf("status %d, want 200", code)
	}
	c.Close()
	if r := h.onlyRow(t); r.Outcome != OutcomeAllowed || r.Host != "any.example" {
		t.Fatalf("row = %+v", r)
	}

	// A non-443 port no token entry declares is still refused.
	h2 := newHarness(t, nil)
	h2.setView("s1", ghOpenView())
	code, _ = h2.connect(t, "any.example:8443", "s1", h2.tok("s1", nonceA))
	if code != 405 {
		t.Fatalf("port 8443: status %d, want 405", code)
	}
	if r := h2.onlyRow(t); r.Outcome != OutcomeBadMethod {
		t.Fatalf("row = %+v", r)
	}
	if len(h2.dials()) != 0 {
		t.Fatal("dialled")
	}

	// Private, tailnet, loopback and host addresses are still refused.
	for _, ip := range []string{"10.0.0.5", "100.64.0.7", "127.0.0.1", hostAddr.String()} {
		h3 := newHarness(t, func(string) []netip.Addr { return []netip.Addr{netip.MustParseAddr(ip)} })
		h3.p.SetHostAddrs([]netip.Addr{hostAddr})
		h3.setView("s1", ghOpenView())
		code, _ = h3.connect(t, "any.example:443", "s1", h3.tok("s1", nonceA))
		if code != 403 {
			t.Fatalf("%s: status %d, want 403", ip, code)
		}
		if r := h3.onlyRow(t); r.Outcome != OutcomePrivateAddr {
			t.Fatalf("%s: row = %+v", ip, r)
		}
		if len(h3.dials()) != 0 {
			t.Fatalf("%s: dialled", ip)
		}
	}

	// A literal IP is still refused.
	h4 := newHarness(t, nil)
	h4.setView("s1", ghOpenView())
	if code, _ = h4.connect(t, "1.1.1.1:443", "s1", h4.tok("s1", nonceA)); code != 403 {
		t.Fatalf("ip literal: status %d, want 403", code)
	}

	// Not open: denied.
	h5 := newHarness(t, nil)
	h5.setView("s1", ghView())
	code, _ = h5.connect(t, "any.example:443", "s1", h5.tok("s1", nonceA))
	if code != 403 {
		t.Fatalf("not open: status %d, want 403", code)
	}
	if r := h5.onlyRow(t); r.Outcome != OutcomeDenied {
		t.Fatalf("row = %+v", r)
	}
}

func TestProxyProfileEgressAllow(t *testing.T) {
	v := ghView()
	v.EgressAllow = []string{"pkg.example"}
	h := newHarness(t, nil)
	h.setView("s1", v)
	code, c := h.connect(t, "pkg.example:443", "s1", h.tok("s1", nonceA))
	if code != 200 {
		t.Fatalf("pkg.example: status %d, want 200", code)
	}
	c.Close()
	h2 := newHarness(t, nil)
	h2.setView("s1", v)
	if code, _ = h2.connect(t, "other.example:443", "s1", h2.tok("s1", nonceA)); code != 403 {
		t.Fatalf("other.example: status %d, want 403", code)
	}
	if r := h2.onlyRow(t); r.Outcome != OutcomeDenied {
		t.Fatalf("row = %+v", r)
	}
}
