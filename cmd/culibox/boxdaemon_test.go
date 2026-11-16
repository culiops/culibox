package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
)

// daemonDocker writes a docker whose `info` prints stderr and exits code; every
// other subcommand answers nothing, successfully.
func daemonDocker(t *testing.T, stderr string, code int) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "docker")
	script := fmt.Sprintf("#!/bin/sh\nfor a in \"$@\"; do if [ \"$a\" = info ]; then printf '%%s\\n' '%s' >&2; exit %d; fi; done\nexit 0\n", stderr, code)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

const (
	stoppedText = "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?"
	deniedText  = "permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock: dial unix /var/run/docker.sock: connect: permission denied"
)

// #154: the box layer builds when docker is installed whatever the daemon is
// doing (it may start later), so the daemon's state is asked live: at boot, on
// every boxed spawn, and on a short cache for /api/meta.
func TestLinuxLayerSaysWhyTheDaemonCannotBeUsed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stderr string
		code   int
		reason string // "" = healthy
		refuse bool   // a boxed spawn is refused
	}{
		{"healthy", "", 0, "", false},
		{"stopped", stoppedText, 1, "Docker is not running", true},
		{"not permitted", deniedText, 1, "no permission to use Docker", true},
		// Neither class: shown with the doctor pointer, but not a refusal of its own: the
		// spawn's later docker calls say what failed, and a refusal here would be a guess.
		{"other", "error getting credentials - err: exit status 1", 1, "run `culibox doctor` for details", false},
	} {
		cfg := config.Config{DockerBin: daemonDocker(t, tc.stderr, tc.code), BoxImage: "me/img:1"}
		l, err := newBoxLayer(cfg, t.TempDir(), t.TempDir(), "linux", nil, nil)
		if err != nil {
			t.Fatalf("%s: the layer must build whatever the daemon is doing: %v", tc.name, err)
		}
		if got := l.daemonReason(context.Background()); got != tc.reason {
			t.Errorf("%s: reason = %q, want %q", tc.name, got, tc.reason)
		}
		err = l.boxes.Prepare(context.Background())
		switch {
		case tc.refuse:
			if !errors.Is(err, box.ErrBoxUnavailable) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("%s: a boxed spawn must be refused with the reason, got %v", tc.name, err)
			}
			if strings.Contains(err.Error(), "/var/run") {
				t.Errorf("%s: the refusal carries docker's own text: %v", tc.name, err)
			}
		case err != nil:
			t.Errorf("%s: a boxed spawn must not be refused here: %v", tc.name, err)
		}
	}
}

// A stopped daemon that is started again is not stale: the next boxed spawn
// asks it afresh, and so does the next /api/meta once the cache is old.
func TestDaemonWatchRefreshes(t *testing.T) {
	var up atomic.Bool
	var calls atomic.Int32
	done := make(chan struct{}, 8)
	w := newDaemonWatch(func(context.Context) error {
		calls.Add(1)
		defer func() { done <- struct{}{} }()
		if up.Load() {
			return nil
		}
		return box.ErrDockerNotRunning
	}, time.Minute)
	now := time.Now()
	w.now = func() time.Time { return now }

	if got := box.DaemonReason(w.Check(context.Background())); got != "Docker is not running" {
		t.Fatalf("first check = %q", got)
	}
	<-done
	// Inside the cache window the answer is the stored one and docker is not asked.
	up.Store(true)
	if got := w.Reason(); got != "Docker is not running" || calls.Load() != 1 {
		t.Fatalf("fresh cache: reason %q after %d calls", got, calls.Load())
	}
	// Past it, with an ERROR stored, the first read waits for the probe behind it: the user started
	// Docker and reloaded, and the page must not say "not running" until a second reload.
	now = now.Add(2 * time.Minute)
	if got := w.Reason(); got != "" {
		t.Fatalf("the first read after the daemon came up still says %q", got)
	}
	<-done
	// A spawn-time check is always fresh and also updates what /api/meta serves.
	up.Store(false)
	if got := box.DaemonReason(w.Check(context.Background())); got != "Docker is not running" {
		t.Fatalf("spawn check = %q", got)
	}
	if got := w.Reason(); got != "Docker is not running" {
		t.Fatalf("the spawn check did not update the cache: %q", got)
	}
}

// One refresh at a time, and a probe that never answers is cut off: /api/meta
// must not queue a docker call per request behind a hung daemon.
func TestDaemonWatchRunsOneRefreshAndBoundsIt(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	w := newDaemonWatch(func(ctx context.Context) error {
		calls.Add(1)
		if _, ok := ctx.Deadline(); !ok {
			t.Error("the probe has no deadline")
		}
		<-release
		return nil
	}, 0)
	for i := 0; i < 5; i++ {
		w.Reason()
	}
	time.Sleep(50 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d probes in flight for 5 requests", n)
	}
	close(release)
}

// A stale HEALTHY answer is served at once (the page never waits on docker in the common case), and a
// hung daemon costs one read at most the wait, not the probe's own 5 s.
func TestDaemonWatchStaleReadsAreBounded(t *testing.T) {
	release := make(chan struct{})
	var hung atomic.Bool
	w := newDaemonWatch(func(context.Context) error {
		if hung.Load() {
			<-release
		}
		return nil
	}, time.Minute)
	now := time.Now()
	w.now = func() time.Time { return now }
	w.wait = 50 * time.Millisecond
	w.Check(context.Background())
	now = now.Add(2 * time.Minute)
	hung.Store(true)
	start := time.Now()
	if got := w.Reason(); got != "" || time.Since(start) > 40*time.Millisecond {
		t.Fatalf("a stale healthy answer must come back at once: %q after %v", got, time.Since(start))
	}
	// An error stored while the daemon hangs: the read waits no longer than the bound.
	w.mu.Lock()
	w.err = box.ErrDockerNotRunning
	w.mu.Unlock()
	start = time.Now()
	if got := w.Reason(); got != "Docker is not running" {
		t.Fatalf("a hung daemon leaves the stored answer, got %q", got)
	}
	if d := time.Since(start); d < 40*time.Millisecond || d > time.Second {
		t.Fatalf("the read took %v, want about the wait", d)
	}
	close(release)
}

// Two probes can finish out of order; the one that STARTED later is the newer fact.
func TestDaemonWatchKeepsTheNewerAnswer(t *testing.T) {
	slowStarted, releaseSlow := make(chan struct{}), make(chan struct{})
	var n atomic.Int32
	w := newDaemonWatch(func(context.Context) error {
		if n.Add(1) == 1 {
			close(slowStarted)
			<-releaseSlow
			return box.ErrDockerNotRunning // asked before Docker came up, answered after
		}
		return nil
	}, time.Minute)
	now := time.Now()
	var mu sync.Mutex
	w.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	slowDone := make(chan struct{})
	go func() { w.Check(context.Background()); close(slowDone) }()
	<-slowStarted
	mu.Lock()
	now = now.Add(time.Second)
	mu.Unlock()
	if err := w.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(releaseSlow)
	<-slowDone
	if got := w.Reason(); got != "" {
		t.Fatalf("an older probe that finished last overwrote the newer answer: %q", got)
	}
}
