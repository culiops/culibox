package broker

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
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
	AWS   *config.AWSBinding
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
	creds     Creds
	nonce     string
	scopeHash string
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
	return &Broker{secret: secret, lookup: lookup, minter: m, rec: rec, now: time.Now,
		cache: map[string]cached{}, mintTimes: map[string][]time.Time{}, stats: map[string]*Stats{},
		denied: map[deniedKey]*deniedState{}}
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
	defer b.mu.Unlock()
	delete(b.cache, sessionID)
	delete(b.mintTimes, sessionID)
	delete(b.stats, sessionID)
}

func (b *Broker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/credentials", b.serveCredentials)
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
	if reason := refusalReason(sv, found, nonce); reason != "" {
		b.recordDenied(Row{Time: now, SessionID: sid, Outcome: OutcomeDeniedSession, Error: reason})
		replyErr(w, http.StatusForbidden, "this session is not a live boxed session with an AWS binding") // same text for every reason: no oracle to the box
		return
	}
	bnd := *sv.AWS
	base := Row{Time: now, SessionID: sid, Profile: sv.Agent, AccountID: awsscope.AccountOf(bnd.RoleARN),
		RoleARN: bnd.RoleARN, SourceIdentity: awsscope.SessionName(sid), ScopeHash: bnd.Hash()}

	b.mu.Lock()
	st := b.statsLocked(sid)
	if c, ok := b.cache[sid]; ok && c.nonce == nonce && c.scopeHash == base.ScopeHash &&
		c.creds.Expiration.Sub(now) > refreshMargin {
		st.CacheHits++
		b.mu.Unlock()
		writeCreds(w, c.creds)
		return
	}
	if !b.allowMintLocked(sid, now) {
		b.mu.Unlock()
		row := base
		row.Outcome = OutcomeRateLimited
		b.recordDenied(row)
		replyErr(w, http.StatusTooManyRequests, fmt.Sprintf("more than %d credential mints in a minute; slow down", mintsPerMinute))
		return
	}
	b.mu.Unlock()

	policy, err := awsscope.SessionPolicy(awsscope.PartitionOf(bnd.RoleARN), bnd.S3Read)
	if err != nil { // validated at save; reaching here means the profile file was edited by hand
		b.fail(w, base, OutcomeSTSError, http.StatusBadGateway, "the session's AWS binding is invalid; re-save the profile")
		return
	}
	c, err := b.minter.Mint(r.Context(), MintInput{SourceProfile: bnd.SourceProfile, Region: bnd.Region,
		RoleARN: bnd.RoleARN, SessionName: base.SourceIdentity, Policy: policy, Duration: credTTL})
	if errors.Is(err, ErrSSOExpired) {
		b.fail(w, base, OutcomeSSOExpired, http.StatusServiceUnavailable, fmt.Sprintf(
			`SSO session for profile %q expired — run "aws sso login --profile %s" on the host, then retry`,
			bnd.SourceProfile, bnd.SourceProfile))
		return
	}
	if err != nil {
		b.fail(w, base, OutcomeSTSError, http.StatusBadGateway, "AssumeRole failed: "+sanitize(err.Error()))
		return
	}
	b.mu.Lock()
	b.cache[sid] = cached{creds: c, nonce: nonce, scopeHash: base.ScopeHash}
	st = b.statsLocked(sid)
	st.Mints++
	st.LastMint, st.LastError = now, ""
	b.mu.Unlock()
	row := base
	row.Outcome, row.Expires = OutcomeMinted, c.Expiration
	b.record(row)
	writeCreds(w, c)
}

func (b *Broker) fail(w http.ResponseWriter, row Row, o Outcome, status int, msg string) {
	b.mu.Lock()
	b.statsLocked(row.SessionID).LastError = msg
	b.mu.Unlock()
	row.Outcome, row.Error = o, msg
	b.record(row)
	replyErr(w, status, msg)
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
func refusalReason(sv SessionView, found bool, nonce string) string {
	switch {
	case !found:
		return "unknown session"
	case !sv.Live:
		return "not live"
	case !sv.Boxed:
		return "not boxed"
	case sv.AWS == nil:
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
func sanitize(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxErrLen {
		s = strings.ToValidUTF8(s[:maxErrLen], "") + "…"
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
