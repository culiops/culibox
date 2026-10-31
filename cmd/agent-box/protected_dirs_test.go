package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The import deny list covers the same directories protectedDirs does, so a
// CLAUDE.md cannot import agent-box's own config or profiles through a link.
func TestImportDenyDirsMirrorsProtectedDirs(t *testing.T) {
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(tmp, "cfg", "config.json")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(real, "config.json"), cfg); err != nil {
		t.Fatal(err)
	}
	profiles := filepath.Join(tmp, "prof", "profiles.json")
	got := importDenyDirs(filepath.Join(tmp, "data"), filepath.Join(tmp, "state"), []string{cfg}, profiles)
	for _, want := range []string{filepath.Join(tmp, "data"), filepath.Join(tmp, "state"), filepath.Dir(cfg), real, filepath.Dir(profiles)} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %s in %v", want, got)
		}
	}
}
