//go:build darwin

package box

import (
	"path/filepath"
	"testing"
)

// TestInPlaceHooksCaseVariantIsGuarded measures #299: on case-insensitive APFS
// a core.hooksPath that spells the workspace in another letter case is the same
// folder, so the in-place guard must bind its hooks dir exactly as it does for
// the same-case spelling.
func TestInPlaceHooksCaseVariantIsGuarded(t *testing.T) {
	ws, lower := caseTwins(t)
	gitRepo(t, ws, "[core]\n\thooksPath = "+filepath.Join(lower, "hooks")+"\n")
	mkdirs(t, filepath.Join(ws, "hooks"))
	got, err := inPlaceMounts(ws)
	if err != nil {
		t.Fatal(err)
	}
	wantMounts(t, ws, got, ".git", ".claude", "hooks")
}
