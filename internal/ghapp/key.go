// Package ghapp mints short-lived, read-only installation tokens for a
// configured App. It holds the App's private key on the host only: the key and
// the App JWT never leave this package's callers (the credential broker).
package ghapp

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/credref"
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
	data, err := credref.ReadFile(ref.Path, maxKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("private key %w", err)
	}
	return parseKey(ref.Path, data)
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
