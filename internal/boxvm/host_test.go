package boxvm

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestVersionAtLeast(t *testing.T) {
	for v, want := range map[string]bool{"0.8.1": true, "0.8.0": true, "0.7.9": false, "1.0.0": true, "": false, "x.y": false} {
		if got := VersionAtLeast(v, 0, 8); got != want {
			t.Fatalf("VersionAtLeast(%q) = %v", v, got)
		}
	}
}

func TestCheckUserDockerContextFlagsOurProfile(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho colima-agent-box\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckUserDockerContext(context.Background(), bin); err == nil {
		t.Fatal("the user's context pointing at our VM must be an error")
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho colima\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckUserDockerContext(context.Background(), bin); err != nil {
		t.Fatalf("the user's own colima context is fine: %v", err)
	}
}
