// Package broker mints short-lived, downscoped AWS credentials for boxed agent
// sessions. It is the most sensitive component in agent-box: it holds the
// user's SSO session, which can mint every permission set the user has.
package broker

import "github.com/culiops/agent-box/internal/sesstoken"

// NewNonce returns a fresh per-spawn nonce: 16 random bytes as 32 lowercase hex
// chars. Session ids are reused (<agent>-<n>, counters reset on restart), so the
// nonce is what makes a token specific to one spawn rather than to an id.
func NewNonce() string { return sesstoken.NewNonce() }

// Token is "<sessionID>.<nonce>.<hex HMAC-SHA256(secret, "broker\x00"+sessionID+"\x00"+nonce)>".
// Stateless: it survives a daemon restart as long as the secret does. It is
// only honoured while the session is live and the nonce matches the live
// session's (checked by the broker, not here).
func Token(secret []byte, sessionID, nonce string) string {
	return sesstoken.Mint(sesstoken.DomainBroker, secret, sessionID, nonce)
}

// ParseToken splits tok on its last two dots, requires a well-formed nonce, and
// verifies the MAC in constant time. It never returns a session id or nonce
// unless the whole token is authentic.
func ParseToken(secret []byte, tok string) (sessionID, nonce string, ok bool) {
	return sesstoken.Parse(sesstoken.DomainBroker, secret, tok)
}
