package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credref"
	"github.com/culiops/culibox/internal/ghapp"
	"github.com/culiops/culibox/internal/secretstore"
)

// ghRefreshMargin: a cached GitHub token is reused only while more than this
// remains, so a long git fetch or `gh run view --log` never starts on a token
// that is about to die.
const ghRefreshMargin = 10 * time.Minute

// maxGitHubBody bounds the request body: it is two short names.
const maxGitHubBody = 4 << 10

// GitHubMinter turns a binding into installation tokens. *ghapp.Minter is the
// real one.
type GitHubMinter interface {
	Check(app string) error
	Mint(ctx context.Context, in ghapp.MintInput) (ghapp.Minted, error)
	Revoke(ctx context.Context, token string) error
}

// bindingKind says which binding a request needs the session to hold.
type bindingKind int

const (
	kindAWS bindingKind = iota
	kindGitHub
	kindStatic
)

// ghKey scopes the token cache: one token per session and owner.
type ghKey struct{ sid, owner string } // owner is lower-cased

// ghCached is a minted token plus what it was minted for. A hit requires the
// same spawn (nonce) and the same binding.
type ghCached struct {
	token    string
	expires  time.Time
	nonce    string
	scopeKey string // the binding hash
}

// issued is a token handed to a session. Every one stays listed until it is
// revoked, including tokens a refresh superseded: a running git may hold them.
type issued struct {
	value        string
	expires      time.Time // zero when unknown
	owner        string
	installation int64
}

// ghRevokeTimeout is the default total budget for revoking one session's tokens.
const ghRevokeTimeout = 5 * time.Second

// ghTokenTimeout caps one GitHub Revoke call, so a single slow call cannot use
// up the budget the session's other tokens share.
const ghTokenTimeout = 2 * time.Second

// ghFlight marks one mint that has left b.mu for GitHub. Forget and
// RevokeAllGitHub set dead, so a mint that returns after its session was torn
// down is revoked instead of listed.
type ghFlight struct{ dead bool }

// ghConfigured reports whether /v1/github/token can mint: a minter is
// installed and, if it can say so, has an App defined. Revoking needs only the
// installed minter (the token is its own credential), so it checks b.gh alone.
func (b *Broker) ghConfigured() bool {
	if b.gh == nil {
		return false
	}
	if c, ok := b.gh.(interface{ Configured() bool }); ok {
		return c.Configured()
	}
	return true
}

// SetGitHub installs the minter behind /v1/github/token. Call it before the
// broker serves; until then the route answers 503.
func (b *Broker) SetGitHub(m GitHubMinter) { b.gh = m }

// CheckGitHub verifies, for every distinct App a binding names, that the host
// can mint with it (key readable, App known). An owner that uses a PAT entry
// needs no App, so a binding of only those passes with no minter configured.
func (b *Broker) CheckGitHub(g config.GitHubBinding) error {
	seen := map[string]bool{}
	for _, o := range g.Owners {
		if o.Token != "" {
			continue
		}
		if !b.ghConfigured() {
			return errors.New("GitHub is not configured")
		}
		if seen[o.App] {
			continue
		}
		seen[o.App] = true
		if err := b.gh.Check(o.App); err != nil {
			return err
		}
	}
	return nil
}

// forgetGitHubLocked drops a session's GitHub state and returns the tokens the
// caller must now revoke. b.mu must be held; the revoke itself must not be.
func (b *Broker) forgetGitHubLocked(sid string) []issued {
	for k := range b.ghCache {
		if k.sid == sid {
			delete(b.ghCache, k)
		}
	}
	for f := range b.ghFlight[sid] {
		f.dead = true
	}
	delete(b.ghFlight, sid)
	toks := b.ghIssued[sid]
	delete(b.ghIssued, sid)
	return toks
}

// RevokeGitHub revokes, at GitHub, every token minted for the session and
// forgets them. It returns at once; the revoking runs in one goroutine bounded
// by ghRevokeTimeout. A second call finds nothing left to revoke.
func (b *Broker) RevokeGitHub(sessionID string) {
	b.mu.Lock()
	toks := b.forgetGitHubLocked(sessionID)
	b.mu.Unlock()
	b.revokeAsync(sessionID, toks)
}

// RevokeAllGitHub revokes every session's tokens, synchronously, until ctx is
// done, and waits (within ctx) for revokes started earlier. From now on
// /v1/github/token answers 503 and mints nothing. For daemon shutdown.
func (b *Broker) RevokeAllGitHub(ctx context.Context) {
	b.mu.Lock()
	b.closing = true
	sids := map[string]struct{}{}
	for sid := range b.ghIssued {
		sids[sid] = struct{}{}
	}
	for sid := range b.ghFlight {
		sids[sid] = struct{}{}
	}
	all := map[string][]issued{}
	for sid := range sids {
		all[sid] = b.forgetGitHubLocked(sid)
	}
	b.mu.Unlock()
	for sid, toks := range all {
		b.revokeTokens(ctx, sid, toks)
	}
	idle := make(chan struct{})
	go func() { b.revokeWG.Wait(); close(idle) }() // ends when the earlier revokes do (each has its own timeout)
	select {
	case <-idle:
	case <-ctx.Done():
	}
}

// revokeAsync revokes toks in one goroutine that ends within ghRevokeTimeout.
//
// Ordering: revokeWG.Add runs under b.mu and only while closing is false.
// RevokeAllGitHub sets closing under b.mu before its revokeWG.Wait, so every
// Add happens-before that Wait begins; an Add can never race a Wait (that is a
// WaitGroup misuse and can panic). Once closing is set, nothing may be added:
// the caller revokes inline instead. That caller is the HTTP handler of a
// tombstoned mint (srv.Shutdown tracks it) or a late Forget, both bounded by
// ghRevokeTimeout and the per-token timeout.
func (b *Broker) revokeAsync(sid string, toks []issued) {
	if len(toks) == 0 {
		return
	}
	timeout := b.ghRevokeTimeout
	b.mu.Lock()
	if b.closing {
		b.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		b.revokeTokens(ctx, sid, toks)
		return
	}
	b.revokeWG.Add(1)
	b.mu.Unlock()
	go func() {
		defer b.revokeWG.Done()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		b.revokeTokens(ctx, sid, toks)
	}()
}

// revokeTokens revokes each token and writes one revoked or revoke_failed row
// per token. A token already past its expiry is skipped: GitHub has nothing
// left to revoke, so there is no call and no row. Each call gets at most
// ghTokenTimeout of ctx's remaining time, so one slow call cannot starve the
// rest. It never holds b.mu, and the rows never carry a token.
func (b *Broker) revokeTokens(ctx context.Context, sid string, toks []issued) {
	for _, t := range toks {
		if !t.expires.IsZero() && !t.expires.After(b.now()) {
			continue
		}
		var err error
		switch {
		case b.gh == nil:
			err = errors.New("GitHub is not configured")
		case ctx.Err() != nil:
			err = ctx.Err()
		default:
			cctx, cancel := context.WithTimeout(ctx, b.ghTokenTimeout)
			err = b.gh.Revoke(cctx, t.value)
			cancel()
		}
		row := Row{Time: b.now(), SessionID: sid, Provider: ProviderGitHub, Subject: t.owner,
			Installation: t.installation, Outcome: OutcomeRevoked}
		if err != nil {
			row.Outcome = OutcomeRevokeFailed
			row.Error = sanitize(strings.ReplaceAll(err.Error(), t.value, "[token]"))
		}
		b.record(row)
	}
}

// ghBeginLocked registers a mint that is about to leave b.mu. b.mu must be held.
func (b *Broker) ghBeginLocked(sid string) *ghFlight {
	f := &ghFlight{}
	if b.ghFlight[sid] == nil {
		b.ghFlight[sid] = map[*ghFlight]struct{}{}
	}
	b.ghFlight[sid][f] = struct{}{}
	return f
}

func (b *Broker) ghEnd(sid string, f *ghFlight) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.ghFlight[sid], f)
	if len(b.ghFlight[sid]) == 0 {
		delete(b.ghFlight, sid)
	}
}

type ghTokenRequest struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
}

func (b *Broker) serveGitHubToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		replyErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	now := b.now()
	tok, hasBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	sid, nonce, ok := ParseToken(b.secret, tok)
	if !hasBearer || !ok {
		b.recordDenied(Row{Time: now, Provider: ProviderGitHub, Outcome: OutcomeDeniedToken}) // the claimed id is unverified: never stored
		replyErr(w, http.StatusUnauthorized, "not an agent-box session")
		return
	}
	sv, found := b.lookup(sid)
	if !found || !sv.Live {
		b.Forget(sid)
	}
	if o, why := b.refusal(r, sv, found, nonce, kindGitHub); o != "" {
		b.recordDenied(Row{Time: now, SessionID: sid, Provider: ProviderGitHub, Outcome: o, Error: why})
		replyErr(w, http.StatusForbidden, "this session is not a live boxed session with a GitHub binding") // same text for every reason: no oracle to the box
		return
	}

	var req ghTokenRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGitHubBody))
	if err := dec.Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			replyErr(w, http.StatusRequestEntityTooLarge, "request too large")
			return
		}
		replyErr(w, http.StatusBadRequest, `expected {"owner":"...","repo":"..."}`)
		return
	}
	if req.Owner == "" {
		replyErr(w, http.StatusBadRequest, `expected {"owner":"...","repo":"..."}`)
		return
	}

	gb := *sv.GitHub // read-only view of the session's binding
	base := Row{Time: now, SessionID: sid, Profile: sv.Agent, Provider: ProviderGitHub, ScopeHash: gb.Hash()}
	deny := func(why string) {
		row := base
		if config.ValidGitHubOwner(req.Owner) {
			row.Subject = req.Owner // only a well-formed login is stored
		}
		row.Outcome, row.Error = OutcomeDeniedScope, why
		b.recordDenied(row)
		replyErr(w, http.StatusForbidden, "this session's GitHub binding does not cover that owner or repository")
	}
	if !config.ValidGitHubOwner(req.Owner) || (req.Repo != "" && !config.ValidGitHubRepo(req.Repo)) {
		deny("malformed owner or repo")
		return
	}
	entry, ok := gb.OwnerEntry(req.Owner)
	if !ok {
		deny("owner not bound")
		return
	}
	pat := entry.Token != ""
	if pat {
		base.Entry = entry.Token // every PAT row, denials included, names the entry
	}
	if pat && req.Repo == "" {
		// A PAT is not narrowed at GitHub, so the repos check below is the only
		// scope there is: an empty repo would skip it.
		row := base
		row.Subject, row.Outcome, row.Error = entry.Owner, OutcomeDeniedScope, "repo required for a PAT owner"
		b.recordDenied(row)
		replyErr(w, http.StatusForbidden, "name the repository: a personal access token is not scoped by agent-box")
		return
	}
	if req.Repo != "" && !hasRepo(entry.Repos, req.Repo) {
		deny("repo not bound")
		return
	}
	base.Subject = entry.Owner
	base.Repos = len(entry.Repos)
	if pat {
		b.serveGitHubPAT(w, base, sv, entry, sid, now, deny)
		return
	}
	if !b.ghConfigured() {
		replyErr(w, http.StatusServiceUnavailable, "GitHub is not configured on this agent-box host")
		return
	}

	key := ghKey{sid, strings.ToLower(entry.Owner)}
	b.mu.Lock()
	// closing is tested here, in the same b.mu section that registers the
	// flight, and not earlier: the box controls when its body arrives, so an
	// earlier check could pass, RevokeAllGitHub could then sweep, and this
	// request would register its flight after the sweep (never marked dead) and
	// mint a token that outlives shutdown. RevokeAllGitHub sets closing under
	// b.mu, so a flight registered below was registered before the sweep and is
	// marked dead by it.
	if b.closing {
		b.mu.Unlock()
		replyErr(w, http.StatusServiceUnavailable, "agent-box is shutting down")
		return
	}
	if c, ok := b.ghCache[key]; ok && c.nonce == nonce && c.scopeKey == base.ScopeHash &&
		c.expires.Sub(now) > ghRefreshMargin {
		b.mu.Unlock()
		writeGitHubToken(w, c.token, c.expires)
		return
	}
	if !b.allowLocked(sid, bucketMint, now) {
		b.mu.Unlock()
		row := base
		row.Outcome = OutcomeRateLimited
		b.recordDenied(row)
		replyErr(w, http.StatusTooManyRequests, fmt.Sprintf("more than %d credential mints in a minute; slow down", mintsPerMinute))
		return
	}
	fl := b.ghBeginLocked(sid)
	b.mu.Unlock()
	defer b.ghEnd(sid, fl)

	// Outside b.mu: this is an HTTP round trip to GitHub. Concurrent first
	// requests may each mint; every token is listed in ghIssued below.
	m, err := b.gh.Mint(r.Context(), ghapp.MintInput{App: entry.App, Owner: entry.Owner,
		Repos: entry.Repos, Permissions: gb.EffectivePermissions()})
	if err != nil {
		b.ghMintFailed(w, base, sid, entry.Owner, err)
		return
	}
	b.storeUnlocked(sid)                          // a mint reads the App key from wherever it lives; it worked
	if m.Value == "" || !m.ExpiresAt.After(now) { // never hand out or cache a token that is already dead
		b.ghFail(w, base, OutcomeGitHubError, http.StatusBadGateway,
			"GitHub returned an unusable token", "GitHub returned an empty or already-expired token")
		return
	}

	// The minted row is written before the token leaves (fail closed): a token
	// with no audit row is never handed out, cached or listed. It is minted and
	// unused, and expires on its own within the hour.
	row := base
	row.Outcome, row.Installation, row.Expires = OutcomeMinted, m.InstallationID, m.ExpiresAt
	if err := b.rec.Record(row); err != nil {
		log.Printf("broker: audit write failed (%v); GitHub token for %s withheld", err, sid) // the row holds identifiers only
		replyErr(w, http.StatusServiceUnavailable, "agent-box could not audit this credential, so it was not issued; retry shortly")
		b.revokeAsync(sid, []issued{{value: m.Value, owner: entry.Owner, installation: m.InstallationID, expires: m.ExpiresAt}}) // withheld, so kill it
		return
	}
	// The session may have ended while GitHub was minting. Forget marks this
	// mint dead (it closes the window between lookup and b.mu); lookup also
	// catches a new spawn under the same id. lookup runs before b.mu, as it
	// does everywhere else.
	cur, curFound := b.lookup(sid)
	o, _ := b.refusal(r, cur, curFound, nonce, kindGitHub) // the same judge, with the box this request came from
	stillLive := o == ""
	b.mu.Lock()
	if fl.dead || !stillLive || b.closing {
		closing := b.closing
		b.mu.Unlock()
		b.revokeAsync(sid, []issued{{value: m.Value, owner: entry.Owner, installation: m.InstallationID, expires: m.ExpiresAt}})
		if closing {
			replyErr(w, http.StatusServiceUnavailable, "agent-box is shutting down")
			return
		}
		replyErr(w, http.StatusForbidden, "this session is not a live boxed session with a GitHub binding")
		return
	}
	b.ghIssued[sid] = append(b.ghIssued[sid], issued{value: m.Value, owner: entry.Owner, installation: m.InstallationID, expires: m.ExpiresAt})
	b.ghCache[key] = ghCached{token: m.Value, expires: m.ExpiresAt, nonce: nonce, scopeKey: base.ScopeHash}
	b.mu.Unlock()
	writeGitHubToken(w, m.Value, m.ExpiresAt)
}

// serveGitHubPAT answers for an owner bound to a PAT entry: the value is read
// at fetch time, never cached, listed or revoked (agent-box does not own it),
// and the fetched row is written before it leaves (fail closed). It counts
// against the static bucket: with no cache every git or gh call lands here.
func (b *Broker) serveGitHubPAT(w http.ResponseWriter, base Row, sv SessionView, owner config.GitHubOwner,
	sid string, now time.Time, deny func(string)) {
	base.Entry = owner.Token
	var pat config.StaticToken
	found := false
	for _, e := range sv.PATs {
		if e.Name == owner.Token {
			pat, found = e, true
			break
		}
	}
	if !found {
		deny("token entry not defined")
		return
	}
	b.mu.Lock()
	allowed := b.allowLocked(sid, bucketStatic, now)
	b.mu.Unlock()
	if !allowed {
		row := base
		row.Outcome = OutcomeRateLimited
		b.recordDenied(row)
		replyErr(w, http.StatusTooManyRequests, fmt.Sprintf("more than %d token fetches in a minute; slow down", staticFetchesPerMinute))
		return
	}
	value, err := b.resolve.Resolve(pat.Value)
	if errors.Is(err, credref.ErrLocked) {
		b.storeLockedReply(w, base, sid)
		return
	}
	if b.storeUnavailableReply(w, base, err) {
		return
	}
	if err != nil {
		msg := sanitize(err.Error()) // names the reference, never the value
		row := base
		row.Outcome, row.Error = OutcomeUnresolved, msg
		b.recordDenied(row)
		replyErr(w, http.StatusServiceUnavailable, msg)
		return
	}
	b.storeUnlocked(sid)
	row := base
	row.Outcome = OutcomeFetched
	if err := b.rec.Record(row); err != nil {
		log.Printf("broker: audit write failed (%v); GitHub PAT for %s withheld", err, sid) // the row holds identifiers only
		replyErr(w, http.StatusServiceUnavailable, "agent-box could not audit this token fetch, so it was not issued; retry shortly")
		return
	}
	writeGitHubToken(w, string(value), time.Time{})
}

// ghMintFailed maps a minter error to its outcome, audit row and reply.
func (b *Broker) ghMintFailed(w http.ResponseWriter, base Row, sid, owner string, err error) {
	var se *ghapp.ScopeError
	var ue *secretstore.UnavailableError
	switch {
	case errors.Is(err, credref.ErrLocked): // the App key is in the store and it is locked
		b.ghFail(w, base, OutcomeStoreLocked, http.StatusServiceUnavailable, storeLockedMsg, storeLockedMsg)
		b.storeWasLocked(sid)
	case errors.As(err, &ue): // the App key is in the store and the Keychain failed
		b.ghFail(w, base, OutcomeStoreUnavailable, http.StatusServiceUnavailable,
			boxUnavailableMsg(ue.Kind), sanitizeN(ue.Error(), maxUnavailableLen))
	case errors.Is(err, ghapp.ErrNotInstalled):
		msg := "install the GitHub App on " + owner
		b.ghFail(w, base, OutcomeNotInstalled, http.StatusForbidden, msg, msg)
	case errors.As(err, &se):
		msg := "GitHub refused the token scope: " + sanitize(se.Msg)
		b.ghFail(w, base, OutcomeScopeError, http.StatusForbidden, msg, msg)
	default:
		// The box gets a static message: the detail can name host key paths.
		b.ghFail(w, base, OutcomeGitHubError, http.StatusBadGateway,
			"GitHub token request failed; see the agent-box audit log on the host", sanitize(err.Error()))
	}
}

// ghFail records a failed request and replies; boxMsg goes to the box, hostMsg
// to the audit row.
func (b *Broker) ghFail(w http.ResponseWriter, row Row, o Outcome, status int, boxMsg, hostMsg string) {
	row.Outcome, row.Error = o, hostMsg
	b.record(row)
	replyErr(w, status, boxMsg)
}

func hasRepo(repos []string, name string) bool {
	for _, r := range repos {
		if strings.EqualFold(r, name) { // GitHub repo names are case-insensitive
			return true
		}
	}
	return false
}

func writeGitHubToken(w http.ResponseWriter, token string, expires time.Time) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	out := map[string]string{"token": token}
	if !expires.IsZero() { // a PAT has no expiry the broker knows
		out["expires_at"] = expires.UTC().Format(time.RFC3339)
	}
	_ = json.NewEncoder(w).Encode(out)
}
