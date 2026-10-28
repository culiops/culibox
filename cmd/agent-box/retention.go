package main

import (
	"context"
	"time"

	"github.com/culiops/agent-box/internal/egress"
)

// retentionEvery is how often the daemon prunes; the first run is at start.
const retentionEvery = 24 * time.Hour

// retentionStopWait bounds how long shutdown waits for a prune in flight, so
// the audit store is not closed under it.
const retentionStopWait = 2 * time.Second

// retentionBatch is how many rows one Prune statement deletes.
const retentionBatch = 1000

// startRetention prunes egress audit rows older than days: once at start, then
// on every tick of every. It blocks until ctx is cancelled, so callers run it
// in a goroutine, and returns at once when days is 0 (keep forever) or a is
// nil (or every is not positive). Only egress rows are pruned. A run that deletes nothing logs nothing.
func startRetention(ctx context.Context, a *egress.Audit, days int, every time.Duration, logf func(string, ...any)) {
	if a == nil || days <= 0 || every <= 0 {
		return
	}
	run := func() {
		cutoff := time.Now().AddDate(0, 0, -days)
		n, err := a.Prune(cutoff, retentionBatch)
		if err != nil {
			logf("audit: prune of egress rows older than %d days failed after %d deleted: %v", days, n, err)
			return
		}
		if n > 0 {
			logf("audit: pruned %d egress rows older than %d days", n, days)
		}
	}
	run()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

// runRetention starts startRetention in the background and returns the step
// that cancels it and waits (bounded) for it to finish. Nil audit or days == 0
// starts nothing and returns a no-op.
func runRetention(a *egress.Audit, days int, logf func(string, ...any)) (stop func()) {
	if a == nil || days <= 0 {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		startRetention(ctx, a, days, retentionEvery, logf)
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(retentionStopWait):
		}
	}
}
