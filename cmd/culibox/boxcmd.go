package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	boximage "github.com/culiops/culibox/docker/agent-box"
	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
)

// runBox serves `agent-box box …`. It never touches a running daemon's state
// beyond docker objects, so it is safe to run while agent-box serves.
func runBox(a cliArgs, cfg config.Config, home string, cfgFiles []string, w io.Writer) int {
	if runtime.GOOS == "darwin" {
		return runBoxDarwin(a, cfg, home, cfgFiles, w)
	}
	if a.Sub == "share" {
		fmt.Fprintln(w, "culibox box share: only on macOS, where boxes run in CuliBox's own VM")
		return 1
	}
	bin, err := box.LookDocker(cfg.DockerBin)
	if err != nil {
		fmt.Fprintf(w, "culibox box: %v\n", err)
		return 1
	}
	ctx := context.Background()
	switch a.Sub {
	case "setup":
		set, err := cfg.ExtraCA()
		if err != nil {
			fmt.Fprintf(w, "FAIL extra-ca: %v\n", err)
			return 1
		}
		printExtraCA(w, set, time.Now())
		d := boximage.Docker{Bin: bin, ExtraCA: set.PEM()}
		adoptDefaultImage(ctx, d, cfg, w)
		dataDir, err := filepath.Abs(cfg.ResolvedDataDir(home))
		if err != nil {
			dataDir = ""
		}
		src := pinSource{cfg: cfg, home: home, dataDir: dataDir, xdg: xdgConfigHome(), cfgFiles: cfgFiles}
		return boxSetupAndPrune(ctx, d, cfg.BoxImageOrDefault(), a, src, time.Now(), runtime.GOOS, w)
	case "status":
		d := boximage.Docker{Bin: bin}
		adoptDefaultImage(ctx, d, cfg, w)
		rc := boxImageStatus(ctx, d, cfg.BoxImageOrDefault(), w)
		return max(rc, boxListStatus(ctx, box.CLIClient{Bin: bin}, w))
	default: // stop
		fmt.Fprintln(w, "culibox box stop: only on macOS, where boxes run in CuliBox's own VM")
		return 1
	}
}

// adoptedImageMsg is the line that says the legacy default was re-tagged.
const adoptedImageMsg = "box image: tagged " + config.LegacyDefaultBoxImage + " as " + config.DefaultBoxImage + " (kept the old tag)"

// adoptDefaultImage is the legacy-default adoption for `box setup` and `box
// status`, which run before the daemon: with box_image unset and the new default
// missing, an upgraded install's culiops/agent-box:base is tagged as the new
// default (the old tag stays). A pinned box_image is never touched. A daemon
// that does not answer is left to the image check that follows.
func adoptDefaultImage(ctx context.Context, d boximage.Docker, cfg config.Config, w io.Writer) {
	if cfg.BoxImage != "" {
		return
	}
	adopted, err := boximage.AdoptLegacyDefault(ctx, d, config.DefaultBoxImage, config.LegacyDefaultBoxImage)
	switch {
	case err != nil:
		fmt.Fprintf(w, "WARN image: could not adopt %s as %s: %v\n", config.LegacyDefaultBoxImage, config.DefaultBoxImage, err)
	case adopted:
		fmt.Fprintln(w, adoptedImageMsg)
	}
}

// boxSetupImage builds the image unless a current, native one is present. Every
// build also gets a dated tag. A dated box_image is pinned: it is never built,
// rebuilt or moved, and -rebuild on it builds a new dated tag instead.
func boxSetupImage(ctx context.Context, d boximage.Docker, image string, rebuild bool, now time.Time, goos string, w io.Writer) int {
	rc, _ := boxSetupImageRefs(ctx, d, image, "", rebuild, now, goos, w)
	return rc
}

// publishedBoxImage is the ref a release binary pulls for the default image
// (boximage.Published); a variable so tests can stand in for a release build.
var publishedBoxImage = boximage.Published

// pullFrom is where `box setup` gets the default image: the published ref this
// binary carries, when the config names no box_image and -rebuild was not given.
// "" means the local build, as before #335: a source build, an explicit box_image
// (even one that names the default), and -rebuild never pull it.
func pullFrom(cfgBoxImage, image, published string, rebuild bool) string {
	if cfgBoxImage != "" || image != config.DefaultBoxImage || published == "" || rebuild {
		return ""
	}
	return published
}

// boxSetupImageRefs is boxSetupImage, and also returns the names of the image(s)
// it left current: the configured image, plus the new dated tag when it built
// one. With a pinned box_image a rebuild leaves the config on the old pin and
// builds a NEW dated tag, so both are what setup produced and neither may be
// pruned (#6). nil on failure.
//
// from (pullFrom) is the published ref the default image comes from: a default
// that is missing, stale or of another arch is pulled from it and named image; a
// pull that fails or brings an unusable image falls back to the build, and says
// why.
func boxSetupImageRefs(ctx context.Context, d boximage.Docker, image, from string, rebuild bool, now time.Time, goos string, w io.Writer) (int, []string) {
	arch, err := d.ServerArch(ctx)
	if err != nil {
		fmt.Fprintf(w, "FAIL image: %v\n", err)
		return 1, nil
	}
	st, err := d.Inspect(ctx, image)
	if err != nil {
		fmt.Fprintf(w, "FAIL image: %v\n", err)
		return 1, nil
	}
	if boximage.IsRemote(image) {
		rc := boxSetupRemote(ctx, d, image, arch, st, rebuild, goos, w)
		return rc, refsIf(rc, image)
	}
	pinned := boximage.IsDated(image)
	fatal, msg := st.CheckFor(image, arch)
	if !rebuild && !fatal {
		if msg == "" {
			fmt.Fprintf(w, "PASS image: %s is current (linux/%s)\n", image, arch)
			return 0, []string{image}
		}
		if pinned {
			fmt.Fprintf(w, "WARN image: %s\n", imageLine(image, st.Arch, msg))
			return 0, []string{image}
		}
	}
	if !rebuild && fatal && pinned {
		fmt.Fprintf(w, "FAIL image: %s\n", msg)
		return 1, nil
	}
	if from != "" && !rebuild && !pinned {
		if done, rc := setupPublished(ctx, d, image, from, arch, goos, w); done {
			return rc, refsIf(rc, image, from)
		}
	}
	repo, err := boximage.Repo(image)
	if err != nil {
		fmt.Fprintln(w, "FAIL image: box_image is a digest; CuliBox cannot build it")
		return 1, nil
	}
	dated, err := d.NextDatedTag(ctx, repo, now, image)
	if err != nil {
		fmt.Fprintf(w, "FAIL image: %v\n", err)
		return 1, nil
	}
	tags := []string{dated}
	if !pinned {
		tags = append(tags, image)
	}
	fmt.Fprintf(w, "building %s for linux/%s ...\n", strings.Join(tags, ", "), arch)
	if err := d.Build(ctx, tags, w); err != nil {
		fmt.Fprintf(w, "FAIL image: %v\n", err)
		hintBuildFailure(ctx, w, d, goos, err)
		return 1, nil
	}
	fmt.Fprintf(w, "PASS image: built %s (linux/%s, dockerfile %s)\n", dated, arch, boximage.Hash())
	if pinned {
		fmt.Fprintf(w, "this config stays on %s until you set \"box_image\": \"%s\"\n", image, dated)
		return 0, tags
	}
	fmt.Fprintf(w, "PASS image: %s now points at %s\n", image, dated)
	fmt.Fprintf(w, "pin this instance: set \"box_image\": \"%s\" in its config\n", dated)
	return 0, tags
}

// refsIf is the image(s) just set up when rc says it worked.
func refsIf(rc int, images ...string) []string {
	if rc != 0 {
		return nil
	}
	return images
}

// setupPublished pulls from (the published default) for the daemon's arch and
// names it image. done is false when the pull failed or brought an image this
// binary cannot run (another arch, another Dockerfile): the reason is printed and
// the caller builds instead. A failed tag is done with rc 1: the daemon refused a
// metadata change, and a build would end in the same tag.
func setupPublished(ctx context.Context, d boximage.Docker, image, from, arch, goos string, w io.Writer) (done bool, rc int) {
	fallback := func(reason string) (bool, int) {
		fmt.Fprintf(w, "WARN image: could not use the published image %s: %s; building it here instead (this takes several minutes)\n", from, reason)
		return false, 0
	}
	// Already in the store and usable (the default name was removed, or another
	// instance pulled it): only the name is missing, so no pull, also offline.
	st, err := d.Inspect(ctx, from)
	if err != nil {
		return fallback(err.Error())
	}
	pulled := false
	if !st.Present || st.Arch != arch || st.Hash != boximage.Hash() {
		pulled = true
		fmt.Fprintf(w, "pulling %s for linux/%s ...\n", from, arch)
		if err := d.Pull(ctx, from, arch, w); err != nil {
			hintPullFailure(ctx, w, d, goos, err)
			return fallback(err.Error())
		}
		if st, err = d.Inspect(ctx, from); err != nil {
			return fallback(err.Error())
		}
	}
	switch {
	case !st.Present:
		return fallback("the daemon does not show it after the pull")
	case st.Arch != arch:
		return fallback(fmt.Sprintf("it is linux/%s but the daemon is %s", st.Arch, arch))
	case st.Hash != boximage.Hash():
		return fallback(fmt.Sprintf("it was built from Dockerfile %q, this CuliBox carries %s", st.Hash, boximage.Hash()))
	}
	if pulled {
		fmt.Fprintf(w, "PASS image: pulled %s (linux/%s)\n", from, arch)
	} else {
		fmt.Fprintf(w, "PASS image: %s is already here (linux/%s)\n", from, arch)
	}
	if err := d.Tag(ctx, from, image); err != nil {
		fmt.Fprintf(w, "FAIL image: %v\n", err)
		return true, 1
	}
	fmt.Fprintf(w, "PASS image: %s now points at %s\n", image, from)
	fmt.Fprintf(w, "pin this instance: set \"box_image\": \"%s\" in its config\n", from)
	return true, 0
}

// boxSetupRemote makes a digest or registry box_image present: it pulls a
// missing one for the daemon's architecture and never builds it. An image that
// is already there is only checked, never re-pulled.
func boxSetupRemote(ctx context.Context, d boximage.Docker, image, arch string, st boximage.ImageState, rebuild bool, goos string, w io.Writer) int {
	if !strings.Contains(image, "@sha256:") {
		// A registry tag can be moved by whoever pushes it; only a digest pins.
		fmt.Fprintf(w, "WARN image: %s is a mutable tag; pin box_image by digest (ghcr.io/culiops/culibox@sha256:…)\n", image)
	}
	if rebuild {
		fmt.Fprintf(w, "FAIL image: %s comes from a registry; CuliBox pulls it and never builds it\n", image)
		return 1
	}
	fatal, msg := st.CheckFor(image, arch)
	if !fatal {
		if msg == "" {
			fmt.Fprintf(w, "PASS image: %s is current (linux/%s)\n", image, arch)
		} else {
			fmt.Fprintf(w, "WARN image: %s\n", imageLine(image, st.Arch, msg))
		}
		return 0
	}
	if st.Present {
		// Present but unusable (wrong arch): pulling the same ref again would
		// not change it. Say why, so the digest gets fixed.
		fmt.Fprintf(w, "FAIL image: %s\n", msg)
		return 1
	}
	fmt.Fprintf(w, "pulling %s for linux/%s ...\n", image, arch)
	if err := d.Pull(ctx, image, arch, w); err != nil {
		fmt.Fprintf(w, "FAIL image: %v\n", err)
		hintPullFailure(ctx, w, d, goos, err)
		return 1
	}
	st, err := d.Inspect(ctx, image)
	if err != nil {
		fmt.Fprintf(w, "FAIL image: %v\n", err)
		return 1
	}
	fatal, msg = st.CheckFor(image, arch)
	if fatal {
		fmt.Fprintf(w, "FAIL image: %s\n", msg)
		return 1
	}
	fmt.Fprintf(w, "PASS image: pulled %s (linux/%s)\n", image, arch)
	if msg != "" {
		fmt.Fprintf(w, "WARN image: %s\n", imageLine(image, st.Arch, msg))
	}
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
		fmt.Fprintf(w, "FAIL image: %s\n", imageLine(image, "", msg))
		return 1
	case msg != "":
		fmt.Fprintf(w, "WARN image: %s\n", imageLine(image, st.Arch, msg))
	default:
		fmt.Fprintf(w, "image: %s is current (linux/%s)\n", image, st.Arch)
	}
	return 0
}

// imageLine words a status line about image. A pinned-image message already
// names the image, so it is not prefixed with it again.
func imageLine(image, arch, msg string) string {
	if strings.Contains(msg, image) {
		return msg
	}
	if arch != "" {
		return fmt.Sprintf("%s (linux/%s): %s", image, arch, msg)
	}
	return image + ": " + msg
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
