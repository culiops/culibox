package gitview

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/culiops/agent-box/internal/clone"
)

const (
	defaultTimeout     = 10 * time.Second
	defaultReuse       = 2 * time.Second
	defaultIdle        = 5 * time.Minute
	defaultMaxParallel = 2

	maxStatusEntries = 5000
	// maxStatusBytes bounds the porcelain buffer. Hitting it means far more than
	// maxStatusEntries paths, so it reads as "toomany".
	maxStatusBytes = 8 << 20
	maxDiffBytes   = 2 << 20
	maxReasonRunes = 200
	removeTimeout  = 10 * time.Second
)

// Status is the tree's change set for one repository. State is one of ok,
// notrepo, toomany, unavailable; Entries is never nil.
type Status struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	Top    string `json:"top,omitempty"`
	// RootRel is where the requested tree root sits inside Top (slash form,
	// "." for Top itself). The web handler sets it per request; the runner
	// and its cache, shared by every root of one checkout, leave it empty.
	RootRel string  `json:"rootRel"`
	NoHead  bool    `json:"noHead,omitempty"`
	Entries []Entry `json:"entries"`
}

// Diff is one file's diff. State is ok or unavailable; Kind is text, binary or unchanged.
type Diff struct {
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Diff      string `json:"diff"`
	Truncated bool   `json:"truncated"`
}

// Runner runs git status and diff in throwaway containers. Zero durations and
// limits take the defaults (10s timeout, 2s reuse, 5m idle, 2 parallel). It is
// safe for concurrent use; its mutex is never held while docker runs.
type Runner struct {
	Docker               clone.Docker
	Image                string
	UID, GID             int
	Timeout, Reuse, Idle time.Duration
	MaxParallel          int
	Now                  func() time.Time

	mu      sync.Mutex
	cache   map[string]*cached
	flights map[string]*flight
	semOnce sync.Once
	sem     chan struct{}
}

type cached struct {
	st       Status
	at       time.Time // when the run finished
	lastUsed time.Time
}

// flight is one status run that every concurrent caller for a Top waits on.
type flight struct {
	done chan struct{}
	st   Status
}

func orDur(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) semaphore() chan struct{} {
	r.semOnce.Do(func() {
		n := r.MaxParallel
		if n <= 0 {
			n = defaultMaxParallel
		}
		r.sem = make(chan struct{}, n)
	})
	return r.sem
}

// Status returns the change set of t. Concurrent calls for the same Top share
// one container run, and a finished result is reused for Reuse. A refused
// target, a docker failure or a timeout is State "unavailable".
func (r *Runner) Status(ctx context.Context, t Target) Status {
	if why := t.Unsafe(); why != "" {
		return unavailableStatus(t, why)
	}
	key := t.key()
	now := r.now()
	r.mu.Lock()
	r.sweepLocked(now)
	if c := r.cache[key]; c != nil && now.Sub(c.at) < orDur(r.Reuse, defaultReuse) {
		c.lastUsed = now
		st := cloneStatus(c.st)
		r.mu.Unlock()
		return st
	}
	f := r.flights[key]
	if f == nil {
		f = &flight{done: make(chan struct{})}
		if r.flights == nil {
			r.flights = map[string]*flight{}
		}
		r.flights[key] = f
		// The run belongs to the flight, not to the caller that started it: its
		// own context, so one caller leaving does not fail the others.
		go r.runStatus(t, key, f)
	}
	r.mu.Unlock()
	select {
	case <-f.done:
		return cloneStatus(f.st)
	case <-ctx.Done():
		return unavailableStatus(t, "request cancelled")
	}
}

// key identifies a repository for the cache and single-flight: Root, Top and
// Common, since two resolutions of one Top may name different common git dirs
// and a boxed target mounts differently from a host one.
func (t Target) key() string { return t.Root + "\x00" + t.Top + "\x00" + t.Common }

// sweepLocked drops cache entries nothing has asked for within Idle.
func (r *Runner) sweepLocked(now time.Time) {
	idle := orDur(r.Idle, defaultIdle)
	for k, c := range r.cache {
		if now.Sub(c.lastUsed) >= idle {
			delete(r.cache, k)
		}
	}
}

func (r *Runner) runStatus(t Target, key string, f *flight) {
	st := r.doStatus(t)
	now := r.now()
	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]*cached{}
	}
	r.cache[key] = &cached{st: st, at: now, lastUsed: now}
	delete(r.flights, key)
	r.mu.Unlock()
	f.st = st
	close(f.done)
}

func (r *Runner) doStatus(t Target) Status {
	ctx, cancel := context.WithTimeout(context.Background(), orDur(r.Timeout, defaultTimeout))
	defer cancel()
	release, err := r.acquire(ctx)
	if err != nil {
		return unavailableStatus(t, "timed out waiting for a free git container")
	}
	defer release()
	name := newName()
	out := &capBuffer{max: maxStatusBytes}
	err = r.Docker.RunStream(ctx, StatusArgs(t, r.Image, name, r.UID, r.GID), out)
	if err != nil {
		// Killing the docker CLI does not stop the container, so remove it on
		// every error, even one that still yields a result (the output cap).
		r.remove(name)
	}
	switch {
	case out.hit || errors.Is(err, errCapped):
		return Status{State: "toomany", Top: t.Top, Entries: []Entry{}}
	case err != nil:
		if strings.Contains(err.Error(), "not a git repository") {
			return Status{State: "notrepo", Top: t.Top, Entries: []Entry{}}
		}
		return unavailableStatus(t, failReason(ctx, err))
	}
	entries, noHead, tooMany := ParseStatus(out.b, maxStatusEntries)
	if tooMany {
		return Status{State: "toomany", Top: t.Top, Entries: []Entry{}}
	}
	if entries == nil {
		entries = []Entry{}
	}
	return Status{State: "ok", Top: t.Top, NoHead: noHead, Entries: entries}
}

// Diff returns the diff of one file, rel being its path relative to t.Top. It
// is not cached. Output past 2 MiB is cut and marked Truncated.
func (r *Runner) Diff(ctx context.Context, t Target, rel string, untracked bool) Diff {
	if why := t.Unsafe(); why != "" {
		return Diff{State: "unavailable", Reason: why}
	}
	if !safePath(rel) {
		return Diff{State: "unavailable", Reason: "invalid path"}
	}
	ctx, cancel := context.WithTimeout(ctx, orDur(r.Timeout, defaultTimeout))
	defer cancel()
	release, err := r.acquire(ctx)
	if err != nil {
		return Diff{State: "unavailable", Reason: "timed out waiting for a free git container"}
	}
	defer release()
	name := newName()
	out := &capBuffer{max: maxDiffBytes}
	err = r.Docker.RunStream(ctx, DiffArgs(t, r.Image, name, r.UID, r.GID, rel, untracked), out)
	if err != nil {
		// As in doStatus: remove the container on every error, result or not.
		r.remove(name)
	}
	if err != nil && !errors.Is(err, errCapped) && !(untracked && exitCode(err) == 1) {
		// A cut-short diff is a result, not a failure; so is exit 1 of --no-index,
		// which git uses to say "the files differ".
		return Diff{State: "unavailable", Reason: failReason(ctx, err)}
	}
	b := out.b
	if out.hit {
		b = trimPartialRune(b)
	}
	return Diff{State: "ok", Kind: DiffKind(b), Diff: string(b), Truncated: out.hit}
}

// acquire takes one of the MaxParallel slots, or fails when ctx ends first.
func (r *Runner) acquire(ctx context.Context) (release func(), err error) {
	sem := r.semaphore()
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// remove force-removes the named container on a fresh context, since the run's
// own is already done. docker rm -f of a container that is gone is not an error.
func (r *Runner) remove(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), removeTimeout)
	defer cancel()
	if err := r.Docker.Remove(ctx, name); err != nil {
		log.Printf("gitview: removing container %s: %s", name, failReasonText(err.Error()))
	}
}

func newName() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("gitview: crypto/rand: " + err.Error())
	}
	return "agent-box-git-" + hex.EncodeToString(b[:])
}

// exitCode is the container's exit status carried by err, or -1.
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func failReason(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "git timed out"
	}
	return failReasonText(err.Error())
}

func failReasonText(s string) string {
	s = strings.ToValidUTF8(strings.TrimSpace(s), "")
	if rs := []rune(s); len(rs) > maxReasonRunes {
		s = string(rs[len(rs)-maxReasonRunes:])
	}
	return s
}

func unavailableStatus(t Target, why string) Status {
	return Status{State: "unavailable", Reason: why, Top: t.Top, Entries: []Entry{}}
}

func cloneStatus(s Status) Status {
	s.Entries = slices.Clone(s.Entries)
	if s.Entries == nil {
		s.Entries = []Entry{}
	}
	return s
}

// errCapped is what capBuffer's Write returns once it is full; with a real
// ExecDocker a failing writer stops the container.
var errCapped = errors.New("output limit reached")

// capBuffer keeps at most max bytes and then refuses further writes.
type capBuffer struct {
	b   []byte
	max int
	hit bool
}

func (c *capBuffer) Write(p []byte) (int, error) {
	room := c.max - len(c.b)
	if len(p) <= room {
		c.b = append(c.b, p...)
		return len(p), nil
	}
	if room > 0 {
		c.b = append(c.b, p[:room]...)
	}
	c.hit = true
	return max(room, 0), errCapped
}

// trimPartialRune drops a character cut in half at the end of b.
func trimPartialRune(b []byte) []byte {
	for i := 1; i < utf8.UTFMax && i <= len(b); i++ {
		if utf8.RuneStart(b[len(b)-i]) {
			if !utf8.FullRune(b[len(b)-i:]) {
				return b[:len(b)-i]
			}
			return b
		}
	}
	return b
}
