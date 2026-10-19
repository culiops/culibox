package broker

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/culiops/agent-box/internal/awsscope"
	"github.com/culiops/agent-box/internal/config"
)

const (
	credTTL        = 15 * time.Minute // STS minimum
	refreshMargin  = 5 * time.Minute
	mintsPerMinute = 10
	maxErrLen      = 300 // bytes of minter error text kept in a reply or audit row
)

// SessionView is what the broker needs to know about a session. Nonce is the
// per-spawn secret the session's token was issued with (see Token).
type SessionView struct {
	Agent string
	Nonce string
	Boxed bool
	Live  bool
	// Exclusive: the session's box is its own (an elevate binding); a grant is
	// refused without it.
	Exclusive bool
	AWS       *config.AWSBinding
	GitHub    *config.GitHubBinding
}

// Lookup resolves a session id; ok is false for an unknown session.
type Lookup func(id string) (SessionView, bool)

type Creds struct {
	AccessKeyID, SecretAccessKey, SessionToken string
	Expiration                                 time.Time
}

type MintInput struct {
	SourceProfile, Region, RoleARN, SessionName, Policy string
	Duration                                            time.Duration
}

type Minter interface {
	Mint(ctx context.Context, in MintInput) (Creds, error)
}

// ErrSSOExpired is wrapped by a Minter when the host's SSO login has lapsed.
var ErrSSOExpired = errors.New("SSO session expired")

// MintError is a failed mint. Code is the AWS error code (or a generic one) and
// is the only part of the failure the box is shown, next to a static hint;
// Detail is the full SDK text, which can name the human's SSO principal and host
// paths, so it goes only to the audit row and Stats.LastError. A Minter may
// return any error; one that is not a *MintError is treated as Code
// "MintFailed" with its text as Detail.
type MintError struct {
	Code, Detail string
	cause        error
}

func (e *MintError) Error() string { return e.Code + ": " + e.Detail }
func (e *MintError) Unwrap() error { return e.cause }

// Recorder receives one row per mint and per refused or failed request.
type Recorder interface{ Record(Row) error }

type Stats struct {
	Mints     int       `json:"mints"`
	CacheHits int       `json:"cache_hits"`
	LastMint  time.Time `json:"last_mint"`
	LastError string    `json:"last_error,omitempty"`
}

// cached is a minted credential plus what it was minted for. A hit requires the
// same spawn (nonce) and the same binding, so a session id that is reused by a
// new spawn, or a binding that was edited, never gets the old credentials.
type cached struct {
	creds    Creds
	nonce    string
	scopeKey string // the binding hash, plus the window end for an elevated credential
}

type Broker struct {
	secret []byte
	lookup Lookup
	minter Minter
	rec    Recorder
	now    func() time.Time

	mu        sync.Mutex
	cache     map[string]cached
	mintTimes map[string][]time.Time
	stats     map[string]*Stats
	denied    map[deniedKey]*deniedState
	lastPrune time.Time

	// Elevation overlay (elevate.go); all guarded by mu except after/claimCheck/appr,
	// which are set before the broker serves.
	elev       map[string]*elevation
	dropped    map[string]time.Time // sid -> latest Expiration still valid after a drop
	gen        uint64
	refused    map[refusedKey]time.Time
	lastRefuse time.Time
	after      func(time.Duration, func()) stopper
	claimCheck func(sessionID string) error
	appr       ApprovalRecorder // nil when rec cannot store approvals: every grant is refused

	// SSO expiry (sso.go); guarded by mu.
	srcKeys map[string]string    // source profile -> its sourceKey, read once
	ssoBad  map[string]time.Time // sourceKey -> when a mint last failed on it
	ssoNeed map[string]string    // session id -> the sourceKey that session's mint failed on

	// GitHub tokens (github.go); gh is set before the broker serves, the maps are guarded by mu.
	gh       GitHubMinter
	ghCache  map[ghKey]ghCached
	ghIssued map[string][]issued               // every token minted for a session, until it is revoked
	ghFlight map[string]map[*ghFlight]struct{} // mints between Mint and the listing; Forget marks them dead

	ghRevokeTimeout time.Duration  // total budget of one RevokeGitHub; New sets 5s, tests shrink it
	revokeWG        sync.WaitGroup // every revoke goroutine; tests Wait on it
}

// deniedKey scopes the audit throttle: sid is the MAC-verified session id, or
// "" for denied_token (whose claimed id is never trusted).
type deniedKey struct {
	outcome Outcome
	sid     string
}

// deniedState is the last row written for a key and how many were dropped since.
type deniedState struct {
	row        Row
	suppressed int
}

const deniedWindow = time.Second

func New(secret []byte, lookup Lookup, m Minter, rec Recorder) *Broker {
	b := &Broker{secret: secret, lookup: lookup, minter: m, rec: rec, now: time.Now,
		cache: map[string]cached{}, mintTimes: map[string][]time.Time{}, stats: map[string]*Stats{},
		denied: map[deniedKey]*deniedState{},
		elev:   map[string]*elevation{}, dropped: map[string]time.Time{}, refused: map[refusedKey]time.Time{},
		srcKeys: map[string]string{}, ssoBad: map[string]time.Time{}, ssoNeed: map[string]string{},
		ghCache: map[ghKey]ghCached{}, ghIssued: map[string][]issued{}, ghFlight: map[string]map[*ghFlight]struct{}{},
		ghRevokeTimeout: ghRevokeTimeout,
		after:           func(d time.Duration, f func()) stopper { return time.AfterFunc(d, f) }}
	b.appr, _ = rec.(ApprovalRecorder)
	return b
}

// Token is the bearer token for one spawn of a session.
func (b *Broker) Token(sessionID, nonce string) string { return Token(b.secret, sessionID, nonce) }

func (b *Broker) Stats(sessionID string) (Stats, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.stats[sessionID]
	if !ok {
		return Stats{}, false
	}
	return *s, true
}

// Forget drops everything held for a session. The session layer calls it when a
// session is removed, so per-session state does not outlive the session.
func (b *Broker) Forget(sessionID string) {
	b.mu.Lock()
	delete(b.cache, sessionID)
	delete(b.mintTimes, sessionID)
	delete(b.stats, sessionID)
	if e := b.elev[sessionID]; e != nil {
		e.timer.Stop()
		delete(b.elev, sessionID)
	}
	delete(b.dropped, sessionID)
	delete(b.ssoNeed, sessionID)
	toks := b.forgetGitHubLocked(sessionID)
	delete(b.refused, refusedKey{sessionID, CredCookie})
	delete(b.refused, refusedKey{sessionID, CredBearer})
	delete(b.refused, refusedKey{sessionID, credNone})
	b.mu.Unlock()
	b.revokeAsync(sessionID, toks) // never under b.mu: it is HTTP
}

func (b *Broker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/credentials", b.serveCredentials)
	mux.HandleFunc("/v1/github/token", b.serveGitHubToken)
	return mux
}

func (b *Broker) serveCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		replyErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	now := b.now()
	tok, hasBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	sid, nonce, ok := ParseToken(b.secret, tok)
	if !hasBearer || !ok {
		b.recordDenied(Row{Time: now, Outcome: OutcomeDeniedToken}) // the claimed id is unverified: never stored
		replyErr(w, http.StatusUnauthorized, "not an agent-box session")
		return
	}
	sv, found := b.lookup(sid)
	if !found || !sv.Live {
		// The session is gone (no live session holds this id), so its state can go too.
		b.Forget(sid)
	}
	// A nonce mismatch must NOT forget: the live session under this id is someone
	// else's, and a stale token must not reset its cache or rate-limit window.
	if reason := refusalReason(sv, found, nonce, kindAWS); reason != "" {
		b.recordDenied(Row{Time: now, SessionID: sid, Outcome: OutcomeDeniedSession, Error: reason})
		replyErr(w, http.StatusForbidden, "this session is not a live boxed session with an AWS binding") // same text for every reason: no oracle to the box
		return
	}
	bnd := *sv.AWS // a shallow copy: bnd.Elevate is shared with the session, read it only
	base := Row{Time: now, SessionID: sid, Profile: sv.Agent, AccountID: awsscope.AccountOf(bnd.RoleARN),
		RoleARN: bnd.RoleARN, SourceIdentity: awsscope.SessionName(sid), ScopeHash: bnd.Hash()}

	// Attempt 0 mints what the window allows. Attempt 1 exists only to fall back to
	// read-only after an elevated credential was discarded below; it never loops.
	for attempt := 0; attempt < 2; attempt++ {
		b.mu.Lock()
		p := b.planLocked(sid, bnd, now, attempt > 0)
		st := b.statsLocked(sid)
		if c, ok := b.cache[sid]; ok && c.nonce == nonce && c.scopeKey == p.key &&
			c.creds.Expiration.Sub(now) > refreshMargin {
			st.CacheHits++
			b.mu.Unlock()
			writeCreds(w, c.creds)
			return
		}
		if attempt == 0 && !b.allowMintLocked(sid, now) { // the fallback mint is not a second request
			b.mu.Unlock()
			row := base
			row.Outcome = OutcomeRateLimited
			b.recordDenied(row)
			replyErr(w, http.StatusTooManyRequests, fmt.Sprintf("more than %d credential mints in a minute; slow down", mintsPerMinute))
			return
		}
		b.mu.Unlock()

		partition := awsscope.PartitionOf(bnd.RoleARN)
		var policy string
		var err error
		if p.elevated {
			policy, err = awsscope.ElevatedPolicy(partition, bnd.S3Read, bnd.Elevate.AllowIAM)
		} else {
			policy, err = awsscope.SessionPolicy(partition, bnd.S3Read)
		}
		if err != nil { // validated at save; reaching here means the profile file was edited by hand
			b.fail(w, base, OutcomeSTSError, http.StatusBadGateway, "the session's AWS binding is invalid; re-save the profile")
			return
		}
		row := base
		row.RoleARN, row.Elevated = p.role, p.elevated // ScopeHash stays the binding's; these say which side minted
		c, err := b.minter.Mint(r.Context(), MintInput{SourceProfile: bnd.SourceProfile, Region: bnd.Region,
			RoleARN: p.role, SessionName: base.SourceIdentity, Policy: policy, Duration: credTTL})
		if errors.Is(err, ErrSSOExpired) {
			key := b.SourceKeyOf(bnd.SourceProfile)
			b.mu.Lock()
			b.ssoBad[key], b.ssoNeed[sid] = now, key
			b.mu.Unlock()
			boxMsg := fmt.Sprintf(
				`SSO session for profile %q expired. agent-box has asked a human to sign in; retry after they do (or run "aws sso login --profile %s" on the host).`,
				bnd.SourceProfile, bnd.SourceProfile)
			hostMsg := boxMsg
			var me *MintError
			if errors.As(err, &me) && me.Detail != "" { // a misclassified failure must stay debuggable from the audit
				hostMsg += ": " + sanitize(me.Detail)
			}
			b.failSplit(w, row, OutcomeSSOExpired, http.StatusServiceUnavailable, boxMsg, hostMsg)
			return
		}
		if err != nil {
			code, detail := "MintFailed", err.Error()
			var me *MintError
			if errors.As(err, &me) {
				code, detail = me.Code, me.Detail
			}
			code = safeCode(code)
			b.failSplit(w, row, OutcomeSTSError, http.StatusBadGateway,
				fmt.Sprintf("STS AssumeRole failed: %s — %s", code, stsHint(code)),
				"AssumeRole failed: "+code+": "+sanitize(detail))
			return
		}

		key := b.SourceKeyOf(bnd.SourceProfile)
		b.mu.Lock()
		if _, bad := b.ssoBad[key]; bad { // STS accepted this sign-in, so every session waiting on it can retry
			b.clearSSOLocked(key)
		}
		why := b.discardReasonLocked(sid, p, c, now)
		b.mu.Unlock()
		if why != "" {
			bad := row
			bad.Outcome, bad.Error = OutcomeSTSError, why
			b.record(bad)
			continue
		}
		// The minted row is written before the credential leaves (fail closed, as
		// a grant is): a credential with no audit row is never handed out, and is
		// not cached, so the next request mints and audits afresh.
		row.Outcome, row.Expires = OutcomeMinted, c.Expiration
		if err := b.rec.Record(row); err != nil {
			log.Printf("broker: audit write failed (%v); credential for %s withheld", err, sid) // the row holds identifiers only
			b.mu.Lock()
			b.statsLocked(sid).LastError = "the credential could not be audited, so it was not issued"
			b.mu.Unlock()
			replyErr(w, http.StatusServiceUnavailable, "agent-box could not audit this credential, so it was not issued; retry shortly")
			return
		}
		b.mu.Lock()
		// Checked again: the window may have changed while the row was written. The
		// request-time now is enough: a drop, extend or expiry changes gen or removes
		// the window, and the bound itself compares Expiration with p.until.
		if why := b.discardReasonLocked(sid, p, c, now); why != "" {
			b.mu.Unlock()
			bad := row
			bad.Outcome, bad.Expires, bad.Error = OutcomeSTSError, time.Time{}, why+" (after its minted row was written)"
			b.record(bad)
			continue
		}
		if p.elevated {
			b.noteElevatedExp(sid, c.Expiration) // same critical section as the check: a drop now sees it
		}
		b.cache[sid] = cached{creds: c, nonce: nonce, scopeKey: p.key}
		st = b.statsLocked(sid)
		st.Mints++
		st.LastMint, st.LastError = now, ""
		b.mu.Unlock()
		writeCreds(w, c)
		return
	}
	// Unreachable: attempt 1 is read-only and always returns. Fail closed if that changes.
	b.fail(w, base, OutcomeSTSError, http.StatusBadGateway, "no credential could be minted")
}

// discardReasonLocked says why a freshly minted elevated credential must be
// discarded rather than handed out ("" = keep it). The hard bound (spec §5.2):
// a write credential never outlives the window it was minted under, and is
// never handed out for a window that changed while STS was being called.
// Either way it is discarded, not cached, and the box gets read-only instead.
func (b *Broker) discardReasonLocked(sid string, p mintPlan, c Creds, now time.Time) string {
	if !p.elevated {
		return ""
	}
	e := b.elev[sid]
	switch {
	case !c.Expiration.After(now): // an absent or past Expiration proves nothing about the bound
		return "elevated credential has no usable expiration; minted read-only instead"
	case c.Expiration.After(p.until):
		return "elevated credential would outlive the window; minted read-only instead"
	case e == nil || e.gen != p.gen:
		return "the elevation changed during the mint; minted read-only instead"
	}
	return ""
}

// mintPlan is what one mint attempt will ask STS for. key is the cache key: the
// binding hash, plus the window end for an elevated credential so a grant,
// extend or drop never reuses a credential minted under another window.
type mintPlan struct {
	elevated  bool
	until     time.Time
	gen       uint64
	key, role string
}

// planLocked decides, under b.mu, whether a mint is elevated (spec §5.2): only
// while elevatedLocked says a window is in force with enough time left, and only
// if the binding still has an elevate role. forceRead is the read-only fallback.
func (b *Broker) planLocked(sid string, bnd config.AWSBinding, now time.Time, forceRead bool) mintPlan {
	p := mintPlan{key: bnd.Hash(), role: bnd.RoleARN}
	if forceRead || bnd.Elevate == nil {
		return p
	}
	if e, ok := b.elevatedLocked(sid, now); ok {
		p.elevated, p.until, p.gen = true, e.until, e.gen
		// gen too: Drop leaves the cache alone, so a later window ending in the
		// same second must not hit the dropped window's credential.
		p.key += "/e" + strconv.FormatInt(e.until.Unix(), 10) + "." + strconv.FormatUint(e.gen, 10)
		p.role = bnd.Elevate.RoleARN
	}
	return p
}

func (b *Broker) fail(w http.ResponseWriter, row Row, o Outcome, status int, msg string) {
	b.failSplit(w, row, o, status, msg, msg)
}

// failSplit replies to the box with boxMsg but keeps hostMsg (which may carry
// host-side detail) for the audit row and Stats.LastError.
func (b *Broker) failSplit(w http.ResponseWriter, row Row, o Outcome, status int, boxMsg, hostMsg string) {
	b.mu.Lock()
	b.statsLocked(row.SessionID).LastError = hostMsg
	b.mu.Unlock()
	row.Outcome, row.Error = o, hostMsg
	b.record(row)
	replyErr(w, status, boxMsg)
}

// safeCode keeps an error code to a short identifier, so a Minter cannot smuggle
// free text to the box through it.
func safeCode(c string) string {
	if c == "" || len(c) > 64 {
		return "MintFailed"
	}
	for _, r := range c {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return "MintFailed"
		}
	}
	return c
}

// stsHint is the static advice shown to the box for an AWS error code.
func stsHint(code string) string {
	switch code {
	case "AccessDenied":
		return "check the agent role's trust policy and the binding's role ARN"
	case "ValidationError", "MalformedPolicyDocument", "PackedPolicyTooLarge":
		return "the session policy or role binding was rejected; re-save the profile"
	case "RegionDisabledException", "InvalidClientTokenId":
		return "the region or account is not usable with this binding"
	case "ExpiredToken", "ExpiredTokenException":
		return "the host's AWS login is no longer valid"
	case "ConfigError":
		return "the binding's source profile could not be loaded on the host"
	case "Throttling", "ThrottlingException":
		return "AWS is throttling; retry shortly"
	}
	return "see the agent-box audit log on the host for details"
}

func (b *Broker) statsLocked(sid string) *Stats {
	s, ok := b.stats[sid]
	if !ok {
		s = &Stats{}
		b.stats[sid] = s
	}
	return s
}

func (b *Broker) allowMintLocked(sid string, now time.Time) bool {
	kept := b.mintTimes[sid][:0]
	for _, t := range b.mintTimes[sid] {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	if len(kept) >= mintsPerMinute {
		b.mintTimes[sid] = kept
		return false
	}
	b.mintTimes[sid] = append(kept, now)
	return true
}

// recordDenied writes at most one row per (outcome, session) per second, so a
// looping or hostile client cannot grow the audit store without bound, and a
// probe of one session cannot mask another's row. Dropped requests are counted
// and the count rides on the next row for that key ("+N similar refusals
// suppressed"). A key that goes quiet with a pending count is flushed as a
// summary row the next time the (at most once-a-second) prune runs.
func (b *Broker) recordDenied(r Row) {
	key := deniedKey{r.Outcome, r.SessionID}
	var out []Row
	b.mu.Lock()
	if b.now().Sub(b.lastPrune) >= deniedWindow {
		b.lastPrune = b.now()
		for k, st := range b.denied {
			if r.Time.Sub(st.row.Time) >= deniedWindow && k != key {
				if st.suppressed > 0 {
					sum := st.row
					sum.Time, sum.Error = r.Time, suppressedNote(st.row.Error, st.suppressed)
					out = append(out, sum)
				}
				delete(b.denied, k)
			}
		}
	}
	st, ok := b.denied[key]
	switch {
	case ok && r.Time.Sub(st.row.Time) < deniedWindow:
		st.suppressed++
	default:
		if ok && st.suppressed > 0 {
			r.Error = suppressedNote(r.Error, st.suppressed)
		}
		b.denied[key] = &deniedState{row: r}
		out = append(out, r)
	}
	b.mu.Unlock()
	for _, row := range out {
		b.record(row)
	}
}

func suppressedNote(reason string, n int) string {
	note := fmt.Sprintf("+%d similar refusals suppressed", n)
	if reason == "" {
		return note
	}
	return reason + "; " + note
}

// refusalReason names why a session may not mint ("" = it may). Stored in the
// audit row only; the HTTP reply is deliberately uniform.
func refusalReason(sv SessionView, found bool, nonce string, k bindingKind) string {
	switch {
	case !found:
		return "unknown session"
	case !sv.Live:
		return "not live"
	case !sv.Boxed:
		return "not boxed"
	case k == kindAWS && sv.AWS == nil, k == kindGitHub && sv.GitHub == nil:
		return "no binding"
	case sv.Nonce == "" || subtle.ConstantTimeCompare([]byte(sv.Nonce), []byte(nonce)) != 1:
		return "nonce mismatch"
	}
	return ""
}

func (b *Broker) record(r Row) {
	if err := b.rec.Record(r); err != nil {
		log.Printf("broker: audit write failed (%v)", err) // the row holds identifiers only, never credentials
	}
}

// sanitize makes a minter error safe to store and return: one line, bounded.
func sanitize(s string) string { return sanitizeN(s, maxErrLen) }

// sanitizeN is sanitize with an explicit byte bound.
func sanitizeN(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = strings.ToValidUTF8(s[:n], "") + "…"
	}
	return s
}

func writeCreds(w http.ResponseWriter, c Creds) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"Version": 1, "AccessKeyId": c.AccessKeyID,
		"SecretAccessKey": c.SecretAccessKey, "SessionToken": c.SessionToken,
		"Expiration": c.Expiration.UTC().Format(time.RFC3339)})
}

func replyErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
