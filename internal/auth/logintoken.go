package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/datacompat"
)

// LoginTokenFile holds the generated login secret in token mode.
const LoginTokenFile = "login.token"

const (
	ModePassword = "password" // secret from the env var named by password_env
	ModeToken    = "token"    // secret generated into <dataDir>/login.token
)

// Login is the secret /login checks and where it came from. Secret is never
// logged.
type Login struct {
	Secret    string
	Mode      string
	TokenPath string // token mode only
}

var loginTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// ResolveLogin picks the login secret. A configured password_env is a server
// and keeps today's behaviour, including failing when the variable is empty: a
// misconfigured server must not silently fall into token mode. With no
// password_env, the instance runs in token mode. Auth is never optional — this
// process spawns shells.
func ResolveLogin(c config.Config, dataDir string) (Login, error) {
	if c.PasswordEnv != "" {
		p, err := ResolveCred(c)
		if err != nil {
			return Login{}, err
		}
		return Login{Secret: p, Mode: ModePassword}, nil
	}
	path := filepath.Join(dataDir, LoginTokenFile)
	tok, err := LoadOrCreateLoginToken(path)
	if err != nil {
		return Login{}, err
	}
	return Login{Secret: tok, Mode: ModeToken, TokenPath: path}, nil
}

// LoadOrCreateLoginToken returns the token in path, generating 32 random bytes
// (base64url, 43 characters) when the file is absent or blank. Surrounding
// whitespace is ignored, so a hand-edited file keeps working. Any other content
// is refused, never replaced: a newer CuliBox may have written a token in a form
// this one cannot read. Any read error other than not-exist is fatal too:
// regenerating would lock out a browser for no reason the user could see.
func LoadOrCreateLoginToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		s := strings.TrimSpace(string(b))
		if loginTokenRe.MatchString(s) {
			return s, nil
		}
		if s != "" {
			return "", fmt.Errorf("%w; or delete it to make a new one; to use your own password, set password_env",
				datacompat.Newer(path, "it is not a 43-character login token"))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("auth: read login token %s: %w", path, err)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(raw)
	if err := writeSecretFile(path, []byte(tok+"\n")); err != nil {
		return "", err
	}
	return tok, nil
}

// WeakPassword reports a password short enough to warn about at startup.
func WeakPassword(s string) bool { return utf8.RuneCountInString(s) < 12 }

// LoginURL is the URL printed at startup in token mode. The token rides in the
// fragment, which browsers never send to the server, so it stays out of access
// logs; login.html moves it into the form and strips it from the URL bar.
func LoginURL(bindAddr, token string) string {
	return "http://" + bindAddr + "/login#token=" + token
}
