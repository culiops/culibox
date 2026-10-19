package broker

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/credref"
)

// maxStaticBody bounds the request body: it is one short name.
const maxStaticBody = 4 << 10

// SetResolver replaces how a static token's value is read. Call it before the
// broker serves; New defaults to credref.Default{}.
func (b *Broker) SetResolver(r credref.Resolver) { b.resolve = r }

type staticTokenRequest struct {
	Name string `json:"name"`
}

// serveStaticToken hands a boxed session the value of a token entry it is bound
// to. The value is read at fetch time, never cached or stored. The fetched row
// is written before the value leaves (fail closed); refusals are best-effort.
func (b *Broker) serveStaticToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		replyErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	now := b.now()
	tok, hasBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	sid, nonce, ok := ParseToken(b.secret, tok)
	if !hasBearer || !ok {
		b.recordDenied(Row{Time: now, Provider: ProviderStatic, Outcome: OutcomeDeniedToken}) // the claimed id is unverified: never stored
		replyErr(w, http.StatusUnauthorized, "not an agent-box session")
		return
	}
	sv, found := b.lookup(sid)
	if !found || !sv.Live {
		b.Forget(sid)
	}
	if reason := refusalReason(sv, found, nonce, kindStatic); reason != "" {
		b.recordDenied(Row{Time: now, SessionID: sid, Provider: ProviderStatic, Outcome: OutcomeDeniedSession, Error: reason})
		replyErr(w, http.StatusForbidden, "this session is not a live boxed session with a token binding") // same text for every reason: no oracle to the box
		return
	}

	var req staticTokenRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxStaticBody))
	if err := dec.Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			replyErr(w, http.StatusRequestEntityTooLarge, "request too large")
			return
		}
		replyErr(w, http.StatusBadRequest, `expected {"name":"..."}`)
		return
	}
	if !config.ValidTokenName(req.Name) {
		replyErr(w, http.StatusBadRequest, `expected {"name":"..."}`)
		return
	}

	base := Row{Time: now, SessionID: sid, Profile: sv.Agent, Provider: ProviderStatic, Subject: req.Name}
	entry, bound := config.StaticToken{}, false
	for _, e := range sv.Tokens {
		if e.Name == req.Name {
			entry, bound = e, true
			break
		}
	}
	if !bound {
		row := base
		row.Outcome, row.Error = OutcomeDeniedScope, "token entry not bound"
		b.recordDenied(row)
		replyErr(w, http.StatusForbidden, fmt.Sprintf("this session has no token entry named %q", req.Name))
		return
	}
	base.ScopeHash = entry.DefHash()

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

	value, err := b.resolve.Resolve(entry.Value)
	if err != nil {
		msg := sanitize(err.Error()) // names the reference, never the value
		row := base
		row.Outcome, row.Error = OutcomeUnresolved, msg
		b.recordDenied(row)
		replyErr(w, http.StatusServiceUnavailable, msg)
		return
	}

	// The fetched row is written before the value leaves (fail closed): a value
	// with no audit row is never handed out.
	row := base
	row.Outcome = OutcomeFetched
	if err := b.rec.Record(row); err != nil {
		log.Printf("broker: audit write failed (%v); static token %q for %s withheld", err, req.Name, sid) // the row holds identifiers only
		replyErr(w, http.StatusServiceUnavailable, "agent-box could not audit this token fetch, so it was not issued; retry shortly")
		return
	}
	writeStaticToken(w, string(value), entry)
}

func writeStaticToken(w http.ResponseWriter, value string, e config.StaticToken) {
	cfg := e.ConfigEnv
	if cfg == nil {
		cfg = map[string]string{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"value": value, "env": e.Env, "config_env": cfg})
}
