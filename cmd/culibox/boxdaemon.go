package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/culiops/culibox/internal/box"
)

// daemonTTL is how long /api/meta trusts the last answer before it asks the
// daemon again (in the background). A boxed spawn never uses it: that asks
// afresh.
const daemonTTL = 15 * time.Second

// daemonWatch remembers whether the local docker daemon can be used (#154).
// The box layer exists whenever the docker CLI does, so "the daemon is stopped
// or not permitted" is a state the layer is in, not a reason it failed to
// build: it can change while CuliBox runs, and a verdict taken once at boot
// would stay wrong after the user starts Docker (or joins the docker group).
//
// Two readers. Check asks the daemon now (bounded) and stores the answer: boot
// and every boxed spawn. Reason serves the stored answer to /api/meta and, once
// it is older than ttl, starts one background Check behind it, so a page load
// never waits on docker and a hung daemon holds no more than one probe.
type daemonWatch struct {
	ping func(context.Context) error
	ttl  time.Duration
	now  func() time.Time // a test replaces it

	mu   sync.Mutex
	err  error
	at   time.Time
	busy bool
}

func newDaemonWatch(ping func(context.Context) error, ttl time.Duration) *daemonWatch {
	return &daemonWatch{ping: ping, ttl: ttl, now: time.Now}
}

// Check probes now, bounded by box.ProbeTimeout, remembers the answer and
// returns it (nil: the daemon answers; see box.Ping for the classes).
func (w *daemonWatch) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, box.ProbeTimeout)
	defer cancel()
	err := w.ping(ctx)
	w.mu.Lock()
	w.err, w.at = err, w.now()
	w.mu.Unlock()
	return err
}

// Reason is the stored answer as the plain sentence the page shows ("" when
// the daemon answered or was never asked). It never blocks on docker.
func (w *daemonWatch) Reason() string {
	w.mu.Lock()
	err := w.err
	stale := w.at.IsZero() || w.now().Sub(w.at) >= w.ttl
	start := stale && !w.busy
	if start {
		w.busy = true
	}
	w.mu.Unlock()
	if start {
		go func() {
			w.Check(context.Background())
			w.mu.Lock()
			w.busy = false
			w.mu.Unlock()
		}()
	}
	return box.DaemonReason(err)
}

// daemonReason asks the daemon now and returns the plain sentence ("" when it
// answers). "" too when the layer has no local daemon to ask (macOS: the box VM
// starts on the first boxed spawn, and prepare reports what stops it).
func (l *boxLayer) daemonReason(ctx context.Context) string {
	if l == nil || l.daemon == nil {
		return ""
	}
	return box.DaemonReason(l.daemon.Check(ctx))
}

// layerDaemonReason is what /api/meta publishes as box_daemon_reason: why the
// box layer cannot be used right now, from the last answer. nil when there is
// no layer or no local daemon to ask (macOS), so the field is never published.
func layerDaemonReason(l *boxLayer) func() string {
	if l == nil || l.daemon == nil {
		return nil
	}
	return l.daemon.Reason
}

// refuseUnusableDaemon is the boxed spawn's first check on Linux: a daemon
// that is stopped, or that this user may not use, is refused with the reason
// as ErrBoxUnavailable (shown to the user as is) instead of failing later as
// "could not start session". Any other probe failure passes: the spawn's own
// docker calls report what is wrong, and a refusal here would be a guess.
func (l *boxLayer) refuseUnusableDaemon(ctx context.Context) error {
	err := l.daemon.Check(ctx)
	if errors.Is(err, box.ErrDockerNotRunning) || errors.Is(err, box.ErrDockerDenied) {
		return fmt.Errorf("%w: %s", box.ErrBoxUnavailable, box.DaemonReason(err))
	}
	return nil
}
