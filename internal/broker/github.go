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

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/ghapp"
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
	owner        string
	installation int64
}

// SetGitHub installs the minter behind /v1/github/token. Call it before the
// broker serves; until then the route answers 503.
func (b *Broker) SetGitHub(m GitHubMinter) { b.gh = m }

// CheckGitHub verifies, for every distinct App a binding names, that the host
// can mint with it (key readable, App known).
func (b *Broker) CheckGitHub(g config.GitHubBinding) error {
	if b.gh == nil {
		return errors.New("GitHub is not configured")
	}
	seen := map[string]bool{}
	for _, o := range g.Owners {
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

// forgetGitHubLocked drops a session's GitHub state. b.mu must be held.
func (b *Broker) forgetGitHubLocked(sid string) {
	for k := range b.ghCache {
		if k.sid == sid {
			delete(b.ghCache, k)
		}
	}
	delete(b.ghIssued, sid)
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
	if reason := refusalReason(sv, found, nonce, kindGitHub); reason != "" {
		b.recordDenied(Row{Time: now, SessionID: sid, Provider: ProviderGitHub, Outcome: OutcomeDeniedSession, Error: reason})
		replyErr(w, http.StatusForbidden, "this session is not a live boxed session with a GitHub binding") // same text for every reason: no oracle to the box
		return
	}
	if b.gh == nil {
		replyErr(w, http.StatusServiceUnavailable, "GitHub is not configured on this agent-box host")
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
	if req.Repo != "" && !hasRepo(entry.Repos, req.Repo) {
		deny("repo not bound")
		return
	}
	base.Subject = entry.Owner
	base.Repos = len(entry.Repos)

	key := ghKey{sid, strings.ToLower(entry.Owner)}
	b.mu.Lock()
	if c, ok := b.ghCache[key]; ok && c.nonce == nonce && c.scopeKey == base.ScopeHash &&
		c.expires.Sub(now) > ghRefreshMargin {
		b.mu.Unlock()
		writeGitHubToken(w, c.token, c.expires)
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

	// Outside b.mu: this is an HTTP round trip to GitHub. Concurrent first
	// requests may each mint; every token is listed in ghIssued below.
	m, err := b.gh.Mint(r.Context(), ghapp.MintInput{App: entry.App, Owner: entry.Owner,
		Repos: entry.Repos, Permissions: gb.EffectivePermissions()})
	if err != nil {
		b.ghMintFailed(w, base, entry.Owner, err)
		return
	}
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
		return
	}
	b.mu.Lock()
	b.ghIssued[sid] = append(b.ghIssued[sid], issued{value: m.Value, owner: entry.Owner, installation: m.InstallationID})
	b.ghCache[key] = ghCached{token: m.Value, expires: m.ExpiresAt, nonce: nonce, scopeKey: base.ScopeHash}
	b.mu.Unlock()
	writeGitHubToken(w, m.Value, m.ExpiresAt)
}

// ghMintFailed maps a minter error to its outcome, audit row and reply.
func (b *Broker) ghMintFailed(w http.ResponseWriter, base Row, owner string, err error) {
	var se *ghapp.ScopeError
	switch {
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
	_ = json.NewEncoder(w).Encode(map[string]string{"token": token, "expires_at": expires.UTC().Format(time.RFC3339)})
}
