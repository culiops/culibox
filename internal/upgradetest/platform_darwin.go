//go:build upgrade

package upgradetest

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// seedDarwin writes the darwin-only data root files in the formats vN-1 and vN
// both read (internal/vmshares/store.go version 1, internal/box/guardlock.go
// version 2), so the byte compare covers them.
func seedDarwin(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "vm-shares.json"), `{"version":1,"shares":[]}`, 0o600)
	writeFile(t, filepath.Join(root, "inplace-locks.json"), "{\n  \"version\": 2,\n  \"locks\": []\n}", 0o600)
}

// cleanupStoreKeys registers the removal of this run's login Keychain items
// (service agent-box-store, account = each data root's KeyHash). hashes runs
// at cleanup, so a root created during the test (S2's moved root) is covered.
func cleanupStoreKeys(t *testing.T, hashes func() []string) {
	t.Cleanup(func() {
		for _, args := range keychainCleanupArgs(hashes()) {
			out, err := exec.Command("/usr/bin/security", args...).CombinedOutput()
			if err != nil && !strings.Contains(string(out), "could not be found") {
				t.Logf("keychain cleanup %v: %v: %s", args, err, out)
			} else if err == nil {
				t.Logf("keychain cleanup: removed the item for %s", args[len(args)-1])
			}
		}
	})
}
