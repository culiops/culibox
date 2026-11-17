package box

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// errDaemonDown stands for a docker that cannot be asked at boot: stopped,
// starting, or slow enough to hit the probe's deadline.
var errDaemonDown = fmt.Errorf("%w: docker info: exit status 1", ErrDockerNotRunning)

// newRuntimeManager builds a Manager on f as it is set up now, so a test can
// choose what the boot probe sees.
func newRuntimeManager(t *testing.T, f *fakeClient) *Manager {
	t.Helper()
	m, err := New(Options{DockerBin: "/usr/bin/docker", Image: "img", Instance: "TEST", IdleTTL: time.Hour, StateRoot: t.TempDir(), Client: f})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// lastRunArgs is the argv of the fake's last `docker run`.
func (f *fakeClient) lastRunArgs() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.runArgs) == 0 {
		return ""
	}
	return strings.Join(f.runArgs[len(f.runArgs)-1], " ")
}

func spawnIn(t *testing.T, m *Manager) error {
	t.Helper()
	_, _, _, err := m.Wrap(context.Background(), t.TempDir(), "s-1", BoxKey{}, []string{"claude"}, nil, nil)
	return err
}

// #334: Docker down (or slow) when CuliBox starts is a supported flow since
// #154. The runtime the boot probe could not read is read again before the
// next box is created, so a box never runs on runc only because Docker was
// started after CuliBox.
func TestRuntimeIsDetectedAgainOnceTheDaemonAnswers(t *testing.T) {
	f := newFake()
	f.setDaemon(true, errDaemonDown)
	m := newRuntimeManager(t, f)
	if rt, known := m.RuntimeKnown(); known || rt != runtimeRunc {
		t.Fatalf("boot probe failed: RuntimeKnown() = %q, %v; want runc, not known", rt, known)
	}
	f.setDaemon(true, nil) // the user starts Docker
	if err := spawnIn(t, m); err != nil {
		t.Fatal(err)
	}
	if args := f.lastRunArgs(); !strings.Contains(args, "--runtime="+runtimeSysbox) {
		t.Fatalf("the box was created without sysbox after Docker came up: %s", args)
	}
	if rt, known := m.RuntimeKnown(); !known || rt != runtimeSysbox {
		t.Fatalf("RuntimeKnown() = %q, %v; want sysbox-runc, known", rt, known)
	}
	if m.Runtime() != runtimeSysbox {
		t.Fatalf("Runtime() (the badge) = %q, want the re-detected sysbox-runc", m.Runtime())
	}
	// Settled: the next box does not ask again.
	n := f.runtimeProbes()
	if err := spawnIn(t, m); err != nil {
		t.Fatal(err)
	}
	if f.runtimeProbes() != n {
		t.Fatalf("a settled runtime was probed again (%d -> %d)", n, f.runtimeProbes())
	}
}

// A daemon that came up without sysbox settles on runc, as definitively as a
// boot that saw it.
func TestRuntimeDetectedAgainCanSettleOnRunc(t *testing.T) {
	f := newFake()
	f.setDaemon(false, errDaemonDown)
	m := newRuntimeManager(t, f)
	f.setDaemon(false, nil)
	if err := spawnIn(t, m); err != nil {
		t.Fatal(err)
	}
	if args := f.lastRunArgs(); strings.Contains(args, "--runtime") {
		t.Fatalf("no sysbox, yet: %s", args)
	}
	if rt, known := m.RuntimeKnown(); !known || rt != runtimeRunc {
		t.Fatalf("RuntimeKnown() = %q, %v; want runc, known", rt, known)
	}
}

// A boot probe that got a definitive answer is not asked again: no probe cost
// on any spawn. A definitive "no sysbox" stays runc for the process (installing
// sysbox restarts Docker; CuliBox picks it up when it restarts).
func TestDefinitiveBootAnswerIsNotProbedAgain(t *testing.T) {
	for _, sysbox := range []bool{false, true} {
		f := newFake()
		f.setDaemon(sysbox, nil)
		m := newRuntimeManager(t, f)
		want := runtimeRunc
		if sysbox {
			want = runtimeSysbox
		}
		if rt, known := m.RuntimeKnown(); !known || rt != want {
			t.Fatalf("sysbox=%v: RuntimeKnown() = %q, %v; want %q, known", sysbox, rt, known, want)
		}
		f.setDaemon(!sysbox, nil) // what a re-probe would see
		for i := 0; i < 3; i++ {
			if err := spawnIn(t, m); err != nil {
				t.Fatal(err)
			}
		}
		if got := m.RuntimeFresh(time.Second); got != want {
			t.Fatalf("sysbox=%v: RuntimeFresh = %q, want the boot answer %q", sysbox, got, want)
		}
		if n := f.runtimeProbes(); n != 1 {
			t.Fatalf("sysbox=%v: %d runtime probes, want the boot one only", sysbox, n)
		}
		if strings.Contains(f.lastRunArgs(), "--runtime") != sysbox {
			t.Fatalf("sysbox=%v: last run %s", sysbox, f.lastRunArgs())
		}
	}
}

// While the runtime cannot be read, no box is created: one created then would
// run on runc only because the probe failed, the silent downgrade of #334.
func TestSpawnIsRefusedWhileTheRuntimeCannotBeRead(t *testing.T) {
	f := newFake()
	f.setDaemon(true, errDaemonDown)
	m := newRuntimeManager(t, f)
	err := spawnIn(t, m)
	if !errors.Is(err, ErrBoxUnavailable) || !strings.Contains(err.Error(), "isolation runtime") {
		t.Fatalf("spawn with the runtime unknown: %v, want ErrBoxUnavailable naming the runtime", err)
	}
	if f.runs() != 0 {
		t.Fatalf("a box was created with the runtime unknown (%d runs)", f.runs())
	}
	if _, known := m.RuntimeKnown(); known {
		t.Fatal("a failed probe settled the runtime")
	}
	f.setDaemon(true, nil)
	if err := spawnIn(t, m); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.lastRunArgs(), "--runtime="+runtimeSysbox) {
		t.Fatalf("after the daemon answered: %s", f.lastRunArgs())
	}
}

// A daemon that accepts and never answers holds a spawn no longer than the
// probe's bound.
func TestRuntimeProbeAtSpawnIsBounded(t *testing.T) {
	old := runtimeProbeTimeout
	runtimeProbeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { runtimeProbeTimeout = old })
	f := newFake()
	f.runtimeHook = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	m := newRuntimeManager(t, f)
	start := time.Now()
	err := spawnIn(t, m)
	if !errors.Is(err, ErrBoxUnavailable) {
		t.Fatalf("hung daemon: %v, want ErrBoxUnavailable", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("the spawn waited %s on a hung probe", d)
	}
}

// The badge reader (RuntimeFresh, behind /api/meta) re-detects too, so a page
// reloaded after Docker starts shows the runtime the next box will get. Many
// readers and a spawn at once share one probe, and the reader's background
// probe ends with its bound (no leaked goroutine).
func TestRuntimeFreshSharesOneBoundedProbe(t *testing.T) {
	old := runtimeProbeTimeout
	runtimeProbeTimeout = 2 * time.Second
	t.Cleanup(func() { runtimeProbeTimeout = old })
	f := newFake()
	f.setDaemon(true, errDaemonDown)
	m := newRuntimeManager(t, f)
	boot := f.runtimeProbes()

	entered := make(chan struct{}, 16)
	release := make(chan struct{})
	f.mu.Lock()
	f.runtimeHook = func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Unlock()
	f.setDaemon(true, nil)

	if got := m.RuntimeFresh(0); got != runtimeRunc {
		t.Fatalf("RuntimeFresh while the probe runs = %q, want the placeholder runc", got)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("RuntimeFresh started no probe while the runtime is unknown")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); m.RuntimeFresh(10 * time.Millisecond) }()
	}
	spawned := make(chan error, 1)
	go func() { spawned <- spawnIn(t, m) }()
	wg.Wait()
	close(release)
	if err := <-spawned; err != nil {
		t.Fatal(err)
	}
	if n := f.runtimeProbes() - boot; n != 1 {
		t.Fatalf("%d probes for 8 readers and a spawn, want one shared", n)
	}
	if !strings.Contains(f.lastRunArgs(), "--runtime="+runtimeSysbox) {
		t.Fatalf("spawn after the shared probe: %s", f.lastRunArgs())
	}
	if got := m.RuntimeFresh(0); got != runtimeSysbox {
		t.Fatalf("RuntimeFresh = %q, want sysbox-runc", got)
	}
	waitFor(t, 2*time.Second, func() bool { return !m.runtimeProbing() })
}

// A reader does not ask a daemon that keeps failing more than once per
// runtimeRetry, and waits no longer than it was told to.
func TestRuntimeFreshDoesNotHammerADownDaemon(t *testing.T) {
	f := newFake()
	f.setDaemon(true, errDaemonDown)
	m := newRuntimeManager(t, f)
	boot := f.runtimeProbes()
	for i := 0; i < 20; i++ {
		if got := m.RuntimeFresh(50 * time.Millisecond); got != runtimeRunc {
			t.Fatalf("down daemon: RuntimeFresh = %q", got)
		}
	}
	waitFor(t, 2*time.Second, func() bool { return !m.runtimeProbing() })
	if n := f.runtimeProbes() - boot; n > 1 {
		t.Fatalf("%d probes from 20 quick reads of a down daemon, want at most 1", n)
	}
}
