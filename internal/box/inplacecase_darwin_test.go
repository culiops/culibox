//go:build darwin

package box

import (
	"errors"
	"path/filepath"
	"strings"
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

// A hooks path that spells the workspace in another case and goes through a
// symlink in it: it is the same folder, so the link is refused like the
// same-case spelling, not followed out (#299).
func TestInPlaceHooksCaseVariantSymlinkIsRefused(t *testing.T) {
	ws, lower := caseTwins(t)
	out := filepath.Join(filepath.Dir(ws), "out")
	mkdirs(t, filepath.Join(out, "hooks"))
	symlink(t, out, filepath.Join(ws, "link"))
	gitRepo(t, ws, "[core]\n\thooksPath = "+filepath.Join(lower, "link", "hooks")+"\n")
	_, err := inPlaceMounts(ws)
	if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "reached through the symlink") {
		t.Fatalf("want a symlink refusal, got %v", err)
	}
}
