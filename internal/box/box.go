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
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/culiops/culibox/internal/extraca"
	"github.com/culiops/culibox/internal/wsroots"
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
	// LabelIdentity marks a box created by a build that checks, when it starts,
	// that the workspace and its guards are the folder and files that passed the
	// checks (#108). A box without it was never checked.
	LabelIdentity = "io.culiops.agent-box.identity"
	// LabelCAScratch marks the never-started container caBundle creates to read
	// the image's trust bundle. It is deliberately not LabelInstance: claimCheck
	// and Sweep select boxes by that key and a leftover would read as a box with
	// no workspace, blocking every spawn. Reconcile and Sweep reap it by this one.
	LabelCAScratch = "io.culiops.agent-box.ca-scratch"
	namePrefix     = "ab-box-"
	stateDirName   = "boxstate"
	// lastUsedDirName holds one empty file per box whose mtime is the last-use
	// signal: the last spawn, or the end of the last session. It is not a box
	// name, so the reclaim sweep never considers it.
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
	// path or an in-place guard, on Linux read on the host). Once the box runs,
	// ensure refuses it unless Box shows the same identity inside it
	// (checkMountIDs).
	ID *FileID
	// CheckID marks a mount whose identity must be checked even though the host
	// recorded none (a context path on macOS, where a host inode number is not
	// the one the box sees). With no ID and Options.VMIDs set, ensure reads the
	// identity inside the VM before the mount is made, and checkMountIDs then
	// compares it like any other. Context mounts set it, and so do the workspace
	// mount ensure adds and the in-place guards of the workspace (which
	// inPlaceMounts already refuses for being symlinks): a config overlay is
	// never refused for being a symlink.
	CheckID bool
	// kind says what the mount is for, which only decides the refusal when its
	// identity does not match (mountChanged). Unexported, so a caller outside
	// this package cannot mark its mount as the workspace.
	kind mountKind
}

// mountKind is what an identity-checked mount stands for.
type mountKind uint8

const (
	mountExtra     mountKind = iota // a caller's mount, a context path
	mountWorkspace                  // the workspace
	mountGuard                      // an in-place guard of the workspace
)

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
// On macOS the comparison ignores case, like ProtectedOverlap: APFS does by
// default, so /Users/a/Proj is /Users/a/proj. A case-sensitive APFS volume
// therefore sees a false overlap; that is the safe side and is accepted.
// Unicode normalisation (NFC/NFD) is not folded.
func Overlaps(a, b string) bool {
	return overlaps(a, b, runtime.GOOS == "darwin")
}

// overlaps is Overlaps with the case folding as a parameter, so Linux tests can
// pin the darwin behaviour. It shares pathWithin with ProtectedOverlap.
func overlaps(a, b string, fold bool) bool {
	return pathWithin(a, b, fold) || pathWithin(b, a, fold)
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

// underMount is UnderMount for host paths: with fold set (macOS, where APFS is
// case-insensitive by default) X and x are one folder, so it ignores letter
// case, like Overlaps. Unicode normalisation (NFC/NFD) is not folded. Bundle
// keeps the case-sensitive UnderMount on purpose: its host-path checks are mostly
// "outside, so refuse", where folding would widen access, and its deny list is
// backed by an identity check.
func underMount(path string, mounts []string, fold bool) bool {
	for _, mt := range mounts {
		if pathWithin(path, mt, fold) {
			return true
		}
	}
	return false
}

// BoxLabels are the labels of a box that ensure reads when it finds one running.
type BoxLabels struct {
	// CA is the extraca.Label value, "" when the box has none.
	CA string
	// Identity is whether the box carries LabelIdentity=1: it was created by a
	// build that checks the workspace and guard identity at start.
	Identity bool
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
	HasRuntime(ctx context.Context, name string) (bool, error)
	// InspectMounts is Inspect plus the in-box destinations of the box's bind
	// mounts, as docker reports them: the truth for every box, adopted ones and
	// ones created by an older build included. Mounts are nil unless running.
	// The third value is the box's labels that ensure reads.
	InspectMounts(ctx context.Context, name string) (State, []string, BoxLabels, error)
	// BindSource is the host path of the running box's bind mount at the in-box
	// destination dst ("" when it has none). Only the broker's mount is read this
	// way (reuseBrokerDir), so a box pays the extra call only when it has one.
	BindSource(ctx context.Context, name, dst string) (string, error)
	// BindSources is BindSource for every bind of the running box at once (in-box
	// destination → host path), for a check that needs several: whether the
	// mounts reuseMounts did not ask for are all kept folders (keptOnly).
	BindSources(ctx context.Context, name string) (map[string]string, error)
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
	// OnStop is told that this process stopped a box, after the stop succeeded,
	// with the box's workspace (symlinks resolved; "" when it could not be
	// read). It runs in its own goroutine and outside opMu, so it may take as
	// long as it likes and may call back into the Manager. Fired by the idle
	// stop, the removal of an idle box in a spawn's way, and the sweep's
	// removal; nil = no notice.
	OnStop func(name, workspace string)
	// Audit records one row of what the keep pass did (keptpass.go): action
	// box_kept or box_kept_held, name the store's profile, detail as the pass
	// writes it. A failure is logged and never stops the pass; nil = no rows.
	Audit func(action, name, detail string) error
	// VMIDs reads the identity of each host path as the docker daemon's kernel
	// sees it, in order (on macOS: inside the VM, since a Mac inode number is
	// not the one virtiofs shows the box). ensure calls it, once, for the
	// workspace (always asked, first) and the mounts that are CheckID with no ID,
	// before the box is created. An error or a wrong count refuses the spawn.
	// nil = such mounts are not checked (Linux records the identity on the host
	// itself, the workspace's included).
	VMIDs func(ctx context.Context, paths []string) ([]FileID, error)
	// Shares reads the live list of host folders the docker daemon can see; a
	// workspace outside the applied ones (or read-only) is refused. nil = no VM,
	// every path is visible.
	Shares SharesFunc
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
	// LockGuards (macOS only, #96; needs GuardInPlace) also locks the guarded
	// single files on the host while a box spawned in place runs, on a
	// case-insensitive volume, where the read-only bind covers only one
	// spelling of a file's name (see guardLocks). New refuses it on an OS
	// without file flags. false: no lock, no record, no file in the data root.
	LockGuards bool
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
	opts Options

	// rtMu guards the runtime fields. runtime is what new boxes get; rtKnown
	// is whether a definitive answer from docker decided it (until then runtime
	// is the runc placeholder and no box is created, #334). rtProbe is open
	// while a badge read's background probe runs; rtTried is when one last
	// started. Never held across a docker call.
	rtMu    sync.Mutex
	runtime string
	rtKnown bool
	rtProbe chan struct{}
	rtTried time.Time

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
	// mismatched holds the boxes that failed a mount check and whose removal
	// failed: they still run, with a mount that is not the checked one. ensure
	// removes one before it can be reused (removeMismatched), and refuses the
	// spawn while it cannot. Cleared when the box is gone (boxGone). Guarded by mu.
	mismatched map[string]bool
	// guardsChecked holds the boxes without LabelIdentity (an older build's,
	// never checked) whose guards this process has compared once, so a later
	// editor save does not refuse every session. Cleared by boxGone. Guarded by mu.
	guardsChecked map[string]bool
	// drift marks boxes whose counter disagreed with the registry at the last
	// sweep (guarded by mu).
	drift map[string]bool
	// egress is set once at boot by SetEgress (guarded by mu).
	egress Egress
	// exclusive marks boxes that belong to one session (BoxKey.Session, M3b):
	// they are removed as soon as their last session is released, with no
	// idle-TTL reuse. Set by ensure and, after a restart, by MarkExclusive.
	exclusive map[string]bool
	// owner is the session an exclusive box belongs to (BoxKey.Session), and
	// adoptedAt when this process adopted it after a restart (#325): what the
	// write-hold hook is asked about. Set by Wrap and AdoptExclusive, cleared
	// with exclusive. Guarded by mu.
	owner     map[string]string
	adoptedAt map[string]time.Time
	// ownerWS is the workspace (symlinks resolved) of an exclusive box this
	// process started for its owner, set by Wrap with owner. An adopted box's
	// comes from its label when it is needed.
	ownerWS map[string]string
	// ended is, by box name, the write hold of a session whose exclusive box
	// is gone (#333): the box goes as soon as its session ends, but write
	// credentials it was given may outlive it. claimCheck refuses the folder
	// while the hook still holds the session, and drops an entry once it does
	// not. Guarded by mu.
	ended map[string]endedHold
	// conv is, per box, the key of the conversations folder it mounts ("" when it
	// mounts none: a box from before #330, or a spawn with no profile), as ensure
	// last found or made it. Dropped when the box is gone. Guarded by mu.
	conv map[string]string
	// adopt, when set, is told before each move adoptConversations makes out of
	// a home (SetAdoptHook). Guarded by mu.
	adopt AdoptHook
	// started is when this Manager was made: write credentials minted before a
	// restart cannot outlive it by more than bootHoldFor, so an orphan's hold
	// (keepEndedHold with no owner) counts from here.
	started time.Time
	// writeHold is the hook SetWriteHold installs (guarded by mu); nil = none.
	writeHold WriteHold
	// hostHolds is the hook SetHostHolds installs (guarded by mu); nil = none.
	hostHolds HostHolds
	// memTotal is docker's MemTotal once read (0 = not yet); memFallbackLogged
	// stops the unreadable-MemTotal fallback from logging on every spawn. Both
	// guarded by mu.
	memTotal          int64
	memFallbackLogged bool
	// locks are the macOS guard locks (Options.LockGuards); nil = off.
	locks *guardLocks
	// keptMu is, per kept store key, the lock a keep pass holds from reading
	// the store's state.json to writing it back, so two passes of one store
	// never interleave. Taken before opMu, never while holding it. The map is
	// guarded by mu (keptStoreLock).
	keptMu map[string]*sync.Mutex
	// keptPasses counts the keep passes running in the background, started by
	// stopped() and StartKeepStoppedHomes (WaitKeptPasses). Each is bounded:
	// keptPassBudget, keptBootBudget.
	keptPasses sync.WaitGroup
	// keptBadAttr holds the store keys whose state.json named a profile it
	// does not belong to, once logged. Guarded by mu.
	keptBadAttr map[string]bool
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

		verified:      map[string]bool{},
		mismatched:    map[string]bool{},
		guardsChecked: map[string]bool{},
		drift:         map[string]bool{},
		exclusive:     map[string]bool{},
		owner:         map[string]string{},
		adoptedAt:     map[string]time.Time{},
		ownerWS:       map[string]string{},
		ended:         map[string]endedHold{},
		conv:          map[string]string{},
		keptMu:        map[string]*sync.Mutex{},
		started:       time.Now(),
	}
	if opts.LockGuards {
		ops := platformFlagOps()
		if ops == nil || !opts.GuardInPlace {
			return nil, fmt.Errorf("box: in-place guard locks need the in-place guard and file flags (macOS)")
		}
		m.locks = newGuardLocks(opts.StateRoot, ops)
	}
	// Bounded: a daemon that accepts and never answers must not hold the boot.
	// A daemon that is stopped, not permitted or slow is not an error: the layer
	// exists, Ping (daemon.go) says why it cannot be used, and the runtime stays
	// unknown until docker answers (settledRuntime, #334). Only a definitive
	// answer decides it, and then for the life of the process.
	pctx, cancel := context.WithTimeout(context.Background(), runtimeProbeTimeout)
	defer cancel()
	if ok, err := opts.Client.HasRuntime(pctx, runtimeSysbox); err == nil {
		m.rtKnown = true
		if ok {
			m.runtime = runtimeSysbox
		}
	} else {
		log.Printf("box: could not read docker's runtimes (%v); they are read again before the first box is created", err)
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

// WriteHold says why the session that owns an exclusive box may hold AWS write
// credentials right now: a write window open or being granted, write
// credentials from a dropped window still valid, or (adoptedAt set: the box
// was adopted after a restart at that time) credentials issued before the
// restart that may still be valid. "" = it holds none, and other boxes may use
// its folder (#325). The sentence names the session; it is shown as is.
//
// It is called with opMu held, so it must not call back into the Manager.
type WriteHold func(sessionID string, adoptedAt time.Time) string

// SetWriteHold installs the write-hold hook. Call it once at boot, before any
// boxed spawn. Without it, an exclusive box of this instance refuses every
// other spawn into its folder, as no window can be ruled out.
func (m *Manager) SetWriteHold(f WriteHold) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writeHold = f
}

// HostHold is a Host session (#360) that may hold AWS write credentials: its
// folder (symlink-resolved; "" = not known, which overlaps every folder) and
// why, a WriteHold answer led by the session's name. Ended: the session has
// ended, and write credentials it was given may still be valid (#333).
type HostHold struct {
	Workspace, Why string
	Ended          bool
}

// HostHolds lists the Host sessions that hold their folder now. Host sessions
// have no box, so no container listing shows them. It is called with opMu held,
// so it must not call back into the Manager.
type HostHolds func() []HostHold

// SetHostHolds installs the Host-hold hook. Call it once at boot, before any
// boxed spawn. Without it no Host session holds a folder (none can be granted
// a window: the hook comes with the broker).
func (m *Manager) SetHostHolds(f HostHolds) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hostHolds = f
}

// RuntimeSysbox and RuntimeRunc are the isolation runtimes a box can get, for
// a reader outside this package (doctor).
const (
	RuntimeSysbox = runtimeSysbox
	RuntimeRunc   = runtimeRunc
)

// runtimeProbeTimeout bounds one ask for docker's runtimes (boot, a spawn, a
// badge read); runtimeRetry is the least time between two asks a badge read
// starts, so a page polling a stopped daemon does not run docker every time.
// Vars so a test can shorten them.
var (
	runtimeProbeTimeout = ProbeTimeout
	runtimeRetry        = 2 * time.Second
)

// Runtime reports the isolation runtime boxes get: the detected one, or runc
// while it is not known yet (no box is created then; see settledRuntime). It
// never asks docker.
func (m *Manager) Runtime() string {
	rt, _ := m.RuntimeKnown()
	return rt
}

// RuntimeKnown is Runtime and whether a definitive answer from docker decided
// it. false: the boot probe could not read docker's runtimes (Docker stopped,
// starting or slow when CuliBox started), and they are read again before the
// next box is created (#334).
func (m *Manager) RuntimeKnown() (string, bool) {
	m.rtMu.Lock()
	defer m.rtMu.Unlock()
	return m.runtime, m.rtKnown
}

// RuntimeFresh is Runtime for the UI badge (/api/meta). While the runtime is
// not known it starts one bounded probe in the background (one at a time, and
// not again within runtimeRetry) and waits for it up to wait, so a page
// reloaded after the user starts Docker shows what the next box will get. Once
// known it never asks docker again.
func (m *Manager) RuntimeFresh(wait time.Duration) string {
	m.rtMu.Lock()
	if m.rtKnown {
		rt := m.runtime
		m.rtMu.Unlock()
		return rt
	}
	ch := m.rtProbe
	if ch == nil && time.Since(m.rtTried) >= runtimeRetry {
		ch = make(chan struct{})
		m.rtProbe, m.rtTried = ch, time.Now()
		go func() {
			defer close(ch)
			ctx, cancel := context.WithTimeout(context.Background(), runtimeProbeTimeout)
			defer cancel()
			if ok, err := m.opts.Client.HasRuntime(ctx, runtimeSysbox); err == nil {
				m.settleRuntime(ok)
			}
			m.rtMu.Lock()
			m.rtProbe = nil
			m.rtMu.Unlock()
		}()
	}
	m.rtMu.Unlock()
	if ch != nil && wait > 0 {
		t := time.NewTimer(wait)
		select {
		case <-ch:
		case <-t.C:
		}
		t.Stop()
	}
	return m.Runtime()
}

// settledRuntime is the runtime a new box gets, asking docker first when no
// definitive answer has decided it yet (#334: a boot with Docker down must not
// leave every later box on runc). It waits for a probe a badge read already
// started, then asks itself if that one did not settle it: at most two bounded
// asks. An error means docker's runtimes still cannot be read, and the caller
// creates no box: one created now would be on runc only because the probe
// failed. Once settled it never asks again, so a spawn pays nothing.
func (m *Manager) settledRuntime(ctx context.Context) (string, error) {
	m.rtMu.Lock()
	rt, known, ch := m.runtime, m.rtKnown, m.rtProbe
	m.rtMu.Unlock()
	if known {
		return rt, nil
	}
	if ch != nil {
		select {
		case <-ch:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		if rt, known = m.RuntimeKnown(); known {
			return rt, nil
		}
	}
	pctx, cancel := context.WithTimeout(ctx, runtimeProbeTimeout)
	defer cancel()
	ok, err := m.opts.Client.HasRuntime(pctx, runtimeSysbox)
	if err != nil {
		return "", err
	}
	return m.settleRuntime(ok), nil
}

// settleRuntime records a definitive answer once and returns the runtime. A
// later answer never changes a settled one.
func (m *Manager) settleRuntime(sysbox bool) string {
	m.rtMu.Lock()
	defer m.rtMu.Unlock()
	if !m.rtKnown {
		m.runtime, m.rtKnown = runtimeRunc, true
		if sysbox {
			m.runtime = runtimeSysbox
		}
		log.Printf("box: isolation runtime %s (detected once docker answered)", m.runtime)
	}
	return m.runtime
}

// runtimeProbing reports whether a badge read's background probe still runs.
func (m *Manager) runtimeProbing() bool {
	m.rtMu.Lock()
	defer m.rtMu.Unlock()
	return m.rtProbe != nil
}

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
	// CopyDir is the private copy (a clone directory, symlinks resolved) this spawn
	// runs in without a clone setup of its own, as the quick shell beside a clone
	// session does (#371); "" = none. Wrap skips the refusal of a git work tree in
	// place, and only that, when its own resolution of the workspace is at or under
	// CopyDir; a workspace that resolves anywhere else is refused as without it.
	// Every guard is derived as for any in-place workspace. Set by the session
	// layer from the clone hooks' positive match. Not identity: nameFor ignores it.
	CopyDir string
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
	// PreAcceptBypass asks for Claude Code's "Bypass Permissions mode" warning to
	// be pre-accepted in the box home (see acceptBypassPermissions). Not identity,
	// like PreTrust: it is applied when the box is created. The session layer sets
	// it only when the spawn creating the box runs the agent with the bypass option
	// (--dangerously-skip-permissions or --permission-mode bypassPermissions), so
	// the option the user chose is the consent. Host-tier sessions never get here.
	PreAcceptBypass bool
	// NoImports is set when the profile turns CLAUDE.md imports off, so the box
	// carries no import mounts. It is part of the identity, like Context:
	// reuseMounts treats a box with extra mounts as fine for a spawn that asks
	// for fewer, so without it an imports-off profile could land in a live box
	// that still mounts imports.
	NoImports bool
	// Profile is the name of the profile the session runs (#330). Not identity
	// (nameFor ignores it): with it set, a box this spawn creates mounts the
	// profile's conversations folder (ConversationsKey of the instance, the
	// workspace's real path, the account and this name) over its projects dir,
	// so conversations follow the profile across boxes of other scopes.
	// "" mounts no folder.
	Profile string
}

// CloneMounts is the clone-mode part of a BoxKey. Hash identifies the clone spec
// (config.Clone.Hash); RO are the read-only mounts the clone needs: the source
// repo's .git, the shared paths and the memory dir. Wrap appends them to the
// extra read-only binds. A clone adds no writable bind (spec D12): the writable
// mounts of a box stay the ones runArgs makes for every box.
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

// NamedFor reports whether the box Wrap named for key is the one a workspace at
// the real path `real` gets: Wrap names a box after the path it resolved, so a
// caller that resolved the same path itself can tell the two agree (a symlink
// moved in between makes them differ).
func (m *Manager) NamedFor(real string, key BoxKey, name string) bool {
	return m.nameFor(real, key) == name
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

// touchMarker records that a session used this box now. It runs when a spawn
// lands (ensure), when the last session is released (ReleaseBox) and when Sweep
// clears a counter whose release never came. Best effort: a failure here must
// never fail a spawn or a release. The cost of losing it is a home that looks
// older than it is, and the reclaim sweep refuses on two further grounds anyway.
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
	return ensureMountpointsUnder(root, boxHome, mounts)
}

// ensureMountpointsUnder is ensureMountpoints for a root that is mounted at the
// in-box path base (the home, or the conversations folder at ConversationsBox).
func ensureMountpointsUnder(root *os.Root, base string, mounts []Mount) error {
	for _, mt := range mounts {
		rel, err := filepath.Rel(base, mt.Box)
		if err != nil || rel == "." || rel == ".." ||
			strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue // outside base: it lands on the read-only rootfs (or another mount)
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
func unboxableWorkspace(cwd string, protected []ProtectedDir, view func() (*ShareView, error)) (string, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", nil
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	} else {
		// A directory that does not exist yet (a profile's default, say) is still
		// judged by where it would be created: the existing part of its path
		// resolved, as ProtectedOverlap always did.
		abs = resolveExisting(abs)
	}
	return unboxableResolved(abs, protected, view)
}

// WorkspaceRefusal is the workspace rule without the box VM's share list: the
// reason cwd cannot be a box's workspace (the filesystem root, home or an
// ancestor of it, a protected CuliBox directory, an unsafe mount source), or ""
// when it is allowed. `culibox init` and Manager.UnboxableIgnoringShares both
// use it, so init refuses the folders `box` refuses. With no share view the
// error is always nil; it stays in the signature to match unboxableWorkspace.
func WorkspaceRefusal(cwd string, protected []ProtectedDir) (reason string, err error) {
	return unboxableWorkspace(cwd, protected, nil)
}

// unboxableResolved is unboxableWorkspace for a path that is already absolute and
// symlink-resolved (workspace.Path), which it does not resolve again: the
// protected check in it (protectedOverlapResolved) is about this very path.
//
// It returns a reason, or, for a share failure, the *NotSharedError itself
// (the spawn API needs the type); both empty = boxable. view reads the live
// share list (nil view = no VM).
func unboxableResolved(abs string, protected []ProtectedDir, view func() (*ShareView, error)) (string, error) {
	if reason := MountUnsafe(abs); reason != "" {
		return reason, nil
	}
	if abs == string(filepath.Separator) {
		return "it is the filesystem root", nil
	}
	var resolvedHome string
	if home, err := os.UserHomeDir(); err == nil {
		if real, err := filepath.EvalSymlinks(home); err == nil {
			home = real
		}
		if reason := homeReason(abs, home); reason != "" {
			return reason, nil
		}
		resolvedHome = home
	}
	// System and credential folders (#343). DataDir stays empty: the protected
	// list below already covers CuliBox's data.
	if reason := wsroots.Refusal(abs, wsroots.Env{Home: resolvedHome}); reason != "" {
		return reason, nil
	}
	if reason := protectedOverlapResolved(abs, protected); reason != "" {
		return reason, nil
	}
	if view != nil {
		v, err := view()
		if err != nil {
			return "cannot read the box VM's folder list (" + err.Error() + "); run `culibox doctor`", nil
		}
		if v != nil {
			if err := v.Check(abs, true); err != nil {
				return "", err
			}
		}
	}
	return "", nil
}

// errUnboxable is Wrap's refusal of a workspace unboxableWorkspace (or
// unboxableResolved) gave a reason for. It names cwd, the path the user chose.
func errUnboxable(cwd, reason string) error {
	return fmt.Errorf("%w: %s (%s). Pick a project directory, "+
		"or choose \"Run on trusted host\" to run it uncontained on purpose",
		ErrUnboxableWorkspace, cwd, reason)
}

// refuseUnboxable is Wrap's refusal: the typed *NotSharedError as it is (it
// names the folder and the fix), else errUnboxable's wording of the reason.
func refuseUnboxable(cwd, reason string, serr error) error {
	if serr != nil {
		return serr
	}
	return errUnboxable(cwd, reason)
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
	reason, err := unboxableWorkspace(cwd, m.protected(), m.opts.view)
	if err != nil {
		return err.Error(), true
	}
	return reason, reason != ""
}

// UnboxableIgnoringShares is Unboxable without the VM share rule: the verdict
// for a record that must not change when the user edits the VM folder list
// (the boxed-workspace registry feeds the fs containment trees).
func (m *Manager) UnboxableIgnoringShares(cwd string) (string, bool) {
	reason, err := WorkspaceRefusal(cwd, m.protected())
	if err != nil {
		return err.Error(), true
	}
	return reason, reason != ""
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
	real, err := resolvePath(cwd)
	if err != nil {
		// A workspace that does not exist yet and overlaps a protected directory
		// (a config dir not created yet) is still refused for that reason, which
		// says what to do, before the plain resolve failure: as it always was.
		// Nothing is created either way; this only words the refusal. It is for a
		// resolution that failed here, never for a path whose identity cannot be
		// read below: that one changed, whatever it leads to now.
		if reason, serr := unboxableWorkspace(cwd, m.protected(), m.opts.view); serr != nil || reason != "" {
			return nil, nil, "", refuseUnboxable(cwd, reason, serr)
		}
		return nil, nil, "", err
	}
	ws, err := workspaceAt(cwd, real, workspaceID)
	if err != nil {
		return nil, nil, "", err
	}
	if wrapResolvedHook != nil {
		wrapResolvedHook(ws)
	}
	if reason, serr := unboxableResolved(ws.Path, m.protected(), m.opts.view); serr != nil || reason != "" {
		return nil, nil, "", refuseUnboxable(cwd, reason, serr)
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
	var lk *lockReq // the macOS guard locks this spawn needs; nil = none
	if key.Clone != nil {
		if key.Clone.Hash == "" {
			return nil, nil, "", errors.New("box: a clone-mode spawn needs the clone's hash")
		}
		// Ordinary read-only extras: MountUnsafe, reuseMounts (a live box missing
		// a required clone mount is refused, an idle one recreated) and runArgs
		// all see them, and runArgs renders every extra read-only.
		extraRO = append(slices.Clip(extraRO), key.Clone.RO...)
	}
	// The spawn checked this folder inside a private copy (key.CopyDir). Wrap
	// resolved the path again, seconds later and after the copy's box may have run:
	// if it now leads outside the copy, the folder was re-pointed (a link planted
	// in the copy). Refused outright, never judged as a plain in-place folder: a
	// folder that is not git would box in place with nothing recorded. Exact case,
	// component-wise: folding the case here would only widen what is skipped.
	copied := false
	if key.CopyDir != "" && key.Clone == nil {
		if !pathWithin(ws.Path, key.CopyDir, false) {
			return nil, nil, "", fmt.Errorf("%w: %s is not inside the private copy %s it was checked in: the folder changed since the spawn was checked. "+
				"Start the session again", ErrUnboxableWorkspace, ws.Path, key.CopyDir)
		}
		copied = true
	}
	// A clone workspace is the box's own tree, rewritable by the box by design;
	// guarding its .git/.claude would only protect the box from itself.
	if m.opts.GuardInPlace && key.Clone == nil {
		// A git work tree never boxes in place (#371): it boxes as a private copy.
		// Before the guards, which stay as the second net for what this misses.
		// Read from ws, the single resolution above, as the guards are. A folder
		// inside the private copy it was checked in is skipped, and only that.
		if !copied {
			if err := gitInPlaceRefusal(ws.Path); err != nil {
				return nil, nil, "", err
			}
		}
		// Derived from ws, the single resolution above, which is what docker
		// mounts. Appended as ordinary read-only extras, so MountUnsafe,
		// reuseMounts (a live box without a guard is refused, an idle one
		// recreated) and runArgs all see them. Clip: never write into the caller's
		// backing array.
		guard, files, err := inPlaceGuardsAt(ws)
		if err != nil {
			return nil, nil, "", err
		}
		extraRO = append(slices.Clip(extraRO), m.vmView(guard)...)
		if m.locks != nil {
			lk = &lockReq{ws: ws.Path, files: files, session: sessionID}
		}
	}
	// Last, so the clone and guard mounts above count as covering too.
	extraRO = yieldCovered(ws.Path, extraRO)
	if key.Session != "" {
		// Before ensure: from the moment the box runs, another spawn's
		// claimCheck must be able to ask whether its session holds a window.
		// The name hashes key.Session, so the owner never changes.
		m.mu.Lock()
		m.owner[name] = key.Session
		m.ownerWS[name] = ws.Path // what the session's hold covers once the box is gone (#333)
		m.mu.Unlock()
	}
	// The profile's conversations folder, from the same resolution as the name
	// (#330). Not identity: the name above does not hash it.
	conv := m.ConversationsKeyFor(ws.Path, key.Account, key.Profile)
	if err := m.ensure(ctx, name, ws, extraRO, conv, keptOwner{Profile: key.Profile, Account: key.Account}, key.PartialMounts, key.Binding != "", key.Session != "", key.PreTrust, key.PreAcceptBypass, lk); err != nil {
		if key.Session != "" {
			m.mu.Lock()
			if !m.exclusive[name] { // no box of this session counted: nothing to own
				delete(m.owner, name)
				delete(m.ownerWS, name)
			}
			m.mu.Unlock()
		}
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
//
// lk, when set, are the guard locks the box must hold before anything runs in
// it: taken right before `docker run`, or, for a box already running, before
// the session is counted. A lock that cannot be taken refuses the spawn.
//
// conv is the key of the conversations folder the spawn asks for ("" none): a
// box created here mounts it; a running one is judged by reuseConversations.
// Either way the key of the folder the box ends up mounting is remembered for
// ConversationsKeyOf. conv is also the key of the profile's kept-folder store:
// a box created here mounts its kept folders (prepareKept), and owner is whose
// spawn it is, for the box's record.
func (m *Manager) ensure(ctx context.Context, name string, ws workspace, extraRO []Mount, conv string, owner keptOwner, partial, bound, exclusive, preTrust, preAcceptBypass bool, lk *lockReq) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	// The workspace is checked like a context mount: what the box shows at its
	// path must be the directory that was resolved and checked. With a VM hook
	// the identity is the VM's, whatever the host recorded.
	wsm := ws.mount(m.opts.VMIDs != nil)

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
	st, have, labels, err := m.opts.Client.InspectMounts(ctx, name)
	boxConv := "" // the key of the folder the box mounts, once known
	if err != nil {
		m.rearmIfIdle(name)
		return fmt.Errorf("box: inspecting %s: %w", name, err)
	}
	if st == StateMissing {
		// No container: nothing can be left over from one that failed a check.
		m.mu.Lock()
		delete(m.mismatched, name)
		m.mu.Unlock()
	}
	if st == StateStopped {
		// Its agents are already dead (docker exec does not survive the
		// container), and its name blocks a fresh `docker run`.
		m.unverify(name)
		if err := m.opts.Client.Stop(ctx, name); err != nil {
			m.rearmIfIdle(name)
			return fmt.Errorf("box: removing stopped %s: %w", name, err)
		}
		m.boxGone(name)
		st = StateMissing
	}
	if st == StateRunning && m.isMismatched(name) {
		// A box that failed a mount check earlier and could not be removed then:
		// its guards are not compared on reuse, so it must not be reused. Removed
		// now, and the spawn creates one that is checked; refused while it stays.
		if st, err = m.removeMismatched(ctx, name); err != nil {
			m.rearmIfIdle(name)
			return err
		}
		// Removing it took a docker call: claim the folder again, as for any
		// box removed to be recreated.
		if err := m.claimCheck(ctx, name, ws.Path, exclusive); err != nil {
			m.rearmIfIdle(name)
			return err
		}
	}
	if st == StateRunning {
		// Mounts are fixed at `docker run`, and the box key does not say which
		// agent will run in the box, so check the box can serve this spawn
		// before anything else trusts it (brain #131).
		if st, err = m.reuseMounts(ctx, name, ws.Path, have, extraRO, partial, conv != ""); err != nil {
			m.rearmIfIdle(name)
			return err
		}
		// Destinations alone cannot tell one profile's folder from another's.
		if st == StateRunning {
			if st, boxConv, err = m.reuseConversations(ctx, name, have, conv); err != nil {
				m.rearmIfIdle(name)
				return err
			}
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
			if st, err = m.reuseCA(ctx, name, labels.CA); err != nil {
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
			m.boxGone(name)
			st = StateMissing
		} else {
			m.markVerified(name)
		}
	}
	created := false
	// A box this build checked when it started has its guards compared only then.
	// One without the label was never checked: its guards are compared once, by
	// this process (see skipGuard).
	guards := st == StateRunning && !labels.Identity && !m.isGuardsChecked(name)
	if st == StateMissing {
		// First, before anything is made for the box: which runtime it gets. A
		// boot with Docker down left it unknown; it is read now (#334), and while
		// docker cannot say, no box is created on the runc guess. The browser
		// gets a fixed sentence; docker's own text (it can name a socket path)
		// goes to the log.
		if _, err := m.settledRuntime(ctx); err != nil {
			m.rearmIfIdle(name)
			log.Printf("box: %s: could not read docker's runtimes: %v", name, err)
			return fmt.Errorf("%w: could not read which isolation runtime Docker offers, so no box is created on a guess; try again once Docker answers, or run `culibox doctor`", ErrBoxUnavailable)
		}
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
		convDir := ""
		if conv != "" {
			if convDir, err = ensureConversationsDir(m.opts.StateRoot, conv); err != nil {
				m.rearmIfIdle(name)
				return err
			}
			srcs = append(srcs, convDir)
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
		// With a conversations folder, the mounts that land inside it get their
		// mountpoints there, not in the home it covers.
		homeRO, convRO := extraRO, []Mount(nil)
		if convDir != "" {
			homeRO, convRO = splitUnder(extraRO, ConversationsBox)
		}
		if _, err := m.ensureStateDir(name, trust, homeRO); err != nil {
			m.rearmIfIdle(name)
			return err
		}
		if convDir != "" {
			dropped, err := m.ensureConversationsMount(name, convDir, convRO)
			if err != nil {
				m.rearmIfIdle(name)
				return err
			}
			// An optional mount whose mountpoint was not plain is left out: never
			// handed to docker over what another box put there.
			extraRO = slices.DeleteFunc(slices.Clone(extraRO), func(mt Mount) bool {
				return slices.ContainsFunc(dropped, func(d Mount) bool { return d.Box == mt.Box && d.Host == mt.Host })
			})
			// No container, so nothing writes in the home: what it holds moves
			// into the folder before the mount hides it.
			m.adoptConversations(ctx, name, convDir)
		}
		// The profile's kept folders, as its store holds them now: checked, their
		// mountpoints made in the home, and the box's record written (with no
		// profile, one left from a profile's spawn removed), all before the box
		// exists. After the mountpoints above, so a kept folder that would hold
		// one of them is left out.
		var kept []Mount
		if kept, err = m.prepareKept(name, conv, owner, wsm, extraRO); err != nil {
			m.rearmIfIdle(name)
			return err
		}
		if preAcceptBypass {
			// Best effort, like the trust entry: without it the agent just asks.
			if err := m.preAcceptBypass(name); err != nil {
				log.Printf("box: %s: bypass-permissions warning not pre-accepted: %v", name, err)
			}
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
		// The identity of the workspace and of each context mount as the VM sees
		// it (macOS), taken as late as it can be and still before the mount
		// exists: a path flipped after this makes docker mount something the
		// in-box check below sees as another file. A box reused instead takes it
		// just before that check. One read for all of them, the workspace first.
		if wsm, extraRO, err = m.fillMountIDs(ctx, wsm, extraRO, true); err != nil {
			m.rearmIfIdle(name)
			return err
		}
		// Locked before the box exists: nothing in it ever sees the files
		// unlocked. Held by the box's name from here; released when its
		// container is gone (boxGone), whatever happens below.
		if err := m.holdLocks(name, lk); err != nil {
			m.rearmIfIdle(name)
			return err
		}
		if _, err := m.opts.Client.Run(ctx, m.runArgs(name, ws.Path, extraRO, fenced, exclusive, bundle, memory, convDir, kept)); err != nil {
			// A box that never came to be holds no lock. One docker may have left
			// behind keeps it until it is removed.
			if m.locks != nil {
				if st, _, _, ierr := m.opts.Client.InspectMounts(ctx, name); ierr == nil && st == StateMissing {
					m.boxGone(name)
				}
			}
			// The idle timer was cancelled above. Without re-arming, a box that
			// survives a failed spawn would never be stopped by anyone.
			m.rearmIfIdle(name)
			return fmt.Errorf("box: starting %s: %w", name, err)
		}
		created = true
		guards = true
		boxConv = conv
		if verify != nil {
			if err := verify(ctx, name); err != nil {
				return m.refuseUnverified(ctx, name, err)
			}
			m.markVerified(name)
		}
	}
	// The box runs, created or reused; the agent has not started. The workspace
	// or a mount that is not the checked path is refused here, before anything
	// runs in it. A reused box has had no VM identity read yet (a created one
	// has, above, and this is then a no-op): the box's mount is old, the path's
	// identity is now.
	if wsm, extraRO, err = m.fillMountIDs(ctx, wsm, extraRO, guards); err != nil {
		m.rearmIfIdle(name)
		return err
	}
	if err := m.checkMountIDs(ctx, name, withWorkspace(wsm, extraRO), created, guards); err != nil {
		return m.refuseMountChanged(ctx, name, created, err)
	}
	if guards && !created {
		m.markGuardsChecked(name)
	}
	// A box this spawn did not create: it may hold the locks already (another
	// session's), or predate them, or the files may have changed since. Held
	// for this spawn before the agent starts; a failure leaves the box to the
	// sessions already in it.
	if !created {
		if err := m.holdLocks(name, lk); err != nil {
			m.rearmIfIdle(name)
			return err
		}
	}
	// Count the session only once the box is genuinely usable, so a failed spawn
	// cannot leak a reference that keeps the box alive forever.
	m.acquire(name)
	if exclusive {
		m.MarkExclusive(name)
	}
	m.mu.Lock()
	m.conv[name] = boxConv
	m.mu.Unlock()
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
// still runs; its screenshots just stay inside it). The conversations folder
// (ConversationsBox, wantConv) is judged the same way: never an extra mount,
// and a missing OPTIONAL one when the spawn asks for it. A profile's kept
// folders are neither asked for nor extra: they are applied at create only, so
// a mount at a kept folder's path whose source is a kept store's folder
// (keptOnly, the one host source read here) is left out of the comparison.
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
func (m *Manager) reuseMounts(ctx context.Context, name, cwd string, have []string, extraRO []Mount, partial, wantConv bool) (State, error) {
	got := map[string]bool{}
	hasOut, hasConv := false, false
	for _, d := range have {
		d = filepath.Clean(d)
		if d == PlaywrightOutBox {
			hasOut = true // judged below, never an extra mount
			continue
		}
		if d == ConversationsBox {
			hasConv = true // like the out dir; its source is judged by reuseConversations
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
	if wantConv && !hasConv {
		// Optional too: a live box from before #330 keeps its sessions writing
		// in its home, where the readers still look.
		missing = append(missing, ConversationsBox)
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
	// A mount the spawn did not ask for is an extra one, unless it is a kept
	// folder's: those are applied at create only (keptMounts), so a box is never
	// recreated because its profile's store changed since. Asked of docker only
	// when it decides the answer.
	var unasked []string
	for d := range got {
		if !partial && !want[d] {
			unasked = append(unasked, d)
		}
	}
	extra := len(unasked) > 0
	if extra && len(missing) == 0 {
		extra = !m.keptOnly(ctx, name, unasked)
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
		return StateRunning, fmt.Errorf("%w: box %s started with a different CA set (extra_ca_certs changed) and still runs %d session(s); end them, then spawn again (macOS: or run `culibox box stop`)",
			ErrBoxUnavailable, name, live)
	}
	if err := m.removeIdle(ctx, name); err != nil {
		return StateRunning, fmt.Errorf("box: removing idle %s to recreate it with the current CA set: %w", name, err)
	}
	return StateMissing, nil
}

// claimCheck enforces exclusive workspaces across every agent-box instance on
// this docker daemon (DEV and PROD share it). A pair of boxes over overlapping
// folders where either side is exclusive is checked; two plain ones never are.
// An idle box of this instance is removed instead of blocking, whichever side
// is exclusive (spec correction 6); if that removal fails, an exclusive one
// still blocks. A failed listing refuses (correction 9).
//
// Within this instance the risk is a write window, not the box (#325): an
// exclusive box blocks only while writeHeld says its session may hold write
// credentials, and a plain box never blocks an exclusive spawn (the grant,
// ClaimForGrant, refuses while it runs). Another instance's windows cannot be
// seen from here, so its boxes are checked as before #325: an exclusive pair
// conflicts.
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
			return fmt.Errorf("%w: container %q carries CuliBox labels that cannot be read, so it may be using %s. "+
				"Remove that container, or use a separate folder or worktree.", ErrWorkspaceHeld, b.Name, cwd)
		}
		if knownWorkspace(b.Workspace) && !Overlaps(b.Workspace, cwd) {
			continue
		}
		if !exclusive && !b.Exclusive {
			continue
		}
		if b.Instance != m.opts.Instance {
			return fmt.Errorf("%w: %s is in use by a box of another CuliBox instance (%s), and one of the two sessions can be granted AWS write access; "+
				"this CuliBox cannot see that instance's write windows. Use a separate folder or worktree, or end the session there.",
				ErrWorkspaceHeld, cwd, b.Instance)
		}
		if m.idle(b.Name) {
			if m.removeIdle(ctx, b.Name) == nil || !b.Exclusive {
				continue // gone, or plain: a plain box never held write credentials
			}
			return fmt.Errorf("%w: %s is in use by box %s, whose session has ended but which could not be removed, and that session could be granted AWS write access. "+
				"Try again, or use a separate folder or worktree.", ErrWorkspaceHeld, cwd, b.Name)
		}
		if !b.Exclusive {
			continue // the exclusive spawn's own grant is refused while this box runs
		}
		if why := m.writeHeld(b.Name); why != "" {
			return heldByWindow(cwd, why)
		}
	}
	// After the loop: removing an idle box above can leave an ended hold.
	for _, h := range m.endedHolds() {
		if h.name == name || (knownWorkspace(h.ws) && !Overlaps(h.ws, cwd)) {
			continue
		}
		if !knownWorkspace(h.ws) {
			return fmt.Errorf("%w: a box of a session that has ended was removed, and CuliBox could not read its folder, so no other box can start in any folder "+
				"until write credentials of that session expire: %s. Wait until then.", ErrWorkspaceHeld, h.why)
		}
		return heldByEnded(cwd, h.why)
	}
	// Host sessions (#360) have no box for the loops above to see; last, so a
	// box's refusal reads as before. A folder not known overlaps every one.
	m.mu.Lock()
	hostHolds := m.hostHolds
	m.mu.Unlock()
	if hostHolds != nil {
		for _, h := range hostHolds() {
			if knownWorkspace(h.Workspace) && !Overlaps(h.Workspace, cwd) {
				continue
			}
			if h.Ended {
				return heldByEnded(cwd, h.Why)
			}
			return heldByWindow(cwd, h.Why)
		}
	}
	return nil
}

// heldByWindow refuses a spawn into cwd while a session in it may write to AWS.
func heldByWindow(cwd, why string) error {
	return fmt.Errorf("%w: %s is closed to other boxes while a session in it can write to AWS: %s. "+
		"Wait for it to end (or drop the window in that session), or use a separate folder or worktree.", ErrWorkspaceHeld, cwd, why)
}

// heldByEnded refuses a spawn into cwd while write credentials of an ended
// session may still be valid.
func heldByEnded(cwd, why string) error {
	return fmt.Errorf("%w: %s is closed to other boxes until write credentials of a session that has ended expire: %s. "+
		"Wait until then (drop the window first if that session is still listed), or use a separate folder or worktree.", ErrWorkspaceHeld, cwd, why)
}

// writeHeld is why the session owning exclusive box name may hold AWS write
// credentials ("" = it holds none). It fails closed: with no hook, or a box
// whose session this process does not know, nothing can be ruled out. Caller
// holds opMu (the hook runs under it), never mu.
func (m *Manager) writeHeld(name string) string {
	m.mu.Lock()
	hook, sid, at := m.writeHold, m.owner[name], m.adoptedAt[name]
	m.mu.Unlock()
	if hook == nil || sid == "" {
		return fmt.Sprintf("box %s belongs to a session that can be granted AWS write access, and this CuliBox cannot tell whether a write window is open", name)
	}
	return hook(sid, at)
}

// endedHold is what an exclusive box leaves behind when it goes while its
// session may hold write credentials: who to ask, and the folder (ws "" = not
// known, which overlaps every folder).
type endedHold struct {
	sid, ws   string
	adoptedAt time.Time
	boot      bool // reaped at boot (keepBootHold): held for bootHoldFor while no hook is installed
}

// keepEndedHold is called before a box is removed: if the hook still holds the
// session that owns it, the hold outlives the box in ended (#333). A box no
// session of this process owns is a plain box, or an exclusive orphan whose boot
// Reconcile did not run (Docker down or slow at boot, then back): its label
// says which, and an exclusive or unreadable one gets a boot hold counted from
// this process's start, fail closed, as Reconcile would have left. It also
// prunes the ended holds the hook no longer holds, so they cannot pile up.
// Caller holds opMu (the hook runs under it), never mu.
func (m *Manager) keepEndedHold(ctx context.Context, name string) {
	m.endedHolds()
	m.mu.Lock()
	hook, sid, at, ws := m.writeHold, m.owner[name], m.adoptedAt[name], m.ownerWS[name]
	m.mu.Unlock()
	if sid == "" {
		// Never over a hold already kept for this box (its owner's, kept
		// before a failed stop), and nothing once a credential's life has
		// passed since start: a boot hold would already be over.
		m.mu.Lock()
		_, kept := m.ended[name]
		m.mu.Unlock()
		if kept || time.Since(m.started) >= bootHoldFor {
			return
		}
		row, found, err := m.labelRow(ctx, name)
		switch {
		case err != nil:
			m.keepBootHold(name, nil, m.started) // unreadable: every folder
		case found:
			m.keepBootHold(name, map[string]BoxInfo{name: row}, m.started)
		} // not listed: no container, so nothing of it to hold
		return
	}
	if hook == nil || hook(sid, at) == "" {
		return
	}
	if ws == "" { // adopted after a restart: the folder is on the box's label
		if row, found, err := m.labelRow(ctx, name); err == nil && found && !row.Unreadable && knownWorkspace(row.Workspace) {
			ws = row.Workspace
		}
	}
	m.mu.Lock()
	m.ended[name] = endedHold{sid: sid, ws: ws, adoptedAt: at}
	m.mu.Unlock()
}

// bootHoldFor bounds the hold of an exclusive box reaped at boot while no
// write-hold hook is installed yet (Reconcile runs before the broker's hook):
// a write credential's longest life, 15 minutes (the STS minimum the broker
// mints), plus a minute for clock skew, as the broker's minElevatedRemaining.
const bootHoldFor = 16 * time.Minute

// keepBootHold records an ended hold for an orphan Reconcile is about to reap,
// unless rows show it was a plain box: an exclusive one's session may have been
// given write credentials before the restart that are still valid (#333). Its
// session is not known (sid ""); adoptedAt is the boot, as for an adopted box.
// rows nil (unreadable), or no usable row, holds it fail closed. Caller holds
// opMu.
func (m *Manager) keepBootHold(name string, rows map[string]BoxInfo, at time.Time) {
	b, ok := rows[name]
	if ok && !b.Unreadable && !b.Exclusive {
		return
	}
	ws := ""
	if ok && !b.Unreadable && knownWorkspace(b.Workspace) {
		ws = b.Workspace
	}
	m.mu.Lock()
	m.ended[name] = endedHold{ws: ws, adoptedAt: at, boot: true}
	m.mu.Unlock()
}

// labelReadTimeout bounds labelRow: it runs under opMu, some callers with
// context.Background (the idle timer), and a hung docker must not hold every
// spawn behind it.
const labelReadTimeout = 10 * time.Second

// labelRow is this instance's row for box name as docker lists it. found is
// false when docker lists no such box; err is set when the list failed.
func (m *Manager) labelRow(ctx context.Context, name string) (row BoxInfo, found bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, labelReadTimeout)
	defer cancel()
	boxes, err := m.opts.Client.ListBoxes(ctx, LabelInstance+"="+m.opts.Instance)
	if err != nil {
		return BoxInfo{}, false, err
	}
	for _, b := range boxes {
		if b.Name == name {
			return b, true, nil
		}
	}
	return BoxInfo{}, false, nil
}

// heldEnded is an ended hold the hook still holds, with the hook's reason.
type heldEnded struct {
	name, ws, why string
}

// endedHolds asks the hook about every ended hold, drops those it no longer
// holds and returns the rest. Caller holds opMu, never mu.
func (m *Manager) endedHolds() []heldEnded {
	m.mu.Lock()
	hook := m.writeHold
	snap := maps.Clone(m.ended)
	m.mu.Unlock()
	var out []heldEnded
	for name, e := range snap {
		why := ""
		switch end := e.adoptedAt.Add(bootHoldFor); {
		case hook != nil:
			why = hook(e.sid, e.adoptedAt)
		case e.boot && time.Now().Before(end):
			why = "a session was running when CuliBox or its box VM restarted, and write credentials it may have been given before then stay valid until " +
				end.Local().Format("15:04") + " at the latest"
		}
		if why != "" {
			out = append(out, heldEnded{name, e.ws, why})
			continue
		}
		m.mu.Lock()
		if cur, ok := m.ended[name]; ok && cur == e {
			delete(m.ended, name)
		}
		m.mu.Unlock()
	}
	return out
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
	m.keepEndedHold(ctx, name)
	m.unverify(name)
	ws := m.stopWorkspace(ctx, name)
	if err := m.opts.Client.Stop(ctx, name); err != nil {
		return err
	}
	m.stopped(name, ws)
	m.boxGone(name)
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.timers[name]; ok {
		t.Stop()
		delete(m.timers, name)
	}
	m.forgetExclusiveLocked(name)
	return nil
}

// MarkExclusive records that a box belongs to one session, so it is removed at
// release rather than kept for its idle TTL. ensure marks the boxes it starts.
func (m *Manager) MarkExclusive(name string) {
	if name == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exclusive[name] = true
}

// AdoptExclusive is MarkExclusive for a box adopted after a restart, with the
// session it belongs to: the write-hold hook is asked about that session, and
// told when the box was adopted, since credentials issued before the restart
// may still be valid while the broker no longer knows of a window (#325).
func (m *Manager) AdoptExclusive(name, sessionID string) {
	if name == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exclusive[name] = true
	if sessionID != "" {
		m.owner[name] = sessionID
		m.adoptedAt[name] = time.Now()
	}
}

// forgetExclusiveLocked drops what is recorded about a box's exclusivity once
// the box is gone. Caller holds mu.
func (m *Manager) forgetExclusiveLocked(name string) {
	delete(m.exclusive, name)
	delete(m.owner, name)
	delete(m.adoptedAt, name)
	delete(m.ownerWS, name)
}

// idleTTL is how long an idle box is kept: zero for an exclusive one, which
// is never reused. Caller holds mu.
//
// A box holding a guard lock is removed at once too: what its sessions started
// can outlive them, and the lock is only lifted once the box is gone, so an
// idle box would keep the user's files locked with no session to show for it.
func (m *Manager) idleTTL(name string) time.Duration {
	if m.exclusive[name] || (m.locks != nil && m.locks.holdsOurs(name)) {
		return 0
	}
	return m.opts.IdleTTL
}

// GrantBlock is why a write window cannot be granted now. Box is set when the
// blocker is a live box of this instance, so the caller can name the sessions
// in it; Why is the box layer's own sentence. The zero value = nothing blocks.
type GrantBlock struct {
	Box, Why string
}

// GrantBlocker is what the grant panel shows (M3b §7.3): any other running box,
// of any instance, over a folder that overlaps cwd. One from a build that does
// not enforce claims (no claims label) is named as such. self is the session's
// own box. Like claimCheck it fails closed: an unreadable row, or a live
// (running, paused or restarting) box with no usable workspace label, blocks.
// It only reads: an idle box of this instance is not reported, because the
// grant (ClaimForGrant) removes it.
func (m *Manager) GrantBlocker(ctx context.Context, cwd, self string) (GrantBlock, error) {
	return m.grantBlock(ctx, cwd, self, false)
}

// ClaimForGrant is the grant's own check (spec correction 8, #325): as
// GrantBlocker, but under opMu, and an idle box of this instance in the way is
// removed (one that cannot be removed blocks). Holding opMu orders it against
// every spawn of this instance: one that passed its claimCheck has its box
// running before this lists, and one after it sees the caller's write hold,
// which the caller sets before calling this. Another instance's spawn is
// refused by this box's exclusive label. It gives up when ctx ends before
// opMu is free.
func (m *Manager) ClaimForGrant(ctx context.Context, cwd, self string) (GrantBlock, error) {
	if err := m.lockOp(ctx); err != nil {
		return GrantBlock{}, fmt.Errorf("box: waiting for a box operation to finish: %w", err)
	}
	defer m.opMu.Unlock()
	return m.grantBlock(ctx, cwd, self, true)
}

// lockOp takes opMu, or returns ctx's error if ctx ends first. An abandoned
// wait still takes the lock when it frees up, and gives it straight back.
func (m *Manager) lockOp(ctx context.Context) error {
	if m.opMu.TryLock() {
		return nil
	}
	got := make(chan struct{})
	go func() {
		m.opMu.Lock()
		close(got)
	}()
	select {
	case <-got:
		return nil
	case <-ctx.Done():
		go func() {
			<-got
			m.opMu.Unlock()
		}()
		return ctx.Err()
	}
}

// grantBlock is GrantBlocker (remove false: read only) and ClaimForGrant
// (remove true; caller holds opMu).
func (m *Manager) grantBlock(ctx context.Context, cwd, self string, remove bool) (GrantBlock, error) {
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return GrantBlock{}, fmt.Errorf("box: resolving workspace %q: %w", cwd, err)
	}
	boxes, err := m.opts.Client.ListBoxes(ctx, LabelInstance)
	if err != nil {
		// Only the listing's own error, while ctx is live, is classified (as
		// Ping's is): nothing answering at the daemon's endpoint is marked
		// ErrDockerNotRunning, text unchanged, so a Host session's grant can
		// tell "no box can be running" from any other failure (#360).
		if ctx.Err() == nil && errors.Is(classifyDaemonErr(err), ErrDockerNotRunning) {
			err = daemonDownErr{err}
		}
		return GrantBlock{}, fmt.Errorf("box: listing boxes: %w", err)
	}
	for _, b := range boxes {
		if b.Name == self || !holdsWorkspace(b.State) {
			continue
		}
		switch {
		case b.Unreadable:
			return GrantBlock{Why: fmt.Sprintf("container %q carries CuliBox labels that cannot be read, so it may use %s; remove it first", b.Name, real)}, nil
		case !knownWorkspace(b.Workspace):
			return GrantBlock{Why: fmt.Sprintf("a box (instance %s) has no usable workspace label, so it may use %s; stop it first", b.Instance, real)}, nil
		case !Overlaps(b.Workspace, real):
			continue
		case !b.Claims:
			return GrantBlock{Why: fmt.Sprintf("a box started by an older CuliBox (instance %s) uses %s; stop it or upgrade that instance first", b.Instance, b.Workspace)}, nil
		}
		why := fmt.Sprintf("another box (instance %s) uses %s; stop the session in it first", b.Instance, b.Workspace)
		if b.Instance != m.opts.Instance {
			return GrantBlock{Why: why}, nil
		}
		if m.idle(b.Name) {
			if !remove {
				continue // the grant removes it
			}
			if m.removeIdle(ctx, b.Name) == nil {
				continue
			}
			return GrantBlock{Why: fmt.Sprintf("box %s uses %s; its sessions have ended but it could not be removed, try again", b.Name, b.Workspace)}, nil
		}
		return GrantBlock{Box: b.Name, Why: why}, nil
	}
	return GrantBlock{}, nil
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
	} else {
		m.boxGone(name)
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

func (m *Manager) isMismatched(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mismatched[name]
}

// markMismatched remembers a box that failed a mount check and could not be removed.
func (m *Manager) markMismatched(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mismatched[name] = true
}

func (m *Manager) isGuardsChecked(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.guardsChecked[name]
}

func (m *Manager) markGuardsChecked(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.guardsChecked[name] = true
}

// removeMismatched removes a box that failed a mount check earlier and was left
// running because its removal failed. It answers StateMissing once it is gone
// (the caller creates a new box) and refuses the spawn when it cannot be removed
// again: never a reuse. Caller holds opMu.
func (m *Manager) removeMismatched(ctx context.Context, name string) (State, error) {
	m.unverify(name)
	if err := m.opts.Client.Stop(ctx, name); err != nil {
		return StateRunning, fmt.Errorf("%w: box %s failed a mount check and could not be removed: %v; it is not reused and is removed again on the next spawn",
			ErrBoxUnavailable, name, err)
	}
	m.boxGone(name)
	return StateMissing, nil
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
	if m.release(name) {
		// The reclaim sweep ages a home by this marker, and a box was last used
		// when its last session ended, not when that session started: a long
		// session must not look an hour idle the moment it ends. Outside mu, like
		// ensure's call, because it is file I/O. A box that is removed at once
		// (exclusive) outlives this by a docker call, and reclaim only looks at
		// boxes whose container is gone.
		m.touchMarker(name)
	}
}

// release drops one session's count and, when that was the last, arms the idle
// timer. It reports whether a counted session was the last one released, which
// is the only time the box was just used.
func (m *Manager) release(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	counted := m.active[name] > 0
	if counted {
		m.active[name]--
	}
	if m.active[name] > 0 {
		return false
	}
	delete(m.active, name)
	if t, ok := m.timers[name]; ok {
		t.Stop()
	}
	m.timers[name] = time.AfterFunc(m.idleTTL(name), func() { m.stopIfIdle(name) })
	return counted
}

// stopWorkspace is the workspace of box name, read before the box is stopped
// (a stopped container is gone from the listing). "" when no OnStop is
// installed (nothing to tell, so no docker call), or when neither the
// exclusive owner's record nor the box's label gives an absolute path. Caller
// holds opMu, as for labelRow.
func (m *Manager) stopWorkspace(ctx context.Context, name string) string {
	if m.opts.OnStop == nil {
		return ""
	}
	m.mu.Lock()
	ws := m.ownerWS[name]
	m.mu.Unlock()
	if knownWorkspace(ws) {
		return ws
	}
	if row, found, err := m.labelRow(ctx, name); err == nil && found && !row.Unreadable && knownWorkspace(row.Workspace) {
		return row.Workspace
	}
	return ""
}

// stopped tells OnStop that box name was stopped, then runs the keep pass on
// its home (keepPass), each in its own goroutine, so a caller holding opMu
// never waits on them. The pass takes opMu itself once it has screened.
func (m *Manager) stopped(name, ws string) {
	if h := m.opts.OnStop; h != nil {
		go h(name, ws)
	}
	m.keptPasses.Add(1)
	go func() {
		defer m.keptPasses.Done()
		m.runKeepPass(context.Background(), name, keptPassOpts{})
	}()
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
	m.keepEndedHold(context.Background(), name)
	m.unverify(name)
	ws := m.stopWorkspace(context.Background(), name)
	if m.opts.Client.Stop(context.Background(), name) != nil {
		// Still there: keep its owner, so the removal that follows (the sweep,
		// a spawn's check) still knows whose hold it is (#333).
		return
	}
	m.stopped(name, ws)
	m.boxGone(name)
	m.mu.Lock()
	m.forgetExclusiveLocked(name)
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
	left := map[string]bool{} // containers still there after the pass
	// rows are this instance's boxes, read once before the first orphan is
	// reaped (nil = unreadable): which are exclusive, and their folders.
	var rows map[string]BoxInfo
	read, bootAt := false, time.Now()
	for _, name := range names {
		m.mu.Lock()
		live := m.active[name]
		m.mu.Unlock()
		if live > 0 {
			left[name] = true
			continue
		}
		if !read {
			read = true
			if bs, err := m.opts.Client.ListBoxes(ctx, LabelInstance+"="+m.opts.Instance); err == nil {
				rows = map[string]BoxInfo{}
				for _, b := range bs {
					rows[b.Name] = b
				}
			}
		}
		m.keepBootHold(name, rows, bootAt)
		m.unverify(name)
		if err := m.opts.Client.Stop(ctx, name); err != nil {
			left[name] = true
			// Left running with no session and no memory of its checks: the
			// identity label says a checking build made it, not that it passed
			// (a crash between `docker run` and the check leaves such a box). Never
			// reused as it is: the next spawn removes it again first.
			m.markMismatched(name)
			errs = append(errs, fmt.Errorf("box: reaping orphan %s: %w", name, err))
			continue
		}
		m.boxGone(name)
	}
	// The boot sweep of the guard locks: a lock whose boxes are all gone (a
	// crash, a VM restart) is lifted. A box still listed keeps its lock.
	m.sweepLocks(left)
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
			// The release that never came is when the box's last session ended as
			// far as anyone can tell: ReleaseBox's marker, for the same reason.
			m.touchMarker(name)
			rep.Corrected = append(rep.Corrected, SweepAction{b.Name,
				fmt.Sprintf("counter said %d live session(s), the registry says 0 on two sweeps; idle timer armed", count)})
		}
	}
	m.mu.Lock()
	m.drift = nextDrift
	m.mu.Unlock()
	// Guard locks of boxes docker no longer lists (a VM or docker restart, a
	// removal that happened elsewhere) or that this pass removed are lifted.
	// Every other listed box keeps its own, whatever its state: one whose
	// removal failed (paused, restarting, exited) may still run or run again,
	// as Reconcile keeps what is left.
	left := map[string]bool{}
	for _, b := range boxes {
		if !slices.ContainsFunc(rep.Removed, func(a SweepAction) bool { return a.Box == b.Name }) {
			left[b.Name] = true
		}
	}
	m.sweepLocks(left)
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
	m.keepEndedHold(ctx, name)
	m.unverify(name)
	ws := m.stopWorkspace(ctx, name)
	if err := m.opts.Client.Stop(ctx, name); err != nil {
		return fmt.Errorf("box: sweep removing %s: %w", name, err)
	}
	m.stopped(name, ws)
	m.boxGone(name)
	m.mu.Lock()
	if m.active[name] == 0 { // no release will come to clear it
		m.forgetExclusiveLocked(name)
	}
	m.mu.Unlock()
	return nil
}
