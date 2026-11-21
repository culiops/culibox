package web

import (
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// rootHealthTTL is how long a root's last answer is trusted before a
	// request checks it again.
	rootHealthTTL = time.Second
	// rootHealthWait bounds how long a request waits on the checks it needs: a
	// check it started, or the first check of a root never answered yet.
	rootHealthWait = 200 * time.Millisecond
	// rootHealthHang is how long the re-check of a root last seen live may run
	// before the root counts as hung: a slow but alive mount stays served
	// (and keeps its search index) instead of flapping.
	rootHealthHang = 5 * time.Second
)

// rootIsLiveDir is the part of the fs policy's root rules that touches the
// root itself: it still resolves to its stored path (a box did not swap the
// folder for a link) and is a directory.
func rootIsLiveDir(root string) bool {
	real, err := filepath.EvalSymlinks(root)
	if err != nil || real != filepath.Clean(root) {
		return false
	}
	fi, err := os.Stat(real)
	return err == nil && fi.IsDir()
}

// rootHealth caches, per added workspace root, rootIsLiveDir: the only root
// rule of the fs policy and of the Settings list that makes a syscall on the
// root, and one that blocks for good on a hung network mount. The check runs in
// its own goroutine, at most one per root at a time, and only when the last
// answer is older than rootHealthTTL. A root last seen live keeps that answer
// while its re-check runs, and no request waits on it; only a re-check still
// running after rootHealthHang makes it "not live". Any other root (never
// answered, or last seen not live) is waited on for at most rootHealthWait in
// total, and a check that has not answered by then counts as "not live" until
// it does. Either way a hung root is dropped from the policy and shown not
// available, and no later request waits on it. A root never checked before may
// cost one request that wait. The zero value is ready.
type rootHealth struct {
	mu    sync.Mutex
	roots map[string]*rootCheck

	// For tests; zero means rootIsLiveDir, rootHealthTTL, rootHealthWait and
	// rootHealthHang.
	check           func(root string) bool
	ttl, wait, hang time.Duration
}

// rootCheck is one root's last answer and its running check, if any.
type rootCheck struct {
	ok, known bool      // known: a check answered, or one ran past the wait (ok false)
	at        time.Time // when the last check answered
	started   time.Time // when the running check started
	running   chan struct{}
}

// live reports, for each root, whether it is live now (see rootHealth).
func (h *rootHealth) live(roots []string) map[string]bool {
	ttl, wait, hang := h.ttl, h.wait, h.hang
	if ttl == 0 {
		ttl = rootHealthTTL
	}
	if wait == 0 {
		wait = rootHealthWait
	}
	if hang == 0 {
		hang = rootHealthHang
	}
	h.mu.Lock()
	if h.roots == nil {
		h.roots = map[string]*rootCheck{}
	}
	listed := make(map[string]bool, len(roots))
	for _, r := range roots {
		listed[r] = true
	}
	for r, c := range h.roots {
		// A root no longer listed is forgotten, unless its check still runs:
		// re-adding it must not start a second check on the same mount.
		if !listed[r] && c.running == nil {
			delete(h.roots, r)
		}
	}
	now := time.Now()
	var waits []chan struct{}
	for _, r := range roots {
		c := h.roots[r]
		if c == nil {
			c = &rootCheck{}
			h.roots[r] = c
		}
		switch {
		case c.running == nil && (!c.known || now.Sub(c.at) >= ttl):
			h.startLocked(r, c, now)
			if !c.ok { // a live root keeps its answer while it is re-checked
				waits = append(waits, c.running)
			}
		case c.running != nil && !c.known:
			waits = append(waits, c.running) // another request's first check
		}
	}
	h.mu.Unlock()

	if len(waits) > 0 {
		t := time.NewTimer(wait)
	wait:
		for _, ch := range waits {
			select {
			case <-ch:
			case <-t.C:
				break wait
			}
		}
		t.Stop()
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	now = time.Now()
	out := make(map[string]bool, len(roots))
	for _, r := range roots {
		c := h.roots[r]
		if c == nil {
			// A concurrent call over a shorter list forgot it between our two
			// locked phases: it is no longer a root.
			out[r] = false
			continue
		}
		// No answer in time: not live until it answers. A root last seen live
		// gets the hang threshold, any other the wait.
		if limit := wait; c.running != nil {
			if c.ok {
				limit = hang
			}
			if now.Sub(c.started) >= limit {
				c.ok, c.known = false, true
			}
		}
		out[r] = c.known && c.ok
	}
	return out
}

// startLocked runs one check of root in the background. The caller holds h.mu.
func (h *rootHealth) startLocked(root string, c *rootCheck, now time.Time) {
	check := h.check
	if check == nil {
		check = rootIsLiveDir
	}
	done := make(chan struct{})
	c.running, c.started = done, now
	go func() {
		ok := check(root)
		h.mu.Lock()
		c.ok, c.known, c.at, c.running = ok, true, time.Now(), nil
		h.mu.Unlock()
		close(done)
	}()
}
