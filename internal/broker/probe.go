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
	targets := map[string]ProbeTarget{}
	if bs.AWS != nil {
		targets[""] = TargetOf(*bs.AWS)
	}
	for _, c := range bs.AWSConns {
		targets[c.Name] = TargetOf(c.AWSBinding)
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
				out[k] = res
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
func (b *Broker) probeOne(sid, agent, entry string, t ProbeTarget) awsprobe.Result {
	pk := probeKey{t.SourceProfile, t.RoleARN}
	now := b.now()
	b.mu.Lock()
	if c, ok := b.probeCache[pk]; ok {
		if now.Sub(c.at) < probeCacheTTL {
			b.mu.Unlock()
			logProbe(t.RoleARN, c.res, true)
			return c.res
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
		b.probeCache[pk] = probeCached{res: res, at: at}
		b.mu.Unlock()
	}
	logProbe(t.RoleARN, res, false)
	return res
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
