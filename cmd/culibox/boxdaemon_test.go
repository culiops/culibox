package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
				t.Errorf("%s: a boxed spawn must be refused with the reason, got %v", tc.name, err)
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
	// Past it, the stored answer is returned at once and one probe runs behind it.
	now = now.Add(2 * time.Minute)
	if got := w.Reason(); got != "Docker is not running" {
		t.Fatalf("a stale answer is served while it refreshes, got %q", got)
	}
	<-done
	deadline := time.Now().Add(2 * time.Second)
	for w.Reason() != "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := w.Reason(); got != "" {
		t.Fatalf("the refreshed answer never replaced the stale one: %q", got)
	}
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
