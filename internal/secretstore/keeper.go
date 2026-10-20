package secretstore

import (
	"errors"
	"time"
)

// Sentinels. Callers match with errors.Is; none carries a value or passphrase.
var (
	ErrLocked         = errors.New("store is locked")
	ErrNotInitialized = errors.New("store is not initialized")
	ErrNoSuchValue    = errors.New("no such value in the store")
	ErrExists         = errors.New("store already exists")
	ErrBadPassphrase  = errors.New("wrong passphrase")
	ErrNotApplicable  = errors.New("not applicable on this platform")
	ErrUnsupported    = errors.New("the encrypted store is not supported on this platform")
	ErrBadID          = errors.New("invalid store id: use letters, digits, _ and -, starting with a letter or digit, at most 64 characters")
	ErrTooLarge       = errors.New("value is too large")
)

// Keeper holds the unlocked master identity outside the process: the kernel
// user keyring on Linux, the login Keychain on macOS. The Store never caches
// what it returns.
type Keeper interface {
	// NeedsPassphrase reports whether unlocking takes a passphrase (Linux
	// true, macOS false).
	NeedsPassphrase() bool
	// Load returns the age identity string and when it stops being readable
	// (zero when it does not expire). It returns ErrLocked when no unlocked
	// identity is available (Linux) and may return ErrNotInitialized when the
	// master key is absent (macOS).
	Load() (identity string, expires time.Time, err error)
	// Save stores the identity for ttl; ttl is ignored on macOS.
	Save(identity string, ttl time.Duration) error
	// Clear forgets the identity.
	Clear() error
}

// Event is one audit record. Detail holds an id or a TTL, never a value or a
// passphrase. Action is one of: init unlocked unlock_failed
// unlock_rate_limited locked passwd_changed value_set value_removed.
type Event struct{ Action, Actor, Detail string }

// Recorder persists an Event. Store writes and unlocks are fail-closed: when
// RecordStore errors the action is not taken.
type Recorder interface{ RecordStore(Event) error }
