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

func TestWithDetectedShell(t *testing.T) {
	dir := t.TempDir()
	sh := filepath.Join(dir, "zsh")
	if err := os.WriteFile(sh, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("appends when no profile uses that shell", func(t *testing.T) {
		t.Setenv("SHELL", sh)
		out, name := WithDetectedShell([]AgentConfig{{Name: "claude", Bin: "claude"}})
		if name != "zsh" {
			t.Fatalf("name = %q, want zsh", name)
		}
		if len(out) != 2 {
			t.Fatalf("len(out) = %d, want 2", len(out))
		}
		got := out[1]
		if got.Name != "zsh" || got.Bin != sh || got.BasePolicy() != BaseInherit {
			t.Fatalf("appended = %+v", got)
		}
	})

	t.Run("existing profile with same bin wins, nothing appended", func(t *testing.T) {
		t.Setenv("SHELL", sh)
		out, name := WithDetectedShell([]AgentConfig{{Name: "my-shell", Bin: sh}})
		if name != "my-shell" {
			t.Fatalf("name = %q, want my-shell", name)
		}
		if len(out) != 1 {
			t.Fatalf("len(out) = %d, want 1 (no duplicate)", len(out))
		}
	})

	t.Run("basename match also counts as existing", func(t *testing.T) {
		t.Setenv("SHELL", sh)
		out, name := WithDetectedShell([]AgentConfig{{Name: "zshell", Bin: "zsh"}})
		if name != "zshell" || len(out) != 1 {
			t.Fatalf("name = %q len = %d, want zshell/1", name, len(out))
		}
	})

	t.Run("name taken by another bin falls back to shell", func(t *testing.T) {
		t.Setenv("SHELL", sh)
		out, name := WithDetectedShell([]AgentConfig{{Name: "zsh", Bin: "bash"}})
		if name != "shell" {
			t.Fatalf("name = %q, want shell", name)
		}
		if len(out) != 2 || out[1].Name != "shell" || out[1].Bin != sh {
			t.Fatalf("out = %+v", out)
		}
	})

	t.Run("all candidate names taken gives no quick shell", func(t *testing.T) {
		t.Setenv("SHELL", sh)
		in := []AgentConfig{
			{Name: "zsh", Bin: "bash"},
			{Name: "shell", Bin: "bash"},
			{Name: "zsh-shell", Bin: "bash"},
		}
		out, name := WithDetectedShell(in)
		if name != "" {
			t.Fatalf("name = %q, want empty", name)
		}
		if len(out) != 3 {
			t.Fatalf("len(out) = %d, want 3 (unchanged)", len(out))
		}
	})

	// A plain append() would write the seed into the caller's spare capacity,
	// silently changing a slice the caller still holds. len() can never catch
	// that — inspect the backing array itself.
	t.Run("does not write into the caller's backing array", func(t *testing.T) {
		t.Setenv("SHELL", sh)
		in := make([]AgentConfig, 1, 4)
		in[0] = AgentConfig{Name: "claude", Bin: "claude"}
		WithDetectedShell(in)
		if len(in) != 1 {
			t.Fatalf("caller slice length changed: %+v", in)
		}
		if spare := in[:2]; spare[1].Name != "" || spare[1].Bin != "" {
			t.Fatalf("wrote into the caller's spare capacity: %+v", spare[1])
		}
	})
}
