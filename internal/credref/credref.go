// Package credref resolves a config.KeyRef (a file or an environment variable
// on the host) to the bytes it names, at the moment of use. Nothing is cached.
// Errors name the reference and the reason, never the bytes.
package credref

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/secretstore"
)

// MaxValueBytes bounds a referenced value: one limit for files and the store.
const MaxValueBytes = secretstore.MaxValueBytes

// ErrLocked is what a store reference resolves to while the store is locked.
var ErrLocked = secretstore.ErrLocked

// StoreGetter is the part of the encrypted store a reference needs. Get returns
// the value's raw bytes.
type StoreGetter interface {
	Get(id string) ([]byte, error)
}

// ErrStoreNotConfigured is returned for a store reference when no store was given.
var ErrStoreNotConfigured = errors.New("store references need the agent-box store (not configured)")

// ErrEnvNotSet is what Resolve wraps when an env reference names a variable
// that is unset or empty. Callers that only report on a reference (doctor) can
// tell this apart from a bad file without matching message text.
var ErrEnvNotSet = errors.New("environment variable is not set")

// envNotSetError keeps the message callers already see and matches ErrEnvNotSet.
type envNotSetError struct{ v string }

func (e envNotSetError) Error() string {
	return fmt.Sprintf("env var %q is not set in the agent-box daemon's environment", e.v)
}
func (e envNotSetError) Is(target error) bool { return target == ErrEnvNotSet }

// Resolver turns a reference into its current bytes.
type Resolver interface {
	Resolve(ref config.KeyRef) ([]byte, error)
}

// Default resolves file, env and store references. A nil LookupEnv means
// os.LookupEnv; a nil Store refuses store references.
type Default struct {
	LookupEnv func(string) (string, bool)
	Store     StoreGetter
}

// Resolve validates ref, reads it fresh, and returns the trimmed value.
func (d Default) Resolve(ref config.KeyRef) ([]byte, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	var raw []byte
	switch ref.Type {
	case "file":
		b, err := ReadFile(ref.Path, MaxValueBytes)
		if err != nil {
			return nil, err
		}
		raw = b
	case "env":
		lookup := d.LookupEnv
		if lookup == nil {
			lookup = os.LookupEnv
		}
		v, ok := lookup(ref.Var)
		if !ok || v == "" {
			return nil, envNotSetError{ref.Var}
		}
		raw = []byte(v)
	case "store":
		b, err := GetStore(d.Store, ref.ID)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	out, err := Value(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	return out, nil
}

// GetStore reads a store value's raw bytes. Errors keep the store's sentinels
// (errors.Is ErrLocked / secretstore.ErrKeeperUnavailable are passed through
// unwrapped; others name the id) and never the
// bytes. A nil store is ErrStoreNotConfigured.
func GetStore(s StoreGetter, id string) ([]byte, error) {
	if s == nil {
		return nil, ErrStoreNotConfigured
	}
	b, err := s.Get(id)
	if err != nil {
		if errors.Is(err, ErrLocked) || errors.Is(err, secretstore.ErrKeeperUnavailable) {
			// The message is fixed and carries its fix: no store id wrapped round it.
			return nil, err
		}
		return nil, fmt.Errorf("store %s: %w", id, err)
	}
	return b, nil
}

// Value trims one trailing "\n" or "\r\n", then refuses an empty value or one
// holding a control byte (< 0x20 or 0x7f).
func Value(raw []byte) ([]byte, error) {
	switch {
	case len(raw) >= 2 && raw[len(raw)-2] == '\r' && raw[len(raw)-1] == '\n':
		raw = raw[:len(raw)-2]
	case len(raw) >= 1 && raw[len(raw)-1] == '\n':
		raw = raw[:len(raw)-1]
	}
	if len(raw) == 0 {
		return nil, errors.New("value is empty")
	}
	for _, c := range raw {
		if c < 0x20 || c == 0x7f {
			return nil, errors.New("value contains a control character")
		}
	}
	return raw, nil
}

// missingError is a file that does not exist; it matches fs.ErrNotExist so a
// caller can tell "absent" from "present but refused".
type missingError struct{ msg string }

func (e missingError) Error() string        { return e.msg }
func (e missingError) Is(target error) bool { return target == fs.ErrNotExist }

// openChecked is the gate in front of every host secret file read: an absolute
// path, a directory that opens, and an Lstat'ed entry that is a regular file
// (never a symlink) owned by the current user with no group or other bits. It
// returns the root on the file's directory and the file's name in it. It never
// opens the file itself.
func openChecked(path string) (*os.Root, string, error) {
	if !filepath.IsAbs(path) {
		return nil, "", fmt.Errorf("%q: path must be absolute", path)
	}
	fail := func(err error) (*os.Root, string, error) {
		e := fmt.Errorf("%q: %s", path, reason(err))
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "", missingError{e.Error()}
		}
		return nil, "", e
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return fail(err)
	}
	base := filepath.Base(path)
	fi, err := root.Lstat(base)
	if err != nil {
		root.Close()
		return fail(err)
	}
	if err := checkFile(path, fi); err != nil {
		root.Close()
		return nil, "", err
	}
	return root, base, nil
}

// CheckFile runs the checks ReadFile runs before it opens a host secret file,
// and nothing more: it never opens or reads the file. A nil error means a read
// would get past them. An error that matches fs.ErrNotExist means the file (or
// its directory) is not there; any other error is a refusal. ReadFile calls the
// same gate, so the two cannot drift.
func CheckFile(path string) error {
	root, _, err := openChecked(path)
	if err != nil {
		return err
	}
	return root.Close()
}

// ReadFile reads a host secret file. The file must be a regular file (never a
// symlink) owned by the current user with no group or other permission bits,
// and at most max bytes. All access goes through an os.Root on its directory.
// Errors name the path and the reason, never the bytes.
func ReadFile(path string, max int64) ([]byte, error) {
	root, base, err := openChecked(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(base)
	if err != nil {
		return nil, fmt.Errorf("%q: %s", path, reason(err))
	}
	defer f.Close()
	// Re-check the opened file: the name could have been swapped after Lstat.
	fi2, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("%q: %s", path, reason(err))
	}
	if err := checkFile(path, fi2); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, fmt.Errorf("%q: read failed", path)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%q: file too large", path)
	}
	return data, nil
}

// reason strips the path off an *fs.PathError so the message names it once.
func reason(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

func checkFile(path string, fi os.FileInfo) error {
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%q: not a regular file (symlinks are refused)", path)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%q: not owned by the current user", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%q: mode %04o lets group or others in; use 0600", path, fi.Mode().Perm())
	}
	return nil
}
