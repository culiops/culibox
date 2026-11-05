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
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/culiops/culibox/internal/extraca"
)

// Container labels. Instance keeps the DEV and PROD daemons from adopting or
// reaping each other's boxes.
const (
	LabelWorkspace = "io.culiops.agent-box.workspace"
	LabelInstance  = "io.culiops.agent-box.instance"
	// LabelExclusive marks a box another spawn must not share a folder with;
	// LabelClaims marks a box created by a build that enforces exclusive
	// workspaces, so older boxes (without it) are known not to honour claims.
	LabelExclusive = "io.culiops.agent-box.exclusive"
	LabelClaims    = "io.culiops.agent-box.claims"
	// LabelCAScratch marks the never-started container caBundle creates to read
	// the image's trust bundle. It is deliberately not LabelInstance: claimCheck
	// and Sweep select boxes by that key and a leftover would read as a box with
	// no workspace, blocking every spawn. Reconcile and Sweep reap it by this one.
	LabelCAScratch = "io.culiops.agent-box.ca-scratch"
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
//
// Optional marks a mount the session runs without (a config overlay), as
// opposed to one its argv or env points into. It only matters when a running
// box with live sessions lacks it: a missing optional mount is tolerated there,
// a missing required one refuses the spawn (see reuseMounts).
type Mount struct {
	Host, Box string
	Optional  bool
	// Yield marks a mount that gives way: Wrap drops it when the workspace or any
	// non-yielding extra mount already shows its Box path (yieldCovered), so the
	// box sees the live file and docker gets no nested or duplicate mount. A
	// CLAUDE.md import is one.
	Yield bool
	// ID, when set, is Host's identity when it was last checked (a context
	// path, on Linux read on the host). Once the box runs, ensure refuses it
	// unless Box shows the same identity inside it (checkMountIDs).
	ID *FileID
	// CheckID marks a mount whose identity must be checked even though the host
	// recorded none (a context path on macOS, where a host inode number is not
	// the one the box sees). With no ID and Options.VMIDs set, ensure reads the
	// identity inside the VM before the mount is made, and checkMountIDs then
	// compares it like any other. Only context mounts set it: a config overlay
	// or guard mount is never refused for being a symlink.
	CheckID bool
}

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
	Instance               string // the instance label (DEV/PROD), "" when absent
	Exclusive, Claims      bool   // the exclusive / claims labels (see LabelExclusive)
	// Unreadable marks a row that did not split into the listing's fields: a tab
	// or newline inside a label of a container agent-box did not create (this
	// package refuses such workspace paths). Its workspace, instance and flags
	// are then guesses, so the workspace checks treat it as a conflict.
	Unreadable bool
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

// SeccompOffText is the refusal (and doctor FAIL) when Docker's seccomp is off
// while an encrypted store exists: without it a box could call keyctl and read
// the unlocked store key from the user keyring.
const SeccompOffText = "Docker seccomp is off: a box could read the unlocked store key. Enable the default seccomp profile."

// ErrSeccompOff is that refusal as an ErrBoxUnavailable, so the web layer
// shows its text (a fixed sentence, no values).
var ErrSeccompOff = fmt.Errorf("%w: %s", ErrBoxUnavailable, SeccompOffText)

// ErrBoxOwned marks a host-tier spawn refused because its directory is a clone
// that a box owns: the box can rewrite anything in it, so the host must not run
// an agent (or git, or a build) there. Its message names the path the user
// picked and the remedy, so it is shown to them.
var ErrBoxOwned = errors.New("box-owned directory")

// ErrWorkspaceHeld marks a boxed spawn refused because its folder overlaps a
// box it must not share with (M3b §7.2). Its message names only the folder the
// user picked and the remedy, so it is shown to them.
var ErrWorkspaceHeld = errors.New("workspace held")

// Overlaps reports whether two workspaces share files: equal, or one is an
// ancestor of the other, compared component-wise. Callers pass absolute,
// symlink-resolved paths; Clean takes care of trailing slashes and dot segments.
func Overlaps(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b {
		return true
	}
	under := func(p, dir string) bool {
		return strings.HasPrefix(p, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator))
	}
	return under(a, b) || under(b, a)
}

// holdsWorkspace reports whether a box in that docker state keeps its folder
// mounted: a paused or restarting box resumes into it, so it counts like a
// running one. Exited, created and dead boxes do not.
func holdsWorkspace(state string) bool {
	return state == "running" || state == "paused" || state == "restarting"
}

// knownWorkspace reports whether a foreign container's workspace label is a
// path the overlap check can compare. agent-box writes absolute resolved paths,
// so an empty or relative label is something else's and is taken to overlap.
func knownWorkspace(w string) bool { return filepath.IsAbs(w) }

// OtherInstanceHolds reports whether a box outside this instance's knowledge
// may have dir mounted: a live box of another instance over an overlapping
// folder, or any live row whose workspace cannot be trusted (unreadable, or no
// usable workspace label). A box of this instance with a known workspace never
// counts: its sessions are this instance's own to check, and an idle one (kept
// for its TTL) must not block. dir is the symlink-resolved path. It returns the
// first holder's name.
func OtherInstanceHolds(boxes []BoxInfo, dir, instance string) (holder string, busy bool) {
	for _, b := range boxes {
		if !holdsWorkspace(b.State) {
			continue
		}
		if b.Unreadable || !knownWorkspace(b.Workspace) || (b.Instance != instance && Overlaps(b.Workspace, dir)) {
			return b.Name, true
		}
	}
	return "", false
}

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
	// Stdout is Run reading stdout only, for an answer that is compared line by
	// line and must not be shifted by a CLI warning on stderr.
	Stdout(ctx context.Context, args []string) (string, error)
	Inspect(ctx context.Context, name string) (State, error)
	ListBoxes(ctx context.Context, label string) ([]BoxInfo, error)
	ListByLabel(ctx context.Context, label string) ([]string, error)
	Stop(ctx context.Context, name string) error
	HasRuntime(ctx context.Context, name string) bool
	// InspectMounts is Inspect plus the in-box destinations of the box's bind
	// mounts, as docker reports them: the truth for every box, adopted ones and
	// ones created by an older build included. Mounts are nil unless running.
	// The third value is the box's extraca.Label value, "" when it has none.
	InspectMounts(ctx context.Context, name string) (State, []string, string, error)
	// BindSource is the host path of the running box's bind mount at the in-box
	// destination dst ("" when it has none). Only the broker's mount is read this
	// way (reuseBrokerDir), so a box pays the extra call only when it has one.
	BindSource(ctx context.Context, name, dst string) (string, error)
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
	// VMIDs reads the identity of each host path as the docker daemon's kernel
	// sees it, in order (on macOS: inside the VM, since a Mac inode number is
	// not the one virtiofs shows the box). ensure calls it, once, for the mounts
	// that are CheckID with no ID, before the box is created. An error or a
	// wrong count refuses the spawn. nil = such mounts are not checked (Linux
	// records the identity on the host itself).
	VMIDs func(ctx context.Context, paths []string) ([]FileID, error)
	// Shared lists the host directories the docker daemon can see; a workspace
	// outside them is refused. nil = every path is visible.
	Shared []string
	// Protected are host directories besides StateRoot (always protected) that
	// a workspace must not overlap: the config and profiles directories when
	// they live outside the data root.
	Protected []ProtectedDir
	// Reachable reports whether the daemon answers; false = down, and the idle
	// sweep skips instead of reaping on a failed list. nil = assume reachable.
	Reachable func(ctx context.Context) bool
	// EgressAll fences every box: it joins Egress.Network and is checked by
	// Egress.Verify instead of Options.Verify (both OSes since #69). false: only
	// bound boxes are fenced, and an unbound box keeps the default network.
	EgressAll bool
	// GuardInPlace binds the workspace root's .git, .claude/, CLAUDE.md,
	// CLAUDE.local.md, AGENTS.md, .mcp.json and an in-workspace core.hooksPath
	// directory read-only onto themselves in every box (see inPlaceMounts), so a
	// box cannot write those top-level names directly. It does not stop
	// everything host git or a host-tier agent later runs or loads: the same
	// names in subdirectories stay writable (sub/CLAUDE.md, sub/.mcp.json,
	// sub/.claude/settings.json, loaded by a host session started in sub/), a
	// box can still create an absent CLAUDE.md, CLAUDE.local.md, AGENTS.md or
	// .mcp.json and a .git anywhere in the workspace, and a guarded file can
	// still call writable workspace code (a hook running a repo script, a
	// CLAUDE.md @import). A workspace whose guards cannot be derived exactly is
	// refused.
	GuardInPlace bool
	// PidsLimit caps processes and threads per box; 0 = DefaultPidsLimit.
	// TmpSize is the size of the box's /tmp tmpfs ("512m", number + k/m/g);
	// "" = DefaultTmpSize. Memory is the box's RAM cap ("4g", number + k/m/g,
	// at least 1g, or "max"); swap is capped to the same value. "" = half of docker's
	// memory, at least min(4g, all of it) (DefaultMemory if that cannot be read), "max" = all of it (see
	// memoryCap). CPUs caps a box's CPU time (--cpus); 0 = no cap.
	// New validates all four (see ValidateLimits, ValidateCPUs).
	PidsLimit int
	TmpSize   string
	Memory    string
	CPUs      float64

	// ExtraCA is the operator's extra trust set (config extra_ca_certs). Empty =
	// off: no label, no mount, no docker call, runArgs unchanged. When set, every
	// new box mounts a bundle (the image's own roots plus these) read-only over
	// extraca.ContainerBundle and carries extraca.Label, and every session gets
	// the extraca.EnvNames pointing at it.
	ExtraCA extraca.Set
	// ExtraCAErr is why the configured CAs could not be loaded. Non-nil refuses
	// every boxed spawn: starting without the trust the operator asked for would
	// fail on the very proxy they configured it for, or worse, look configured.
	ExtraCAErr error
}

// Egress fences boxes that carry an AWS binding (BoxKey.Binding != ""), and
// every box when Options.EgressAll is set. With an empty Network or a nil
// Verify, such a box is refused: it must never start on
// an open network, or be used unverified.
type Egress struct {
	Network  string
	Verify   func(ctx context.Context, box string) error // runs INSTEAD of Options.Verify for these boxes
	AddHosts []string                                    // --add-host values, e.g. "name:10.0.0.1" (darwin no longer uses it)
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
	// egress is set once at boot by SetEgress (guarded by mu).
	egress Egress
	// exclusive marks boxes that belong to one session (BoxKey.Session, M3b):
	// they are removed as soon as their last session is released, with no
	// idle-TTL reuse. Set by ensure and, after a restart, by MarkExclusive.
	exclusive map[string]bool
	// memTotal is docker's MemTotal once read (0 = not yet); memFallbackLogged
	// stops the unreadable-MemTotal fallback from logging on every spawn. Both
	// guarded by mu.
	memTotal          int64
	memFallbackLogged bool
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
	if err := ValidateLimits(opts.PidsLimit, opts.TmpSize, opts.Memory); err != nil {
		return nil, fmt.Errorf("box: %w", err)
	}
	if err := ValidateCPUs(opts.CPUs); err != nil {
		return nil, fmt.Errorf("box: %w", err)
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

		verified:  map[string]bool{},
		drift:     map[string]bool{},
		exclusive: map[string]bool{},
	}
	if opts.Client.HasRuntime(context.Background(), runtimeSysbox) {
		m.runtime = runtimeSysbox
	}
	return m, nil
}

// SetEgress configures the egress fence: for every box when EgressAll is set,
// otherwise for bound boxes only. Call it once at boot, before Reconcile and
// before any boxed spawn.
func (m *Manager) SetEgress(e Egress) {
	e.AddHosts = append([]string(nil), e.AddHosts...)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.egress = e
}

func (m *Manager) egressConfig() Egress {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.egress
}

// Runtime reports the detected isolation runtime, for the UI badge.
func (m *Manager) Runtime() string { return m.runtime }

// BrokerMountDest is where a bound box's own broker socket directory is mounted
// inside the box, and BrokerDirRel is where those directories live under the
// state root, one per box name. The broker creates them; the box manager only
// checks that a box mounts its OWN: the broker takes the box for whoever's
// directory a request arrives through. BrokerMountDest is not the path boxes
// used before the per-box directories (reuseMounts compares in-box destinations
// only, so a new path is what keeps an older box from being reused silently).
const (
	BrokerMountDest = "/opt/agentbox-broker"
	BrokerDirRel    = "broker/b"
)

// BrokerDir is box name's broker socket directory under stateRoot: the last 12
// hex digits of the name, so the path is as short as a unix socket needs.
// name must pass IsBoxName.
func BrokerDir(stateRoot, name string) string {
	return filepath.Join(stateRoot, filepath.FromSlash(BrokerDirRel), name[len(name)-12:])
}

// BoxKey is everything besides the workspace that decides which box a session
// runs in. Account is the agent account name ("" or "default" for the default
// account). Binding is the hash of the session's AWS binding (config.AWSBinding
// Hash), "" for a session without one. Session is the session id for a binding
// with elevate (M3b): such a box belongs to one session and is removed when it
// ends. It is empty for every other session.
//
// PartialMounts is not part of the identity (boxName ignores it): it says the
// caller could not compute every mount it asked for, because the config bundle
// failed to build. A box carrying more than such a spawn asks for is then not
// taken as differing from it, so a failed build never strips an idle box of the
// bundle the next spawn will want back (see reuseMounts).
//
// Clone is set by the session layer for a clone-mode profile (nil for every
// other spawn). Its hash is part of the identity: reuseMounts compares in-box
// destinations only, so without it a profile whose clone_of changed, with the
// same default_cwd and share list, would reuse a box still mounting the old
// repo's files.
type BoxKey struct {
	Account, Binding, Session string
	// Expect is the box name the caller prepared things for before calling Wrap
	// (its broker socket directory, named by BoxName). Not identity. When set, Wrap
	// refuses, before anything is created, unless the name it computes is exactly
	// this one: a workspace path that changed in between must not start another
	// box with the mounts prepared for this one.
	Expect        string
	PartialMounts bool
	Clone         *CloneMounts
	// Context is config.ContextHash of the profile's context_paths ("" = none).
	// It is part of the identity for the same reason Clone's hash is: reuseMounts
	// compares in-box destinations only, so a changed list must not land in a box
	// that still mounts the old one.
	Context string
	// PreTrust asks for Claude Code's workspace trust dialog to be pre-accepted
	// in the box home (see trustWorkspace). Not identity, like PartialMounts: it
	// is applied when the box is created. The session layer sets it only when the
	// spawn creating it runs claude with the config bundle's --setting-sources
	// user, which keeps a repo's own settings and hooks from loading; without it
	// the dialog is the only gate before those run, so it stays.
	PreTrust bool
	// NoImports is set when the profile turns CLAUDE.md imports off, so the box
	// carries no import mounts. It is part of the identity, like Context:
	// reuseMounts treats a box with extra mounts as fine for a spawn that asks
	// for fewer, so without it an imports-off profile could land in a live box
	// that still mounts imports.
	NoImports bool
}

// CloneMounts is the clone-mode part of a BoxKey. Hash identifies the clone spec
// (config.Clone.Hash); RO are the read-only mounts the clone needs: the source
// repo's .git, the shared paths and the memory dir. Wrap appends them to the
// extra read-only binds. A clone adds no writable bind (spec D12): the only
// writable mounts of a box stay its workspace and its home.
type CloneMounts struct {
	Hash string
	RO   []Mount
}

// boxName derives the container name from the canonical workspace path.
// Symlinks are resolved so two spellings of one directory never produce two
// boxes, and the instance is mixed in so DEV and PROD stay separate. The agent
// account is mixed in for every account but the default, so each (workspace,
// account) pair gets its own container and home; the default account hashes
// exactly what it always has, keeping existing box names and boxstate valid.
// An AWS binding hash is mixed in last, so a session without a binding never
// shares a box with one that has a binding, sessions with the same binding
// share, and an unbound session's name is unchanged from before bindings.
// A clone-mode spec hash goes after those, so a non-clone name is unchanged too,
// and a context_paths hash goes after that for the same reason. An imports-off
// key adds "\x00noimports" last, only when set, so every other name is unchanged.
func (m *Manager) boxName(cwd string, k BoxKey) (string, error) {
	abs, err := resolvePath(cwd)
	if err != nil {
		return "", err
	}
	return m.nameFor(abs, k), nil
}

// nameFor is boxName for a workspace path that is already absolute and
// symlink-resolved (workspace.Path). Wrap names the box from the same path it
// checked and mounts; boxName resolves first, so BoxName and Wrap agree.
func (m *Manager) nameFor(abs string, k BoxKey) string {
	key := m.opts.Instance + "\x00" + abs
	if k.Account != "" && k.Account != "default" { // account.Default; box does not import account
		key += "\x00" + k.Account
	}
	if k.Binding != "" {
		key += "\x00aws:" + k.Binding
	}
	if k.Session != "" {
		key += "\x00session:" + k.Session
	}
	if k.Clone != nil {
		key += "\x00clone:" + k.Clone.Hash
	}
	if k.Context != "" {
		key += "\x00ctx:" + k.Context
	}
	if k.NoImports {
		key += "\x00noimports"
	}
	sum := sha256.Sum256([]byte(key))
	return namePrefix + hex.EncodeToString(sum[:6])
}

// BoxName is the container name Wrap uses for (cwd, key): the same function, so
// a caller that must prepare something for the box before it exists (the
// credential broker's per-box socket) learns the name without starting it. The
// key must be complete (binding, elevate session, clone, context), exactly as
// it will be passed to Wrap.
func (m *Manager) BoxName(cwd string, key BoxKey) (string, error) {
	return m.boxName(cwd, key)
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
// markers are configuration, not credentials. With trust set (a workspace
// path, "" for none) it also pre-accepts Claude Code's trust dialog there (see
// trustWorkspace), which is safe from races because ensure calls this only
// while the box's container is absent.
//
// Everything BELOW the home is created through an os.Root anchored at it, never
// through plain path joins. The box writes in this directory as the daemon user,
// so between two spawns it can replace any path this function is about to create
// with a symlink and steer the daemon's own MkdirAll/WriteFile at a host path of
// its choosing. os.Root resolves each component with openat/O_NOFOLLOW
// semantics, so a symlink out of the home fails instead of being followed —
// the same boundary internal/web puts around $HOME for uploads and file ops.
func (m *Manager) ensureStateDir(boxName, trust string, extraRO []Mount) (string, error) {
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
	switch {
	case err == nil:
		_, err = f.Write(body)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return "", fmt.Errorf("box: seeding agent state in %q: %w", dir, err)
		}
	case os.IsExist(err):
		// state a running agent has already written: merged below, never replaced
	default:
		return "", fmt.Errorf("box: seeding agent state in %q: %w", dir, err)
	}
	// New and existing homes alike: homes from before this existed lack the
	// entry. Best effort: without it the agent just asks, as it always did.
	if trust != "" {
		if err := trustWorkspace(root, trust); err != nil {
			log.Printf("box: %s: workspace trust not pre-accepted: %v", boxName, err)
		}
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
func unboxableWorkspace(cwd string, protected []ProtectedDir, shared []string) (string, bool) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", false
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	} else {
		// A directory that does not exist yet (a profile's default, say) is still
		// judged by where it would be created: the existing part of its path
		// resolved, as ProtectedOverlap always did.
		abs = resolveExisting(abs)
	}
	return unboxableResolved(abs, protected, shared)
}

// unboxableResolved is unboxableWorkspace for a path that is already absolute and
// symlink-resolved (workspace.Path), which it does not resolve again: the
// protected check in it (protectedOverlapResolved) is about this very path.
func unboxableResolved(abs string, protected []ProtectedDir, shared []string) (string, bool) {
	if reason := MountUnsafe(abs); reason != "" {
		return reason, true
	}
	if abs == string(filepath.Separator) {
		return "it is the filesystem root", true
	}
	if home, err := os.UserHomeDir(); err == nil {
		if real, err := filepath.EvalSymlinks(home); err == nil {
			home = real
		}
		if reason := homeReason(abs, home); reason != "" {
			return reason, true
		}
	}
	if reason := protectedOverlapResolved(abs, protected); reason != "" {
		return reason, true
	}
	if len(shared) > 0 && !UnderMount(abs, shared) {
		return "it is not under the box VM's shared directories (" + strings.Join(shared, ", ") +
			"), so docker cannot see it", true
	}
	return "", false
}

// errUnboxable is Wrap's refusal of a workspace unboxableWorkspace (or
// unboxableResolved) gave a reason for. It names cwd, the path the user chose.
func errUnboxable(cwd, reason string) error {
	return fmt.Errorf("%w: %s (%s). Pick a project directory, "+
		"or choose \"Run on trusted host\" to run it uncontained on purpose",
		ErrUnboxableWorkspace, cwd, reason)
}

// homeReason reports why abs (absolute, symlinks resolved) cannot be mounted
// because it is, or is an ancestor of, home (also resolved), or "" when it is
// neither.
func homeReason(abs, home string) string {
	if abs == filepath.Clean(home) {
		return "it is your home directory, so the box would mount everything in it"
	}
	// An ancestor of home is worse still.
	if rel, err := filepath.Rel(abs, home); err == nil && rel != "." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
		return "it contains your home directory"
	}
	return ""
}

// protected is every directory a workspace must not overlap: the state root
// first, then the caller's (config, profiles), deduplicated.
func (m *Manager) protected() []ProtectedDir {
	return DedupeProtected(append([]ProtectedDir{DataDirProtected(m.opts.StateRoot)}, m.opts.Protected...))
}

// Unboxable is the workspace refusal Wrap applies, for the boot-time check.
func (m *Manager) Unboxable(cwd string) (string, bool) {
	return unboxableWorkspace(cwd, m.protected(), m.opts.Shared)
}

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
// execute. The returned env is the caller's env minus the extra-CA variables,
// plus those variables when CAs are configured: it stays on the
// docker CLIENT process so `-e NAME` can forward values without ever putting
// them in argv, which is world-readable through `ps` on the host.
// It also returns the box name. That name is the box's identity from here on:
// callers persist it and use it for adopt/release/kill instead of re-deriving it
// from cwd. A workspace directory can be moved or deleted while its agent is
// still running, and a name that cannot be recomputed is a box that leaks or,
// worse, gets reaped out from under a live session.
func (m *Manager) Wrap(ctx context.Context, cwd, sessionID string, key BoxKey, argv, env []string, extraRO []Mount) ([]string, []string, string, error) {
	if m.opts.ExtraCAErr != nil {
		return nil, nil, "", fmt.Errorf("%w: extra_ca_certs: %v", ErrBoxUnavailable, m.opts.ExtraCAErr)
	}
	// The one resolution: every use of the workspace below takes ws.Path (what
	// is checked, named, guarded, labelled and mounted), so a symlink in the path
	// that changes after this point cannot make one of them look somewhere the
	// others did not. cwd is only named in refusals.
	ws, err := resolveWorkspace(cwd)
	if err != nil {
		// A workspace that does not exist yet and overlaps a protected directory
		// (a config dir not created yet) is still refused for that reason, which
		// says what to do, before the plain resolve failure: as it always was.
		// Nothing is created either way; this only words the refusal.
		if _, perr := resolvePath(cwd); perr != nil {
			if reason, bad := unboxableWorkspace(cwd, m.protected(), m.opts.Shared); bad {
				return nil, nil, "", errUnboxable(cwd, reason)
			}
		}
		return nil, nil, "", err
	}
	if wrapResolvedHook != nil {
		wrapResolvedHook(ws)
	}
	if reason, bad := unboxableResolved(ws.Path, m.protected(), m.opts.Shared); bad {
		return nil, nil, "", errUnboxable(cwd, reason)
	}
	name := m.nameFor(ws.Path, key)
	// Before anything exists: the caller prepared this box's broker socket
	// directory for the name it asked for. If a symlink on the workspace path was
	// repointed since, the name differs, and a box created now would carry
	// another box's directory and be taken for that box.
	if key.Expect != "" && name != key.Expect {
		return nil, nil, "", fmt.Errorf("%w: the box for %s is %s, not %s, which this spawn's broker access was prepared for (the workspace path changed in between); not starting",
			ErrBoxUnavailable, cwd, name, key.Expect)
	}
	if err := m.checkBrokerMount(name, extraRO); err != nil {
		return nil, nil, "", err
	}
	if key.Clone != nil {
		if key.Clone.Hash == "" {
			return nil, nil, "", errors.New("box: a clone-mode spawn needs the clone's hash")
		}
		// Ordinary read-only extras: MountUnsafe, reuseMounts (a live box missing
		// a required clone mount is refused, an idle one recreated) and runArgs
		// all see them, and runArgs renders every extra read-only.
		extraRO = append(slices.Clip(extraRO), key.Clone.RO...)
	}
	// A clone workspace is the box's own tree, rewritable by the box by design;
	// guarding its .git/.claude would only protect the box from itself.
	if m.opts.GuardInPlace && key.Clone == nil {
		// Derived from ws, the single resolution above, which is what docker
		// mounts. Appended as ordinary read-only extras, so MountUnsafe,
		// reuseMounts (a live box without a guard is refused, an idle one
		// recreated) and runArgs all see them. Clip: never write into the caller's
		// backing array.
		guard, err := inPlaceMountsAt(ws)
		if err != nil {
			return nil, nil, "", err
		}
		extraRO = append(slices.Clip(extraRO), guard...)
	}
	// Last, so the clone and guard mounts above count as covering too.
	extraRO = yieldCovered(ws.Path, extraRO)
	if err := m.ensure(ctx, name, ws, extraRO, key.PartialMounts, key.Binding != "", key.Session != "", key.PreTrust); err != nil {
		return nil, nil, "", err
	}
	// The CA variables are fixed container paths set here and nowhere else: any
	// incoming value (a profile's, the host's) is dropped first, so nothing can
	// point a tool at a different bundle. SSL_CERT_DIR and GIT_SSL_CAPATH are
	// dropped too and never set. Off, they are simply dropped.
	env = withoutNames(env, extraca.DeniedEnvNames)
	if !m.opts.ExtraCA.Empty() {
		for _, n := range extraca.EnvNames {
			env = append(env, n+"="+extraca.ContainerBundle)
		}
	}
	m.prepareHomePid(name, sessionID)
	return m.execArgv(name, ws.Path, sessionID, argv, env), env, name, nil
}

// yieldCovered drops each Yield mount whose Box path the workspace or a
// non-yielding extra already covers (UnderMount: at or below, so equality
// counts). Yield mounts are checked against non-yield mounts only, never each
// other. It returns a new slice and keeps the order of the rest; the caller's
// backing array is never written to.
func yieldCovered(workspace string, extraRO []Mount) []Mount {
	covers := []string{workspace}
	for _, mt := range extraRO {
		if !mt.Yield {
			covers = append(covers, mt.Box)
		}
	}
	out := make([]Mount, 0, len(extraRO))
	for _, mt := range extraRO {
		if mt.Yield && UnderMount(filepath.Clean(mt.Box), covers) {
			continue
		}
		out = append(out, mt)
	}
	return out
}

// withoutNames returns env without the named variables, as a new slice: the
// caller's backing array is never written to.
func withoutNames(env, names []string) []string {
	out := make([]string, 0, len(env)+len(names))
	for _, kv := range env {
		n, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(names, n) {
			out = append(out, kv)
		}
	}
	return out
}

// ensure starts the box if it is not already running, and registers the caller
// as a live session.
//
// The whole body runs under opMu, which also guards the idle stop. Without that
// serialization two first spawns race to `docker run` the same --name (docker
// answers with a Conflict), and a spawn can `docker exec` into a container the
// idle timer is concurrently removing.
func (m *Manager) ensure(ctx context.Context, name string, ws workspace, extraRO []Mount, partial, bound, exclusive, preTrust bool) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	if t, ok := m.timers[name]; ok {
		t.Stop()
		delete(m.timers, name)
	}
	m.mu.Unlock()

	// verify is this box's containment check. A fenced box (an AWS binding, or
	// any box when EgressAll is on) is checked by the egress probe INSTEAD of
	// Options.Verify (the loopback/DNS check unfenced boxes get): the two
	// describe opposite network policies. A fenced box that has no egress
	// control to be checked by never starts.
	fenced := bound || m.opts.EgressAll
	verify := m.opts.Verify
	if fenced {
		e := m.egressConfig()
		if e.Network == "" || e.Verify == nil {
			m.rearmIfIdle(name)
			if bound {
				return fmt.Errorf("%w: egress control is not running, so a box with AWS access cannot start", ErrBoxUnavailable)
			}
			return fmt.Errorf("%w: egress control is not running, so this box cannot start", ErrBoxUnavailable)
		}
		verify = e.Verify
	}

	// Under opMu with the docker run below, so two spawns in this process
	// cannot both pass the check.
	if err := m.claimCheck(ctx, name, ws.Path, exclusive); err != nil {
		m.rearmIfIdle(name)
		return err
	}

	// One docker call answers both whether the box runs and what it mounts.
	st, have, caLabel, err := m.opts.Client.InspectMounts(ctx, name)
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
	if st == StateRunning {
		// Mounts are fixed at `docker run`, and the box key does not say which
		// agent will run in the box, so check the box can serve this spawn
		// before anything else trusts it (brain #131).
		if st, err = m.reuseMounts(ctx, name, ws.Path, have, extraRO, partial); err != nil {
			m.rearmIfIdle(name)
			return err
		}
		// reuseMounts compares destinations only, so a box that mounts ANOTHER
		// box's broker directory looks just like one that mounts its own.
		if st == StateRunning {
			if st, err = m.reuseBrokerDir(ctx, name, extraRO); err != nil {
				m.rearmIfIdle(name)
				return err
			}
		}
		// The bundle mounts at one fixed destination, so reuseMounts cannot see a
		// stale CA set; the label can (also for a box from before extra CAs).
		if st == StateRunning {
			if st, err = m.reuseCA(ctx, name, caLabel); err != nil {
				m.rearmIfIdle(name)
				return err
			}
		}
		// Removing the box took a docker call, and DEV and PROD share the
		// daemon: another instance may have claimed the folder meanwhile. Check
		// again, so the window before `docker run` is no wider than a fresh
		// box's.
		if st == StateMissing {
			if err := m.claimCheck(ctx, name, ws.Path, exclusive); err != nil {
				m.rearmIfIdle(name)
				return err
			}
		}
	}
	if st == StateRunning && verify != nil && !m.isVerified(name) {
		// Found, not created, by this process: adopted after a restart, or left
		// running by a failed removal. Never trust it unchecked.
		if err := verify(ctx, name); err != nil {
			if !fenced {
				return m.refuseUnverified(ctx, name, err)
			}
			// A fenced box from before egress control (a bound one from M3a, any box
			// on macOS from before egress control for all boxes, or on Linux any box
			// from before EgressAll) fails the probe on its
			// first spawn after the upgrade. With sessions still running in it,
			// removing it would kill them: refuse the spawn and leave it be.
			m.unverify(name)
			m.mu.Lock()
			live := m.active[name]
			m.mu.Unlock()
			if live > 0 {
				m.rearmIfIdle(name) // no-op while sessions are live
				return fmt.Errorf("%w: box %s was started before egress control (or failed its egress check) and still runs %d session(s); end them, then spawn again",
					ErrBoxUnavailable, name, live)
			}
			// Idle: nothing to lose. Remove it and recreate it on the egress network.
			if stopErr := m.opts.Client.Stop(ctx, name); stopErr != nil {
				m.rearmIfIdle(name)
				return fmt.Errorf("%w: box %s failed its egress check and could not be removed: %v", ErrBoxUnavailable, name, stopErr)
			}
			st = StateMissing
		} else {
			m.markVerified(name)
		}
	}
	created := false
	if st == StateMissing {
		var bundle string
		if !m.opts.ExtraCA.Empty() {
			// Before the mount-source check below, which then covers the bundle
			// path too. A box that cannot get its bundle does not start: it would
			// run without the trust the operator configured.
			if bundle, err = m.caBundle(ctx); err != nil {
				m.rearmIfIdle(name)
				return fmt.Errorf("%w: extra_ca_certs: building the trust bundle: %v", ErrBoxUnavailable, err)
			}
		}
		srcs := append([]string{ws.Path, m.stateDir(name), OutputDir(m.opts.StateRoot, name)}, mountSources(extraRO)...)
		if bundle != "" {
			srcs = append(srcs, bundle)
		}
		for _, p := range srcs {
			if reason := MountUnsafe(p); reason != "" {
				m.rearmIfIdle(name)
				return fmt.Errorf("box: mount source %q: %s", p, reason)
			}
		}
		trust := ""
		if preTrust {
			trust = ws.Path
		}
		if _, err := m.ensureStateDir(name, trust, extraRO); err != nil {
			m.rearmIfIdle(name)
			return err
		}
		// Like the home: made by us, as us, before docker can create it as root.
		// Also where the previous run's old screenshots are pruned.
		if _, err := m.prepareOutputDir(name, time.Now()); err != nil {
			m.rearmIfIdle(name)
			return fmt.Errorf("box: preparing output dir: %w", err)
		}
		memory, err := m.memoryCap(ctx)
		if err != nil {
			m.rearmIfIdle(name)
			return fmt.Errorf("%w: %v", ErrBoxUnavailable, err)
		}
		// The identity of each context mount as the VM sees it (macOS), taken as
		// late as it can be and still before the mount exists: a path flipped
		// after this makes docker mount something the in-box check below sees as
		// another file. A box reused instead takes it just before that check.
		if extraRO, err = m.fillVMIDs(ctx, extraRO); err != nil {
			m.rearmIfIdle(name)
			return err
		}
		if _, err := m.opts.Client.Run(ctx, m.runArgs(name, ws.Path, extraRO, fenced, exclusive, bundle, memory)); err != nil {
			// The idle timer was cancelled above. Without re-arming, a box that
			// survives a failed spawn would never be stopped by anyone.
			m.rearmIfIdle(name)
			return fmt.Errorf("box: starting %s: %w", name, err)
		}
		created = true
		if verify != nil {
			if err := verify(ctx, name); err != nil {
				return m.refuseUnverified(ctx, name, err)
			}
			m.markVerified(name)
		}
	}
	// The box runs, created or reused; the agent has not started. A mount that
	// is not the checked path is refused here, before anything runs in it. A
	// reused box has had no VM identity read yet (a created one has, above, and
	// this is then a no-op): the box's mount is old, the path's identity is now.
	if extraRO, err = m.fillVMIDs(ctx, extraRO); err != nil {
		m.rearmIfIdle(name)
		return err
	}
	if err := m.checkMountIDs(ctx, name, extraRO); err != nil {
		return m.refuseMountChanged(ctx, name, created, err)
	}
	// Count the session only once the box is genuinely usable, so a failed spawn
	// cannot leak a reference that keeps the box alive forever.
	m.acquire(name)
	if exclusive {
		m.MarkExclusive(name)
	}
	m.touchMarker(name)
	return nil
}

// reuseMounts decides whether a running box can serve a spawn that asks for
// extraRO, and answers the state ensure should continue from: StateRunning to
// reuse it, StateMissing once it was removed to be recreated.
//
// have is the box's bind destinations as docker reported them. The comparison
// is on in-box destinations, read from docker rather than remembered, so a box adopted after a restart or created by an older build is
// judged by what it really mounts. Host sources are not compared: a box keeps
// the bundle it was started with (session.go logs that per spawn), and a
// source spelled differently by docker must not turn every reuse into a
// recreate.
//
// Every box this build creates also mounts the out dir (PlaywrightOutBox). It is
// not in extraRO — the caller never asks for it — so it is judged here: present
// is the norm, absent is a missing OPTIONAL mount (a box from before it existed
// still runs; its screenshots just stay inside it).
//
//   - Exactly what this spawn would create: reuse.
//   - Idle (no session of this process uses it): removed and recreated, for a
//     missing mount and an extra one alike, so an idle box always matches what
//     a fresh one would be. With partial (the caller could not compute all its
//     mounts) an extra mount is not a difference: "not asked for" then means
//     "unknown", not "must go". Its home is a host directory keyed by the box name,
//     which does not change, so nothing the agent kept is lost.
//   - Live: never removed (M3b spec correction 6). Reused when only optional
//     mounts are missing (the sessions already in it run without them too);
//     refused with the remedy when a required one is missing, because the agent
//     would start pointed at a path that is not there.
//
// Caller holds opMu.
func (m *Manager) reuseMounts(ctx context.Context, name, cwd string, have []string, extraRO []Mount, partial bool) (State, error) {
	got := map[string]bool{}
	hasOut := false
	for _, d := range have {
		d = filepath.Clean(d)
		if d == PlaywrightOutBox {
			hasOut = true // judged below, never an extra mount
			continue
		}
		// The two every box has, and the CA bundle: its presence is judged by
		// the label (reuseCA), never as an extra mount.
		if d != cwd && d != boxHome && d != extraca.ContainerBundle {
			got[d] = true
		}
	}
	want := map[string]bool{}
	var missing, required []string
	if !hasOut {
		missing = append(missing, PlaywrightOutBox) // optional: never in required
	}
	for _, mt := range extraRO {
		d := filepath.Clean(mt.Box)
		want[d] = true
		if got[d] {
			continue
		}
		missing = append(missing, d)
		if !mt.Optional {
			required = append(required, d)
		}
	}
	extra := false
	for d := range got {
		if partial {
			break
		}
		if !want[d] {
			extra = true
			break
		}
	}
	if len(missing) == 0 && !extra {
		return StateRunning, nil
	}
	m.mu.Lock()
	live := m.active[name]
	m.mu.Unlock()
	if live > 0 {
		if len(required) == 0 {
			return StateRunning, nil
		}
		return StateRunning, fmt.Errorf("%w: box %s was started without %s, which this session needs, and still runs %d session(s); end them, then spawn again",
			ErrBoxUnavailable, name, strings.Join(required, ", "), live)
	}
	if err := m.removeIdle(ctx, name); err != nil {
		return StateRunning, fmt.Errorf("box: removing idle %s to recreate it with this session's mounts: %w", name, err)
	}
	return StateMissing, nil
}

// sameDir: a and b name the same directory, spelled alike or through symlinks.
// An unresolvable path matches only an identical spelling.
func sameDir(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b {
		return true
	}
	ra, ea := filepath.EvalSymlinks(a)
	rb, eb := filepath.EvalSymlinks(b)
	return ea == nil && eb == nil && ra == rb
}

// wantsBrokerDir: the spawn asks for the broker socket directory mount.
func wantsBrokerDir(extraRO []Mount) bool {
	return slices.ContainsFunc(extraRO, func(mt Mount) bool { return filepath.Clean(mt.Box) == BrokerMountDest })
}

// checkBrokerMount refuses a spawn whose broker socket directory is not box
// name's own. The broker takes a box for whoever's directory a request arrives
// through, so a box created with another's would be taken for it: a token that
// leaked from that box would work from this one.
func (m *Manager) checkBrokerMount(name string, extraRO []Mount) error {
	own := BrokerDir(m.opts.StateRoot, name)
	for _, mt := range extraRO {
		if filepath.Clean(mt.Box) == BrokerMountDest && !sameDir(mt.Host, own) {
			return fmt.Errorf("%w: the broker socket directory %s offered for box %s is not that box's own (%s); not starting",
				ErrBoxUnavailable, mt.Host, name, own)
		}
	}
	return nil
}

// reuseBrokerDir decides whether a running box mounts its own broker socket
// directory, the way reuseMounts does for mounts: the same directory reuses it;
// another box's removes an idle box (it is recreated with its own) and refuses
// with the remedy while sessions are live in it. A spawn without a broker mount
// asks for nothing; a box whose mount cannot be read is refused (fail closed).
// Caller holds opMu.
func (m *Manager) reuseBrokerDir(ctx context.Context, name string, extraRO []Mount) (State, error) {
	if !wantsBrokerDir(extraRO) {
		return StateRunning, nil
	}
	src, err := m.opts.Client.BindSource(ctx, name, BrokerMountDest)
	if err != nil {
		return StateRunning, fmt.Errorf("%w: box %s: reading its broker mount: %v", ErrBoxUnavailable, name, err)
	}
	if own := BrokerDir(m.opts.StateRoot, name); src != "" && sameDir(src, own) {
		return StateRunning, nil
	}
	m.mu.Lock()
	live := m.active[name]
	m.mu.Unlock()
	if live > 0 {
		return StateRunning, fmt.Errorf("%w: box %s mounts a broker socket directory that is not its own and still runs %d session(s); end them, then spawn again",
			ErrBoxUnavailable, name, live)
	}
	if err := m.removeIdle(ctx, name); err != nil {
		return StateRunning, fmt.Errorf("box: removing idle %s to recreate it with its own broker socket directory: %w", name, err)
	}
	return StateMissing, nil
}

// reuseCA decides whether a running box carries the CA set this manager is
// configured with, the way reuseMounts does for mounts: the same label (or none
// on both sides, including a box from before extra CAs) reuses it; a mismatch
// removes an idle box and refuses with the remedy while sessions are live in it.
// Either direction counts, turning CAs off included: the old bundle is still
// mounted in the box. Caller holds opMu.
func (m *Manager) reuseCA(ctx context.Context, name, label string) (State, error) {
	if label == m.opts.ExtraCA.Hash() {
		return StateRunning, nil
	}
	m.mu.Lock()
	live := m.active[name]
	m.mu.Unlock()
	if live > 0 {
		return StateRunning, fmt.Errorf("%w: box %s started with a different CA set (extra_ca_certs changed) and still runs %d session(s); end them, then spawn again (macOS: or run `agent-box box stop`)",
			ErrBoxUnavailable, name, live)
	}
	if err := m.removeIdle(ctx, name); err != nil {
		return StateRunning, fmt.Errorf("box: removing idle %s to recreate it with the current CA set: %w", name, err)
	}
	return StateMissing, nil
}

// claimCheck enforces exclusive workspaces across every agent-box instance on
// this docker daemon (DEV and PROD share it). An exclusive spawn conflicts with
// any running box over an overlapping folder; any other spawn conflicts only
// with an exclusive one. An idle box of this instance is removed instead of
// blocking, whichever side is exclusive (spec correction 6); if that removal
// fails, the spawn is refused. A failed listing refuses (correction 9).
//
// Rows can come from containers agent-box did not create, so the check fails
// closed on what it cannot read: an unreadable row conflicts with every spawn,
// and a live box with no usable workspace label is taken to overlap. cwd is the
// symlink-resolved workspace. Caller holds opMu.
func (m *Manager) claimCheck(ctx context.Context, name, cwd string, exclusive bool) error {
	boxes, err := m.opts.Client.ListBoxes(ctx, LabelInstance) // the key alone: every instance
	if err != nil {
		return fmt.Errorf("%w: listing boxes for the workspace check: %v", ErrBoxUnavailable, err)
	}
	for _, b := range boxes {
		if b.Name == name || !holdsWorkspace(b.State) {
			continue
		}
		if b.Unreadable {
			return fmt.Errorf("%w: container %q carries agent-box labels that cannot be read, so it may be using %s. "+
				"Remove that container, or use a separate folder or worktree.", ErrWorkspaceHeld, b.Name, cwd)
		}
		if knownWorkspace(b.Workspace) && !Overlaps(b.Workspace, cwd) {
			continue
		}
		if !exclusive && !b.Exclusive {
			continue
		}
		if b.Instance == m.opts.Instance && m.idle(b.Name) && m.removeIdle(ctx, b.Name) == nil {
			continue
		}
		return fmt.Errorf("%w: %s is in use by an AWS-write-capable session (or another session sits in this folder). "+
			"Use a separate folder or worktree, or stop the other session.", ErrWorkspaceHeld, cwd)
	}
	return nil
}

// idle reports whether no session of this process uses the box.
func (m *Manager) idle(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active[name] == 0
}

// removeIdle removes an idle box that stands in a spawn's way, with its idle
// timer and exclusive mark. Caller holds opMu, which also orders it against the
// timer's own stopIfIdle.
func (m *Manager) removeIdle(ctx context.Context, name string) error {
	m.unverify(name)
	if err := m.opts.Client.Stop(ctx, name); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.timers[name]; ok {
		t.Stop()
		delete(m.timers, name)
	}
	delete(m.exclusive, name)
	return nil
}

// MarkExclusive records that a box belongs to one session, so it is removed at
// release rather than kept for its idle TTL. ensure marks the boxes it starts;
// the session layer calls this for a box adopted after a restart.
func (m *Manager) MarkExclusive(name string) {
	if name == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exclusive[name] = true
}

// idleTTL is how long an idle box is kept: zero for an exclusive one, which
// is never reused. Caller holds mu.
func (m *Manager) idleTTL(name string) time.Duration {
	if m.exclusive[name] {
		return 0
	}
	return m.opts.IdleTTL
}

// GrantBlocker names why a grant must wait, or "" (M3b §7.3): any other running
// box, of any instance, over a folder that overlaps cwd. One from a build that
// does not enforce claims (no claims label) is named as such. self is the
// session's own box. Like claimCheck it fails closed: an unreadable row, or a
// live (running, paused or restarting) box with no usable workspace label, blocks.
func (m *Manager) GrantBlocker(ctx context.Context, cwd, self string) (string, error) {
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", fmt.Errorf("box: resolving workspace %q: %w", cwd, err)
	}
	boxes, err := m.opts.Client.ListBoxes(ctx, LabelInstance)
	if err != nil {
		return "", fmt.Errorf("box: listing boxes: %w", err)
	}
	for _, b := range boxes {
		if b.Name == self || !holdsWorkspace(b.State) {
			continue
		}
		switch {
		case b.Unreadable:
			return fmt.Sprintf("container %q carries agent-box labels that cannot be read, so it may use %s; remove it first", b.Name, real), nil
		case !knownWorkspace(b.Workspace):
			return fmt.Sprintf("a box (instance %s) has no usable workspace label, so it may use %s; stop it first", b.Instance, real), nil
		case !Overlaps(b.Workspace, real):
			continue
		case !b.Claims:
			return fmt.Sprintf("a box started by an older agent-box (instance %s) uses %s; stop it or upgrade that instance first", b.Instance, b.Workspace), nil
		}
		return fmt.Sprintf("another box (instance %s) uses %s; stop the session in it first", b.Instance, b.Workspace), nil
	}
	return "", nil
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
	m.timers[name] = time.AfterFunc(m.idleTTL(name), func() { m.stopIfIdle(name) })
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
// a project does not pay start-up again. An exclusive box is removed at once
// (M3b spec correction 7): it belongs to the session that just ended.
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
	m.timers[name] = time.AfterFunc(m.idleTTL(name), func() { m.stopIfIdle(name) })
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
	m.mu.Lock()
	delete(m.exclusive, name)
	m.mu.Unlock()
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
	_, scratchErrs := m.reapCAScratch(ctx)
	return errors.Join(append(errs, scratchErrs...)...)
}

// reapCAScratch removes the CA-extraction containers a crash left behind (see
// LabelCAScratch). Caller holds opMu, which caBundle also holds, so one being
// used right now is never seen here.
func (m *Manager) reapCAScratch(ctx context.Context) (removed []string, errs []error) {
	names, err := m.opts.Client.ListByLabel(ctx, LabelCAScratch+"="+m.opts.Instance)
	if err != nil {
		return nil, []error{fmt.Errorf("box: listing CA scratch containers: %w", err)}
	}
	for _, name := range names {
		if err := m.opts.Client.Stop(ctx, name); err != nil {
			errs = append(errs, fmt.Errorf("box: reaping CA scratch container %s: %w", name, err))
			continue
		}
		removed = append(removed, name)
	}
	return removed, errs
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
			m.timers[name] = time.AfterFunc(m.idleTTL(name), func() { m.stopIfIdle(name) })
			m.mu.Unlock()
			rep.Corrected = append(rep.Corrected, SweepAction{b.Name,
				fmt.Sprintf("counter said %d live session(s), the registry says 0 on two sweeps; idle timer armed", count)})
		}
	}
	m.mu.Lock()
	m.drift = nextDrift
	m.mu.Unlock()
	removed, scratchErrs := m.reapCAScratch(ctx)
	for _, name := range removed {
		rep.Removed = append(rep.Removed, SweepAction{name, "CA extraction container left by a crash"})
	}
	return rep, errors.Join(append(errs, scratchErrs...)...)
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
	m.mu.Lock()
	if m.active[name] == 0 { // no release will come to clear it
		delete(m.exclusive, name)
	}
	m.mu.Unlock()
	return nil
}
