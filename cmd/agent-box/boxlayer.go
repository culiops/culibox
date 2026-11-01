package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	boximage "github.com/culiops/agent-box/docker/agent-box"
	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/boxvm"
	"github.com/culiops/agent-box/internal/bundle"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/egress"
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
	image      string // the fallback box image (box_image)
	goos       string // runtime.GOOS as the layer was built; linux fences and guards every box
	dataDir    string // the absolute data dir, for the store doctor and box setup read
	stateRoot  string // where box homes and the bundle live; resolved on darwin
	// egressProbe is egress.Probe; a test replaces it.
	egressProbe func(ctx context.Context, p *egress.Proxy, box string, t egress.ProbeTarget, exec egress.ExecInput) error
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
	l := &boxLayer{goos: goos, dockerBin: bin, image: cfg.BoxImageOrDefault(), stateRoot: dataDir, dataDir: dataDir}
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
		l.vm = boxvm.NewVM(configFor(cfg, dataDir, home, colima, bin, hostCPUs, hostMem), runner)
		// Symlink-resolved: the VM mounts these paths, so every path a box
		// mounts or a workspace is checked against must be spelled the same way.
		vc := l.vm.Config()
		l.stateRoot = vc.Root
		l.dockerArgs = vc.DockerArgs()
	}
	l.cli = box.CLIClient{Bin: bin, Args: l.dockerArgs}
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
		// option (c) relay, since #69). Only Linux has its guard files
		// read-only in place (#68).
		EgressAll:    true,
		GuardInPlace: l.goos == "linux",
	}
	if l.vm != nil {
		o.Prepare = l.prepare
		o.Verify = l.verify
		o.Shared = l.vm.Config().Mounts()
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
	return " (the box VM's provision config is out of date, so its egress rules may be missing; run `agent-box box setup -recreate`)"
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
// boxed spawn, whose VM start runs the same pass (prepare).
func (l *boxLayer) bootReconcile(ctx context.Context) {
	if l.vmStopped(ctx) {
		log.Printf("box VM stopped: box reconcile and reclaim wait for the first boxed spawn")
		return
	}
	l.reconcileAndReclaim(ctx)
}

// dailyReclaim is the reclaim ticker's pass. Silent while the VM is stopped:
// that is a normal state and must not log once a day.
func (l *boxLayer) dailyReclaim(ctx context.Context) {
	if l.vmStopped(ctx) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, vmStartPassTimeout)
	defer cancel()
	reclaimBoxState(ctx, l.boxes)
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
	reclaimBoxState(ctx, l.boxes)
}
