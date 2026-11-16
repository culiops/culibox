package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	boximage "github.com/culiops/culibox/docker/agent-box"
	"github.com/culiops/culibox/internal/config"
)

// pruneTimeout bounds the whole prune: reading configs, a few docker calls, and
// the removals.
const pruneTimeout = 10 * time.Minute

// xdgConfigHome is $XDG_CONFIG_HOME as main reads it for the config lookup. It is
// read again here because `box setup` also looks at the OTHER instance's default
// ~/.config files, and runBox is handed only the config files this instance reads.
func xdgConfigHome() string { return os.Getenv("XDG_CONFIG_HOME") }

// pinSource is where `box setup` looks for the box images instances pin.
type pinSource struct {
	cfg      config.Config
	home     string   // "" skips the default roots and ~/.config
	dataDir  string   // this instance's data root, "" when unknown
	xdg      string   // $XDG_CONFIG_HOME ("" = ~/.config)
	cfgFiles []string // every config file this instance reads (config.ConfigFiles)
}

// imagePins are the box images the configs this CuliBox can see pin.
type imagePins struct {
	live     []string // box_image of every visible config, and of this one: never pruned
	rollback []string // box_image of configs inside retained upgrade backups that no registry can supply again
}

// pins reads every config an instance sharing this Docker store could be running
// from, as far as this process can find them, within ctx's deadline:
//   - this instance's config files (-config, or the data root's and the ~/.config
//     ones), and its loaded config;
//   - the two default data roots' config.json and the two ~/.config files, even
//     when this instance runs from elsewhere (CULIBOX_HOME, -config): the usual
//     second instance is the default one;
//   - config.json inside each data root's retained upgrade backups, which the
//     documented rollback restores (docs/UPGRADING.md "Rolling back").
//
// Only regular files are read (a FIFO or a device at one of these paths would
// block the read). A config that exists but cannot be read or parsed is an
// error, not an empty pin.
//
// NOT found: an instance whose config sits at another path (its own -config or
// CULIBOX_HOME). Its boxes are removed when idle, so "no container" is its normal
// state, and nothing on disk or in Docker names it. That is why PruneOld removes
// only images a registry can supply again, plus the one this setup orphaned, and
// lists the rest.
func (s pinSource) pins(ctx context.Context) (imagePins, error) {
	type result struct {
		p   imagePins
		err error
	}
	ch := make(chan result, 1)
	go func() {
		p, err := s.readPins()
		ch <- result{p, err}
	}()
	select {
	case r := <-ch:
		return r.p, r.err
	case <-ctx.Done():
		return imagePins{}, fmt.Errorf("reading the configs: %w", ctx.Err())
	}
}

func (s pinSource) readPins() (imagePins, error) {
	var p imagePins
	add := func(dst *[]string, ref string) {
		if ref != "" && !containsRef(*dst, ref) {
			*dst = append(*dst, ref)
		}
	}
	add(&p.live, s.cfg.BoxImageOrDefault())
	roots := []string{}
	files := append([]string(nil), s.cfgFiles...)
	if s.dataDir != "" {
		roots = append(roots, s.dataDir)
	}
	if s.home != "" {
		newRoot, legacy := config.DefaultRoots(s.home)
		roots = append(roots, newRoot, legacy)
		base := config.XDGBase(s.home, s.xdg)
		files = append(files, filepath.Join(base, config.RootName, "config.json"), filepath.Join(base, config.LegacyRootName, "config.json"))
	}
	var backups []string
	for _, r := range roots {
		files = append(files, filepath.Join(r, "config.json"))
		m, _ := filepath.Glob(filepath.Join(r, "backups", "*", "config.json"))
		backups = append(backups, m...)
	}
	for _, f := range dedupe(files) {
		ref, err := boxImageOf(f)
		if err != nil {
			return imagePins{}, err
		}
		add(&p.live, ref)
	}
	for _, f := range dedupe(backups) {
		ref, err := boxImageOf(f)
		if err != nil {
			return imagePins{}, err
		}
		// A registry ref can be pulled again by `box setup`; a dated tag or :base
		// is only ever built here (box setup never pulls one), so what a restored
		// backup pins in that form can only be kept, not re-fetched. Decided by the
		// pin's form, not by what the image shows: the containerd image store shows
		// a digest on local builds too.
		if ref != "" && !boximage.IsRemote(ref) {
			add(&p.rollback, ref)
		}
	}
	return p, nil
}

// boxImageOf is the box_image a config file names ("" for none, for no file, and
// for anything that is not a regular file).
func boxImageOf(path string) (string, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return "", nil
	}
	c, err := config.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return c.BoxImage, nil
}

func dedupe(paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		p = filepath.Clean(p)
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func containsRef(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// boxSetupAndPrune is `box setup`'s image step: set the image up, and when that
// succeeded, remove the old box images nothing needs (pruneOldBoxImages). -no-prune
// skips the second part. The prune never changes the exit code.
//
// What it must keep is what the setup produced, which is not always the configured
// name: with a pinned (dated) box_image, -rebuild builds a NEW dated tag and leaves
// the config on the old pin. boxSetupImageRefs names both. The image the configured
// name held BEFORE the setup is read first: that is the image the setup orphaned.
func boxSetupAndPrune(ctx context.Context, d boximage.Docker, image string, a cliArgs, src pinSource, now time.Time, goos string, w io.Writer) int {
	orphan := ""
	if !a.NoPrune {
		orphan = d.OrphanOf(ctx, image)
	}
	rc, made := boxSetupImageRefs(ctx, d, image, a.Rebuild, now, goos, w)
	if rc != 0 || a.NoPrune {
		return rc
	}
	keep := append([]string{image}, made...)
	pruneOldBoxImages(ctx, d, keep, orphan, a.PruneLocal, src, goos, w)
	return rc
}

// pruneOldBoxImages removes the box images nothing needs, after `box setup` made
// keep current (#6). Why it exists: every upgrade left the previous ~4 GB image in
// the store for good (one upgrade took a 49 GB data root from 77% to 99% full).
//
// The default is the safest that fixes that, because the risk of a wrong delete
// lands on the user and a local build cannot be brought back. Boxes are removed
// when idle and at serve start, so another instance's "no container" is its normal
// state, and this process cannot see its config if that sits at another path. So,
// by default, it removes only:
//   - images a registry can supply again (named only under ghcr.io/culiops/…, with
//     a registry digest): the worst a wrong guess costs is a `box setup` pull;
//   - the one image this setup orphaned (the configured name held it before the
//     pull or build moved the name): the default :base upgrade leak.
//
// Every other unused local box image is listed, with its size and the line that
// removes it, and removed only with -prune-local. All of it is subject to the keep
// rules (see boximage.Docker.PruneOld).
//
// Rollback (#229). A binary-only rollback (<bin>.prev) reads the same config, so
// it runs the image just set up, which is never removed. The documented full
// rollback also restores <data root>/backups/*/config.json, and that config can
// pin an older image. A pin to a registry ref can be pulled again by `box setup`.
// A pin to a dated tag or :base cannot (box setup never pulls one, and a missing
// pinned tag is fatal), so an image a retained backup's config pins in that form is
// kept, decided by the pin's form (pins). It is named, with the command that
// removes it. Not "keep the previous image": that costs 4 GB forever for a
// rollback that may never happen, and an image no backup pins is not needed by any
// restore.
//
// Build cache is not touched. `box setup` builds with the daemon's own cache, and
// docker marks nothing in it as CuliBox's: `docker builder prune` removes every
// project's unused cache on that daemon.
//
// Every problem is a warning. The image was set up; failing setup over leftovers
// would turn a disk-space nicety into a broken install step.
func pruneOldBoxImages(ctx context.Context, d boximage.Docker, keep []string, orphan string, local bool, src pinSource, goos string, w io.Writer) {
	ctx, cancel := context.WithTimeout(ctx, pruneTimeout)
	defer cancel()
	pins, err := src.pins(ctx)
	if err != nil {
		fmt.Fprintf(w, "WARN image: old box images not removed: cannot read a config that could pin one (%v)\n", err)
		return
	}
	res, err := d.PruneOld(ctx, boximage.PruneInput{Keep: keep, Orphan: orphan, Pins: pins.live, RollbackPins: pins.rollback, Local: local})
	if err != nil {
		fmt.Fprintf(w, "WARN image: old box images not removed: %v\n", err)
		return
	}
	for _, im := range res.Removed {
		fmt.Fprintf(w, "removed old box image %s (%d bytes, %s)\n", im.Name(), im.Size, humanBytes(im.Size))
	}
	for _, f := range res.Failed {
		fmt.Fprintf(w, "WARN image: could not remove old box image %s: %v\n", f.Image.Name(), f.Err)
	}
	for _, im := range res.KeptForRollback {
		fmt.Fprintf(w, "kept %s: a saved upgrade backup's config pins it and it cannot be pulled again (a rollback that restores the backup needs it); %s\n", im.Name(), rollbackRemoval(im, goos))
	}
	if len(res.Removed) > 0 {
		fmt.Fprintf(w, "removed %d old box image(s), freeing up to %d bytes (%s; layers shared with a kept image stay)\n",
			len(res.Removed), res.FreedBytes(), humanBytes(res.FreedBytes()))
	}
	if n := len(res.KeptLocal); n > 0 {
		var total int64
		for _, im := range res.KeptLocal {
			total += im.Size
		}
		fmt.Fprintf(w, "kept %d unused local box image(s), %s in all. They cannot be pulled again, and another CuliBox on this Docker may pin one, so none is removed unless you ask:\n", n, humanBytes(total))
		for _, im := range res.KeptLocal {
			fmt.Fprintf(w, "  %s (%s): %s\n", im.Name(), humanBytes(im.Size), rmLine(im, goos))
		}
		fmt.Fprintln(w, "or run `culibox box setup -prune-local` to remove all of them")
	}
	if len(res.Removed) == 0 && len(res.Failed) == 0 && len(res.KeptLocal) == 0 && len(res.KeptForRollback) == 0 {
		fmt.Fprintln(w, "no old box images to remove")
	}
}

// rollbackRemoval ends the "kept for rollback" line: how to free the image later.
// -prune-local does not (a rollback image is always kept), and on macOS the plain
// docker CLI is not the box VM's.
func rollbackRemoval(im boximage.StoreImage, goos string) string {
	if goos == "darwin" {
		return "remove it from the box VM's Docker when you no longer need that rollback"
	}
	return "remove it with `docker image rm " + strings.Join(im.RmArgs(), " ") + "` when you no longer need that rollback"
}

// rmLine is the command that removes the image. On macOS the plain docker CLI is
// not the box VM's, so the line is the -prune-local one.
func rmLine(im boximage.StoreImage, goos string) string {
	if goos == "darwin" {
		return "`culibox box setup -prune-local`"
	}
	return "`docker image rm " + strings.Join(im.RmArgs(), " ") + "`"
}

// humanBytes is n in decimal units, as docker prints image sizes.
func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}
