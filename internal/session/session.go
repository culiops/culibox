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
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/awsscope"
	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/bundle"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credref"
	"github.com/culiops/culibox/internal/launcher"
	"github.com/culiops/culibox/internal/secretstore"
	"github.com/culiops/culibox/internal/sesstoken"
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

// quietActivity is what Info's activity would read for this live session once
// its PTY has gone silent ("waiting" for an agent given a turn, else "idle"),
// and how long the PTY has been silent. regActivity uses the first when the
// registry says the turn has ended, the second to distrust a stale busy.
func (s *Session) quietActivity() (string, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	return activityOf(false, false, 0, time.Time{}, now, idleThreshold, s.agentKind, s.submitted), now.Sub(s.lastActivityAt)
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

// inputMuteMax is the input-mute budget per idleThreshold window: viewer input
// may keep activity muted for at most this long in each window, so at least
// 7 s of every 10 s is unmuted and real work on the PTY fallback always shows,
// however the input is spaced (a per-run cap let input every 1.01 s mute ~99%
// of the time, since each write just past the grace began a new run).
const inputMuteMax = 3 * time.Second

// noteInput mutes activity for viewer input written at now (see Write): up to
// attachRepaintGrace past now, drawn from the current window's budget. The
// window starts at the first input after the previous one ended and lasts
// idleThreshold; a pause does not refill it. It never shortens a longer mute
// already set by a resize or forced redraw.
//
// A write holding '\r' or '\n' is a submitted line, not an echo: it mutes
// nothing and ends the mute its keystrokes started (and any resize mute, which
// at worst reads as work just as the agent starts some), so the line's echo
// and the agent's start of work count. That also makes a second SendMessage
// right after see "working". Caller holds s.mu.
func (s *Session) noteInput(now time.Time, p []byte) {
	if bytes.ContainsAny(p, "\r\n") {
		s.endInputMute()
		return
	}
	if now.Sub(s.inputBudgetStart) >= idleThreshold {
		s.inputBudgetStart, s.inputMuteUsed = now, 0
	}
	from := now
	if s.inputMuteUntil.After(from) {
		from = s.inputMuteUntil // already muted up to here: only the extension costs budget
	}
	grant := now.Add(attachRepaintGrace).Sub(from)
	if left := inputMuteMax - s.inputMuteUsed; grant > left {
		grant = left
	}
	if grant <= 0 {
		return
	}
	s.inputMuteUsed += grant
	s.inputMuteUntil = from.Add(grant)
	if s.inputMuteUntil.After(s.activityMutedUntil) {
		s.activityMutedUntil = s.inputMuteUntil
	}
}

// endInputMute ends any activity mute now (a submitted line or a control
// action: what follows is the agent's reaction). The window's budget is not
// refilled. Caller holds s.mu.
func (s *Session) endInputMute() {
	s.activityMutedUntil = time.Time{}
	s.inputMuteUntil = time.Time{}
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
	// BoxName is the name Wrap will give the box for (cwd, key): the same
	// function, so a caller can prepare something for the box (its broker
	// socket) before it exists. The key must be complete.
	BoxName(cwd string, key box.BoxKey) (string, error)
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

// BypassResolver is implemented by launchers that know whether a spawn runs its
// agent with the bypass option (launcher.AgentLauncher.BypassesPermissions).
// Optional, like BinResolver: without it no box home is asked to pre-accept the
// bypass-permissions warning, and the dialog shows as before.
type BypassResolver interface {
	BypassesPermissions(spec launcher.SpawnSpec) bool
}

// AWSResolver is implemented by launchers that know a profile's AWS binding.
// Optional, like BinResolver: without it no session is ever bound.
type AWSResolver interface {
	AWSFor(agent string) *config.AWSBinding
}

// AWSConnResolver is implemented by launchers that know a profile's named AWS
// connections: deep copies sorted by name. An error is a refusal (a name the
// instance does not define, or a profile-binding rule that no longer holds).
// Optional, like AWSResolver.
type AWSConnResolver interface {
	AWSConnectionsFor(agent string) ([]config.AWSConnection, error)
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

// EgressResolver is implemented by launchers that know a profile's network
// policy: extra allowlist hosts and the open flag. Optional, like AWSResolver:
// without it a session has no policy. It matters only to a bound session.
type EgressResolver interface {
	EgressFor(agent string) (allow []string, open bool)
}

// CloneResolver is implemented by launchers that know a profile's clone spec.
// Optional, like AWSResolver: without it no session is a clone session. A nil,
// nil answer means the profile has no clone_of.
type CloneResolver interface {
	CloneFor(agent string) (*config.Clone, error)
}

// ContextResolver is implemented by launchers that know a profile's
// context_paths. Optional, like CloneResolver: without it no session mounts
// context. A nil, nil answer means the profile has none.
type ContextResolver interface {
	ContextPathsFor(agent string) ([]string, error)
}

// ImportsResolver is implemented by launchers that know a profile's imports_off
// switch. Optional, like ContextResolver: without it the imports are mounted.
type ImportsResolver interface {
	ImportsOffFor(agent string) bool
}

// CloneHooks is what spawning a clone-mode session needs from the clone layer,
// kept as an interface so this package does not import it. A nil CloneHooks
// means clone mode is unavailable, and clone profiles are refused.
type CloneHooks interface {
	// Prepare makes the clone ready on the host (creating it if absent) and
	// returns the box identity and read-only mounts for it. withMemory adds the
	// clone's claude memory mount, which only the default account gets.
	// created reports that this call made the clone (it did not exist before).
	Prepare(ctx context.Context, c config.Clone, withMemory bool) (box.CloneMounts, bool, error)
	// Refresh brings an existing clone up to date with host main before the box
	// starts. Never an error: failures are in the result. release must be
	// called once the new session is published in m.sessions, or the spawn
	// failed; it is never nil.
	Refresh(ctx context.Context, c config.Clone, o RefreshOpts) (CloneInfo, func())
	// BoxOwned reports whether cwd is inside any profile's clone, with the
	// reason a host-tier spawn there is refused.
	BoxOwned(cwd string) (reason string, owned bool)
	// HostOrigin is the host repo's remote.origin.url ("" when unset). It reads
	// `of`, the user's repo, never the box's clone.
	HostOrigin(ctx context.Context, of string) (string, error)
	// ReturnWork brings the clone's branches home (or, with c.NoAutoReturn and a
	// non-"now" trigger, records what is not home). Never an error: failures are
	// in the summary. It takes the clone's lock; with Trigger != ReturnNow it
	// skips ("in-use") when o.LiveSession reports true after the lock is held.
	ReturnWork(ctx context.Context, c config.Clone, o ReturnOpts) ReturnSummary
}

// cloneRefreshTimeout bounds one Refresh. It is best-effort and never fails a
// spawn, so a slow or wedged one is cut off rather than waited for.
const cloneRefreshTimeout = 60 * time.Second

// RefreshOpts is what Spawn tells Refresh about the clone it is about to start.
type RefreshOpts struct {
	Created bool
	// LiveSession reports whether a non-exited boxed session in this manager has
	// a Cwd overlapping the clone dir. The hooks must call it only AFTER taking
	// the per-clone lock: a spawn that holds the lock publishes its session
	// before releasing it, so a call made after waiting for the lock sees it.
	// nil means false.
	LiveSession func() bool
	// ReturnBudget > 0 runs the spawn-time return first, bounded by this.
	ReturnBudget time.Duration
}

// CloneInfo is the outcome of the clone refresh done before a clone-mode box
// started, for the UI. Skipped is why nothing was fetched ("created", "in-use",
// "other-instance", "no-base"). Box output is display-only here: the clone
// layer has already validated and sanitized every string. Immutable once the
// session is published; Info hands out copies.
type CloneInfo struct {
	Refreshed     bool     `json:"refreshed"`
	Skipped       string   `json:"skipped,omitempty"`
	Error         string   `json:"error,omitempty"` // sanitized
	Base          string   `json:"base,omitempty"`  // "main" | "master"
	Branch        string   `json:"branch,omitempty"`
	Ahead         int      `json:"ahead"`
	Behind        int      `json:"behind"`
	FFFrom        string   `json:"ff_from,omitempty"`
	FFTo          string   `json:"ff_to,omitempty"`
	FFCount       int      `json:"ff_count,omitempty"`
	FFRefused     bool     `json:"ff_refused,omitempty"`
	HeadInHost    *bool    `json:"head_in_host,omitempty"` // absent = unknown
	StaleBranches []string `json:"stale_branches,omitempty"`
	Rewritten     bool     `json:"rewritten,omitempty"`
	Unverified    bool     `json:"unverified,omitempty"`
	// PushRepo is "owner/repo" when the GitHub binding can push to the clone's
	// origin, so a plain `git push` in the box reaches GitHub.
	PushRepo string `json:"push_repo,omitempty"`
	// Return is the spawn-time return that ran before this refresh.
	Return *ReturnSummary `json:"return,omitempty"`
}

// copy returns an independent CloneInfo (own slice, own pointer), nil for nil.
func (c *CloneInfo) copy() *CloneInfo {
	if c == nil {
		return nil
	}
	out := *c
	out.StaleBranches = slices.Clone(c.StaleBranches)
	out.Return = c.Return.copy()
	if c.HeadInHost != nil {
		v := *c.HeadInHost
		out.HeadInHost = &v
	}
	return &out
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

// withGitPushRewrite routes `git push` of the clone's origin (a host path) to
// GitHub: GIT_CONFIG_COUNT grows by one and a pushInsteadOf pair is appended at
// the new index, so fetches still use the local path. Any pair already at that
// index is dropped first. A missing or non-numeric count returns env unchanged
// (fail closed: no rewrite). env itself is never modified.
//
// pushInsteadOf is a prefix match, so of=/a/x also rewrites an origin /a/x2 to
// a repository that is not bound. The broker refuses that, so it is harmless.
func withGitPushRewrite(env []string, of, ownerRepo string) []string {
	n := -1
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GIT_CONFIG_COUNT="); ok {
			c, err := strconv.Atoi(v)
			if err != nil || c < 0 {
				return env
			}
			n = c
			break
		}
	}
	if n < 0 {
		return env
	}
	idx := strconv.Itoa(n)
	out := withoutEnv(env, []string{"GIT_CONFIG_KEY_" + idx, "GIT_CONFIG_VALUE_" + idx})
	for i, kv := range out {
		if strings.HasPrefix(kv, "GIT_CONFIG_COUNT=") {
			out[i] = "GIT_CONFIG_COUNT=" + strconv.Itoa(n+1)
		}
	}
	return append(out,
		"GIT_CONFIG_KEY_"+idx+"=url.https://github.com/"+ownerRepo+".git.pushInsteadOf",
		"GIT_CONFIG_VALUE_"+idx+"="+of)
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
	// base is the spawn's base-role probe results (nil without AWS), which the
	// box's aws.json carries per connection.
	BoxFor(bs config.Bindings, base map[string]awsprobe.Result) (mounts []box.Mount, env []string, err error)
	// AcquireBox takes a reference on the named box's own broker socket and
	// returns the read-only mount that carries it into the box (none on macOS,
	// where the box is identified per request). Every call that returns a nil
	// error is paired with exactly one ReleaseBox: on every spawn failure after
	// it, and when the session ends.
	AcquireBox(boxName string) ([]box.Mount, error)
	// AdoptBox is AcquireBox for a live session restored after a daemon restart:
	// the socket returns in the directory the running box already mounts. Paired
	// with ReleaseBox like AcquireBox.
	AdoptBox(boxName string) error
	// ReleaseBox drops one reference; at zero the box's socket closes.
	ReleaseBox(boxName string)
	// ProbeBase checks, at spawn, whether each bound AWS base role can write.
	// The map is keyed "" for the legacy binding, else by connection name, and
	// never nil. A warning, never a gate: any failure is an unverified result.
	ProbeBase(sessionID, agent string, bs config.Bindings) map[string]awsprobe.Result
	// WatchBase asks for a re-probe of the session's keys whose result is not
	// definite, after their next successful base mint. Every watched session
	// must eventually be Forget-ed.
	WatchBase(sessionID string, keys []string)
	// SetBaseNotify installs the sink a re-probe result is delivered to.
	SetBaseNotify(f func(sessionID, key string, r awsprobe.Result))
	// WriteAWSListing rewrites the binding dir's aws.json with new base-role
	// results; a no-op without named connections.
	WriteAWSListing(bs config.Bindings, base map[string]awsprobe.Result) error
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
	// session's write window ends (zero = none), the connection it is for (""
	// = the legacy binding), and the sign-in a human must renew on the host
	// ("" = none). Read-only; it takes only the broker's lock.
	AWSState(sessionID string) (elevatedUntil time.Time, conn string, ssoExpired string)
	// AWSConnState maps each named connection waiting on a human sign-in to
	// its sign-in key. Read-only; it takes only the broker's lock.
	AWSConnState(sessionID string) map[string]string
	// StoreLockedFor reports whether the session's last read of the encrypted
	// store was refused because it is locked. Read-only; broker lock only.
	StoreLockedFor(sessionID string) bool
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
	// Mode is "sso_direct" when the agent holds the SSO permission set's own
	// credentials; absent for the usual assumed agent role. Hours is how long
	// those credentials were valid at spawn, rounded up; absent otherwise.
	Mode  string `json:"mode,omitempty"`
	Hours int    `json:"hours,omitempty"`
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
	// Fields is the base role's write probe (base_access and friends).
	awsprobe.Fields
}

// AWSConnInfo is the wire view of one of a session's named AWS connections:
// what it can reach, never how it authenticates (no source profile, no
// nonce, no token).
type AWSConnInfo struct {
	Name      string `json:"name"`
	Label     string `json:"label"`
	AccountID string `json:"account_id"`
	RoleARN   string `json:"role_arn"`
	Region    string `json:"region"`
	Prod      bool   `json:"prod"`
	// Mode and Hours as in AWSInfo.
	Mode  string `json:"mode,omitempty"`
	Hours int    `json:"hours,omitempty"`
	// WriteCapable: the connection names an elevate role, so a human may widen
	// it to write.
	WriteCapable bool         `json:"write_capable"`
	Elevate      *ElevateInfo `json:"elevate,omitempty"`
	// ElevatedUntil and SSOExpired are filled by List only (the broker owns the
	// state); absent otherwise.
	ElevatedUntil *time.Time `json:"elevated_until,omitempty"`
	SSOExpired    string     `json:"sso_expired,omitempty"`
	// Fields is the base role's write probe (base_access and friends).
	awsprobe.Fields
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
	// The two opt-in prefixes (#211, #302), shown on the grant panel so the
	// user sees what a window can reach. Not secrets; omitted when unset.
	BoxBucketPrefix string `json:"box_bucket_prefix,omitempty"`
	PassRolePrefix  string `json:"pass_role_prefix,omitempty"`
}

// elevateInfo is the wire view of an elevate block; nil for none.
func elevateInfo(e *config.AWSElevate) *ElevateInfo {
	if e == nil {
		return nil
	}
	return &ElevateInfo{RoleARN: e.RoleARN, MaxMinutes: e.Max(), AllowIAM: e.AllowIAM,
		BoxBucketPrefix: e.BoxBucketPrefix, PassRolePrefix: e.PassRolePrefix}
}

// Bundles supplies the pinned agent-config bundle for a boxed session. Nil means
// no bundle: sessions still run, just without user config.
type Bundles interface {
	// For returns the read-only mounts a box carries (the same for every bin,
	// because sessions of different bins share a box) and the extra argv flags
	// bin's agent needs, plus the bundle hash for the log line. A bin with no
	// layout (a shell, or "" when unresolved) gets the mounts and no flags.
	For(bin string, withImports bool) (bundle.Grant, error)
	// SessionMCP builds the MCP config of a boxed claude session that binds token
	// entries declaring MCP servers: the bundle's servers (when includeUser), the
	// entries' and the image's. drops is the bundle grant's; the result's Drops
	// is what to report.
	SessionMCP(includeUser bool, drops []bundle.Drop, entries []bundle.EntryServer) (bundle.SessionMCP, error)
	// SessionMCPMount and WriteSessionMCP carry a session config that holds the
	// user's personal servers (includeUser). It must not go in the binding dir,
	// which every account's box with that binding mounts, so it has a directory
	// of its own that only a box of an inheriting account mounts. bindingKey is
	// the binding's hash.
	SessionMCPMount(bindingKey string) (box.Mount, error)
	WriteSessionMCP(bindingKey, key string, doc []byte) (boxPath string, err error)
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
	// un-raised value. In List the agent's own live status, when it reports
	// one, decides it (regActivity).
	Activity string `json:"activity"`
	// ActivitySource says what decided Activity for a live session in List:
	// "agent" (the agent's own status report) or "pty" (the PTY-silence
	// fallback, which output blips can flip; the client debounces only it).
	ActivitySource string `json:"activity_source,omitempty"`
	// ReplyReady is whether SendMessage would accept a reply right now (the
	// same replyGate), so the UI offers exactly the sends the server takes.
	// Filled only in List; absent elsewhere, so a client can tell "not ready"
	// from "an older server that does not say".
	ReplyReady *bool  `json:"reply_ready,omitempty"`
	Name       string `json:"name"`              // mutable display label (id is the stable key)
	Runtime    string `json:"runtime,omitempty"` // box | host
	// ParentID is the session this one is a companion shell of ("" = top-level).
	ParentID string `json:"parent_id,omitempty"`
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
	// CostUSD is the summed analytics cost of every conversation this session
	// has had since spawn. Present only for a claude session whose whole
	// conversation history is known (costComplete) while a cost source is set:
	// a partial history would understate it, and an understated price is a
	// guess. Present and 0 means no cost has been ingested yet (ingest lags the
	// transcript), not that the session is free. Filled by List only.
	CostUSD *float64 `json:"cost_usd,omitempty"`
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
	// Attention is how the session asks for the operator, one word for the
	// desktop sidebar's groups: "block" (blocked on you), "done" (finished its
	// turn) or empty. Derived from Exited, Activity and Need (see attentionOf),
	// so it is provider-neutral and never set on an exited session. Filled by
	// List only, after every raise above has settled.
	Attention string `json:"attention,omitempty"`
	// Resumable: an exited claude (host, or a non-exclusive box) whose
	// conversation id is known — Restart will try to continue it (`--resume`);
	// for a boxed one that still depends on the transcript being in the box at
	// restart time. Filled by List only.
	Resumable bool `json:"resumable,omitempty"`
	// ResumeSkipped is set on the NEW session of a boxed restart that had to
	// start a fresh conversation because the old one's transcript was not in
	// the box's home. The UI turns it into one toast.
	ResumeSkipped string `json:"resume_skipped,omitempty"`
	// Account is the agent account the session runs under; empty for the
	// default account, so a default session's wire shape is unchanged.
	Account string `json:"account,omitempty"`
	// AWS is the binding the session was spawned with; nil (absent) for an
	// unbound session, so its wire shape is unchanged.
	AWS *AWSInfo `json:"aws,omitempty"`
	// AWSConnections are the named AWS connections the session was spawned
	// with, sorted by name; absent without any, so a legacy or unbound
	// session's wire shape is unchanged.
	AWSConnections []AWSConnInfo `json:"aws_connections,omitempty"`
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
	// Clone is what refreshing the clone before this spawn did. Clone-mode
	// sessions only; absent otherwise.
	Clone *CloneInfo `json:"clone,omitempty"`
	// Return is the latest exit or "Bring work home now" return of a clone-mode
	// session; absent until one has run.
	Return *ReturnSummary `json:"return,omitempty"`
	// ContextPaths are the host paths the box can read (the profile's
	// context_paths at spawn, mounted read-only at the same path); CloneOf is
	// the host repo a clone-mode session was cloned from. Both are snapshots
	// taken at spawn: a later profile edit never changes a running session's.
	// Absent without them, so a plain session's wire shape is unchanged.
	ContextPaths []string `json:"context_paths,omitempty"`
	CloneOf      string   `json:"clone_of,omitempty"`
	// LockedFiles are the workspace files the box layer keeps locked on the
	// host while this session's box runs (macOS on a case-insensitive volume,
	// where the box could otherwise change a guarded file through another
	// letter case), as of the spawn. Absent without any.
	LockedFiles []string `json:"locked_files,omitempty"`
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
	// ParentID is the session this is a companion shell of. Set before
	// publication and never mutated, so read without a lock.
	ParentID string
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
	// AWSConns are the named AWS connections the session was spawned with
	// (deep copies, sorted by name); exclusive with AWS. Immutable and read
	// without the lock, like AWS: a later edit or removal in aws_connections
	// never changes a running session's scope.
	AWSConns []config.AWSConnection
	// GitHub is the GitHub binding the session was spawned with; immutable,
	// read without the lock, like AWS.
	GitHub *config.GitHubBinding
	// Tokens are the static-token entries the session was spawned with and PATs
	// the entries its GitHub owners use as personal access tokens (deep copies,
	// sorted by name). Immutable and read without the lock, like AWS: a later
	// edit to static_tokens never changes a running session's scope.
	Tokens, PATs []config.StaticToken
	// EgressAllow and EgressOpen are the profile's network policy as spawned
	// (see config.Bindings). Immutable and read without the lock.
	EgressAllow []string
	EgressOpen  bool
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
	// Clone is the clone refresh result for a clone-mode session. Immutable
	// after publication, read without the lock; Info hands out a deep copy.
	Clone *CloneInfo
	// ret is the latest exit or "now" return, returning that a "now" is running.
	// Both guarded by mu; ret is not persisted (a restart drops it, the clone's
	// state file keeps the truth).
	ret       *ReturnSummary
	returning bool
	// ContextPaths and CloneOf are the spawn-time snapshots Info reports (see
	// Info.ContextPaths). Immutable after publication, read without the lock;
	// Info hands out a copy.
	ContextPaths []string
	CloneOf      string
	// LockedFiles is Info.LockedFiles; immutable after publication.
	LockedFiles []string
	// Exclusive: this session's box is its own (an elevate binding). Immutable;
	// the broker refuses a grant without it.
	Exclusive bool

	// base is the base-role probe result per AWS binding key ("" = the legacy
	// binding, else the connection name). Guarded by mu, unlike the binding
	// fields: a re-probe changes it. nil for a record from before the probe,
	// which reads as "not checked".
	base map[string]awsprobe.Result
	// sso is what the sso_direct gate left per AWS binding key at spawn (hours
	// of validity, the account). Fixed before the session is published, so it
	// needs no lock; nil without an sso_direct binding.
	sso map[string]ssoSpawnInfo
	// persistMu serializes a re-probe's snapshot of base with its writes to
	// the sidecar and aws.json, so two re-probes landing together never
	// persist an older snapshot last. Outermost: taken with no other lock held.
	persistMu sync.Mutex

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
	// claudeSessionIDs is every conversation id noted since spawn, oldest
	// first, at most maxConversationIDs; costComplete says it is the whole
	// history (see sessionMeta.CostComplete). Guarded by mu.
	claudeSessionIDs []string
	costComplete     bool
	// restoredUnchecked is set when the session was re-attached after a daemon
	// restart and no registry read has happened since: the first sid noted
	// decides whether a /clear slipped by while nobody was watching. Memory
	// only; guarded by mu.
	restoredUnchecked bool
	// convMu orders the sidecar writes of the three fields above (and
	// claudeSessionID): taken before the snapshot, held across the write.
	convMu sync.Mutex
	// cwdFormsCache is cwdFormsOf(Cwd), filled on first use (cwdForms); Cwd is
	// immutable, so it is never invalidated. Guarded by mu.
	cwdFormsCache []string
	// resumeSkipped: see Info.ResumeSkipped. Set before publication, immutable.
	resumeSkipped string
	// restarting latches the first Restart of an exited session, so a double
	// click cannot start two replacements. Guarded by mu.
	restarting bool

	// brokerBox is the box whose broker socket this session holds a reference on
	// ("" = none: an unbound session, or a restored one whose adopt failed, which
	// therefore never releases another session's reference). Set before
	// publication and never mutated.
	brokerBox string
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

	// sizeMu orders every PTY size change (Resize, ForceRedraw and its restore)
	// with the size it records; taken before mu, never inside it. redrawSeq
	// (guarded by sizeMu) names the latest ForceRedraw, the only one that restores.
	sizeMu    sync.Mutex
	redrawSeq uint64

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
	inputBudgetStart   time.Time     // start of the current input-mute budget window (noteInput; mu)
	inputMuteUsed      time.Duration // input mute granted in that window (noteInput; mu)
	inputMuteUntil     time.Time     // end of the latest input mute (noteInput; mu)
	exitCode           int           // agent exit code; meaningful only when exitCodeKnown
	exitCodeKnown      bool          // true only for ephemeral sessions (cmd IS the agent)
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
//
// A write without Enter also mutes activity for attachRepaintGrace (budgeted
// per window, see noteInput): a TUI repaints within milliseconds of a
// focus/mouse report or a keystroke (measured, #77), and that echo is the
// viewer's doing, not agent work. The mute is set BEFORE the bytes reach the
// PTY so the echo cannot land ahead of it. A submitted line is never muted, so
// the agent's start of work counts at once. A live registry status overrides
// all of this (regActivity); the mute matters for the PTY fallback.
func (s *Session) Write(p []byte) (int, error) { return s.write(p, false) }

// writeAction writes a control action's key (Interrupt's Esc/Ctrl+C, Answer's
// 1/Esc). Like a submitted line it is never muted and ends any input mute:
// the agent's reaction to it is real activity.
func (s *Session) writeAction(p []byte) (int, error) { return s.write(p, true) }

func (s *Session) write(p []byte, action bool) (int, error) {
	s.mu.Lock()
	if action {
		s.endInputMute()
	} else {
		s.noteInput(time.Now(), p)
	}
	s.mu.Unlock()
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

// Resize resizes the PTY and records the size for ForceRedraw. sizeMu holds
// the record and the ioctl together, so a delayed restore cannot set an older
// size between them.
func (s *Session) Resize(rows, cols uint16) error {
	s.sizeMu.Lock()
	defer s.sizeMu.Unlock()
	s.mu.Lock()
	s.rows, s.cols = rows, cols
	s.muteActivity(time.Now())
	s.mu.Unlock()
	return setPTYSize(s.ptmx, rows, cols)
}

// redrawGap is how long ForceRedraw holds its nudged size. MEASURED (#126):
// Claude Code reads its size when its event loop reaches the SIGWINCH, so a
// nudge undone at once was read as "unchanged" and nothing repainted; a 10 ms
// hold gave a full repaint. 100 ms leaves room for abduco's or docker exec's
// extra hop and an agent whose event loop is busy. A var only for tests.
var redrawGap = 100 * time.Millisecond

// ForceRedraw makes a TUI repaint over the best-effort replay on attach: the
// PTY goes one column narrower for redrawGap, then back. Narrower, so the frame
// painted in between fits the viewer's grid instead of wrapping. The restore
// runs on a timer (the caller's read loop must not sleep) and sets the size
// recorded when it fires, so a resize during the hold wins. Only the latest
// redraw restores: an earlier timer must not cut a later hold short. The one
// activity mute covers both repaints (attachRepaintGrace > redrawGap).
func (s *Session) ForceRedraw() {
	s.sizeMu.Lock()
	defer s.sizeMu.Unlock()
	s.mu.Lock()
	r, c := s.rows, s.cols
	s.muteActivity(time.Now())
	s.mu.Unlock()
	if c < 2 {
		return
	}
	s.redrawSeq++
	seq := s.redrawSeq
	_ = setPTYSize(s.ptmx, r, c-1)
	time.AfterFunc(redrawGap, func() {
		s.sizeMu.Lock()
		defer s.sizeMu.Unlock()
		if s.redrawSeq != seq {
			return
		}
		s.mu.Lock()
		r, c := s.rows, s.cols
		s.mu.Unlock()
		_ = setPTYSize(s.ptmx, r, c)
	})
}

// setPTYSize sets the PTY size through the file's own reference. pty.Setsize
// reads the raw fd number (f.Fd()) with no hold on it; a restore that fires
// while the session closes could race the close or reach a PTY that reused the
// number. Control holds the fd for the ioctl and fails on a closed file.
func setPTYSize(f *os.File, rows, cols uint16) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ierr error
	if err := rc.Control(func(fd uintptr) {
		ierr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: cols})
	}); err != nil {
		return err
	}
	return ierr
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
	in := Info{ID: s.ID, Agent: s.Agent, Cwd: s.Cwd, Created: s.Created, Exited: s.exited, Status: s.status, Activity: activity, Name: s.name, Runtime: s.Runtime, ParentID: s.ParentID, Attached: s.sub != nil, Replyable: s.replyable, Options: s.options, Account: s.Account, ResumeSkipped: s.resumeSkipped}
	in.Egress = EgressMode(s.EgressNonce != "", s.Bindings().Any() && !s.EgressOpen)
	in.BundleDrops = slices.Clone(s.BundleDrops)
	in.Clone = s.Clone.copy()
	in.Return = s.ret.copy()
	in.ContextPaths = slices.Clone(s.ContextPaths)
	in.CloneOf = s.CloneOf
	in.LockedFiles = slices.Clone(s.LockedFiles)
	if s.AWS != nil {
		in.AWS = &AWSInfo{Label: s.AWS.Label, AccountID: awsscope.AccountOf(s.AWS.RoleARN), RoleARN: s.AWS.RoleARN, Prod: s.AWS.Prod,
			Elevate: elevateInfo(s.AWS.Elevate), Fields: awsprobe.Lookup(s.base, "").Fields()}
		if s.AWS.SSODirect() {
			in.AWS.Mode, in.AWS.Hours, in.AWS.AccountID = config.AWSModeSSODirect, s.sso[""].Hours, s.sso[""].AccountID
		}
	}
	for _, c := range s.AWSConns {
		ci := AWSConnInfo{Name: c.Name, Label: c.Label, AccountID: awsscope.AccountOf(c.RoleARN),
			RoleARN: c.RoleARN, Region: c.Region, Prod: c.Prod, WriteCapable: c.Elevate != nil, Elevate: elevateInfo(c.Elevate),
			Fields: awsprobe.Lookup(s.base, c.Name).Fields()}
		if c.SSODirect() {
			ci.Mode, ci.Hours, ci.AccountID = config.AWSModeSSODirect, s.sso[c.Name].Hours, s.sso[c.Name].AccountID
		}
		in.AWSConnections = append(in.AWSConnections, ci)
	}
	slices.SortFunc(in.AWSConnections, func(a, b AWSConnInfo) int { return strings.Compare(a.Name, b.Name) })
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

// EgressMode is the network posture a session reports (callers pass bound=false
// for an egress_open profile, which is not limited to an allowlist): "allowlist" (egress
// control on and at least one credential binding: only its hosts), "open"
// (egress control on, unbound: any host through the authenticated proxy) or ""
// (no egress control). Info and the profile list both use it, so a profile
// previews exactly what its session will show.
func EgressMode(egress, bound bool) string {
	switch {
	case egress && bound:
		return "allowlist"
	case egress:
		return "open"
	}
	return ""
}

// BaseAccess is a copy of the session's base-role probe results.
func (s *Session) BaseAccess() map[string]awsprobe.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.base)
}

// awsKeys are the session's base-role probe keys: "" for the legacy binding,
// else the connection names; none without AWS. Immutable fields, no lock.
func (s *Session) awsKeys() []string {
	return awsKeysOf(s.Bindings())
}

func awsKeysOf(bs config.Bindings) []string {
	if bs.AWS != nil {
		return []string{""}
	}
	keys := make([]string, 0, len(bs.AWSConns))
	for _, c := range bs.AWSConns {
		keys = append(keys, c.Name)
	}
	return keys
}

// Bindings is the set of credential bindings the session was spawned with.
// Immutable, so it needs no lock.
func (s *Session) Bindings() config.Bindings {
	return config.Bindings{AWS: s.AWS, AWSConns: s.AWSConns, GitHub: s.GitHub, Tokens: s.Tokens, PATs: s.PATs,
		EgressAllow: s.EgressAllow, EgressOpen: s.EgressOpen}
}

// claudeSID returns the last validated Claude conversation id ("" if none).
func (s *Session) claudeSID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claudeSessionID
}

// conversations returns a copy of every conversation id this session has had
// since spawn and whether that list is the whole history.
func (s *Session) conversations() (ids []string, complete bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.claudeSessionIDs), s.costComplete
}

// addConversationLocked appends sid to the history unless it is already there.
// A sid that fails sessionIDRe, or one past maxConversationIDs, is not
// recorded and leaves the history incomplete. It reports whether anything
// changed. s.mu must be held.
func (s *Session) addConversationLocked(sid string) (changed bool) {
	if slices.Contains(s.claudeSessionIDs, sid) {
		return false
	}
	if !sessionIDRe.MatchString(sid) || len(s.claudeSessionIDs) >= maxConversationIDs {
		changed = s.costComplete
		s.costComplete = false
		return changed
	}
	s.claudeSessionIDs = append(s.claudeSessionIDs, sid)
	return true
}

// inheritConversations folds a predecessor's history in front of this
// session's own (Restart: the replacement is a new session that resumes the
// old conversation). It is complete only if both were. It returns the merged
// state for the caller to persist.
func (s *Session) inheritConversations(ids []string, wasComplete bool) ([]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	own := s.claudeSessionIDs
	s.claudeSessionIDs = nil
	s.costComplete = s.costComplete && wasComplete
	for _, sid := range slices.Concat(ids, own) {
		s.addConversationLocked(sid)
	}
	return slices.Clone(s.claudeSessionIDs), s.costComplete
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
	// idMark is the highest id number ever issued per agent, persisted at
	// idStatePath ("" = not persisted); see idfloor.go.
	idMark      map[string]int
	idStatePath string
	// storeLocked reports whether the encrypted store is locked right now.
	// Nil = no store: no session ever needs it unlocked.
	storeLocked func() bool
	egress      EgressHooks // nil = egress control not running; bound profiles are refused
	egressAll   bool        // every boxed spawn needs egress; see SetEgressAll
	clone       CloneHooks  // nil = clone mode unavailable; clone profiles are refused
	home        string      // daemon user's home; where a host session's ~/.claude lives
	stateDir    string      // data dir; a boxed session's ~/.claude lives under <stateDir>/boxstate/<box>
	costSrc     CostSource  // prices a session's conversations (analytics); nil = off, no session reports a cost
	repliesOn   bool        // config replies_enabled kill-switch; false = no reply, answer or opt-in

	// contextCheck is the on-disk check of one context path (existence,
	// symlinks, sockets, protected dirs). Nil = it cannot run, so a profile
	// with context_paths is refused rather than mounted unchecked.
	// It returns the path's host identity (box.CheckContextPathID), read by the
	// same walk that refused symlinks. Nil on macOS: a Mac inode number is not
	// the one the box sees, so the box layer reads the identity inside the VM
	// instead (box.Mount.CheckID, box.Options.VMIDs).
	contextCheck func(path string) (*box.FileID, error)

	// boxedRecorder persists a workspace as box-written before a boxed Wrap
	// starts a box in it. Nil = nothing is recorded (tests, hosts without
	// containment).
	boxedRecorder func(cwd string) error

	// peekMu guards peekCache. It is SEPARATE from m.mu on purpose: PeekFor is
	// called from List after m.mu is released, and sharing the mutex would make
	// that ordering fragile.
	peekMu    sync.Mutex
	peekCache map[string]peekEntry

	// usageMu guards usageWide, UsageFor's cache of the widened (multi-MB)
	// transcript read, one entry per session, dropped by peekForget. Separate
	// from m.mu for the same reason as peekMu.
	usageMu   sync.Mutex
	usageWide map[string]usageWideEntry

	// timelineMu guards timelines, the per-session incremental activity
	// parses (timeline_cache.go). Like peekMu it is separate from m.mu and
	// never taken while m.mu is held.
	timelineMu sync.Mutex
	timelines  map[string]*timelineState

	// procStart reads a pid's starttime for the registry's pid-reuse guard.
	// NewManager sets procStartOf; a nil value (struct-literal test managers)
	// disables the registry path, leaving the session unbound.
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

// SetBroker installs the credential-broker hooks and registers the re-probe
// sink with them. Call once at boot, before Reconcile.
func (m *Manager) SetBroker(h BrokerHooks) {
	m.mu.Lock()
	m.broker = h
	m.mu.Unlock()
	if h != nil {
		h.SetBaseNotify(m.baseProbed) // outside m.mu: the broker takes its own lock
	}
}

// CostSource prices conversations by their Claude conversation id. The
// analytics service implements it; it is an interface so this package does not
// import analytics.
type CostSource interface {
	CostOf(sids []string) float64
}

// SetCostSource sets where List reads each session's cost. A nil interface, or
// a typed nil (a nil *Service stored in the interface, which compares non-nil
// and would panic on first use), means "no source".
func (m *Manager) SetCostSource(c CostSource) {
	if c != nil {
		if v := reflect.ValueOf(c); v.Kind() == reflect.Pointer && v.IsNil() {
			c = nil
		}
	}
	m.mu.Lock()
	m.costSrc = c
	m.mu.Unlock()
}

// SetStoreLocked installs the store's current-state probe. A listed session the
// broker saw refused for a locked store needs the operator only while this still
// says locked. Call once at boot.
func (m *Manager) SetStoreLocked(f func() bool) {
	m.mu.Lock()
	m.storeLocked = f
	m.mu.Unlock()
}

// baseProbed is the broker's re-probe sink: a watched base role was probed
// again after a successful mint. It updates the session, its sidecar entry and,
// for a session with named connections, the binding dir's aws.json (the legacy
// binding has none). The broker calls it with its lock released, possibly
// concurrently for two keys of one session, and possibly racing the session's
// removal: an unknown id or key is ignored.
func (m *Manager) baseProbed(id, key string, r awsprobe.Result) {
	m.mu.Lock()
	s := m.sessions[id]
	sc := m.sidecar
	brk := m.broker
	m.mu.Unlock()
	if s == nil || !slices.Contains(s.awsKeys(), key) {
		return
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	if s.base == nil { // a record from before the probe: the very sessions that are watched
		s.base = map[string]awsprobe.Result{}
	}
	s.base[key] = r
	snap := maps.Clone(s.base)
	s.mu.Unlock()
	if sc != nil {
		sc.setBaseAccess(id, snap)
	}
	if brk != nil && len(s.AWSConns) > 0 {
		if err := brk.WriteAWSListing(s.Bindings(), snap); err != nil {
			log.Printf("session %s: aws base-role re-probe: rewriting aws.json: %v", id, err)
		}
	}
	log.Printf("session %s: aws base-role re-probe %q → %s", id, key, r.Normalize().State)
}

// SetEgress installs the egress-proxy hooks. Call once at boot, with SetBroker.
func (m *Manager) SetEgress(h EgressHooks) {
	m.mu.Lock()
	m.egress = h
	m.mu.Unlock()
}

// SetEgressAll makes egress mandatory for every boxed spawn, bound or not: an
// unbound box gets the same proxy env in open mode, and is refused when egress
// control is not running. Call once at boot, with SetEgress.
func (m *Manager) SetEgressAll(all bool) {
	m.mu.Lock()
	m.egressAll = all
	m.mu.Unlock()
}

// SetClone installs the clone-mode hooks. Call once at boot, with
// SetBoxes. Nil means clone mode is unavailable: clone profiles are refused.
func (m *Manager) SetClone(h CloneHooks) {
	m.mu.Lock()
	m.clone = h
	m.mu.Unlock()
}

// SetContextCheck installs the per-path check a spawn runs on every
// context_paths entry, the same one a profile save runs. Call once at boot.
// Nil means context_paths cannot be checked and such profiles are refused.
func (m *Manager) SetContextCheck(f func(path string) (*box.FileID, error)) {
	m.mu.Lock()
	m.contextCheck = f
	m.mu.Unlock()
}

// SetBoxedRecorder installs the hook Spawn calls with spec.Cwd right before a
// boxed Wrap. The record must be durable before the box can write anything: a
// workspace the host does not know a box wrote in is one the fs API would treat
// as host-owned. An error fails the spawn ("record boxed workspace: ...") and
// Wrap is not called. Call once at boot. Nil records nothing.
func (m *Manager) SetBoxedRecorder(f func(cwd string) error) {
	m.mu.Lock()
	m.boxedRecorder = f
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
		idMark:    map[string]int{},
		ringCap:   ringCap,
		retention: retention,
		procStart: procStartOf,
	}
}

// Spawn builds the agent command and starts a session — under the persistence
// backend (abduco) if present, else a daemon-owned PTY directly.
func (m *Manager) Spawn(spec launcher.SpawnSpec) (*Session, error) {
	// Every start funnels through here (the API, Restart, a clone, a shell), so
	// this is where a launcher that cannot vouch for its profiles stops them all
	// before anything is prepared (#291).
	if r, ok := m.launcher.(interface{ SpawnRefusal() error }); ok {
		if err := r.SpawnRefusal(); err != nil {
			return nil, err
		}
	}
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
	var awsConns []config.AWSConnection
	if r, ok := m.launcher.(AWSConnResolver); ok {
		var err error
		awsConns, err = r.AWSConnectionsFor(spec.Agent)
		if err != nil {
			// ErrBoxUnavailable so the UI shows which profile and why.
			return nil, fmt.Errorf("%w: %w", box.ErrBoxUnavailable, err)
		}
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
	var egressAllow []string
	var egressOpen bool
	if r, ok := m.launcher.(EgressResolver); ok {
		egressAllow, egressOpen = r.EgressFor(spec.Agent)
	}
	bs := config.Bindings{AWS: binding, AWSConns: awsConns, GitHub: ghBinding, Tokens: tokens, PATs: pats,
		EgressAllow: egressAllow, EgressOpen: egressOpen}
	if !bs.Any() {
		// No binding: the box is open already, so the policy has no effect and
		// must not split the box key or the binding hash.
		egressAllow, egressOpen = nil, false
		bs.EgressAllow, bs.EgressOpen = nil, false
	}
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
	// context_paths are read-only mounts of host paths, which only a box has:
	// a host session reads any path already. The paths are re-validated purely
	// on every spawn (a hand-edited profiles.json), the disk check runs below.
	var ctxPaths []string
	var lockedFiles []string // what the box layer locked on the host (macOS), set after Wrap
	if r, ok := m.launcher.(ContextResolver); ok {
		p, err := r.ContextPathsFor(spec.Agent)
		if err != nil {
			return nil, fmt.Errorf("%w: profile %q: %w", box.ErrBoxUnavailable, spec.Agent, err)
		}
		ctxPaths = p
	}
	var importsOff bool
	if r, ok := m.launcher.(ImportsResolver); ok {
		importsOff = r.ImportsOffFor(spec.Agent)
	}
	if len(ctxPaths) > 0 && runtime != config.RuntimeBox {
		// ErrUnboxableWorkspace so the UI shows the refusal (writeSpawnErr).
		return nil, fmt.Errorf("%w: profile %q: %w", box.ErrUnboxableWorkspace, spec.Agent, config.ErrContextNeedsBox)
	}
	// Both checks compare an absolute path: a relative cwd never compares as
	// under an absolute clone dir, yet the process resolves it against the
	// daemon's own cwd. Fail closed when it cannot be made absolute.
	absCwd, absErr := filepath.Abs(spec.Cwd)
	// Everything below (the box, the boxed record, the process, the saved session)
	// uses the path these checks saw. filepath.Abs cleans `..` lexically against
	// the logical cwd; the kernel would resolve a raw `..` from the PHYSICAL cwd,
	// so through a symlinked directory (macOS /tmp and /var, a symlinked home) the
	// raw spelling names another directory (#313). "" stays "": the daemon's own
	// cwd is not a workspace to record.
	if spec.Cwd != "" && absErr == nil {
		spec.Cwd = absCwd
	}
	switch {
	case cl != nil:
		if runtime != config.RuntimeBox {
			// ErrUnboxableWorkspace so the UI shows the refusal (writeSpawnErr).
			return nil, fmt.Errorf("%w: profile %q is a clone-mode profile, so it can only run in a box, never on the host tier",
				box.ErrUnboxableWorkspace, spec.Agent)
		}
		if ch == nil {
			return nil, fmt.Errorf("%w: clone mode is not available on this instance", box.ErrBoxUnavailable)
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

	// checkContext refuses a context path that is unsafe now: deleted, swapped
	// for a symlink or made unsafe since the profile was saved, or overlapping
	// the workspace (already mounted, and a read-only mount over part of a
	// writable one would shadow it). The message names the path, so it never
	// surfaces as a docker bind error. It runs before Prepare (cheap refusal,
	// before the box VM starts) and again right before Wrap, to narrow the window
	// between the check and the mount.
	checkContext := func() ([]*box.FileID, error) { return nil, nil }
	if len(ctxPaths) > 0 {
		m.mu.Lock()
		check := m.contextCheck
		m.mu.Unlock()
		if check == nil {
			return nil, fmt.Errorf("%w: profile %q has context_paths but they cannot be checked on this host", box.ErrBoxUnavailable, spec.Agent)
		}
		if absErr != nil {
			return nil, fmt.Errorf("%w: profile %q: cannot resolve working directory %q: %v", box.ErrBoxUnavailable, spec.Agent, spec.Cwd, absErr)
		}
		ws := absCwd // in clone mode this is the clone dir (enforced above)
		if real, err := filepath.EvalSymlinks(ws); err == nil {
			ws = real
		}
		checkContext = func() ([]*box.FileID, error) {
			ids := make([]*box.FileID, 0, len(ctxPaths))
			for _, p := range ctxPaths {
				id, err := check(p)
				if err != nil {
					return nil, fmt.Errorf("%w: profile %q: %w", box.ErrBoxUnavailable, spec.Agent, err)
				}
				if box.Overlaps(p, ws) {
					return nil, fmt.Errorf("%w: profile %q: context_paths %s overlaps the session workspace %s; the workspace is already mounted",
						box.ErrBoxUnavailable, spec.Agent, p, spec.Cwd)
				}
				ids = append(ids, id)
			}
			return ids, nil
		}
		if _, err := checkContext(); err != nil {
			return nil, err
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
	var cloneInfo *CloneInfo
	var cloneGH string // "owner/repo" of the clone's origin when the binding covers it
	// Held until the new session is published (or the spawn fails): a second
	// concurrent spawn of this clone must then see the first as a live session.
	releaseClone := func() {}
	defer func() { releaseClone() }()
	if cl != nil {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		cm, created, err := ch.Prepare(cctx, *cl, acct.IsDefault)
		cancel()
		if err != nil {
			return nil, err
		}
		cloneMounts = &cm
		// The scan runs under m.mu but only when the hooks call it, after they
		// hold the per-clone lock. Refresh itself runs outside m.mu: it can
		// take a while and must not stall the manager.
		rctx, rcancel := context.WithTimeout(context.Background(), cloneRefreshTimeout+cloneSpawnReturnBudget)
		info, release := ch.Refresh(rctx, *cl, RefreshOpts{Created: created, LiveSession: m.cloneLive(cl.Dir, nil), ReturnBudget: cloneSpawnReturnBudget})
		rcancel()
		releaseClone = sync.OnceFunc(release)
		cloneInfo = &info
		if bs.GitHub != nil {
			// Asked once: the answer feeds both GH_REPO and the push rewrite.
			cloneGH = cloneGHRepo(ch, cl.Of, bs.GitHub)
			if owner, repo, ok := strings.Cut(cloneGH, "/"); ok && bs.GitHub.CanPush(owner, repo) {
				cloneInfo.PushRepo = cloneGH
			}
		}
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
	var preTrust bool     // the agent gets --setting-sources user: see box.BoxKey.PreTrust
	var noImports bool    // the bundle was asked without the CLAUDE.md imports: see box.BoxKey.NoImports
	var importMounts int  // CLAUDE.md import mounts the bundle granted
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
			g, err := bundles.For(bin, !importsOff)
			noImports = importsOff
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
				importMounts = g.Imports
			}
			// The flag itself, not "the bundle applied": only the claude layout
			// carries it, so a shell, codex or wrapper-bin spawn that creates the
			// box leaves the dialog in place for any claude run there later.
			preTrust = settingsLimitedToUser(g.Flags)
			spec.ExtraArgs = append(spec.ExtraArgs, g.Flags...)
		}
		// A token entry's MCP server lives in a config of this session's own,
		// which replaces the shared bundle's path. Claude only: it is the agent
		// that reads --mcp-config, and a shell is never handed agent flags.
		includeUser := acct.IsDefault || acct.InheritPersonal
		if bundles != nil && !bundleFailed && includeUser && hasTokenMCP(bs.Tokens) {
			// Whatever the bin: a box is shared by the bins of a workspace and its
			// mounts are fixed at start, like the bundle's. Only boxes of an
			// account that inherits the personal config get it (the config in it
			// names the user's own servers).
			mt, err := bundles.SessionMCPMount(bs.Hash())
			if err != nil {
				return nil, fmt.Errorf("spawn %s: preparing token MCP servers: %w", spec.Agent, err)
			}
			mt.Optional = !isClaudeBin(bin)
			mounts = append(slices.Clip(mounts), mt)
		}
		if len(bs.Tokens) > 0 && isClaudeBin(bin) {
			flags, drops, err := m.tokenMCP(spec, brk, bs, bundles, includeUser, bundleFailed, bundleDrops)
			if err != nil {
				return nil, fmt.Errorf("spawn %s: preparing token MCP servers: %w", spec.Agent, err)
			}
			spec.ExtraArgs, bundleDrops = flags, drops
		}
		// Claude only: a shell is never handed agent flags. Flag and value are
		// separate argv elements, ahead of the "--" Build adds for a first message.
		if cl != nil && isClaudeBin(bin) {
			spec.ExtraArgs = append(slices.Clip(spec.ExtraArgs), "--append-system-prompt",
				cloneGuidance(cl.Of, cloneInfo.PushRepo != "", !cl.NoAutoReturn))
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

	// baseAccess is the base-role probe's results (AWS-bound sessions only). A key
	// it leaves unverified is watched before the box starts, so the watch is in
	// place for the box's first mint; a spawn that fails after that must drop
	// the watch, or the broker would keep it for an id that never ran.
	var baseAccess map[string]awsprobe.Result
	var ssoInfo map[string]ssoSpawnInfo // the sso_direct gate's per-key info
	watched := false
	started := false
	// heldBrokerBox is the box whose broker socket this spawn took a reference
	// on. Until the session is published every way out gives it back (the defer
	// below); after that the session owns it and gives it back when it ends.
	var heldBrokerBox string
	defer func() {
		if watched && !started {
			brk.Forget(id)
		}
		if heldBrokerBox != "" && !started {
			brk.ReleaseBox(heldBrokerBox)
		}
	}()

	// Containment is applied here, between the launcher and the process start,
	// so abduco persistence and the whole session lifecycle stay untouched.
	var boxName string
	var nonce, enonce string
	var exclusive bool
	var cwdForms []string    // boxed only: seeds Session.cwdFormsCache
	var resumeSkipped string // boxed only: set when --resume was dropped
	if runtime == config.RuntimeBox {
		m.mu.Lock()
		boxes := m.boxes
		recordBoxed := m.boxedRecorder
		m.mu.Unlock()
		key := box.BoxKey{Account: acctName, PartialMounts: bundleFailed, Clone: cloneMounts, PreTrust: preTrust, NoImports: noImports}
		// The bypass option the user chose for the profile or this spawn is the
		// consent: the box home it creates pre-accepts the warning that nothing
		// would answer in an unattended session. Not tied to the bundle or the
		// account, since the dialog is Claude Code's own and shows for every box.
		if br, ok := m.launcher.(BypassResolver); ok {
			key.PreAcceptBypass = br.BypassesPermissions(spec)
		}
		if len(ctxPaths) > 0 {
			key.Context = config.ContextHash(ctxPaths)
		}
		var bm []box.Mount
		var benv, penv []string
		var plannedBox string // the box this bound spawn's broker socket is prepared for
		if bs.Any() {
			// The box's name is its key's hash, so the key is completed first and
			// the name asked for here, before the broker mount is built. Wrap must
			// then report this same name (checked below), or the spawn is refused.
			key.Binding = bs.Hash()
			if (binding != nil && binding.Elevate != nil) || slices.ContainsFunc(awsConns, func(c config.AWSConnection) bool { return c.Elevate != nil }) {
				// An elevatable binding (or any elevatable connection) gets a box of its own (M3b §7.1):
				// processes in a shared box can read each other's broker token
				// from /proc, and an elevated token must not be readable by
				// another session.
				key.Session = id
			}
			var nerr error
			if plannedBox, nerr = boxes.BoxName(spec.Cwd, key); nerr != nil {
				return nil, fmt.Errorf("spawn %s: naming its box: %w", spec.Agent, nerr)
			}
			// Wrap computes the name again from the workspace path. It is told this
			// one, and refuses before it creates anything if the two differ (a
			// symlink on the path flipped in between): a box made with the mounts
			// prepared for another box would be taken for that box.
			key.Expect = plannedBox
			if bs.GitHub != nil {
				// Doctor-style and secret-free, so its message can be shown as is.
				if err := brk.CheckGitHub(*bs.GitHub); err != nil {
					if errors.Is(err, credref.ErrLocked) {
						// A key in the locked store: say so (423), not a containment failure.
						return nil, fmt.Errorf("%w (GitHub App key, profile %q)", launcher.ErrStoreLocked, spec.Agent)
					}
					var ue *secretstore.UnavailableError
					if errors.As(err, &ue) {
						// The Keychain did not give the key: 503 with the fix, not a containment failure.
						return nil, fmt.Errorf("%w: %w (GitHub App key, profile %q)", launcher.ErrStoreUnavailable, ue, spec.Agent)
					}
					return nil, fmt.Errorf("%w: profile %q: %w", box.ErrBoxUnavailable, spec.Agent, err)
				}
			}
			if bs.AWS != nil || len(bs.AWSConns) > 0 {
				// An sso_direct binding hands the agent the human's own
				// permission-set credentials, so unlike an assumed role it is
				// refused unless the read-only gate says so. A passed gate is a
				// definite base result for its key.
				var gated map[string]awsprobe.Result
				var err error
				gated, ssoInfo, err = gateSSODirect(brk, bs, spec.Agent)
				if err != nil {
					return nil, err
				}
				// Warn, never block: the probe has no error, only results.
				baseAccess = brk.ProbeBase(id, spec.Agent, bs)
				if baseAccess == nil {
					baseAccess = map[string]awsprobe.Result{}
				}
				maps.Copy(baseAccess, gated)
				if keys := awsprobe.NeedsRecheck(baseAccess, awsKeysOf(bs)); len(keys) > 0 {
					brk.WatchBase(id, keys)
					watched = true
				}
			}
			var err error
			bm, benv, err = brk.BoxFor(bs, baseAccess)
			if err != nil {
				return nil, fmt.Errorf("spawn %s: preparing %s access: %w", spec.Agent, bindingNames(bs), err)
			}
			nonce = brk.NewNonce()
			sm, err := brk.AcquireBox(plannedBox)
			if err != nil {
				return nil, fmt.Errorf("spawn %s: preparing %s access: %w", spec.Agent, bindingNames(bs), err)
			}
			heldBrokerBox = plannedBox
			bm = append(slices.Clip(bm), sm...) // never write into the hooks' own array
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
				if cloneGH != "" {
					env = append(env, "GH_REPO="+cloneGH)
				}
				if cloneInfo.PushRepo != "" {
					env = withGitPushRewrite(env, cl.Of, cloneInfo.PushRepo)
				}
			}
			mounts = append(slices.Clip(mounts), bm...) // never write into the bundle provider's array
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
		// The variable is agent-box's alone: whatever the profile or daemon env
		// says is dropped, so it is set exactly once and only for a context
		// profile. The mounts are read-only extras like the bundle's; the key's
		// hash gives a changed list a box of its own (reuseMounts compares
		// destinations only).
		env = withoutEnv(env, []string{config.ContextPathsEnv})
		if len(ctxPaths) > 0 {
			// Last look before the mount: the bundle, broker and egress work above
			// can take a while.
			ids, err := checkContext()
			if err != nil {
				return nil, err
			}
			for i, p := range ctxPaths {
				// The identity the check's own no-symlink walk saw. Docker follows
				// symlinks in a bind source, so a path or parent swapped after the
				// check mounts something else; Wrap compares the identity inside the
				// running box before the agent starts. On macOS the walk records none
				// (ids[i] is nil): CheckID has the box layer read it inside the VM
				// before the mount (box.Options.VMIDs) and compare the same way.
				mounts = append(slices.Clip(mounts), box.Mount{Host: p, Box: p, ID: ids[i], CheckID: true})
			}
			env = append(env, config.ContextPathsEnv+"="+config.ContextPathsEnvValue(ctxPaths))
			log.Printf("session %s (profile %s): context paths %v", id, spec.Agent, ctxPaths)
		}
		if recordBoxed != nil {
			// Last step before the box exists: from here the agent may write in
			// the workspace, so the host must already remember that it did.
			if err := recordBoxed(spec.Cwd); err != nil {
				return nil, fmt.Errorf("record boxed workspace %q: %w", spec.Cwd, err)
			}
		}
		agentArgv := slices.Clone(argv) // the pre-Wrap argv: the tail Wrap must leave intact
		wrapped, wrappedEnv, name, err := boxes.Wrap(context.Background(), spec.Cwd, id, key, argv, env, mounts)
		if err != nil {
			if errors.Is(err, box.ErrContextMountChanged) {
				// Shaped like the other context refusals, which name the profile.
				return nil, fmt.Errorf("%w: profile %q: %w", box.ErrBoxUnavailable, spec.Agent, err)
			}
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
		if heldBrokerBox != "" && name != heldBrokerBox {
			// Fail closed: the socket mounted into this box was prepared for another
			// box's name. Give back what Wrap took (the defer gives back the socket).
			m.releaseBoxNamed(name)
			return nil, fmt.Errorf("%w: spawn %s: box %s is not %s, the box its broker access was prepared for; not starting",
				box.ErrBoxUnavailable, spec.Agent, name, heldBrokerBox)
		}
		argv, env, boxName = wrapped, wrappedEnv, name
		if lf, ok := boxes.(interface{ LockedFiles(box string) []string }); ok {
			lockedFiles = lf.LockedFiles(name)
		}
		exclusive = key.Session != ""
		cwdForms = cwdFormsOf(spec.Cwd)
		if spec.Resume != "" {
			// A boxed claude started with an id whose transcript is not in its own
			// home exits at once. The box home is the same expression locate uses;
			// an unknown name or state dir means no look is possible, so no resume.
			m.mu.Lock()
			stateDir := m.stateDir
			m.mu.Unlock()
			boxDir := ""
			if boxName != "" && stateDir != "" {
				boxDir = boxHome(stateDir, boxName)
			}
			if !boxedTranscriptPresent(boxDir, spec.Resume, cwdForms) {
				// Drop only on the agent argv's own tail of the wrapped argv: the
				// wrapper's prefix (docker args, a profile's args) may hold a "--"
				// that would stop dropResume short. Fail closed: a claude started
				// with --resume <missing id> exits at once.
				n := len(agentArgv)
				if len(argv) < n || !slices.Equal(argv[len(argv)-n:], agentArgv) {
					m.releaseBoxNamed(boxName)
					return nil, fmt.Errorf("session: box %s: wrapped argv does not end with the agent argv; not starting with a resume that cannot be dropped", boxName)
				}
				tail, ok := dropResume(argv[len(argv)-n:], spec.Resume)
				if !ok {
					m.releaseBoxNamed(boxName)
					return nil, fmt.Errorf("session: box %s: could not drop --resume from the agent argv; not starting", boxName)
				}
				out := make([]string, 0, len(argv)-n+len(tail))
				out = append(out, argv[:len(argv)-n]...)
				argv = append(out, tail...)
				resumeSkipped = resumeSkippedNote
				log.Printf("session %s: resume %s skipped: transcript not in box %s", id, spec.Resume, boxName)
			}
		}
		if bundleHash != "" {
			// Logged per spawn so a box still carrying an older bundle is
			// visible rather than silent.
			log.Printf("session %s: box %s, config bundle %s", id, boxName, bundleHash)
		}
		if importMounts > 0 {
			log.Printf("session %s (profile %s): %d CLAUDE.md import(s) mounted read-only", id, spec.Agent, importMounts)
		}
	}

	cmd, ptmx, err := m.startProcess(id, spec, argv, env)
	if err != nil {
		m.releaseBoxNamed(boxName) // never leak a box for a session that failed to start
		return nil, err
	}
	started = true // from here the session is published; its removal forgets it

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
	sess.AWSConns = awsConns
	sess.GitHub = ghBinding
	sess.Tokens, sess.PATs = tokens, pats
	sess.EgressAllow, sess.EgressOpen = egressAllow, egressOpen
	sess.BrokerNonce = nonce
	sess.EgressNonce = enonce
	sess.brokerBox = heldBrokerBox // published: the session now owns the reference
	sess.BundleDrops = bundleDrops
	sess.Clone = cloneInfo
	sess.ContextPaths = slices.Clone(ctxPaths)
	sess.LockedFiles = slices.Clone(lockedFiles)
	if cl != nil {
		sess.CloneOf = cl.Of
	}
	sess.Exclusive = exclusive
	sess.cwdFormsCache = cwdForms
	// Only a claude session born here has seen every conversation it will
	// have; its history starts empty and is complete until proven otherwise.
	sess.costComplete = isClaudeBin(bin)
	sess.resumeSkipped = resumeSkipped
	sess.base = maps.Clone(baseAccess) // its own: a re-probe writes into it
	sess.sso = ssoInfo
	sess.onSubmitted = m.submittedHook(id)
	sess.agentKind = isAgentBin(bin) // before publication: no reader can see sess yet
	sess.replyable = replyable
	sess.submitted = submitted
	sess.options = opts
	sess.ParentID = spec.ParentID
	m.sessions[id] = sess
	sc := m.sidecar
	m.mu.Unlock()
	releaseClone() // published: the next spawn of this clone sees this session as live

	if sc != nil {
		sc.put(id, sessionMeta{Agent: spec.Agent, Name: name, Cwd: spec.Cwd, Created: created, Runtime: runtime, Box: boxName,
			Replyable: replyable, Submitted: boolPtr(submitted), Options: opts,
			Account: acctName, ConfigDir: cfgDir, AWS: binding, AWSConns: awsConns, GitHub: ghBinding, Tokens: tokens, PATs: pats, EgressAllow: egressAllow, EgressOpen: egressOpen, BrokerNonce: nonce, EgressNonce: enonce, BundleDrops: bundleDrops, Clone: cloneInfo.copy(), Exclusive: exclusive,
			ContextPaths: slices.Clone(ctxPaths), CloneOf: sess.CloneOf, LockedFiles: slices.Clone(lockedFiles),
			BaseAccess: maps.Clone(baseAccess), SSODirect: maps.Clone(ssoInfo), ParentID: spec.ParentID, CostComplete: sess.costComplete})
	}
	m.watchRetention(id, sess)
	return sess, nil
}

// tokenMCP writes the session's own MCP config when a bound token entry declares
// a box-side server, and returns the agent's extra args pointing at it plus the
// drops to report. The config replaces the shared bundle's, whose servers it
// carries when includeUser says the account may have them; a session whose
// account does not inherit them gets a config of entry and image servers only,
// confined with --strict-mcp-config like every boxed claude.
//
// bundles is nil with the bundle off, and bundleFailed means the bundle could
// not be built for this spawn: both still write the entries, unprobed and with
// no image servers.
func (m *Manager) tokenMCP(spec launcher.SpawnSpec, brk BrokerHooks, bs config.Bindings, bundles Bundles,
	includeUser, bundleFailed bool, drops []bundle.Drop) ([]string, []bundle.Drop, error) {
	bound := make(map[string]bool, len(bs.Tokens))
	for _, e := range bs.Tokens {
		bound[e.Name] = true
	}
	var entries []bundle.EntryServer
	drops = slices.Clone(drops)
	for _, e := range bs.Tokens {
		if e.MCP == nil {
			continue
		}
		args, err := config.EntryMCPArgs(e, bound)
		if err != nil {
			drops = append(drops, bundle.Drop{Kind: "mcp", Name: e.MCP.Server, Reason: err.Error()})
			continue
		}
		entries = append(entries, bundle.EntryServer{Server: e.MCP.Server, Entry: e.Name, Command: e.MCP.Command, Args: args})
	}
	if len(entries) == 0 {
		// Nothing to add (no entry declares a server, or each was left out): the
		// session keeps the bundle's own flags, byte for byte.
		return spec.ExtraArgs, drops, nil
	}

	var sm bundle.SessionMCP
	if bundles == nil || bundleFailed {
		log.Printf("spawn %s: no config bundle, so token MCP servers are written unprobed and without the image's servers", spec.Agent)
		servers, d := bundle.ComposeSessionMCP(nil, nil, nil, drops, entries)
		doc, err := bundle.MarshalSessionMCP(servers)
		if err != nil {
			return nil, nil, err
		}
		sm = bundle.SessionMCP{Doc: doc, Key: "nobundle", Drops: d}
	} else {
		var err error
		if sm, err = bundles.SessionMCP(includeUser, drops, entries); err != nil {
			return nil, nil, err
		}
	}
	// A document with the user's servers goes only where inheriting boxes look.
	var path string
	var err error
	if bundles != nil && !bundleFailed && includeUser {
		path, err = bundles.WriteSessionMCP(bs.Hash(), sm.Key, sm.Doc)
	} else {
		path, err = brk.WriteSessionMCP(bs, sm.Key, sm.Doc)
	}
	if err != nil {
		return nil, nil, err
	}
	args := slices.Clone(spec.ExtraArgs) // never write into the caller's slice
	if i := slices.Index(args, "--mcp-config"); i >= 0 && i+1 < len(args) {
		args[i+1] = path
	} else {
		args = append(args, "--mcp-config", path, "--strict-mcp-config")
	}
	return args, sm.Drops, nil
}

// hasTokenMCP reports whether any bound entry declares a box-side MCP server.
func hasTokenMCP(ts []config.StaticToken) bool {
	return slices.ContainsFunc(ts, func(e config.StaticToken) bool { return e.MCP != nil })
}

// bindingKinds lists the kinds of binding present, in a fixed order.
func bindingKinds(bs config.Bindings) []string {
	var k []string
	if bs.AWS != nil || len(bs.AWSConns) > 0 {
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
	s.releaseOnce.Do(func() {
		m.sessionEndedInBox(s)
		m.releaseBoxNamed(s.Box)
		m.brokerReleaseBox(s)
	})
}

// sessionEndedInBox tells a box layer that tracks it (the macOS guard locks'
// display) which session ended in the box. Before releaseBoxNamed, which may
// remove the box.
func (m *Manager) sessionEndedInBox(s *Session) {
	if s.Box == "" {
		return
	}
	m.mu.Lock()
	boxes := m.boxes
	m.mu.Unlock()
	if se, ok := boxes.(interface{ SessionEnded(box, sessionID string) }); ok {
		se.SessionEnded(s.Box, s.ID)
	}
}

// brokerReleaseBox gives back the reference on the box's broker socket that the
// session holds, if it holds one. Called once, from releaseSessionBox.
func (m *Manager) brokerReleaseBox(s *Session) {
	if s.brokerBox == "" {
		return
	}
	m.mu.Lock()
	brk := m.broker
	m.mu.Unlock()
	if brk != nil {
		brk.ReleaseBox(s.brokerBox)
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
			m.releaseSessionBox(sess)
			m.brokerRevoke(sess)
			go m.returnWork(sess, ReturnOnExit)
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
		m.timelineForget(id)
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

// BoxedCwds returns the working directory of every boxed session, retained and
// exited ones included (the tree a box wrote stays untrusted after it exits),
// without List's per-session transcript peek. It is the fs API's containment
// feed and runs on every /api/fs request, so it only reads fields under m.mu:
// no I/O. Empty cwds are dropped; duplicates are the caller's to drop.
func (m *Manager) BoxedCwds() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, s := range m.sessions {
		if s.Runtime == config.RuntimeBox && s.Cwd != "" {
			out = append(out, s.Cwd)
		}
	}
	return out
}

// List returns all sessions, oldest first.
func (m *Manager) List() []Info {
	brk := func() BrokerHooks {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.broker
	}()
	storeLocked := func() func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.storeLocked
	}()
	costSrc := func() CostSource {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.costSrc
	}()
	// The critical section is a closure so the unlock is a defer again: it used
	// to be an explicit Unlock (so PeekFor, which takes the same non-reentrant
	// mutex, could run after it), which meant a panic inside s.Info() would
	// leave m.mu held forever and wedge every spawn, kill, get and list for the
	// process lifetime. The closure keeps BOTH properties, and makes "no early
	// return between Lock and Unlock" structural rather than a rule to remember.
	// Conversation histories are copied inside the critical section (s.mu under
	// m.mu is the order Info() already takes) and priced after it: CostOf takes
	// the analytics lock, which is never held together with a session lock.
	type history struct {
		ids      []string
		complete bool
	}
	histories := map[string]history{}
	type ptyQuiet struct {
		act    string        // the silent-PTY activity
		silent time.Duration // how long the PTY has been silent
	}
	quiet := map[string]ptyQuiet{} // per session, for regActivity
	var repliesOn bool
	out := func() []Info {
		m.mu.Lock()
		defer m.mu.Unlock()
		repliesOn = m.repliesOn
		o := make([]Info, 0, len(m.sessions))
		for _, s := range m.sessions {
			i := s.Info()
			// Bin is written under m.mu (ResolveBins), so this is the one place
			// it can be read without racing.
			i.Claude = isClaudeBin(s.Bin)
			i.Resumable = i.Exited && resumeFor(s.Runtime, s.Bin, s.claudeSID(), s.Exclusive) != ""
			if costSrc != nil && i.Claude {
				ids, complete := s.conversations()
				histories[i.ID] = history{ids, complete}
			}
			q, silent := s.quietActivity()
			quiet[i.ID] = ptyQuiet{q, silent}
			o = append(o, i)
		}
		return o
	}()
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	// Summaries and needs are filled AFTER m.mu is released. PeekFor takes the
	// same non-reentrant mutex, so filling them inside the loop above deadlocks
	// the daemon on the first poll.
	var storeIsLocked *bool
	for i := range out {
		// A cost is shown only for a history known to be whole: a partial sum
		// would understate it. A known conversation with no cost ingested yet is
		// a real $0.00 (ingest lags the transcript), so it is present. A whole
		// history with no conversation at all is absent: we never saw one (the
		// registry may never validate), so $0.00 would be a guess.
		if h, ok := histories[out[i].ID]; ok && h.complete && len(h.ids) > 0 {
			c := costSrc.CostOf(h.ids)
			out[i].CostUSD = &c
		}
		p, ok, reg := m.peekAndReg(out[i].ID)
		blocked := reg == regStatusWaiting
		if ok {
			out[i].Summary = summaryLine(p.Text)
		}
		q := quiet[out[i].ID]
		// SendMessage's own verdict, judged on the PTY activity BEFORE the
		// registry folds into it. A non-agent session has no transcript, so
		// replyGate refuses it as SendMessage's agent check does.
		ready := repliesOn
		if ready {
			ready, _ = replyGate(out[i].Replyable, out[i].Activity, reg, q.silent, p, ok)
		}
		out[i].ReplyReady = &ready
		out[i].Activity, out[i].ActivitySource = regActivity(out[i].Activity, q.act, reg, q.silent)
		out[i].Activity, out[i].Need, out[i].Ask = needOf(out[i].Activity, out[i].Claude, p, ok, blocked)
		if out[i].Need == "ask" && out[i].Replyable && blocked && ok {
			out[i].AskFull = p.AskFull
		}
		if brk != nil && (out[i].AWS != nil || len(out[i].AWSConnections) > 0) {
			st := awsState{}
			st.until, st.conn, st.sso = brk.AWSState(out[i].ID)
			if len(out[i].AWSConnections) > 0 {
				st.conns = brk.AWSConnState(out[i].ID)
			}
			applyAWSState(&out[i], st)
		}
		// Read the store only for a session the broker actually refused, and
		// at most once per list.
		if brk != nil && storeLocked != nil && !out[i].Exited && out[i].Need != "ask" && brk.StoreLockedFor(out[i].ID) {
			if storeIsLocked == nil {
				v := storeLocked()
				storeIsLocked = &v
			}
			if *storeIsLocked {
				applyStoreNeed(&out[i])
			}
		}
		out[i].Attention = attentionOf(out[i])
	}
	return out
}

// applyStoreNeed marks a session that waits on the operator to unlock the
// encrypted store. Lower than "sso" and never over "ask", whose card has a
// prompt to answer. Activity is left alone: the row keeps saying what the agent
// is doing. An exited session needs nothing.
func applyStoreNeed(in *Info) {
	if in.Exited || in.Need == "ask" || in.Need == "sso" {
		return
	}
	in.Need = "store_locked"
}

// awsState is the broker's state for one listed session (BrokerHooks.AWSState
// and, for a session with connections, AWSConnState).
type awsState struct {
	until     time.Time         // end of the live write window; zero = none
	conn, sso string            // the window's connection ("" = legacy); the sign-in key awaited
	conns     map[string]string // named connection -> the sign-in key it awaits
}

// applyAWSState folds the broker's state into a listed bound session. A
// pending sign-in makes Need "sso" — over "" and "turn", never over "ask",
// whose card has a prompt to answer — for any bound session, claude or not.
// Activity is left alone: the row keeps saying what the agent is doing. An
// exited session shows no write window: nothing can mint with it any more.
// A session with connections shows the window on the connection it is for
// and each expired sign-in on its own connection.
func applyAWSState(in *Info, st awsState) {
	window := !st.until.IsZero() && !in.Exited
	needSSO := false
	switch {
	case in.AWS != nil:
		if window {
			u := st.until
			in.AWS.ElevatedUntil = &u
		}
		in.AWS.SSOExpired = st.sso
		needSSO = st.sso != ""
	case len(in.AWSConnections) > 0:
		for i := range in.AWSConnections {
			c := &in.AWSConnections[i]
			if window && c.Name == st.conn {
				u := st.until
				c.ElevatedUntil = &u
			}
			c.SSOExpired = st.conns[c.Name]
			needSSO = needSSO || c.SSOExpired != ""
		}
	default:
		return
	}
	if needSSO && in.Need != "ask" {
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
	m.timelineForget(id)
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
		if md.ParentID != "" && !aliveSet[md.ParentID] {
			// The parent did not survive the restart, so its id is free again
			// (counters are reseeded from live ids only) and the next session of
			// that agent may reuse it. Drop the link before publication so a
			// stranger can never adopt this shell or end it with itself.
			md.ParentID = ""
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
			// instead of being removed with its session. The session id is who
			// the box layer asks about write windows before sharing the folder.
			if mx, ok := boxes.(interface{ AdoptExclusive(box, sessionID string) }); ok {
				mx.AdoptExclusive(md.Box, id)
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
	// A restored bound session's box gets its broker socket back, BEFORE the
	// session is published: the socket returns in the directory the running box
	// already mounts. The reference is held for the session's life. If the adopt
	// fails the session holds nothing (so its end releases nothing, which would
	// close a socket another session in the box relies on) and its credential
	// requests are refused until it is respawned. Nothing between here and
	// publication can fail, so a held reference always reaches a session.
	heldBox := ""
	if runtime == config.RuntimeBox && md.Box != "" &&
		(config.Bindings{AWS: md.AWS, AWSConns: md.AWSConns, GitHub: md.GitHub, Tokens: md.Tokens}).Any() {
		m.mu.Lock()
		abrk := m.broker
		m.mu.Unlock()
		if abrk != nil {
			if err := abrk.AdoptBox(md.Box); err != nil {
				log.Printf("session %s: broker socket for box %s: %v (its credential requests are refused until it is respawned)", id, md.Box, err)
			} else {
				heldBox = md.Box
			}
		}
	}
	m.mu.Lock()
	sess := newSession(id, md.Agent, name, md.Cwd, created, cmd, ptmx, m.ringCap, m.backend)
	sess.Runtime = runtime
	sess.Box = md.Box
	sess.brokerBox = heldBox
	sess.Bin = bin
	sess.Account = md.Account
	sess.ConfigDir = md.ConfigDir
	sess.AWS = md.AWS
	sess.AWSConns = md.AWSConns
	sess.GitHub = md.GitHub
	sess.Tokens, sess.PATs = md.Tokens, md.PATs
	sess.EgressAllow, sess.EgressOpen = md.EgressAllow, md.EgressOpen
	sess.BrokerNonce = md.BrokerNonce
	sess.EgressNonce = md.EgressNonce
	if sess.EgressNonce == "" && md.BrokerNonce != "" {
		// A bound session from before A1 minted its proxy token over BrokerNonce.
		sess.EgressNonce = md.BrokerNonce
	}
	sess.BundleDrops = md.BundleDrops
	sess.Clone = md.Clone.copy()
	sess.ContextPaths = slices.Clone(md.ContextPaths)
	sess.CloneOf = md.CloneOf
	sess.LockedFiles = slices.Clone(md.LockedFiles)
	sess.Exclusive = md.Exclusive
	// nil for a record from before the probe: every key reads "not checked"
	// and is watched below, never a clean result.
	sess.base = maps.Clone(md.BaseAccess)
	sess.sso = maps.Clone(md.SSODirect)
	// Restored from the sidecar as-is: a pre-feature record decodes Replyable
	// to false (its zero value), so a session can never become repliable by
	// merely surviving a restart. With the kill-switch off the persisted value
	// is dropped in memory only; the sidecar keeps it, so turning replies back
	// on restores the operator's opt-ins.
	sess.replyable = md.Replyable && m.repliesOn
	sess.options = md.Options
	sess.ParentID = md.ParentID
	sess.costComplete = md.CostComplete
	if sessionIDRe.MatchString(md.ClaudeSessionID) { // never trust a path-ish id, even our own file's
		sess.claudeSessionID = md.ClaudeSessionID
	} else if md.ClaudeSessionID != "" {
		sess.costComplete = false // dropped: the history cannot vouch for its own current conversation
	}
	// The history is trusted only whole: one entry that is not a plain id (a
	// hand-edited or corrupt file) drops them all and the cost reads partial.
	// A record from before the list holds just the singular id and can never
	// be complete, since earlier conversations were not kept.
	if slices.ContainsFunc(md.ClaudeSessionIDs, func(sid string) bool { return !sessionIDRe.MatchString(sid) }) {
		sess.costComplete = false
	} else {
		sess.claudeSessionIDs = slices.Clone(md.ClaudeSessionIDs)
	}
	if len(sess.claudeSessionIDs) > maxConversationIDs {
		sess.claudeSessionIDs, sess.costComplete = sess.claudeSessionIDs[:maxConversationIDs], false
	}
	if sess.claudeSessionID != "" {
		sess.addConversationLocked(sess.claudeSessionID) // not yet published: no lock needed
		if !slices.Contains(md.ClaudeSessionIDs, sess.claudeSessionID) {
			sess.costComplete = false // the list did not know its own current conversation
		}
	}
	// The daemon was down for an unknown stretch: until the first registry read
	// the current conversation is only what the sidecar last saw.
	sess.restoredUnchecked = true
	// nil = a record written before the flag existed, or an orphan adopted with
	// no sidecar: restore as submitted (a stale amber beats a hidden waiter).
	sess.submitted = md.Submitted == nil || *md.Submitted
	sess.onSubmitted = m.submittedHook(id)
	sess.agentKind = isAgentBin(bin) // before publication: no reader can see sess yet
	m.sessions[id] = sess
	brk := m.broker
	m.mu.Unlock()
	// Not re-probed now (a restart must not mint for every session): an
	// unverified or unchecked key waits for the session's next base mint.
	if brk != nil {
		if keys := awsprobe.NeedsRecheck(md.BaseAccess, sess.awsKeys()); len(keys) > 0 {
			brk.WatchBase(id, keys)
		}
	}
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
// It counts on from the highest number ever issued for the agent, not just from
// the live sessions, so an id is never reissued (see idfloor.go).
func (m *Manager) nextIDLocked(agent string) string {
	if m.idMark[agent] > m.counters[agent] {
		m.counters[agent] = m.idMark[agent]
	}
	for {
		m.counters[agent]++
		id := fmt.Sprintf("%s-%d", agent, m.counters[agent])
		if _, exists := m.sessions[id]; exists {
			continue
		}
		if m.backend != nil && m.backend.Alive(id) {
			continue
		}
		m.idMark[agent] = m.counters[agent]
		m.persistIDMarksLocked()
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

// settingsLimitedToUser reports whether args make Claude Code load user settings
// only (--setting-sources user), so a workspace's own .claude/settings.json, its
// hooks included, never loads. That is what makes pre-accepting the workspace
// trust dialog safe (box.BoxKey.PreTrust).
func settingsLimitedToUser(args []string) bool {
	for i, a := range args {
		if a == "--setting-sources=user" || (a == "--setting-sources" && i+1 < len(args) && args[i+1] == "user") {
			return true
		}
	}
	return false
}
