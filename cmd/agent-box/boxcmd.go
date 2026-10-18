package main

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"
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
		return boxSetupImage(ctx, boximage.Docker{Bin: bin}, cfg.BoxImageOrDefault(), a.Rebuild, time.Now(), w)
	case "status":
		rc := boxImageStatus(ctx, boximage.Docker{Bin: bin}, cfg.BoxImageOrDefault(), w)
		return max(rc, boxListStatus(ctx, box.CLIClient{Bin: bin}, w))
	default: // stop
		fmt.Fprintln(w, "agent-box box stop: only on macOS, where boxes run in agent-box's own VM")
		return 1
	}
}

// boxSetupImage builds the image unless a current, native one is present. Every
// build also gets a dated tag. A dated box_image is pinned: it is never built,
// rebuilt or moved, and -rebuild on it builds a new dated tag instead.
func boxSetupImage(ctx context.Context, d boximage.Docker, image string, rebuild bool, now time.Time, w io.Writer) int {
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
	pinned := boximage.IsDated(image)
	fatal, msg := st.CheckFor(image, arch)
	if !rebuild && !fatal {
		if msg == "" {
			fmt.Fprintf(w, "PASS image: %s is current (linux/%s)\n", image, arch)
			return 0
		}
		if pinned {
			fmt.Fprintf(w, "WARN image: %s (linux/%s): %s\n", image, st.Arch, msg)
			return 0
		}
	}
	if !rebuild && fatal && pinned {
		fmt.Fprintf(w, "FAIL image: %s\n", msg)
		return 1
	}
	repo, err := boximage.Repo(image)
	if err != nil {
		fmt.Fprintln(w, "FAIL image: box_image is a digest; agent-box cannot build it")
		return 1
	}
	dated, err := d.NextDatedTag(ctx, repo, now)
	if err != nil {
		fmt.Fprintf(w, "FAIL image: %v\n", err)
		return 1
	}
	tags := []string{dated}
	if !pinned {
		tags = append(tags, image)
	}
	fmt.Fprintf(w, "building %s for linux/%s ...\n", strings.Join(tags, ", "), arch)
	if err := d.Build(ctx, tags, w); err != nil {
		fmt.Fprintf(w, "FAIL image: %v\n", err)
		return 1
	}
	fmt.Fprintf(w, "PASS image: built %s (linux/%s, dockerfile %s)\n", dated, arch, boximage.Hash())
	if pinned {
		fmt.Fprintf(w, "this config stays on %s until you set \"box_image\": \"%s\"\n", image, dated)
		return 0
	}
	fmt.Fprintf(w, "PASS image: %s now points at %s\n", image, dated)
	fmt.Fprintf(w, "pin this instance: set \"box_image\": \"%s\" in its config\n", dated)
	return 0
}

// boxImageStatus prints the image's state against the daemon: present, built
// for the daemon's architecture, and from this binary's Dockerfile. A missing or
// foreign image fails (no box can start); a stale one only warns.
func boxImageStatus(ctx context.Context, d boximage.Docker, image string, w io.Writer) int {
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
	switch fatal, msg := st.CheckFor(image, arch); {
	case fatal:
		fmt.Fprintf(w, "FAIL image: %s: %s\n", image, msg)
		return 1
	case msg != "":
		fmt.Fprintf(w, "WARN image: %s (linux/%s): %s\n", image, st.Arch, msg)
	default:
		fmt.Fprintf(w, "image: %s is current (linux/%s)\n", image, st.Arch)
	}
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
