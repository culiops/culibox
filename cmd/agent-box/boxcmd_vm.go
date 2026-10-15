package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/boxvm"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/doctor"
)

// setupProbeTimeout bounds box setup's containment probe (a throwaway box).
const setupProbeTimeout = 2 * time.Minute

// runBoxDarwin serves `agent-box box …` on macOS, where boxes run in agent-box's
// own colima VM. Untagged (runBox gates it on GOOS) so its logic is tested on
// Linux against fakes.
func runBoxDarwin(a cliArgs, cfg config.Config, home string, w io.Writer) int {
	// Absolute and clean, as serve and doctor derive it: the VM's paths are
	// built from this string.
	dataDir, err := filepath.Abs(cfg.ResolvedDataDir(home))
	if err != nil {
		fmt.Fprintf(w, "FAIL data dir: %v\n", err)
		return 1
	}
	l, err := newBoxLayer(cfg, dataDir, home, "darwin", boxvm.ExecRunner{})
	if err != nil {
		fmt.Fprintf(w, "FAIL box VM: %v\n", err)
		return 1
	}
	return runBoxVM(context.Background(), a, cfg, l, w)
}

// runBoxVM is `box setup|stop|status` against the layer's VM. Only setup ever
// starts the VM.
func runBoxVM(ctx context.Context, a cliArgs, cfg config.Config, l *boxLayer, w io.Writer) int {
	switch a.Sub {
	case "status":
		return boxStatusVM(ctx, l, w)
	case "stop":
		return boxStopVM(ctx, a.Force, l, w)
	}
	return boxSetupVM(ctx, a, cfg, l, w)
}

func boxStatusVM(ctx context.Context, l *boxLayer, w io.Writer) int {
	st, err := l.vm.Status(ctx)
	switch {
	case err != nil:
		fmt.Fprintf(w, "FAIL box VM: %v\n", err)
		return 1
	case !l.vm.SetUp():
		fmt.Fprintln(w, "box VM: not set up (run `agent-box box setup`)")
		return 0
	case !st.Running:
		fmt.Fprintln(w, "box VM: stopped (starts on the first boxed spawn)")
		return 0
	}
	fmt.Fprintf(w, "box VM: running, %s, mount %s, arch %s\n", st.Driver, st.MountType, st.Arch)
	rc := boxImageStatus(ctx, l.img, l.image, w)
	return max(rc, boxListStatus(ctx, l.cli, w))
}

// boxStopVM stops the VM, refusing while any box runs: an idle box counts, since
// stopping the VM ends whatever session is attached to it. A listing that fails
// refuses too, rather than reading as "no boxes".
func boxStopVM(ctx context.Context, force bool, l *boxLayer, w io.Writer) int {
	running := 0
	if l.vm.Running(ctx) {
		bs, err := l.cli.ListBoxes(ctx, box.LabelInstance)
		if err != nil {
			fmt.Fprintf(w, "FAIL box VM: listing boxes: %v\n", err)
			return 1
		}
		for _, b := range bs {
			if b.State == "running" {
				running++
				fmt.Fprintf(w, "running: %s %s\n", b.Name, b.Workspace)
			}
		}
	}
	if running > 0 && !force {
		fmt.Fprintf(w, "refusing: %d box(es) running (idle boxes count; their sessions would end). Use -force.\n", running)
		return 1
	}
	if err := l.vm.Stop(ctx); err != nil {
		fmt.Fprintf(w, "FAIL box VM stop: %v\n", err)
		return 1
	}
	fmt.Fprintln(w, "box VM stopped")
	return 0
}

// boxSetupVM provisions and starts the VM, builds the image, proves the
// loopback block, and checks that the user's docker context is still theirs.
// The colima flags it starts with (boxvm.Config.StartArgs) keep the user's
// docker context and ~/.ssh/config untouched; the context check confirms it.
func boxSetupVM(ctx context.Context, a cliArgs, cfg config.Config, l *boxLayer, w io.Writer) int {
	if major := boxvm.MacOSMajor(ctx); major != 0 && major < 13 {
		fmt.Fprintf(w, "FAIL macOS %d: the box VM needs macOS 13+ (vz + virtiofs)\n", major)
		return 1
	}
	if err := l.vm.Provision(ctx, a.Recreate, w); err != nil {
		fmt.Fprintf(w, "FAIL box VM: %v\n", err)
		return 1
	}
	st, err := l.vm.Status(ctx)
	if err != nil || !st.Running {
		fmt.Fprintf(w, "FAIL box VM: not running after start (%v)\n", err)
		return 1
	}
	if want := l.vm.Config().DockerSocket(); st.DockerSocket != want {
		fmt.Fprintf(w, "FAIL box VM: docker socket is %q, agent-box expects %q\n", st.DockerSocket, want)
		return 1
	}
	if st.MountType != "virtiofs" {
		fmt.Fprintf(w, "WARN box VM: mount type %s (virtiofs is about 3.5x faster); run `agent-box box setup -recreate`\n", st.MountType)
	}
	fmt.Fprintf(w, "PASS box VM: running (%s, %s)\n", st.Driver, st.MountType)
	if rc := boxSetupImage(ctx, l.img, l.image, a.Rebuild, w); rc != 0 {
		return rc
	}
	pctx, cancel := context.WithTimeout(ctx, setupProbeTimeout)
	defer cancel()
	if err := boxvm.ProbeLoopback(pctx, boxvm.ThrowawayProbe(l.cli, l.image)); err != nil {
		fmt.Fprintf(w, "FAIL containment: %v\n", err)
		return 1
	}
	fmt.Fprintln(w, "PASS containment: a box cannot reach the Mac's loopback services")
	switch err := boxvm.CheckUserDockerContext(ctx, l.dockerBin); {
	case errors.Is(err, boxvm.ErrContextUnchecked):
		fmt.Fprintf(w, "WARN docker context: %v\n", err)
	case err != nil:
		fmt.Fprintf(w, "FAIL docker context: %v\n", err)
		return 1
	default:
		fmt.Fprintln(w, "PASS docker context: yours is untouched")
	}
	r := doctor.ClaudeBoxToken(cfg)
	fmt.Fprintf(w, "%s %s: %s\n", r.Status, r.Check, r.Detail)
	return 0
}
