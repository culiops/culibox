//go:build darwin

package secretstore

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	keychainService = "agent-box-store"
	// security(1) exits 44 when the item is not found.
	keychainNotFound = 44
)

// keychainKeeper keeps the master identity in the login Keychain. There is no
// passphrase and no TTL: the Keychain unlocks with the login session.
type keychainKeeper struct{ account string }

// NewKeeper returns the macOS keeper for dataDir.
func NewKeeper(dataDir string) Keeper { return &keychainKeeper{account: KeyHash(dataDir)} }

func (k *keychainKeeper) NeedsPassphrase() bool { return false }

func (k *keychainKeeper) Save(identity string, _ time.Duration) error {
	// The identity goes to security(1) on stdin, never argv. Age identities
	// are [A-Z0-9-]; refusing anything else means nothing needs quoting.
	if !keychainSafe(identity) {
		return errors.New("master key has unexpected characters")
	}
	cmd := exec.Command("security", "-i")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("add-generic-password -U -s %s -a %s -w %s\n", keychainService, k.account, identity))
	if err := cmd.Run(); err != nil {
		return errors.New("keychain write failed")
	}
	return nil
}

func (k *keychainKeeper) Load() (string, time.Time, error) {
	var out bytes.Buffer
	cmd := exec.Command("security", "find-generic-password", "-s", keychainService, "-a", k.account, "-w")
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == keychainNotFound {
			return "", time.Time{}, ErrNotInitialized
		}
		return "", time.Time{}, errors.New("keychain read failed")
	}
	ident := strings.TrimSpace(out.String())
	if ident == "" {
		return "", time.Time{}, ErrNotInitialized
	}
	return ident, time.Time{}, nil
}

// Clear is not applicable: there is no unlock to undo, and deleting the
// master key would orphan every stored value.
func (k *keychainKeeper) Clear() error { return ErrNotApplicable }

func keychainSafe(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
