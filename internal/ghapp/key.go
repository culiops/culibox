// Package ghapp mints short-lived, read-only installation tokens for a
// configured App. It holds the App's private key on the host only: the key and
// the App JWT never leave this package's callers (the credential broker).
package ghapp

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/culiops/agent-box/internal/config"
)

// maxKeyBytes bounds the key file read; a 4096-bit PEM key is about 3.3 KB.
const maxKeyBytes = 64 << 10

// LoadKey reads the App's RSA private key from a host file. The file must be
// a regular file (never a symlink) owned by the current user with no group or
// other permission bits. Errors name the path and the reason, never the bytes.
func LoadKey(ref config.KeyRef) (*rsa.PrivateKey, error) {
	if ref.Type != "file" {
		return nil, fmt.Errorf("private key: unsupported ref type %q (only \"file\")", ref.Type)
	}
	path := ref.Path
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("private key %q: path must be absolute", path)
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("private key %q: %s", path, reason(err))
	}
	defer root.Close()
	base := filepath.Base(path)

	fi, err := root.Lstat(base)
	if err != nil {
		return nil, fmt.Errorf("private key %q: %s", path, reason(err))
	}
	if err := checkKeyFile(path, fi); err != nil {
		return nil, err
	}
	f, err := root.Open(base)
	if err != nil {
		return nil, fmt.Errorf("private key %q: %s", path, reason(err))
	}
	defer f.Close()
	// Re-check the opened file: the name could have been swapped after Lstat.
	fi2, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("private key %q: %s", path, reason(err))
	}
	if err := checkKeyFile(path, fi2); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxKeyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("private key %q: read failed", path)
	}
	if len(data) > maxKeyBytes {
		return nil, fmt.Errorf("private key %q: file too large", path)
	}
	return parseKey(path, data)
}

// reason strips the path off an *fs.PathError so the message names it once.
func reason(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

func checkKeyFile(path string, fi os.FileInfo) error {
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("private key %q: not a regular file (symlinks are refused)", path)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("private key %q: not owned by the current user", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("private key %q: mode %04o lets group or others in; use 0600", path, fi.Mode().Perm())
	}
	return nil
}

func parseKey(path string, data []byte) (*rsa.PrivateKey, error) {
	blk, _ := pem.Decode(data)
	if blk == nil {
		return nil, fmt.Errorf("private key %q: not PEM encoded", path)
	}
	switch blk.Type {
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(blk.Bytes)
		if err != nil {
			return nil, fmt.Errorf("private key %q: not a valid PKCS#1 RSA key", path)
		}
		return k, nil
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
		if err != nil {
			return nil, fmt.Errorf("private key %q: not a valid PKCS#8 key", path)
		}
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("private key %q: not an RSA key", path)
		}
		return rk, nil
	}
	return nil, fmt.Errorf("private key %q: unsupported PEM block type (want RSA PRIVATE KEY or PRIVATE KEY)", path)
}
