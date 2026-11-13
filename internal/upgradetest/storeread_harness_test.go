//go:build upgrade

package upgradetest

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/secretstore"
)

// ReadStoreCopy decrypts the store vN-1 wrote with the current build's
// secretstore package, from a copy, so nothing is written into the data root.
// Linux unlocks the copy with the passphrase through an in-memory keeper. On
// darwin the identity lives only in the login Keychain (no identity.age), so it
// is read, never saved or cleared, under the root's KeyHash.
func ReadStoreCopy(t *testing.T, root string, want map[string]string) {
	t.Helper()
	dir := t.TempDir()
	if err := copyTree(filepath.Join(root, "store"), filepath.Join(dir, "store")); err != nil {
		t.Fatal(err)
	}
	var st *secretstore.Store
	if runtime.GOOS == "darwin" {
		st = secretstore.Open(dir, secretstore.NewKeeperForHash(secretstore.KeyHash(root)), nil)
	} else {
		st = secretstore.Open(dir, secretstore.NewFakeKeeper(true), nil)
		if err := st.Unlock([]byte(passphrase), time.Minute, "upgrade-test"); err != nil {
			t.Fatalf("the current secretstore cannot unlock vN-1's store: %v", err)
		}
	}
	for id, v := range want {
		got, err := st.Get(id)
		if err != nil {
			t.Errorf("store value %s: %v", id, err)
			continue
		}
		if string(got) != v {
			t.Errorf("store value %s = %q, want %q", id, got, v)
		}
	}
	if !t.Failed() {
		t.Logf("PASS the current secretstore decrypts every vN-1 store value (%d)", len(want))
	}
}

// copyTree copies a directory tree, keeping file and directory modes.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dst, rel)
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			if err := os.MkdirAll(to, 0o700); err != nil {
				return err
			}
			return os.Chmod(to, fi.Mode().Perm())
		}
		return copyFile(p, to)
	})
}
