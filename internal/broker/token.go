// Package broker mints short-lived, downscoped AWS credentials for boxed agent
// sessions. It is the most sensitive component in agent-box: it holds the
// user's SSO session, which can mint every permission set the user has.
package broker

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

const nonceHexLen = 32 // 16 random bytes, hex

// NewNonce returns a fresh per-spawn nonce: 16 random bytes as 32 lowercase hex
// chars. Session ids are reused (<agent>-<n>, counters reset on restart), so the
// nonce is what makes a token specific to one spawn rather than to an id. It
// panics only if crypto/rand fails, like the stdlib.
func NewNonce() string {
	b := make([]byte, nonceHexLen/2)
	if _, err := rand.Read(b); err != nil {
		panic("broker: crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func mac(secret []byte, sessionID, nonce string) string {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte("broker\x00" + sessionID + "\x00" + nonce))
	return hex.EncodeToString(h.Sum(nil))
}

// Token is "<sessionID>.<nonce>.<hex HMAC-SHA256(secret, "broker\x00"+sessionID+"\x00"+nonce)>".
// Stateless: it survives a daemon restart as long as the secret does. It is
// only honoured while the session is live and the nonce matches the live
// session's (checked by the broker, not here).
func Token(secret []byte, sessionID, nonce string) string {
	return sessionID + "." + nonce + "." + mac(secret, sessionID, nonce)
}

// ParseToken splits tok on its last two dots, requires a well-formed nonce, and
// verifies the MAC in constant time. It never returns a session id or nonce
// unless the whole token is authentic.
func ParseToken(secret []byte, tok string) (sessionID, nonce string, ok bool) {
	i := strings.LastIndexByte(tok, '.')
	if i <= 0 {
		return "", "", false
	}
	j := strings.LastIndexByte(tok[:i], '.')
	if j <= 0 {
		return "", "", false
	}
	sid, n := tok[:j], tok[j+1:i]
	if !validNonce(n) {
		return "", "", false
	}
	if subtle.ConstantTimeCompare([]byte(Token(secret, sid, n)), []byte(tok)) != 1 {
		return "", "", false
	}
	return sid, n, true
}

func validNonce(n string) bool {
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
