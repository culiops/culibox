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
	"time"

	boximage "github.com/culiops/culibox/docker/agent-box"
	"github.com/culiops/culibox/internal/config"
)

// pruneTimeout bounds the whole prune (a few docker calls and the removals).
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
	rollback []string // box_image of configs inside retained upgrade backups
	repos    []string // the repositories of both, counted as box repositories
}

// pins reads every config an instance sharing this Docker store could be running
// from, as far as this process can find them:
//   - this instance's config files (-config, or the data root's and the ~/.config
//     ones), and its loaded config;
//   - the two default data roots' config.json and the two ~/.config files, even
//     when this instance runs from elsewhere (CULIBOX_HOME, -config): the usual
//     second instance is the default one;
//   - config.json inside each data root's retained upgrade backups, which the
//     documented rollback restores (docs/UPGRADING.md "Rolling back").
//
// NOT found: an instance whose config sits at another path (its own -config or
// CULIBOX_HOME) and that has no container, running or stopped. Nothing on disk
// or in Docker names such an instance, so its pin is invisible here; see
// PruneOld for what keeps its running boxes safe. A config that exists but
// cannot be read or parsed is an error, not an empty pin.
func (s pinSource) pins() (imagePins, error) {
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
		add(&p.rollback, ref)
	}
	for _, ref := range append(append([]string(nil), p.live...), p.rollback...) {
		if r := boximage.RepoOf(ref); r != "" && !containsRef(p.repos, r) {
			p.repos = append(p.repos, r)
		}
	}
	return p, nil
}

// boxImageOf is the box_image a config file names ("" for none, or no file).
func boxImageOf(path string) (string, error) {
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
func boxSetupAndPrune(ctx context.Context, d boximage.Docker, image string, a cliArgs, src pinSource, now time.Time, goos string, w io.Writer) int {
	rc := boxSetupImage(ctx, d, image, a.Rebuild, now, goos, w)
	if rc != 0 || a.NoPrune {
		return rc
	}
	pruneOldBoxImages(ctx, d, image, src, w)
	return rc
}

// pruneOldBoxImages removes the box images no container uses and no visible
// config pins, after `box setup` made image current (#6). Why it exists: every
// upgrade left the previous ~4 GB image in the store for good (one upgrade took a
// 49 GB data root from 77% to 99% full). What it keeps, and why, is in
// boximage.Docker.PruneOld; the choices that belong here:
//
// Rollback (#229). A binary-only rollback (<bin>.prev) reads the same config, so
// it runs the image just set up, which is never removed. The documented full
// rollback also restores <data root>/backups/*/config.json, and that config can
// pin an older image. A pin to a registry digest can be pulled again by `box
// setup`. A pin to a locally built dated tag cannot: the rolled-back binary would
// find "pinned box image is missing" and could only build a NEW tag. So an image
// pinned by a retained backup's config is kept when it has no registry digest,
// and let go when it has one. That is the whole rule: not "keep the previous
// image" (it costs 4 GB forever for a rollback that may never happen, and an image
// no backup pins is not needed by any restore). The kept image is named, with the
// command that removes it.
//
// Build cache is not touched. `box setup` builds with the daemon's own cache, and
// docker marks nothing in it as CuliBox's: `docker builder prune` removes every
// project's unused cache on that daemon.
//
// Every problem is a warning. The image was set up; failing setup over leftovers
// would turn a disk-space nicety into a broken install step.
func pruneOldBoxImages(ctx context.Context, d boximage.Docker, image string, src pinSource, w io.Writer) {
	pins, err := src.pins()
	if err != nil {
		fmt.Fprintf(w, "WARN image: old box images not removed: cannot read a config that could pin one (%v)\n", err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, pruneTimeout)
	defer cancel()
	res, err := d.PruneOld(ctx, boximage.PruneInput{Keep: image, Pins: pins.live, RollbackPins: pins.rollback, Repos: pins.repos})
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
		fmt.Fprintf(w, "kept %s: a saved upgrade backup's config pins it and it cannot be pulled again (a rollback that restores the backup needs it); remove it with `docker image rm %s` when you no longer need that rollback\n", im.Name(), firstRef(im))
	}
	switch {
	case len(res.Removed) > 0:
		fmt.Fprintf(w, "removed %d old box image(s), freeing up to %d bytes (%s; layers shared with a kept image stay)\n",
			len(res.Removed), res.FreedBytes(), humanBytes(res.FreedBytes()))
		fmt.Fprintln(w, "kept: images a container uses and images pinned by a config this CuliBox can see; an instance with its config elsewhere and no box is not seen (use -no-prune if one shares this Docker)")
	case len(res.Failed) == 0 && len(res.KeptForRollback) == 0:
		fmt.Fprintln(w, "no old box images to remove")
	}
}

func firstRef(im boximage.StoreImage) string {
	if len(im.Refs) > 0 {
		return im.Refs[0]
	}
	return im.ID
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
