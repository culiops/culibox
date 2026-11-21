package wsroots

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// scanRig is a Service whose scan is a fake that the test releases by hand.
type scanRig struct {
	svc   *Service
	calls atomic.Int32
	gate  chan struct{} // nil: the fake returns at once
	mu    sync.Mutex
	now   time.Time
	envs  []Env
	next  []Place
}

func newScanRig(t *testing.T, budget time.Duration) *scanRig {
	t.Helper()
	r := &scanRig{now: time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC), next: []Place{{Path: "/srv/a", Label: "a"}}}
	home, data := realDir(t), realDir(t)
	r.svc = NewService(data, home, nil, func() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.now })
	r.svc.scanOpts = ScanOpts{Budget: budget}
	r.svc.scanBases = func() []string { return []string{"/srv"} }
	r.svc.scanFn = func(ctx context.Context, bases []string, env Env, o ScanOpts) []Place {
		r.calls.Add(1)
		r.mu.Lock()
		r.envs = append(r.envs, env)
		out := append([]Place{}, r.next...)
		gate := r.gate
		r.mu.Unlock()
		if gate != nil {
			<-gate
		}
		return out
	}
	t.Cleanup(func() {
		r.mu.Lock()
		g := r.gate
		r.gate = nil
		r.mu.Unlock()
		if g != nil {
			select {
			case <-g:
			default:
				close(g)
			}
		}
	})
	return r
}

func (r *scanRig) advance(d time.Duration) { r.mu.Lock(); r.now = r.now.Add(d); r.mu.Unlock() }

func (r *scanRig) setGate(g chan struct{}) { r.mu.Lock(); r.gate = g; r.mu.Unlock() }

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", what)
}

func TestElsewhereFirstCallScansAndCaches(t *testing.T) {
	r := newScanRig(t, time.Second)
	got := r.svc.Elsewhere(context.Background())
	if len(got) != 1 || got[0].Path != "/srv/a" {
		t.Fatalf("got %v", got)
	}
	r.advance(4 * time.Minute)
	r.svc.Elsewhere(context.Background())
	if n := r.calls.Load(); n != 1 {
		t.Fatalf("scanned %d times inside the 5-minute cache, want 1", n)
	}
	if e := r.envs[0]; e.Home != r.svc.home || e.DataDir != r.svc.dataDir {
		t.Fatalf("scan env = %+v, want home %q and data dir %q", e, r.svc.home, r.svc.dataDir)
	}
	got[0].Path = "changed"
	if again := r.svc.Elsewhere(context.Background()); again[0].Path != "/srv/a" {
		t.Fatal("the caller got the cached slice, not a copy")
	}
}

func TestElsewhereFirstCallNeverWaitsPastTheBudget(t *testing.T) {
	r := newScanRig(t, 50*time.Millisecond)
	gate := make(chan struct{})
	r.setGate(gate)
	start := time.Now()
	got := r.svc.Elsewhere(context.Background())
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("waited %v for a scan that has not finished", d)
	}
	if len(got) != 0 {
		t.Fatalf("got %v from an unfinished scan", got)
	}
	// The scan is still running: another call starts none and does not wait long either.
	r.svc.Elsewhere(context.Background())
	if n := r.calls.Load(); n != 1 {
		t.Fatalf("%d scans in flight, want 1", n)
	}
	close(gate)
	eventually(t, "the unfinished scan's result is served", func() bool { return len(r.svc.Elsewhere(context.Background())) == 1 })
	if n := r.calls.Load(); n != 1 {
		t.Fatalf("%d scans, want 1", n)
	}
}

func TestElsewhereFirstCallStopsWaitingWhenTheRequestEnds(t *testing.T) {
	r := newScanRig(t, 10*time.Second)
	r.setGate(make(chan struct{}))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	r.svc.Elsewhere(ctx)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("waited %v after the request ended", d)
	}
}

func TestElsewhereStaleListIsServedWhileOneRefreshRuns(t *testing.T) {
	r := newScanRig(t, time.Second)
	r.svc.Elsewhere(context.Background()) // fills the cache
	r.advance(6 * time.Minute)
	gate := make(chan struct{})
	r.setGate(gate)
	r.mu.Lock()
	r.next = []Place{{Path: "/srv/b", Label: "b"}}
	r.mu.Unlock()

	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := r.svc.Elsewhere(context.Background())
			if len(got) != 1 || got[0].Path != "/srv/a" {
				t.Errorf("a stale call got %v, want the old list", got)
			}
		}()
	}
	wg.Wait()
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("stale calls waited %v on the refresh", d)
	}
	eventually(t, "the refresh started", func() bool { return r.calls.Load() >= 2 })
	if n := r.calls.Load(); n != 2 {
		t.Fatalf("%d scans, want the first plus exactly one refresh", n)
	}
	close(gate)
	eventually(t, "the refresh's result is served", func() bool {
		g := r.svc.Elsewhere(context.Background())
		return len(g) == 1 && g[0].Path == "/srv/b"
	})
	if n := r.calls.Load(); n != 2 {
		t.Fatalf("%d scans after the refresh, want 2", n)
	}
}
