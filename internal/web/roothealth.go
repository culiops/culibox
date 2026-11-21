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
// answer is older than rootHealthTTL. A request waits on the checks it needs
// for at most rootHealthWait in total; a check that has not answered by then
// counts as "not live" until it does, so a hung root is dropped from the
// policy and shown not available, and no later request waits on it. A root
// never checked before may cost one request that wait. The zero value is ready.
type rootHealth struct {
	mu    sync.Mutex
	roots map[string]*rootCheck

	// For tests; zero means rootIsLiveDir, rootHealthTTL and rootHealthWait.
	check     func(root string) bool
	ttl, wait time.Duration
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
	ttl, wait := h.ttl, h.wait
	if ttl == 0 {
		ttl = rootHealthTTL
	}
	if wait == 0 {
		wait = rootHealthWait
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
			waits = append(waits, c.running)
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
		if c.running != nil && now.Sub(c.started) >= wait {
			c.ok, c.known = false, true // no answer in time: not live until it answers
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
