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
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/culiops/agent-box/internal/config"
)

// CookieName is the WS auth cookie.
const CookieName = "ab_session"

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
			w.Header().Set("WWW-Authenticate", `Basic realm="agent-box"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CookieAuth mints and validates a signed cookie for the WebSocket route.
type CookieAuth struct{ secret []byte }

// NewCookieAuth returns a CookieAuth backed by a 32-byte signing secret loaded
// from path, generating and persisting a fresh one (file 0600, dir 0700) if the
// file is absent or malformed. Persisting the secret is what lets a browser's WS
// auth cookie survive a daemon restart: the /ws route is cookie-only gated, so a
// per-process secret would 401 the client's auto-reconnect forever. An empty
// path yields an ephemeral per-process secret (tests).
func NewCookieAuth(path string) (*CookieAuth, error) {
	if path == "" {
		return newEphemeral()
	}
	if b, err := os.ReadFile(path); err == nil {
		if len(b) == 32 {
			return &CookieAuth{secret: b}, nil
		}
		// Present but malformed (torn/hand-edited) — fall through to regenerate.
	} else if !errors.Is(err, os.ErrNotExist) {
		// An existing secret we merely couldn't read (perms/IO): refuse to
		// silently overwrite it — that would invalidate every live cookie, the
		// exact failure this persistence exists to prevent. Fail fast instead.
		return nil, fmt.Errorf("auth: read cookie secret %s: %w", path, err)
	}
	// Absent or malformed — mint and persist a fresh secret.
	s := make([]byte, 32)
	if _, err := rand.Read(s); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// Atomic write so a crash mid-write can't leave a torn secret.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, s, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	return &CookieAuth{secret: s}, nil
}

func newEphemeral() (*CookieAuth, error) {
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
