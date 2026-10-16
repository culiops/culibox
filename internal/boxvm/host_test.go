package boxvm

import (
	"context"
	"errors"
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

func TestCheckUserDockerContextUnrunnableIsUnchecked(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckUserDockerContext(context.Background(), bin); !errors.Is(err, ErrContextUnchecked) {
		t.Fatalf("an unrunnable docker CLI is unknown, not fine: %v", err)
	}
}

func TestColimaVersionRunsWithThePinnedEnv(t *testing.T) {
	t.Setenv("COLIMA_HOME", "/users/own/.colima") // must not reach colima
	dir := t.TempDir()
	seen := filepath.Join(dir, "seen")
	bin := filepath.Join(dir, "colima")
	script := "#!/bin/sh\nprintf %s \"$COLIMA_HOME\" > " + seen + "\necho colima version 0.8.1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Root: "/data/ab", Home: "/home/a", ColimaBin: bin, DockerBin: "/d", CPUs: 4, MemoryGiB: 4, BrokerPort: 9071}
	if v := ColimaVersion(context.Background(), cfg); v != "0.8.1" {
		t.Fatalf("version = %q", v)
	}
	got, err := os.ReadFile(seen)
	if err != nil || string(got) != cfg.ColimaHome() {
		t.Fatalf("colima ran with COLIMA_HOME=%q (%v), want %q", got, err, cfg.ColimaHome())
	}
}
