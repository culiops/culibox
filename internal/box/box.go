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
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Container labels. Instance keeps the DEV and PROD daemons from adopting or
// reaping each other's boxes.
const (
	LabelWorkspace = "io.culiops.agent-console.workspace"
	LabelInstance  = "io.culiops.agent-console.instance"
	namePrefix     = "ac-box-"
	statePrefix    = "ac-state-"

	runtimeRunc   = "runc"
	runtimeSysbox = "sysbox-runc"

	// boxHome is the image's home for the unprivileged agent user. The state
	// volume mounts at <boxHome>/.claude so the agent keeps history and resume
	// data across spawns.
	boxHome = "/home/agent"
)

// Client is the narrow slice of the docker CLI this package needs. A fake drives
// every unit test, so none of them requires a running daemon.
type Client interface {
	Run(ctx context.Context, args []string) (string, error)
	Inspect(ctx context.Context, name string) (running bool, err error)
	ListByLabel(ctx context.Context, label string) ([]string, error)
	Stop(ctx context.Context, name string) error
	HasRuntime(ctx context.Context, name string) bool
}

// Options configures a Manager.
type Options struct {
	DockerBin string // absolute path; resolved by the caller at startup
	Image     string
	Instance  string // env label (DEV/PROD)
	IdleTTL   time.Duration
	Client    Client
}

// Manager tracks one box per workspace and keeps their lifecycle honest.
type Manager struct {
	opts    Options
	runtime string

	mu     sync.Mutex
	active map[string]int // box name → live session count
	timers map[string]*time.Timer
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
	m := &Manager{
		opts:    opts,
		runtime: runtimeRunc,
		active:  map[string]int{},
		timers:  map[string]*time.Timer{},
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

// stateVolume names the per-box volume holding the agent's ~/.claude. TrimPrefix
// rather than a slice: a name shorter than the prefix must not panic.
func stateVolume(boxName string) string {
	return statePrefix + strings.TrimPrefix(boxName, namePrefix)
}

// Wrap ensures the workspace's box is running and returns the argv/env to
// execute. The returned env is the caller's env unchanged: it stays on the
// docker CLIENT process so `-e NAME` can forward values without ever putting
// them in argv, which is world-readable through `ps` on the host.
func (m *Manager) Wrap(ctx context.Context, cwd string, argv, env []string) ([]string, []string, error) {
	name, err := m.boxName(cwd)
	if err != nil {
		return nil, nil, err
	}
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return nil, nil, fmt.Errorf("box: resolving workspace %q: %w", cwd, err)
	}
	if err := m.ensure(ctx, name, real); err != nil {
		return nil, nil, err
	}
	m.acquire(name)
	return m.execArgv(name, real, argv, env), env, nil
}

// ensure starts the box if it is not already running, cancelling any pending
// idle stop first so a spawn inside the idle window never races the timer.
func (m *Manager) ensure(ctx context.Context, name, cwd string) error {
	m.mu.Lock()
	if t, ok := m.timers[name]; ok {
		t.Stop()
		delete(m.timers, name)
	}
	m.mu.Unlock()

	running, err := m.opts.Client.Inspect(ctx, name)
	if err != nil {
		return fmt.Errorf("box: inspecting %s: %w", name, err)
	}
	if running {
		return nil
	}
	if _, err := m.opts.Client.Run(ctx, m.runArgs(name, cwd)); err != nil {
		return fmt.Errorf("box: starting %s: %w", name, err)
	}
	return nil
}

func (m *Manager) acquire(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active[name]++
}

// Release records that a session for this workspace ended. On the last one it
// arms the idle timer; the box outlives its sessions by IdleTTL so returning to
// a project does not pay start-up again.
func (m *Manager) Release(cwd string) {
	name, err := m.boxName(cwd)
	if err != nil {
		return // an unresolvable path never produced a box
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
	m.mu.Lock()
	if m.active[name] > 0 {
		m.mu.Unlock()
		return
	}
	delete(m.timers, name)
	m.mu.Unlock()
	_ = m.opts.Client.Stop(context.Background(), name)
}

// Reconcile adopts this instance's boxes after a daemon restart and stops the
// ones with no live sessions. This is the orphan guard: leaked containers would
// be the 2.0 of the orphaned `abduco -a` clients that pegged a core, so it is
// tested rather than trusted.
func (m *Manager) Reconcile(ctx context.Context) error {
	names, err := m.opts.Client.ListByLabel(ctx, LabelInstance+"="+m.opts.Instance)
	if err != nil {
		return fmt.Errorf("box: listing boxes: %w", err)
	}
	for _, name := range names {
		m.mu.Lock()
		live := m.active[name]
		m.mu.Unlock()
		if live > 0 {
			continue
		}
		if err := m.opts.Client.Stop(ctx, name); err != nil {
			return fmt.Errorf("box: reaping orphan %s: %w", name, err)
		}
	}
	return nil
}
