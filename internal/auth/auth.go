// Package auth provides the credential primitives for the box: the master
// password (ResolveCred), the persisted cookie-signing secret (CookieAuth), the
// session cookie (session.go), and bearer access tokens (tokens.go).
package auth

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/culiops/agent-box/internal/config"
)

// CookieName is the WS auth cookie.
const CookieName = "ab_session"

// ResolveCred reads the master login password from the configured env var; it
// is never stored in config or logged. The username field is retained for
// backward compatibility but no longer used for auth (login is password-only),
// so an empty basic_auth_user is not an error.
func ResolveCred(c config.Config) (user, pass string, err error) {
	if c.BasicAuthPassEnv == "" {
		return "", "", errors.New("auth: basic_auth_pass_env not set")
	}
	pass = os.Getenv(c.BasicAuthPassEnv)
	if pass == "" {
		return "", "", errors.New("auth: env var " + c.BasicAuthPassEnv + " is empty")
	}
	return c.BasicAuthUser, pass, nil
}

// CookieAuth loads and persists the 32-byte signing secret shared by the
// session cookie (see session.go). It no longer issues cookies itself.
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

// Secret returns the persisted signing secret, so a Session can be built from
// the same key that the WS cookie already uses.
func (c *CookieAuth) Secret() []byte { return c.secret }
