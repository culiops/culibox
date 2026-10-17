package broker

import (
	"context"
	"time"
)

const sourceKeyTimeout = 5 * time.Second

// SourceKeyer is what a Minter implements to say which SSO session a source
// profile signs in with. A Minter without it keys every profile on its own name.
type SourceKeyer interface {
	SourceKey(ctx context.Context, profile string) string
}

// SourceKeyOf is the profile's sourceKey (its sso_session, else the profile
// name), read from the minter once per profile and cached: the host's config is
// not re-read on every credential request.
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
		if got := kr.SourceKey(ctx, profile); got != "" {
			k = got
		}
	}
	b.mu.Lock()
	b.srcKeys[profile] = k
	b.mu.Unlock()
	return k
}

// SSOExpired is the sourceKey session sid is waiting on a human to sign in to,
// or "" when its last mint did not fail on an expired SSO login.
func (b *Broker) SSOExpired(sid string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ssoNeed[sid]
}

// SSOLoginDone records the outcome of a sign-in that sid's card started for the
// sourceKey. A completed one clears the flag for every session on that key; a
// failed one clears nothing. Both are audited.
func (b *Broker) SSOLoginDone(key, sid string, ok bool, detail string) {
	row := ApprovalRow{Time: b.now(), SessionID: sid, Action: ActionSSOLoginFailed, Error: sanitize(detail)}
	if ok {
		row.Action = ActionSSOLoginOK
		b.mu.Lock()
		b.clearSSOLocked(key)
		b.mu.Unlock()
	}
	if sv, found := b.lookup(sid); found {
		identify(&row, sv)
	}
	b.recordApproval(row)
}

// clearSSOLocked forgets an expiry for key and every session waiting on it.
func (b *Broker) clearSSOLocked(key string) {
	delete(b.ssoBad, key)
	for sid, k := range b.ssoNeed {
		if k == key {
			delete(b.ssoNeed, sid)
		}
	}
}
