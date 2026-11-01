package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/bundle"
)

func TestRunBundleGCPassesAtStartAndOnEachTick(t *testing.T) {
	var passes atomic.Int32
	stop := runBundleGC(func(context.Context) { passes.Add(1) }, time.Millisecond)
	defer stop()
	deadline := time.Now().Add(5 * time.Second)
	for passes.Load() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("passes = %d", passes.Load())
		}
		time.Sleep(time.Millisecond)
	}
}

// Stop cancels a pass in flight and waits for it, so nothing runs on past
// shutdown.
func TestRunBundleGCStopCancelsAndWaits(t *testing.T) {
	entered := make(chan struct{})
	finished := make(chan struct{})
	stop := runBundleGC(func(ctx context.Context) {
		close(entered)
		<-ctx.Done()
		close(finished)
	}, time.Hour)
	<-entered
	stop()
	select {
	case <-finished:
	default:
		t.Fatal("stop returned while the pass was still running")
	}
}

// A pass that ignores cancellation cannot hold shutdown forever.
func TestRunBundleGCStopIsBounded(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	stop := runBundleGC(func(context.Context) {
		close(entered)
		<-release
	}, time.Hour)
	<-entered
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(retentionStopWait + 5*time.Second):
		t.Fatal("stop did not return")
	}
}

func TestLogBundleGC(t *testing.T) {
	var lines []string
	logf := func(f string, a ...any) { lines = append(lines, strings.TrimSpace(fmt.Sprintf(f, a...))) }

	logBundleGC(bundle.GCReport{}, nil, logf)
	if len(lines) != 0 {
		t.Fatalf("a pass that did nothing logged %q", lines)
	}

	logBundleGC(bundle.GCReport{Removed: []bundle.GCRemoved{{Name: "abc", Age: 72 * time.Hour, Bytes: 3 << 20}}, Swept: 2}, nil, logf)
	all := strings.Join(lines, "\n")
	for _, want := range []string{"abc", "3 days", "3 MiB", "2 leftover"} {
		if !strings.Contains(all, want) {
			t.Errorf("log %q lacks %q", all, want)
		}
	}

	lines = nil
	logBundleGC(bundle.GCReport{}, errors.New("docker gone"), logf)
	if len(lines) != 1 || !strings.Contains(lines[0], "docker gone") {
		t.Fatalf("lines = %q", lines)
	}
}

func TestAwaitShutdownStopsBundleGC(t *testing.T) {
	stopped := false
	sig := make(chan os.Signal, 1)
	sig <- syscall.SIGTERM
	awaitShutdown(sig, shutdownSteps{
		DetachAll:     func() int { return 0 },
		StopAnalytics: func() {},
		StopBundleGC:  func() { stopped = true },
		ShutdownHTTP:  func(context.Context) error { return nil },
	}, func(string, ...any) {})
	if !stopped {
		t.Fatal("shutdown left the bundle GC running")
	}
}

// The mount-source spelling on the colima VM is unverified, and a wrong spelling
// reads as "not mounted": nothing may be removed there until it is checked.
func TestBundleGCDoesNotRunOnDarwin(t *testing.T) {
	var lines []string
	logf := func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
	passed := make(chan struct{}, 1)
	pass := func(context.Context) { passed <- struct{}{} }

	stop := (&boxLayer{goos: "darwin"}).startBundleGC(pass, logf)
	stop()
	select {
	case <-passed:
		t.Fatal("a GC pass ran on darwin")
	case <-time.After(50 * time.Millisecond):
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "darwin") {
		t.Fatalf("lines = %q", lines)
	}

	lines = nil
	stop = (&boxLayer{goos: "linux"}).startBundleGC(pass, logf)
	defer stop()
	select {
	case <-passed:
	case <-time.After(5 * time.Second):
		t.Fatal("no GC pass on linux")
	}
	if len(lines) != 0 {
		t.Fatalf("lines = %q", lines)
	}
}
