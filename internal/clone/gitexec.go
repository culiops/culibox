// Package clone creates and serves a boxed profile's clone of a host repo
// (spec §6). This file holds the only place agent-box executes the host's git:
// TestOnlyGitexecRunsGit fails if any other non-test file runs it.
//
// The rules that keep a box-owned .git from being a way onto the host:
// git runs on the host only while a clone is being created (before any box has
// seen it) or against the user's own repo, never with `-C <clone>`; and every
// exec gets an explicit clean environment, never os.Environ().
package clone

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

const (
	maxGitStdout = 16 << 20 // a git call here prints a config value or a progress line, not a pack
	maxGitStderr = 8 << 10  // error text kept in the returned error
)

// Git runs one host git command. dir == "" means no -C. env is used exactly:
// an implementation must not add the process environment to it.
type Git interface {
	Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error)
}

// ExecGit is the real Git, running the binary at Bin.
type ExecGit struct{ Bin string }

// NewExecGit finds git on the daemon's PATH. This is the only LookPath("git")
// in the codebase.
func NewExecGit() (ExecGit, error) {
	bin, err := exec.LookPath("git")
	if err != nil {
		return ExecGit{}, fmt.Errorf("git not found on PATH (clone mode needs the host git): %w", err)
	}
	return ExecGit{Bin: bin}, nil
}

// CleanEnv is the environment of every host git exec: no inherited variables
// (no GIT_DIR, GIT_EXEC_PATH, GIT_SSH_COMMAND, credentials, proxies), no system
// or user config, no prompts. Callers that need the user's global config
// replace GIT_CONFIG_GLOBAL and HOME explicitly (see globalEnv).
func CleanEnv() []string {
	return []string{
		"PATH=/usr/bin:/bin",
		"LC_ALL=C",
		"HOME=/nonexistent",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
	}
}

// hardenFlags are the git options every host git call in a user repo
// (clone_of) starts with. A hook (reference-transaction fires on the ref update
// of fetch; measured, git 2.43) or an fsmonitor command planted in that repo's
// .git must never run on the host, whatever the repo's own config says: a
// command-line -c beats the repo config.
//
// Nor may a call leave work running after it returns: `git fetch` starts
// automatic maintenance (`maintenance run --auto`, detached since git 2.47;
// `gc --auto` before 2.29), which with git 2.56 was measured repacking clone_of
// (cruft pack, multi-pack-index, info/refs) after the fetch had returned.
// maintenance.auto=false and gc.auto=0 turn that off for CuliBox's own calls
// only; the user's git still maintains the repo as before.
var hardenFlags = []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
	"-c", "maintenance.auto=false", "-c", "gc.auto=0"}

// hardened is args with hardenFlags in front, before the subcommand (and before
// any caller -c, which is for the fetch's fsck options).
func hardened(args ...string) []string {
	return append(append([]string{}, hardenFlags...), args...)
}

// StdinGit is a Git that can also feed a command's standard input. Only the
// fetch path needs it (index-pack --stdin of a bundle's pack); it lives here so
// this file stays the only place git is executed.
type StdinGit interface {
	Git
	RunStdin(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) ([]byte, error)
}

// StreamGit is a Git that can hand a command's standard output to the caller
// as it is produced, instead of buffering it under maxGitStdout. The handback
// scan needs it: it reads blobs of any size and keeps only their first bytes.
// It lives here for the same reason as StdinGit.
type StreamGit interface {
	Git
	Stream(ctx context.Context, dir string, env []string, stdin io.Reader, stdout io.Writer, args ...string) error
}

// Run executes git with stdin closed and env as the whole environment. stdout
// is capped at 16 MiB (more is an error, not a truncation); the error carries
// at most 8 KiB of stderr. A non-zero exit is an *exec.ExitError under the
// returned error (errors.As).
func (g ExecGit) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	return g.RunStdin(ctx, dir, env, nil, args...)
}

// RunStdin is Run with stdin read from the given reader (nil = closed).
func (g ExecGit) RunStdin(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) ([]byte, error) {
	argv := make([]string, 0, len(args)+2)
	if dir != "" {
		argv = append(argv, "-C", dir)
	}
	argv = append(argv, args...)
	cmd := exec.CommandContext(ctx, g.Bin, argv...)
	cmd.Env = append([]string{}, env...) // non-nil: an empty env is empty, not the process env
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdin = stdin
	out := &capBuffer{max: maxGitStdout}
	errb := &capBuffer{max: maxGitStderr}
	cmd.Stdout, cmd.Stderr = out, errb
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", sub, err, strings.TrimSpace(errb.buf.String()))
	}
	if out.over {
		return nil, fmt.Errorf("git %s: output larger than %d bytes", sub, maxGitStdout)
	}
	return out.buf.Bytes(), nil
}

// Stream is RunStdin with stdout going to the caller's writer as git writes it.
// A writer that returns an error stops git: the process is killed (WaitDelay
// bounds the wait for its pipes) and that error is returned, so a reader that
// has seen enough of a huge blob ends it without draining it. Stderr is kept up
// to maxGitStderr for the error text, as in Run.
func (g ExecGit) Stream(ctx context.Context, dir string, env []string, stdin io.Reader, stdout io.Writer, args ...string) error {
	argv := make([]string, 0, len(args)+2)
	if dir != "" {
		argv = append(argv, "-C", dir)
	}
	argv = append(argv, args...)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(cctx, g.Bin, argv...)
	cmd.Env = append([]string{}, env...)
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdin = stdin
	sw := &stopWriter{w: stdout, stop: cancel}
	errb := &capBuffer{max: maxGitStderr}
	cmd.Stdout, cmd.Stderr = sw, errb
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	err := cmd.Run()
	if sw.err != nil {
		return sw.err // the reader stopped git; the kill is not a git failure
	}
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", sub, err, strings.TrimSpace(errb.buf.String()))
	}
	return nil
}

// stopWriter remembers the first error of w and calls stop on it, which kills
// the process that is writing.
type stopWriter struct {
	w    io.Writer
	stop func()
	err  error
}

func (s *stopWriter) Write(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	n, err := s.w.Write(p)
	if err != nil {
		s.err = err
		s.stop()
	}
	return n, err
}

// capBuffer keeps the first max bytes and swallows the rest, so a chatty child
// never blocks on a full pipe; over reports that bytes were dropped.
type capBuffer struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); len(p) <= room {
		c.buf.Write(p)
	} else {
		if room > 0 {
			c.buf.Write(p[:room])
		}
		c.over = true
	}
	return len(p), nil
}

// exitCode returns git's exit status when err is a non-zero exit.
func exitCode(err error) (int, bool) {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), true
	}
	return 0, false
}
