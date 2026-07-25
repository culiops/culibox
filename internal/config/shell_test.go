package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectShell(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "zsh")
	if err := os.WriteFile(good, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	notExec := filepath.Join(dir, "noexec")
	if err := os.WriteFile(notExec, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	nologin := filepath.Join(dir, "nologin")
	if err := os.WriteFile(nologin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, shell, want string
	}{
		{"executable shell is used", good, good},
		{"unset falls back", "", "/bin/bash"},
		{"nologin falls back", nologin, "/bin/bash"},
		{"non-executable falls back", notExec, "/bin/bash"},
		{"missing path falls back", filepath.Join(dir, "nope"), "/bin/bash"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SHELL", tc.shell)
			if got := DetectShell(); got != tc.want {
				t.Fatalf("DetectShell() = %q, want %q", got, tc.want)
			}
		})
	}
}
