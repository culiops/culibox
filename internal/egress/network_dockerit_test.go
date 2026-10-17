//go:build dockerit

package egress

import (
	"context"
	"net"
	"os/exec"
	"testing"
)

// cliDocker runs the real docker CLI.
type cliDocker struct{}

func (cliDocker) Run(ctx context.Context, args []string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return string(out), err
}

// TestRealDockerInternalNetwork creates a throwaway --internal network on the
// real docker daemon, checks the flags and that the gateway IP is listenable and
// dialable from the host, and removes it. The name is agentbox-egress-T8TEST-UNIT and
// the instance label is T8TEST, so it cannot collide with a live instance's network.
func TestRealDockerInternalNetwork(t *testing.T) {
	const instance = "T8TEST"
	name := NetworkName(instance + "-UNIT")
	ctx := context.Background()
	t.Cleanup(func() { exec.Command("docker", "network", "rm", name).Run() })
	if _, ok, err := InspectNetwork(ctx, cliDocker{}, name); err != nil || ok {
		t.Skipf("network %s already exists or docker unusable (ok=%v err=%v); not touching it", name, ok, err)
	}
	n, err := EnsureNetwork(ctx, cliDocker{}, name, instance, Want{Internal: true})
	if err != nil {
		t.Fatal(err)
	}
	if !n.Internal {
		t.Fatalf("not internal: %+v", n)
	}
	addr, err := listenAddr("linux", n, 0)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("cannot listen on the gateway %s: %v", addr, err)
	}
	defer l.Close()
	go func() {
		if c, err := l.Accept(); err == nil {
			c.Close()
		}
	}()
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatalf("cannot dial the gateway from the host: %v", err)
	}
	c.Close()
	// The label is on it.
	out, _ := exec.Command("docker", "network", "inspect", name, "--format", "{{index .Labels \""+InstanceLabel+"\"}}").Output()
	if string(out) != instance+"\n" {
		t.Fatalf("label = %q", out)
	}
}
