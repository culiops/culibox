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
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/culiops/culibox/internal/boxaddr"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/hostmatch"
	"github.com/culiops/culibox/internal/proxyline"
	"github.com/culiops/culibox/internal/sesstoken"
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
	// forbidden answers a CONNECT whose host the session's policy stopped
	// allowing after it was checked (see stillHolds), also written raw.
	forbidden = "HTTP/1.1 403 Forbidden\r\nConnection: close\r\nContent-Length: 0\r\n\r\n"
	// unavailable answers a CONNECT that was still being set up when a drain
	// or Close began (see register), also written raw.
	unavailable = "HTTP/1.1 503 Service Unavailable\r\nConnection: close\r\nContent-Length: 0\r\n\r\n"
	// drainTick is how often Drain looks for tunnels that have gone quiet.
	drainTick = 100 * time.Millisecond
)

// lookupNetIP is the system resolver; a variable so a test can see what it is asked.
var lookupNetIP = net.DefaultResolver.LookupNetIP

// SessionView is what the proxy needs to know about a session. No AWS, no
// GitHub and no Tokens together mean open mode: the session is boxed but has no
// binding, so any public hostname is allowed on 443 (every other check still
// applies). Any of them makes the session bound, with an allowlist. Tokens and
// PATs are the session's spawn-time token entries: their hosts join the
// allowlist, and their declared ports and private exceptions apply to exactly
// those hosts.
type SessionView struct {
	Nonce       string
	Box         string
	Boxed, Live bool
	AWS         *config.AWSBinding
	AWSConns    []config.AWSConnection
	GitHub      *config.GitHubBinding
	Tokens      []config.StaticToken
	PATs        []config.StaticToken
	// EgressAllow and EgressOpen are the profile's network policy as it applies
	// now: copied at spawn, then changed by a profile edit (#95). They matter
	// only for a bound session: EgressAllow joins its
	// allowlist, EgressOpen puts it in open mode (any public host on 443; the
	// port, private-address and host-address checks still apply). An unbound
	// session is open already.
	EgressAllow []string
	EgressOpen  bool
}

// Lookup resolves a session id; ok is false for an unknown session. It is asked
// on every CONNECT, so a session's mode follows its current view.
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
	// BoxAddr is a box's address on the egress network; with the source check
	// on, a principal is accepted only from its own box's address. nil → the
	// one Service installs (docker inspect); none at all refuses every CONNECT.
	BoxAddr func(ctx context.Context, box string) (netip.Addr, error)
	// BoxAddrs is a box-address cache shared with another component (the
	// credential broker). nil → the proxy builds its own from BoxAddr and Now.
	BoxAddrs *boxaddr.Cache
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

	mu sync.Mutex
	// closed: no new tunnel (set by Drain or Close). shut: Close has run.
	closed      bool
	shut        bool
	subnet      netip.Prefix
	hostAddrs   []netip.Addr // the host's own interface addresses: never dialled
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
	addrs       *boxaddr.Cache    // box name → its address on the egress network (own lock; may be shared)
	sidBox      map[string]string // session id → its box, so Forget can drop the entry
}

type tunnel struct {
	sid        string
	client, up net.Conn
	once       sync.Once
	// host, port and viaPrivate are what the tunnel was opened to, so a policy
	// change can tell whether the session may still hold it (Reconcile).
	host       string
	port       int
	viaPrivate bool
	// last is when the tunnel last carried a byte either way (UnixNano), shared
	// with splice's idle timer; Drain reads it to find quiet tunnels.
	last atomic.Int64
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
		probes: map[string]*probePrincipal{}, denied: map[deniedKey]*deniedState{},
		sidBox: map[string]string{}}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	if p.addrs = o.BoxAddrs; p.addrs == nil {
		p.addrs = boxaddr.New(p.ctx, o.BoxAddr, o.Now)
	}
	p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 8 << 10,
		IdleTimeout: 30 * time.Second}
	return p
}

// SetNetwork records the egress network. subnet is refused as a dial target;
// with checkSource a connection is accepted only from inside subnet and not
// from gw itself, and a principal only from its own box's address. On macOS the
// address is the one the relay's PROXY line carries (see Service.Ready).
func (p *Proxy) SetNetwork(subnet netip.Prefix, gw netip.Addr, checkSource bool) {
	p.mu.Lock()
	p.subnet, p.gw, p.checkSource = subnet, gw, checkSource
	p.mu.Unlock()
}

// SetHostAddrs records the host's own interface addresses, which no tunnel may
// dial in any mode. It stores a normalised copy.
func (p *Proxy) SetHostAddrs(a []netip.Addr) {
	c := make([]netip.Addr, 0, len(a))
	for _, x := range a {
		if x.IsValid() {
			c = append(c, x.WithZone("").Unmap())
		}
	}
	p.mu.Lock()
	p.hostAddrs = c
	p.mu.Unlock()
}

// Serve accepts on l until Close. It returns http.ErrServerClosed after Close.
func (p *Proxy) Serve(l net.Listener) error { return p.srv.Serve(l) }

// DrainResult is what a drain did with the tunnels open when it began.
type DrainResult struct {
	Open  int // tunnels open when the drain began
	Quiet int // closed by the drain after carrying no byte for the quiet window
	Left  int // still open when ctx ended; Close cuts them
}

// Drain lets the open tunnels finish before Close (#362). It closes the
// listener, so a new connection is refused, and answers a CONNECT still being
// set up with 503. A tunnel that carries no byte for quiet is closed: an idle
// keep-alive connection would otherwise hold the drain to its bound, and its
// client opens a new one when it needs it. Drain returns once no tunnel is
// left, or when ctx ends with some still open; Close cuts those. Every tunnel
// is audited when it ends, as always. A second call returns at once.
func (p *Proxy) Drain(ctx context.Context, quiet time.Duration) DrainResult {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return DrainResult{}
	}
	p.closed = true
	res := DrainResult{Open: len(p.tunnels)}
	p.mu.Unlock()
	// Shutdown closes the listener at once; the rest of its wait (in-flight
	// CONNECTs, which register now refuses) is bounded by ctx, and Close ends it.
	go p.srv.Shutdown(ctx)
	tick := time.NewTicker(drainTick)
	defer tick.Stop()
	quieted := map[*tunnel]bool{}
	for {
		var stale []*tunnel
		p.mu.Lock()
		for t := range p.tunnels {
			if !quieted[t] && time.Since(time.Unix(0, t.last.Load())) >= quiet {
				stale = append(stale, t)
			}
		}
		busy := p.total
		p.mu.Unlock()
		for _, t := range stale {
			quieted[t] = true
			t.close()
			res.Quiet++
		}
		if busy == 0 {
			return res
		}
		select {
		case <-ctx.Done():
			p.mu.Lock()
			for t := range p.tunnels {
				if !quieted[t] {
					res.Left++
				}
			}
			p.mu.Unlock()
			return res
		case <-tick.C:
		}
	}
}

// Close stops the listener, closes every open tunnel and in-flight request, and
// waits until every tunnel's goroutines have finished and been audited. After a
// Drain it cuts whatever the drain left open.
func (p *Proxy) Close() error {
	p.mu.Lock()
	if p.shut {
		p.mu.Unlock()
		return nil
	}
	p.closed, p.shut = true, true
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
	if b, ok := p.sidBox[id]; ok {
		delete(p.sidBox, id)
		p.dropBoxLocked(b)
	}
	// Never deleted: an in-flight request may still hold epoch 0. One small
	// entry per ended bound session over the daemon's life is accepted.
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

// Reconcile closes every tunnel of session id that its current policy no
// longer admits, and reports how many it closed (#95). The session manager
// calls it after it has stored a session's new policy, so the lookup here reads
// that policy. A session the lookup no longer knows, or that has ended, keeps
// none. A tunnel still allowed is left alone.
func (p *Proxy) Reconcile(id string) int {
	p.mu.Lock()
	var ts []*tunnel
	for t := range p.tunnels {
		if t.sid == id {
			ts = append(ts, t)
		}
	}
	p.mu.Unlock()
	if len(ts) == 0 {
		return 0
	}
	pol, live := p.currentPolicy(id)
	n := 0
	for _, t := range ts {
		if !live || !pol.holds(t.host, t.port, t.viaPrivate) {
			t.close()
			n++
		}
	}
	return n
}

// PolicyChanged is Reconcile plus one audit row for the change: outcome
// policy_changed, the summary (with the number of tunnels closed) as its host.
// It is never throttled: one row per session per profile change. A failed
// audit write is logged and the change stands, so a narrowing is never undone
// by the audit store.
func (p *Proxy) PolicyChanged(id, box, summary string) int {
	n := p.Reconcile(id)
	if n > 0 {
		// First: the host column is cut at 253 bytes, and the count must survive.
		summary = strings.TrimSpace("closed:" + strconv.Itoa(n) + " " + summary)
	}
	p.record(Row{Time: p.o.Now(), SessionID: id, Box: box, Host: cleanHost(summary), Outcome: OutcomePolicyChanged})
	return n
}

// currentPolicy reads session id's policy through the lookup, with none of
// p's locks held. live is false for a session that is unknown, has ended or is
// not boxed: it may hold nothing.
func (p *Proxy) currentPolicy(id string) (pol policy, live bool) {
	if p.o.Lookup == nil {
		return policy{}, false
	}
	v, found := p.o.Lookup(id)
	if !found || !v.Live || !v.Boxed {
		return policy{}, false
	}
	return policyOf(v), true
}

// stillHolds re-reads t's session policy and reports whether it still admits t.
func (p *Proxy) stillHolds(t *tunnel) bool {
	pol, live := p.currentPolicy(t.sid)
	return live && pol.holds(t.host, t.port, t.viaPrivate)
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
	// A probe verifies a box that was just (re)created, maybe under the name
	// of one whose address is cached: look it up afresh.
	p.dropBoxLocked(box)
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
	policy
	sid   string // MAC-verified session id; "" for a probe principal
	box   string
	key   string // cap and throttle key
	probe *probePrincipal
	user  string
	epoch uint64 // p.epoch[sid] when authenticated
}

// policy is what a principal may reach, built from its session's current view
// (policyOf) at authenticate, and again whenever a tunnel is re-checked.
type policy struct {
	allow  []string
	open   bool // open mode: unbound, or a bound profile with egress_open; no allowlist
	github bool // GitHub-bound: GitHub's Actions-log shard hosts are allowed
	// ports maps an exact entry host to the extra (non-443) TLS ports its
	// entries declare; private lists the (exact host, cidr) exceptions. An
	// unbound open policy has neither; a bound open one keeps its token-entry
	// ports and private exceptions; probes have none.
	ports   map[string]map[int]bool
	private []privateRule
}

// policyOf is the policy of a session view. No binding means open mode; a
// bound session gets its allowlist, and open mode only with egress_open.
func policyOf(v SessionView) policy {
	bs := config.Bindings{AWS: v.AWS, AWSConns: v.AWSConns, GitHub: v.GitHub, Tokens: v.Tokens, PATs: v.PATs, EgressAllow: v.EgressAllow}
	if !bs.Any() {
		return policy{open: true}
	}
	pol := policy{allow: Effective(bs), github: v.GitHub != nil}
	pol.ports, pol.private = entryRules(v.Tokens, v.PATs)
	// Open mode for a bound session skips only the host allowlist: the port
	// check and the private-address filtering run for every principal.
	pol.open = v.EgressOpen
	return pol
}

// portOK: 443 is fine for every allowed host; any other port only for the
// exact host of a token entry that declares it.
func (pol policy) portOK(name string, port int) bool {
	return port == 443 || pol.ports[name][port]
}

// hostOK: name (normalised) passes the allowlist, or the policy is open.
func (pol policy) hostOK(name string) bool {
	return pol.open || hostmatch.Match(pol.allow, name) || (pol.github && GitHubResultsHost(name))
}

// holds reports whether pol still admits a tunnel opened to host:port, through
// a private exception when viaPrivate.
func (pol policy) holds(host string, port int, viaPrivate bool) bool {
	if !pol.portOK(host, port) || !pol.hostOK(host) {
		return false
	}
	return !viaPrivate || slices.ContainsFunc(pol.private, func(r privateRule) bool { return r.host == host })
}

// privateRule is one token entry's private exception: the exact normalised
// host, and the one cidr its resolved addresses may fall in.
type privateRule struct {
	host string
	cidr netip.Prefix
}

// entryRules reads the declared ports and private exceptions out of a session's
// token entries. A host or cidr that does not parse yields no rule (fail
// closed); config validation has already refused such entries at load.
func entryRules(lists ...[]config.StaticToken) (map[string]map[int]bool, []privateRule) {
	var ports map[string]map[int]bool
	var rules []privateRule
	for _, list := range lists {
		for _, e := range list {
			for _, h := range e.Egress {
				name, ok := hostmatch.Normalize(h.Host)
				if !ok {
					continue
				}
				if port := h.EffectivePort(); port != 443 {
					if ports == nil {
						ports = map[string]map[int]bool{}
					}
					if ports[name] == nil {
						ports[name] = map[int]bool{}
					}
					ports[name][port] = true
				}
				if h.Private {
					if c, err := netip.ParsePrefix(h.CIDR); err == nil {
						rules = append(rules, privateRule{host: name, cidr: c.Masked()})
					}
				}
			}
		}
	}
	return ports, rules
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
	// Canonical decimal 1-65535 only ("0443", "+443" and the like are junk);
	// which numbers a session may use is decided after it authenticates.
	portN, perr := strconv.Atoi(port)
	if perr != nil || portN < 1 || portN > 65535 || strconv.Itoa(portN) != port {
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
	// A principal is good only from its own box. Anything else is answered
	// exactly as a bad or stale credential: nothing says the token was valid.
	if !p.boxSourceAllowed(r.Context(), pr.box, r.RemoteAddr) {
		if pr.probe != nil {
			p.refuse(w, http.StatusProxyAuthRequired, row, OutcomeBadAuth, "")
			return
		}
		row.SessionID = pr.sid
		p.refuse(w, http.StatusProxyAuthRequired, row, OutcomeBadAuth, pr.key)
		return
	}
	if pr.probe == nil {
		p.mu.Lock()
		if p.currentLocked(pr) {
			p.sidBox[pr.sid] = pr.box
		}
		p.mu.Unlock()
	}
	row.SessionID, row.Box = pr.sid, pr.box

	name, ok := hostmatch.Normalize(host)
	if ok {
		row.Host = name
	}
	// 443 is fine for every allowed host; any other port only for the exact
	// host of a token entry that declares it. Unbound open principals and probes have none; a bound open one keeps
	// its token-entry ports.
	if !pr.portOK(name, portN) {
		p.refuse(w, http.StatusMethodNotAllowed, row, OutcomeBadMethod, pr.key)
		return
	}
	if !ok || !pr.hostOK(name) {
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
	subnet, hostAddrs := p.subnet, p.hostAddrs
	p.mu.Unlock()
	// Dial set: the public addresses, plus (only for a host with a private
	// exception, matched exactly) its in-cidr private ones. The box subnet is
	// out of both, and the host's own addresses are removed last.
	pub := PublicAddrs(addrs, subnet)
	var priv []netip.Addr
	for _, rule := range pr.private {
		if rule.host == name {
			priv = append(priv, PrivateAddrs(addrs, rule.cidr, subnet)...)
		}
	}
	pub = WithoutAddrs(append(pub, priv...), hostAddrs)
	if len(pub) == 0 {
		p.countDenied(pr)
		p.refuse(w, http.StatusForbidden, row, OutcomePrivateAddr, pr.key)
		return
	}
	var up net.Conn
	var viaPrivate bool // the address that answered came from the private set
	dctx, dcancel := context.WithTimeout(ctx, dialTimeout)
	for _, a := range pub {
		if c, err := p.o.Dial(dctx, netip.AddrPortFrom(a, uint16(portN))); err == nil {
			up, viaPrivate = c, slices.Contains(priv, a)
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
	t := &tunnel{sid: pr.sid, client: conn, up: up, host: name, port: portN, viaPrivate: viaPrivate}
	t.last.Store(time.Now().UnixNano())
	if err := p.register(t, pr); err != nil {
		conn.SetWriteDeadline(time.Now().Add(time.Second))
		switch err {
		case errRevoked:
			conn.Write([]byte(revoked))
			p.recordDenied(withOutcome(row, OutcomeBadAuth), pr.key)
		case errClosed:
			conn.Write([]byte(unavailable)) // draining or closing: no tunnel, and no row (like the cap's 503)
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
	// The policy may have narrowed while this request resolved and dialled
	// (#95). Read it again now that the tunnel is registered: a Reconcile that
	// runs from here on sees the tunnel, and one that ran before has already
	// stored the policy this lookup reads.
	if pr.probe == nil && !p.stillHolds(t) {
		conn.SetWriteDeadline(time.Now().Add(time.Second))
		conn.Write([]byte(forbidden))
		p.countDenied(pr)
		p.recordDenied(withOutcome(row, OutcomeDenied), pr.key)
		return
	}
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
	u, downN := splice(conn, up, p.o.IdleTimeout, &t.last)
	row.Outcome, row.BytesUp, row.BytesDown, row.Duration = OutcomeAllowed, upN+u, downN, time.Since(begin)
	if viaPrivate {
		row.Outcome = OutcomeAllowedPrivate
	}
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
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		return false
	}
	return proxyline.InSubnetNotGateway(subnet, gw)(ap.Addr())
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
		return principal{policy: policy{allow: []string{p.o.ProbeHost}}, box: pp.box, key: "probe:" + user, probe: pp, user: user}, authOK
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
	if !found || !v.Live || !v.Boxed || v.Nonce == "" ||
		subtle.ConstantTimeCompare([]byte(v.Nonce), []byte(nonce)) != 1 {
		return bad, authBad
	}
	return principal{policy: policyOf(v), sid: sid, box: v.Box, key: sid, epoch: epoch}, authOK
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
// one moves, so a long one-way download is not cut. last is the tunnel's
// activity time, which Drain reads too.
func splice(client, up net.Conn, idle time.Duration, last *atomic.Int64) (upN, downN int64) {
	last.Store(time.Now().UnixNano())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); upN = copyIdle(up, client, last, idle) }()
	go func() { defer wg.Done(); downN = copyIdle(client, up, last, idle) }()
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

// probeSeen is what the probe principal user has done so far, without retiring
// it. The 200 of an allowed tunnel is written before it is counted, so Probe
// uses this to let that last count land before it reads done().
func (p *Proxy) probeSeen(user string) ProbeResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pp := p.probes[user]; pp != nil {
		return ProbeResult{Allowed: pp.allowed, Denied: pp.denied}
	}
	return ProbeResult{}
}
