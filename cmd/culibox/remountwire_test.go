package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	boximage "github.com/culiops/culibox/docker/culibox"
	"github.com/culiops/culibox/internal/box"
)

// modeFlippedGuest is the guest's /proc/self/mounts after a folder-mode switch
// the VM has not picked up yet: the first mount is at the other mode. fixed is
// what it lists once that mount is remounted.
func modeFlippedGuest(l *boxLayer) (flipped, fixed string) {
	var fl, fx strings.Builder
	for i, m := range l.vm.Config().Mounts() {
		mode := "ro"
		if m.Writable {
			mode = "rw"
		}
		other := map[string]string{"ro": "rw", "rw": "ro"}[mode]
		fmt.Fprintf(&fx, "mount%d %s virtiofs %s,relatime 0 0\n", i, m.Location, mode)
		if i == 0 {
			mode = other
		}
		fmt.Fprintf(&fl, "mount%d %s virtiofs %s,relatime 0 0\n", i, m.Location, mode)
	}
	return fl.String(), fx.String()
}

// #236: a spawn whose prepare STARTED the VM remounts a mode-only mismatch and
// proceeds.
func TestPrepareAfterStartRemountsAModeOnlyMismatch(t *testing.T) {
	l, fc, d := darwinLayer(t, false)
	d.set(t, "image", "arm64|"+boximage.Hash()+"\n")
	fc.mounts, fc.fixedMounts = modeFlippedGuest(l)
	if err := l.prepare(context.Background()); err != nil {
		t.Fatalf("prepare after a start must fix a mode-only mismatch: %v", err)
	}
	if !fc.called("start") || fc.remounts != 1 {
		t.Fatalf("started %v, remounts %d", fc.called("start"), fc.remounts)
	}
	if !l.foldersVerified() {
		t.Fatal("the fixed check must be remembered")
	}
}

// A spawn that found the VM already running only reads: it never changes the
// guest, and a mismatch is a refusal.
func TestPrepareOnARunningVMNeverRemounts(t *testing.T) {
	l, fc, d := darwinLayer(t, false)
	d.set(t, "image", "arm64|"+boximage.Hash()+"\n")
	fc.running = true
	fc.mounts, fc.fixedMounts = modeFlippedGuest(l)
	err := l.prepare(context.Background())
	if err == nil || !strings.Contains(err.Error(), "folders do not match") || !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("a mismatch found on a running VM must refuse: %v", err)
	}
	if fc.remounts != 0 {
		t.Fatalf("remounted %d times on a VM this spawn did not start", fc.remounts)
	}
}

// The post-apply check follows an apply that started the VM: it remounts.
func TestCheckAfterApplyRemountsAModeOnlyMismatch(t *testing.T) {
	l, fc, _ := applyLayer(t)
	fc.mounts, fc.fixedMounts = modeFlippedGuest(l)
	if err := l.checkAfterApply(context.Background()); err != nil {
		t.Fatalf("checkAfterApply: %v", err)
	}
	if fc.remounts != 1 {
		t.Fatalf("remounts %d, want 1", fc.remounts)
	}
}
