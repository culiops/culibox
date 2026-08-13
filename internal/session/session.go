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

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/launcher"
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

// idleThreshold: a live session with no PTY output for this long reads as
// "waiting" (a proxy for "wants your input") rather than "working".
const idleThreshold = 10 * time.Second

// attachRepaintGrace: output produced within this window after a resize or a
// forced redraw is a repaint WE triggered by attaching a viewer, not the agent
// working — so it must not count toward activity. A SIGWINCH repaint finishes in
// well under this window; only a burst that both starts and ends inside it is
// hidden, which in practice is only the repaint.
const attachRepaintGrace = 1 * time.Second

// activityOf classifies a session for the UI. Pure: no locks, no clock — the
// caller passes `now`. Exit dominates the idle test because a just-exited
// session's lastActivity is stale. exitKnown gates crashed: for abduco/boxed
// sessions the reaped code is the attach process's, not the agent's, so it must
// not be trusted into a red "crashed".
func activityOf(exited, exitKnown bool, exitCode int, lastActivity, now time.Time, idle time.Duration) string {
	if exited {
		if exitKnown && exitCode != 0 {
			return "crashed"
		}
		return "done"
	}
	if now.Sub(lastActivity) < idle {
		return "working"
	}
	return "waiting"
}

// recordActivity reports whether output observed at `now` should count as agent
// activity: it does, unless `now` is still at or inside the muted window set by a
// control-action repaint (see muteActivity).
func recordActivity(now, mutedUntil time.Time) bool {
	return now.After(mutedUntil)
}

// noteOutput records PTY output as agent activity unless we are inside the
// post-control-action grace window. Caller holds s.mu.
func (s *Session) noteOutput(now time.Time) {
	if recordActivity(now, s.activityMutedUntil) {
		s.lastActivityAt = now
	}
}

// muteActivity suppresses activity attribution for the next attachRepaintGrace,
// so a repaint triggered by our own resize/forced-redraw is not read as work.
// Caller holds s.mu.
func (s *Session) muteActivity(now time.Time) {
	s.activityMutedUntil = now.Add(attachRepaintGrace)
}

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
	Wrap(ctx context.Context, cwd, sessionID string, argv, env []string, extraRO []box.Mount) (argvOut, envOut []string, boxName string, err error)
	AdoptBox(boxName string)
	ReleaseBox(boxName string)
	KillSession(ctx context.Context, boxName, sessionID string) error
	Runtime() string
}

// RuntimeResolver is implemented by launchers that understand tiers. It is an
// optional interface so existing fake builders keep compiling.
type RuntimeResolver interface {
	RuntimeFor(spec launcher.SpawnSpec) (string, error)
}

// BinResolver is implemented by launchers that can name an agent's binary, which
// is how a config-bundle layout is chosen. Optional, like RuntimeResolver.
type BinResolver interface {
	BinFor(spec launcher.SpawnSpec) (string, error)
}

// Bundles supplies the pinned agent-config bundle for a boxed session. Nil means
// no bundle: sessions still run, just without user config.
type Bundles interface {
	// For returns the read-only mounts and the extra argv flags a boxed agent
	// needs, plus the bundle hash for the log line. An empty bin match yields no
	// mounts and no flags.
	For(bin string) (mounts []box.Mount, flags []string, hash string, err error)
}

// Info is a read-only snapshot for listing.
type Info struct {
	ID      string    `json:"id"`
	Agent   string    `json:"agent"`
	Cwd     string    `json:"cwd,omitempty"`
	Created time.Time `json:"created"`
	Exited  bool      `json:"exited"`
	Status  string    `json:"status"`
	// Activity is a UI-facing classification: working|waiting|done|crashed|detached.
	Activity string `json:"activity"`
	Name     string `json:"name"`              // mutable display label (id is the stable key)
	Runtime  string `json:"runtime,omitempty"` // box | host
	// Attached reports that a viewer is subscribed right now. It says only THAT
	// someone is watching, never who: every client authenticates with the same
	// credentials, so the daemon has no identity to report and inventing one
	// would be a lie.
	Attached bool `json:"attached"`
	// Summary is the first line of the agent's last message, elided to
	// summaryMax runes. Empty for a non-claude session or one whose transcript
	// cannot be read — the client tells those apart via the /peek endpoint.
	Summary string `json:"summary,omitempty"`
}

// Session owns one agent child process and its PTY.
type Session struct {
	ID      string
	Agent   string
	Cwd     string
	Created time.Time
	Runtime string // box | host; immutable for the session's lifetime
	Box     string // container name for a boxed session; "" on the host tier
	Bin     string // resolved agent binary (for claude-only features like usage)

	// releaseOnce makes the box release idempotent. Two different paths finalize
	// a session (the retention goroutine, and Kill), and a DETACHED session is
	// abandoned by the retention goroutine entirely — so both must be able to
	// release without the risk of double-counting.
	releaseOnce sync.Once

	cmd  *exec.Cmd
	ptmx *os.File

	// trim removes abduco's attach preamble; nil for an ephemeral session,
	// which has no abduco in front of it. Only pump touches it.
	trim *preambleTrimmer

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

	lastActivityAt time.Time // last non-empty PTY output (guarded by mu)
	// activityMutedUntil: output before this instant is a control-action repaint
	// (resize/forced-redraw on attach), not agent work, so it must not count as
	// activity. Guarded by mu.
	activityMutedUntil time.Time
	exitCode           int  // agent exit code; meaningful only when exitCodeKnown
	exitCodeKnown      bool // true only for ephemeral sessions (cmd IS the agent)
	removed            chan struct{}
	removedOnce        sync.Once
	detachedCh         chan struct{} // closed when the session goes detached (agent alive, pipe lost)
	detachedOnce       sync.Once

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
		status:         statusLive,
		name:           name,
		backend:        backend,
		lastActivityAt: created,
	}
	if backend != nil {
		s.trim = &preambleTrimmer{}
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
		if s.trim != nil {
			if rest := s.trim.flush(); len(rest) > 0 {
				s.rb.append(rest)
			}
		}
		s.pumpFinished = true
		s.cond.Broadcast()
		s.mu.Unlock()
	}()
	buf := make([]byte, 4096)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if s.trim != nil {
				chunk = s.trim.filter(chunk)
			}
			if len(chunk) > 0 {
				s.mu.Lock()
				s.rb.append(chunk)
				s.noteOutput(time.Now())
				s.cond.Broadcast()
				s.mu.Unlock()
			}
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
	err := s.cmd.Wait()
	detached := s.backend != nil && s.backend.InnerAlive(s.ID)
	s.mu.Lock()
	if detached {
		s.status = statusDetached
	} else {
		s.status = statusExited
		s.exited = true
		s.exitAt = time.Now()
		if s.backend == nil { // ephemeral: cmd IS the agent, so its code is real
			s.exitCodeKnown = true
			if err == nil {
				s.exitCode = 0
			} else if ee, ok := err.(*exec.ExitError); ok {
				s.exitCode = ee.ExitCode()
			} else {
				s.exitCodeKnown = false
			}
		}
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
	s.muteActivity(time.Now())
	s.mu.Unlock()
	return pty.Setsize(s.ptmx, &pty.Winsize{Rows: rows, Cols: cols})
}

// ForceRedraw nudges the size (±1 col then back) to make a full-screen TUI
// repaint over a best-effort replay on attach.
func (s *Session) ForceRedraw() {
	s.mu.Lock()
	r, c := s.rows, s.cols
	s.muteActivity(time.Now())
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
	activity := statusDetached
	if s.status != statusDetached {
		activity = activityOf(s.exited, s.exitCodeKnown, s.exitCode, s.lastActivityAt, time.Now(), idleThreshold)
	}
	return Info{ID: s.ID, Agent: s.Agent, Cwd: s.Cwd, Created: s.Created, Exited: s.exited, Status: s.status, Activity: activity, Name: s.name, Runtime: s.Runtime, Attached: s.sub != nil}
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
	boxes     Boxes   // nil = containment unavailable; every spawn is host tier
	bundles   Bundles // nil = boxed sessions run without user config
	home      string  // daemon user's home; where a host session's ~/.claude lives
	stateDir  string  // data dir; a boxed session's ~/.claude lives under <stateDir>/boxstate/<box>

	// peekMu guards peekCache. It is SEPARATE from m.mu on purpose: PeekFor is
	// called from List after m.mu is released, and sharing the mutex would make
	// that ordering fragile.
	peekMu    sync.Mutex
	peekCache map[string]peekEntry
}

// SetPaths records where transcripts live: the daemon home (host-tier sessions)
// and the data dir (boxed sessions, under boxstate/<box>). Optional — unset
// leaves UsageFor unable to locate transcripts, which it reports as "no usage".
func (m *Manager) SetPaths(home, stateDir string) {
	m.mu.Lock()
	m.home, m.stateDir = home, stateDir
	m.mu.Unlock()
}

// SetBundles installs the config-bundle provider. Call once at boot.
func (m *Manager) SetBundles(b Bundles) {
	m.mu.Lock()
	m.bundles = b
	m.mu.Unlock()
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
	// Decide the tier FIRST and make it authoritative on the spec, so the
	// launcher composes for the tier the session will actually run on. Deciding
	// it in two places let a host-tier session be built with boxed semantics
	// (curated env, no login wrap) whenever containment was unavailable.
	runtime := m.runtimeFor(spec)
	spec.Runtime = runtime

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

	// Resolve the agent binary once, up front: it gates claude-only features
	// (usage) and feeds the boxed config bundle below. Best-effort — an
	// unresolvable bin just means no usage and no bundle.
	bin := ""
	if br, ok := m.launcher.(BinResolver); ok {
		if b, err := br.BinFor(spec); err == nil {
			bin = b
		}
	}

	// Containment is applied here, between the launcher and the process start,
	// so abduco persistence and the whole session lifecycle stay untouched.
	m.mu.Lock()
	id := m.nextIDLocked(spec.Agent)
	m.mu.Unlock()

	var boxName string
	if runtime == config.RuntimeBox {
		m.mu.Lock()
		boxes, bundles := m.boxes, m.bundles
		m.mu.Unlock()

		// The bundle must be resolved BEFORE Wrap: Wrap is what starts the
		// container, and its mounts are fixed at that moment.
		var mounts []box.Mount
		var bundleFlags []string
		var bundleHash string
		if bundles != nil && bin != "" {
			var err error
			mounts, bundleFlags, bundleHash, err = bundles.For(bin)
			if err != nil {
				log.Printf("session %s: config bundle unavailable (%v); the box will run without user config", id, err)
			}
		}
		argv = append(argv, bundleFlags...)

		wrapped, wrappedEnv, name, err := boxes.Wrap(context.Background(), spec.Cwd, id, argv, env, mounts)
		if err != nil {
			// A refusal already carries its own actionable message; wrapping it
			// again would print the remedy twice.
			if errors.Is(err, box.ErrUnboxableWorkspace) {
				return nil, err
			}
			// Name the way out: a silent failure here is what makes people
			// disable containment entirely.
			return nil, fmt.Errorf("session: starting box for %q: %w (spawn with runtime %q to run on the host instead)",
				spec.Cwd, err, config.RuntimeHost)
		}
		argv, env, boxName = wrapped, wrappedEnv, name
		if bundleHash != "" {
			// Logged per spawn so a box still carrying an older bundle is
			// visible rather than silent.
			log.Printf("session %s: box %s, config bundle %s", id, boxName, bundleHash)
		}
	}

	cmd, ptmx, err := m.startProcess(id, spec, argv, env)
	if err != nil {
		m.releaseBoxNamed(boxName) // never leak a box for a session that failed to start
		return nil, err
	}

	created := time.Now()
	name := deriveName(spec.Name, spec.Cwd, id)
	m.mu.Lock()
	sess := newSession(id, spec.Agent, name, spec.Cwd, created, cmd, ptmx, m.ringCap, m.backend)
	sess.Runtime = runtime
	sess.Box = boxName
	sess.Bin = bin
	m.sessions[id] = sess
	sc := m.sidecar
	m.mu.Unlock()

	if sc != nil {
		sc.put(id, sessionMeta{Agent: spec.Agent, Name: name, Cwd: spec.Cwd, Created: created, Runtime: runtime, Box: boxName})
	}
	m.watchRetention(id, sess)
	return sess, nil
}

// releaseBoxNamed tells the box layer one session for this box is gone. Safe
// with an empty name (host tier) or no box layer.
func (m *Manager) releaseBoxNamed(name string) {
	if name == "" {
		return
	}
	m.mu.Lock()
	boxes := m.boxes
	m.mu.Unlock()
	if boxes != nil {
		boxes.ReleaseBox(name)
	}
}

// releaseSessionBox releases a session's box exactly once, however the session
// ended. Both the retention goroutine and Kill call this; a detached session is
// only ever released by Kill, because the retention goroutine has already left.
func (m *Manager) releaseSessionBox(s *Session) {
	if s.Runtime != config.RuntimeBox {
		return
	}
	s.releaseOnce.Do(func() { m.releaseBoxNamed(s.Box) })
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
			m.releaseSessionBox(sess)
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
	out := make([]Info, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s.Info())
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	// Summaries are filled AFTER m.mu is released. PeekFor takes the same
	// non-reentrant mutex, so filling them inside the loop above deadlocks the
	// daemon on the first poll.
	for i := range out {
		if p, ok := m.PeekFor(out[i].ID); ok {
			out[i].Summary = summaryLine(p.Text)
		}
	}
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
			if err := boxes.KillSession(context.Background(), s.Box, id); err != nil {
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
	// Release here too, not only from the retention goroutine: that goroutine
	// RETURNS when a session goes detached, so for a detached-then-killed
	// session nothing else would ever release the box and it would outlive its
	// idle TTL forever. releaseSessionBox is once-guarded, so the common path
	// (both here and there) still counts exactly one release.
	m.releaseSessionBox(s)
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

	// Adopt every surviving box BEFORE attaching anything. Attaching can fail,
	// and Reconcile deliberately leaves such an abduco session running for a
	// later restart — so its container must survive too. Adoption keyed on
	// abduco liveness (the real source of truth for "the agent is alive"), never
	// on whether re-attaching happened to work.
	m.adoptSurvivingBoxes(aliveIDs, meta)

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

// adoptSurvivingBoxes re-registers the box of every alive boxed session, so the
// box reaper cannot destroy a container whose agent is still running. It uses
// the stored box name, so a workspace directory that has since been moved or
// deleted cannot cost a live session its container.
func (m *Manager) adoptSurvivingBoxes(aliveIDs []string, meta map[string]sessionMeta) {
	m.mu.Lock()
	boxes := m.boxes
	m.mu.Unlock()
	if boxes == nil {
		return
	}
	for _, id := range aliveIDs {
		md, ok := meta[id]
		if !ok || md.Runtime != config.RuntimeBox || md.Box == "" {
			continue
		}
		boxes.AdoptBox(md.Box)
	}
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
	// Re-resolve the agent bin from its name: the sidecar does not store it, and
	// without this a session that survived a daemon restart would have an empty
	// Bin — disabling claude-only features (usage) for every reconciled session,
	// i.e. almost all of them under abduco persistence.
	bin := ""
	if br, ok := m.launcher.(BinResolver); ok {
		if b, err := br.BinFor(launcher.SpawnSpec{Agent: md.Agent}); err == nil {
			bin = b
		}
	}
	m.mu.Lock()
	sess := newSession(id, md.Agent, name, md.Cwd, created, cmd, ptmx, m.ringCap, m.backend)
	sess.Runtime = runtime
	sess.Box = md.Box
	sess.Bin = bin
	m.sessions[id] = sess
	m.mu.Unlock()
	// The box was already adopted by Reconcile, before this attach was even
	// tried — see adoptSurvivingBoxes.
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
