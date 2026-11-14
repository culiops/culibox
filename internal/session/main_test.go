package session

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain points TMPDIR at the resolved temp directory (#296). On macOS the
// default is under /var/folders, and /var is a symlink to /private/var: the
// product resolves paths, so a test comparing the path it made with the one the
// product reports would fail there for no product reason.
func TestMain(m *testing.M) {
	if real, err := filepath.EvalSymlinks(os.TempDir()); err == nil && real != os.TempDir() {
		os.Setenv("TMPDIR", real)
	}
	os.Exit(m.Run())
}
