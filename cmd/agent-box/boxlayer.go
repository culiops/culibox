package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	boximage "github.com/culiops/culibox/docker/agent-box"
	"github.com/culiops/culibox/internal/auth"
	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/boxaddr"
	"github.com/culiops/culibox/internal/boxvm"
	"github.com/culiops/culibox/internal/bundle"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/egress"
	"github.com/culiops/culibox/internal/proxyline"
	"github.com/culiops/culibox/internal/vmshares"
)

// verifyTimeout bounds the containment probe of a new box. The spawn holds the
// box manager's operation lock while it runs, so a hung exec must not hold it
// (and every other spawn) indefinitely. A var so a test can shorten it.
var verifyTimeout = 30 * time.Second

// prepareVMTimeout bounds a boxed spawn's wait for the VM. The session layer
// calls prepare with a context that never ends, and EnsureRunning holds the
// VM's single-slot lock while `colima status` runs, so a wedged hostagent
// would otherwise block this spawn and every later boxed spawn until restart.
// It bounds only the wait: a VM start is detached and has its own cap (120 s).
// A var so a test can shorten it.
var prepareVMTimeout = 3 * time.Minute

// imageCheckTimeout bounds Prepare's image check (two quick docker calls).
const imageCheckTimeout = 30 * time.Second

// vmStartPassTimeout bounds one reconcile+reclaim pass (at boot, or after
// prepare starts the VM), as sweepBoxes bounds the sweep: both docker listings
// and the reclaim's per-box re-check run under the operation lock.
const vmStartPassTimeout = 2 * time.Minute

// boxLayer is the containment layer and how to reach its docker daemon.
//
// dockerArgs is the ONE slice of global docker flags (on darwin: the VM's
// socket and docker config dir). Every docker caller takes it from here — the
// box client, execArgv, the bundle prober and the image commands — because a
// caller without it reaches the user's own daemon, outside the VM's loopback
// block. Empty on Linux.
type boxLayer struct {
	boxes      *box.Manager
	vm         *boxvm.VM // nil off darwin
	dockerBin  string
	dockerArgs []string
	cli        box.CLIClient
	img        boximage.Docker
	image      string             // the fallback box image (box_image)
	goos       string             // runtime.GOOS as the layer was built
	dataDir    string             // the absolute data dir, for the store doctor and box setup read
	protected  []box.ProtectedDir // CuliBox's own host dirs (config, profiles, bundle); no VM share may overlap them
	stateRoot  string             // where box homes and the bundle live; resolved on darwin
	// addrs is the one box-address cache the egress proxy and the credential
	// broker share (both OSes). One instance on purpose: the proxy drops a box's
	// entry when its session ends or a probe re-verifies a recreated box, and a
	// second cache would never be cleared. nil only for a layer built by hand in
	// a test.
	addrs *boxaddr.Cache
	// relayKey is the secret CuliBox's relay and the broker and egress
	// listeners prove to each other before the relay's PROXY line is read
	// (#114, #118: it never crosses the wire). One per data directory, loaded
	// once per daemon start by loadRelayKey. Zero off darwin (Linux has no
	// relay) and zero when relay2.secret could not be read, in which case the
	// darwin listeners and the relay refuse to start.
	relayKey proxyline.Key
	// reclaimErr remembers the last reclaim error that was logged, so one that
	// is still there on the next pass is not logged again.
	reclaimErr logOnChange
	// egressProbe is egress.Probe; a test replaces it.
	egressProbe func(ctx context.Context, p *egress.Proxy, box string, t egress.ProbeTarget, exec egress.ExecInput) error
	// ctl is the VM folder control (macOS, set by the daemon): while it reports
	// an apply, spawns are refused instead of racing the VM restart.
	ctl vmControl
	// folderCheckFailed is true while the last VM folder check failed; every
	// spawn then runs the check again until one passes.
	folderCheckFailed atomic.Bool
}

// relaySecretFile is the relay key's file in the data directory. The name pairs
// with the AGENTBOX-RELAY2 hello: it is the key of the challenge-response
// handshake, where the key never crosses the wire (#118).
const relaySecretFile = "relay2.secret"

// retiredRelaySecretFile is the v1 relay key's file. v1 sent that key in clear
// on every connection, so whatever held a port while CuliBox did not, or read
// the loopback interface, may have learned it. It is never loaded again:
// loadRelayKey deletes it. A new file name, not a stamp inside the old file,
// because auth.NewCookieAuth owns the 32-byte format and replaces anything that
// is not exactly that.
const retiredRelaySecretFile = "relay.secret"

// loadRelayKey reads, or creates, the relay key for this data directory and
// sets l.relayKey. Off darwin (no VM, so no relay) it does nothing, and leaves
// any relay.secret there alone. On darwin it first deletes the retired v1 key
// file (best effort: a failure is logged, without any key, and the load goes
// on). It uses the same helper and protection as broker.secret and
// cookie.secret: 32 random bytes, mode 0600 in the 0700 data directory, an
// unreadable existing file is an error and is never overwritten. An error
// leaves the key zero, and a zero key fails closed everywhere it is used.
func (l *boxLayer) loadRelayKey() error {
	if l.vm == nil {
		return nil
	}
	retired := filepath.Join(l.dataDir, retiredRelaySecretFile)
	if err := os.Remove(retired); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("relay: could not remove the retired %s (%v); it is no longer used", retired, err)
	}
	path := filepath.Join(l.dataDir, relaySecretFile)
	sec, err := auth.NewCookieAuth(path)
	if err != nil {
		// The helper words its error for the cookie secret and repeats the path.
		// Keep only the OS error and say what this file is and what fixes it.
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return fmt.Errorf("relay secret %s cannot be read or created (%w); delete it (CuliBox creates a new one and replaces the relay once); fixing its permissions keeps the old key", path, err)
	}
	k, err := proxyline.KeyFromBytes(sec.Secret())
	if err != nil {
		// The helper only returns 32 bytes, so what KeyFromBytes refuses is an
		// all-zero file, which no listener accepts as a key. It is readable, so
		// the fix is a new file.
		return fmt.Errorf("relay secret %s is all zero, which is not a key; delete it and restart CuliBox to create a new one", path)
	}
	l.relayKey = k
	return nil
}

// newBoxLayer builds the containment layer, or reports why it is unavailable.
// On darwin the layer exists whenever colima and docker resolve. Whether the
// VM is set up, running and has a fitting image is decided per spawn (prepare),
// and reaches the user as ErrBoxUnavailable with its remedy. Nothing here
// starts the VM (D3). protected are the directories besides the state root a
// workspace must not overlap (config, profiles); nil off the serve path.
func newBoxLayer(cfg config.Config, dataDir, home, goos string, runner boxvm.Runner, protected []box.ProtectedDir) (*boxLayer, error) {
	return newBoxLayerFor(cfg, dataDir, home, goos, runner, protected, false)
}

// hostSize reads the machine's CPUs and RAM for sizing the box VM; a test replaces it.
var hostSize = boxvm.HostSize

// newBoxLayerFor is newBoxLayer with fresh: true for `box setup -recreate`, which
// sizes the new VM from the host instead of keeping the one it is deleting. Every
// other caller (serve, doctor, status, stop, plain setup) keeps an existing VM's
// size, so they all agree with what is on disk.
func newBoxLayerFor(cfg config.Config, dataDir, home, goos string, runner boxvm.Runner, protected []box.ProtectedDir, fresh bool) (*boxLayer, error) {
	bin, err := box.LookDocker(cfg.DockerBin)
	if err != nil {
		return nil, err
	}
	l := &boxLayer{protected: protected, goos: goos, dockerBin: bin, image: cfg.BoxImageOrDefault(), stateRoot: dataDir, dataDir: dataDir}
	if goos == "darwin" {
		colima, err := boxvm.LookColima(cfg.ColimaBin)
		if err != nil {
			return nil, err
		}
		hostCPUs, hostMem := hostSize()
		configFor := boxvm.ConfigFor
		if fresh {
			configFor = boxvm.ConfigForFresh
		}
		vcfg := configFor(cfg, dataDir, home, colima, bin, hostCPUs, hostMem)
		vcfg.Fold = true // goos == "darwin" here: APFS compares case-insensitively
		l.vm = boxvm.NewVM(vcfg, runner)
		l.vm.SetShares(bootShares(dataDir, l.vm.Config(), protected))
		// Symlink-resolved: the VM mounts these paths, so every path a box
		// mounts or a workspace is checked against must be spelled the same way.
		vc := l.vm.Config()
		l.stateRoot = vc.Root
		l.dockerArgs = vc.DockerArgs()
	}
	l.cli = box.CLIClient{Bin: bin, Args: l.dockerArgs}
	l.addrs = newBoxAddrs(l.cli, cfg.BoxInstance())
	l.img = boximage.Docker{Bin: bin, Args: l.dockerArgs}
	o := l.options(cfg)
	o.Protected = protected
	// The image build trusts the same CAs as the boxes; with a load error it
	// gets none, and the manager refuses every boxed spawn (ExtraCAErr).
	l.img.ExtraCA = o.ExtraCA.PEM()
	m, err := box.New(o)
	if err != nil {
		return nil, err
	}
	l.boxes = m
	return l, nil
}

// options is what the box manager is built with. Off darwin there are no hooks:
// Linux behaves exactly as before the VM existed.
func (l *boxLayer) options(cfg config.Config) box.Options {
	// Loaded once. The daemon refuses to boot on a bad extra_ca_certs, but a
	// layer built any other way must still fail closed: ExtraCAErr refuses
	// every boxed spawn.
	extra, extraErr := cfg.ExtraCA()
	o := box.Options{
		DockerBin:  l.dockerBin,
		DockerArgs: l.dockerArgs,
		Image:      l.image,
		Instance:   cfg.BoxInstance(),
		IdleTTL:    cfg.BoxIdleTTL(),
		StateRoot:  l.stateRoot,
		Client:     l.cli,
		PidsLimit:  cfg.BoxPidsLimit,
		TmpSize:    cfg.BoxTmpSize,
		Memory:     cfg.BoxMemory,
		CPUs:       cfg.BoxCPUs,
		ExtraCA:    extra,
		ExtraCAErr: extraErr,
		// Both OSes: every box sits on the egress network (darwin through the
		// option (c) relay, since #69) and has its guard files read-only in
		// place (darwin since #68: the full read-only self-bind of .git held
		// through colima virtiofs on a Mac, the partial overlay did not).
		EgressAll:    true,
		GuardInPlace: true,
	}
	if l.vm != nil {
		o.Prepare = l.prepare
		o.Verify = l.verify
		// #72: the context-path identity, read in the VM (Linux records it on the
		// host and needs no hook).
		o.VMIDs = l.vmIDs
		o.Shares = l.shareView
		// Reachable only asks; it never starts the VM. The sweep skips while it
		// is stopped instead of reaping on a failed listing.
		o.Reachable = l.vm.Running
	}
	return o
}

// prober is the config bundle's MCP probe, on the same daemon as the boxes.
func (l *boxLayer) prober(image string) bundle.ImageProber {
	return bundle.ImageProber{Docker: l.dockerBin, Args: l.dockerArgs, Image: image}
}

// prepare readies the VM for a boxed spawn: started (lazily), and holding an
// image this daemon can run. It runs outside the manager's operation lock.
//
// The first boxed spawn after boot waits here while the VM starts (20–30 s;
// colima start is capped at 120 s). Nothing in between times that out:
// main.go's http.Server sets no WriteTimeout and the UI's spawn POST has no
// deadline, so the spawn simply takes longer and no "starting the box VM"
// state is needed in the UI (spec §3.6).
func (l *boxLayer) prepare(ctx context.Context) error {
	if l.ctl != nil && l.ctl.Applying() {
		return box.ErrVMRestarting
	}
	vctx, vcancel := context.WithTimeout(ctx, prepareVMTimeout)
	started, err := l.vm.EnsureRunning(vctx)
	vcancel()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return fmt.Errorf("%w: the box VM did not answer within %s; check `agent-box box status`",
				box.ErrBoxUnavailable, prepareVMTimeout)
		}
		return fmt.Errorf("%w: %v", box.ErrBoxUnavailable, err)
	}
	if started {
		// Every box in a freshly started VM is a leftover from before it
		// stopped, and the boot pass skipped them while the VM was down.
		l.reconcileAndReclaim(context.WithoutCancel(ctx))
	}
	if started || l.folderCheckFailed.Load() {
		mctx, mcancel := context.WithTimeout(ctx, imageCheckTimeout)
		err := l.verifyMounts(mctx)
		mcancel()
		if err != nil {
			return fmt.Errorf("%w: the box VM's folders do not match its folder list (%v); run `agent-box doctor`", box.ErrBoxUnavailable, err)
		}
	}
	ictx, cancel := context.WithTimeout(ctx, imageCheckTimeout)
	defer cancel()
	arch, err := l.img.ServerArch(ictx)
	if err != nil {
		return fmt.Errorf("%w: %v", box.ErrBoxUnavailable, err)
	}
	st, err := l.img.Inspect(ictx, l.image)
	if err != nil {
		return fmt.Errorf("%w: %v", box.ErrBoxUnavailable, err)
	}
	// A stale image (older Dockerfile) only warns in doctor and still runs.
	if fatal, msg := st.CheckFor(l.image, arch); fatal {
		return fmt.Errorf("%w: %s", box.ErrBoxUnavailable, msg)
	}
	return nil
}

// verify proves a new box cannot reach the Mac's loopback services, by running
// the probe inside the real container. Fail closed: a probe that cannot run is
// a refusal.
func (l *boxLayer) verify(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	err := boxvm.ProbeLoopback(ctx, l.execNode(name))
	if err != nil {
		return fmt.Errorf("%w: %v", box.ErrBoxUnavailable, err)
	}
	return nil
}

// vmIDsTimeout bounds the VM identity read of a boxed spawn's context paths.
// The box manager runs it under its operation lock, so a wedged `colima ssh`
// must not hold every other spawn. A var so a test can shorten it.
var vmIDsTimeout = 30 * time.Second

// vmIDs is box.Options.VMIDs on macOS: the identity of each path as the VM
// sees it, which is the one the box's bind mount shows (a Mac inode number is
// not). It only asks; a stopped VM is an error, never a start.
func (l *boxLayer) vmIDs(ctx context.Context, paths []string) ([]box.FileID, error) {
	ctx, cancel := context.WithTimeout(ctx, vmIDsTimeout)
	defer cancel()
	ids, err := l.vm.PathIDs(ctx, paths)
	if err != nil {
		return nil, err
	}
	out := make([]box.FileID, len(ids))
	for i, id := range ids {
		out[i] = box.FileID{Dev: id.Dev, Ino: id.Ino}
	}
	return out, nil
}

// execNode runs `node -e <script>` in the named box and returns its stdout.
func (l *boxLayer) execNode(name string) boxvm.ExecFunc {
	return func(ctx context.Context, script string) (string, error) {
		return l.cli.Run(ctx, []string{"exec", name, "node", "-e", script})
	}
}

// egressExec runs the egress probe's script in the named box. The credential
// goes to `node -e` on stdin only (docker exec -i), never in argv or env.
func (l *boxLayer) egressExec(name string) egress.ExecInput {
	return func(ctx context.Context, script, stdin string) (string, error) {
		return l.cli.RunInput(ctx, []string{"exec", "-i", name, "node", "-e", script}, stdin)
	}
}

// egressVerify is box.Egress.Verify: it proves a fenced box is contained by the
// egress network and allowlist, by running the probe inside the real container
// against the live proxy. It runs instead of verify for every fenced box (every box on macOS). Fail
// closed: a proxy that cannot start or a probe that cannot run is a refusal.
//
// darwin adds the loopback check after the egress probe. The network is
// --internal there too, but its gateway is the relay's address inside the VM, so
// the probe's DIRECT/DNS legs prove the VM rules only for the paths they try;
// the loopback check proves the Mac's loopback services stay unreachable
// through the gateway. It runs second because the probe's
// ALLOW 200 is its positive control (a fenced box's DNS is closed, so the plain
// check's DNS control cannot be used); a probe that fails never reaches it.
func (l *boxLayer) egressVerify(svc *egress.Service) func(ctx context.Context, name string) error {
	return func(ctx context.Context, name string) error {
		ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
		defer cancel()
		ep, err := svc.Ready(ctx)
		if err != nil {
			return fmt.Errorf("%w: %v", box.ErrBoxUnavailable, err)
		}
		probe := l.egressProbe
		if probe == nil {
			probe = egress.Probe
		}
		err = probe(ctx, svc.Proxy(), name, egress.ProbeTarget{ProxyHost: ep.ProxyHost, Port: ep.Port}, l.egressExec(name))
		if err == nil && l.goos == "darwin" {
			err = boxvm.ProbeLoopbackBound(ctx, l.execNode(name))
		}
		if err != nil {
			return fmt.Errorf("%w: %v%s", box.ErrBoxUnavailable, err, l.recreateHint())
		}
		return nil
	}
}

// recreateHint is appended to a darwin refusal of a boxed spawn when the VM's
// provision config has drifted: a VM provisioned before the egress subnet rules
// existed fails the probe with a message that does not say what to do. Only the
// text changes; the refusal itself is unchanged. Empty off darwin and when the
// config has not drifted.
func (l *boxLayer) recreateHint() string {
	if l.vm == nil || !l.vm.Drifted() {
		return ""
	}
	return " (the box VM's provision config is out of date, so its firewall rules may be too; run `agent-box box setup -recreate`)"
}

// vmStopped reports a darwin VM that is not running. Background passes check it
// first: they must never start the VM, and docker is unreachable while it is
// down, which must not read as "no boxes" (and reap) or fill the log.
func (l *boxLayer) vmStopped(ctx context.Context) bool {
	if l.vm == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return !l.vm.Running(ctx)
}

// bootReconcile reaps this instance's orphaned boxes and their stale homes at
// boot. With the VM stopped no box can be alive, so it waits for the first
// boxed spawn, whose VM start runs the same pass (prepare). It reports whether
// it ran: false only while the VM is stopped and its docker unreachable.
func (l *boxLayer) bootReconcile(ctx context.Context) bool {
	if l.vmStopped(ctx) {
		log.Printf("box VM stopped: box reconcile and reclaim wait for the first boxed spawn")
		return false
	}
	l.reconcileAndReclaim(ctx)
	return true
}

// reclaimEvery is how often the ticker runs a reclaim pass. Hourly because the
// caches of a dead box go an hour after its last use (box.DefaultCacheTTL), so a
// longer period would only delay them; a pass with nothing to do costs a
// directory listing and one docker call.
const reclaimEvery = time.Hour

// periodicReclaim is the reclaim ticker's pass. Silent while the VM is stopped:
// that is a normal state and must not log once an hour.
func (l *boxLayer) periodicReclaim(ctx context.Context) {
	if l.vmStopped(ctx) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, vmStartPassTimeout)
	defer cancel()
	l.reclaimBoxState(ctx)
}

// reconcileAndReclaim runs AFTER session reconcile (at boot) or right after a
// VM start: reconcile removes the orphaned containers, so what is left on disk
// belongs to boxes that are genuinely gone. The whole pass shares one deadline.
func (l *boxLayer) reconcileAndReclaim(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, vmStartPassTimeout)
	defer cancel()
	if err := l.boxes.Reconcile(ctx); err != nil {
		log.Printf("box reconcile: %v (continuing; an orphaned box may be left running)", err)
	}
	l.reclaimBoxState(ctx)
}

// reclaimBoxState removes the host homes of boxes that are gone and unused, and
// the caches of those unused for an hour, and reports what it freed.
// internal/box returns results rather than logging so its decisions stay
// testable without a daemon; the printing belongs here.
func (l *boxLayer) reclaimBoxState(ctx context.Context) {
	done, err := l.boxes.ReclaimState(ctx, box.DefaultStateTTL, box.DefaultCacheTTL)
	for _, r := range done {
		mib := r.Bytes / (1024 * 1024)
		if r.Caches {
			log.Printf("pruned caches of box state %s: unused for %s, %d MiB freed",
				r.Box, r.Age.Round(time.Minute), mib)
			continue
		}
		log.Printf("reclaimed box state %s: unused for %d days, %d MiB freed",
			r.Box, int(r.Age.Hours()/24), mib)
	}
	if err == nil {
		l.reclaimErr.changed("") // a clean pass: the same error later is news again
		return
	}
	// The same failure comes back every hour until someone fixes it, and says
	// the same thing each time.
	if l.reclaimErr.changed(err.Error()) {
		log.Printf("box state reclaim: %v. What it names stays (a whole home, or a cache "+
			"path inside one); usually a directory a box left unwritable (chmod it back), "+
			"otherwise a file owned by another uid or an old root-owned stub, which has to be "+
			"removed as its owner. New boxes create neither. Logged once while it stays the "+
			"same.", err)
	}
}

// logOnChange tells a periodic pass whether to log an error: the first time it
// appears, and again when it changes, but not while the same one repeats. The
// hourly reclaim would otherwise log a stuck directory 24 times a day.
type logOnChange struct {
	mu   sync.Mutex
	last string
}

// changed reports whether msg differs from the last one seen and remembers it.
// The empty message stands for a clean pass: it resets, so the same error
// coming back later is logged again, and it is never itself worth logging.
func (l *logOnChange) changed(msg string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if msg == l.last {
		return false
	}
	l.last = msg
	return msg != ""
}

// bootShares is the folder list the VM is built with: vm-shares.json, normalised
// (every Load consumer does). A missing or damaged file gives an empty list for
// now; skips are logged.
func bootShares(dataDir string, vc boxvm.Config, protected []box.ProtectedDir) []vmshares.Share {
	list, _, err := vmshares.Load(dataDir)
	if err != nil {
		log.Printf("box VM folders: %v; starting with none listed", err)
		return nil
	}
	out, skipped := vmshares.Normalize(list, vmshares.Env{Home: vc.Home, DataRoot: vc.Root, Fold: vc.Fold, Protected: protected})
	for _, s := range skipped {
		log.Printf("box VM folders: skipping %s: %s", s.Path, s.Reason)
	}
	return out
}

// shareView is the live view of the box VM's folders: Applied is what its
// written colima.yaml mounts (the only folders docker can see) minus the four
// data-root mounts, Listed is the user's list (vm-shares.json, resolved) whether
// applied yet or not. A mounts block that cannot be read fails closed. A VM
// with no colima.yaml yet has nothing applied and is no error: the spawn meets
// ErrNotSetUp in Prepare first.
func (l *boxLayer) shareView() (box.ShareView, error) {
	cfg := l.vm.Config()
	v := box.ShareView{Home: cfg.Home, Fold: l.goos == "darwin"}
	ms, err := l.vm.AppliedMounts()
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return box.ShareView{}, err
	default:
		data := map[string]bool{}
		for _, d := range boxvm.DataMounts(cfg.Root) {
			data[d.Location] = true
		}
		for _, m := range ms {
			if !data[m.Location] {
				v.Applied = append(v.Applied, box.VMShare{Path: m.Location, Writable: m.Writable})
			}
		}
	}
	for _, s := range cfg.Shares {
		v.Listed = append(v.Listed, box.VMShare{Path: s.Path, Writable: s.Mode == vmshares.RW})
	}
	return v, nil
}

// sharesFunc is shareView as a box.SharesFunc, nil without a VM (Linux: every
// path is visible).
func (l *boxLayer) sharesFunc() box.SharesFunc {
	if l == nil || l.vm == nil {
		return nil
	}
	return l.shareView
}
