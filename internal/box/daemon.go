package box

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ProbeTimeout bounds one daemon probe (Ping, and the runtime check at boot): a
// daemon that accepts the connection and never answers must not hold boot, a
// boxed spawn or /api/meta. A healthy or a stopped daemon answers in well under
// a second; this only caps the hung one.
const ProbeTimeout = 5 * time.Second

// The two ways a docker that is installed can be unusable, as a probe sees them.
// Anything else a probe reports is unclassified (DaemonReason says to run
// doctor). The error text of a classified error still carries docker's own
// words, for the log; the web layer never shows it (DaemonReason does).
var (
	// ErrDockerNotRunning: nothing answers at the daemon's endpoint (stopped, or
	// its socket is gone).
	ErrDockerNotRunning = errors.New("docker daemon is not running")
	// ErrDockerDenied: the endpoint is there and this user may not use it (not in
	// the docker group).
	ErrDockerDenied = errors.New("no permission to use the docker daemon")
)

// Ping asks the daemon to answer and classifies a failure: ErrDockerNotRunning,
// ErrDockerDenied, or the raw error. Read-only and cheap (`docker info` on a
// healthy host; a stopped daemon refuses at once). Callers bound it with
// ProbeTimeout.
func (c CLIClient) Ping(ctx context.Context) error {
	// Not c.run: WaitDelay is set for this probe only. A child that outlives docker
	// and keeps its output pipe open would otherwise hold CombinedOutput (and the
	// caller's "refresh running" flag) past ctx; the other docker calls are left as
	// they were, since a long-running one may rely on its pipe.
	cmd := c.command(ctx, "info", "--format", "{{.ServerVersion}}")
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if errors.Is(err, exec.ErrWaitDelay) {
		// docker itself exited 0 (Go reports this error only then) and a child held
		// the pipe: the daemon answered.
		err = nil
	}
	if err != nil {
		err = fmt.Errorf("docker info: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if err != nil && ctx.Err() != nil {
		// A killed child reads "signal: killed"; say what ended it.
		return fmt.Errorf("docker info: %w", ctx.Err())
	}
	return classifyDaemonErr(err)
}

// classifyDaemonErr maps what the docker CLI printed. Permission is tested
// first: its message also names the socket and the daemon. A context that ran
// out is neither: the daemon may be hung, and "not running" would be a guess.
func classifyDaemonErr(err error) error {
	if err == nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "permission denied"):
		return fmt.Errorf("%w: %v", ErrDockerDenied, err)
	case strings.Contains(s, "cannot connect to the docker daemon"),
		strings.Contains(s, "is the docker daemon running"),
		strings.Contains(s, "if the daemon is running"),
		strings.Contains(s, "connection refused"),
		(strings.Contains(s, "dial unix") || strings.Contains(s, "dial tcp")) && strings.Contains(s, "no such file or directory"):
		return fmt.Errorf("%w: %v", ErrDockerNotRunning, err)
	}
	return err
}

// DaemonReason is the plain sentence for a probe error ("" for nil), for the
// profile page and a refused spawn. Never err's own text: it can hold a socket
// path, and this goes to the browser.
func DaemonReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrDockerNotRunning):
		return "Docker is not running"
	case errors.Is(err, ErrDockerDenied):
		return "no permission to use Docker"
	}
	return "run `culibox doctor` for details"
}
