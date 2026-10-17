package box

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// CLIClient drives the real docker CLI. It is deliberately thin: every decision
// about WHAT to run lives in Manager, so the policy is unit-tested without a
// daemon and this type stays a transport.
type CLIClient struct {
	Bin string // absolute path to the docker binary
	// Args are global flags placed before every subcommand (--host, --config).
	// Empty on Linux. On macOS they pin agent-box's own colima VM: the session
	// exec runs under a curated env that drops DOCKER_HOST and DOCKER_CONTEXT,
	// so only flags reach the same daemon from every call site.
	Args []string
}

func (c CLIClient) command(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, c.Bin, append(append([]string(nil), c.Args...), args...)...)
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
	out, err := c.command(ctx, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// RunInput is run with stdin. It exists to hand a credential to `docker exec -i`
// without putting it in argv (world-readable through ps) or in the environment.
// The error carries the command's output but never the stdin payload, and the
// payload is not logged here.
func (c CLIClient) RunInput(ctx context.Context, args []string, stdin string) (string, error) {
	cmd := c.command(ctx, args...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Run starts a detached container and returns its id.
func (c CLIClient) Run(ctx context.Context, args []string) (string, error) {
	return c.run(ctx, args...)
}

// Inspect reports whether the named container is missing, running or stopped.
// "No such object" is the common first-spawn answer and not an error; any other
// failure (an unreachable daemon, a stopped VM) is, because guessing "missing"
// would send the caller into a docker run that cannot work either.
func (c CLIClient) Inspect(ctx context.Context, name string) (State, error) {
	// Stdout only: a CLI or context warning on stderr must not be read as the
	// answer, because "not exactly true" means Stopped and the caller removes it.
	out, err := c.command(ctx, "inspect", "-f", "{{.State.Running}}", name).Output()
	if err != nil {
		var stderr string
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr = strings.TrimSpace(string(ee.Stderr))
			// The wording's case differs between docker versions ("Error: No such
			// object" vs "error: no such object"), so compare case-insensitively.
			if l := strings.ToLower(stderr); strings.Contains(l, "no such object") || strings.Contains(l, "no such container") {
				return StateMissing, nil
			}
		}
		return StateMissing, fmt.Errorf("docker inspect: %w: %s", err, stderr)
	}
	switch s := strings.TrimSpace(string(out)); s {
	case "true":
		return StateRunning, nil
	case "false":
		return StateStopped, nil
	default:
		// Fail closed: the caller force-removes a Stopped box, so an answer we
		// cannot read must never be taken for one.
		return StateMissing, fmt.Errorf("docker inspect %s: unexpected output %q", name, s)
	}
}

// ListBoxes returns every container carrying the label, running or not.
func (c CLIClient) ListBoxes(ctx context.Context, label string) ([]BoxInfo, error) {
	out, err := c.run(ctx, "ps", "-a", "--filter", "label="+label, "--format",
		"{{.Names}}\t{{.State}}\t{{.Label \""+LabelWorkspace+"\"}}\t{{.CreatedAt}}")
	if err != nil || out == "" {
		return nil, err
	}
	var boxes []BoxInfo
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, "\t", 4)
		if len(f) < 2 {
			continue
		}
		b := BoxInfo{Name: f[0], State: f[1]}
		if len(f) > 2 {
			b.Workspace = f[2]
		}
		if len(f) > 3 {
			b.Created, _ = time.Parse("2006-01-02 15:04:05 -0700 MST", f[3])
		}
		boxes = append(boxes, b)
	}
	return boxes, nil
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
