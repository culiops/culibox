//go:build uidemo

package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/culiops/culibox/internal/boxvm"
	"github.com/culiops/culibox/internal/vmshares"
	"github.com/culiops/culibox/internal/web"
)

// A UI demo for Linux: the real folder service over a fake home and a fake VM,
// so Settings can be checked in a browser. Never part of a release build.
//
//	AB_UIDEMO_VMFOLDERS = applied | whole_home | pending
//	AB_UIDEMO_HOME      = the fake home, an empty scratch folder (required; its data dir is <home>-data)
//	AB_UIDEMO_APPLY     = fail → the start step fails after 5 s

func init() { uidemoVMFolders = uidemoBuild }

type demoVMControl struct {
	mu      sync.Mutex
	applied []vmshares.Share
	whole   bool
	listed  []vmshares.Share
	busy    bool
	fail    bool
}

func (d *demoVMControl) Applied() ([]vmshares.Share, bool, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]vmshares.Share{}, d.applied...), d.whole, true, nil
}

func (d *demoVMControl) SetListed(l []vmshares.Share) {
	d.mu.Lock()
	d.listed = append([]vmshares.Share{}, l...)
	d.mu.Unlock()
}

func (d *demoVMControl) RunningBoxed() []string { return []string{"culibox", "infra-audit"} }
func (d *demoVMControl) RunningCwds() []string  { return nil }
func (d *demoVMControl) VMRunning() bool        { return false }
func (d *demoVMControl) Applying() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.busy
}

func (d *demoVMControl) Apply(ctx context.Context, step func(name, state, sub string)) error {
	d.mu.Lock()
	d.busy = true
	d.mu.Unlock()
	defer func() { d.mu.Lock(); d.busy = false; d.mu.Unlock() }()
	step("stop", "running", "")
	time.Sleep(2 * time.Second)
	step("stop", "done", "")
	step("start", "running", "")
	if d.fail {
		time.Sleep(5 * time.Second)
		step("start", "failed", "")
		return boxvm.ErrStartTimeout
	}
	time.Sleep(10 * time.Second)
	step("start", "done", "")
	step("check", "running", "")
	time.Sleep(2 * time.Second)
	step("check", "done", "")
	d.mu.Lock()
	d.applied, d.whole = append([]vmshares.Share{}, d.listed...), false
	d.mu.Unlock()
	return nil
}

func uidemoBuild(home string) web.VMFolders {
	mode := os.Getenv("AB_UIDEMO_VMFOLDERS")
	switch mode {
	case "applied", "whole_home", "pending":
	default:
		return nil
	}
	home = os.Getenv("AB_UIDEMO_HOME") // never the real home: the demo creates folders in it
	if home == "" {
		log.Print("uidemo: set AB_UIDEMO_HOME to an empty scratch folder")
		return nil
	}
	for _, d := range []string{"code/culibox", "code/infra", "box-clones", "notes/runbooks", "code/newproj"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			log.Printf("uidemo: %v", err)
			return nil
		}
	}
	data := filepath.Clean(home) + "-data" // beside the fake home, so it is not inside a share
	root := filepath.Join(data, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		log.Printf("uidemo: %v", err)
		return nil
	}
	h := func(p string) string { return filepath.Join(home, p) }
	profiles := []vmshares.Profile{
		{Name: "culibox", Workspace: h("code/culibox")},
		{Name: "culibox-clone", Workspace: h("box-clones"), Clone: true, CloneOf: "culibox"},
		{Name: "ops", Workspace: h("notes/runbooks")},
	}
	ctl := &demoVMControl{fail: os.Getenv("AB_UIDEMO_APPLY") == "fail"}
	svc, err := newVMShareSvc(data, vmshares.Env{Home: home, DataRoot: root},
		ctl, func() []vmshares.Profile { return profiles },
		func() []string { return []string{h("code/infra")} }, func() []string { return nil }, log.Printf)
	if err != nil {
		log.Printf("uidemo: %v", err)
		return nil
	}
	listed := svc.Listed()
	switch mode {
	case "applied":
		ctl.applied = listed
	case "whole_home":
		ctl.whole = true
	case "pending":
		// The VM lacks the first folder, has the second with the other mode, and
		// still has a folder the list no longer holds.
		if len(listed) > 1 {
			sw := listed[1]
			if sw.Mode == vmshares.RW {
				sw.Mode = vmshares.RO
			} else {
				sw.Mode = vmshares.RW
			}
			ctl.applied = append([]vmshares.Share{sw}, listed[2:]...)
		}
		ctl.applied = append(ctl.applied, vmshares.Share{Path: h("old/gone"), Mode: vmshares.RW})
	}
	log.Printf("uidemo: VM folders demo %q, fake home %s", mode, home)
	return svc
}
