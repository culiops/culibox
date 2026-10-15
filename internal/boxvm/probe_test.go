package boxvm

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strconv"
	"strings"
	"testing"
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
