package main

import (
	"context"
	"fmt"
	"log"
	"time"

	boximage "github.com/culiops/agent-box/docker/agent-box"
	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/boxvm"
	"github.com/culiops/agent-box/internal/bundle"
	"github.com/culiops/agent-box/internal/config"
)

// verifyTimeout bounds the containment probe of a new box. The spawn holds the
// box manager's operation lock while it runs, so a hung exec must not hold it
// (and every other spawn) indefinitely. A var so a test can shorten it.
var verifyTimeout = 30 * time.Second

// imageCheckTimeout bounds Prepare's image check (two quick docker calls).
const imageCheckTimeout = 30 * time.Second

// vmStartPassTimeout bounds the reconcile Prepare runs after starting the VM,
// as sweepBoxes bounds the sweep: it holds the operation lock too.
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
	stateRoot  string // where box homes and the bundle live; resolved on darwin
}

// newBoxLayer builds the containment layer, or reports why it is unavailable.
// On darwin the layer exists whenever colima and docker resolve. Whether the
// VM is set up, running and has a fitting image is decided per spawn (prepare),
// and reaches the user as ErrBoxUnavailable with its remedy. Nothing here
// starts the VM (D3).
func newBoxLayer(cfg config.Config, dataDir, home, goos string, runner boxvm.Runner) (*boxLayer, error) {
	bin, err := box.LookDocker(cfg.DockerBin)
	if err != nil {
		return nil, err
	}
	l := &boxLayer{dockerBin: bin, image: cfg.BoxImageOrDefault(), stateRoot: dataDir}
	if goos == "darwin" {
		colima, err := boxvm.LookColima(cfg.ColimaBin)
		if err != nil {
			return nil, err
		}
		l.vm = boxvm.NewVM(boxvm.ConfigFor(cfg, dataDir, home, colima, bin), runner)
		// Symlink-resolved: the VM mounts these paths, so every path a box
		// mounts or a workspace is checked against must be spelled the same way.
		vc := l.vm.Config()
		l.stateRoot = vc.Root
		l.dockerArgs = vc.DockerArgs()
	}
	l.cli = box.CLIClient{Bin: bin, Args: l.dockerArgs}
	l.img = boximage.Docker{Bin: bin, Args: l.dockerArgs}
	m, err := box.New(l.options(cfg))
	if err != nil {
		return nil, err
	}
	l.boxes = m
	return l, nil
}

// options is what the box manager is built with. Off darwin there are no hooks:
// Linux behaves exactly as before the VM existed.
func (l *boxLayer) options(cfg config.Config) box.Options {
	o := box.Options{
		DockerBin:  l.dockerBin,
		DockerArgs: l.dockerArgs,
		Image:      l.image,
		Instance:   cfg.BoxInstance(),
		IdleTTL:    cfg.BoxIdleTTL(),
		StateRoot:  l.stateRoot,
		Client:     l.cli,
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
	started, err := l.vm.EnsureRunning(ctx)
	if err != nil {
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
	if fatal, msg := st.Check(arch); fatal {
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
	err := boxvm.ProbeLoopback(ctx, func(ctx context.Context, script string) (string, error) {
		return l.cli.Run(ctx, []string{"exec", name, "node", "-e", script})
	})
	if err != nil {
		return fmt.Errorf("%w: %v", box.ErrBoxUnavailable, err)
	}
	return nil
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
	reclaimBoxState(l.boxes)
}

// reconcileAndReclaim runs AFTER session reconcile (at boot) or right after a
// VM start: reconcile removes the orphaned containers, so what is left on disk
// belongs to boxes that are genuinely gone.
func (l *boxLayer) reconcileAndReclaim(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, vmStartPassTimeout)
	defer cancel()
	if err := l.boxes.Reconcile(ctx); err != nil {
		log.Printf("box reconcile: %v (continuing; an orphaned box may be left running)", err)
	}
	reclaimBoxState(l.boxes)
}
