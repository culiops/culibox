package broker

import (
	"context"
	"errors"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/culiops/agent-box/internal/awsprobe"
	"github.com/culiops/agent-box/internal/awsscope"
	"github.com/culiops/agent-box/internal/config"
)

// ProbeTarget is what a base-role probe needs from a binding.
type ProbeTarget struct {
	SourceProfile, Region, RoleARN string
	S3Read                         []string
}

// TargetOf is the probe target of a binding.
func TargetOf(b config.AWSBinding) ProbeTarget {
	return ProbeTarget{SourceProfile: b.SourceProfile, Region: b.Region, RoleARN: b.RoleARN, S3Read: slices.Clone(b.S3Read)}
}

// ProbeRole mints the base role the way a session would (same session policy,
// the given session name) and has the role simulate its own write access. It
// is the single mint+simulate implementation shared by the daemon and doctor.
// Every failure is an Unverified result with an enum reason; it never returns
// AWS error text. The probe credential never leaves this function.
func ProbeRole(ctx context.Context, m Minter, sim awsprobe.Simulator, t ProbeTarget, sessionName string, now func() time.Time) awsprobe.Result {
	policy, err := awsscope.SessionPolicy(awsscope.PartitionOf(t.RoleARN), t.S3Read)
	if err != nil {
		return awsprobe.Fail(awsprobe.ReasonError, now())
	}
	c, err := m.Mint(ctx, MintInput{SourceProfile: t.SourceProfile, Region: t.Region, RoleARN: t.RoleARN,
		SessionName: sessionName, Policy: policy, Duration: credTTL})
	if err != nil {
		return awsprobe.Fail(mintReason(ctx, err), now())
	}
	creds := aws.Credentials{AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey,
		SessionToken: c.SessionToken, Source: "agentbox-probe"}
	dec, err := sim.Simulate(ctx, creds, t.Region, t.RoleARN, awsprobe.WriteActions)
	if err != nil {
		if ctx.Err() != nil {
			return awsprobe.Fail(awsprobe.ReasonTimeout, now())
		}
		return awsprobe.Fail(awsprobe.SimulateReason(err), now())
	}
	res := awsprobe.Classify(dec)
	res.CheckedAt = now()
	return res
}

// mintReason maps a mint failure to a Reason.
func mintReason(ctx context.Context, err error) awsprobe.Reason {
	var me *MintError
	code := ""
	if errors.As(err, &me) {
		code = me.Code
	}
	switch {
	case ctx.Err() != nil || code == "Timeout":
		return awsprobe.ReasonTimeout
	case errors.Is(err, ErrSSOExpired):
		return awsprobe.ReasonSSOExpired
	case code == "AccessDenied":
		return awsprobe.ReasonAssumeDenied
	}
	return awsprobe.ReasonError
}

const (
	// probeTimeout bounds one base-role probe (mint plus simulation) in the daemon.
	probeTimeout = 5 * time.Second
	// probeCacheTTL is how long a definite result is reused. Unverified results
	// are never cached.
	probeCacheTTL = time.Hour
)

// probeKey is what a probe measures: the role as reached from a source profile.
type probeKey struct{ src, role string }

type probeCached struct {
	res awsprobe.Result
	at  time.Time
}

// SetSimulator installs the IAM simulator. Call it before the broker serves; a
// nil simulator switches the probe off (every result is probe_disabled).
func (b *Broker) SetSimulator(s awsprobe.Simulator) { b.sim = s }

// ProbeBase checks whether each AWS base role bound to a session can write. The
// result is keyed like the broker's bindings: "" for the legacy inline binding,
// otherwise the connection name. Distinct (source_profile, role_arn) pairs are
// probed in parallel, each under its own timeout; connections sharing a pair
// are probed once. A probe never touches the mint rate, the credential cache,
// Stats or the SSO state: its only side effects are an audit row per fresh
// mint, a log line and the definite-result cache. It never fails the caller.
func (b *Broker) ProbeBase(sid, agent string, bs config.Bindings) map[string]awsprobe.Result {
	// An sso_direct binding has no agent role to assume or simulate: its
	// read-only proof is the gate the spawn runs (CheckSSODirect), so it is
	// left out here and has no key in the result.
	targets := map[string]ProbeTarget{}
	if bs.AWS != nil && !bs.AWS.SSODirect() {
		targets[""] = TargetOf(*bs.AWS)
	}
	for _, c := range bs.AWSConns {
		if !c.SSODirect() {
			targets[c.Name] = TargetOf(c.AWSBinding)
		}
	}
	out := make(map[string]awsprobe.Result, len(targets))
	if b.sim == nil {
		for k := range targets {
			out[k] = awsprobe.Disabled()
		}
		return out
	}
	groups := map[probeKey][]string{}
	for k, t := range targets {
		pk := probeKey{t.SourceProfile, t.RoleARN}
		groups[pk] = append(groups[pk], k)
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, keys := range groups {
		slices.Sort(keys)
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := b.probeOne(sid, agent, keys[0], targets[keys[0]])
			mu.Lock()
			for _, k := range keys {
				out[k] = cloneResult(res) // each key its own Actions
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// probeOne probes one (source profile, role) pair: a fresh definite result from
// the cache, else a mint and simulation under its own probeTimeout. entry is
// the binding key the audit row names. Safe to call concurrently.
//
// It takes b.mu itself and may block for probeTimeout: never call it with b.mu
// held, and never on a request's reply path (the mint path runs it in its own
// goroutine, see reprobe). The returned Actions are the caller's own: cached
// results are cloned on the way in and out, so no two callers share a slice.
func (b *Broker) probeOne(sid, agent, entry string, t ProbeTarget) awsprobe.Result {
	pk := probeKey{t.SourceProfile, t.RoleARN}
	now := b.now()
	b.mu.Lock()
	if c, ok := b.probeCache[pk]; ok {
		if now.Sub(c.at) < probeCacheTTL {
			res := cloneResult(c.res)
			b.mu.Unlock()
			logProbe(t.RoleARN, res, true)
			return res
		}
		delete(b.probeCache, pk)
	}
	b.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), b.probeTimeout)
	defer cancel()
	m := probeAuditMinter{b: b, inner: b.minter, row: Row{SessionID: sid, Profile: agent,
		AccountID: awsscope.AccountOf(t.RoleARN), RoleARN: t.RoleARN, SourceIdentity: awsscope.SessionName(sid), Entry: entry}}
	res := ProbeRole(ctx, m, b.sim, t, awsscope.SessionName(sid), b.now)
	if res.Definite() {
		at := b.now()
		b.mu.Lock()
		for k, c := range b.probeCache { // bounded by the number of distinct roles; drop the stale ones
			if at.Sub(c.at) >= probeCacheTTL {
				delete(b.probeCache, k)
			}
		}
		b.probeCache[pk] = probeCached{res: cloneResult(res), at: at}
		b.mu.Unlock()
	}
	logProbe(t.RoleARN, res, false)
	return res
}

// cloneResult is r with its own Actions slice.
func cloneResult(r awsprobe.Result) awsprobe.Result {
	r.Actions = slices.Clone(r.Actions)
	return r
}

func logProbe(role string, r awsprobe.Result, cached bool) {
	s := "broker: aws base-role probe " + role + " → " + string(r.State)
	if r.State == awsprobe.Unverified {
		s += " (" + string(r.Reason) + ")"
	}
	if cached {
		s += " (cached)"
	}
	log.Print(s)
}

// probeAuditMinter wraps the broker's Minter for a probe: it writes the audit
// row for the mint and nothing else. An audit failure is logged and ignored;
// the probe credential never leaves the host.
type probeAuditMinter struct {
	b     *Broker
	inner Minter
	row   Row
}

func (m probeAuditMinter) Mint(ctx context.Context, in MintInput) (Creds, error) {
	c, err := m.inner.Mint(ctx, in)
	row := m.row
	row.Time = m.b.now()
	if err != nil {
		row.Outcome, row.Error = OutcomeProbeFailed, probeErrText(err)
	} else {
		row.Outcome, row.Expires = OutcomeProbeMinted, c.Expiration
	}
	m.b.record(row)
	return c, err
}

// probeErrText is a mint error as stored in the audit row: the STS code and a
// bounded one-line detail, like the credentials route stores it.
func probeErrText(err error) string {
	var me *MintError
	if errors.As(err, &me) {
		s := safeCode(me.Code)
		if d := sanitize(me.Detail); d != "" {
			s += ": " + d
		}
		return s
	}
	if errors.Is(err, ErrSSOExpired) {
		return "SSO session expired"
	}
	if s := sanitize(err.Error()); s != "" {
		return s
	}
	return "MintFailed"
}

// WatchBase registers the session's keys ("" for the legacy binding, else a
// connection name) whose base-role result is not definite: the next successful
// non-elevated mint of each re-probes it in the background. Each key gets a new
// watch generation, so a re-probe started for an earlier spawn under the same
// id never reports to this one. A no-op without a simulator. The session layer
// calls it before the box process starts.
func (b *Broker) WatchBase(sid string, keys []string) {
	if b.sim == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, k := range keys {
		b.watchGen++
		b.baseWatch[connKey{sid, k}] = b.watchGen
	}
}

// SetBaseNotify installs the sink for re-probe results. Call it once at boot;
// it may be after the broker started serving. It must not block for long: the
// key it reports on starts no new re-probe until it returns. A result reached
// before it is set is not delivered and leaves the watch in place, so the next
// real mint retries.
func (b *Broker) SetBaseNotify(f func(sid, key string, r awsprobe.Result)) {
	b.mu.Lock()
	b.onBase = f
	b.mu.Unlock()
}

// StopProbes stops starting re-probes and sso_direct flights and waits, at
// most d, for the ones in flight to finish (each is bounded by probeTimeout), so their audit rows are
// written before the audit store closes. For daemon shutdown.
//
// Ordering (as revokeAsync): probeWG.Add runs under b.mu and only while
// probeClosing is false, and probeClosing is set under b.mu before the Wait,
// so every Add happens-before the Wait begins.
func (b *Broker) StopProbes(d time.Duration) {
	b.mu.Lock()
	b.probeClosing = true
	b.mu.Unlock()
	idle := make(chan struct{})
	go func() { b.probeWG.Wait(); b.ssoWG.Wait(); close(idle) }() // ends when the re-probes and sso_direct flights do
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-idle:
	case <-t.C:
	}
}

// startReprobeLocked decides, under b.mu and in the critical section that
// caches a fresh credential for ck, whether that mint starts a re-probe: only
// a non-elevated mint of a watched key with none in flight, while the broker is
// not closing. On true the caller must run reprobe with gen, outside b.mu and
// after the reply is written.
func (b *Broker) startReprobeLocked(ck connKey, elevated bool) (gen uint64, ok bool) {
	if elevated || b.sim == nil || b.probeClosing {
		return 0, false
	}
	gen, watched := b.baseWatch[ck]
	if !watched {
		return 0, false
	}
	if _, busy := b.reprobing[ck]; busy {
		return 0, false
	}
	b.reprobing[ck] = gen
	b.probeWG.Add(1)
	return gen, true
}

// reprobe probes ck's base role once more and reports the result. It runs in
// its own goroutine (probeOne may block for probeTimeout).
func (b *Broker) reprobe(ck connKey, agent string, t ProbeTarget, gen uint64) {
	defer b.probeWG.Done()
	b.finishReprobe(ck, gen, b.probeOne(ck.sid, agent, ck.conn, t))
}

// finishReprobe delivers res only if the watch is still the generation the
// re-probe ran for (a Forget, or a new spawn under the same id, since it
// started means nobody is waiting for it), a sink is installed and the broker
// is not closing (the session layer may be tearing down). A result that
// endsWatch ends the watch; any other keeps it, so the next real mint retries.
// The notify runs outside b.mu.
//
// The in-flight mark is cleared only after the delivery, and only if it is
// still this re-probe's: until then no other re-probe of ck starts, so a stale
// result can never be delivered after a newer one.
//
// A Forget that lands between the generation check and the notify is not
// seen: the session layer must ignore a result for a session it no longer has.
func (b *Broker) finishReprobe(ck connKey, gen uint64, res awsprobe.Result) {
	b.mu.Lock()
	notify := b.onBase
	g, ok := b.baseWatch[ck]
	deliver := ok && g == gen && notify != nil && !b.probeClosing
	if deliver && endsWatch(res) {
		delete(b.baseWatch, ck)
	}
	b.mu.Unlock()
	if deliver {
		notify(ck.sid, ck.conn, res)
	}
	b.mu.Lock()
	if g, ok := b.reprobing[ck]; ok && g == gen {
		delete(b.reprobing, ck)
	}
	b.mu.Unlock()
}

// endsWatch reports whether a re-probe result ends the watch: a definite
// result, or a failure a sign-in cannot fix (the role cannot be assumed, or
// cannot simulate itself). SSO expiry, timeouts and other errors keep it.
func endsWatch(r awsprobe.Result) bool {
	return r.Definite() || r.Reason == awsprobe.ReasonSimulateDenied || r.Reason == awsprobe.ReasonAssumeDenied
}
