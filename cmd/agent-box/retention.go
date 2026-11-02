package main

import (
	"context"
	"strconv"
	"time"

	"github.com/culiops/culibox/internal/egress"
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
		n, err := a.Prune(ctx, cutoff, retentionBatch)
		if err != nil && ctx.Err() != nil {
			return // shutdown; the rest is pruned on the next start
		}
		if err != nil {
			logf("audit: prune of egress rows older than %s failed after %d deleted: %v", retentionDays(days), n, err)
			return
		}
		if n > 0 {
			logf("audit: pruned %d egress rows older than %s", n, retentionDays(days))
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

// retentionDays is the window in words for the log: "1 day", "90 days".
func retentionDays(days int) string {
	if days == 1 {
		return "1 day"
	}
	return strconv.Itoa(days) + " days"
}

// retentionInEffect is the egress retention the job will really apply, for the
// Audit footer: 0 (kept forever) when there is no egress audit store to prune,
// since then the job never starts whatever the config says (#24).
func retentionInEffect(a *egress.Audit, days int) int {
	if a == nil || days <= 0 {
		return 0
	}
	return days
}

// runRetention starts startRetention in the background and returns the step
// that cancels it and waits (bounded) for it to finish. Nil audit or days == 0
// starts nothing and returns a no-op.
func runRetention(a *egress.Audit, days int, logf func(string, ...any)) (stop func()) {
	if retentionInEffect(a, days) == 0 {
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
