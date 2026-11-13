//go:build upgrade

package upgradetest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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

// cleanupSocketDirs removes the abduco socket dirs serve made for these roots
// under /tmp (outside the throwaway tree on darwin). Each root is tried as
// given and with symlinks resolved, since /tmp is /private/tmp; a dir is
// removed only when it is ours (owned by this uid, named for it).
func cleanupSocketDirs(t *testing.T, roots func() []string) {
	t.Cleanup(func() {
		var all []string
		for _, r := range roots() {
			all = append(all, r)
			if res, err := filepath.EvalSymlinks(r); err == nil {
				all = append(all, res)
			}
		}
		uid := os.Getuid()
		for _, d := range darwinSocketDirs(all, uid) {
			fi, err := os.Lstat(d)
			if err != nil || !fi.IsDir() {
				continue
			}
			if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid {
				continue
			}
			if err := os.RemoveAll(d); err != nil {
				t.Logf("socket dir cleanup %s: %v", d, err)
			}
		}
	})
}

// keyringUsable is Linux's: on darwin the store keeps its key in the login
// Keychain and needs no unlock.
func keyringUsable() bool { return true }
