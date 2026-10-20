package broker

import (
	"context"
	"slices"
	"time"
)

const sourceKeyTimeout = 5 * time.Second

// SourceKeyer is what a Minter implements to say which SSO session a source
// profile signs in with. A Minter without it keys every profile on its own name.
// A non-nil error means the config could not be read: the key returned is then
// only the profile's own name, a fallback for this call.
type SourceKeyer interface {
	SourceKey(ctx context.Context, profile string) (string, error)
}

// SourceKeyOf is the profile's sourceKey (its sso_session, else the profile
// name), read from the minter once per profile and cached: the host's config is
// not re-read on every credential request. Only a successful read is cached; a
// failed or timed-out one uses the profile name for this call alone, so a
// transient error cannot pin the wrong key.
func (b *Broker) SourceKeyOf(profile string) string {
	b.mu.Lock()
	k, ok := b.srcKeys[profile]
	b.mu.Unlock()
	if ok {
		return k
	}
	k = profile
	if kr, ok := b.minter.(SourceKeyer); ok { // outside mu: it reads files
		ctx, cancel := context.WithTimeout(context.Background(), sourceKeyTimeout)
		defer cancel()
		got, err := kr.SourceKey(ctx, profile)
		if err != nil || got == "" {
			return k
		}
		k = got
	}
	b.mu.Lock()
	b.srcKeys[profile] = k
	b.mu.Unlock()
	return k
}

// SSOExpired is the sourceKey session sid is waiting on a human to sign in to,
// or "" when its last mint did not fail on an expired SSO login.
// For a session with connections it is the legacy key if any, else the first by
// connection name.
func (b *Broker) SSOExpired(sid string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if k, ok := b.ssoNeed[connKey{sid, ""}]; ok {
		return k
	}
	var conns []string
	for k := range b.ssoNeed {
		if k.sid == sid {
			conns = append(conns, k.conn)
		}
	}
	if len(conns) == 0 {
		return ""
	}
	slices.Sort(conns)
	return b.ssoNeed[connKey{sid, conns[0]}]
}

// SSOExpiredConns maps each of session sid's named connections whose last
// mint failed on an expired SSO login to the sourceKey it waits on. The legacy
// binding (conn "") is not in it: SSOExpired reports that one.
func (b *Broker) SSOExpiredConns(sid string) map[string]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[string]string{}
	for k, key := range b.ssoNeed {
		if k.sid == sid && k.conn != "" {
			out[k.conn] = key
		}
	}
	return out
}

// SSOLoginDone records the outcome of a sign-in that sid's card started for the
// sourceKey, for connection conn ("" = the legacy binding). A completed one
// clears the flag for every session on that key; a failed one clears nothing.
// Both are audited.
func (b *Broker) SSOLoginDone(key, sid, conn string, ok bool, detail string) {
	row := ApprovalRow{Time: b.now(), SessionID: sid, Action: ActionSSOLoginFailed, Error: sanitize(detail)}
	if ok {
		row.Action = ActionSSOLoginOK
		b.mu.Lock()
		b.clearSSOLocked(key)
		b.mu.Unlock()
	}
	if sv, found := b.lookup(sid); found {
		identify(&row, sv, conn)
	}
	b.recordApproval(row)
}

// clearSSOLocked forgets an expiry for key and every session waiting on it.
func (b *Broker) clearSSOLocked(key string) {
	delete(b.ssoBad, key)
	for ck, k := range b.ssoNeed {
		if k == key {
			delete(b.ssoNeed, ck)
		}
	}
}
