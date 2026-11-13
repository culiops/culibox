//go:build uidemo

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// #235 item 6: the demo creates folders in its fake home, so it refuses the
// real home, a folder that holds it, and a fake home that is a link to it.
func TestUIDemoRefusesTheRealHome(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "u", "me")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(base, "scratch")
	for _, demo := range []string{real, real + "/", alias, filepath.Dir(real), "/"} {
		if err := uidemoHomeCheck(real, demo); err == nil {
			t.Errorf("uidemoHomeCheck(real, %q) accepted the real home", demo)
		}
	}
	if err := uidemoHomeCheck(real, ""); err == nil {
		t.Error("an empty fake home was accepted")
	}
	if err := uidemoHomeCheck(real, "relative/dir"); err == nil {
		t.Error("a relative fake home was accepted")
	}
	if err := uidemoHomeCheck(real, scratch); err != nil {
		t.Errorf("a scratch folder was refused: %v", err)
	}
}
