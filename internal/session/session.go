// Package session is the daemon-owned source of truth for agent sessions.
// Each session owns one child process on its own PTY for the process's whole
// lifetime. A pump goroutine drains the PTY into a ring buffer (never blocking
// on any viewer); a per-subscriber delivery goroutine tails the ring to a
// WebSocket (may block on a slow viewer, harmlessly). A single reaper owns
// cmd.Wait() and closing done. No tmux, no external multiplexer.
package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/culiops/agent-box/internal/account"
	"github.com/culiops/agent-box/internal/awsscope"
	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/bundle"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/launcher"
	"github.com/culiops/agent-box/internal/sesstoken"
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
// session's lastActivity is stale. exitKnown gates crashed (see below).
//
// Silence alone is not "needs you": a quiet shell, or an agent nobody has given
// a turn yet, is "idle". Only an agent (agent) that has had at least one
// Enter-submitted input (submitted) reads "waiting" once it goes quiet. The
// wire value "waiting" keeps meaning "needs you" so every consumer stays right.
//
// exitKnown gates crashed: for abduco/boxed sessions the reaped code is the
// attach process's, not the agent's, so it must not be trusted into a red
// "crashed".
func activityOf(exited, exitKnown bool, exitCode int, lastActivity, now time.Time, idle time.Duration, agent, submitted bool) string {
	if exited {
		if exitKnown && exitCode != 0 {
			return "crashed"
		}
		return "done"
	}
	if now.Sub(lastActivity) < idle {
		return "working"
	}
	if agent && submitted {
		return "waiting"
	}
	return "idle"
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
	// Prepare readies containment for a boxed spawn; an ErrBoxUnavailable
	// error carries a user-facing remedy.
	Prepare(ctx context.Context) error
	Wrap(ctx context.Context, cwd, sessionID string, key box.BoxKey, argv, env []string, extraRO []box.Mount) (argvOut, envOut []string, boxName string, err error)
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

// TierRequester is implemented by launchers that can say which tier a spawn
// explicitly asked for (its own override, the profile's, or the instance
// default), as opposed to the tier it would fall back to. Optional, like
// RuntimeResolver.
type TierRequester interface {
	RequestedRuntime(launcher.SpawnSpec) (string, error)
}

// BinResolver is implemented by launchers that can name an agent's binary, which
// is how a config-bundle layout is chosen. Optional, like RuntimeResolver.
type BinResolver interface {
	BinFor(spec launcher.SpawnSpec) (string, error)
}

// AccountForResolver is implemented by launchers that resolve which agent
// account a spawn runs under. Optional, like BinResolver: without it every
// session runs under the default account.
type AccountForResolver interface {
	AccountFor(spec launcher.SpawnSpec) (account.Resolved, error)
}

// ReplyResolver is implemented by launchers that know a profile's reply
// default. Optional, like BinResolver: without it every session starts with
// replies off.
type ReplyResolver interface {
	ReplyableFor(spec launcher.SpawnSpec) bool
}

// OptionsResolver reports which per-spawn option overrides the launcher
// accepted, so a restart re-applies exactly those. Optional.
type OptionsResolver interface {
	AcceptedOptions(spec launcher.SpawnSpec) map[string]any
}

// AWSResolver is implemented by launchers that know a profile's AWS binding.
// Optional, like BinResolver: without it no session is ever bound.
type AWSResolver interface {
	AWSFor(agent string) *config.AWSBinding
}

// GitHubResolver is implemented by launchers that know a profile's GitHub
// binding. Optional, like AWSResolver.
type GitHubResolver interface {
	GitHubFor(agent string) *config.GitHubBinding
}

// TokenResolver is implemented by launchers that know a profile's static-token
// bindings: the entries it binds (tokens) and the entries its GitHub owners use
// as personal access tokens (pats), each sorted by name, pats deduped. An error
// names an entry the instance does not define. Optional, like AWSResolver.
type TokenResolver interface {
	TokensFor(agent string) (tokens, pats []config.StaticToken, err error)
}

// CloneResolver is implemented by launchers that know a profile's clone spec.
// Optional, like AWSResolver: without it no session is a clone session. A nil,
// nil answer means the profile has no clone_of.
type CloneResolver interface {
	CloneFor(agent string) (*config.Clone, error)
}

// CloneHooks is what spawning a clone-mode session needs from the clone layer,
// kept as an interface so this package does not import it. A nil CloneHooks
// means clone mode is unavailable (Linux only), and clone profiles are refused.
type CloneHooks interface {
	// Prepare makes the clone ready on the host (creating it if absent) and
	// returns the box identity and read-only mounts for it. withMemory adds the
	// clone's claude memory mount, which only the default account gets.
	Prepare(ctx context.Context, c config.Clone, withMemory bool) (box.CloneMounts, error)
	// BoxOwned reports whether cwd is inside any profile's clone, with the
	// reason a host-tier spawn there is refused.
	BoxOwned(cwd string) (reason string, owned bool)
	// HostOrigin is the host repo's remote.origin.url ("" when unset). It reads
	// `of`, the user's repo, never the box's clone.
	HostOrigin(ctx context.Context, of string) (string, error)
}

// cloneGHRepo is "owner/repo" for a clone-mode box when the host repo's origin
// is a GitHub repository the binding covers, else "". In a clone the box's
// origin is a host path, so gh cannot find the repo on its own. Any failure
// only costs the GH_REPO hint, never the spawn.
func cloneGHRepo(h CloneHooks, of string, g *config.GitHubBinding) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	origin, err := h.HostOrigin(ctx, of)
	if err != nil {
		log.Printf("session: clone_of %s: cannot read its origin, so no GH_REPO: %v", of, err)
		return ""
	}
	owner, repo, ok := config.ParseGitHubRepo(origin)
	if !ok {
		if origin != "" {
			log.Printf("session: clone_of %s: origin is not a plain GitHub repository, so no GH_REPO", of)
		}
		return ""
	}
	entry, ok := g.OwnerEntry(owner)
	if !ok || !slices.ContainsFunc(entry.Repos, func(r string) bool { return strings.EqualFold(r, repo) }) {
		log.Printf("session: clone_of %s: origin %s/%s is outside the GitHub binding, so no GH_REPO", of, owner, repo)
		return ""
	}
	return owner + "/" + repo
}

// BrokerHooks is what spawning a bound session needs from the credential
// broker, kept as an interface so this package does not import it (the broker
// adapter implements it). A nil BrokerHooks means the broker is not running,
// and every bound profile is refused rather than started without it.
type BrokerHooks interface {
	// NewNonce returns a fresh per-spawn secret. Session ids are reused, so the
	// token is bound to (id, nonce), never to the id alone.
	NewNonce() string
	// Token is the bearer token the session's box presents to the broker.
	Token(sessionID, nonce string) string
	// BoxFor prepares the binding's host files and returns the read-only
	// mounts and the env (NAME=value) that point a box's AWS and GitHub tooling at the
	// broker. The values reach the box by name only (box.Wrap's `-e NAME`).
	BoxFor(bs config.Bindings) (mounts []box.Mount, env []string, err error)
	// CheckGitHub reports whether every App a GitHub binding names is usable
	// (configured, key loads; no network call). Doctor-style: never key bytes.
	CheckGitHub(g config.GitHubBinding) error
	// StripEnv names every variable a bound session must not inherit from its
	// profile: static credentials and anything that would shadow the broker.
	StripEnv() []string
	// StripGitHubEnv names the extra variables a session with a GitHub binding
	// must not inherit (static-token routes, git and gh settings). Applied only
	// to such sessions, so an AWS-only session keeps its profile's own values.
	StripGitHubEnv() []string
	// Forget drops the broker's per-session state (cache, stats) once a
	// session is gone from the registry.
	Forget(sessionID string)
	// RevokeGitHub revokes, at GitHub, the installation tokens minted for the
	// session. It returns at once (the broker revokes in the background) and
	// is idempotent. Called when the agent exits, not when it merely detaches.
	RevokeGitHub(sessionID string)
	// AWSState is the broker-held state a session list shows: when the
	// session's write window ends (zero = none) and the sign-in a human must
	// renew on the host ("" = none). Read-only; it takes only the broker's lock.
	AWSState(sessionID string) (elevatedUntil time.Time, ssoExpired string)
	// WriteSessionMCP writes the session's own MCP config (the entry servers a
	// token binding adds) into the binding dir and returns its path inside the
	// box. key names the file; the document carries no secret.
	WriteSessionMCP(bs config.Bindings, key string, doc []byte) (string, error)
}

// EgressHooks is what spawning a bound session needs from the egress proxy,
// kept as an interface so this package does not import it. A nil EgressHooks
// means egress control is not running, and bound profiles are refused: a bound
// box with no proxy in front of it would have the open network the allowlist
// exists to close.
type EgressHooks interface {
	// SpawnEnv readies the egress path (network + listener) and returns the env
	// (NAME=value) that points a bound box's tools at the proxy, carrying this
	// spawn's token. Values reach the box by name only (box.Wrap's `-e NAME`).
	SpawnEnv(ctx context.Context, sessionID, nonce string) ([]string, error)
	// StripEnv names every variable a bound session must not inherit: proxy
	// settings that would point elsewhere or exempt hosts.
	StripEnv() []string
	// Forget drops the proxy's per-session state (counters, open tunnels) once
	// the session has left the registry.
	Forget(sessionID string)
}

// brokerTokenEnv carries the session's token into its box (the broker
// package's TokenEnv; this package does not import it).
const brokerTokenEnv = "AGENTBOX_BROKER_TOKEN"

// AWSInfo is the wire view of a session's AWS binding: what it can reach, never
// how it authenticates (no source profile, no nonce, no token).
type AWSInfo struct {
	Label     string `json:"label"`
	AccountID string `json:"account_id"`
	RoleARN   string `json:"role_arn"`
	Prod      bool   `json:"prod"`
	// Elevate is the write role a human may widen the session to; absent when
	// the binding allows none. It names no source profile: how the daemon signs
	// in stays off the list (the sign-in command travels only in GET .../aws).
	Elevate *ElevateInfo `json:"elevate,omitempty"`
	// ElevatedUntil is when the live write window ends; absent with none.
	// Filled by List only.
	ElevatedUntil *time.Time `json:"elevated_until,omitempty"`
	// SSOExpired is the sign-in key the session waits on a human to renew;
	// absent with none. Filled by List only.
	SSOExpired string `json:"sso_expired,omitempty"`
}

// GitHubInfo is the wire view of a session's GitHub binding: which owners and
// how many repositories it can reach, never an App, a token or a nonce.
type GitHubInfo struct {
	Owners []string `json:"owners"`
	Repos  int      `json:"repos"`
}

// ElevateInfo is the wire view of a binding's elevate block.
type ElevateInfo struct {
	RoleARN    string `json:"role_arn"`
	MaxMinutes int    `json:"max_minutes"`
	AllowIAM   bool   `json:"allow_iam"`
}

// Bundles supplies the pinned agent-config bundle for a boxed session. Nil means
// no bundle: sessions still run, just without user config.
type Bundles interface {
	// For returns the read-only mounts a box carries (the same for every bin,
	// because sessions of different bins share a box) and the extra argv flags
	// bin's agent needs, plus the bundle hash for the log line. A bin with no
	// layout (a shell, or "" when unresolved) gets the mounts and no flags.
	For(bin string) (bundle.Grant, error)
}

// TokenInfo is the wire view of one bound static-token entry: what it is called
// and which MCP server uses it, never its value, its reference or its hosts.
type TokenInfo struct {
	Name      string `json:"name"`
	Provider  string `json:"provider,omitempty"`
	Label     string `json:"label,omitempty"`
	Prod      bool   `json:"prod,omitempty"`
	MCPServer string `json:"mcp_server,omitempty"`
}

// Info is a read-only snapshot for listing.
type Info struct {
	ID      string    `json:"id"`
	Agent   string    `json:"agent"`
	Cwd     string    `json:"cwd,omitempty"`
	Created time.Time `json:"created"`
	Exited  bool      `json:"exited"`
	Status  string    `json:"status"`
	// Activity is a UI-facing classification: working|waiting|idle|done|crashed|detached.
	// List can RAISE this to "waiting" for a claude session the live registry
	// says is blocked on a prompt (see needOf) — it never lowers it. That raise
	// happens only in List, the same way Claude below is only filled there: a
	// Get/Spawn response is never rendered as a row, so it carries the
	// un-raised value.
	Activity string `json:"activity"`
	Name     string `json:"name"`              // mutable display label (id is the stable key)
	Runtime  string `json:"runtime,omitempty"` // box | host
	// Options are the per-spawn option overrides this session started with
	// (bools and enum choices — never secrets). Duplicate pre-fills from them.
	Options map[string]any `json:"options,omitempty"`
	// Attached reports that a viewer is subscribed right now. It says only THAT
	// someone is watching, never who: every client authenticates with the same
	// credentials, so the daemon has no identity to report and inventing one
	// would be a lie.
	Attached bool `json:"attached"`
	// Summary is the first line of the agent's last message, elided to
	// summaryMax runes. Empty for a non-claude session or one whose transcript
	// cannot be read — the client tells those apart via the /peek endpoint.
	Summary   string `json:"summary,omitempty"`
	Replyable bool   `json:"replyable"`
	// Claude reports isClaudeBin(Bin) — the seam every peek/reply feature is
	// scoped by. The client needs it to decide whether a session HAS a peek at
	// all, which it cannot infer from Summary (empty also means "transcript
	// unreadable") nor from Agent (a profile name, which may be anything).
	// Filled by List, the only caller that reads Bin under m.mu — the Spawn and
	// Get responses leave it false. That is safe only because the client always
	// re-lists after a spawn and never renders a row from those responses; a
	// new surface that needs it must go through List or take m.mu itself.
	Claude bool `json:"claude"`
	// Need says why a waiting claude session needs you: "ask" (blocked on a
	// permission/choice prompt) or "turn" (it finished its turn); or "sso" (a
	// bound session, claude or not, whose AWS sign-in on the host has expired;
	// it replaces "" and "turn", never "ask"). Empty for
	// every other session, including a waiting one whose transcript cannot be
	// read — that one is still amber, just without a label. Like the registry
	// raise itself (see needOf), filled by List only.
	Need string `json:"need,omitempty"`
	// Ask is Peek.Ask for a Need=="ask" session: the tool call it wants to run.
	Ask string `json:"ask,omitempty"`
	// AskFull is Peek.AskFull for a Need=="ask" session that is registry-
	// blocked AND replyable — the one case the card can answer (Manager.Answer
	// refuses every other). Filled by List only.
	AskFull string `json:"ask_full,omitempty"`
	// Resumable: an exited host-tier claude whose conversation id is known —
	// Restart will continue it (`--resume`). Filled by List only.
	Resumable bool `json:"resumable,omitempty"`
	// Account is the agent account the session runs under; empty for the
	// default account, so a default session's wire shape is unchanged.
	Account string `json:"account,omitempty"`
	// AWS is the binding the session was spawned with; nil (absent) for an
	// unbound session, so its wire shape is unchanged.
	AWS *AWSInfo `json:"aws,omitempty"`
	// GitHub is the GitHub binding the session was spawned with; nil (absent)
	// without one, so an AWS-only or unbound session's wire shape is unchanged.
	GitHub *GitHubInfo `json:"github,omitempty"`
	// Tokens are the static-token entries the session was spawned with; absent
	// without any, so a session with none keeps its wire shape. Never a value.
	Tokens []TokenInfo `json:"tokens,omitempty"`
	// Egress is the session's network posture: "allowlist" (bound, only its
	// binding's hosts), "open" (unbound, any host through the authenticated
	// proxy) or "" (no egress control: host tier). Never the nonce.
	Egress string `json:"egress,omitempty"`
	// BundleDrops is what the box's config bundle left out and why (a config
	// key, an MCP server, the plugin set, or the whole bundle). Boxed sessions
	// whose bundle was consulted only; absent otherwise, so a host session's
	// wire shape is unchanged. Reasons are fixed strings, never an error text.
	BundleDrops []bundle.Drop `json:"bundle_drops,omitempty"`
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
	// Account is the agent account the session runs under ("" = default).
	// ConfigDir is that account's config dir, set only for a host-tier session
	// under a non-default account: a boxed session's config lives in its box,
	// and the default account's dir is never ours to record. Both are set
	// before publication and never mutated.
	Account   string
	ConfigDir string
	// AWS is the binding the session was spawned with; immutable, read without
	// the lock. A later profile edit never changes a running session's scope.
	AWS *config.AWSBinding
	// GitHub is the GitHub binding the session was spawned with; immutable,
	// read without the lock, like AWS.
	GitHub *config.GitHubBinding
	// Tokens are the static-token entries the session was spawned with and PATs
	// the entries its GitHub owners use as personal access tokens (deep copies,
	// sorted by name). Immutable and read without the lock, like AWS: a later
	// edit to static_tokens never changes a running session's scope.
	Tokens, PATs []config.StaticToken
	// BrokerNonce is this spawn's broker-token nonce (set iff the session has
	// any binding). Immutable, read without the lock. Never put it on the wire:
	// Info leaves it out.
	BrokerNonce string
	// EgressNonce is this spawn's egress-token nonce, set for every session
	// whose box has egress (bound or open). Immutable, read without the lock.
	// Never put it on the wire: Info leaves it out.
	EgressNonce string
	// BundleDrops is what the config bundle left out for this boxed session
	// (see Info.BundleDrops). Immutable after publication, read without the
	// lock; Info hands out a copy.
	BundleDrops []bundle.Drop
	// Exclusive: this session's box is its own (an elevate binding). Immutable;
	// the broker refuses a grant without it.
	Exclusive bool

	// Replyable is guarded by s.mu: unlike Runtime it changes during the
	// session's life, from the peek panel's toggle.
	replyable bool

	// submitted: at least one Enter (\r) has been written to this session —
	// the only input that means "the operator gave it a turn". Guarded by mu.
	submitted bool
	// agentKind mirrors isAgentBin(Bin) under mu, so Info() can gate "waiting"
	// without m.mu (Bin itself is guarded by m.mu: ResolveBins backfills it).
	agentKind bool
	// onSubmitted persists the false->true edge of submitted. Set before the
	// session is published to m.sessions and never changed, so it needs no
	// lock; called with NO lock held.
	onSubmitted func()

	// options are the accepted per-spawn overrides (OptionsResolver). Set
	// before publication and never mutated, so read without a lock.
	options map[string]any

	// claudeSessionID is Claude Code's conversation id as last validated by
	// locate (registry), so it follows /clear. Guarded by mu.
	claudeSessionID string
	// restarting latches the first Restart of an exited session, so a double
	// click cannot start two replacements. Guarded by mu.
	restarting bool

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

	// answerMu serializes Manager.Answer for THIS session: the gate-check
	// (peekAndBlocked + the lastAnswered memo below) through the Write is one
	// critical section, so two concurrent or rapidly-repeated Answer calls on
	// an unchanged prompt cannot both pass the gates and both write — see
	// Answer's doc comment. answerMu is the OUTERMOST lock: Answer takes it
	// only after releasing mu and the Manager's mu from its earlier reads,
	// and peekAndBlocked's own m.locate call DOES take the Manager's mu (and
	// nested under it, mu) WHILE answerMu is still held — that nesting is
	// fine and expected. What must never happen is the reverse: acquiring
	// answerMu while mu or the Manager's mu is already held.
	answerMu sync.Mutex
	// lastAnswered is the pending-prompt identity Answer last wrote a key
	// for (zero value = none yet). Guarded by answerMu; only Answer touches
	// it.
	lastAnswered answerState

	// escMu guards lastEscAt, shared by Interrupt (agent Esc) and
	// Answer("no") so the two paths debounce each other's Esc — see
	// escGate. Independent of answerMu: Interrupt never takes it.
	escMu     sync.Mutex
	lastEscAt time.Time // guarded by escMu; zero = no Esc sent yet
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

// Write sends input to the PTY. An Enter anywhere in p marks the session as
// having been given a turn (see submitted). Focus reports, CPR/DA replies and
// text without Enter do not count: xterm emits the former on its own.
func (s *Session) Write(p []byte) (int, error) {
	n, err := s.ptmx.Write(p)
	if n > 0 && bytes.IndexByte(p[:n], '\r') >= 0 {
		s.markSubmitted()
	}
	return n, err
}

// markSubmitted sets submitted and fires onSubmitted on the false->true edge
// only — one sidecar write per session lifetime.
func (s *Session) markSubmitted() {
	s.mu.Lock()
	first := !s.submitted
	s.submitted = true
	cb := s.onSubmitted
	s.mu.Unlock()
	if first && cb != nil {
		cb()
	}
}

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
		activity = activityOf(s.exited, s.exitCodeKnown, s.exitCode, s.lastActivityAt, time.Now(), idleThreshold, s.agentKind, s.submitted)
	}
	in := Info{ID: s.ID, Agent: s.Agent, Cwd: s.Cwd, Created: s.Created, Exited: s.exited, Status: s.status, Activity: activity, Name: s.name, Runtime: s.Runtime, Attached: s.sub != nil, Replyable: s.replyable, Options: s.options, Account: s.Account}
	switch {
	case s.EgressNonce != "" && s.Bindings().Any():
		in.Egress = "allowlist"
	case s.EgressNonce != "":
		in.Egress = "open"
	}
	in.BundleDrops = slices.Clone(s.BundleDrops)
	if s.AWS != nil {
		in.AWS = &AWSInfo{Label: s.AWS.Label, AccountID: awsscope.AccountOf(s.AWS.RoleARN), RoleARN: s.AWS.RoleARN, Prod: s.AWS.Prod}
		if e := s.AWS.Elevate; e != nil {
			in.AWS.Elevate = &ElevateInfo{RoleARN: e.RoleARN, MaxMinutes: e.Max(), AllowIAM: e.AllowIAM}
		}
	}
	if g := s.GitHub; g != nil {
		gi := &GitHubInfo{Owners: make([]string, len(g.Owners))}
		for i, o := range g.Owners {
			gi.Owners[i] = o.Owner
			gi.Repos += len(o.Repos)
		}
		in.GitHub = gi
	}
	for _, e := range s.Tokens {
		ti := TokenInfo{Name: e.Name, Provider: e.Provider, Label: e.Label, Prod: e.Prod}
		if e.MCP != nil {
			ti.MCPServer = e.MCP.Server
		}
		in.Tokens = append(in.Tokens, ti)
	}
	return in
}

// Bindings is the set of credential bindings the session was spawned with.
// Immutable, so it needs no lock.
func (s *Session) Bindings() config.Bindings {
	return config.Bindings{AWS: s.AWS, GitHub: s.GitHub, Tokens: s.Tokens, PATs: s.PATs}
}

// claudeSID returns the last validated Claude conversation id ("" if none).
func (s *Session) claudeSID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claudeSessionID
}

// SetName updates the mutable display label.
func (s *Session) SetName(name string) {
	s.mu.Lock()
	s.name = name
	s.mu.Unlock()
}

// SetReplyable flips the operator's opt-in and persists it. Turning it OFF must
// always work for a live session; turning it ON is the act that carries risk,
// and is deliberate.
//
// Granting is gated on isClaudeBin — the same seam SendMessage enforces, so the
// UI cannot offer an opt-in the send path would then refuse, and no future
// caller can grant it by going around the handler. REVOKING is never gated: a
// session that somehow holds the flag (restored from a sidecar, or whose Bin
// resolved differently later) must always be able to give it up.
func (m *Manager) SetReplyable(id string, v bool) error {
	m.mu.Lock()
	s := m.sessions[id]
	sc := m.sidecar
	repliesOn := m.repliesOn
	var bin string
	if s != nil {
		bin = s.Bin // guarded by m.mu: ResolveBins backfills it post-reconcile
	}
	m.mu.Unlock()
	if s == nil {
		return ErrNoSession
	}
	// Granting while the kill-switch is off is refused; revoking never is.
	if v && !repliesOn {
		return ErrRepliesDisabled
	}
	if v && !isClaudeBin(bin) {
		return ErrNotClaude
	}
	s.mu.Lock()
	s.replyable = v
	s.mu.Unlock()
	if sc != nil {
		sc.setReplyable(id, v)
	}
	return nil
}

// submittedHook persists a session's first submitted input. Takes m.mu only
// long enough to read the sidecar pointer; Write never holds m.mu, so the lock
// order stays m.mu -> s.mu with nothing inverted.
func (m *Manager) submittedHook(id string) func() {
	return func() {
		m.mu.Lock()
		sc := m.sidecar
		m.mu.Unlock()
		if sc != nil {
			sc.setSubmitted(id)
		}
	}
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
	boxes     Boxes       // nil = containment unavailable; every spawn is host tier
	bundles   Bundles     // nil = boxed sessions run without user config
	broker    BrokerHooks // nil = credential broker not running; bound profiles are refused
	egress    EgressHooks // nil = egress control not running; bound profiles are refused
	egressAll bool        // every boxed spawn needs egress (Linux); see SetEgressAll
	clone     CloneHooks  // nil = clone mode unavailable; clone profiles are refused
	home      string      // daemon user's home; where a host session's ~/.claude lives
	stateDir  string      // data dir; a boxed session's ~/.claude lives under <stateDir>/boxstate/<box>
	repliesOn bool        // config replies_enabled kill-switch; false = no reply, answer or opt-in

	// peekMu guards peekCache. It is SEPARATE from m.mu on purpose: PeekFor is
	// called from List after m.mu is released, and sharing the mutex would make
	// that ordering fragile.
	peekMu    sync.Mutex
	peekCache map[string]peekEntry

	// procStart reads a pid's starttime for the registry's pid-reuse guard.
	// NewManager sets procStartOf; a nil value (struct-literal test managers)
	// disables the registry path, falling back to the heuristic.
	procStart procStartFunc
}

// SetPaths records where transcripts live: the daemon home (host-tier sessions)
// and the data dir (boxed sessions, under boxstate/<box>). Optional — unset
// leaves UsageFor unable to locate transcripts, which it reports as "no usage".
func (m *Manager) SetPaths(home, stateDir string) {
	m.mu.Lock()
	m.home, m.stateDir = home, stateDir
	m.mu.Unlock()
}

// SetRepliesEnabled sets the instance-wide reply kill-switch (config
// replies_enabled). Call once at boot, before Reconcile and Spawn: both read it
// to decide whether a session may be replyable. Default (never called) is off.
func (m *Manager) SetRepliesEnabled(v bool) {
	m.mu.Lock()
	m.repliesOn = v
	m.mu.Unlock()
}

// SetBundles installs the config-bundle provider. Call once at boot.
func (m *Manager) SetBundles(b Bundles) {
	m.mu.Lock()
	m.bundles = b
	m.mu.Unlock()
}

// SetBroker installs the credential-broker hooks. Call once at boot.
func (m *Manager) SetBroker(h BrokerHooks) {
	m.mu.Lock()
	m.broker = h
	m.mu.Unlock()
}

// SetEgress installs the egress-proxy hooks. Call once at boot, with SetBroker.
func (m *Manager) SetEgress(h EgressHooks) {
	m.mu.Lock()
	m.egress = h
	m.mu.Unlock()
}

// SetEgressAll makes egress mandatory for every boxed spawn, bound or not: an
// unbound box gets the same proxy env in open mode, and is refused when egress
// control is not running. Call once at boot (Linux), with SetEgress.
func (m *Manager) SetEgressAll(all bool) {
	m.mu.Lock()
	m.egressAll = all
	m.mu.Unlock()
}

// SetClone installs the clone-mode hooks. Call once at boot (Linux), with
// SetBoxes. Nil means clone mode is unavailable: clone profiles are refused.
func (m *Manager) SetClone(h CloneHooks) {
	m.mu.Lock()
	m.clone = h
	m.mu.Unlock()
}

// SetBoxes installs the containment layer. Call once at boot, before Reconcile.
func (m *Manager) SetBoxes(b Boxes) {
	m.mu.Lock()
	m.boxes = b
	m.mu.Unlock()
}

// runtimeFor resolves the tier for a spawn. Without a tier-aware launcher or a
// box layer the answer is host — containment is never assumed, only granted,
// and never silently withdrawn from a request that named it: with no box layer,
// a spawn that asked for box (its own Runtime, else the profile's or instance's
// tier via TierRequester) is refused rather than quietly run uncontained.
func (m *Manager) runtimeFor(spec launcher.SpawnSpec) (string, error) {
	m.mu.Lock()
	boxes := m.boxes
	m.mu.Unlock()
	if boxes == nil {
		asked := spec.Runtime
		if asked == "" {
			if tr, ok := m.launcher.(TierRequester); ok {
				if rt, err := tr.RequestedRuntime(spec); err == nil {
					asked = rt
				}
			}
		}
		if asked == config.RuntimeBox {
			return "", fmt.Errorf("%w: this session asks for runtime box, and this instance has no box layer (see the boot log); set the profile's tier to host (or send runtime \"host\") or fix containment", box.ErrBoxUnavailable)
		}
		return config.RuntimeHost, nil
	}
	rr, ok := m.launcher.(RuntimeResolver)
	if !ok {
		return config.RuntimeHost, nil
	}
	rt, err := rr.RuntimeFor(spec)
	if err != nil || rt == "" {
		return config.RuntimeHost, nil
	}
	return rt, nil
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
		procStart: procStartOf,
	}
}

// Spawn builds the agent command and starts a session — under the persistence
// backend (abduco) if present, else a daemon-owned PTY directly.
func (m *Manager) Spawn(spec launcher.SpawnSpec) (*Session, error) {
	// Decide the tier FIRST and make it authoritative on the spec, so the
	// launcher composes for the tier the session will actually run on. Deciding
	// it in two places let a host-tier session be built with boxed semantics
	// (curated env, no login wrap) whenever containment was unavailable.
	runtime, err := m.runtimeFor(spec)
	if err != nil {
		return nil, err
	}
	spec.Runtime = runtime

	// Resolve the agent binary once, up front: it gates claude-only features
	// (usage, replies) and feeds the boxed config bundle below. Best-effort —
	// an unresolvable bin just means no usage, no replies and no bundle.
	bin := ""
	if br, ok := m.launcher.(BinResolver); ok {
		if b, err := br.BinFor(spec); err == nil {
			bin = b
		}
	}

	// The account is resolved before anything is prepared or started, so a
	// refused one (unknown, codex, a profile carrying its own auth) costs
	// nothing. Without a resolver every session is the default account.
	acct := account.DefaultResolved("")
	if ar, ok := m.launcher.(AccountForResolver); ok {
		r, err := ar.AccountFor(spec)
		if err != nil {
			return nil, err
		}
		acct = r
	}
	acctName, cfgDir := "", ""
	if !acct.IsDefault {
		acctName = acct.Name
		if runtime != config.RuntimeBox {
			cfgDir = acct.ConfigDir
		}
	}

	// An AWS binding runs only in a box, and only through the broker. Refused
	// here, before anything is prepared, built or issued: a host-tier spawn
	// (the profile's tier, a per-spawn "host" override, or containment being
	// unavailable) would hand the agent the host's own AWS access, and no token
	// is minted for a spawn that is refused.
	// A GitHub binding is the same: bound means "has any binding".
	var binding *config.AWSBinding
	if r, ok := m.launcher.(AWSResolver); ok {
		binding = r.AWSFor(spec.Agent)
	}
	var ghBinding *config.GitHubBinding
	if r, ok := m.launcher.(GitHubResolver); ok {
		ghBinding = r.GitHubFor(spec.Agent)
	}
	var tokens, pats []config.StaticToken
	if r, ok := m.launcher.(TokenResolver); ok {
		var err error
		tokens, pats, err = r.TokensFor(spec.Agent)
		if err != nil {
			// ErrBoxUnavailable so the UI shows which profile and why.
			return nil, fmt.Errorf("%w: %w", box.ErrBoxUnavailable, err)
		}
	}
	bs := config.Bindings{AWS: binding, GitHub: ghBinding, Tokens: tokens, PATs: pats}
	m.mu.Lock()
	brk := m.broker
	eg := m.egress
	egressAll := m.egressAll
	m.mu.Unlock()
	needEgress := runtime == config.RuntimeBox && (bs.Any() || egressAll)
	if bs.Any() {
		kind := bindingKind(bs)
		if runtime != config.RuntimeBox {
			return nil, fmt.Errorf("profile %q has %s, so it can only run in a box, never on the host tier", spec.Agent, kind)
		}
		if brk == nil {
			return nil, fmt.Errorf("profile %q has %s but the credential broker is not running", spec.Agent, kind)
		}
		if eg == nil {
			// ErrBoxUnavailable: a containment refusal the UI shows verbatim.
			return nil, fmt.Errorf("%w: profile %q has %s but egress control is not running, so it cannot spawn",
				box.ErrBoxUnavailable, spec.Agent, kind)
		}
	} else if needEgress && eg == nil {
		// Fail closed before Prepare: a box with no proxy in front of it would
		// have the open network the egress control exists to close.
		return nil, fmt.Errorf("%w: egress control is not running, so no box can start", box.ErrBoxUnavailable)
	}

	// A clone profile runs only in a box, only in its own clone. Refused before
	// anything is prepared, built or created: the host tier would run the agent
	// (and its tools) inside a tree a box can rewrite, and a spawn anywhere but
	// the clone root would mount a subdirectory and leave the .git and the share
	// mountpoints out (spec §6.1). The reverse also holds (§6.4): a host-tier
	// spawn inside any clone is refused, because the clone is the box's.
	var cl *config.Clone
	m.mu.Lock()
	ch := m.clone
	m.mu.Unlock()
	if r, ok := m.launcher.(CloneResolver); ok {
		c, err := r.CloneFor(spec.Agent)
		if err != nil {
			// ErrBoxUnavailable so the UI shows which profile and why.
			return nil, fmt.Errorf("%w: profile %q: %w", box.ErrBoxUnavailable, spec.Agent, err)
		}
		cl = c
	}
	// Both checks compare an absolute path: a relative cwd never compares as
	// under an absolute clone dir, yet the process resolves it against the
	// daemon's own cwd. Fail closed when it cannot be made absolute.
	absCwd, absErr := filepath.Abs(spec.Cwd)
	switch {
	case cl != nil:
		if runtime != config.RuntimeBox {
			// ErrUnboxableWorkspace so the UI shows the refusal (writeSpawnErr).
			return nil, fmt.Errorf("%w: profile %q is a clone-mode profile, so it can only run in a box, never on the host tier",
				box.ErrUnboxableWorkspace, spec.Agent)
		}
		if ch == nil {
			return nil, fmt.Errorf("%w: clone mode is not available on this host (Linux only)", box.ErrBoxUnavailable)
		}
		if absErr != nil || absCwd != cl.Dir {
			return nil, fmt.Errorf("%w: profile %q runs only in its clone %s, not %s", box.ErrUnboxableWorkspace, spec.Agent, cl.Dir, spec.Cwd)
		}
	case runtime != config.RuntimeBox && ch != nil:
		if absErr != nil {
			return nil, fmt.Errorf("%w: cannot resolve working directory %q: %v", box.ErrBoxOwned, spec.Cwd, absErr)
		}
		if reason, owned := ch.BoxOwned(absCwd); owned {
			return nil, fmt.Errorf("%w: %s", box.ErrBoxOwned, reason)
		}
	}

	if runtime == config.RuntimeBox {
		m.mu.Lock()
		boxes := m.boxes
		m.mu.Unlock()
		// Before the bundle: its MCP probe runs a container, which on macOS
		// needs the box VM up. An ErrBoxUnavailable reaches the user as-is.
		if err := boxes.Prepare(context.Background()); err != nil {
			return nil, err
		}
	}

	// The clone is made (or verified) on the host only after every cheap
	// refusal above, and before the box exists: Wrap's mounts are fixed at
	// container start. Creating a repo copy can take minutes; the error is
	// returned as is because it already carries its own remedy.
	var cloneMounts *box.CloneMounts
	if cl != nil {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		cm, err := ch.Prepare(cctx, *cl, acct.IsDefault)
		cancel()
		if err != nil {
			return nil, err
		}
		cloneMounts = &cm
	}

	// The bundle is resolved BEFORE Build: its flags must sit ahead of the "--"
	// that ends options (a first message follows it), and Build alone owns the
	// argv order. Its mounts are fixed when Wrap starts the container below, and
	// they are the box's, not the agent's: every boxed spawn asks, whatever its
	// bin, so a box a shell starts already carries what a claude session that
	// reuses it will need (brain #131). Only the flags depend on bin.
	var mounts []box.Mount
	var bundleHash string
	var bundleFailed bool // mounts unknown, not empty: see box.BoxKey.PartialMounts
	var bundleDrops []bundle.Drop
	if runtime == config.RuntimeBox {
		m.mu.Lock()
		bundles := m.bundles
		m.mu.Unlock()
		// A non-default account gets the personal config only when it opted in:
		// the bundle carries the default login's skills and MCP servers. The
		// account is part of the box key, so this gate never differs between
		// two sessions that share a box.
		if bundles != nil && (acct.IsDefault || acct.InheritPersonal) {
			g, err := bundles.For(bin)
			if err != nil {
				bundleFailed = true
				// One row, not silence. The reason is a fixed string: err can
				// carry host paths and stays in the log line below only.
				bundleDrops = []bundle.Drop{{Kind: bundle.KindBundle, Name: "*", Reason: bundle.ReasonBundleFailed}}
				log.Printf("spawn %s: config bundle unavailable (%v); the box will run without user config", spec.Agent, err)
			}
			mounts, bundleHash = g.Mounts, g.Hash
			if err == nil {
				bundleDrops = g.Drops
			}
			spec.ExtraArgs = append(spec.ExtraArgs, g.Flags...)
		}
	}

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
	id := m.nextIDLocked(spec.Agent)
	m.mu.Unlock()

	// Containment is applied here, between the launcher and the process start,
	// so abduco persistence and the whole session lifecycle stay untouched.
	var boxName string
	var nonce, enonce string
	var exclusive bool
	if runtime == config.RuntimeBox {
		m.mu.Lock()
		boxes := m.boxes
		m.mu.Unlock()
		key := box.BoxKey{Account: acctName, PartialMounts: bundleFailed, Clone: cloneMounts}
		var bm []box.Mount
		var benv, penv []string
		if bs.Any() {
			if bs.GitHub != nil {
				// Doctor-style and secret-free, so its message can be shown as is.
				if err := brk.CheckGitHub(*bs.GitHub); err != nil {
					return nil, fmt.Errorf("%w: profile %q: %w", box.ErrBoxUnavailable, spec.Agent, err)
				}
			}
			var err error
			bm, benv, err = brk.BoxFor(bs)
			if err != nil {
				return nil, fmt.Errorf("spawn %s: preparing %s access: %w", spec.Agent, bindingNames(bs), err)
			}
			nonce = brk.NewNonce()
		}
		if needEgress {
			// The proxy URL carries a token derived from this nonce, so a
			// restored session's box keeps working. A failure here refuses the
			// spawn: no proxy, no network, no session.
			enonce = sesstoken.NewNonce()
			ectx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			var err error
			penv, err = eg.SpawnEnv(ectx, id, enonce)
			cancel()
			if err != nil {
				// Shown to the user (writeSpawnErr): these are operator remedies
				// (a network to remove, a port in use), and the token is only
				// derived after Ready succeeds, so it can never be in err.
				return nil, fmt.Errorf("%w: spawn %s: preparing egress: %w", box.ErrBoxUnavailable, spec.Agent, err)
			}
		}
		if bs.Any() {
			// Strip before adding: whatever the profile or daemon env says about
			// AWS must neither bypass credential_process nor shadow a broker
			// value. The broker's own names are stripped too, so each is set
			// exactly once, by the broker.
			strip := append(slices.Clone(brk.StripEnv()), brokerTokenEnv) // never append into the hooks' own slice
			strip = append(strip, eg.StripEnv()...)
			if bs.GitHub != nil {
				// Only a GitHub-bound session loses a profile's own GH_TOKEN and
				// friends; an AWS-only session keeps them.
				strip = append(strip, brk.StripGitHubEnv()...)
			}
			for _, e := range bs.Tokens {
				// An entry's variables are delivered by agentbox-env, per child
				// process: a profile's own value for one must not sit in the
				// session env beside it.
				strip = append(strip, e.Names()...)
			}
			for _, kv := range slices.Concat(benv, penv) {
				if i := strings.IndexByte(kv, '='); i > 0 {
					strip = append(strip, kv[:i])
				}
			}
			env = withoutEnv(env, strip)
			env = append(env, benv...)
			env = append(env, penv...)
			env = append(env, brokerTokenEnv+"="+brk.Token(id, nonce))
			if cl != nil && bs.GitHub != nil {
				// After the strip, which drops a profile's own GH_REPO.
				if repo := cloneGHRepo(ch, cl.Of, bs.GitHub); repo != "" {
					env = append(env, "GH_REPO="+repo)
				}
			}
			mounts = append(slices.Clip(mounts), bm...) // never write into the bundle provider's array
			key.Binding = bs.Hash()
			if binding != nil && binding.Elevate != nil {
				// An elevatable binding gets a box of its own (M3b §7.1):
				// processes in a shared box can read each other's broker token
				// from /proc, and an elevated token must not be readable by
				// another session.
				key.Session = id
			}
		} else if needEgress {
			// Open mode: the same proxy env, no broker. The profile's own proxy
			// settings are stripped so each name is set exactly once.
			strip := slices.Clone(eg.StripEnv())
			for _, kv := range penv {
				if i := strings.IndexByte(kv, '='); i > 0 {
					strip = append(strip, kv[:i])
				}
			}
			env = withoutEnv(env, strip)
			env = append(env, penv...)
		}
		wrapped, wrappedEnv, name, err := boxes.Wrap(context.Background(), spec.Cwd, id, key, argv, env, mounts)
		if err != nil {
			// A refusal already carries its own actionable message; wrapping it
			// again would print the remedy twice. A containment-check failure
			// gets no "run on the host" hint either: that is the one thing to
			// push the user away from right after containment failed.
			if errors.Is(err, box.ErrUnboxableWorkspace) || errors.Is(err, box.ErrBoxUnavailable) || errors.Is(err, box.ErrWorkspaceHeld) {
				return nil, err
			}
			// Name the way out: a silent failure here is what makes people
			// disable containment entirely.
			return nil, fmt.Errorf("session: starting box for %q: %w (spawn with runtime %q to run on the host instead)",
				spec.Cwd, err, config.RuntimeHost)
		}
		argv, env, boxName = wrapped, wrappedEnv, name
		exclusive = key.Session != ""
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

	// Replies start at the profile default (or this spawn's override) and stay
	// a claude-only feature whatever the profile says — SetReplyable's seam.
	replyable := false
	if rr, ok := m.launcher.(ReplyResolver); ok {
		replyable = rr.ReplyableFor(spec)
	}
	// The kill-switch wins over the profile default and the spawn override.
	m.mu.Lock()
	repliesOn := m.repliesOn
	m.mu.Unlock()
	replyable = replyable && isClaudeBin(bin) && repliesOn
	var opts map[string]any
	if ar, ok := m.launcher.(OptionsResolver); ok {
		opts = ar.AcceptedOptions(spec)
	}
	// A first message IS the operator's first turn: the agent was given its
	// task, so its silence afterwards means "your turn", not "idle".
	submitted := spec.Prompt != ""

	created := time.Now()
	base := deriveName(spec.Name, spec.Cwd, id)
	m.mu.Lock()
	// Uniqueness is resolved in the SAME critical section that inserts
	// m.sessions[id] below, so two concurrent spawns can never both win the
	// same label. A session counts as "taken" only if it is not exited (read
	// under its own s.mu, nested inside m.mu — the same m.mu-then-s.mu order
	// locate() already uses to read another session's status): an exited
	// namesake must not force a suffix, which is exactly what lets Restart
	// keep the old name (the old, exited session is still in m.sessions when
	// Spawn runs for the replacement).
	name := uniqueName(base, func(n string) bool {
		for _, other := range m.sessions {
			other.mu.Lock()
			collide := other.name == n && other.status != statusExited
			other.mu.Unlock()
			if collide {
				return true
			}
		}
		return false
	})
	sess := newSession(id, spec.Agent, name, spec.Cwd, created, cmd, ptmx, m.ringCap, m.backend)
	sess.Runtime = runtime
	sess.Box = boxName
	sess.Bin = bin
	sess.Account = acctName
	sess.ConfigDir = cfgDir
	sess.AWS = binding
	sess.GitHub = ghBinding
	sess.Tokens, sess.PATs = tokens, pats
	sess.BrokerNonce = nonce
	sess.EgressNonce = enonce
	sess.BundleDrops = bundleDrops
	sess.Exclusive = exclusive
	sess.onSubmitted = m.submittedHook(id)
	sess.agentKind = isAgentBin(bin) // before publication: no reader can see sess yet
	sess.replyable = replyable
	sess.submitted = submitted
	sess.options = opts
	m.sessions[id] = sess
	sc := m.sidecar
	m.mu.Unlock()

	if sc != nil {
		sc.put(id, sessionMeta{Agent: spec.Agent, Name: name, Cwd: spec.Cwd, Created: created, Runtime: runtime, Box: boxName,
			Replyable: replyable, Submitted: boolPtr(submitted), Options: opts,
			Account: acctName, ConfigDir: cfgDir, AWS: binding, GitHub: ghBinding, Tokens: tokens, PATs: pats, BrokerNonce: nonce, EgressNonce: enonce, BundleDrops: bundleDrops, Exclusive: exclusive})
	}
	m.watchRetention(id, sess)
	return sess, nil
}

// bindingKinds lists the kinds of binding present, in a fixed order.
func bindingKinds(bs config.Bindings) []string {
	var k []string
	if bs.AWS != nil {
		k = append(k, "AWS")
	}
	if bs.GitHub != nil {
		k = append(k, "GitHub")
	}
	if len(bs.Tokens) > 0 {
		k = append(k, "token")
	}
	return k
}

// joinKinds reads a list as prose: "AWS", "AWS and GitHub", "AWS, GitHub and token".
func joinKinds(k []string) string {
	if len(k) <= 1 {
		return strings.Join(k, "")
	}
	return strings.Join(k[:len(k)-1], ", ") + " and " + k[len(k)-1]
}

// bindingKind names a binding set for a refusal: "an AWS binding", "a GitHub
// binding", "a token binding", or for several "AWS and GitHub bindings" /
// "AWS, GitHub and token bindings".
func bindingKind(bs config.Bindings) string {
	switch k := bindingKinds(bs); len(k) {
	case 0:
		// Only an empty set gets here; keep the historical wording.
		return "an AWS binding"
	case 1:
		if k[0] == "AWS" {
			return "an AWS binding"
		}
		return "a " + k[0] + " binding"
	default:
		return joinKinds(k) + " bindings"
	}
}

// bindingNames is bindingKind without the article, for "preparing X access".
func bindingNames(bs config.Bindings) string {
	if k := bindingKinds(bs); len(k) > 0 {
		return joinKinds(k)
	}
	return "AWS"
}

// brokerForget drops a bound session's broker state (cached credentials,
// stats) once it has left the registry. Called after the delete, so the
// broker's lookup already refuses the id.
func (m *Manager) brokerForget(s *Session) {
	if !s.Bindings().Any() {
		return
	}
	m.mu.Lock()
	brk := m.broker
	m.mu.Unlock()
	if brk != nil {
		brk.Forget(s.ID)
	}
}

// brokerRevoke revokes a session's GitHub tokens at GitHub the moment its agent
// has exited, rather than when retention prunes it: nothing is left to use
// them, and they would otherwise live on for up to the retention window. It
// returns at once (the broker revokes in the background) and is a no-op for a
// session without a GitHub binding.
func (m *Manager) brokerRevoke(s *Session) {
	if s.GitHub == nil {
		return
	}
	m.mu.Lock()
	brk := m.broker
	m.mu.Unlock()
	if brk != nil {
		brk.RevokeGitHub(s.ID)
	}
}

// egressForget drops a session's proxy state (counters, open tunnels).
// Like brokerForget it runs after the registry delete, so the proxy's session
// lookup already reports the id as not live: a CONNECT that races the end is
// refused, not re-admitted after the forget.
func (m *Manager) egressForget(s *Session) {
	if s.EgressNonce == "" {
		return
	}
	m.mu.Lock()
	eg := m.egress
	m.mu.Unlock()
	if eg != nil {
		eg.Forget(s.ID)
	}
}

// withoutEnv drops every "NAME=..." entry whose NAME is listed.
func withoutEnv(env, names []string) []string {
	drop := make(map[string]bool, len(names))
	for _, n := range names {
		drop[n] = true
	}
	out := env[:0:0]
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 && drop[kv[:i]] {
			continue
		}
		out = append(out, kv)
	}
	return out
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

// LiveBoxCounts is the registry's view, per box, of sessions that have not
// exited, for the box sweep. A detached session counts: its agent still runs.
func (m *Manager) LiveBoxCounts() map[string]int {
	m.mu.Lock()
	ss := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		ss = append(ss, s)
	}
	m.mu.Unlock()
	out := map[string]int{}
	for _, s := range ss {
		if s.Runtime != config.RuntimeBox || s.Box == "" || s.Exited() {
			continue
		}
		out[s.Box]++
	}
	return out
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
			m.brokerRevoke(sess)
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
		m.peekForget(id) // the cached transcript parse dies with the session
		m.brokerForget(sess)
		m.egressForget(sess)
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
	brk := func() BrokerHooks {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.broker
	}()
	// The critical section is a closure so the unlock is a defer again: it used
	// to be an explicit Unlock (so PeekFor, which takes the same non-reentrant
	// mutex, could run after it), which meant a panic inside s.Info() would
	// leave m.mu held forever and wedge every spawn, kill, get and list for the
	// process lifetime. The closure keeps BOTH properties, and makes "no early
	// return between Lock and Unlock" structural rather than a rule to remember.
	out := func() []Info {
		m.mu.Lock()
		defer m.mu.Unlock()
		o := make([]Info, 0, len(m.sessions))
		for _, s := range m.sessions {
			i := s.Info()
			// Bin is written under m.mu (ResolveBins), so this is the one place
			// it can be read without racing.
			i.Claude = isClaudeBin(s.Bin)
			i.Resumable = i.Exited && resumeFor(s.Runtime, s.Bin, s.claudeSID()) != ""
			o = append(o, i)
		}
		return o
	}()
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	// Summaries and needs are filled AFTER m.mu is released. PeekFor takes the
	// same non-reentrant mutex, so filling them inside the loop above deadlocks
	// the daemon on the first poll.
	for i := range out {
		p, ok, blocked := m.peekAndBlocked(out[i].ID)
		if ok {
			out[i].Summary = summaryLine(p.Text)
		}
		out[i].Activity, out[i].Need, out[i].Ask = needOf(out[i].Activity, out[i].Claude, p, ok, blocked)
		if out[i].Need == "ask" && out[i].Replyable && blocked && ok {
			out[i].AskFull = p.AskFull
		}
		if out[i].AWS != nil && brk != nil {
			until, sso := brk.AWSState(out[i].ID)
			applyAWSState(&out[i], until, sso)
		}
	}
	return out
}

// applyAWSState folds the broker's state into a listed bound session. A
// pending sign-in makes Need "sso" — over "" and "turn", never over "ask",
// whose card has a prompt to answer — for any bound session, claude or not.
// Activity is left alone: the row keeps saying what the agent is doing. An
// exited session shows no write window: nothing can mint with it any more.
func applyAWSState(in *Info, until time.Time, sso string) {
	if in.AWS == nil {
		return
	}
	if !until.IsZero() && !in.Exited {
		u := until
		in.AWS.ElevatedUntil = &u
	}
	in.AWS.SSOExpired = sso
	if sso != "" && in.Need != "ask" {
		in.Need = "sso"
	}
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
	m.removeSession(id, s)
	return nil
}

// removeSession drops a session that is already stopped (killed, or exited and
// being replaced by Restart) from everything the daemon tracks. Shared so the
// teardown list cannot drift between Kill and Restart.
func (m *Manager) removeSession(id string, s *Session) {
	_ = s.ptmx.Close()
	m.mu.Lock()
	delete(m.sessions, id)
	sc := m.sidecar
	m.mu.Unlock()
	m.peekForget(id) // the cached transcript parse dies with the session
	m.brokerForget(s)
	m.egressForget(s)
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
		if md.Exclusive {
			// The sidecar is the only memory of which boxes are exclusive: an
			// adopted box is otherwise shared, so it would idle for the TTL
			// instead of being removed with its session.
			if mx, ok := boxes.(interface{ MarkExclusive(string) }); ok {
				mx.MarkExclusive(md.Box)
			}
		}
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
	sess.Account = md.Account
	sess.ConfigDir = md.ConfigDir
	sess.AWS = md.AWS
	sess.GitHub = md.GitHub
	sess.Tokens, sess.PATs = md.Tokens, md.PATs
	sess.BrokerNonce = md.BrokerNonce
	sess.EgressNonce = md.EgressNonce
	if sess.EgressNonce == "" && md.BrokerNonce != "" {
		// A bound session from before A1 minted its proxy token over BrokerNonce.
		sess.EgressNonce = md.BrokerNonce
	}
	sess.BundleDrops = md.BundleDrops
	sess.Exclusive = md.Exclusive
	// Restored from the sidecar as-is: a pre-feature record decodes Replyable
	// to false (its zero value), so a session can never become repliable by
	// merely surviving a restart. With the kill-switch off the persisted value
	// is dropped in memory only; the sidecar keeps it, so turning replies back
	// on restores the operator's opt-ins.
	sess.replyable = md.Replyable && m.repliesOn
	sess.options = md.Options
	if sessionIDRe.MatchString(md.ClaudeSessionID) { // never trust a path-ish id, even our own file's
		sess.claudeSessionID = md.ClaudeSessionID
	}
	// nil = a record written before the flag existed, or an orphan adopted with
	// no sidecar: restore as submitted (a stale amber beats a hidden waiter).
	sess.submitted = md.Submitted == nil || *md.Submitted
	sess.onSubmitted = m.submittedHook(id)
	sess.agentKind = isAgentBin(bin) // before publication: no reader can see sess yet
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

// uniqueName returns base unchanged if taken(base) is false, else the first
// of base+"-2", base+"-3", ... for which taken returns false. base is
// trimmed rune-safe (like capName) so the suffixed candidate still fits
// within maxNameLen runes — never bytes, so a multibyte base is never sliced
// mid-rune. Any space or hyphen left dangling right where the cut landed is
// also trimmed, so the suffix never reads as "foo -2" or "foo--2" — cosmetic,
// but the candidate is still compared against taken() unchanged from the
// caller's perspective (only the rendered name is tidied). Pure: taken is
// the caller's own "is this name in use" check.
func uniqueName(base string, taken func(string) bool) string {
	if !taken(base) {
		return base
	}
	baseRunes := []rune(base)
	for n := 2; ; n++ {
		suffix := fmt.Sprintf("-%d", n)
		limit := maxNameLen - len([]rune(suffix))
		if limit < 0 {
			limit = 0
		}
		trimmed := baseRunes
		if len(trimmed) > limit {
			trimmed = trimmed[:limit]
		}
		candidate := strings.TrimRight(string(trimmed), " -") + suffix
		if !taken(candidate) {
			return candidate
		}
	}
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
