// Package box runs agent sessions inside a per-workspace container.
//
// It owns box identity, lifecycle, and the argv transformation that turns a
// host command into a `docker exec` into the right box. It deliberately does
// NOT compose environment or resolve secrets — that stays in the launcher, so
// there is exactly one place where secret values are read.
//
// Roughly 90% of the containment this package buys comes from what is absent
// inside a box: no host filesystem beyond the workspace, no ~/.ssh, no host
// ~/.claude, no docker socket, no inherited daemon environment. The hardening
// flags are the remaining ~10%. The adversary in scope runs arbitrary userspace
// commands as the box user; a kernel-level adversary is explicitly out of scope.
package box

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Container labels. Instance keeps the DEV and PROD daemons from adopting or
// reaping each other's boxes.
const (
	LabelWorkspace = "io.culiops.agent-box.workspace"
	LabelInstance  = "io.culiops.agent-box.instance"
	namePrefix     = "ab-box-"
	stateDirName   = "boxstate"
	// lastUsedDirName holds one empty file per box whose mtime is the last-use
	// signal. It is not a box name, so the reclaim sweep never considers it.
	lastUsedDirName = ".lastused"

	runtimeRunc   = "runc"
	runtimeSysbox = "sysbox-runc"

	// boxHome is the image's home for the unprivileged agent user. The state
	// volume mounts over the WHOLE home so the agent keeps history and resume
	// data across spawns — see runArgs for why ~/.claude alone is not enough.
	boxHome = "/home/agent"
)

// Mount is an extra read-only bind the caller wants in the box. Passed in rather
// than discovered here, so this package keeps knowing nothing about agents or
// config and the deny-by-default mount rule stays visible in one function.
type Mount struct{ Host, Box string }

// State is what docker says about a box container. Stopped is not Missing: an
// exited container keeps its name, so treating it as absent makes the next
// `docker run --name` fail with Conflict until someone removes it.
type State int

const (
	StateMissing State = iota
	StateRunning
	StateStopped
)

// BoxInfo is one box container as `docker ps -a` reports it.
type BoxInfo struct {
	Name, Workspace, State string // State is docker's .State: running, exited, created, ...
	Created                time.Time
}

// ErrUnboxableWorkspace marks a refusal the user can act on directly: the
// workspace they picked cannot be contained. Its message names the path they
// chose and nothing else, so it is safe to show them — unlike a generic spawn
// failure, which must stay server-side.
var ErrUnboxableWorkspace = errors.New("workspace cannot be contained")

// ErrBoxUnavailable marks a boxed spawn the containment layer cannot serve right
// now: the box VM is not set up or will not start, the image is missing or built
// for another architecture, or a new box failed its containment check. Its
// message carries the remedy (a command, a path) and is shown to the user, so it
// must never contain env values or secrets.
var ErrBoxUnavailable = errors.New("containment unavailable")

// UnderMount reports whether path (absolute, symlinks resolved) is at or below
// one of mounts. Component-wise, so /Users/ab is not under /Users/a.
func UnderMount(path string, mounts []string) bool {
	for _, mt := range mounts {
		mt = filepath.Clean(mt)
		if path == mt || strings.HasPrefix(path, mt+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Client is the narrow slice of the docker CLI this package needs. A fake drives
// every unit test, so none of them requires a running daemon.
type Client interface {
	Run(ctx context.Context, args []string) (string, error)
	Inspect(ctx context.Context, name string) (State, error)
	ListBoxes(ctx context.Context, label string) ([]BoxInfo, error)
	ListByLabel(ctx context.Context, label string) ([]string, error)
	Stop(ctx context.Context, name string) error
	HasRuntime(ctx context.Context, name string) bool
}

// Options configures a Manager.
type Options struct {
	DockerBin string // absolute path; resolved by the caller at startup
	// DockerArgs are global docker flags for execArgv; they must equal the
	// Client's. The session exec runs under a curated env that drops
	// DOCKER_HOST/DOCKER_CONTEXT, while management calls inherit the daemon env,
	// so flags, not env, are the only way both groups reach the same daemon.
	DockerArgs []string
	Image      string
	Instance   string // env label (DEV/PROD)
	IdleTTL    time.Duration
	StateRoot  string // where per-box agent homes live (usually data_dir)
	Client     Client

	// Hooks for a containment layer that needs more than a local daemon (on
	// macOS: a VM). All are optional; nil leaves the behaviour unchanged.

	Prepare func(ctx context.Context) error             // before any docker call of a boxed spawn
	Verify  func(ctx context.Context, box string) error // right after a NEW container starts
	// Shared lists the host directories the docker daemon can see; a workspace
	// outside them is refused. nil = every path is visible.
	Shared []string
	// Reachable reports whether the daemon answers; false = down, and the idle
	// sweep skips instead of reaping on a failed list. nil = assume reachable.
	Reachable func(ctx context.Context) bool
}

// Manager tracks one box per workspace and keeps their lifecycle honest.
type Manager struct {
	opts    Options
	runtime string

	// opMu serializes start/stop of boxes against each other. mu guards only the
	// maps below and is never held across a docker call.
	opMu sync.Mutex

	mu     sync.Mutex
	active map[string]int // box name → live session count
	timers map[string]*time.Timer
	// verified holds the boxes this process has seen pass Verify since they were
	// last (re)created. With a Verify hook, ensure reuses a running box only if
	// it is listed here, so a box left over from before a daemon restart, or one
	// whose failed removal left it running, is verified again before any use.
	verified map[string]bool
	// drift marks boxes whose counter disagreed with the registry at the last
	// sweep (guarded by mu).
	drift map[string]bool
}

// New probes for a stronger runtime and returns a ready Manager. Detection is
// read-only: sysbox is never installed, because its installer rewrites
// /etc/docker/daemon.json and restarts the daemon, bouncing every container on
// the host.
func New(opts Options) (*Manager, error) {
	if opts.Client == nil {
		return nil, fmt.Errorf("box: a docker client is required")
	}
	if opts.Image == "" {
		return nil, fmt.Errorf("box: an image is required")
	}
	if opts.IdleTTL <= 0 {
		opts.IdleTTL = 15 * time.Minute
	}
	if opts.StateRoot == "" {
		return nil, fmt.Errorf("box: a state root is required")
	}
	m := &Manager{
		opts:    opts,
		runtime: runtimeRunc,
		active:  map[string]int{},
		timers:  map[string]*time.Timer{},

		verified: map[string]bool{},
		drift:    map[string]bool{},
	}
	if opts.Client.HasRuntime(context.Background(), runtimeSysbox) {
		m.runtime = runtimeSysbox
	}
	return m, nil
}

// Runtime reports the detected isolation runtime, for the UI badge.
func (m *Manager) Runtime() string { return m.runtime }

// boxName derives the container name from the canonical workspace path.
// Symlinks are resolved so two spellings of one directory never produce two
// boxes, and the instance is mixed in so DEV and PROD stay separate.
func (m *Manager) boxName(cwd string) (string, error) {
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", fmt.Errorf("box: resolving workspace %q: %w", cwd, err)
	}
	abs, err := filepath.Abs(real)
	if err != nil {
		return "", fmt.Errorf("box: absolute path for %q: %w", real, err)
	}
	sum := sha256.Sum256([]byte(m.opts.Instance + "\x00" + abs))
	return namePrefix + hex.EncodeToString(sum[:6]), nil
}

// stateDir is the HOST directory holding a box's agent home. It is a plain
// directory rather than a named volume on purpose: a named volume is seeded
// from the image and inherits the image's ownership, so any host uid that
// differs from the one baked at build time cannot write to its own home — no
// auth, no resume, nothing persisted. A directory the daemon creates is owned
// by the running user by construction, so the box works for every uid.
func (m *Manager) stateDir(boxName string) string {
	return filepath.Join(m.opts.StateRoot, stateDirName, boxName)
}

// markerPath is where a box's last-use timestamp lives. Deliberately OUTSIDE the
// box home: that directory is mounted as the agent's $HOME and the agent writes
// there constantly, and bookkeeping the host's reclaim depends on must not sit
// inside the blast radius of the thing being contained.
func (m *Manager) markerPath(boxName string) string {
	return filepath.Join(m.opts.StateRoot, stateDirName, lastUsedDirName, boxName)
}

// touchMarker records that a session used this box now. Best effort: a failure
// here must never fail a spawn. The cost of losing it is a home that looks older
// than it is, and the reclaim sweep refuses on two further grounds anyway.
func (m *Manager) touchMarker(boxName string) {
	p := m.markerPath(boxName)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	now := time.Now()
	if err := os.Chtimes(p, now, now); err == nil {
		return
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_ = f.Close()
}

// ensureStateDir creates the box's home directory before the box starts, owned
// by whoever runs the daemon. 0700: it holds agent credentials and history.
//
// It also seeds first-run state. A box home is empty, so an interactive agent
// otherwise meets an onboarding wizard rather than a prompt — the credential is
// forwarded and valid, but the agent never gets far enough to use it. These
// markers are configuration, not credentials.
//
// Everything BELOW the home is created through an os.Root anchored at it, never
// through plain path joins. The box writes in this directory as the daemon user,
// so between two spawns it can replace any path this function is about to create
// with a symlink and steer the daemon's own MkdirAll/WriteFile at a host path of
// its choosing. os.Root resolves each component with openat/O_NOFOLLOW
// semantics, so a symlink out of the home fails instead of being followed —
// the same boundary internal/web puts around $HOME for uploads and file ops.
func (m *Manager) ensureStateDir(boxName string, extraRO []Mount) (string, error) {
	dir := m.stateDir(boxName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("box: creating state dir %q: %w", dir, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", fmt.Errorf("box: opening state dir %q: %w", dir, err)
	}
	defer root.Close()

	if err := ensureMountpoints(root, extraRO); err != nil {
		return "", err
	}
	// O_EXCL is the whole never-clobber rule: it fails when the name exists in
	// any form, a symlink included, so there is no check-then-write window and
	// no way for an existing name to redirect the write.
	body := []byte("{\n  \"hasCompletedOnboarding\": true,\n  \"theme\": \"dark\"\n}\n")
	f, err := root.OpenFile(".claude.json", os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return dir, nil // state a running agent has already written
		}
		return "", fmt.Errorf("box: seeding agent state in %q: %w", dir, err)
	}
	defer f.Close()
	if _, err := f.Write(body); err != nil {
		return "", fmt.Errorf("box: seeding agent state in %q: %w", dir, err)
	}
	return dir, nil
}

// ensureMountpoints pre-creates the host side of every bind whose target sits
// inside the box home. Docker creates a missing bind target itself — as root,
// because the daemon is root — and those stubs land in the state directory,
// which the daemon user then cannot unlink or reclaim. Creating them here first
// means Docker finds them present and creates nothing.
//
// The type must match: a file-typed bind onto a directory (or the reverse) is a
// mount error, so the host side decides. Existing paths are left exactly as they
// are — a running agent may have written there.
//
// root is the box home. A path that leaves it — the box planted a symlink where
// a mountpoint belongs — is refused rather than followed or quietly skipped: a
// home in that state is one the box has rigged, and creating its container
// anyway would hand the same path to Docker, which resolves it as root.
func ensureMountpoints(root *os.Root, mounts []Mount) error {
	for _, mt := range mounts {
		rel, err := filepath.Rel(boxHome, mt.Box)
		if err != nil || rel == "." || rel == ".." ||
			strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue // outside the home: it lands on the read-only rootfs
		}
		if _, err := root.Lstat(rel); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("box: checking mountpoint %q: %w", rel, err)
		}
		if fi, err := os.Stat(mt.Host); err == nil && fi.IsDir() {
			if err := root.MkdirAll(rel, 0o700); err != nil {
				return fmt.Errorf("box: creating mountpoint %q: %w", rel, err)
			}
			continue
		}
		if parent := filepath.Dir(rel); parent != "." {
			if err := root.MkdirAll(parent, 0o700); err != nil {
				return fmt.Errorf("box: creating mountpoint parent of %q: %w", rel, err)
			}
		}
		f, err := root.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return fmt.Errorf("box: creating mountpoint %q: %w", rel, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("box: creating mountpoint %q: %w", rel, err)
		}
	}
	return nil
}

// unboxableWorkspace refuses a workspace whose mount would defeat the point.
// The workspace is the box's one writable mount, so containing $HOME hands the
// agent the whole home — ~/.ssh, any secrets file, the agent's own config —
// and every other control in this package becomes decoration. Refuse loudly
// rather than narrow silently: a surprising mount is how containment dies.
func unboxableWorkspace(cwd string, shared []string) (string, bool) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", false
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	if reason := MountUnsafe(abs); reason != "" {
		return reason, true
	}
	if abs == string(filepath.Separator) {
		return "it is the filesystem root", true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	if abs == filepath.Clean(home) {
		return "it is your home directory, so the box would mount everything in it", true
	}
	// An ancestor of home is worse still.
	if rel, err := filepath.Rel(abs, home); err == nil && rel != "." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
		return "it contains your home directory", true
	}
	if len(shared) > 0 && !UnderMount(abs, shared) {
		return "it is not under the box VM's shared directories (" + strings.Join(shared, ", ") +
			"), so docker cannot see it", true
	}
	return "", false
}

// Unboxable is the workspace refusal Wrap applies, for the boot-time check.
func (m *Manager) Unboxable(cwd string) (string, bool) { return unboxableWorkspace(cwd, m.opts.Shared) }

// Prepare readies the containment layer for a boxed spawn (on macOS: the VM is
// running and the image fits). The session layer calls it BEFORE resolving the
// config bundle, whose MCP probe needs a reachable daemon too.
func (m *Manager) Prepare(ctx context.Context) error {
	if m.opts.Prepare == nil {
		return nil
	}
	return m.opts.Prepare(ctx)
}

// Wrap ensures the workspace's box is running and returns the argv/env to
// execute. The returned env is the caller's env unchanged: it stays on the
// docker CLIENT process so `-e NAME` can forward values without ever putting
// them in argv, which is world-readable through `ps` on the host.
// It also returns the box name. That name is the box's identity from here on:
// callers persist it and use it for adopt/release/kill instead of re-deriving it
// from cwd. A workspace directory can be moved or deleted while its agent is
// still running, and a name that cannot be recomputed is a box that leaks or,
// worse, gets reaped out from under a live session.
func (m *Manager) Wrap(ctx context.Context, cwd, sessionID string, argv, env []string, extraRO []Mount) ([]string, []string, string, error) {
	if reason, bad := unboxableWorkspace(cwd, m.opts.Shared); bad {
		return nil, nil, "", fmt.Errorf("%w: %s (%s). Pick a project directory, "+
			"or choose \"Run on trusted host\" to run it uncontained on purpose",
			ErrUnboxableWorkspace, cwd, reason)
	}
	name, err := m.boxName(cwd)
	if err != nil {
		return nil, nil, "", err
	}
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return nil, nil, "", fmt.Errorf("box: resolving workspace %q: %w", cwd, err)
	}
	if err := m.ensure(ctx, name, real, extraRO); err != nil {
		return nil, nil, "", err
	}
	return m.execArgv(name, real, sessionID, argv, env), env, name, nil
}

// ensure starts the box if it is not already running, and registers the caller
// as a live session.
//
// The whole body runs under opMu, which also guards the idle stop. Without that
// serialization two first spawns race to `docker run` the same --name (docker
// answers with a Conflict), and a spawn can `docker exec` into a container the
// idle timer is concurrently removing.
func (m *Manager) ensure(ctx context.Context, name, cwd string, extraRO []Mount) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	if t, ok := m.timers[name]; ok {
		t.Stop()
		delete(m.timers, name)
	}
	m.mu.Unlock()

	st, err := m.opts.Client.Inspect(ctx, name)
	if err != nil {
		m.rearmIfIdle(name)
		return fmt.Errorf("box: inspecting %s: %w", name, err)
	}
	if st == StateStopped {
		// Its agents are already dead (docker exec does not survive the
		// container), and its name blocks a fresh `docker run`.
		m.unverify(name)
		if err := m.opts.Client.Stop(ctx, name); err != nil {
			m.rearmIfIdle(name)
			return fmt.Errorf("box: removing stopped %s: %w", name, err)
		}
		st = StateMissing
	}
	if st == StateRunning && m.opts.Verify != nil && !m.isVerified(name) {
		// Found, not created, by this process: adopted after a restart, or left
		// running by a failed removal. Never trust it unchecked.
		if err := m.opts.Verify(ctx, name); err != nil {
			return m.refuseUnverified(ctx, name, err)
		}
		m.markVerified(name)
	}
	if st == StateMissing {
		for _, p := range append([]string{cwd, m.stateDir(name)}, mountSources(extraRO)...) {
			if reason := MountUnsafe(p); reason != "" {
				m.rearmIfIdle(name)
				return fmt.Errorf("box: mount source %q: %s", p, reason)
			}
		}
		if _, err := m.ensureStateDir(name, extraRO); err != nil {
			m.rearmIfIdle(name)
			return err
		}
		if _, err := m.opts.Client.Run(ctx, m.runArgs(name, cwd, extraRO)); err != nil {
			// The idle timer was cancelled above. Without re-arming, a box that
			// survives a failed spawn would never be stopped by anyone.
			m.rearmIfIdle(name)
			return fmt.Errorf("box: starting %s: %w", name, err)
		}
		if m.opts.Verify != nil {
			if err := m.opts.Verify(ctx, name); err != nil {
				return m.refuseUnverified(ctx, name, err)
			}
			m.markVerified(name)
		}
	}
	// Count the session only once the box is genuinely usable, so a failed spawn
	// cannot leak a reference that keeps the box alive forever.
	m.acquire(name)
	m.touchMarker(name)
	return nil
}

// refuseUnverified handles a box that failed its containment check: it is
// removed so nothing reuses it, and the spawn is refused. If the removal fails
// the box may still be running; it is not in verified, so the next ensure
// verifies it again and removes it rather than trusting Inspect.
func (m *Manager) refuseUnverified(ctx context.Context, name string, verr error) error {
	m.unverify(name)
	if !errors.Is(verr, ErrBoxUnavailable) {
		verr = fmt.Errorf("%w: %v", ErrBoxUnavailable, verr)
	}
	if stopErr := m.opts.Client.Stop(ctx, name); stopErr != nil {
		verr = fmt.Errorf("%w (box %s could not be removed: %v; it is not reused and is checked again on the next spawn)",
			verr, name, stopErr)
	}
	m.rearmIfIdle(name)
	return verr
}

func (m *Manager) isVerified(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.verified[name]
}

func (m *Manager) markVerified(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.verified[name] = true
}

// unverify forgets that a box passed Verify. Every path that stops or removes a
// box calls it first, so a re-created container is verified again.
func (m *Manager) unverify(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.verified, name)
}

// mountSources lists every path of the extra mounts that docker will parse.
func mountSources(ms []Mount) []string {
	out := make([]string, 0, 2*len(ms))
	for _, mt := range ms {
		out = append(out, mt.Host, mt.Box)
	}
	return out
}

// rearmIfIdle restores the idle timer after a failed ensure, but only when no
// session is live — a live session must never be handed a dying box.
func (m *Manager) rearmIfIdle(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active[name] > 0 {
		return
	}
	if t, ok := m.timers[name]; ok {
		t.Stop()
	}
	m.timers[name] = time.AfterFunc(m.opts.IdleTTL, func() { m.stopIfIdle(name) })
}

// KillSession terminates a session's process INSIDE the box. Signals do not
// cross `docker exec`, so killing the host-side client would leave the agent
// running — with box reuse, a "killed" runaway agent would keep going.
func (m *Manager) KillSession(ctx context.Context, name, sessionID string) error {
	if name == "" {
		return nil
	}
	pf := pidFile(sessionID)
	// TERM the recorded pid and any child it left behind, then remove the
	// pidfile. Missing pid or already-dead process is success, not an error:
	// this runs on the teardown path and must be idempotent.
	script := `p=$(cat "$1" 2>/dev/null) || exit 0; [ -n "$p" ] || exit 0; ` +
		`kill -TERM "$p" 2>/dev/null; sleep 1; kill -KILL "$p" 2>/dev/null; rm -f "$1"; exit 0`
	_, err := m.opts.Client.Run(ctx, []string{"exec", name, "sh", "-c", script, "sh", pf})
	return err
}

func (m *Manager) acquire(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active[name]++
}

// Release records that a session for this workspace ended. On the last one it
// arms the idle timer; the box outlives its sessions by IdleTTL so returning to
// a project does not pay start-up again.
func (m *Manager) ReleaseBox(name string) {
	if name == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active[name] > 0 {
		m.active[name]--
	}
	if m.active[name] > 0 {
		return
	}
	delete(m.active, name)
	if t, ok := m.timers[name]; ok {
		t.Stop()
	}
	m.timers[name] = time.AfterFunc(m.opts.IdleTTL, func() { m.stopIfIdle(name) })
}

// stopIfIdle re-checks under the lock, so a session started during the idle
// window keeps the box alive even if the timer had already fired.
func (m *Manager) stopIfIdle(name string) {
	// Same lock ensure() holds, so a spawn can never exec into a container this
	// is removing: the two operations are strictly ordered.
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	if m.active[name] > 0 { // a session started during the idle window
		m.mu.Unlock()
		return
	}
	delete(m.timers, name)
	m.mu.Unlock()
	m.unverify(name)
	_ = m.opts.Client.Stop(context.Background(), name)
}

// AdoptBox re-registers a box whose session survived a daemon restart, so the
// following Reconcile does not reap it out from under a live agent. Takes the
// stored name, never a path: it must not be able to fail, because the only
// alternative to adopting is destroying a live session's container.
//
// Callers MUST adopt every surviving boxed session before calling Reconcile.
func (m *Manager) AdoptBox(name string) {
	if name == "" {
		return
	}
	m.acquire(name)
}

// Reconcile adopts this instance's boxes after a daemon restart and stops the
// ones with no live sessions. This is the orphan guard: leaked containers would
// be the 2.0 of the orphaned `abduco -a` clients that pegged a core, so it is
// tested rather than trusted.
//
// The whole pass holds opMu, as Sweep does: Prepare can call it while another
// spawn's ensure is verifying a box it just created, and that box has no
// session counted yet. A failed removal does not stop the pass; every orphan is
// tried and the failures are returned together.
func (m *Manager) Reconcile(ctx context.Context) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	names, err := m.opts.Client.ListByLabel(ctx, LabelInstance+"="+m.opts.Instance)
	if err != nil {
		return fmt.Errorf("box: listing boxes: %w", err)
	}
	var errs []error
	for _, name := range names {
		m.mu.Lock()
		live := m.active[name]
		m.mu.Unlock()
		if live > 0 {
			continue
		}
		m.unverify(name)
		if err := m.opts.Client.Stop(ctx, name); err != nil {
			errs = append(errs, fmt.Errorf("box: reaping orphan %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// SweepAction is one thing the sweep did, with the reason it logs.
type SweepAction struct{ Box, Reason string }

// SweepReport is what one sweep did. Skipped means the daemon was unreachable,
// so nothing was looked at.
type SweepReport struct {
	Skipped            bool
	Removed, Corrected []SweepAction
}

// Sweep reconciles running boxes with the session registry. Boot-time Reconcile
// covers a daemon restart; this covers everything after it: a docker or VM
// restart leaves stopped boxes, and a missed release leaves a counter that keeps
// a box alive forever. live is the registry's per-box count of sessions that
// have not exited. The whole pass holds opMu, so it never races ensure or the
// idle stop, and the listing it acts on cannot go stale mid-pass.
func (m *Manager) Sweep(ctx context.Context, live map[string]int) (SweepReport, error) {
	var rep SweepReport
	if m.opts.Reachable != nil && !m.opts.Reachable(ctx) {
		rep.Skipped = true
		return rep, nil
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()
	boxes, err := m.opts.Client.ListBoxes(ctx, LabelInstance+"="+m.opts.Instance)
	if err != nil {
		return rep, fmt.Errorf("box: sweep listing: %w", err)
	}
	var errs []error
	nextDrift := map[string]bool{}
	for _, b := range boxes {
		m.mu.Lock()
		count := m.active[b.Name]
		_, armed := m.timers[b.Name]
		drifted := m.drift[b.Name]
		m.mu.Unlock()
		switch {
		case b.State != "running":
			if err := m.remove(ctx, b.Name); err != nil {
				errs = append(errs, err)
				continue
			}
			rep.Removed = append(rep.Removed, SweepAction{b.Name, "not running (" + b.State + ")"})
		case live[b.Name] == 0 && count == 0 && !armed:
			if err := m.remove(ctx, b.Name); err != nil {
				errs = append(errs, err)
				continue
			}
			rep.Removed = append(rep.Removed, SweepAction{b.Name, "running with no session and no idle timer"})
		case live[b.Name] == 0 && count > 0:
			if !drifted {
				nextDrift[b.Name] = true
				continue
			}
			m.mu.Lock()
			delete(m.active, b.Name)
			if t, ok := m.timers[b.Name]; ok {
				t.Stop()
			}
			name := b.Name
			m.timers[name] = time.AfterFunc(m.opts.IdleTTL, func() { m.stopIfIdle(name) })
			m.mu.Unlock()
			rep.Corrected = append(rep.Corrected, SweepAction{b.Name,
				fmt.Sprintf("counter said %d live session(s), the registry says 0 on two sweeps; idle timer armed", count)})
		}
	}
	m.mu.Lock()
	m.drift = nextDrift
	m.mu.Unlock()
	return rep, errors.Join(errs...)
}

// remove deletes a box's container. It leaves the session counter alone, like
// ensure's stopped-box path: sessions that are still registered will release
// their count later, and a count dropped here would underflow against the
// container a new spawn re-creates under the same name. Callers hold opMu.
func (m *Manager) remove(ctx context.Context, name string) error {
	m.unverify(name)
	if err := m.opts.Client.Stop(ctx, name); err != nil {
		return fmt.Errorf("box: sweep removing %s: %w", name, err)
	}
	return nil
}
