package boxaddr

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeLookup is a box-address resolver: box name → address, with a call count.
type fakeLookup struct {
	mu    sync.Mutex
	ips   map[string]netip.Addr
	err   error
	calls atomic.Int32
	gate  chan struct{} // non-nil: each lookup waits for it
}

func newFake() *fakeLookup { return &fakeLookup{ips: map[string]netip.Addr{}} }

func (f *fakeLookup) set(box, ip string) {
	f.mu.Lock()
	f.ips[box] = netip.MustParseAddr(ip)
	f.mu.Unlock()
}

func (f *fakeLookup) resolve(_ context.Context, box string) (netip.Addr, error) {
	f.calls.Add(1)
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return netip.Addr{}, f.err
	}
	a, ok := f.ips[box]
	if !ok {
		return netip.Addr{}, errors.New("no such box")
	}
	return a, nil
}

// fakeClock is a settable now.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }

func ip(s string) netip.Addr { return netip.MustParseAddr(s) }

// waitCalls blocks until the fake has been asked at least n times.
func waitCalls(t *testing.T, f *fakeLookup, n int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for f.calls.Load() < n {
		if time.Now().After(deadline) {
			t.Fatal("the lookup never started")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestMatchIsCachedNoSecondLookup(t *testing.T) {
	f := newFake()
	f.set("a", "10.0.0.5")
	c := New(context.Background(), f.resolve, newClock().now)
	for i := 0; i < 5; i++ {
		if !c.SourceOK(context.Background(), "a", ip("10.0.0.5")) {
			t.Fatalf("call %d: refused the box's own address", i)
		}
	}
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("lookups = %d, want 1 (the first contact only)", n)
	}
}

func TestMismatchLooksUpAtMostOncePerInterval(t *testing.T) {
	f := newFake()
	f.set("a", "10.0.0.5")
	clk := newClock()
	c := New(context.Background(), f.resolve, clk.now)
	for i := 0; i < 6; i++ {
		if c.SourceOK(context.Background(), "a", ip("10.0.0.6")) {
			t.Fatalf("attempt %d: another box's address accepted", i)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c.SourceOK(context.Background(), "a", ip("10.0.0.6")) {
				t.Error("concurrent attempt accepted")
			}
		}()
	}
	wg.Wait()
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("lookups within one interval = %d, want 1", n)
	}
	clk.advance(RefreshInterval + time.Second)
	c.SourceOK(context.Background(), "a", ip("10.0.0.6"))
	c.SourceOK(context.Background(), "a", ip("10.0.0.6"))
	if n := f.calls.Load(); n != 2 {
		t.Fatalf("after the interval: lookups = %d, want 2", n)
	}
}

func TestChangedAddressServedAfterRefresh(t *testing.T) {
	f := newFake()
	f.set("a", "10.0.0.9")
	clk := newClock()
	c := New(context.Background(), f.resolve, clk.now)
	if c.SourceOK(context.Background(), "a", ip("10.0.0.5")) {
		t.Fatal("accepted a source that is not the box's address")
	}
	f.set("a", "10.0.0.5") // the box was recreated
	clk.advance(RefreshInterval + time.Second)
	if !c.SourceOK(context.Background(), "a", ip("10.0.0.5")) {
		t.Fatal("refused the box's new address after the interval")
	}
	if n := f.calls.Load(); n != 2 {
		t.Fatalf("lookups = %d, want 2", n)
	}
}

func TestLookupErrorAndNoLookupRefuse(t *testing.T) {
	bad := newFake()
	bad.err = errors.New("docker: no such container")
	c := New(context.Background(), bad.resolve, newClock().now)
	if c.SourceOK(context.Background(), "a", ip("10.0.0.5")) {
		t.Fatal("a lookup error must refuse")
	}

	none := New(context.Background(), nil, nil)
	if none.SourceOK(context.Background(), "a", ip("10.0.0.5")) {
		t.Fatal("no lookup installed must refuse")
	}
	f := newFake()
	f.set("a", "10.0.0.5")
	none.SetLookup(f.resolve)
	if !none.SourceOK(context.Background(), "a", ip("10.0.0.5")) {
		t.Fatal("refused after SetLookup installed one")
	}
	g := newFake()
	g.set("a", "10.0.0.5")
	none.SetLookup(g.resolve)
	none.Drop("a") // forces a fresh lookup through whichever function is installed
	if !none.SourceOK(context.Background(), "a", ip("10.0.0.5")) {
		t.Fatal("refused after a second SetLookup")
	}
	if g.calls.Load() != 0 {
		t.Fatal("a second SetLookup replaced the first")
	}
	if f.calls.Load() != 2 {
		t.Fatalf("first lookup calls = %d, want 2", f.calls.Load())
	}
}

func TestInvalidInputRefuses(t *testing.T) {
	f := newFake()
	f.set("a", "10.0.0.5")
	c := New(context.Background(), f.resolve, newClock().now)
	if c.SourceOK(context.Background(), "", ip("10.0.0.5")) {
		t.Fatal("empty box accepted")
	}
	if c.SourceOK(context.Background(), "a", netip.Addr{}) {
		t.Fatal("invalid source accepted")
	}
	if n := f.calls.Load(); n != 0 {
		t.Fatalf("lookups = %d, want 0", n)
	}
}

// Concurrent first contacts from a legitimate box all wait for the one lookup
// and then pass: the single flight must not refuse them.
func TestConcurrentFirstContactsShareOneLookup(t *testing.T) {
	f := newFake()
	f.set("a", "10.0.0.5")
	f.gate = make(chan struct{})
	c := New(context.Background(), f.resolve, newClock().now)
	var wg sync.WaitGroup
	oks := make(chan bool, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			oks <- c.SourceOK(context.Background(), "a", ip("10.0.0.5"))
		}()
	}
	waitCalls(t, f, 1)
	time.Sleep(50 * time.Millisecond)
	close(f.gate)
	wg.Wait()
	close(oks)
	for ok := range oks {
		if !ok {
			t.Fatal("a concurrent first contact was refused")
		}
	}
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("lookups = %d, want 1", n)
	}
}

func TestDropForcesAFreshLookup(t *testing.T) {
	f := newFake()
	f.set("a", "10.0.0.5")
	c := New(context.Background(), f.resolve, newClock().now)
	if !c.SourceOK(context.Background(), "a", ip("10.0.0.5")) {
		t.Fatal("refused the box's own address")
	}
	c.Drop("a")
	f.set("a", "10.0.0.7") // recreated; no clock advance
	if !c.SourceOK(context.Background(), "a", ip("10.0.0.7")) {
		t.Fatal("refused the recreated box after Drop")
	}
	if n := f.calls.Load(); n != 2 {
		t.Fatalf("lookups = %d, want 2", n)
	}
}

func TestLookupIsBoundedAndUsesBase(t *testing.T) {
	blocking := func(ctx context.Context, _ string) (netip.Addr, error) {
		<-ctx.Done()
		return netip.Addr{}, ctx.Err()
	}
	c := New(context.Background(), blocking, newClock().now)
	c.timeout = 50 * time.Millisecond
	start := time.Now()
	if c.SourceOK(context.Background(), "a", ip("10.0.0.5")) {
		t.Fatal("a lookup that never answers must refuse")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("SourceOK took %v with a 50ms lookup timeout", d)
	}

	// Cancelling the base context aborts an in-flight lookup.
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	aborted := make(chan struct{})
	c2 := New(base, func(ctx context.Context, _ string) (netip.Addr, error) {
		close(started)
		<-ctx.Done()
		close(aborted)
		return netip.Addr{}, ctx.Err()
	}, newClock().now)
	done := make(chan bool, 1)
	go func() { done <- c2.SourceOK(context.Background(), "a", ip("10.0.0.5")) }()
	<-started
	cancel()
	select {
	case <-aborted:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling the base context did not abort the lookup")
	}
	if ok := <-done; ok {
		t.Fatal("an aborted lookup must refuse")
	}
}

// A caller that hangs up must not leave a failed entry that refuses the box's
// own next request: the lookup runs under the cache's context, not the
// caller's.
func TestCallerHangupLeavesNoFailedEntry(t *testing.T) {
	f := newFake()
	f.set("a", "10.0.0.5")
	f.gate = make(chan struct{})
	c := New(context.Background(), f.resolve, newClock().now)

	// The first caller starts the lookup; its context is cancelled while the
	// lookup runs. A second caller is waiting on that lookup and hangs up too.
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan bool, 1)
	go func() { first <- c.SourceOK(ctx, "a", ip("10.0.0.5")) }()
	waitCalls(t, f, 1)
	waiter := make(chan bool, 1)
	go func() { waiter <- c.SourceOK(ctx, "a", ip("10.0.0.5")) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if ok := <-waiter; ok {
		t.Fatal("a caller that hung up while waiting was served")
	}

	close(f.gate) // the lookup completes under the base context
	<-first
	if !c.SourceOK(context.Background(), "a", ip("10.0.0.5")) {
		t.Fatal("the owning box was refused after callers hung up")
	}
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("lookups = %d, want 1", n)
	}
}
