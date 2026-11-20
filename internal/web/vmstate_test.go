package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func getVMState(a *API, ctx context.Context) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	a.handleVMState(w, httptest.NewRequest(http.MethodGet, "/api/vm-state", nil).WithContext(ctx))
	return w
}

func wantState(t *testing.T, w *httptest.ResponseRecorder, want string) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", w.Code, w.Body)
	}
	if got := strings.TrimSpace(w.Body.String()); got != `{"state":"`+want+`"}` {
		t.Fatalf("body = %s, want state %q", got, want)
	}
}

func TestVMStateNilHook(t *testing.T) {
	wantState(t, getVMState(&API{}, context.Background()), "")
}

func TestVMStateOnlyGet(t *testing.T) {
	var calls atomic.Int32
	a := &API{VMState: func(context.Context) string { calls.Add(1); return "stopped" }}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		w := httptest.NewRecorder()
		a.handleVMState(w, httptest.NewRequest(m, "/api/vm-state", nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: code = %d, want 405", m, w.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("a refused method must not read the VM")
	}
}

func TestVMStateCached15s(t *testing.T) {
	now := time.Unix(1000, 0)
	var calls atomic.Int32
	a := &API{VMState: func(context.Context) string {
		if calls.Add(1) == 1 {
			return "stopped"
		}
		return "running"
	}}
	a.vmCache.now = func() time.Time { return now }

	wantState(t, getVMState(a, context.Background()), "stopped")
	now = now.Add(14 * time.Second)
	wantState(t, getVMState(a, context.Background()), "stopped")
	if calls.Load() != 1 {
		t.Fatalf("hook calls within 15s = %d, want 1", calls.Load())
	}
	now = now.Add(time.Second) // 15s after the first answer
	wantState(t, getVMState(a, context.Background()), "running")
	if calls.Load() != 2 {
		t.Fatalf("hook calls after 15s = %d, want 2", calls.Load())
	}
}

func TestVMStateSingleFlight(t *testing.T) {
	const n = 10
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	a := &API{VMState: func(context.Context) string {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return "stopped"
	}}

	var wg sync.WaitGroup
	out := make([]*httptest.ResponseRecorder, n)
	for i := range out {
		wg.Add(1)
		go func() { defer wg.Done(); out[i] = getVMState(a, context.Background()) }()
	}
	<-started
	// Every request is now waiting on, or has started, the one call.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("hook calls = %d, want 1", calls.Load())
	}
	for _, w := range out {
		wantState(t, w, "stopped")
	}
}

// A waiter whose client went away returns at once; the call it was waiting on
// carries on for the others and its answer is cached.
func TestVMStateWaiterCancelDoesNotHangOrPoisonTheCache(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	a := &API{VMState: func(ctx context.Context) string {
		calls.Add(1)
		close(started)
		<-release
		if ctx.Err() != nil {
			return "" // a hook that saw a cancelled context gives no answer
		}
		return "stopped"
	}}

	first, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan struct{})
	go func() { defer close(firstDone); getVMState(a, first) }()
	<-started

	second := make(chan *httptest.ResponseRecorder, 1)
	go func() { second <- getVMState(a, context.Background()) }()
	time.Sleep(50 * time.Millisecond)

	cancelFirst() // the requester that started the call disconnects
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled waiter must return, not wait for the call")
	}
	close(release)
	wantState(t, <-second, "stopped")
	wantState(t, getVMState(a, context.Background()), "stopped")
	if calls.Load() != 1 {
		t.Fatalf("hook calls = %d, want 1", calls.Load())
	}
}

func TestVMStateRouteIsBehindAuth(t *testing.T) {
	s, plain := testServerWithAuth(t)
	var calls atomic.Int32
	s.API = &API{VMState: func(context.Context) string { calls.Add(1); return "stopped" }}
	h := s.Handler()

	r := httptest.NewRequest(http.MethodGet, "/api/vm-state", nil)
	r.Host = "example.com"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no credential: code = %d, want 401", w.Code)
	}
	if calls.Load() != 0 {
		t.Fatal("an unauthenticated request must not read the VM")
	}

	r = httptest.NewRequest(http.MethodGet, "/api/vm-state", nil)
	r.Host = "example.com"
	r.Header.Set("Authorization", "Bearer "+plain)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	wantState(t, w, "stopped")
}
