package secretstore

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
)

// KeyHash names one data dir in the keeper: the first 16 hex characters of
// sha256 of its absolute, cleaned, symlink-resolved path. DEV and PROD daemons have different
// data dirs, so they get different keyring entries and Keychain accounts.
func KeyHash(dataDir string) string {
	p := filepath.Clean(dataDir)
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	// Resolve symlinks so a CLI and the daemon reaching one data dir by
	// different routes agree. A dir that does not exist yet keeps its cleaned
	// absolute path.
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	sum := sha256.Sum256([]byte(p))
	return hex.EncodeToString(sum[:])[:16]
}
