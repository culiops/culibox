// Package sesstoken mints and verifies per-spawn session tokens:
// "<sessionID>.<nonce>.<hex HMAC-SHA256(secret, domain+"\x00"+sessionID+"\x00"+nonce)>".
// The domain keeps a token for one service (the credential broker) from ever
// verifying at another (the egress proxy), even under the same secret.
package sesstoken

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

const (
	DomainBroker = "broker"
	DomainEgress = "egress"
)

const nonceHexLen = 32 // 16 random bytes, hex

// NewNonce returns a fresh per-spawn nonce: 16 random bytes as 32 lowercase hex
// chars. Session ids are reused (<agent>-<n>, counters reset on restart), so the
// nonce is what makes a token specific to one spawn rather than to an id. It
// panics only if crypto/rand fails, like the stdlib.
func NewNonce() string {
	b := make([]byte, nonceHexLen/2)
	if _, err := rand.Read(b); err != nil {
		panic("sesstoken: crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func mac(domain string, secret []byte, sessionID, nonce string) string {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(domain + "\x00" + sessionID + "\x00" + nonce))
	return hex.EncodeToString(h.Sum(nil))
}

// Mint returns the token for (sessionID, nonce) in domain. Stateless: it
// survives a daemon restart as long as the secret does. It is only honoured
// while the session is live and the nonce matches the live session's (checked
// by the verifying service, not here).
func Mint(domain string, secret []byte, sessionID, nonce string) string {
	return sessionID + "." + nonce + "." + mac(domain, secret, sessionID, nonce)
}

// Parse splits tok on its last two dots, requires a well-formed nonce, and
// verifies the MAC in constant time. It never returns a session id or nonce
// unless the whole token is authentic in domain.
func Parse(domain string, secret []byte, tok string) (sessionID, nonce string, ok bool) {
	i := strings.LastIndexByte(tok, '.')
	if i <= 0 {
		return "", "", false
	}
	j := strings.LastIndexByte(tok[:i], '.')
	if j <= 0 {
		return "", "", false
	}
	sid, n := tok[:j], tok[j+1:i]
	if !ValidNonce(n) {
		return "", "", false
	}
	if subtle.ConstantTimeCompare([]byte(Mint(domain, secret, sid, n)), []byte(tok)) != 1 {
		return "", "", false
	}
	return sid, n, true
}

// ValidNonce reports whether n is 32 lowercase hex chars.
func ValidNonce(n string) bool {
	if len(n) != nonceHexLen {
		return false
	}
	for i := 0; i < len(n); i++ {
		c := n[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
