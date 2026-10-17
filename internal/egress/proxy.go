package egress

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/hostmatch"
	"github.com/culiops/agent-box/internal/sesstoken"
)

const (
	defaultIdle       = 10 * time.Minute
	defaultPerSession = 32
	defaultGlobal     = 256
	defaultProbeHost  = "api.anthropic.com"
	dialTimeout       = 10 * time.Second
	resolveTimeout    = 5 * time.Second
	probeTTL          = 60 * time.Second
	deniedWindow      = time.Second
	challenge         = `Basic realm="agent-box"`
	established       = "HTTP/1.1 200 Connection Established\r\n\r\n"
	// revoked answers a CONNECT whose session ended after it authenticated;
	// the connection is already hijacked, so it is written raw.
	revoked = "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: " + challenge +
		"\r\nConnection: close\r\nContent-Length: 0\r\n\r\n"
)

// lookupNetIP is the system resolver; a variable so a test can see what it is asked.
var lookupNetIP = net.DefaultResolver.LookupNetIP

// SessionView is what the proxy needs to know about a session.
type SessionView struct {
	Nonce       string
	Box         string
	Boxed, Live bool
	AWS         *config.AWSBinding
}

// Lookup resolves a session id; ok is false for an unknown session.
type Lookup func(id string) (SessionView, bool)

type Options struct {
	Secret      []byte
	Lookup      Lookup
	Audit       Recorder
	Resolve     func(ctx context.Context, host string) ([]netip.Addr, error)  // nil → the system resolver, asked for host + "." (rooted)
	Dial        func(ctx context.Context, a netip.AddrPort) (net.Conn, error) // nil → (&net.Dialer{}).DialContext(ctx, "tcp", a.String())
	ProbeHost   string                                                        // "" → "api.anthropic.com"
	Now         func() time.Time                                              // audit timestamps, throttle and probe expiry; nil → time.Now
	IdleTimeout time.Duration                                                 // 0 → 10m
	PerSession  int                                                           // 0 → 32
	Global      int                                                           // 0 → 256
}

type Stats struct {
	Allowed int `json:"allowed"`
	Denied  int `json:"denied"` // denied + private_addr for this session since the daemon started
}

// ProbeResult is what a probe principal actually did at the proxy: tunnels it
// opened to the probe host, and CONNECTs refused as not allowlisted.
type ProbeResult struct{ Allowed, Denied int }

// Proxy is the egress CONNECT proxy. Create it with New, call SetNetwork, then Serve.
type Proxy struct {
	o      Options
	srv    *http.Server
	ctx    context.Context // cancelled by Close: aborts in-flight resolves and dials
	cancel context.CancelFunc
	wg     sync.WaitGroup // one per held slot (reserve → release)

	mu          sync.Mutex
	closed      bool
	subnet      netip.Prefix
	gw          netip.Addr
	checkSource bool
	open        map[string]int // slots held per principal key
	total       int
	tunnels     map[*tunnel]struct{}
	stats       map[string]*Stats
	epoch       map[string]uint64 // bumped by Forget: a request authenticated before it can no longer open a tunnel or count
	probes      map[string]*probePrincipal
	denied      map[deniedKey]*deniedState
	lastPrune   time.Time
}

type tunnel struct {
	sid        string
	client, up net.Conn
	once       sync.Once
}

func (t *tunnel) close() {
	t.once.Do(func() {
		t.client.Close()
		t.up.Close()
	})
}

type probePrincipal struct {
	pass                    string
	box                     string
	expires                 time.Time
	allowedUsed, deniedUsed bool
	allowed, denied         int
}

// deniedKey scopes the audit throttle: key is the MAC-verified session id,
// "probe:<user>" for an authenticated probe principal, or "" when unauthenticated.
type deniedKey struct {
	outcome Outcome
	key     string
}

type deniedState struct {
	row        Row
	suppressed int
}

func New(o Options) *Proxy {
	if o.Resolve == nil {
		o.Resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
			// Rooted, so resolv.conf search domains (this host has
			// openstacklocal and a tailnet suffix) are never tried.
			return lookupNetIP(ctx, "ip", host+".")
		}
	}
	if o.Dial == nil {
		o.Dial = func(ctx context.Context, a netip.AddrPort) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", a.String())
		}
	}
	if o.ProbeHost == "" {
		o.ProbeHost = defaultProbeHost
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = defaultIdle
	}
	if o.PerSession <= 0 {
		o.PerSession = defaultPerSession
	}
	if o.Global <= 0 {
		o.Global = defaultGlobal
	}
	p := &Proxy{o: o, open: map[string]int{}, tunnels: map[*tunnel]struct{}{}, stats: map[string]*Stats{}, epoch: map[string]uint64{},
		probes: map[string]*probePrincipal{}, denied: map[deniedKey]*deniedState{}}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 8 << 10,
		IdleTimeout: 30 * time.Second}
	return p
}

// SetNetwork records the egress network. subnet is refused as a dial target;
// with checkSource (Linux) a connection is accepted only from inside subnet
// and not from gw itself.
func (p *Proxy) SetNetwork(subnet netip.Prefix, gw netip.Addr, checkSource bool) {
	p.mu.Lock()
	p.subnet, p.gw, p.checkSource = subnet, gw, checkSource
	p.mu.Unlock()
}

// Serve accepts on l until Close. It returns http.ErrServerClosed after Close.
func (p *Proxy) Serve(l net.Listener) error { return p.srv.Serve(l) }

// Close stops the listener, closes every open tunnel and in-flight request, and
// waits until every tunnel's goroutines have finished and been audited.
func (p *Proxy) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	ts := make([]*tunnel, 0, len(p.tunnels))
	for t := range p.tunnels {
		ts = append(ts, t)
	}
	p.mu.Unlock()
	p.cancel()
	err := p.srv.Close()
	for _, t := range ts {
		t.close()
	}
	p.wg.Wait()
	return err
}

// Token is the egress proxy password for one spawn of a session.
func (p *Proxy) Token(sessionID, nonce string) string {
	return sesstoken.Mint(sesstoken.DomainEgress, p.o.Secret, sessionID, nonce)
}

func (p *Proxy) Stats(id string) (Stats, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.stats[id]
	if !ok {
		return Stats{}, false
	}
	return *s, true
}

// Forget drops a gone session's counters and closes any tunnel it still holds.
// A CONNECT that authenticated before Forget (and is still resolving or
// dialling) gets no tunnel and counts nothing.
func (p *Proxy) Forget(id string) {
	var ts []*tunnel
	p.mu.Lock()
	delete(p.stats, id)
	p.epoch[id]++
	for t := range p.tunnels {
		if t.sid == id {
			ts = append(ts, t)
		}
	}
	p.mu.Unlock()
	for _, t := range ts {
		t.close()
	}
}

// ProbeCredential mints a probe principal for box: a random user and password,
// valid for 60 s, whose allowlist is only the probe host. It is good for one
// tunnel to the probe host and one refused CONNECT elsewhere (the two proxy
// checks of the containment probe); a slot used twice, an expired credential, or
// any use after done() is 407. done() removes the principal and reports what it
// actually did at the proxy, so a probe script cannot claim checks it never made.
func (p *Proxy) ProbeCredential(box string) (user, pass string, done func() ProbeResult) {
	user = "probe-" + sesstoken.NewNonce()
	pass = sesstoken.NewNonce() + sesstoken.NewNonce()
	pp := &probePrincipal{pass: pass, box: box}
	p.mu.Lock()
	now := p.o.Now()
	for u, old := range p.probes {
		if !now.Before(old.expires) {
			delete(p.probes, u)
		}
	}
	pp.expires = now.Add(probeTTL)
	p.probes[user] = pp
	p.mu.Unlock()
	var once sync.Once
	var res ProbeResult
	return user, pass, func() ProbeResult {
		once.Do(func() {
			p.mu.Lock()
			if p.probes[user] == pp {
				delete(p.probes, user)
			}
			res = ProbeResult{Allowed: pp.allowed, Denied: pp.denied}
			p.mu.Unlock()
		})
		return res
	}
}

// principal is an authenticated caller: a session or a probe principal.
type principal struct {
	sid   string // MAC-verified session id; "" for a probe principal
	box   string
	allow []string
	key   string // cap and throttle key
	probe *probePrincipal
	user  string
	epoch uint64 // p.epoch[sid] when authenticated
}

type authVerdict int

const (
	authOK authVerdict = iota
	authMissing
	authBad
)

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	row := Row{Time: p.o.Now()}
	if r.Method != http.MethodConnect {
		row.Host = auditHost(r.Host)
		p.refuse(w, http.StatusMethodNotAllowed, row, OutcomeBadMethod, "")
		return
	}
	// RequestURI is the raw authority-form target, before any URL unescaping.
	host, port, err := net.SplitHostPort(r.RequestURI)
	if err != nil {
		row.Host = auditHost(r.RequestURI)
		p.refuse(w, http.StatusMethodNotAllowed, row, OutcomeBadMethod, "")
		return
	}
	row.Host, row.Port = auditHost(host), auditPort(port)
	if port != "443" {
		p.refuse(w, http.StatusMethodNotAllowed, row, OutcomeBadMethod, "")
		return
	}
	if !p.sourceAllowed(r.RemoteAddr) {
		p.refuse(w, http.StatusProxyAuthRequired, row, OutcomeBadAuth, "")
		return
	}
	pr, verdict := p.authenticate(r.Header)
	switch verdict {
	case authMissing:
		// A client that waits to be challenged (git: libcurl CURLAUTH_ANY)
		// sends its first CONNECT bare. That is not an audit event (spec §7.1).
		refuseHeaders(w, http.StatusProxyAuthRequired)
		return
	case authBad:
		row.SessionID = pr.sid
		p.refuse(w, http.StatusProxyAuthRequired, row, OutcomeBadAuth, pr.key)
		return
	}
	row.SessionID, row.Box = pr.sid, pr.box

	name, ok := hostmatch.Normalize(host)
	if ok {
		row.Host = name
	}
	if !ok || !hostmatch.Match(pr.allow, name) {
		if pr.probe != nil && !p.useProbe(pr, false) {
			p.refuse(w, http.StatusProxyAuthRequired, row, OutcomeBadAuth, pr.key)
			return
		}
		p.countDenied(pr)
		p.refuse(w, http.StatusForbidden, row, OutcomeDenied, pr.key)
		return
	}
	if pr.probe != nil && !p.useProbe(pr, true) {
		p.refuse(w, http.StatusProxyAuthRequired, row, OutcomeBadAuth, pr.key)
		return
	}
	if !p.reserve(pr.key) {
		refuseHeaders(w, http.StatusServiceUnavailable) // bounded by the cap itself: no row
		return
	}
	defer p.release(pr.key)

	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(p.ctx, cancel)
	defer func() { stop(); cancel() }()

	rctx, rcancel := context.WithTimeout(ctx, resolveTimeout)
	addrs, err := p.o.Resolve(rctx, name)
	rcancel()
	if err != nil {
		p.refuse(w, http.StatusBadGateway, row, OutcomeDialError, pr.key)
		return
	}
	p.mu.Lock()
	subnet := p.subnet
	p.mu.Unlock()
	pub := PublicAddrs(addrs, subnet)
	if len(pub) == 0 {
		p.countDenied(pr)
		p.refuse(w, http.StatusForbidden, row, OutcomePrivateAddr, pr.key)
		return
	}
	var up net.Conn
	dctx, dcancel := context.WithTimeout(ctx, dialTimeout)
	for _, a := range pub {
		if c, err := p.o.Dial(dctx, netip.AddrPortFrom(a, 443)); err == nil {
			up = c
			break
		}
	}
	dcancel()
	if up == nil {
		p.refuse(w, http.StatusBadGateway, row, OutcomeDialError, pr.key)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		refuseHeaders(w, http.StatusInternalServerError)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	begin := time.Now()
	conn.SetDeadline(time.Time{}) // the server's header deadline would otherwise kill the tunnel
	t := &tunnel{sid: pr.sid, client: conn, up: up}
	if err := p.register(t, pr); err != nil {
		if err == errRevoked {
			conn.SetWriteDeadline(time.Now().Add(time.Second))
			conn.Write([]byte(revoked))
			p.recordDenied(withOutcome(row, OutcomeBadAuth), pr.key)
		}
		t.close()
		return
	}
	defer func() {
		t.close()
		p.mu.Lock()
		delete(p.tunnels, t)
		p.mu.Unlock()
	}()
	conn.SetWriteDeadline(time.Now().Add(p.o.IdleTimeout))
	if _, err := conn.Write([]byte(established)); err != nil {
		// The client never saw its tunnel: not an allowed tunnel. The dial
		// worked, so this is the closest outcome to "could not be set up".
		p.recordDenied(withOutcome(row, OutcomeDialError), pr.key)
		return
	}
	p.countAllowed(pr)
	var upN int64
	// A client may pipeline its TLS ClientHello right behind the CONNECT.
	if n := brw.Reader.Buffered(); n > 0 {
		b, _ := brw.Reader.Peek(n)
		up.SetWriteDeadline(time.Now().Add(p.o.IdleTimeout))
		k, _ := up.Write(b)
		upN += int64(k)
	}
	u, downN := splice(conn, up, p.o.IdleTimeout)
	row.Outcome, row.BytesUp, row.BytesDown, row.Duration = OutcomeAllowed, upN+u, downN, time.Since(begin)
	p.record(row)
}

func withOutcome(r Row, o Outcome) Row {
	r.Outcome = o
	return r
}

// refuse answers a refused request and audits it (throttled).
func (p *Proxy) refuse(w http.ResponseWriter, code int, row Row, o Outcome, key string) {
	row.Outcome = o
	p.recordDenied(row, key)
	refuseHeaders(w, code)
}

// refuseHeaders writes an empty refusal that closes the connection. A client
// that must authenticate reconnects for its retry (libcurl does on close).
func refuseHeaders(w http.ResponseWriter, code int) {
	w.Header().Set("Connection", "close")
	if code == http.StatusProxyAuthRequired {
		w.Header().Set("Proxy-Authenticate", challenge)
	}
	w.WriteHeader(code)
}

func (p *Proxy) sourceAllowed(remote string) bool {
	p.mu.Lock()
	check, subnet, gw := p.checkSource, p.subnet, p.gw
	p.mu.Unlock()
	return !check || sourceOK(remote, subnet, gw)
}

// sourceOK: remote is inside subnet and is not the gateway. It fails closed:
// an invalid or /0 subnet, an invalid or unspecified gateway, a gateway outside
// the subnet, or an unparsable remote is never ok.
func sourceOK(remote string, subnet netip.Prefix, gw netip.Addr) bool {
	gw = gw.WithZone("").Unmap()
	if !subnet.IsValid() || subnet.Bits() == 0 || !gw.IsValid() || gw.IsUnspecified() || !subnet.Contains(gw) {
		return false
	}
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		return false
	}
	a := ap.Addr().WithZone("").Unmap()
	return subnet.Contains(a) && a != gw
}

// authenticate checks Proxy-Authorization. On authBad, the returned principal
// carries the session id only when the token's MAC verified.
func (p *Proxy) authenticate(h http.Header) (principal, authVerdict) {
	user, pass, present, ok := basicAuth(h)
	if !present {
		return principal{}, authMissing
	}
	if !ok {
		return principal{}, authBad
	}
	p.mu.Lock()
	pp, isProbe := p.probes[user]
	expired := isProbe && !p.o.Now().Before(pp.expires)
	if expired {
		delete(p.probes, user)
	}
	p.mu.Unlock()
	if isProbe {
		if expired || subtle.ConstantTimeCompare([]byte(pass), []byte(pp.pass)) != 1 {
			return principal{}, authBad
		}
		return principal{box: pp.box, allow: []string{p.o.ProbeHost}, key: "probe:" + user, probe: pp, user: user}, authOK
	}
	sid, nonce, ok := sesstoken.Parse(sesstoken.DomainEgress, p.o.Secret, pass)
	if !ok {
		return principal{}, authBad
	}
	bad := principal{sid: sid, key: sid}
	if sid != user || p.o.Lookup == nil {
		return bad, authBad
	}
	// Captured before Lookup: a Forget that follows a "live" answer is then
	// always seen by register as a changed epoch.
	p.mu.Lock()
	epoch := p.epoch[sid]
	p.mu.Unlock()
	v, found := p.o.Lookup(sid)
	if !found || !v.Live || !v.Boxed || v.AWS == nil || v.Nonce == "" ||
		subtle.ConstantTimeCompare([]byte(v.Nonce), []byte(nonce)) != 1 {
		return bad, authBad
	}
	return principal{sid: sid, box: v.Box, allow: Effective(v.AWS), key: sid, epoch: epoch}, authOK
}

// basicAuth parses the one Proxy-Authorization header. present is false only
// when there is none; ok is false for anything but a well-formed Basic
// credential with a non-empty user.
func basicAuth(h http.Header) (user, pass string, present, ok bool) {
	vals := h.Values("Proxy-Authorization")
	if len(vals) == 0 {
		return "", "", false, false
	}
	if len(vals) != 1 {
		return "", "", true, false
	}
	scheme, cred, found := strings.Cut(strings.TrimSpace(vals[0]), " ")
	if !found || !strings.EqualFold(scheme, "basic") {
		return "", "", true, false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cred))
	if err != nil {
		return "", "", true, false
	}
	user, pass, found = strings.Cut(string(raw), ":")
	if !found || user == "" {
		return "", "", true, false
	}
	return user, pass, true, true
}

// useProbe spends one of the probe principal's two slots (one tunnel to the
// probe host, one refused CONNECT). false: the slot is spent, or the principal
// has expired or been retired by done().
func (p *Proxy) useProbe(pr principal, allowed bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	pp := pr.probe
	if p.probes[pr.user] != pp || !p.o.Now().Before(pp.expires) {
		return false
	}
	if allowed {
		if pp.allowedUsed {
			return false
		}
		pp.allowedUsed = true
		return true
	}
	if pp.deniedUsed {
		return false
	}
	pp.deniedUsed = true
	pp.denied++
	return true
}

// currentLocked: pr is a session principal whose session has not been
// forgotten since it authenticated.
func (p *Proxy) currentLocked(pr principal) bool {
	return pr.sid != "" && p.epoch[pr.sid] == pr.epoch
}

func (p *Proxy) countDenied(pr principal) {
	p.mu.Lock()
	if p.currentLocked(pr) {
		p.statsLocked(pr.sid).Denied++
	}
	p.mu.Unlock()
}

// countAllowed runs once the 200 has reached the client.
func (p *Proxy) countAllowed(pr principal) {
	p.mu.Lock()
	if p.currentLocked(pr) {
		p.statsLocked(pr.sid).Allowed++
	}
	if pr.probe != nil {
		pr.probe.allowed++
	}
	p.mu.Unlock()
}

func (p *Proxy) statsLocked(sid string) *Stats {
	s, ok := p.stats[sid]
	if !ok {
		s = &Stats{}
		p.stats[sid] = s
	}
	return s
}

// reserve takes a tunnel slot for key, refusing at either cap or once closed.
func (p *Proxy) reserve(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.open[key] >= p.o.PerSession || p.total >= p.o.Global {
		return false
	}
	p.open[key]++
	p.total++
	p.wg.Add(1)
	return true
}

func (p *Proxy) release(key string) {
	p.mu.Lock()
	if p.open[key]--; p.open[key] <= 0 {
		delete(p.open, key)
	}
	p.total--
	p.mu.Unlock()
	p.wg.Done()
}

// register makes t closable by Close and Forget. errClosed: the proxy is
// closing; errRevoked: the session was forgotten after it authenticated.
func (p *Proxy) register(t *tunnel, pr principal) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errClosed
	}
	if pr.probe == nil && !p.currentLocked(pr) {
		return errRevoked
	}
	p.tunnels[t] = struct{}{}
	return nil
}

var (
	errClosed  = errors.New("proxy closed")
	errRevoked = errors.New("session ended")
)

// recordDenied writes at most one row per (outcome, key) per second, so a
// looping or hostile client cannot grow the audit store without bound, and one
// session's refusals cannot mask another's. Dropped refusals are counted and the
// count rides on the next row for that key as Suppressed. A key that goes quiet
// with a pending count is flushed as a summary row the next time the (at most
// once-a-second) prune runs. Same scheme as the broker's.
func (p *Proxy) recordDenied(r Row, key string) {
	k := deniedKey{r.Outcome, key}
	var out []Row
	p.mu.Lock()
	if now := p.o.Now(); now.Sub(p.lastPrune) >= deniedWindow {
		p.lastPrune = now
		for kk, st := range p.denied {
			if r.Time.Sub(st.row.Time) >= deniedWindow && kk != k {
				if st.suppressed > 0 {
					sum := st.row
					sum.Time, sum.Suppressed = r.Time, st.suppressed
					out = append(out, sum)
				}
				delete(p.denied, kk)
			}
		}
	}
	st, ok := p.denied[k]
	switch {
	case ok && r.Time.Sub(st.row.Time) < deniedWindow:
		st.suppressed++
	default:
		if ok && st.suppressed > 0 {
			r.Suppressed = st.suppressed
		}
		p.denied[k] = &deniedState{row: r}
		out = append(out, r)
	}
	p.mu.Unlock()
	for _, row := range out {
		p.record(row)
	}
}

func (p *Proxy) record(r Row) {
	if p.o.Audit == nil {
		return
	}
	if err := p.o.Audit.Record(r); err != nil {
		log.Printf("egress: audit write failed (%v)", err) // rows hold identifiers only, never credentials
	}
}

// auditHost is the CONNECT host as stored: anything up to a '@' (userinfo,
// which may be a credential) becomes "*", then printable ASCII, ≤ 253 bytes.
func auditHost(h string) string {
	if i := strings.LastIndexByte(h, '@'); i >= 0 {
		h = "*" + h[i:]
	}
	return cleanHost(h)
}

func auditPort(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 65535 {
		return 0
	}
	return n
}

// splice copies both directions until both have finished, then closes both
// conns. The idle timer is shared: a direction keeps waiting while the other
// one moves, so a long one-way download is not cut.
func splice(client, up net.Conn, idle time.Duration) (upN, downN int64) {
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); upN = copyIdle(up, client, &last, idle) }()
	go func() { defer wg.Done(); downN = copyIdle(client, up, &last, idle) }()
	wg.Wait()
	client.Close()
	up.Close()
	return upN, downN
}

// copyIdle copies src to dst until EOF, an error, or idle without traffic in
// either direction, then half-closes dst. A failed write tears down both conns:
// the bytes are lost, so the tunnel cannot continue.
func copyIdle(dst, src net.Conn, last *atomic.Int64, idle time.Duration) int64 {
	buf := make([]byte, 32<<10)
	var n int64
	deadline := time.Now().Add(idle)
	for {
		src.SetReadDeadline(deadline)
		k, err := src.Read(buf)
		if k > 0 {
			last.Store(time.Now().UnixNano())
			dst.SetWriteDeadline(time.Now().Add(idle))
			w, werr := dst.Write(buf[:k])
			n += int64(w)
			if werr != nil {
				src.Close()
				dst.Close()
				return n
			}
			last.Store(time.Now().UnixNano())
			deadline = time.Now().Add(idle)
		}
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				if rem := idle - time.Since(time.Unix(0, last.Load())); rem > 0 {
					deadline = time.Now().Add(rem)
					continue
				}
			}
			break
		}
	}
	closeWrite(dst)
	return n
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok && cw.CloseWrite() == nil {
		return
	}
	c.Close()
}
