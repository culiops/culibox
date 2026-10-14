package config

import (
	"os"
	"path/filepath"
	"testing"
)

func execFile(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseDsclUserShell(t *testing.T) {
	if got := parseDsclUserShell("UserShell: /bin/zsh\n"); got != "/bin/zsh" {
		t.Errorf("got %q", got)
	}
	if got := parseDsclUserShell("No such key: UserShell\n"); got != "" {
		t.Errorf("missing key must give \"\", got %q", got)
	}
	if got := parseDsclUserShell("RecordName: u\nUserShell:   /opt/homebrew/bin/fish  \n"); got != "/opt/homebrew/bin/fish" {
		t.Errorf("got %q", got)
	}
}

func TestDetectShellDarwin(t *testing.T) {
	env := execFile(t, "envsh")
	ds := execFile(t, "dssh")
	none := func() string { return "" }
	if got := detectShellDarwin(env, func() string { return ds }); got != env {
		t.Errorf("$SHELL must win, got %q", got)
	}
	if got := detectShellDarwin("", func() string { return ds }); got != ds {
		t.Errorf("Directory Services second, got %q", got)
	}
	if got := detectShellDarwin("", none); got != "/bin/zsh" {
		t.Errorf("fallback must be /bin/zsh (macOS default), got %q", got)
	}
	if got := detectShellDarwin("relative/sh", none); got != "/bin/zsh" {
		t.Errorf("unusable $SHELL must fall through, got %q", got)
	}
}

func TestDetectShellDarwinSrc(t *testing.T) {
	env := execFile(t, "envsh")
	ds := execFile(t, "dssh")
	none := func() string { return "" }
	for _, c := range []struct {
		envShell string
		fromDS   func() string
		src      string
	}{
		{env, func() string { return ds }, "$SHELL"},
		{"", func() string { return ds }, "dscl"},
		{"", none, "fallback"},
	} {
		if _, src := detectShellDarwinSrc(c.envShell, c.fromDS); src != c.src {
			t.Errorf("source = %q, want %q", src, c.src)
		}
	}
}
