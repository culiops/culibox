package secretstore

import (
	"errors"
	"fmt"
	"strings"
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
	// ErrCaseCollision matches a CaseCollisionError under errors.Is.
	ErrCaseCollision = errors.New("id differs only in case from an existing value")
	// ErrKeeperUnavailable matches an UnavailableError under errors.Is: a
	// passphrase-less keeper (the macOS Keychain) could not give up this
	// store's key. It is never ErrLocked: there is no passphrase to type.
	ErrKeeperUnavailable = errors.New("the store key could not be read from the macOS Keychain")
)

// UnavailableKind says which fix applies to an UnavailableError.
type UnavailableKind string

const (
	// KindAccess: the Keychain refused or could not be read (locked, Deny on
	// the ACL prompt, no interaction allowed). Retrying after a fix works.
	KindAccess UnavailableKind = "access"
	// KindKeyLost: the Keychain answered but holds no usable key for this
	// store (item missing, malformed, or another store's). Nothing stored can
	// be decrypted again.
	KindKeyLost UnavailableKind = "key_lost"
	// KindStoreUnreadable: the store directory or its recipient file could not
	// be read. Not a Keychain problem; Reason is the OS error.
	KindStoreUnreadable UnavailableKind = "store_unreadable"
)

// UnavailableError is a passphrase-less keeper failing to yield the store key.
// Reason is short and printable (never a key); StoreDir is the directory to
// remove when the key is lost.
type UnavailableError struct {
	Kind     UnavailableKind
	Reason   string
	StoreDir string
}

// Error is the user-facing message, fix included.
func (e *UnavailableError) Error() string {
	head := ErrKeeperUnavailable.Error()
	if e.Kind == KindStoreUnreadable {
		head = "the store could not be read"
	}
	return fmt.Sprintf("%s: %s. %s", head, strings.TrimRight(e.Reason, ". "), e.Fix())
}

func (e *UnavailableError) Is(target error) bool { return target == ErrKeeperUnavailable }

// Fix is what the operator does about it.
func (e *UnavailableError) Fix() string {
	if e.Kind == KindStoreUnreadable {
		return fmt.Sprintf("CuliBox cannot read its store directory %s: check that it exists and that your user can read it, then retry.", e.StoreDir)
	}
	if e.Kind == KindKeyLost {
		return fmt.Sprintf(`The key for this store is gone, so its stored values cannot be decrypted. To start over: remove %s, run "culibox store init", then add your tokens again.`, e.StoreDir)
	}
	return "Unlock the login keychain (Keychain Access, or log out and in), or click Allow when macOS asks about CuliBox, then retry."
}

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

// Event is one audit record. Detail holds an id, a TTL or a fixed reason text,
// never a value, a passphrase, a path or a keeper's own error text. Action is one of: init init_failed unlocked unlock_failed
// unlock_rate_limited locked lock_failed passwd_changed passwd_failed value_set
// value_set_failed value_removed value_removed_failed.
type Event struct{ Action, Actor, Detail string }

// Recorder persists an Event. Store writes and unlocks are fail-closed: when
// RecordStore errors the action is not taken.
type Recorder interface{ RecordStore(Event) error }

// CaseCollisionError is Set refusing an id that case-folds onto a different
// existing one. The store keeps one file per id, so on a case-insensitive
// filesystem (macOS APFS by default) the two would be the same file and the
// set would silently replace the other value. It is refused everywhere, so
// behaviour does not depend on the platform.
type CaseCollisionError struct{ ID, Existing string }

func (e *CaseCollisionError) Error() string {
	return fmt.Sprintf("%q collides with existing %q (ids are case-insensitive on macOS); use the exact id", e.ID, e.Existing)
}

func (e *CaseCollisionError) Is(target error) bool { return target == ErrCaseCollision }
