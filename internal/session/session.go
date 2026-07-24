// Package session is the daemon-owned source of truth for agent sessions.
// Each session owns one child process on its own PTY for the process's whole
// lifetime. A pump goroutine drains the PTY into a ring buffer (never blocking
// on any viewer); a per-subscriber delivery goroutine tails the ring to a
// WebSocket (may block on a slow viewer, harmlessly). A single reaper owns
// cmd.Wait() and closing done. No tmux, no external multiplexer.
package session

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

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

// Info is a read-only snapshot for listing.
type Info struct {
	ID      string    `json:"id"`
	Agent   string    `json:"agent"`
	Cwd     string    `json:"cwd,omitempty"`
	Created time.Time `json:"created"`
	Exited  bool      `json:"exited"`
	Status  string    `json:"status"`
}

// Session owns one agent child process and its PTY.
type Session struct {
	ID      string
	Agent   string
	Cwd     string
	Created time.Time

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
	exitAt       time.Time
	pumpFinished bool // pump drained the PTY fully (ring is complete)
	removed      chan struct{}
	removedOnce  sync.Once

	backend Backend       // nil for an ephemeral (daemon-owned-PTY) session
	sub     *subscription // current subscriber (at most one)
}

type subscription struct {
	out      chan []byte
	stop     chan struct{}
	stopOnce sync.Once
	stopped  bool // guarded by Session.mu (for the cond predicate)
}

func newSession(id, agent, cwd string, cmd *exec.Cmd, ptmx *os.File, ringCap int, backend Backend) *Session {
	s := &Session{
		ID: id, Agent: agent, Cwd: cwd, Created: time.Now(),
		cmd: cmd, ptmx: ptmx,
		rb:      newRing(ringCap),
		done:    make(chan struct{}),
		removed: make(chan struct{}),
		rows:    24, cols: 80,
		status:  statusLive,
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
	if !detached {
		s.finalize()
	}
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
func (s *Session) Subscribe() (<-chan []byte, func()) {
	s.mu.Lock()
	if s.sub != nil {
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
	return sub.out, cancel
}

// deliver: ring -> out for one subscription. Blocks on a slow WS write (only
// this viewer lags); if the cursor is evicted it force-resyncs with a terminal
// reset so the client repaints rather than showing a silent gap.
func (s *Session) deliver(sub *subscription, cursor int64) {
	defer close(sub.out)
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
	return Info{ID: s.ID, Agent: s.Agent, Cwd: s.Cwd, Created: s.Created, Exited: s.exited, Status: s.status}
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
	backend   Backend // nil = ephemeral (daemon-owned PTY); set = abduco persistence
	counters  map[string]int
	ringCap   int
	retention time.Duration
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

	m.mu.Lock()
	m.counters[spec.Agent]++
	id := fmt.Sprintf("%s-%d", spec.Agent, m.counters[spec.Agent])
	m.mu.Unlock()

	cmd, ptmx, err := m.startProcess(id, spec, argv, env)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	sess := newSession(id, spec.Agent, spec.Cwd, cmd, ptmx, m.ringCap, m.backend)
	m.sessions[id] = sess
	m.mu.Unlock()

	m.watchRetention(id, sess)
	return sess, nil
}

// startProcess creates the OS process + PTY for a session. With a backend the
// agent runs under abduco (survives restart) and the daemon holds an `abduco -a`
// attach PTY; without one the daemon owns the agent PTY directly (ephemeral).
func (m *Manager) startProcess(id string, spec launcher.SpawnSpec, argv, env []string) (*exec.Cmd, *os.File, error) {
	if m.backend != nil {
		if err := m.backend.Create(id, spec.Cwd, argv, env); err != nil {
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
		<-sess.Done()
		select {
		case <-time.After(m.retention):
		case <-sess.removed:
			return
		}
		_ = sess.ptmx.Close()
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
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
	m.mu.Unlock()
	s.finalize()                                  // close done uniformly (live/exited/detached)
	s.removedOnce.Do(func() { close(s.removed) }) // let the retention goroutine exit now
	return nil
}
