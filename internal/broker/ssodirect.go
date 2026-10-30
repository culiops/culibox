package broker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/culiops/agent-box/internal/awsprobe"
	"github.com/culiops/agent-box/internal/config"
)

// GateFunc runs the sso_direct read-only gate on freshly fetched permission-set
// credentials c, for the profile's region and sso_account_id. Only a
// GateReadOnly result lets c leave the host. ctx carries the time budget.
type GateFunc func(ctx context.Context, c Creds, region, account string) awsprobe.GateResult

const (
	// ssoGateTimeout bounds one gate run (the gate sets no budget of its own).
	ssoGateTimeout = 15 * time.Second
	// maxGateErrLen bounds a gate refusal in an audit row and Stats.LastError:
	// every refused item fits until then, the rest is counted.
	maxGateErrLen = 1500
)

// SetGate installs the sso_direct read-only gate. Call it before the broker
// serves; without one every sso_direct mint and check is refused.
func (b *Broker) SetGate(g GateFunc) { b.gate = g }

// serveSSODirect serves /v1/credentials for an sso_direct binding: the source
// profile's own SSO permission-set credentials, with no AssumeRole, session
// policy or SourceIdentity. The gate runs on every mint, a cache refresh
// included; there is no verdict cache. A cached credential is served while it
// has more than refreshMargin left, as in AssumeRole mode. The minted row is
// written only after the gate passed, and before the credential leaves.
func (b *Broker) serveSSODirect(w http.ResponseWriter, r *http.Request, agent, nonce string, ck connKey, scope string,
	bnd config.AWSBinding, now time.Time) {
	row := Row{Time: now, SessionID: ck.sid, Profile: agent, ScopeHash: scope, Entry: ck.conn, Mode: config.AWSModeSSODirect}

	b.mu.Lock()
	if c, ok := b.cache[ck]; ok && c.nonce == nonce && c.scopeKey == scope && c.creds.Expiration.Sub(now) > refreshMargin {
		b.eachStatsLocked(ck, func(st *Stats) { st.CacheHits++ })
		b.mu.Unlock()
		writeCreds(w, c.creds)
		return
	}
	if !b.allowMintLocked(ck, now) {
		b.mu.Unlock()
		rl := row
		rl.Outcome = OutcomeRateLimited
		b.recordDenied(rl)
		replyErr(w, http.StatusTooManyRequests, fmt.Sprintf("more than %d credential mints in a minute; slow down", mintsPerMinute))
		return
	}
	b.mu.Unlock()

	// Read from the host file (not on a cache hit), so every row of a mint names
	// the account; a failure refuses below, once the mint has said what is
	// wrong with the profile.
	account, accErr := SSOAccountOf(r.Context(), bnd.SourceProfile)
	row.AccountID = account
	gate := b.gate
	if gate == nil { // nothing is fetched that could not be handed out
		b.failSplit(w, row, OutcomeDeniedNotReadOnly, http.StatusServiceUnavailable, boxUnverified,
			"unverified ("+string(awsprobe.ReasonProbeDisabled)+"): the read-only gate is not configured")
		return
	}

	c, err := b.minter.Mint(r.Context(), MintInput{SourceProfile: bnd.SourceProfile, Region: bnd.Region,
		Mode: config.AWSModeSSODirect})
	if errors.Is(err, ErrSSOExpired) {
		b.failSSOExpired(w, row, ck, bnd.SourceProfile, err, now)
		return
	}
	if err != nil {
		code, detail := mintCode(err)
		b.failSplit(w, row, OutcomeSTSError, http.StatusBadGateway,
			fmt.Sprintf("SSO credentials could not be fetched: %s — %s", code, stsHint(code)),
			"SSO credentials failed: "+code+": "+sanitize(detail))
		return
	}
	key := b.SourceKeyOf(bnd.SourceProfile)
	b.mu.Lock()
	if _, bad := b.ssoBad[key]; bad { // the SSO login worked, so every session waiting on it can retry
		b.clearSSOLocked(key)
	}
	b.mu.Unlock()
	if c.Expiration.IsZero() { // the cache and the box need a real expiry
		b.failSplit(w, row, OutcomeSTSError, http.StatusBadGateway,
			"SSO credentials could not be fetched: MintFailed — "+stsHint("MintFailed"),
			"SSO credentials failed: the credentials have no expiration")
		return
	}
	if accErr != nil {
		b.failSplit(w, row, OutcomeDeniedNotReadOnly, http.StatusForbidden, boxNotSSO,
			"unverified (error): "+sanitize(accErr.Error()))
		return
	}

	gctx, cancel := context.WithTimeout(r.Context(), ssoGateTimeout)
	res := gate(gctx, c, bnd.Region, account)
	cancel()
	row.RoleARN = res.RoleARN
	if !gatePassed(res) {
		status, boxMsg := http.StatusServiceUnavailable, boxUnverified
		if res.State == awsprobe.GateNotReadOnly {
			status, boxMsg = http.StatusForbidden, boxNotReadOnly
		}
		b.failSplit(w, row, OutcomeDeniedNotReadOnly, status, boxMsg, gateRefusal(res))
		return
	}

	row.Outcome, row.Expires = OutcomeMinted, c.Expiration
	if err := b.rec.Record(row); err != nil {
		log.Printf("broker: audit write failed (%v); credential for %s withheld", err, ck.sid) // the row holds identifiers only
		b.mu.Lock()
		b.eachStatsLocked(ck, func(st *Stats) { st.LastError = "the credential could not be audited, so it was not issued" })
		b.mu.Unlock()
		replyErr(w, http.StatusServiceUnavailable, "agent-box could not audit this credential, so it was not issued; retry shortly")
		return
	}
	b.mu.Lock()
	b.cache[ck] = cached{creds: c, nonce: nonce, scopeKey: scope}
	b.eachStatsLocked(ck, func(st *Stats) {
		st.Mints++
		st.LastMint, st.LastError = now, ""
	})
	b.mu.Unlock()
	writeCreds(w, c) // no base-role re-probe: there is no agent role to probe
}

// Box-facing refusals. They are static: the refusing actions and reasons are
// for the host (audit row, Stats.LastError), never an oracle to the box.
const (
	boxNotReadOnly = "agent-box refused: this connection's SSO permission set is not read-only, so its credentials are not handed out; the refusing actions are in the agent-box audit log on the host"
	boxUnverified  = "agent-box could not verify that this connection's SSO permission set is read-only, so no credential was issued; retry shortly (the reason is in the agent-box audit log on the host)"
	boxNotSSO      = "agent-box refused: this connection's source profile is not usable for the SSO role directly (it needs an sso_account_id); see the agent-box audit log on the host"
)

// gatePassed is the one test that lets an sso_direct credential out: a
// read-only verdict that names the role it was made for.
func gatePassed(res awsprobe.GateResult) bool {
	return res.State == awsprobe.GateReadOnly && res.RoleARN != ""
}

// mintCode is a mint failure's safe code and its host-side detail.
func mintCode(err error) (code, detail string) {
	code, detail = "MintFailed", err.Error()
	var me *MintError
	if errors.As(err, &me) {
		code, detail = me.Code, me.Detail
	}
	return safeCode(code), detail
}

// gateRefusal is a non-passing gate result as one bounded line: the state
// (with the reason when unverified), the IAM reads to grant, then every
// refused item as "pattern: why". Past maxGateErrLen the rest is counted.
func gateRefusal(res awsprobe.GateResult) string {
	head := "not read-only"
	if res.State != awsprobe.GateNotReadOnly {
		reason := string(res.Reason)
		if reason == "" {
			reason = "no verdict"
		}
		head = "unverified (" + reason + ")"
		if res.State == awsprobe.GateReadOnly { // only reached without a role ARN
			head = "unverified (read-only verdict without a role ARN)"
		}
	}
	var items []string
	if len(res.MissingPerms) > 0 {
		items = append(items, "grant "+strings.Join(res.MissingPerms, ", "))
	}
	for _, v := range res.Refused {
		items = append(items, v.Pattern+": "+v.Why)
	}
	s := head
	for i, it := range items {
		sep := ": "
		if i > 0 {
			sep = "; "
		}
		it = sanitize(it)
		if len(s)+len(sep)+len(it) > maxGateErrLen {
			return s + fmt.Sprintf("; … and %d more", len(items)-i)
		}
		s += sep + it
	}
	return s
}

// CheckSSODirect fetches bnd's SSO permission-set credentials and runs the
// read-only gate on them, the check spawn, save and doctor make. No credential
// leaves this function. The time is the credentials' expiration, zero when
// none were fetched. It writes one probe_minted or probe_failed audit row with
// Mode set (no session: SessionID is empty). It never fails the caller: every
// failure is an Unverified or NotReadOnly result.
func (b *Broker) CheckSSODirect(ctx context.Context, bnd config.AWSBinding) (awsprobe.GateResult, time.Time) {
	now := b.now()
	gate := b.gate
	if gate == nil {
		return awsprobe.GateResult{State: awsprobe.GateUnverified, Reason: awsprobe.ReasonProbeDisabled, CheckedAt: now}, time.Time{}
	}
	account, accErr := SSOAccountOf(ctx, bnd.SourceProfile)
	row := Row{Time: now, AccountID: account, ScopeHash: bnd.Hash(), Mode: config.AWSModeSSODirect}
	c, err := b.minter.Mint(ctx, MintInput{SourceProfile: bnd.SourceProfile, Region: bnd.Region, Mode: config.AWSModeSSODirect})
	if err != nil {
		row.Outcome, row.Error = OutcomeProbeFailed, probeErrText(err)
		b.record(row)
		if code, detail := mintCode(err); code == codeNotSSOProfile || code == codeNotSSOCredentials {
			return awsprobe.GateResult{State: awsprobe.GateNotReadOnly, CheckedAt: now,
				Refused: []awsprobe.Verdict{{Pattern: "source_profile", Why: sanitize(detail)}}}, time.Time{}
		}
		return awsprobe.GateResult{State: awsprobe.GateUnverified, Reason: mintReason(ctx, err), CheckedAt: now}, time.Time{}
	}
	row.Outcome, row.Expires = OutcomeProbeMinted, c.Expiration
	var res awsprobe.GateResult
	if accErr != nil {
		res = awsprobe.GateResult{State: awsprobe.GateUnverified, Reason: awsprobe.ReasonError, CheckedAt: now}
		row.Error = "unverified (error): " + sanitize(accErr.Error())
	} else {
		gctx, cancel := context.WithTimeout(ctx, ssoGateTimeout)
		res = gate(gctx, c, bnd.Region, account)
		cancel()
		row.RoleARN = res.RoleARN
		if !gatePassed(res) {
			row.Error = gateRefusal(res)
			if res.State == awsprobe.GateReadOnly { // no role ARN: not a proof
				res.State, res.Reason = awsprobe.GateUnverified, awsprobe.ReasonError
			}
		}
	}
	b.record(row)
	return res, c.Expiration
}
