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

// ssoMintTimeout bounds the mint of one sso_direct flight. STSMinter applies
// the same bound itself; this one holds for any Minter.
const ssoMintTimeout = mintTimeout

// ssoFlightKey names one in-flight sso_direct mint and gate: the cache key
// (session and connection) of one spawn (nonce) of one definition (scope), so
// a new spawn or an edited binding never joins an older flight.
type ssoFlightKey struct {
	ck           connKey
	nonce, scope string
}

// ssoFlight is one detached mint and gate. res is written before done closes.
type ssoFlight struct {
	done    chan struct{}
	res     ssoOutcome
	waiters int // requests that started or joined it; guarded by mu
}

// ssoOutcome is what a flight answers every request that waited on it: the
// credential (status 200) or a refusal's status and box message.
type ssoOutcome struct {
	creds  Creds
	status int
	msg    string
}

// boxPending answers a request that ended before its flight did.
const boxPending = "agent-box is still checking that this connection's SSO permission set is read-only; retry shortly"

// serveSSODirect serves /v1/credentials for an sso_direct binding: the source
// profile's own SSO permission-set credentials, with no AssumeRole, session
// policy or SourceIdentity. The gate runs on every mint, a cache refresh
// included; there is no verdict cache. A cached credential is served while it
// has more than refreshMargin left, as in AssumeRole mode.
//
// The mint and gate run detached from the request (the box's helper gives up
// after 10 s, and a slow but read-only permission set must still get through):
// one flight per flight key, which concurrent and retried requests join
// instead of starting another, and which only the first request counts against
// the mint rate. A flight is bounded by its own deadlines (ssoMintTimeout,
// then ssoGateTimeout) and leaves the flight table when it ends; a passing
// credential is cached first, so a retry after a dropped request is served
// from the cache.
func (b *Broker) serveSSODirect(w http.ResponseWriter, r *http.Request, agent, nonce string, ck connKey, scope string,
	bnd config.AWSBinding, now time.Time) {
	fk := ssoFlightKey{ck, nonce, scope}
	b.mu.Lock()
	if c, ok := b.cache[ck]; ok && c.nonce == nonce && c.scopeKey == scope && c.creds.Expiration.Sub(now) > refreshMargin {
		b.eachStatsLocked(ck, func(st *Stats) { st.CacheHits++ })
		b.mu.Unlock()
		writeCreds(w, c.creds)
		return
	}
	f, joined := b.ssoFlights[fk]
	if !joined {
		if b.probeClosing { // the daemon is stopping: no detached work starts
			b.mu.Unlock()
			replyErr(w, http.StatusServiceUnavailable, "agent-box is shutting down; no credential was issued")
			return
		}
		if !b.allowMintLocked(ck, now) {
			b.mu.Unlock()
			b.recordDenied(Row{Time: now, SessionID: ck.sid, Profile: agent, ScopeHash: scope, Entry: ck.conn,
				Mode: config.AWSModeSSODirect, Outcome: OutcomeRateLimited})
			replyErr(w, http.StatusTooManyRequests, fmt.Sprintf("more than %d credential mints in a minute; slow down", mintsPerMinute))
			return
		}
		f = &ssoFlight{done: make(chan struct{})}
		b.ssoFlights[fk] = f
		b.ssoWG.Add(1) // under mu while !probeClosing: StopProbes' Wait sees it
		go b.runSSOFlight(fk, f, agent, bnd, now)
	}
	f.waiters++
	b.mu.Unlock()

	select {
	case <-f.done:
		if f.res.status == http.StatusOK {
			writeCreds(w, f.res.creds)
		} else {
			replyErr(w, f.res.status, f.res.msg)
		}
	case <-r.Context().Done():
		// Not a timeout of the check: the requester left (the box helper's own
		// deadline, or a dropped connection). The flight goes on and writes
		// its own audit row; a passing credential waits in the cache.
		log.Printf("broker: sso_direct credential request of %s (connection %q) ended by the client before the mint and read-only gate finished; the check continues and a passing credential is cached for the retry",
			ck.sid, ck.conn)
		replyErr(w, http.StatusServiceUnavailable, boxPending)
	}
}

// runSSOFlight runs one flight to the end. A passing credential is cached, and
// counted, only while the flight is still registered (a Forget of the session
// removes it); either way every waiting request gets the outcome. The cache
// write and the removal from the table are one critical section, so a request
// either joins the flight or finds the cached credential.
func (b *Broker) runSSOFlight(fk ssoFlightKey, f *ssoFlight, agent string, bnd config.AWSBinding, now time.Time) {
	defer b.ssoWG.Done()
	out := b.mintAndGate(fk.ck, agent, fk.scope, bnd, now)
	b.mu.Lock()
	if b.ssoFlights[fk] == f {
		delete(b.ssoFlights, fk)
		if out.status == http.StatusOK {
			b.cache[fk.ck] = cached{creds: out.creds, nonce: fk.nonce, scopeKey: fk.scope}
			b.eachStatsLocked(fk.ck, func(st *Stats) {
				st.Mints++
				st.LastMint, st.LastError = now, ""
			})
		}
	}
	f.res = out
	b.mu.Unlock()
	close(f.done)
}

// mintAndGate fetches the permission-set credentials and gates them, on
// contexts of its own (never the request's). Every refusal is audited here;
// the minted row is written only after the gate passed, and a credential
// whose row cannot be written is withheld.
func (b *Broker) mintAndGate(ck connKey, agent, scope string, bnd config.AWSBinding, now time.Time) ssoOutcome {
	row := Row{Time: now, SessionID: ck.sid, Profile: agent, ScopeHash: scope, Entry: ck.conn, Mode: config.AWSModeSSODirect}
	// Read from the host file, so every row of a mint names the account; a
	// failure refuses below, once the mint has said what is wrong with the profile.
	account, accErr := SSOAccountOf(context.Background(), bnd.SourceProfile)
	row.AccountID = account
	gate := b.gate
	if gate == nil { // nothing is fetched that could not be handed out
		return b.refuse(row, OutcomeDeniedNotReadOnly, http.StatusServiceUnavailable, boxUnverified,
			"unverified ("+string(awsprobe.ReasonProbeDisabled)+"): the read-only gate is not configured")
	}

	mctx, cancel := context.WithTimeout(context.Background(), ssoMintTimeout)
	c, err := b.minter.Mint(mctx, MintInput{SourceProfile: bnd.SourceProfile, Region: bnd.Region, Mode: config.AWSModeSSODirect})
	cancel()
	if errors.Is(err, ErrSSOExpired) {
		return ssoOutcome{status: http.StatusServiceUnavailable, msg: b.noteSSOExpired(row, ck, bnd.SourceProfile, err, now)}
	}
	if err != nil {
		code, detail := mintCode(err)
		return b.refuse(row, OutcomeSTSError, http.StatusBadGateway,
			fmt.Sprintf("SSO credentials could not be fetched: %s — %s", code, stsHint(code)),
			"SSO credentials failed: "+code+": "+sanitize(detail))
	}
	key := b.SourceKeyOf(bnd.SourceProfile)
	b.mu.Lock()
	if _, bad := b.ssoBad[key]; bad { // the SSO login worked, so every session waiting on it can retry
		b.clearSSOLocked(key)
	}
	b.mu.Unlock()
	if c.Expiration.IsZero() { // the cache and the box need a real expiry
		return b.refuse(row, OutcomeSTSError, http.StatusBadGateway,
			"SSO credentials could not be fetched: MintFailed — "+stsHint("MintFailed"),
			"SSO credentials failed: the credentials have no expiration")
	}
	if accErr != nil {
		status, boxMsg := http.StatusServiceUnavailable, boxUnverified // a host file that could not be read: retry
		if errors.Is(accErr, ErrNoSSOAccount) {
			status, boxMsg = http.StatusForbidden, boxNotSSO
		}
		return b.refuse(row, OutcomeDeniedNotReadOnly, status, boxMsg, "unverified (error): "+sanitize(accErr.Error()))
	}

	gctx, cancel := context.WithTimeout(context.Background(), ssoGateTimeout)
	res := gate(gctx, c, bnd.Region, account)
	cancel()
	row.RoleARN = res.RoleARN
	if !gatePassed(res) {
		status, boxMsg := http.StatusServiceUnavailable, boxUnverified
		if res.State == awsprobe.GateNotReadOnly {
			status, boxMsg = http.StatusForbidden, boxNotReadOnly
		}
		return b.refuse(row, OutcomeDeniedNotReadOnly, status, boxMsg, gateRefusal(res))
	}

	row.Outcome, row.Expires = OutcomeMinted, c.Expiration
	if err := b.rec.Record(row); err != nil {
		log.Printf("broker: audit write failed (%v); credential for %s withheld", err, ck.sid) // the row holds identifiers only
		b.mu.Lock()
		b.eachStatsLocked(ck, func(st *Stats) { st.LastError = "the credential could not be audited, so it was not issued" })
		b.mu.Unlock()
		return ssoOutcome{status: http.StatusServiceUnavailable,
			msg: "agent-box could not audit this credential, so it was not issued; retry shortly"}
	}
	return ssoOutcome{status: http.StatusOK, creds: c} // no base-role re-probe: there is no agent role to probe
}

// refuse audits a refused flight (Stats.LastError and the row carry hostMsg)
// and returns what its requests are told.
func (b *Broker) refuse(row Row, o Outcome, status int, boxMsg, hostMsg string) ssoOutcome {
	b.noteFail(row, o, hostMsg)
	return ssoOutcome{status: status, msg: boxMsg}
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
	return b.checkSSODirect(ctx, bnd, bnd.Hash(), "")
}

// CheckSSODirectConn is CheckSSODirect for a named connection: its row carries
// the connection's name and DefHash, as the rows of its mints do.
func (b *Broker) CheckSSODirectConn(ctx context.Context, c config.AWSConnection) (awsprobe.GateResult, time.Time) {
	return b.checkSSODirect(ctx, c.AWSBinding, c.DefHash(), c.Name)
}

// checkSSODirect is CheckSSODirect with the row's scope hash and entry.
func (b *Broker) checkSSODirect(ctx context.Context, bnd config.AWSBinding, scope, entry string) (awsprobe.GateResult, time.Time) {
	now := b.now()
	gate := b.gate
	if gate == nil {
		return awsprobe.GateResult{State: awsprobe.GateUnverified, Reason: awsprobe.ReasonProbeDisabled, CheckedAt: now}, time.Time{}
	}
	account, accErr := SSOAccountOf(ctx, bnd.SourceProfile)
	row := Row{Time: now, AccountID: account, ScopeHash: scope, Entry: entry, Mode: config.AWSModeSSODirect}
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
	switch {
	case errors.Is(accErr, ErrNoSSOAccount): // the profile can never pass
		res = awsprobe.GateResult{State: awsprobe.GateNotReadOnly, CheckedAt: now,
			Refused: []awsprobe.Verdict{{Pattern: "source_profile", Why: sanitize(accErr.Error())}}}
		row.Error = "not read-only: " + sanitize(accErr.Error())
	case accErr != nil: // the host file could not be read: retry
		res = awsprobe.GateResult{State: awsprobe.GateUnverified, Reason: awsprobe.ReasonError, CheckedAt: now}
		row.Error = "unverified (error): " + sanitize(accErr.Error())
	default:
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
