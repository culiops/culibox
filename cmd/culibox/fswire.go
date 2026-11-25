package main

import (
	"context"
	"log"
	"path/filepath"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/boxedws"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/session"
	"github.com/culiops/culibox/internal/web"
)

// boxTreesFunc builds the fs API's per-request view of the host trees a box can
// write. Denied is fixed (the data dir and the box state root). Contained is
// recomputed on every call, so a boxed workspace spawned a moment ago is covered
// at once: the durable record, plus the cwd of every boxed session (boxedCwds does no
// I/O: this runs on every fs request) and the directory of every clone
// profile. Host sessions are not contained (a host
// agent's tree is the operator's own). Duplicates are dropped; the web policy
// resolves and canonicalises each entry itself.
func boxTreesFunc(denied []string, reg *boxedws.Registry,
	boxedCwds func() []string, clones func() []config.Clone) func() web.BoxTrees {
	return func() web.BoxTrees {
		seen := map[string]bool{}
		var contained []string
		add := func(dir string) {
			if dir == "" || seen[dir] {
				return
			}
			seen[dir] = true
			contained = append(contained, dir)
		}
		for _, d := range reg.List() {
			add(d)
		}
		for _, d := range boxedCwds() {
			add(d)
		}
		for _, c := range clones() {
			add(c.Dir)
		}
		return web.BoxTrees{Denied: append([]string(nil), denied...), Contained: contained}
	}
}

// seedDirs lists every directory that may already have been boxed when the
// durable record does not exist yet (first boot of this build): retained boxed
// sessions, clone directories, the default_cwd of every profile that spawns
// boxed, and the workspace label of every box container docker still has (all
// instances). A box row whose labels did not parse, or with no workspace, says
// nothing trustworthy and is skipped. Order is first-seen; duplicates dropped.
func seedDirs(sessions []session.Info, clones []config.Clone, profileCwds []string, boxes []box.BoxInfo) []string {
	seen := map[string]bool{}
	var out []string
	add := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		out = append(out, dir)
	}
	for _, s := range sessions {
		if s.Runtime == config.RuntimeBox {
			add(s.Cwd)
		}
	}
	for _, c := range clones {
		add(c.Dir)
	}
	for _, d := range profileCwds {
		add(d)
	}
	for _, b := range boxes {
		if !b.Unreadable {
			add(b.Workspace)
		}
	}
	return out
}

// boxedRecorder adapts the registry to session.Manager.SetBoxedRecorder. Spawn
// hands over the absolute cwd its checks resolved, or "" (the daemon's own cwd
// would be meant, which is not a workspace to record), which is ignored. A
// relative path is still made absolute, as a backstop. A cwd that is a clone
// profile's directory is a clone spawn and is recorded as before; any other is
// an in-place spawn, and a git work tree never boxes in place (#371), so the
// folder is recorded as boxed in place without git (RecordInPlace false): a
// .git a box writes there later must never become a clone source. clones is
// read on every call (a profile saved after boot counts); nil means none.
func boxedRecorder(reg *boxedws.Registry, clones func() []config.Clone) func(cwd string) error {
	return func(cwd string) error {
		if cwd == "" {
			return nil
		}
		abs, err := filepath.Abs(cwd)
		if err != nil {
			return err
		}
		if clones != nil {
			key := cloneKey(abs)
			for _, c := range clones() {
				if c.Dir != "" && cloneKey(c.Dir) == key {
					return reg.Add(abs)
				}
			}
		}
		return reg.RecordInPlace(abs, false)
	}
}

// recordBoxedProfiles records, on every boot, the directory of every clone
// profile and the default_cwd of every profile that spawns boxed. Call it only
// after the profile manager pushed profiles.json into the launcher: before
// that the launcher holds only the config seeds. Running it every boot, not
// only with the first-boot seed, also repairs a registry that a build seeding
// before the profiles loaded marked seeded without them. Add is a no-op for a
// recorded directory and logs the ones it skips.
func recordBoxedProfiles(reg *boxedws.Registry, clones []config.Clone, profileCwds []string) {
	for _, d := range seedDirs(nil, clones, profileCwds, nil) {
		if err := reg.Add(d); err != nil {
			log.Fatalf("boxed workspaces: recording %s: %v", d, err)
		}
	}
}

// seedBoxedRegistry writes the first-boot record. Call it only after sessions
// were restored into the Manager (Reconcile) and pass that Manager's list: the
// retained boxed sessions are part of the seed. listBoxes is the docker
// listing, nil when there is no containment layer (then there is nothing to
// list and the seed is complete). The seed is marked complete only when every
// source was read: a failed docker listing (e.g. the darwin VM is stopped)
// still records what the other sources gave but leaves the registry unseeded,
// so the next boot retries instead of losing the docker source for good.
func seedBoxedRegistry(reg *boxedws.Registry, sessions []session.Info, clones []config.Clone,
	profileCwds []string, listBoxes func(context.Context) ([]box.BoxInfo, error)) {
	var boxes []box.BoxInfo
	complete := true
	if listBoxes != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		bs, err := listBoxes(ctx)
		if err != nil {
			complete = false
			log.Printf("boxed workspaces: listing existing boxes failed (%v); seeding without them, will retry next start", err)
		}
		boxes = bs
	}
	dirs := seedDirs(sessions, clones, profileCwds, boxes)
	if err := reg.Seed(dirs, complete); err != nil {
		log.Fatalf("boxed workspaces: seeding boxed-workspaces.json: %v", err)
	}
	log.Printf("boxed workspaces: seeded=%v, %d director(ies) recorded", complete, len(reg.List()))
}
