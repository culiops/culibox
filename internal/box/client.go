package box

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// CLIClient drives the real docker CLI. It is deliberately thin: every decision
// about WHAT to run lives in Manager, so the policy is unit-tested without a
// daemon and this type stays a transport.
type CLIClient struct {
	Bin string // absolute path to the docker binary
}

// LookDocker resolves the docker binary to an absolute path. Boxed spawns run
// with a curated PATH that need not contain it, so the path is resolved once at
// startup rather than at spawn time.
func LookDocker(bin string) (string, error) {
	if bin == "" {
		bin = "docker"
	}
	if strings.Contains(bin, "/") {
		return bin, nil
	}
	p, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("box: docker binary %q not found on PATH: %w", bin, err)
	}
	return p, nil
}

func (c CLIClient) run(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, c.Bin, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Run starts a detached container and returns its id.
func (c CLIClient) Run(ctx context.Context, args []string) (string, error) {
	return c.run(ctx, args...)
}

// Inspect reports whether the named container exists AND is running. A missing
// container is not an error — it is the common case on first spawn.
func (c CLIClient) Inspect(ctx context.Context, name string) (bool, error) {
	out, err := exec.CommandContext(ctx, c.Bin, "inspect", "-f", "{{.State.Running}}", name).CombinedOutput()
	if err != nil {
		return false, nil // no such container
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

// ListByLabel returns container names carrying the label, running or not, so
// reconciliation can reap stopped leftovers too.
func (c CLIClient) ListByLabel(ctx context.Context, label string) ([]string, error) {
	out, err := c.run(ctx, "ps", "-a", "--filter", "label="+label, "--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// Stop removes the container. `rm -f` rather than `stop`: a stopped-but-present
// container would be adopted as "not running" on the next spawn and then collide
// on the name.
func (c CLIClient) Stop(ctx context.Context, name string) error {
	_, err := c.run(ctx, "rm", "-f", name)
	return err
}

// HasRuntime reports whether docker knows the named OCI runtime. Read-only
// detection: sysbox is never installed by this program, because its installer
// rewrites /etc/docker/daemon.json and restarts the daemon, which would bounce
// every container on the host.
func (c CLIClient) HasRuntime(ctx context.Context, name string) bool {
	out, err := c.run(ctx, "info", "--format", "{{json .Runtimes}}")
	if err != nil {
		return false
	}
	return strings.Contains(out, `"`+name+`"`)
}
