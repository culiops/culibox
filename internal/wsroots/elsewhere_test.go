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

// A first scan that hangs (a dead mount) costs the first request its budget; the
// requests after it get the empty list at once instead of each waiting.
func TestElsewhereHungFirstScanCostsOneRequest(t *testing.T) {
	r := newScanRig(t, 50*time.Millisecond)
	r.setGate(make(chan struct{})) // never released
	start := time.Now()
	if got := r.svc.Elsewhere(context.Background()); len(got) != 0 {
		t.Fatalf("got %v from a hung scan", got)
	}
	if d := time.Since(start); d < 40*time.Millisecond || d > 600*time.Millisecond {
		t.Fatalf("the first request took %v, want about the budget", d)
	}
	for i := 0; i < 5; i++ {
		start = time.Now()
		if got := r.svc.Elsewhere(context.Background()); len(got) != 0 {
			t.Fatalf("got %v", got)
		}
		if d := time.Since(start); d > 20*time.Millisecond {
			t.Fatalf("request %d waited %v after the first one timed out", i+2, d)
		}
	}
	if n := r.calls.Load(); n != 1 {
		t.Fatalf("%d scans, want 1", n)
	}
}

// A scan that has run past three budgets is given up on: a new one may start, both
// for a refresh behind a stale list and for a cold start that published empty.
func TestElsewhereAbandonsAHungScan(t *testing.T) {
	const budget = 50 * time.Millisecond
	t.Run("refresh behind a stale list", func(t *testing.T) {
		r := newScanRig(t, budget)
		r.svc.Elsewhere(context.Background()) // fills the cache
		r.advance(6 * time.Minute)
		r.setGate(make(chan struct{})) // the refresh hangs
		r.svc.Elsewhere(context.Background())
		eventually(t, "the refresh started", func() bool { return r.calls.Load() == 2 })
		r.advance(2 * budget) // not yet three budgets
		r.svc.Elsewhere(context.Background())
		time.Sleep(50 * time.Millisecond)
		if n := r.calls.Load(); n != 2 {
			t.Fatalf("%d scans before three budgets passed, want 2", n)
		}
		r.advance(2 * budget) // now past three
		r.svc.Elsewhere(context.Background())
		eventually(t, "a new scan started after three budgets", func() bool { return r.calls.Load() == 3 })
	})
	t.Run("cold start", func(t *testing.T) {
		r := newScanRig(t, budget)
		r.setGate(make(chan struct{}))
		r.svc.Elsewhere(context.Background()) // times out, publishes empty
		r.advance(6 * time.Minute)            // the empty list is stale
		r.svc.Elsewhere(context.Background())
		eventually(t, "a new scan started", func() bool { return r.calls.Load() == 2 })
	})
}

// The abandoned scan finishing late must not replace what the newer scan found.
func TestElsewhereAbandonedScanNeverOverwritesTheCache(t *testing.T) {
	const budget = 50 * time.Millisecond
	r := newScanRig(t, budget)
	zombie := make(chan struct{})
	var n atomic.Int32
	r.svc.scanFn = func(context.Context, []string, Env, ScanOpts) []Place {
		if n.Add(1) == 1 {
			<-zombie
			return []Place{{Path: "/srv/old", Label: "old"}}
		}
		return []Place{{Path: "/srv/new", Label: "new"}}
	}
	r.svc.Elsewhere(context.Background()) // scan 1 hangs; empty is published
	r.advance(6 * time.Minute)
	r.svc.Elsewhere(context.Background()) // abandons scan 1, starts scan 2
	eventually(t, "scan 2's result is served", func() bool {
		g := r.svc.Elsewhere(context.Background())
		return len(g) == 1 && g[0].Path == "/srv/new"
	})
	close(zombie) // scan 1 finishes late
	time.Sleep(50 * time.Millisecond)
	if g := r.svc.Elsewhere(context.Background()); len(g) != 1 || g[0].Path != "/srv/new" {
		t.Fatalf("the abandoned scan overwrote the cache: %v", g)
	}
	// And it did not clear the running state of a newer scan: a stale list still refreshes.
	r.advance(6 * time.Minute)
	r.svc.Elsewhere(context.Background())
	eventually(t, "a refresh after the zombie ended", func() bool { return n.Load() == 3 })
}

// I4: scans on a mount that never answers are each given up on, but at most
// two given-up scans are ever left blocked, and a give-up serves the stale list
// for another TTL instead of starting a new scan on every later request.
func TestElsewhereHungScansAreBounded(t *testing.T) {
	const budget = 50 * time.Millisecond
	r := newScanRig(t, budget)
	r.setGate(make(chan struct{}))        // never released: every scan blocks for good
	r.svc.Elsewhere(context.Background()) // scan 1 hangs; empty is published
	r.advance(6 * time.Minute)
	r.svc.Elsewhere(context.Background()) // abandons scan 1, starts scan 2
	eventually(t, "scan 2 started", func() bool { return r.calls.Load() == 2 })
	// Requests a few seconds apart inside the new TTL start nothing.
	for i := 0; i < 20; i++ {
		r.advance(10 * time.Second)
		r.svc.Elsewhere(context.Background())
	}
	time.Sleep(30 * time.Millisecond)
	if n := r.calls.Load(); n != 2 {
		t.Fatalf("%d scans within the TTL after a give-up, want 2", n)
	}
	// Past many TTLs, the hung scans never pile up: two abandoned plus the current one.
	for i := 0; i < 20; i++ {
		r.advance(6 * time.Minute)
		r.svc.Elsewhere(context.Background())
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
	if n := r.calls.Load(); n != 3 {
		t.Fatalf("%d blocked scans, want at most 2 abandoned plus the current one (3)", n)
	}
	r.svc.scanMu.Lock()
	z := r.svc.scanZombies
	r.svc.scanMu.Unlock()
	if z != 2 {
		t.Fatalf("abandoned scans counted %d, want 2", z)
	}
}

// I4: an abandoned scan that finally ends frees its slot.
func TestElsewhereZombieSlotFreedWhenItEnds(t *testing.T) {
	const budget = 50 * time.Millisecond
	r := newScanRig(t, budget)
	gate := make(chan struct{})
	r.setGate(gate)
	r.svc.Elsewhere(context.Background())
	for i := 0; i < 3; i++ {
		r.advance(6 * time.Minute)
		r.svc.Elsewhere(context.Background())
		time.Sleep(2 * time.Millisecond)
	}
	eventually(t, "three scans started", func() bool { return r.calls.Load() >= 3 })
	close(gate) // every blocked scan ends
	eventually(t, "the abandoned scans were counted out", func() bool {
		r.svc.scanMu.Lock()
		defer r.svc.scanMu.Unlock()
		return r.svc.scanZombies == 0
	})
}

func TestDefaultSkips(t *testing.T) {
	if got := DefaultSkips("darwin"); len(got) != 1 || got[0] != "/opt/homebrew" {
		t.Fatalf("darwin: %v", got)
	}
	if got := DefaultSkips("linux"); got != nil {
		t.Fatalf("linux: %v", got)
	}
}
