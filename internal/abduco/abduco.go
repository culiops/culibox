// Package abduco isolates every abduco specific behind one small, testable
// surface. abduco holds an agent's PTY in a daemonized "server" process, so the
// agent survives the daemon (this app) restarting. The daemon connects to a
// session with a replaceable `abduco -a` attach process whose PTY it pumps.
//
// Two hard-won rules from POC (both encoded here):
//   - Discovery is by a deterministic PIDFILE, never pgrep: a `pgrep -f` match
//     can hit our own wrapper shell's command line and kill the wrong group.
//   - Tearing a session down needs TWO kills: the inner agent's PROCESS GROUP
//     (agent + its children, no orphans) AND the abduco SERVER (a separate
//     process group that does not die with the inner; killing it unlinks the
//     socket and delists the session).
package abduco

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/culiops/culibox/internal/procinfo"
)

// ErrNotFound means the session was never created / has no usable pidfile.
var ErrNotFound = errors.New("abduco: session not found")

// Adapter runs abduco against a dedicated socket dir (isolation) and a pidfile
// dir. The socket dir is set on EVERY abduco exec — List/adopt correctness and
// isolation both depend on that invariant.
type Adapter struct {
	Bin       string // "abduco"
	SocketDir string
	PidDir    string
	DetachKey string // caret form, e.g. "^^" = Ctrl-^
	host      string
	user      string
	// CheckDir: when set, every Create re-runs PrepareSocketDir on SocketDir
	// instead of trusting MkdirAll; serve sets it where it prepared the dir at
	// startup.
	CheckDir bool
}

// New creates the adapter and ensures its dirs exist (0700).
func New(bin, socketDir, pidDir string) (*Adapter, error) {
	if bin == "" {
		bin = "abduco"
	}
	host, _ := os.Hostname()
	a := &Adapter{Bin: bin, SocketDir: socketDir, PidDir: pidDir, DetachKey: "^^", host: host, user: CurrentUser()}
	if err := a.ensureDirs(); err != nil {
		return nil, err
	}
	return a, nil
}

// User and Host are the values abduco puts in this adapter's socket paths.
func (a *Adapter) User() string { return a.user }
func (a *Adapter) Host() string { return a.host }

func (a *Adapter) ensureDirs() error {
	if a.CheckDir {
		// The socket dir may sit in a shared /tmp: if it vanished, MkdirAll would
		// quietly accept a dir another local user pre-created at the same path.
		if err := PrepareSocketDir(a.SocketDir, os.Getuid()); err != nil {
			return err
		}
	} else if err := os.MkdirAll(a.SocketDir, 0700); err != nil {
		return err
	}
	return os.MkdirAll(a.PidDir, 0700)
}

func (a *Adapter) pidfile(id string) string { return filepath.Join(a.PidDir, id+".pid") }

// withSocketDir forces ABDUCO_SOCKET_DIR onto env (dedupe any inherited one).
func (a *Adapter) withSocketDir(env []string) []string {
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if strings.HasPrefix(e, "ABDUCO_SOCKET_DIR=") {
			continue
		}
		out = append(out, e)
	}
	return append(out, "ABDUCO_SOCKET_DIR="+a.SocketDir)
}

// Create makes a DETACHED session running argv under a wrapper that records the
// inner (agent) pid and the server pid deterministically. argv is passed as
// POSITIONAL PARAMETERS ("$@") — never string-interpolated (launcher.Build
// already quotes argv, e.g. for base=login). The wrapper uses only shell
// builtins (printf, exec) so a base=clean agent with no PATH still spawns — a
// deliberate choice over tmp+mv, which needs external `mv`.
func (a *Adapter) Create(id, cwd string, argv, env []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("abduco create %q: empty argv", id)
	}
	if err := CheckSocketPath(a.SocketDir, a.user, id, a.host); err != nil {
		return err
	}
	if err := a.ensureDirs(); err != nil {
		return err
	}
	pf := a.pidfile(id)
	_ = os.Remove(pf)
	// $0 = pidfile, "$@" = argv. printf/exec are builtins.
	const wrap = `printf 'inner=%s srv=%s' "$$" "$PPID" > "$0"; exec "$@"`
	args := append([]string{"-n", id, "sh", "-c", wrap, pf}, argv...)
	cmd := exec.Command(a.Bin, args...)
	cmd.Dir = cwd // agent (via abduco → wrapper → exec) inherits the working dir
	cmd.Env = a.withSocketDir(env)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("abduco create %q: %w: %s", id, err, strings.TrimSpace(string(out)))
	}
	// Poll InnerAlive (not just Alive): the socket is bound by the daemonized
	// server slightly before the wrapper writes its pidfile, and callers rely on
	// the pid record existing the moment Create returns.
	for i := 0; i < 100; i++ {
		if a.InnerAlive(id) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("abduco create %q: session/pidfile not ready after create", id)
}

// Attach starts `abduco -e <detachkey> -a <id>` on a fresh PTY. The detach key
// lives on the attach client (it intercepts it) so a user typing through the WS
// can't accidentally detach with the default Ctrl-\. Resizing this PTY forwards
// SIGWINCH to the agent (POC-verified) so the caller's existing resize path is
// unchanged.
func (a *Adapter) Attach(id string, rows, cols uint16) (*exec.Cmd, *os.File, error) {
	var lastErr error
	for i := 0; i < 25; i++ {
		if !a.Alive(id) {
			lastErr = fmt.Errorf("session %q not alive", id)
			time.Sleep(20 * time.Millisecond)
			continue
		}
		cmd := exec.Command(a.Bin, "-e", a.DetachKey, "-a", id)
		cmd.Env = a.withSocketDir(os.Environ())
		f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
		if err == nil {
			return cmd, f, nil
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	return nil, nil, fmt.Errorf("abduco attach %q: %w", id, lastErr)
}

// List returns live session names by walking the socket dir for socket files
// (<SocketDir>/abduco/<user>/<name>@<host>) and stripping the "@<host>" suffix.
func (a *Adapter) List() ([]string, error) {
	if _, err := os.Stat(a.SocketDir); os.IsNotExist(err) {
		return nil, nil
	}
	var ids []string
	err := filepath.WalkDir(a.SocketDir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, e := d.Info()
		if e != nil || info.Mode()&os.ModeSocket == 0 {
			return nil
		}
		name := d.Name()
		if i := strings.LastIndex(name, "@"); i >= 0 {
			name = name[:i]
		}
		ids = append(ids, name)
		return nil
	})
	return ids, err
}

// Alive reports whether the session's socket is present. NOT the exit/detach
// discriminator (socket unlink races the attach EOF) — use InnerAlive for that.
func (a *Adapter) Alive(id string) bool {
	ids, _ := a.List()
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// InnerAlive is the authoritative agent-liveness check: the inner pid exists and
// is genuinely ours (its parent is our abduco server, whose id-bearing cmdline we
// verify). Correctly false after the agent exits even while the server lingers.
func (a *Adapter) InnerAlive(id string) bool {
	inner, srv, err := readPidfile(a.pidfile(id))
	if err != nil {
		return false
	}
	return inner > 1 && ppidOf(inner) == srv && a.srvIsOurs(srv, id)
}

// InnerPid returns the agent's own pid from the session's pidfile (`inner=`).
// The wrapper execs the agent, so this is the agent process itself — the key
// Claude Code's live registry (~/.claude/sessions/<pid>.json) is filed under.
func (a *Adapter) InnerPid(id string) (int, bool) {
	inner, _, err := readPidfile(a.pidfile(id))
	if err != nil || inner <= 0 {
		return 0, false
	}
	return inner, true
}

// Kill tears the session down: inner PROCESS GROUP (agent + children, TERM→KILL)
// then the abduco SERVER (delist/unlink). Guarded against reused pids. Idempotent.
func (a *Adapter) Kill(id string) error {
	pf := a.pidfile(id)
	inner, srv, err := readPidfile(pf)
	if err != nil {
		return ErrNotFound
	}
	// The server's id-bearing cmdline is the anchor of trust. If srv is NOT our
	// abduco server for this id, pids may have been reused and NEITHER pid is
	// trustworthy — signal nothing (the reused-pid guard, applied to the whole
	// teardown, not just the server kill). Limitation: if the server died
	// out-of-band, we won't group-kill the inner here; in practice a server dying
	// closes the pty master and the agent takes SIGHUP with it — a rare surviving
	// orphan is strictly preferable to SIGKILLing a reused foreign process group.
	if srv <= 1 || !a.srvIsOurs(srv, id) {
		_ = os.Remove(pf)
		return nil
	}
	if inner > 1 && ppidOf(inner) == srv {
		_ = syscall.Kill(-inner, syscall.SIGTERM)
		if !waitGone(inner, 3*time.Second) {
			_ = syscall.Kill(-inner, syscall.SIGKILL)
			waitGone(inner, time.Second)
		}
	}
	_ = syscall.Kill(srv, syscall.SIGTERM)
	if !waitGone(srv, 2*time.Second) {
		_ = syscall.Kill(srv, syscall.SIGKILL)
		waitGone(srv, time.Second)
	}
	_ = os.Remove(pf)
	return nil
}

// srvIsOurs confirms pid is an abduco server for exactly this id (exact-token
// match on id so "bash-1" doesn't match "bash-10").
func (a *Adapter) srvIsOurs(pid int, id string) bool {
	hasAbduco, hasID := false, false
	for _, arg := range cmdlineArgs(pid) {
		if strings.Contains(arg, "abduco") {
			hasAbduco = true
		}
		if arg == id {
			hasID = true
		}
	}
	return hasAbduco && hasID
}

func readPidfile(path string) (inner, srv int, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	for _, f := range strings.Fields(string(b)) {
		kv := strings.SplitN(f, "=", 2)
		if len(kv) != 2 {
			continue
		}
		n, e := strconv.Atoi(kv[1])
		if e != nil {
			continue
		}
		switch kv[0] {
		case "inner":
			inner = n
		case "srv":
			srv = n
		}
	}
	if inner == 0 || srv == 0 {
		return inner, srv, fmt.Errorf("abduco: incomplete pidfile %q", path)
	}
	return inner, srv, nil
}

// ppidOf is the parent pid, or 0 when it cannot be read — callers compare it
// against a known server pid, so 0 always reads as "not ours".
func ppidOf(pid int) int {
	p, err := procinfo.Ppid(pid)
	if err != nil {
		return 0
	}
	return p
}

// cmdlineArgs is the process argv, or nil when it cannot be read.
func cmdlineArgs(pid int) []string {
	a, err := procinfo.Args(pid)
	if err != nil {
		return nil
	}
	return a
}

func procExists(pid int) bool { return syscall.Kill(pid, 0) == nil }

func waitGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !procExists(pid) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return !procExists(pid)
}

// attachArgv reports whether argv is an abduco ATTACH client (`-a`/`-A`). A
// server (`-n`/`-c`) is the user's running agent; killing one would destroy the
// session persistence exists to protect.
func attachArgv(argv []string) bool {
	if len(argv) == 0 || filepath.Base(argv[0]) != "abduco" {
		return false
	}
	attach := false
	for _, a := range argv[1:] {
		switch a {
		case "-n", "-c":
			return false
		case "-a", "-A":
			attach = true
		}
	}
	return attach
}

// isAttacher reports whether a process with this argv and env is an attach
// client whose ABDUCO_SOCKET_DIR is exactly socketDir. Session ids collide
// freely between dev, prod and throwaway instances, so the socket dir — not the
// id — is the owner check.
func isAttacher(argv, env []string, socketDir string) bool {
	if !attachArgv(argv) {
		return false
	}
	return slices.Contains(env, "ABDUCO_SOCKET_DIR="+socketDir)
}

// SweepOrphanAttachers kills abduco attach clients left behind by a previous
// daemon for THIS socket dir, and returns how many it killed.
//
// Call it at startup, before Reconcile creates this daemon's own clients — at
// that moment every matching client is by definition an orphan. This is the only
// cleanup that survives a daemon SIGKILL or panic, where no shutdown handler
// ever runs; without it such an orphan spins at 100% CPU until the box is
// rebooted or someone notices.
func (a *Adapter) SweepOrphanAttachers() int {
	pids, err := procinfo.PIDs()
	if err != nil {
		return 0
	}
	self := os.Getpid()
	killed := 0
	for _, pid := range pids {
		if pid <= 1 || pid == self {
			continue
		}
		argv, err := procinfo.Args(pid)
		if err != nil || !attachArgv(argv) {
			continue // argv first: env is read only for abduco attach clients
		}
		env, err := procinfo.Env(pid)
		if err != nil || !isAttacher(argv, env, a.SocketDir) {
			continue
		}
		// SIGKILL directly: a spinning client is in a tight userspace loop and
		// has nothing to flush, and this runs before any session is wired up.
		if err := syscall.Kill(pid, syscall.SIGKILL); err == nil {
			killed++
		}
	}
	return killed
}
