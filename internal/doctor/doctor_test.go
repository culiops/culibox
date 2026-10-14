package doctor

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/procinfo"
)

func input(t *testing.T) Input {
	t.Helper()
	data, err := os.MkdirTemp("/tmp", "abdoc-") // short: the socket-length check uses it
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(data) })
	_ = os.Chmod(data, 0o700)
	return Input{
		Config: config.Config{
			BindAddr: "127.0.0.1:1", DataDir: data,
			AbducoSocketDir: filepath.Join(data, "s"),
			Agents:          []config.AgentConfig{{Name: "sh", Bin: "/bin/sh"}},
		},
		Source: config.Source{Path: "/x/config.json"},
		Home:   filepath.Dir(data), Commit: "abc123",
	}
}

func run(in Input) (string, int) {
	var b bytes.Buffer
	code := Run(&b, in)
	return b.String(), code
}

func needAbduco(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("abduco"); err != nil {
		t.Skip("abduco is not on PATH")
	}
}

func TestDoctor_NeverPrintsSecrets(t *testing.T) {
	in := input(t)
	const pass = "S3cret-Passw0rd-value"
	t.Setenv("AB_DOCTOR_PASS", pass)
	in.Config.PasswordEnv = "AB_DOCTOR_PASS"
	out, _ := run(in)
	if strings.Contains(out, pass) {
		t.Fatalf("password leaked:\n%s", out)
	}

	in = input(t)
	tok := strings.Repeat("A", 43)
	_ = os.WriteFile(filepath.Join(in.Config.DataDir, "login.token"), []byte(tok), 0o600)
	out, _ = run(in)
	if strings.Contains(out, tok) {
		t.Fatalf("login token leaked:\n%s", out)
	}
	if h, _ := os.Hostname(); h != "" && strings.Contains(out, h) {
		t.Fatalf("hostname printed (only its length may be):\n%s", out)
	}
	if strings.Contains(out, in.Home+"/") {
		t.Fatalf("home path not shown as ~:\n%s", out)
	}
}

func TestDoctor_ConfigErrorFails(t *testing.T) {
	in := input(t)
	in.ConfigErr = errors.New("parse config: boom")
	out, code := run(in)
	if code != 1 || !strings.Contains(out, "FAIL config") {
		t.Fatalf("code=%d\n%s", code, out)
	}
}

func TestDoctor_EmptyHomeFailsCleanly(t *testing.T) {
	in := input(t)
	in.Home = ""
	out, code := run(in)
	if code != 1 || !strings.Contains(out, "FAIL home:") {
		t.Fatalf("code=%d\n%s", code, out)
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "PASS data dir") {
			t.Errorf("must not derive paths from an empty home:\n%s", out)
		}
	}
}

func TestDoctor_HealthyLinuxBoxPasses(t *testing.T) {
	needAbduco(t)
	in := input(t)
	out, code := run(in)
	if code != 0 {
		t.Fatalf("expected no FAIL on this box, got:\n%s", out)
	}
	for _, check := range []string{"platform", "config", "data dir", "auth", "bind", "abduco binary",
		"abduco roundtrip", "procinfo", "socket length", "shell"} {
		if !strings.Contains(out, " "+check+":") {
			t.Errorf("missing check %q in:\n%s", check, out)
		}
	}
}

func TestDoctor_MissingAbducoFails(t *testing.T) {
	in := input(t)
	in.Config.AbducoBin = "abduco-does-not-exist"
	out, code := run(in)
	if code != 1 || !strings.Contains(out, "FAIL abduco binary") {
		t.Fatalf("code=%d\n%s", code, out)
	}
}

// Doctor must not PASS a config that serve refuses: a profile name that blows
// the socket-path limit fails the same helper serve uses.
func TestDoctor_SocketLengthMatchesServe(t *testing.T) {
	in := input(t)
	in.Config.Agents = append(in.Config.Agents, config.AgentConfig{Name: strings.Repeat("x", 120), Bin: "/bin/sh"})
	out, code := run(in)
	if code != 1 || !strings.Contains(out, "FAIL socket length") {
		t.Fatalf("code=%d\n%s", code, out)
	}
}

// The quick-shell profile serve appends is part of the length check.
func TestDoctor_SocketLengthIncludesDetectedShell(t *testing.T) {
	in := input(t)
	long := filepath.Join(t.TempDir(), strings.Repeat("s", 90))
	if err := os.WriteFile(long, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", long)
	in.Config.Agents = []config.AgentConfig{{Name: "a", Bin: "/bin/true"}}
	out, _ := run(in)
	if !strings.Contains(out, "FAIL socket length") {
		t.Fatalf("detected shell profile name ignored:\n%s", out)
	}
}

func TestDoctor_ShellSourceReported(t *testing.T) {
	in := input(t)
	t.Setenv("SHELL", "/bin/sh")
	out, _ := run(in)
	if !strings.Contains(out, "shell: /bin/sh (from $SHELL)") {
		t.Fatalf("shell source missing:\n%s", out)
	}
}

// The doctor probe must leave nothing behind: no temp dir, no live process.
func TestDoctor_LeavesNoSessionBehind(t *testing.T) {
	needAbduco(t)
	root, err := os.MkdirTemp("/tmp", "abdp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	old := probeRoot
	probeRoot = root
	t.Cleanup(func() { probeRoot = old })

	in := input(t)
	out, code := run(in)
	if code != 0 || !strings.Contains(out, "PASS abduco roundtrip") {
		t.Fatalf("probe did not run:\n%s", out)
	}
	if left, _ := os.ReadDir(root); len(left) != 0 {
		t.Errorf("probe left files behind: %v", left)
	}
	pids, _ := procinfo.PIDs()
	for _, p := range pids {
		args, err := procinfo.Args(p)
		if err == nil && strings.Contains(strings.Join(args, " "), root) {
			t.Errorf("probe left process %d running: %v", p, args)
		}
	}
}

// A planted FIFO or symlink in ~/.claude/sessions must neither hang doctor nor
// be followed.
func TestDoctor_RegistryIsReadDefensively(t *testing.T) {
	in := input(t)
	in.Home = t.TempDir()
	dir := filepath.Join(in.Home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "1.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "secret.json")
	body := fmt.Sprintf(`{"pid":%d,"procStart":"TOPSECRET-LINK-TARGET"}`, os.Getpid())
	_ = os.WriteFile(secret, []byte(body), 0o600)
	if err := os.Symlink(secret, filepath.Join(dir, "2.json")); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() { out, _ := run(in); done <- out }()
	select {
	case out := <-done:
		if strings.Contains(out, "TOPSECRET") {
			t.Errorf("followed a symlink:\n%s", out)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("doctor hung on a planted FIFO")
	}
}

func TestDoctor_RegistryCompareShowsBothValues(t *testing.T) {
	in := input(t)
	in.Home = t.TempDir()
	dir := filepath.Join(in.Home, ".claude", "sessions")
	_ = os.MkdirAll(dir, 0o700)
	st, err := procinfo.StartTime(os.Getpid())
	if err != nil {
		t.Skip(err)
	}
	body := fmt.Sprintf(`{"pid":%d,"procStart":%q,"cwd":"/very/private/cwd"}`, os.Getpid(), st)
	_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", os.Getpid())), []byte(body), 0o600)
	out, _ := run(in)
	if !strings.Contains(out, "PASS claude registry: pid") || !strings.Contains(out, "match=true") {
		t.Fatalf("registry compare missing:\n%s", out)
	}
	if strings.Contains(out, "/very/private/cwd") {
		t.Fatalf("registry cwd leaked:\n%s", out)
	}
}

func TestTilde(t *testing.T) {
	for _, c := range []struct{ in, home, want string }{
		{"/home/a/x and /home/a", "/home/a", "~/x and ~"},
		{"/home/ab/x", "/home/a", "/home/ab/x"},
		{"x", "", "x"},
		{"/home/a: err", "/home/a", "~: err"},
	} {
		if got := tilde(c.in, c.home); got != c.want {
			t.Errorf("tilde(%q,%q) = %q want %q", c.in, c.home, got, c.want)
		}
	}
}
