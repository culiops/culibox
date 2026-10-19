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

	"github.com/culiops/agent-box/internal/config"
)

// MaxValueBytes bounds a referenced value read from a file.
const MaxValueBytes = 64 << 10

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

// Default resolves file and env references. A nil LookupEnv means os.LookupEnv.
type Default struct {
	LookupEnv func(string) (string, bool)
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
	}
	out, err := Value(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	return out, nil
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

// ReadFile reads a host secret file. The file must be a regular file (never a
// symlink) owned by the current user with no group or other permission bits,
// and at most max bytes. All access goes through an os.Root on its directory.
// Errors name the path and the reason, never the bytes.
func ReadFile(path string, max int64) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%q: path must be absolute", path)
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("%q: %s", path, reason(err))
	}
	defer root.Close()
	base := filepath.Base(path)

	fi, err := root.Lstat(base)
	if err != nil {
		return nil, fmt.Errorf("%q: %s", path, reason(err))
	}
	if err := checkFile(path, fi); err != nil {
		return nil, err
	}
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
