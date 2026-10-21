package boxvm

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/box"
)

var portRe = regexp.MustCompile(`const port=(\d+);`)

func TestProbePassesWhenBlockedAndNetworkAlive(t *testing.T) {
	err := ProbeLoopback(context.Background(), func(context.Context, string) (string, error) {
		return "DNS ok\nBLOCKED host.lima.internal ETIMEDOUT\nBLOCKED 192.168.5.2 ETIMEDOUT\n", nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestProbeFailsWhenReached(t *testing.T) {
	err := ProbeLoopback(context.Background(), func(context.Context, string) (string, error) {
		return "DNS ok\nREACHED host.lima.internal\nBLOCKED 192.168.5.2 ETIMEDOUT\n", nil
	})
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("a reached listener must fail: %v", err)
	}
}

// The real defence against a lying script: the listener itself counts accepts.
// The client closes straight away, so the handshake is queued but never
// Accepted by the time exec returns; the probe must still count it.
func TestProbeFailsWhenTheListenerAcceptsAConnection(t *testing.T) {
	err := ProbeLoopback(context.Background(), func(_ context.Context, script string) (string, error) {
		m := portRe.FindStringSubmatch(script)
		if m == nil {
			t.Fatal("script must embed the port")
		}
		port, _ := strconv.Atoi(m[1])
		c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			t.Fatalf("dialling the probe listener: %v", err)
		}
		c.Close()
		return "DNS ok\nBLOCKED host.lima.internal\n", nil
	})
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("an accepted connection must fail the probe even if the output says BLOCKED: %v", err)
	}
}

func TestProbeFailsClosed(t *testing.T) {
	for name, fn := range map[string]ExecFunc{
		"exec error": func(context.Context, string) (string, error) { return "", errors.New("node: not found") },
		"no lines":   func(context.Context, string) (string, error) { return "", nil },
		"no dns": func(context.Context, string) (string, error) {
			return "DNS fail ENOTFOUND\nBLOCKED host.lima.internal\n", nil
		},
		"dns only": func(context.Context, string) (string, error) { return "DNS ok\n", nil },
		"garbage":  func(context.Context, string) (string, error) { return "Segmentation fault\n", nil },
	} {
		if err := ProbeLoopback(context.Background(), fn); err == nil {
			t.Fatalf("%s: the probe must fail closed", name)
		}
	}
}

// The two connects run together, so a DROP rule costs one timeout, not two.
func TestProbeScriptConnectsConcurrently(t *testing.T) {
	var script string
	_ = ProbeLoopback(context.Background(), func(_ context.Context, s string) (string, error) {
		script = s
		return "DNS ok\nBLOCKED a\n", nil
	})
	if !strings.Contains(script, "Promise.all") {
		t.Fatal("the in-box connects must run concurrently")
	}
}

func TestThrowawayProbeUsesTheGlobalArgsAndTheLockdownFlags(t *testing.T) {
	dir := t.TempDir()
	seen := filepath.Join(dir, "argv")
	bin := filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+seen+"\necho DNS ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cli := box.CLIClient{Bin: bin, Args: []string{"--host", "unix:///vm/docker.sock", "--config", "/data/docker"}}
	out, err := ThrowawayProbe(cli, "img:tag")(context.Background(), "console.log(1)")
	if err != nil || out != "DNS ok" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	b, _ := os.ReadFile(seen)
	got := strings.Split(strings.TrimSpace(string(b)), "\n")
	want := []string{"--host", "unix:///vm/docker.sock", "--config", "/data/docker", "run", "--rm", "--cap-drop=ALL",
		"--security-opt", "no-new-privileges", "--read-only", "--pull=never", "--entrypoint", "node", "img:tag", "-e", "console.log(1)"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("argv = %q\nwant   %q", got, want)
	}
}

// A bound box's DNS is closed on purpose, so the DNS control cannot be part of
// its loopback check: "DNS fail" is expected and is not a reason to refuse. The
// egress probe's ALLOW 200 (run first) is the positive control instead.
func TestProbeLoopbackBoundNeedsNoDNS(t *testing.T) {
	err := ProbeLoopbackBound(context.Background(), func(context.Context, string) (string, error) {
		return "DNS fail EAI_AGAIN\nBLOCKED a ETIMEDOUT\nBLOCKED b ETIMEDOUT\n", nil
	})
	if err != nil {
		t.Fatalf("a closed DNS must not fail the bound check: %v", err)
	}
	// The plain check still insists on the control.
	err = ProbeLoopback(context.Background(), func(context.Context, string) (string, error) {
		return "DNS fail EAI_AGAIN\nBLOCKED a ETIMEDOUT\nBLOCKED b ETIMEDOUT\n", nil
	})
	if err == nil {
		t.Fatal("ProbeLoopback without DNS must stay inconclusive")
	}
}

func TestProbeLoopbackBoundFailsClosed(t *testing.T) {
	for name, fn := range map[string]ExecFunc{
		"reached":    func(context.Context, string) (string, error) { return "BLOCKED a\nREACHED b\n", nil },
		"exec error": func(context.Context, string) (string, error) { return "", errors.New("node: not found") },
		"no lines":   func(context.Context, string) (string, error) { return "", nil },
		"dns only":   func(context.Context, string) (string, error) { return "DNS ok\n", nil },
		"garbage":    func(context.Context, string) (string, error) { return "Segmentation fault\n", nil },
	} {
		if err := ProbeLoopbackBound(context.Background(), fn); err == nil {
			t.Fatalf("%s: the bound check must fail closed", name)
		}
	}
}

// The listener's own accept count is still the defence against a lying script.
func TestProbeLoopbackBoundFailsWhenTheListenerAccepts(t *testing.T) {
	err := ProbeLoopbackBound(context.Background(), func(_ context.Context, script string) (string, error) {
		m := portRe.FindStringSubmatch(script)
		if m == nil {
			t.Fatal("script must embed the port")
		}
		c, err := net.Dial("tcp", "127.0.0.1:"+m[1])
		if err != nil {
			t.Fatalf("dialling the probe listener: %v", err)
		}
		c.Close()
		return "BLOCKED host.lima.internal\n", nil
	})
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("an accepted connection must fail the bound check: %v", err)
	}
}

// The bound script tries the gateway by name and by the measured address, and
// does not wait on a DNS lookup that a bound box cannot answer.
func TestProbeLoopbackBoundScript(t *testing.T) {
	var script string
	_ = ProbeLoopbackBound(context.Background(), func(_ context.Context, s string) (string, error) {
		script = s
		return "BLOCKED a\n", nil
	})
	for _, want := range []string{"host.lima.internal", LimaHostGateway, "Promise.all"} {
		if !strings.Contains(script, want) {
			t.Errorf("bound script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "api.anthropic.com") {
		t.Errorf("bound script must not use the DNS control:\n%s", script)
	}
	if node, err := exec.LookPath("node"); err == nil {
		f := filepath.Join(t.TempDir(), "p.js")
		if err := os.WriteFile(f, []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(node, "--check", f).CombinedOutput(); err != nil {
			t.Fatalf("bound script does not parse: %v\n%s", err, out)
		}
	}
}
