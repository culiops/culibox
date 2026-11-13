//go:build upgrade

package upgradetest

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
