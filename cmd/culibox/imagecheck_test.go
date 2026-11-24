package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	boximage "github.com/culiops/culibox/docker/culibox"
	"github.com/culiops/culibox/internal/boximagecheck"
	"github.com/culiops/culibox/internal/config"
)

func TestRunImageCheckFirstDelayAndStop(t *testing.T) {
	var passes atomic.Int32
	stop := runImageCheck(func(context.Context) { passes.Add(1) }, 10*time.Millisecond, time.Hour)
	deadline := time.Now().Add(3 * time.Second)
	for passes.Load() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("the first pass never ran")
		}
		time.Sleep(time.Millisecond)
	}
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stop did not return")
	}
	if n := passes.Load(); n != 1 {
		t.Fatalf("passes = %d, want 1 (the next one is an hour away)", n)
	}
}

// The first pass waits: nothing runs at start, so a daemon that is restarted in
// a loop never asks the registry.
func TestRunImageCheckWaitsBeforeTheFirstPass(t *testing.T) {
	var passes atomic.Int32
	stop := runImageCheck(func(context.Context) { passes.Add(1) }, time.Hour, time.Hour)
	defer stop()
	time.Sleep(50 * time.Millisecond)
	if passes.Load() != 0 {
		t.Fatal("a pass ran before the first delay")
	}
}

func TestRunImageCheckStopCancelsAndIsBounded(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	stop := runImageCheck(func(context.Context) {
		close(entered)
		<-release // ignores cancellation
	}, time.Millisecond, time.Hour)
	<-entered
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(retentionStopWait + 5*time.Second):
		t.Fatal("stop did not return")
	}
}

func TestAwaitShutdownStopsImageCheck(t *testing.T) {
	stopped := false
	sig := make(chan os.Signal, 1)
	sig <- syscall.SIGTERM
	awaitShutdown(sig, shutdownSteps{
		DetachAll:      func() int { return 0 },
		StopAnalytics:  func() {},
		StopImageCheck: func() { stopped = true },
		ShutdownHTTP:   func(context.Context) error { return nil },
	}, func(string, ...any) {})
	if !stopped {
		t.Fatal("shutdown left the image check running")
	}
}

const testPublished = "ghcr.io/culiops/culibox@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func swapPublished(t *testing.T, ref string) {
	t.Helper()
	old := publishedBoxImage
	t.Cleanup(func() { publishedBoxImage = old })
	publishedBoxImage = func() string { return ref }
}

// A docker CLI that answers after 30 s: the hook gives up at 5 s and still says
// what it can from the config alone.
type slowDocker struct{}

func (slowDocker) ServerArch(ctx context.Context) (string, error) {
	select {
	case <-time.After(30 * time.Second):
		return "amd64", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (slowDocker) Describe(ctx context.Context, _ string) (boximage.ImageInfo, error) {
	<-ctx.Done()
	return boximage.ImageInfo{}, ctx.Err()
}

func TestBoxImageHookDockerDown(t *testing.T) {
	swapPublished(t, testPublished)
	hook := boxImageHook(config.Config{}, t.TempDir(), slowDocker{})
	start := time.Now()
	rep := hook(context.Background())
	if d := time.Since(start); d > 6*time.Second {
		t.Fatalf("hook took %v, want under 6s", d)
	}
	if rep.Verdict != boximagecheck.Unknown || rep.Want != testPublished || rep.Info != nil {
		t.Fatalf("report = %+v", rep)
	}
}

type imgFacts struct {
	arch string
	info boximage.ImageInfo
	err  error
	seen string
}

func (f *imgFacts) ServerArch(context.Context) (string, error) { return f.arch, f.err }
func (f *imgFacts) Describe(_ context.Context, image string) (boximage.ImageInfo, error) {
	f.seen = image
	return f.info, f.err
}

func TestBoxImageHookReadsConfigDockerAndCache(t *testing.T) {
	swapPublished(t, testPublished)
	off := false
	f := &imgFacts{arch: "amd64", info: boximage.ImageInfo{Present: true, RepoDigests: []string{testPublished}}}
	rep := boxImageHook(config.Config{BoxImage: testPublished, ImageCheck: &off}, t.TempDir(), f)(context.Background())
	if f.seen != testPublished {
		t.Fatalf("Describe asked about %q", f.seen)
	}
	if rep.Verdict != boximagecheck.Current || !rep.Explicit || rep.CheckOn {
		t.Fatalf("report = %+v", rep)
	}
}

func TestBoxImageHookNilDockerAndBadCache(t *testing.T) {
	swapPublished(t, testPublished)
	dir := t.TempDir()
	// A cache from a newer CuliBox is an error to LoadCache: the hook goes on without it.
	if err := os.WriteFile(filepath.Join(dir, boximagecheck.CacheFile), []byte(`{"version":9999}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := boxImageHook(config.Config{}, dir, nil)(context.Background())
	if rep.Verdict != boximagecheck.Unknown || rep.Want != testPublished || rep.Info != nil {
		t.Fatalf("report = %+v", rep)
	}
}

func fakeRegistryChecker(t *testing.T, hits *atomic.Int32, status int) boximagecheck.Checker {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return boximagecheck.Checker{Client: srv.Client(), Registry: srv.URL, GitHubAPI: srv.URL}
}

func TestImageCheckPassNoNetworkWhenNothingToAsk(t *testing.T) {
	swapPublished(t, testPublished) // a release binary on its default image: the answer is baked in
	var hits atomic.Int32
	pass := imageCheckPass(config.Config{}, t.TempDir(), fakeRegistryChecker(t, &hits, 200), func(string, ...any) {})
	pass(context.Background())
	if hits.Load() != 0 {
		t.Fatalf("%d request(s) for a default image on a release binary", hits.Load())
	}
}

func TestImageCheckPassOneAttemptPerDayWhateverTheOutcome(t *testing.T) {
	swapPublished(t, "") // a source build has nothing baked, so it asks
	var hits atomic.Int32
	dir := t.TempDir()
	now := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	k := fakeRegistryChecker(t, &hits, http.StatusInternalServerError)
	k.Now = func() time.Time { return now }
	pass := imageCheckPass(config.Config{}, dir, k, func(string, ...any) {})

	pass(context.Background())
	first := hits.Load()
	if first == 0 {
		t.Fatal("the first pass asked nothing")
	}
	// A cache that cannot be saved leaves the check due on every call; the pass
	// must not turn that into a request per tick.
	if err := os.Remove(filepath.Join(dir, boximagecheck.CacheFile)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	pass(context.Background())
	now = now.Add(22 * time.Hour)
	pass(context.Background())
	if hits.Load() != first {
		t.Fatalf("requests grew from %d to %d inside 24h", first, hits.Load())
	}
	now = now.Add(2 * time.Hour) // 25h after the first attempt
	pass(context.Background())
	if hits.Load() == first {
		t.Fatal("no new attempt after 24h")
	}
}

func TestImageCheckPassDoesNotUseUpTheDayWhenNotDue(t *testing.T) {
	swapPublished(t, "")
	var hits atomic.Int32
	dir := t.TempDir()
	now := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	k := fakeRegistryChecker(t, &hits, http.StatusInternalServerError)
	k.Now = func() time.Time { return now }
	// A fresh cache from 20 h ago: the first pass finds nothing due.
	fresh := &boximagecheck.Cache{Version: 1, CheckedAt: now.Add(-20 * time.Hour), Hash: boximage.Hash()}
	if err := boximagecheck.SaveCache(dir, fresh); err != nil {
		t.Fatal(err)
	}
	pass := imageCheckPass(config.Config{}, dir, k, func(string, ...any) {})
	pass(context.Background())
	if hits.Load() != 0 {
		t.Fatal("asked while the cache was fresh")
	}
	now = now.Add(5 * time.Hour) // the cache is now 25 h old
	pass(context.Background())
	if hits.Load() == 0 {
		t.Fatal("a pass that asked nothing held the next one off")
	}
}

func TestImageCheckPassLogsOnlyANewErrorClass(t *testing.T) {
	swapPublished(t, "")
	var hits atomic.Int32
	dir := t.TempDir()
	now := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	k := fakeRegistryChecker(t, &hits, http.StatusInternalServerError)
	k.Now = func() time.Time { return now }
	var lines []string
	logf := func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
	pass := imageCheckPass(config.Config{}, dir, k, logf)

	for range 3 {
		pass(context.Background())
		now = now.Add(25 * time.Hour)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "image check") {
		t.Fatalf("lines = %q, want one line for three passes of one class", lines)
	}

	// A cache from a newer CuliBox is another class.
	if err := os.WriteFile(filepath.Join(dir, boximagecheck.CacheFile), []byte(`{"version":9999}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pass(context.Background())
	if len(lines) != 2 {
		t.Fatalf("lines = %q, want a second for the new class", lines)
	}
}

func TestImageCheckPassErrorsNeverReachTheLog(t *testing.T) {
	swapPublished(t, "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, boximagecheck.CacheFile), []byte(`{"version":9999}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	logf := func(f string, a ...any) { out.WriteString(fmt.Sprintf(f, a...)) }
	imageCheckPass(config.Config{}, dir, boximagecheck.Checker{}, logf)(context.Background())
	if out.Len() == 0 || strings.Contains(out.String(), dir) {
		t.Fatalf("log = %q, want one line without the error text or path", out.String())
	}
}
