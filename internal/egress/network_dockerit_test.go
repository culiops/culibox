//go:build dockerit

package egress

import (
	"context"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"
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

// TestRealDockerICCOffIsolatesBoxes (spec correction 11): on an internal
// network created with inter-container traffic off, one container cannot open
// a TCP connection to a listener in another, while both still reach a listener
// on the gateway (where the proxy listens). It also checks BoxAddr against a
// real container. Everything it creates carries the instance label T11TEST and
// the network is agentbox-egress-T11TEST-ICC; only those are removed.
func TestRealDockerICCOffIsolatesBoxes(t *testing.T) {
	const instance = "T11TEST"
	const image = "alpine:3"
	name := NetworkName(instance + "-ICC")
	ctx := context.Background()
	if out, err := exec.Command("docker", "image", "inspect", image, "--format", "{{.Id}}").CombinedOutput(); err != nil {
		t.Skipf("image %s not present: %v: %s", image, err, out)
	}
	if _, ok, err := InspectNetwork(ctx, cliDocker{}, name); err != nil || ok {
		t.Skipf("network %s already exists or docker unusable (ok=%v err=%v); not touching it", name, ok, err)
	}
	label := InstanceLabel + "=" + instance
	rmContainers := func() {
		out, _ := exec.Command("docker", "ps", "-aq", "--filter", "label="+label).Output()
		if ids := strings.Fields(string(out)); len(ids) > 0 {
			exec.Command("docker", append([]string{"rm", "-f"}, ids...)...).Run()
		}
	}
	t.Cleanup(func() {
		rmContainers()
		exec.Command("docker", "network", "rm", name).Run()
	})
	n, err := EnsureNetwork(ctx, cliDocker{}, name, instance, Want{Internal: true, NoICC: true})
	if err != nil {
		t.Fatal(err)
	}
	if !n.Internal || !n.NoICC {
		t.Fatalf("network: %+v", n)
	}
	// Ensuring the same network again against the real daemon is accepted:
	// inspect read the ICC-off option back.
	if _, err := EnsureNetwork(ctx, cliDocker{}, name, instance, Want{Internal: true, NoICC: true}); err != nil {
		t.Fatal(err)
	}

	// A listener on the gateway, as the proxy has.
	addr, err := listenAddr("linux", n, 0)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen on the gateway %s: %v", addr, err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("gw\n"))
			c.Close()
		}
	}()
	_, gwPort, _ := net.SplitHostPort(l.Addr().String())

	start := func(cname, cmd string) {
		t.Helper()
		out, err := exec.Command("docker", "run", "-d", "--name", cname, "--label", label, "--network", name,
			image, "sh", "-c", cmd).CombinedOutput()
		if err != nil {
			t.Fatalf("docker run %s: %v: %s", cname, err, out)
		}
	}
	a, b := "agentbox-t11test-icc-a", "agentbox-t11test-icc-b"
	start(a, "while true; do echo a | nc -l -p 8080; done")
	start(b, "sleep 120")

	aIP, err := BoxAddr(ctx, cliDocker{}, a, name)
	if err != nil {
		t.Fatalf("BoxAddr(%s): %v", a, err)
	}
	bIP, err := BoxAddr(ctx, cliDocker{}, b, name)
	if err != nil {
		t.Fatalf("BoxAddr(%s): %v", b, err)
	}
	if !n.Subnet.Contains(aIP) || !n.Subnet.Contains(bIP) || aIP == bIP {
		t.Fatalf("box addresses %v, %v not distinct in %v", aIP, bIP, n.Subnet)
	}
	if _, err := BoxAddr(ctx, cliDocker{}, a, "bridge"); err == nil {
		t.Fatal("BoxAddr found the box on a network it is not on")
	}

	// connect runs nc in cname against host:port and reports whether it got a reply.
	connect := func(cname, host, port string) (bool, string) {
		out, err := exec.Command("docker", "exec", cname, "sh", "-c",
			"nc -w 3 "+host+" "+port+" </dev/null").CombinedOutput()
		return err == nil && strings.TrimSpace(string(out)) != "", strings.TrimSpace(string(out))
	}
	// Positive control: a's listener is up (from inside a itself).
	ok := false
	for i := 0; i < 20 && !ok; i++ {
		ok, _ = connect(a, "127.0.0.1", "8080")
		if !ok {
			time.Sleep(250 * time.Millisecond)
		}
	}
	if !ok {
		t.Fatal("the listener in container a never came up")
	}
	if ok, out := connect(b, aIP.String(), "8080"); ok {
		t.Fatalf("container b reached container a's listener with ICC off (%q)", out)
	}
	for _, c := range []string{a, b} {
		if ok, out := connect(c, n.Gateway.String(), gwPort); !ok || out != "gw" {
			t.Fatalf("container %s cannot reach the gateway listener: %q", c, out)
		}
	}
}
