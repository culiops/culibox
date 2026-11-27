package box

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/culiops/culibox/internal/extraca"
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

// RunEnv is run with extra environment variables for the docker child only
// (added to the daemon's own environment; the daemon's is never changed). It
// exists to hand a secret to `docker run -e NAME` without putting the value in
// argv (world-readable through ps). The error carries the command's output but
// never env, and env is not logged here.
func (c CLIClient) RunEnv(ctx context.Context, args, env []string) (string, error) {
	cmd := c.command(ctx, args...)
	cmd.Env = append(cmd.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Stdout is run with stdout alone as the answer. Stderr only enters the error:
// a docker CLI or context warning beside a successful command must not be read
// as part of what the command printed.
func (c CLIClient) Stdout(ctx context.Context, args []string) (string, error) {
	out, err := c.command(ctx, args...).Output()
	if err != nil {
		var stderr string
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		return "", fmt.Errorf("docker %s: %w: %s", args[0], err, stderr)
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
	st, _, _, err := c.inspect(ctx, name, false)
	return st, err
}

// InspectMounts is Inspect plus the box's bind destinations, from the same
// docker call, so ensure pays one round trip under opMu, not two.
func (c CLIClient) InspectMounts(ctx context.Context, name string) (State, []string, BoxLabels, error) {
	return c.inspect(ctx, name, true)
}

// inspectMountsFormat prints the CA label on the first line (blank when the box
// has none), the identity label on the second (blank likewise), the running flag
// on the third and one "type|destination" line per mount after it.
const inspectMountsFormat = `{{index .Config.Labels "` + extraca.Label + `"}}{{"\n"}}{{index .Config.Labels "` + LabelIdentity +
	`"}}{{"\n"}}{{.State.Running}}{{range .Mounts}}{{"\n"}}{{.Type}}|{{.Destination}}{{end}}`

func (c CLIClient) inspect(ctx context.Context, name string, withMounts bool) (State, []string, BoxLabels, error) {
	format := "{{.State.Running}}"
	if withMounts {
		format = inspectMountsFormat
	}
	// Stdout only: a CLI or context warning on stderr must not be read as the
	// answer, because "not exactly true" means Stopped and the caller removes it.
	out, err := c.command(ctx, "inspect", "-f", format, name).Output()
	if err != nil {
		var stderr string
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr = strings.TrimSpace(string(ee.Stderr))
			// The wording's case differs between docker versions ("Error: No such
			// object" vs "error: no such object"), so compare case-insensitively.
			if l := strings.ToLower(stderr); strings.Contains(l, "no such object") || strings.Contains(l, "no such container") {
				return StateMissing, nil, BoxLabels{}, nil
			}
		}
		return StateMissing, nil, BoxLabels{}, fmt.Errorf("docker inspect: %w: %s", err, stderr)
	}
	// Only the end is trimmed: with the label line first, a box without a label
	// starts with a blank line that is part of the answer.
	text := strings.TrimRight(string(out), " \t\r\n")
	var labels BoxLabels
	if withMounts {
		l, rest, _ := strings.Cut(text, "\n")
		id, rest, _ := strings.Cut(rest, "\n")
		labels = BoxLabels{CA: strings.TrimSpace(l), Identity: strings.TrimSpace(id) == "1"}
		text = rest
	}
	first, rest, _ := strings.Cut(text, "\n")
	switch s := strings.TrimSpace(first); s {
	case "true":
		return StateRunning, parseMounts(rest), labels, nil
	case "false":
		return StateStopped, nil, labels, nil
	default:
		// Fail closed: the caller force-removes a Stopped box, so an answer we
		// cannot read must never be taken for one.
		return StateMissing, nil, BoxLabels{}, fmt.Errorf("docker inspect %s: unexpected output %q", name, s)
	}
}

// parseMounts reads InspectMounts' "type|destination" lines, keeping the binds:
// they are all runArgs creates, while a tmpfs or an image-declared volume is not
// a mount the caller asked for.
func parseMounts(out string) []string {
	var dsts []string
	for _, line := range strings.Split(out, "\n") {
		if typ, dst, ok := strings.Cut(strings.TrimSpace(line), "|"); ok && typ == "bind" && dst != "" {
			dsts = append(dsts, dst)
		}
	}
	return dsts
}

// BindSource is the host path of the named box's bind mount at dst, or "" when
// it has none, from docker inspect.
func (c CLIClient) BindSource(ctx context.Context, name, dst string) (string, error) {
	out, err := c.Stdout(ctx, []string{"inspect", "--type", "container", "--format", bindSourcesFormat, name})
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 3)
		if len(parts) == 3 && parts[0] == "bind" && parts[1] == dst {
			return parts[2], nil
		}
	}
	return "", nil
}

// BindSources is the host path of each of the named box's bind mounts, by
// in-box destination, from one docker inspect.
func (c CLIClient) BindSources(ctx context.Context, name string) (map[string]string, error) {
	out, err := c.Stdout(ctx, []string{"inspect", "--type", "container", "--format", bindSourcesFormat, name})
	if err != nil {
		return nil, err
	}
	srcs := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 3)
		if len(parts) == 3 && parts[0] == "bind" && parts[1] != "" {
			if _, dup := srcs[parts[1]]; !dup {
				srcs[parts[1]] = parts[2]
			}
		}
	}
	return srcs, nil
}

// bindSourcesFormat prints one "type|destination|source" line per mount.
const bindSourcesFormat = `{{range .Mounts}}{{.Type}}|{{.Destination}}|{{.Source}}{{"\n"}}{{end}}`

// ListBoxes returns every container carrying the label, running or not.
func (c CLIClient) ListBoxes(ctx context.Context, label string) ([]BoxInfo, error) {
	out, err := c.run(ctx, "ps", "-a", "--filter", "label="+label, "--format", boxListFormat)
	if err != nil || out == "" {
		return nil, err
	}
	return parseBoxList(out), nil
}

// boxListFormat is the --format of ListBoxes; parseBoxList reads it back. It
// ends with a fixed terminator field: the CLI output is trimmed, which eats the
// trailing tabs of a last row with empty labels, and the terminator is how a
// complete row is told from a short or over-long one.
const boxListFormat = "{{.Names}}\t{{.State}}\t{{.Label \"" + LabelWorkspace + "\"}}\t{{.CreatedAt}}" +
	"\t{{.Label \"" + LabelInstance + "\"}}\t{{.Label \"" + LabelExclusive + "\"}}\t{{.Label \"" + LabelClaims + "\"}}" +
	"\t" + boxListEnd

const (
	boxListEnd    = "."
	boxListFields = 8
)

// parseBoxList reads boxListFormat output. A row is readable only when it has
// exactly the format's fields and ends with the terminator; anything else (a tab
// or newline inside a label of a container agent-box did not create, or output
// in another format) is still parsed best-effort, for display, but marked
// Unreadable so no safety check trusts its labels.
func parseBoxList(out string) []BoxInfo {
	var boxes []BoxInfo
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 2 {
			continue
		}
		b := BoxInfo{Name: f[0], State: f[1]}
		b.Unreadable = len(f) != boxListFields || f[boxListFields-1] != boxListEnd
		if len(f) > 2 {
			b.Workspace = f[2]
		}
		if len(f) > 3 {
			b.Created, _ = time.Parse("2006-01-02 15:04:05 -0700 MST", f[3])
		}
		if len(f) > 4 {
			b.Instance = f[4]
		}
		if len(f) > 5 {
			b.Exclusive = f[5] == "1"
		}
		if len(f) > 6 {
			b.Claims = f[6] == "1"
		}
		boxes = append(boxes, b)
	}
	return boxes
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

// mountSourcesFormat prints one line per container: its full id, then its mounts
// as JSON. Only the mounts: a whole inspect answer carries the container's env.
const mountSourcesFormat = `{{.Id}} {{json .Mounts}}`

// MountSources returns the host path of every mount of every container in the
// docker store, running or not and whichever instance created it. Anything it
// cannot read is an error, never a shorter list: the caller deletes what is not
// listed. A container removed between the two calls fails the listing too, which
// costs one retried pass.
func (c CLIClient) MountSources(ctx context.Context) ([]string, error) {
	out, err := c.Stdout(ctx, []string{"ps", "-a", "-q", "--no-trunc"})
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	ids := strings.Split(out, "\n")
	for _, id := range ids {
		if !isContainerID(id) {
			return nil, fmt.Errorf("docker ps: unexpected output %q", id)
		}
	}
	out, err = c.Stdout(ctx, append([]string{"inspect", "--type", "container", "--format", mountSourcesFormat}, ids...))
	if err != nil {
		return nil, err
	}
	return parseMountSources(ids, out)
}

// isContainerID is a full container id, as `ps --no-trunc` prints it.
func isContainerID(s string) bool {
	if len(s) != 64 {
		return false
	}
	return strings.Trim(s, "0123456789abcdef") == ""
}

// parseMountSources reads mountSourcesFormat. Every id asked for must be
// answered, and a mount that should have a host path must state one.
func parseMountSources(ids []string, out string) ([]string, error) {
	seen := make(map[string]bool, len(ids))
	var sources []string
	for line := range strings.Lines(out) {
		id, raw, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			return nil, fmt.Errorf("docker inspect: unexpected line %q", id)
		}
		var mounts *[]struct{ Type, Source string }
		if err := json.Unmarshal([]byte(raw), &mounts); err != nil {
			return nil, fmt.Errorf("docker inspect: mounts of %s: %w", id, err)
		}
		if mounts == nil {
			return nil, fmt.Errorf("docker inspect: %s has no mount list", id)
		}
		for _, mt := range *mounts {
			switch {
			case mt.Type == "":
				return nil, fmt.Errorf("docker inspect: %s has a mount without a type", id)
			case mt.Type == "tmpfs":
				continue // has no host path to hold on to
			case !strings.HasPrefix(mt.Source, "/"):
				return nil, fmt.Errorf("docker inspect: %s has a %s mount with source %q", id, mt.Type, mt.Source)
			}
			sources = append(sources, mt.Source)
		}
		seen[id] = true
	}
	for _, id := range ids {
		if !seen[id] {
			return nil, fmt.Errorf("docker inspect: no answer for container %s", id)
		}
	}
	return sources, nil
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
//
// Only a runtime list docker printed is an answer (#334): an error says docker
// could not tell (not running, slow, or an answer listing no runtime, which a
// working daemon never gives since it always has runc), and the caller asks
// again rather than settle on runc. Stdout only, so a CLI warning on stderr
// does not spoil the list; WaitDelay as in Ping, so a child holding the pipe
// cannot hold the probe past ctx.
func (c CLIClient) HasRuntime(ctx context.Context, name string) (bool, error) {
	cmd := c.command(ctx, "info", "--format", "{{json .Runtimes}}")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil // docker exited 0; a child kept the pipe
	}
	if err != nil {
		var stderr string
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		return false, fmt.Errorf("docker info: %w: %s", err, stderr)
	}
	var runtimes map[string]json.RawMessage
	if err := json.Unmarshal(out, &runtimes); err != nil {
		return false, fmt.Errorf("docker info: unreadable Runtimes: %w", err)
	}
	if len(runtimes) == 0 {
		return false, errors.New("docker info: listed no runtime")
	}
	_, ok := runtimes[name]
	return ok, nil
}

// SeccompActive reports whether the docker daemon applies a seccomp profile to
// containers by default: "name=seccomp" is listed in SecurityOptions without
// "profile=unconfined". Read-only. An error means docker could not say.
func (c CLIClient) SeccompActive(ctx context.Context) (bool, error) {
	out, err := c.run(ctx, "info", "--format", "{{json .SecurityOptions}}")
	if err != nil {
		return false, err
	}
	var opts []string
	if err := json.Unmarshal([]byte(out), &opts); err != nil {
		return false, fmt.Errorf("docker info: unreadable SecurityOptions: %w", err)
	}
	for _, o := range opts {
		fields := strings.Split(o, ",")
		if fields[0] != "name=seccomp" {
			continue
		}
		for _, f := range fields[1:] {
			if f == "profile=unconfined" {
				return false, nil
			}
		}
		return true, nil
	}
	return false, nil
}
