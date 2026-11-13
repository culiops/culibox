//go:build upgrade && !darwin

package upgradetest

import "testing"

// seedDarwin writes the darwin-only data root files; elsewhere there are none.
func seedDarwin(t *testing.T, root string) {}
