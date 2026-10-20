//go:build linux

package secretstore

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// keyringKeeper keeps the unlocked identity in the kernel user keyring (@u),
// as a "user" key with a kernel-enforced timeout. It survives the daemon
// process and dies with the uid's keyring or the TTL.
type keyringKeeper struct{ desc string }

// NewKeeper returns the Linux keeper for dataDir.
func NewKeeper(dataDir string) Keeper {
	return &keyringKeeper{desc: "agent-box:store:" + KeyHash(dataDir)}
}

func (k *keyringKeeper) NeedsPassphrase() bool { return true }

func (k *keyringKeeper) Save(identity string, ttl time.Duration) error {
	if ttl < time.Second {
		return errors.New("unlock ttl must be at least one second")
	}
	expires := time.Now().Add(ttl)
	payload := []byte(identity + "\n" + strconv.FormatInt(expires.Unix(), 10))
	id, err := unix.AddKey("user", k.desc, payload, unix.KEY_SPEC_USER_KEYRING)
	if err != nil {
		return fmt.Errorf("kernel keyring add: %w", err)
	}
	secs := int((ttl + time.Second - 1) / time.Second)
	if _, err := unix.KeyctlInt(unix.KEYCTL_SET_TIMEOUT, id, secs, 0, 0); err != nil {
		// Never leave a key without its timeout behind.
		_, _ = unix.KeyctlInt(unix.KEYCTL_UNLINK, id, unix.KEY_SPEC_USER_KEYRING, 0, 0)
		return fmt.Errorf("kernel keyring set timeout: %w", err)
	}
	return nil
}

func (k *keyringKeeper) Load() (string, time.Time, error) {
	id, err := unix.KeyctlSearch(unix.KEY_SPEC_USER_KEYRING, "user", k.desc, 0)
	if err != nil {
		return "", time.Time{}, keyErr(err, "search")
	}
	// Read with a growing buffer: the key can change between size and read.
	buf := make([]byte, 512)
	for {
		n, err := unix.KeyctlBuffer(unix.KEYCTL_READ, id, buf, 0)
		if err != nil {
			return "", time.Time{}, keyErr(err, "read")
		}
		if n <= len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, n)
	}
	ident, rest, ok := bytes.Cut(buf, []byte("\n"))
	if !ok || len(ident) == 0 {
		return "", time.Time{}, ErrLocked
	}
	secs, err := strconv.ParseInt(string(bytes.TrimSpace(rest)), 10, 64)
	if err != nil {
		return "", time.Time{}, ErrLocked
	}
	exp := time.Unix(secs, 0)
	if !time.Now().Before(exp) {
		return "", time.Time{}, ErrLocked
	}
	return string(ident), exp, nil
}

func (k *keyringKeeper) Clear() error {
	id, err := unix.KeyctlSearch(unix.KEY_SPEC_USER_KEYRING, "user", k.desc, 0)
	if err != nil {
		if gone(err) {
			return nil
		}
		return fmt.Errorf("kernel keyring search: %w", err)
	}
	if _, err := unix.KeyctlInt(unix.KEYCTL_UNLINK, id, unix.KEY_SPEC_USER_KEYRING, 0, 0); err != nil && !gone(err) {
		return fmt.Errorf("kernel keyring unlink: %w", err)
	}
	return nil
}

// gone reports that the key is absent, expired or revoked.
func gone(err error) bool {
	return errors.Is(err, unix.ENOKEY) || errors.Is(err, unix.EKEYEXPIRED) || errors.Is(err, unix.EKEYREVOKED)
}

func keyErr(err error, op string) error {
	if gone(err) {
		return ErrLocked
	}
	return fmt.Errorf("kernel keyring %s: %w", op, err)
}
