package web

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"
)

// vmStateTTL is how long an answer of the VM state hook is served again.
const vmStateTTL = 15 * time.Second

// vmStateCache serves the VM state hook's answer for vmStateTTL and runs one
// hook call at a time. The zero value is ready to use.
type vmStateCache struct {
	mu     sync.Mutex
	now    func() time.Time // nil = time.Now; a test seam
	val    string
	at     time.Time
	has    bool
	flight *vmStateFlight
}

// vmStateFlight is one hook call that callers share.
type vmStateFlight struct {
	done chan struct{} // closed once val is set
	val  string
}

// get returns the cached state, or the answer of the one call in flight, or
// starts that call. It returns ok=false only when ctx ends first (the caller's
// client is gone): the call itself carries on for the other waiters.
//
// The call runs on a context detached from every request (values kept, no
// cancellation): the first requester disconnecting must not hand the others a
// spurious answer that is then cached for vmStateTTL. The hook bounds itself
// (colima status has its own 30 s timeout), so the call cannot leak.
func (c *vmStateCache) get(ctx context.Context, hook func(context.Context) string) (string, bool) {
	c.mu.Lock()
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	if c.has && now().Sub(c.at) < vmStateTTL {
		v := c.val
		c.mu.Unlock()
		return v, true
	}
	f := c.flight
	if f == nil {
		f = &vmStateFlight{done: make(chan struct{})}
		c.flight = f
		go c.run(context.WithoutCancel(ctx), hook, f, now)
	}
	c.mu.Unlock()

	select {
	case <-f.done:
		return f.val, true
	case <-ctx.Done():
		return "", false
	}
}

func (c *vmStateCache) run(ctx context.Context, hook func(context.Context) string, f *vmStateFlight, now func() time.Time) {
	// A panicking hook must not take the process down from this goroutine, and
	// its waiters must be released: no answer is "" ("unknown").
	defer func() {
		if r := recover(); r != nil {
			log.Printf("vm-state hook panicked: %v", r)
		}
		c.mu.Lock()
		c.val, c.at, c.has = f.val, now(), true
		c.flight = nil
		c.mu.Unlock()
		close(f.done)
	}()
	f.val = hook(ctx)
}

// handleVMState serves GET /api/vm-state: {"state":"running"|"stopped"|""}.
// It is not a field of /api/meta because that is probed on load and must not
// wait for colima. "" = unknown or no VM; "stopped" is never shown on a guess.
func (a *API) handleVMState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	state := ""
	if a.VMState != nil {
		v, ok := a.vmCache.get(r.Context(), a.VMState)
		if !ok {
			return // the client is gone
		}
		state = v
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": state})
}
