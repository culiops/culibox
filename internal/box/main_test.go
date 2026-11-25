package box

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain points TMPDIR at the resolved temp directory (#296). On macOS the
// default is under /var/folders, and /var is a symlink to /private/var: the
// product resolves paths, so a test comparing the path it made with the one the
// product reports would fail there for no product reason.
//
// It also turns the git refusal off (#371). Most of this package's Wrap tests box
// a git repo in place to test the guards (.git, hooks, the macOS locks), which
// stay as the second net behind that refusal; they reach them through Wrap with
// a git fixture. TestWrapRefusesInPlaceGitRepo turns the refusal back on.
func TestMain(m *testing.M) {
	gitInPlaceRefusal = func(string) error { return nil }
	if real, err := filepath.EvalSymlinks(os.TempDir()); err == nil && real != os.TempDir() {
		os.Setenv("TMPDIR", real)
	}
	os.Exit(m.Run())
}
