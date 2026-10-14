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

	noPasswd := filepath.Join(dir, "no-such-passwd") // isolate the $SHELL rules

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
			if got := detectShell(tc.shell, noPasswd, 1001); got != tc.want {
				t.Fatalf("detectShell(%q) = %q, want %q", tc.shell, got, tc.want)
			}
		})
	}

	// The exported entry point must return something actually runnable on this host.
	t.Run("exported DetectShell returns a usable shell", func(t *testing.T) {
		got := DetectShell()
		if !usableShell(got) {
			t.Fatalf("DetectShell() = %q, which is not a usable shell", got)
		}
	})
}

// $SHELL is absent from non-login contexts — notably a systemd --user service, which
// is how this console runs in production. The login shell from /etc/passwd is the
// authoritative fallback; without it prod would silently detect /bin/bash.
func TestDetectShellFallsBackToPasswd(t *testing.T) {
	dir := t.TempDir()
	zsh := filepath.Join(dir, "zsh")
	if err := os.WriteFile(zsh, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "fish")
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	passwd := filepath.Join(dir, "passwd")
	body := "root:x:0:0:root:/root:/bin/sh\n" +
		"svc:x:999:999::/home/svc:/usr/sbin/nologin\n" +
		"claude:x:1001:1001::/home/claude:" + zsh + "\n"
	if err := os.WriteFile(passwd, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, envShell string
		uid            int
		want           string
	}{
		{"no SHELL uses the passwd entry", "", 1001, zsh},
		{"SHELL still wins when usable", other, 1001, other},
		{"unusable SHELL falls through to passwd", "/usr/sbin/nologin", 1001, zsh},
		{"nologin passwd entry falls back to bash", "", 999, "/bin/bash"},
		{"uid absent from passwd falls back to bash", "", 4242, "/bin/bash"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectShell(tc.envShell, passwd, tc.uid); got != tc.want {
				t.Fatalf("detectShell(%q, uid %d) = %q, want %q", tc.envShell, tc.uid, got, tc.want)
			}
		})
	}

	t.Run("missing passwd file falls back to bash", func(t *testing.T) {
		if got := detectShell("", filepath.Join(dir, "nope"), 1001); got != "/bin/bash" {
			t.Fatalf("got %q, want /bin/bash", got)
		}
	})
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

	t.Run("second collision falls back to <base>-shell", func(t *testing.T) {
		t.Setenv("SHELL", sh)
		in := []AgentConfig{{Name: "zsh", Bin: "bash"}, {Name: "shell", Bin: "bash"}}
		out, name := WithDetectedShell(in)
		if name != "zsh-shell" {
			t.Fatalf("name = %q, want zsh-shell", name)
		}
		if len(out) != 3 || out[2].Name != "zsh-shell" || out[2].Bin != sh {
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

func TestDetectShellSrc(t *testing.T) {
	dir := t.TempDir()
	sh := filepath.Join(dir, "zsh")
	if err := os.WriteFile(sh, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	passwd := filepath.Join(dir, "passwd")
	if err := os.WriteFile(passwd, []byte("u:x:1001:1001::/home/u:"+sh+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		env, want string
		uid       int
		wantSrc   string
	}{
		{sh, sh, 1001, "$SHELL"},
		{"", sh, 1001, "passwd"},
		{"", "/bin/bash", 4242, "fallback"},
	} {
		got, src := detectShellSrc(c.env, passwd, c.uid)
		if got != c.want || src != c.wantSrc {
			t.Errorf("detectShellSrc(%q, uid %d) = %q,%q want %q,%q", c.env, c.uid, got, src, c.want, c.wantSrc)
		}
	}
	if sh, src := DetectShellSource(); sh != DetectShell() || src == "" {
		t.Errorf("DetectShellSource = %q,%q; must agree with DetectShell %q", sh, src, DetectShell())
	}
}
