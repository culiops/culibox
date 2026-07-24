// Package tmux is a thin adapter over the tmux CLI. tmux is the source of
// truth for sessions; this package never caches state.
package tmux

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// SocketName is the dedicated tmux socket (tmux -L) the console uses for every
// operation. It isolates the console's tmux server from any other tmux on the
// host — most importantly the user's own default-socket sessions — so a console
// bug (e.g. an abruptly-killed attach client) can never wedge or take down a
// tmux server that holds unrelated work.
const SocketName = "agentconsole"

// Session is a live tmux session.
type Session struct {
	Name    string
	Created time.Time
}

// Client is the tmux operations the app depends on.
type Client interface {
	List() ([]Session, error)
	New(name, cwd string, argv, env []string) error
	Kill(name string) error
	Has(name string) (bool, error)
}

// CLIClient implements Client by shelling out to tmux. Run is injectable so
// logic can be unit-tested without a real tmux server.
type CLIClient struct {
	Run func(args ...string) ([]byte, error)
}

// NewCLIClient returns a CLIClient backed by the real tmux binary. Every
// invocation is pinned to the dedicated SocketName socket (tmux -L) so the
// console never shares a server with other tmux sessions on the host.
func NewCLIClient() *CLIClient {
	return &CLIClient{Run: func(args ...string) ([]byte, error) {
		full := append([]string{"-L", SocketName}, args...)
		return exec.Command("tmux", full...).CombinedOutput()
	}}
}

// List returns live sessions. A missing tmux server (no sessions) is not an
// error — it is the common first-load state and yields an empty slice.
func (c *CLIClient) List() ([]Session, error) {
	out, err := c.Run("list-sessions", "-F", "#{session_name}\t#{session_created}")
	if err != nil {
		s := string(out)
		if strings.Contains(s, "no server running") || strings.Contains(s, "no sessions") {
			return nil, nil
		}
		return nil, fmt.Errorf("tmux list-sessions: %v: %s", err, strings.TrimSpace(s))
	}
	var sessions []Session
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		sess := Session{Name: parts[0]}
		if epoch, e := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64); e == nil {
			sess.Created = time.Unix(epoch, 0)
		}
		sessions = append(sessions, sess)
	}
	return sessions, nil
}

// New creates a detached session running argv in cwd, injecting env as
// per-session tmux environment (the secrets/MCP seam).
func (c *CLIClient) New(name, cwd string, argv, env []string) error {
	args := []string{"new-session", "-d", "-s", name}
	if cwd != "" {
		args = append(args, "-c", cwd)
	}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	if len(argv) > 0 {
		args = append(args, "--")
		args = append(args, argv...)
	}
	if out, err := c.Run(args...); err != nil {
		return fmt.Errorf("tmux new-session: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Kill terminates a session.
func (c *CLIClient) Kill(name string) error {
	if out, err := c.Run("kill-session", "-t", name); err != nil {
		return fmt.Errorf("tmux kill-session: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Has reports whether a session exists. A missing session/server is (false, nil).
func (c *CLIClient) Has(name string) (bool, error) {
	out, err := c.Run("has-session", "-t", name)
	if err != nil {
		s := string(out)
		if strings.Contains(s, "can't find session") || strings.Contains(s, "no server running") {
			return false, nil
		}
		return false, fmt.Errorf("tmux has-session: %v: %s", err, strings.TrimSpace(s))
	}
	return true, nil
}
