package main

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"time"

	boximage "github.com/culiops/agent-box/docker/agent-box"
	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/config"
)

// runBox serves `agent-box box …`. It never touches a running daemon's state
// beyond docker objects, so it is safe to run while agent-box serves.
func runBox(a cliArgs, cfg config.Config, home string, w io.Writer) int {
	if runtime.GOOS == "darwin" {
		return runBoxDarwin(a, cfg, home, w)
	}
	bin, err := box.LookDocker(cfg.DockerBin)
	if err != nil {
		fmt.Fprintf(w, "agent-box box: %v\n", err)
		return 1
	}
	ctx := context.Background()
	switch a.Sub {
	case "setup":
		return boxSetupImage(ctx, boximage.Docker{Bin: bin}, boxImage(cfg), a.Rebuild, w)
	case "status":
		return boxListStatus(ctx, box.CLIClient{Bin: bin}, w)
	default: // stop
		fmt.Fprintln(w, "agent-box box stop: only on macOS, where boxes run in agent-box's own VM")
		return 1
	}
}

// boxSetupImage builds the image unless a current, native one is present.
func boxSetupImage(ctx context.Context, d boximage.Docker, image string, rebuild bool, w io.Writer) int {
	arch, err := d.ServerArch(ctx)
	if err != nil {
		fmt.Fprintf(w, "FAIL image: %v\n", err)
		return 1
	}
	st, err := d.Inspect(ctx, image)
	if err != nil {
		fmt.Fprintf(w, "FAIL image: %v\n", err)
		return 1
	}
	if fatal, msg := st.Check(arch); !rebuild && !fatal && msg == "" {
		fmt.Fprintf(w, "PASS image: %s is current (linux/%s)\n", image, arch)
		return 0
	}
	fmt.Fprintf(w, "building %s for linux/%s ...\n", image, arch)
	if err := d.Build(ctx, image, w); err != nil {
		fmt.Fprintf(w, "FAIL image: %v\n", err)
		return 1
	}
	fmt.Fprintf(w, "PASS image: built %s (linux/%s, dockerfile %s)\n", image, arch, boximage.Hash())
	return 0
}

// boxListStatus prints every agent-box box container, all instances. Session
// counts live in the daemon; the sweep log covers orphans.
func boxListStatus(ctx context.Context, c box.CLIClient, w io.Writer) int {
	boxes, err := c.ListBoxes(ctx, box.LabelInstance)
	if err != nil {
		fmt.Fprintf(w, "FAIL boxes: %v\n", err)
		return 1
	}
	if len(boxes) == 0 {
		fmt.Fprintln(w, "no boxes")
		return 0
	}
	for _, b := range boxes {
		age := "?"
		if !b.Created.IsZero() {
			age = time.Since(b.Created).Round(time.Minute).String()
		}
		fmt.Fprintf(w, "%-16s %-8s %-8s %s\n", b.Name, b.State, age, b.Workspace)
	}
	return 0
}
