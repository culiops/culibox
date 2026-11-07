// Package auth: session.go — a self-expiring, HMAC-signed session cookie.
//
// The cookie value is "<exp>.<sig>" where exp is unix-seconds and
// sig = hex(HMAC-SHA256(secret, "<exp>")). It carries its own expiry so no
// server-side session table is needed: the box is issuer and verifier and has a
// single user, so per-session revocation is handled by rotating the secret
// ("log out everywhere"). Individual access tokens carry the fine-grained
// revocation story (see tokens.go).
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// refreshAfter is how old a valid cookie may get before Verify asks for a
// sliding re-issue, keeping an active user logged in indefinitely.
const refreshAfter = 24 * time.Hour

// expiredGrace is how long past its signed expiry the browser keeps the session
// cookie. The dead cookie gains no authority (Verify rejects anything at or past
// the signed exp); it only lets the server tell a login that expired from a first
// visit, so the login page can say so. 30 days matches the session TTL set in
// cmd/agent-box; nothing in the code asks for another value.
const expiredGrace = 30 * 24 * time.Hour

// Session issues and verifies the signed session cookie.
type Session struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// NewSession returns a Session signing with secret and expiring after ttl.
func NewSession(secret []byte, ttl time.Duration) *Session {
	return NewSessionWithClock(secret, ttl, time.Now)
}

// NewSessionWithClock is NewSession reading the time from now: for a test that has
// to issue a cookie, let it expire and present it again (the web package's guard
// tests cannot reach the unexported clock).
func NewSessionWithClock(secret []byte, ttl time.Duration, now func() time.Time) *Session {
	return &Session{secret: secret, ttl: ttl, now: now}
}

func (s *Session) sign(exp int64) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(strconv.FormatInt(exp, 10)))
	return strconv.FormatInt(exp, 10) + "." + hex.EncodeToString(mac.Sum(nil))
}

// Issue sets a fresh session cookie. secure controls the Secure attribute — the
// caller decides based on the request's real scheme (see web.secureCookie).
func (s *Session) Issue(w http.ResponseWriter, secure bool) {
	exp := s.now().Add(s.ttl).Unix()
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    s.sign(exp),
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		// Lax (not Strict): a valid session must survive a top-level navigation
		// from an external link, or the user appears logged out until they reload.
		// Lax still withholds the cookie from cross-site POST, and the same-origin
		// CSRF guard is the real state-change defense regardless.
		SameSite: http.SameSiteLaxMode,
		// The browser keeps the cookie expiredGrace past the signed exp (see there):
		// the signature, not this attribute, is what Verify trusts.
		Expires: time.Unix(exp, 0).Add(expiredGrace),
	})
}

// Verify reports whether the request carries a valid, unexpired session cookie,
// and whether it is old enough to warrant a sliding re-issue.
func (s *Session) Verify(r *http.Request) (ok bool, needsRefresh bool) {
	ck, err := r.Cookie(CookieName)
	if err != nil {
		return false, false
	}
	dot := strings.IndexByte(ck.Value, '.')
	if dot < 0 {
		return false, false
	}
	exp, err := strconv.ParseInt(ck.Value[:dot], 10, 64)
	if err != nil {
		return false, false
	}
	if subtle.ConstantTimeCompare([]byte(ck.Value), []byte(s.sign(exp))) != 1 {
		return false, false
	}
	now := s.now().Unix()
	if now >= exp {
		return false, false
	}
	// issued_at = exp - ttl; refresh once the cookie is older than refreshAfter.
	issued := exp - int64(s.ttl.Seconds())
	needsRefresh = now-issued > int64(refreshAfter.Seconds())
	return true, needsRefresh
}

// Clear expires the session cookie (logout).
func (s *Session) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/",
		HttpOnly: true, MaxAge: -1,
	})
}
