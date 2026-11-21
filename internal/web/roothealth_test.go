package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/wsroots"
)

// hungRootRig is a Server with two added roots, one of them on a "mount" whose
// check never answers (a hung network share).
type hungRootRig struct {
	s              *Server
	home, ok, hung string
	block          chan struct{}
	hungCalls      atomic.Int32
}

func newHungRootRig(t *testing.T) *hungRootRig {
	t.Helper()
	home, ok, hung := rootsFixture(t)
	mustWrite(t, filepath.Join(home, "h.txt"), "x")
	data := realTemp(t)
	svc := wsroots.NewService(data, home, nil, nil)
	svc.SetScanBases(func() []string { return nil })
	for _, r := range []string{ok, hung} {
		if _, err := svc.Add(r, storeActor); err != nil {
			t.Fatal(err)
		}
	}
	g := &hungRootRig{home: home, ok: ok, hung: hung, block: make(chan struct{})}
	t.Cleanup(func() { close(g.block) })
	g.s = &Server{Home: home, DataDir: data, Roots: svc}
	g.s.rootHealth.wait = 50 * time.Millisecond
	g.s.rootHealth.check = func(root string) bool {
		if root == hung {
			g.hungCalls.Add(1)
			<-g.block
		}
		return rootIsLiveDir(root)
	}
	return g
}

// returnsWithin runs f and fails the test if it has not returned after d: a request
// blocked on a hung root must fail the test, not hang it.
func returnsWithin(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %v", what, d)
	}
}

// I3: a hung root never stalls a Files request on home, nor the Settings list,
// and is reported not available and dropped from the policy.
func TestHungRootDoesNotStallRequests(t *testing.T) {
	g := newHungRootRig(t)
	list := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		g.s.handleFsList(rr, httptest.NewRequest("GET", "/api/fs/list?path="+url.QueryEscape(g.home), nil))
		return rr
	}
	// The first request may wait the check deadline once.
	returnsWithin(t, 2*time.Second, "the first fs request on home", func() {
		if rr := list(); rr.Code != http.StatusOK {
			t.Errorf("list home = %d (%s)", rr.Code, rr.Body)
		}
	})
	// Later ones never wait on it: many, concurrently.
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rr := list(); rr.Code != http.StatusOK {
				t.Errorf("list home = %d", rr.Code)
			}
		}()
	}
	returnsWithin(t, 2*time.Second, "20 more fs requests on home", wg.Wait)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("20 requests took %v with a hung root", d)
	}
	returnsWithin(t, 2*time.Second, "GET workspace roots", func() {
		v := decodeRoots(t, do(g.s.handleWorkspaceRoots, storeReq("GET", "/api/workspace-roots", "", "", credCookie)))
		avail := map[string]bool{}
		for _, r := range v.Roots {
			avail[r.Path] = r.Available
		}
		if avail[g.hung] || !avail[g.ok] || !avail[g.home] {
			t.Errorf("available = %v: the hung root must show not available, the others available", avail)
		}
	})
	returnsWithin(t, 2*time.Second, "the policy build", func() {
		p, err := g.s.fsPolicy()
		if err != nil {
			t.Error(err)
			return
		}
		if p.hasRoot(g.hung) || !p.hasRoot(g.ok) {
			t.Errorf("policy roots = %v: the hung root must be dropped, the healthy one kept", p.roots)
		}
	})
	if n := g.hungCalls.Load(); n != 1 {
		t.Fatalf("checks started on the hung root = %d, want exactly 1 (one in flight at a time)", n)
	}
}

// A healthy root's check is cached: requests inside the TTL start no check,
// and a root that goes bad is dropped once the TTL has passed.
func TestRootHealthCachesAndRechecks(t *testing.T) {
	_, root, _ := rootsFixture(t)
	var calls atomic.Int32
	bad := atomic.Bool{}
	h := &rootHealth{ttl: 100 * time.Millisecond, check: func(string) bool { calls.Add(1); return !bad.Load() }}
	for i := 0; i < 10; i++ {
		if !h.live([]string{root})[root] {
			t.Fatal("a healthy root reads not live")
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d checks inside the TTL, want 1", n)
	}
	bad.Store(true)
	time.Sleep(150 * time.Millisecond)
	if h.live([]string{root})[root] {
		t.Fatal("a root that went bad is still live after the TTL")
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("%d checks, want 2", n)
	}
}

// R15: a concurrent live() over a shorter list forgets a root between the two
// locked phases of another live() that still lists it. The first call must not
// dereference the forgotten entry. Real trigger: a DELETE of r2 (its reply
// builds the list) while an fs request waits on r1's first check.
func TestRootHealthForgottenRootMidCall(t *testing.T) {
	_, r1, r2 := rootsFixture(t)
	release := make(chan struct{})
	h := &rootHealth{wait: 5 * time.Second, check: func(r string) bool {
		if r == r1 {
			<-release
		}
		return true
	}}
	state := func(f func() bool) bool { h.mu.Lock(); defer h.mu.Unlock(); return f() }
	aDone := make(chan any, 1)
	go func() {
		defer func() { aDone <- recover() }()
		h.live([]string{r1, r2}) // waits on r1; r2 answers at once
	}()
	eventually := func(what string, ok func() bool) {
		t.Helper()
		for i := 0; i < 400; i++ {
			if ok() {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("timed out: %s", what)
	}
	eventually("r2's check answered", func() bool {
		return state(func() bool { c := h.roots[r2]; return c != nil && c.known && c.running == nil })
	})
	go h.live([]string{r1}) // forgets r2, then waits on r1 too
	eventually("r2 forgotten", func() bool { return state(func() bool { return h.roots[r2] == nil }) })
	close(release)
	select {
	case p := <-aDone:
		if p != nil {
			t.Fatalf("live panicked on a root forgotten mid-call: %v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first live() never returned")
	}
}
