//go:build upgrade

package upgradetest

import (
	"path/filepath"

	"github.com/culiops/culibox/internal/abduco"
	"github.com/culiops/culibox/internal/secretstore"
)

// keychainService is the login Keychain service the darwin store keeper uses
// (internal/secretstore/keychain_darwin.go); the account is the data root's
// KeyHash.
const keychainService = "agent-box-store"

// keychainCleanupArgs returns one `security` argument list per distinct,
// non-empty KeyHash: the Keychain items this run's stores created, and no other.
func keychainCleanupArgs(hashes []string) [][]string {
	seen := map[string]bool{}
	var out [][]string
	for _, h := range hashes {
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, []string{"delete-generic-password", "-s", keychainService, "-a", h})
	}
	return out
}

// darwinSocketDirs returns the abduco socket dirs serve uses on darwin for
// these data roots (/tmp/agent-box-<uid>-<hash>, outside the root, so they are
// not removed with the throwaway tree), deduplicated.
func darwinSocketDirs(roots []string, uid int) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range roots {
		if r == "" {
			continue
		}
		d := abduco.DefaultSocketDir("darwin", r, uid)
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// carriedKeyHash is the KeyHash the #255 move saves the store key under: the
// new root's parent with symlinks resolved, plus its base name (rootmove's
// keyTarget). It equals KeyHash(newRoot) once newRoot exists, and still names
// the right key when the move stopped before the rename (/tmp is
// /private/tmp on darwin).
func carriedKeyHash(newRoot string) string {
	parent, err := filepath.EvalSymlinks(filepath.Dir(newRoot))
	if err != nil {
		return ""
	}
	return secretstore.KeyHash(filepath.Join(parent, filepath.Base(newRoot)))
}
