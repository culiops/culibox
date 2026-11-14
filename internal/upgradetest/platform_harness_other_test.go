//go:build upgrade && !darwin

package upgradetest

import (
	"errors"
	"os"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/secretstore"
)

// seedDarwin writes the darwin-only data root files; elsewhere there are none.
func seedDarwin(t *testing.T, root string) {}

// cleanupStoreKeys registers the removal of the unlocked store keys this run
// left in the kernel user keyring (one per data root KeyHash; an unlock keeps
// them for days). Only these hashes are touched. Without a keyring (a box)
// there is nothing to remove.
func cleanupStoreKeys(t *testing.T, hashes func() []string) {
	t.Cleanup(func() {
		seen := map[string]bool{}
		for _, h := range hashes() {
			if h == "" || seen[h] {
				continue
			}
			seen[h] = true
			// EPERM: no usable keyring here, so no key was ever saved.
			if err := secretstore.NewKeeperForHash(h).Clear(); err != nil && !errors.Is(err, syscall.EPERM) {
				t.Logf("store key cleanup %s: %v", h, err)
			}
		}
	})
}

// cleanupSocketDirs is darwin's: on Linux the abduco sockets live under the
// data root and go with the throwaway tree.
func cleanupSocketDirs(t *testing.T, roots func() []string) {}

// keyringUsable reports whether this process can keep a key in the kernel user
// keyring, which a store unlock needs: it saves and clears a one-second key
// under a name no store uses. A box's seccomp profile refuses keyctl (EPERM).
func keyringUsable() bool {
	k := secretstore.NewKeeperForHash("upgrade-test-probe-" + strconv.Itoa(os.Getpid()))
	if err := k.Save("probe", time.Second); err != nil {
		return false
	}
	_ = k.Clear()
	return true
}
