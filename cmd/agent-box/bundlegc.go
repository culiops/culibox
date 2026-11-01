package main

import (
	"context"
	"log"
	"time"

	"github.com/culiops/agent-box/internal/bundle"
)

// bundleGCEvery is how often the daemon looks for old config bundles; the first
// pass is at start.
const bundleGCEvery = 24 * time.Hour

// runBundleGC runs pass in the background, once at start and then on every tick
// of every, and returns the step that cancels it and waits (bounded) for it to
// finish. A pass stopped half way loses nothing: see bundle.Manager.GC.
func runBundleGC(pass func(context.Context), every time.Duration) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		pass(ctx)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pass(ctx)
			}
		}
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(retentionStopWait):
		}
	}
}

// startBundleGC starts the GC where it is known to be safe. It matches a dir to a
// container by the mount source docker reports. That spelling is verified on Linux
// only: on the colima VM a different one would read as "not mounted" and remove a
// dir a box is using. So no pass runs there, whichever other OS appears either.
func (l *boxLayer) startBundleGC(pass func(context.Context), logf func(string, ...any)) (stop func()) {
	if l.goos != "linux" {
		logf("bundle gc: off on %s until its docker mount path spelling is verified; old bundles and plugin snapshots stay", l.goos)
		return func() {}
	}
	return runBundleGC(pass, bundleGCEvery)
}

// bundleGCPass is one bounded pass.
func (l *boxLayer) bundleGCPass(ctx context.Context, bundles *bundle.Manager) {
	ctx, cancel := context.WithTimeout(ctx, vmStartPassTimeout)
	defer cancel()
	rep, err := bundles.GC(ctx, l.cli)
	logBundleGC(rep, err, log.Printf)
}

// logBundleGC reports a pass. One that removed nothing and failed nothing is
// silent, which is every day but a few.
func logBundleGC(rep bundle.GCReport, err error, logf func(string, ...any)) {
	for _, r := range rep.Removed {
		logf("bundle gc: removed %s: unused for %d days, %d MiB freed", r.Name, int(r.Age.Hours()/24), r.Bytes/(1024*1024))
	}
	if rep.Swept > 0 {
		logf("bundle gc: removed %d leftover(s) of an earlier pass", rep.Swept)
	}
	if rep.Held > 0 {
		logf("bundle gc: %d condemned dir(s) kept because a container names them", rep.Held)
	}
	if err != nil {
		logf("bundle gc: %v", err)
	}
}
