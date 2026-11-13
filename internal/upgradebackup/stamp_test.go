package upgradebackup

import (
	"os"
	"path/filepath"
	"testing"
)

// A symlink planted at the stamp's temp name must not make the stamp write
// overwrite the link's target.
func TestWriteStampDoesNotFollowPlantedTmpSymlink(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "credentials.json", `{"secret":"keep"}`)
	if err := os.Symlink("credentials.json", filepath.Join(dir, StampName+".tmp")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	if err := writeStamp(root, "v2", "abc"); err != nil {
		t.Fatalf("writeStamp: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "credentials.json")); string(b) != `{"secret":"keep"}` {
		t.Errorf("credentials.json = %q: the stamp write followed the planted link", b)
	}
	fi, err := os.Lstat(filepath.Join(dir, StampName))
	if err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("last-version is not a regular file: %v %v", fi, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, StampName)); string(b) != "v2 abc\n" {
		t.Errorf("last-version = %q, want %q", b, "v2 abc\n")
	}
	if _, err := os.Lstat(filepath.Join(dir, StampName+".tmp")); err == nil {
		t.Error("the temp file is left behind")
	}
}
