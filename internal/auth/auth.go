// Package auth provides basic-auth for HTTP routes and a signed-cookie
// mechanism for the WebSocket route (browsers cannot set an Authorization
// header on new WebSocket()).
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"os"

	"github.com/culiops/culiops-agent-console/internal/config"
)

// CookieName is the WS auth cookie.
const CookieName = "ac_session"

// ResolveCred reads the basic-auth credential; the password comes from the
// configured env var and is never stored in config or logged.
func ResolveCred(c config.Config) (user, pass string, err error) {
	if c.BasicAuthUser == "" {
		return "", "", errors.New("auth: basic_auth_user not set")
	}
	pass = os.Getenv(c.BasicAuthPassEnv)
	if pass == "" {
		return "", "", errors.New("auth: env var " + c.BasicAuthPassEnv + " is empty")
	}
	return c.BasicAuthUser, pass, nil
}

// BasicAuth wraps next with HTTP Basic authentication (constant-time compare).
func BasicAuth(user, pass string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(u), []byte(user)) != 1 ||
			subtle.ConstantTimeCompare([]byte(p), []byte(pass)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="agent-console"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CookieAuth mints and validates a signed cookie for the WebSocket route.
// The secret is random per process; a restart simply forces a page reload.
type CookieAuth struct{ secret []byte }

// NewCookieAuth generates a per-process signing secret.
func NewCookieAuth() (*CookieAuth, error) {
	s := make([]byte, 32)
	if _, err := rand.Read(s); err != nil {
		return nil, err
	}
	return &CookieAuth{secret: s}, nil
}

func (c *CookieAuth) token() string {
	mac := hmac.New(sha256.New, c.secret)
	mac.Write([]byte("authenticated"))
	return hex.EncodeToString(mac.Sum(nil))
}

// Issue sets the signed cookie (called on the authed dashboard load).
func (c *CookieAuth) Issue(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    c.token(),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// Valid reports whether the request carries the correct signed cookie.
func (c *CookieAuth) Valid(r *http.Request) bool {
	ck, err := r.Cookie(CookieName)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(ck.Value), []byte(c.token())) == 1
}
