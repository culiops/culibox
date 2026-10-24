package main

import (
	"context"
	"log"
	"path/filepath"
	"time"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/boxedws"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/session"
	"github.com/culiops/agent-box/internal/web"
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
// hands over spec.Cwd raw: "" (the daemon's own cwd would be meant, which is not
// a workspace to record) is ignored, and a relative path is made absolute first,
// because the registry resolves against the daemon's cwd only by accident.
func boxedRecorder(reg *boxedws.Registry) func(cwd string) error {
	return func(cwd string) error {
		if cwd == "" {
			return nil
		}
		abs, err := filepath.Abs(cwd)
		if err != nil {
			return err
		}
		return reg.Add(abs)
	}
}

// seedBoxedRegistry writes the first-boot record. Call it only after sessions
// were restored into the Manager (Reconcile): the retained boxed sessions are
// part of the seed. A docker listing failure only drops that source.
func seedBoxedRegistry(reg *boxedws.Registry, mgr *session.Manager, clones []config.Clone,
	profileCwds []string, cli *box.CLIClient) {
	var boxes []box.BoxInfo
	if cli != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		bs, err := cli.ListBoxes(ctx, box.LabelInstance)
		if err != nil {
			log.Printf("boxed workspaces: listing existing boxes failed (%v); seeding without them", err)
		}
		boxes = bs
	}
	dirs := seedDirs(mgr.List(), clones, profileCwds, boxes)
	if err := reg.Seed(dirs); err != nil {
		log.Fatalf("boxed workspaces: seeding boxed-workspaces.json: %v", err)
	}
	log.Printf("boxed workspaces: seeded %d director(ies) on first start", len(reg.List()))
}
