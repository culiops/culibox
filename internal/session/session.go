// Package session is the daemon-owned source of truth for agent sessions.
// Each session owns one child process on its own PTY for the process's whole
// lifetime. A pump goroutine drains the PTY into a ring buffer (never blocking
// on any viewer); a per-subscriber delivery goroutine tails the ring to a
// WebSocket (may block on a slow viewer, harmlessly). A single reaper owns
// cmd.Wait() and closing done. No tmux, no external multiplexer.
package session

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/culiops/culiops-agent-console/internal/config"
	"github.com/culiops/culiops-agent-console/internal/launcher"
)

// Env composition (base policy, Claude-marker stripping, secret injection) lives
// in the launcher; this package consumes the composed (argv, env) verbatim.

// ErrNotFound is returned by Manager.Kill for an unknown id (so the API can map
// it to 404, incl. the TOCTOU where the retention pruner removes it mid-request).
var ErrNotFound = errors.New("session: not found")

// Session status values. A session is `live` while its agent runs and we are
// attached; `exited` once the agent is gone; `detached` when we lost our attach
// pipe but the agent (under abduco) is still alive — recoverable on next daemon
// restart. `detached` only occurs with a persistence Backend.
const (
	statusLive     = "live"
	statusExited   = "exited"
	statusDetached = "detached"
)

// Backend is an optional session-persistence layer (abduco). When a Manager has
// one, sessions run under it and survive a daemon restart; the daemon holds a
// replaceable attach process. When nil, the Manager owns the agent PTY directly
// (ephemeral: sessions die with the daemon) — used by the streaming-core tests.
type Backend interface {
	Create(id, cwd string, argv, env []string) error
	Attach(id string, rows, cols uint16) (*exec.Cmd, *os.File, error)
	Alive(id string) bool
	InnerAlive(id string) bool
	Kill(id string) error
	List() ([]string, error)
}

// Boxes is the optional containment layer. A nil Boxes means boxing is
// unavailable (no docker, or no image) and every spawn runs on the host tier.
// Kept as an interface here so the session package does not depend on the box
// package and stays unit-testable with a fake.
type Boxes interface {
	Wrap(ctx context.Context, cwd, sessionID string, argv, env []string) ([]string, []string, error)
	Adopt(cwd string) error
	Release(cwd string)
	KillSession(ctx context.Context, cwd, sessionID string) error
	Runtime() string
}

// RuntimeResolver is implemented by launchers that understand tiers. It is an
// optional interface so existing fake builders keep compiling.
type RuntimeResolver interface {
	RuntimeFor(spec launcher.SpawnSpec) (string, error)
}

// Info is a read-only snapshot for listing.
type Info struct {
	ID      string    `json:"id"`
	Agent   string    `json:"agent"`
	Cwd     string    `json:"cwd,omitempty"`
	Created time.Time `json:"created"`
	Exited  bool      `json:"exited"`
	Status  string    `json:"status"`
	Name    string    `json:"name"`              // mutable display label (id is the stable key)
	Runtime string    `json:"runtime,omitempty"` // box | host
}

// Session owns one agent child process and its PTY.
type Session struct {
	ID      string
	Agent   string
	Cwd     string
	Created time.Time
	Runtime string // box | host; immutable for the session's lifetime

	cmd  *exec.Cmd
	ptmx *os.File

	mu   sync.Mutex
	cond *sync.Cond
	rb   *ring
	rows uint16
	cols uint16

	done         chan struct{}
	closeOnce    sync.Once
	exited       bool
	status       string // statusLive | statusExited | statusDetached (guarded by mu)
	name         string // mutable display label (guarded by mu)
	exitAt       time.Time
	pumpFinished bool // pump drained the PTY fully (ring is complete)
	removed      chan struct{}
	removedOnce  sync.Once
	detachedCh   chan struct{} // closed when the session goes detached (agent alive, pipe lost)
	detachedOnce sync.Once

	backend Backend       // nil for an ephemeral (daemon-owned-PTY) session
	sub     *subscription // current subscriber (at most one)
}

type subscription struct {
	out      chan []byte
	stop     chan struct{}
	stopOnce sync.Once
	stopped  bool // guarded by Session.mu (for the cond predicate)
	// superseded distinguishes "a newer Subscribe evicted you" from every other
	// reason this channel can close (the caller cancelled, the session exited).
	// Only the eviction branch in Subscribe sets it. Guarded by Session.mu.
	superseded bool
}

func newSession(id, agent, name, cwd string, created time.Time, cmd *exec.Cmd, ptmx *os.File, ringCap int, backend Backend) *Session {
	s := &Session{
		ID: id, Agent: agent, Cwd: cwd, Created: created,
		cmd: cmd, ptmx: ptmx,
		rb:         newRing(ringCap),
		done:       make(chan struct{}),
		removed:    make(chan struct{}),
		detachedCh: make(chan struct{}),
		rows:       24, cols: 80,
		status:  statusLive,
		name:    name,
		backend: backend,
	}
	s.cond = sync.NewCond(&s.mu)
	go s.pump()
	go s.reap()
	return s
}

// pump: PTY -> ring. The sole appender. Never blocks on a subscriber, so the
// child can never stall on a full PTY.
func (s *Session) pump() {
	// On exit, mark the ring complete so delivery goroutines flush the child's
	// final output before closing — not merely when the process is reaped
	// (cmd.Wait can return before the last PTY bytes are read).
	defer func() {
		s.mu.Lock()
		s.pumpFinished = true
		s.cond.Broadcast()
		s.mu.Unlock()
	}()
	buf := make([]byte, 4096)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.rb.append(buf[:n])
			s.cond.Broadcast()
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// reap: waits on the process (the agent for an ephemeral session, or the abduco
// attach process for a persistent one) and classifies the exit. For a persistent
// session the attach dying does NOT mean the agent died: if the agent (inner pid)
// is still alive we mark the session `detached` and do NOT finalize — it survives
// for the next-boot reconcile. Only a genuine agent exit finalizes.
func (s *Session) reap() {
	_ = s.cmd.Wait()
	detached := s.backend != nil && s.backend.InnerAlive(s.ID)
	s.mu.Lock()
	if detached {
		s.status = statusDetached
	} else {
		s.status = statusExited
		s.exited = true
		s.exitAt = time.Now()
	}
	s.cond.Broadcast()
	s.mu.Unlock()
	if detached {
		// The attach pipe is dead but the agent lives on. Reclaim the (EOF'd)
		// attach PTY fd now and release the retention goroutine — v1 recovers a
		// detached session on the next daemon restart, not in-lifetime.
		_ = s.ptmx.Close()
		s.detachedOnce.Do(func() { close(s.detachedCh) })
		return
	}
	s.finalize()
}

// finalize marks this Session torn down by closing `done` exactly once. `done`
// means "being finalized" (agent exited OR killed) — NOT merely "agent exited" —
// so Kill and the retention pruner can both rely on it even for a detached
// session that reap left open.
func (s *Session) finalize() {
	s.closeOnce.Do(func() { close(s.done) })
}

// Subscribe returns a channel of terminal output. At most one subscriber: a new
// Subscribe cancels the previous (its channel closes). The returned channel is
// closed by its own delivery goroutine — never by anyone else — so there is no
// send-on-closed hazard even across a subscriber swap (each has its own channel).
//
// The third return reports whether THIS subscription was evicted by a later
// Subscribe. A closed channel alone cannot say that, and the difference matters
// to a viewer: one that was taken over must stop reconnecting (or the two
// viewers evict each other forever), while one whose session ended or whose
// server died must keep reconnecting.
func (s *Session) Subscribe() (<-chan []byte, func(), func() bool) {
	s.mu.Lock()
	if s.sub != nil {
		s.sub.superseded = true
		s.sub.stopped = true
		s.sub.stopOnce.Do(func() { close(s.sub.stop) })
	}
	sub := &subscription{out: make(chan []byte, 64), stop: make(chan struct{})}
	s.sub = sub
	cursor := s.rb.start // atomic snapshot+register under the lock
	s.cond.Broadcast()   // wake any prior delivery so it observes stopped
	s.mu.Unlock()

	go s.deliver(sub, cursor)

	cancel := func() {
		s.mu.Lock()
		sub.stopped = true
		sub.stopOnce.Do(func() { close(sub.stop) })
		s.cond.Broadcast()
		s.mu.Unlock()
	}
	superseded := func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return sub.superseded
	}
	return sub.out, cancel, superseded
}

// deliver: ring -> out for one subscription. Blocks on a slow WS write (only
// this viewer lags); if the cursor is evicted it force-resyncs with a terminal
// reset so the client repaints rather than showing a silent gap.
func (s *Session) deliver(sub *subscription, cursor int64) {
	// Deregister before closing. A finished subscription must stop being the
	// session's current one, or a later Subscribe would retro-mark it superseded
	// — telling a viewer whose session merely ended that another tab took it,
	// a state that deliberately stops reconnecting and so never self-corrects.
	// The identity check matters: an eviction has already installed its
	// replacement, and this must not null that out.
	defer func() {
		s.mu.Lock()
		if s.sub == sub {
			s.sub = nil
		}
		s.mu.Unlock()
		close(sub.out)
	}()
	for {
		s.mu.Lock()
		for cursor >= s.rb.end() && !s.pumpFinished && !sub.stopped {
			s.cond.Wait()
		}
		if sub.stopped {
			s.mu.Unlock()
			return
		}
		var chunk []byte
		if cursor < s.rb.start {
			cursor = s.rb.start
			chunk = append([]byte("\x1bc"), s.rb.bytesFrom(cursor)...) // RIS reset + resync
			cursor = s.rb.end()
		} else if cursor < s.rb.end() {
			chunk = s.rb.bytesFrom(cursor)
			cursor = s.rb.end()
		}
		// Terminate only once the pump has fully drained the PTY (ring complete)
		// AND we've sent everything — so a live viewer never misses final output.
		pumpDone := s.pumpFinished
		end := s.rb.end()
		s.mu.Unlock()

		if len(chunk) > 0 {
			select {
			case sub.out <- chunk:
			case <-sub.stop:
				return
			}
		}
		if pumpDone && cursor >= end {
			return
		}
	}
}

// Write sends input to the PTY.
func (s *Session) Write(p []byte) (int, error) { return s.ptmx.Write(p) }

// Resize resizes the PTY and records the size for ForceRedraw.
func (s *Session) Resize(rows, cols uint16) error {
	s.mu.Lock()
	s.rows, s.cols = rows, cols
	s.mu.Unlock()
	return pty.Setsize(s.ptmx, &pty.Winsize{Rows: rows, Cols: cols})
}

// ForceRedraw nudges the size (±1 col then back) to make a full-screen TUI
// repaint over a best-effort replay on attach.
func (s *Session) ForceRedraw() {
	s.mu.Lock()
	r, c := s.rows, s.cols
	s.mu.Unlock()
	if c == 0 {
		return
	}
	_ = pty.Setsize(s.ptmx, &pty.Winsize{Rows: r, Cols: c + 1})
	_ = pty.Setsize(s.ptmx, &pty.Winsize{Rows: r, Cols: c})
}

// Done is closed when the child exits.
func (s *Session) Done() <-chan struct{} { return s.done }

// Exited reports whether the child has exited.
func (s *Session) Exited() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exited
}

// Info returns a listing snapshot.
func (s *Session) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Info{ID: s.ID, Agent: s.Agent, Cwd: s.Cwd, Created: s.Created, Exited: s.exited, Status: s.status, Name: s.name, Runtime: s.Runtime}
}

// SetName updates the mutable display label.
func (s *Session) SetName(name string) {
	s.mu.Lock()
	s.name = name
	s.mu.Unlock()
}

// signal sends sig to the child's whole process group (negative pid). creack/pty
// forces Setsid, so the child leads its own group — this reaches its children
// (e.g. claude's MCP subprocesses) without ever touching the daemon.
func (s *Session) signal(sig syscall.Signal) error {
	if s.cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-s.cmd.Process.Pid, sig)
}

// Manager is the in-memory session registry.
type Manager struct {
	mu        sync.Mutex
	sessions  map[string]*Session
	launcher  launcher.Builder
	backend   Backend  // nil = ephemeral (daemon-owned PTY); set = abduco persistence
	sidecar   *sidecar // nil = no metadata persistence
	counters  map[string]int
	ringCap   int
	retention time.Duration
	boxes     Boxes // nil = containment unavailable; every spawn is host tier
}

// SetBoxes installs the containment layer. Call once at boot, before Reconcile.
func (m *Manager) SetBoxes(b Boxes) {
	m.mu.Lock()
	m.boxes = b
	m.mu.Unlock()
}

// runtimeFor resolves the tier for a spawn. Without a tier-aware launcher or a
// box layer the answer is host — containment is never assumed, only granted.
func (m *Manager) runtimeFor(spec launcher.SpawnSpec) string {
	m.mu.Lock()
	boxes := m.boxes
	m.mu.Unlock()
	if boxes == nil {
		return config.RuntimeHost
	}
	rr, ok := m.launcher.(RuntimeResolver)
	if !ok {
		return config.RuntimeHost
	}
	rt, err := rr.RuntimeFor(spec)
	if err != nil || rt == "" {
		return config.RuntimeHost
	}
	return rt
}

// SetSidecarPath enables metadata persistence + restart reconcile by loading the
// sidecar at path (a missing file is an empty sidecar). Call once at boot before
// Reconcile/serving. Without it, sessions still run but are not recoverable.
func (m *Manager) SetSidecarPath(path string) error {
	sc, err := openSidecar(path)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.sidecar = sc
	m.mu.Unlock()
	return nil
}

// NewManager builds a registry. backend may be nil (ephemeral sessions).
// ringCap<=0 defaults 128 KiB; retention<=0 defaults 5m.
func NewManager(l launcher.Builder, backend Backend, ringCap int, retention time.Duration) *Manager {
	if ringCap <= 0 {
		ringCap = 128 * 1024
	}
	if retention <= 0 {
		retention = 5 * time.Minute
	}
	return &Manager{
		sessions:  map[string]*Session{},
		launcher:  l,
		backend:   backend,
		counters:  map[string]int{},
		ringCap:   ringCap,
		retention: retention,
	}
}

// Spawn builds the agent command and starts a session — under the persistence
// backend (abduco) if present, else a daemon-owned PTY directly.
func (m *Manager) Spawn(spec launcher.SpawnSpec) (*Session, error) {
	argv, env, err := m.launcher.Build(spec)
	if err != nil {
		return nil, err
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("session: launcher produced empty argv")
	}
	// Fail CLOSED: a nil cmd.Env makes exec inherit the daemon's FULL environment
	// (every secret). Force a non-nil (possibly empty) slice so "deny by default"
	// can never silently become "inherit everything".
	if env == nil {
		env = []string{}
	}

	// Containment is applied here, between the launcher and the process start,
	// so abduco persistence and the whole session lifecycle stay untouched.
	runtime := m.runtimeFor(spec)
	m.mu.Lock()
	id := m.nextIDLocked(spec.Agent)
	m.mu.Unlock()

	if runtime == config.RuntimeBox {
		m.mu.Lock()
		boxes := m.boxes
		m.mu.Unlock()
		wrapped, wrappedEnv, err := boxes.Wrap(context.Background(), spec.Cwd, id, argv, env)
		if err != nil {
			// Name the way out: a silent failure here is what makes people
			// disable containment entirely.
			return nil, fmt.Errorf("session: starting box for %q: %w — spawn with runtime %q to run on the host instead",
				spec.Cwd, err, config.RuntimeHost)
		}
		argv, env = wrapped, wrappedEnv
	}

	cmd, ptmx, err := m.startProcess(id, spec, argv, env)
	if err != nil {
		if runtime == config.RuntimeBox {
			m.releaseBox(spec.Cwd) // never leak a box for a session that failed to start
		}
		return nil, err
	}

	created := time.Now()
	name := deriveName(spec.Name, spec.Cwd, id)
	m.mu.Lock()
	sess := newSession(id, spec.Agent, name, spec.Cwd, created, cmd, ptmx, m.ringCap, m.backend)
	sess.Runtime = runtime
	m.sessions[id] = sess
	sc := m.sidecar
	m.mu.Unlock()

	if sc != nil {
		sc.put(id, sessionMeta{Agent: spec.Agent, Name: name, Cwd: spec.Cwd, Created: created, Runtime: runtime})
	}
	m.watchRetention(id, sess)
	return sess, nil
}

// releaseBox tells the box layer one session for this workspace is gone.
func (m *Manager) releaseBox(cwd string) {
	m.mu.Lock()
	boxes := m.boxes
	m.mu.Unlock()
	if boxes != nil {
		boxes.Release(cwd)
	}
}

// startProcess creates the OS process + PTY for a session. With a backend the
// agent runs under abduco (survives restart) and the daemon holds an `abduco -a`
// attach PTY; without one the daemon owns the agent PTY directly (ephemeral).
func (m *Manager) startProcess(id string, spec launcher.SpawnSpec, argv, env []string) (*exec.Cmd, *os.File, error) {
	if m.backend != nil {
		if err := m.backend.Create(id, spec.Cwd, argv, env); err != nil {
			// Create may fail after abduco created the session (e.g. an agent that
			// exits within the readiness poll). Tear down so we never leak it.
			_ = m.backend.Kill(id)
			return nil, nil, err
		}
		cmd, ptmx, err := m.backend.Attach(id, 24, 80)
		if err != nil {
			_ = m.backend.Kill(id) // tear down a session the user never saw start
			return nil, nil, err
		}
		return cmd, ptmx, nil
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if spec.Cwd != "" {
		cmd.Dir = spec.Cwd
	}
	cmd.Env = env
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		return nil, nil, fmt.Errorf("session: start pty: %w", err)
	}
	return cmd, ptmx, nil
}

// watchRetention retains an exited session (so the user can read why it died)
// then prunes it. An explicit Kill closes `removed` to skip the wait. `Done()`
// fires on finalize (agent exit OR kill), so a detached session never leaks here.
func (m *Manager) watchRetention(id string, sess *Session) {
	go func() {
		select {
		case <-sess.Done(): // finalized (agent exited or killed) → retain then prune
			// Release only on finalize. A DETACHED session's agent is still
			// alive inside its box, so releasing there would arm the idle timer
			// and stop the box under a session we intend to recover.
			if sess.Runtime == config.RuntimeBox {
				m.releaseBox(sess.Cwd)
			}
		case <-sess.detachedCh: // agent alive, pipe lost → keep listed for restart recovery
			return
		}
		select {
		case <-time.After(m.retention):
		case <-sess.removed:
			return
		}
		_ = sess.ptmx.Close()
		m.mu.Lock()
		delete(m.sessions, id)
		sc := m.sidecar
		backend := m.backend
		m.mu.Unlock()
		if sc != nil {
			sc.remove(id) // a naturally-exited session is gone for good
		}
		if backend != nil {
			_ = backend.Kill(id) // idempotent: remove the pidfile + any lingering server/socket
		}
	}()
}

// List returns all sessions, oldest first.
func (m *Manager) List() []Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Info, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s.Info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// Get returns a live session by id.
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

// DetachAll releases every attach client the daemon owns, leaving the agents
// running under the backend. It returns how many it released.
//
// This exists because the daemon must not rely on the kernel to clean up after
// it. When the daemon exits, the PTY masters it holds close, and SIGHUP goes
// only to each terminal's FOREGROUND process group. An attach client that is not
// the foreground group at that instant gets no signal, keeps its now-hung-up
// slave as stdin, and abduco's select/read loop then spins at 100% CPU forever —
// observed in the wild as four orphans burning a core each for 11.5 hours.
//
// Without a backend there is nothing to detach FROM: cmd is the agent itself,
// and killing it here would destroy the very thing a restart is meant to
// preserve. So this is a no-op in that mode, by design.
func (m *Manager) DetachAll() int {
	if m.backend == nil {
		return 0
	}
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()

	n := 0
	for _, s := range sessions {
		if s.cmd == nil || s.cmd.Process == nil {
			continue
		}
		// SIGKILL, not SIGTERM: this runs on the way out with no time to wait,
		// and an attach client has no state worth flushing — the agent's state
		// lives in the backend, on the other side of the socket.
		if err := s.signal(syscall.SIGKILL); err == nil {
			n++
		}
	}
	return n
}

// Kill terminates the session and removes it immediately (a user-initiated kill
// needs no retention). With a backend, abduco.Kill tears down the agent group +
// server (TERM→KILL inside the adapter) and we finalize DIRECTLY — never waiting
// on `done`, so a `detached` session (which has no live reaper) cannot hang the
// call. Without one, we signal the process group and wait as before.
func (m *Manager) Kill(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	// Signals do not cross `docker exec`: killing the host-side client leaves the
	// agent running inside a box that other sessions keep alive. Reach the real
	// process first, then tear down the host side.
	if s.Runtime == config.RuntimeBox {
		m.mu.Lock()
		boxes := m.boxes
		m.mu.Unlock()
		if boxes != nil {
			if err := boxes.KillSession(context.Background(), s.Cwd, id); err != nil {
				log.Printf("session %s: killing agent inside its box: %v", id, err)
			}
		}
	}
	if m.backend != nil {
		_ = m.backend.Kill(id)
	} else {
		_ = s.signal(syscall.SIGTERM)
		select {
		case <-s.done:
		case <-time.After(3 * time.Second):
			_ = s.signal(syscall.SIGKILL)
			<-s.done
		}
	}
	_ = s.ptmx.Close()
	m.mu.Lock()
	delete(m.sessions, id)
	sc := m.sidecar
	m.mu.Unlock()
	if sc != nil {
		sc.remove(id)
	}
	s.finalize()                                  // close done uniformly (live/exited/detached)
	s.removedOnce.Do(func() { close(s.removed) }) // let the retention goroutine exit now
	return nil
}

// Reconcile rebuilds the registry from surviving abduco sessions after a daemon
// restart: it re-attaches every live session, drops sidecar entries whose agent
// died while the daemon was down, adopts sockets with no sidecar (agent from the
// id), and seeds the per-agent counters so a fresh spawn cannot collide with a
// recovered id. No-op without a backend.
func (m *Manager) Reconcile() error {
	if m.backend == nil {
		return nil
	}
	live, err := m.backend.List()
	if err != nil {
		return err
	}
	// abduco keeps a session's socket after the inner agent exits (until a client
	// consumes the exit status), so "listed" != "alive". Partition on InnerAlive:
	// genuinely-alive sockets are re-attached; exited-but-lingering ones are torn
	// down (unlink the socket + server) so they don't resurrect as live→exited.
	aliveSet := make(map[string]bool, len(live))
	var aliveIDs []string
	for _, id := range live {
		if m.backend.InnerAlive(id) {
			aliveSet[id] = true
			aliveIDs = append(aliveIDs, id)
		} else {
			_ = m.backend.Kill(id)
		}
	}

	m.mu.Lock()
	sc := m.sidecar
	m.mu.Unlock()

	var meta map[string]sessionMeta
	if sc != nil {
		meta = sc.all()
		// Drop sidecar entries with no genuinely-alive session behind them.
		for id := range meta {
			if !aliveSet[id] {
				sc.remove(id)
			}
		}
	}

	for _, id := range aliveIDs {
		md, ok := meta[id]
		if !ok { // orphan-adopt: recover the agent name from the id
			agent, _, parsed := parseID(id)
			if !parsed {
				agent = "unknown"
			}
			md = sessionMeta{Agent: agent, Created: time.Now()}
			if sc != nil {
				sc.put(id, md)
			}
		}
		if err := m.reattach(id, md); err != nil {
			// Leave the abduco session running; it can be recovered on a later
			// restart. Don't fail the whole reconcile for one bad re-attach.
			continue
		}
	}

	// Seed counters from EVERY id we saw (alive or lingering) so a fresh spawn
	// cannot collide with any recovered id.
	m.mu.Lock()
	for _, id := range live {
		if agent, n, ok := parseID(id); ok && n > m.counters[agent] {
			m.counters[agent] = n
		}
	}
	m.mu.Unlock()
	return nil
}

// reattach connects a fresh attach PTY to a surviving abduco session and rebuilds
// its Session, nudging a repaint (best-effort, no cross-restart scrollback).
func (m *Manager) reattach(id string, md sessionMeta) error {
	cmd, ptmx, err := m.backend.Attach(id, 24, 80)
	if err != nil {
		return err
	}
	created := md.Created
	if created.IsZero() {
		created = time.Now()
	}
	name := md.Name
	if name == "" { // pre-existing sidecar entries predate the Name field
		name = deriveName("", md.Cwd, id)
	}
	runtime := md.Runtime
	if runtime == "" { // sidecar entries written before containment existed
		runtime = config.RuntimeHost
	}
	m.mu.Lock()
	sess := newSession(id, md.Agent, name, md.Cwd, created, cmd, ptmx, m.ringCap, m.backend)
	sess.Runtime = runtime
	m.sessions[id] = sess
	boxes := m.boxes
	m.mu.Unlock()
	// Re-register the box BEFORE the caller reconciles boxes, or the orphan
	// reaper stops the container this session is running inside.
	if runtime == config.RuntimeBox && boxes != nil {
		if err := boxes.Adopt(md.Cwd); err != nil {
			log.Printf("session %s: adopting box for %q: %v", id, md.Cwd, err)
		}
	}
	sess.ForceRedraw()
	m.watchRetention(id, sess)
	return nil
}

// ErrInvalidName is returned by Rename for an empty name (safe to show the user).
var ErrInvalidName = errors.New("session: name must not be empty")

const maxNameLen = 64

// nextIDLocked returns the next free stable id "<agent>-<n>" (caller holds mu).
// The id is the immutable technical key (abduco session, pidfile, WS route); the
// user-facing name is a separate mutable label (see deriveName / Rename).
func (m *Manager) nextIDLocked(agent string) string {
	for {
		m.counters[agent]++
		id := fmt.Sprintf("%s-%d", agent, m.counters[agent])
		if _, exists := m.sessions[id]; exists {
			continue
		}
		if m.backend != nil && m.backend.Alive(id) {
			continue
		}
		return id
	}
}

// deriveName picks the initial display label: an explicit custom name, else the
// working dir's basename, else the id.
func deriveName(custom, cwd, id string) string {
	if n := strings.TrimSpace(custom); n != "" {
		return capName(n)
	}
	if cwd != "" {
		if b := filepath.Base(cwd); b != "" && b != "/" && b != "." && b != ".." {
			return capName(b)
		}
	}
	return id
}

// capName trims and caps to maxNameLen RUNES (not bytes) so a multibyte label
// (e.g. Vietnamese) is never sliced mid-rune into invalid UTF-8.
func capName(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxNameLen {
		s = string(r[:maxNameLen])
	}
	return s
}

// Rename updates a session's display label (not its id). Persisted to the sidecar
// so it survives a restart. Follows the release-before-act lock pattern: look up
// under m.mu, then touch the leaf locks (Session.mu, sidecar.mu) unlocked.
func (m *Manager) Rename(id, name string) error {
	name = capName(name)
	if name == "" {
		return ErrInvalidName
	}
	m.mu.Lock()
	s, ok := m.sessions[id]
	sc := m.sidecar
	m.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	s.SetName(name)
	if sc != nil {
		sc.rename(id, name)
	}
	return nil
}

// parseID splits "<agent>-<n>" on the LAST dash (agent names may contain dashes,
// e.g. "claude-code-3"); ok is false unless the suffix is a positive integer.
func parseID(id string) (agent string, n int, ok bool) {
	i := strings.LastIndex(id, "-")
	if i <= 0 || i == len(id)-1 {
		return "", 0, false
	}
	n, err := strconv.Atoi(id[i+1:])
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return id[:i], n, true
}
