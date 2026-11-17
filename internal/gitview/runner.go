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

	"github.com/culiops/culibox/internal/clone"
)

const (
	defaultTimeout     = 10 * time.Second
	defaultReuse       = 2 * time.Second
	defaultIdle        = 5 * time.Minute
	defaultMaxParallel = 2
	// baseReuse is how long a base answer is trusted for one HEAD. HEAD moving
	// is what usually changes it, but a fetch or a branch update can move the
	// base ref (and so the merge base) with HEAD where it is.
	baseReuse = 30 * time.Second

	maxStatusEntries = 5000
	// maxStatusBytes bounds the porcelain buffer. Hitting it means far more than
	// maxStatusEntries paths, so it reads as "toomany".
	maxStatusBytes = 8 << 20
	// maxNumstatBytes bounds the diff --raw --numstat buffer, which holds each
	// path twice and some 100 bytes per file; hitting it reads as "toomany".
	maxNumstatBytes = 8 << 20
	maxDiffBytes    = 2 << 20
	// maxBaseBytes bounds what the two base runs print: five ref records and one id.
	maxBaseBytes   = 64 << 10
	maxReasonRunes = 200
	removeTimeout  = 10 * time.Second
)

// ReasonNoImage is the reason of an unavailable result when docker has no
// local copy of the box image (the run uses --pull=never). It is a fixed
// string, not docker's stderr, so a caller can recognise it and tell the user
// what to do.
const ReasonNoImage = "box image not present locally"

// Status is the tree's change set for one repository. State is one of ok,
// notrepo, toomany, unavailable; Entries is never nil.
type Status struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	Top    string `json:"top,omitempty"`
	// Branch is the checked-out branch, "" for a detached HEAD; Head is the
	// commit HEAD is at, "" for a repository with no commits.
	Branch string `json:"branch,omitempty"`
	Head   string `json:"head,omitempty"`
	// RootRel is where the requested tree root sits inside Top (slash form,
	// "." for Top itself). The web handler sets it per request; the runner
	// and its cache, shared by every root of one checkout, leave it empty.
	RootRel string  `json:"rootRel"`
	NoHead  bool    `json:"noHead,omitempty"`
	Entries []Entry `json:"entries"`
}

// BaseInfo is what branch-scoped review compares against. State is one of ok,
// nobase (no base branch found, no commits, or no history in common with it),
// notrepo, unavailable. For ok, BaseRef is the full ref the server picked,
// BaseName its short form for display ("main" or "origin/main"), MergeBase the
// merge base of HEAD and BaseRef, and Ahead whether HEAD is past it. Reason
// carries git's stderr only for unavailable, so a web handler must not forward
// it as it is. Branch is the checked-out branch, "" when detached.
type BaseInfo struct {
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
	Branch    string `json:"branch"`
	BaseRef   string `json:"baseRef,omitempty"`
	BaseName  string `json:"baseName,omitempty"`
	MergeBase string `json:"mergeBase,omitempty"`
	Ahead     bool   `json:"ahead"`
}

// The reasons of a nobase result.
const (
	reasonNoCommits = "no commits yet"
	reasonNoBase    = "no base branch found"
	reasonNoCommon  = "no history in common with the base branch"
)

// NumEntry is one file's line counts against the scope's side: P relative to
// the checkout top, X the status letter (A, M or D; a type change is M), or "U"
// for an untracked file, which the web layer counts since git's diff has none.
// Bin marks a binary file, whose counts are 0.
type NumEntry struct {
	P   string `json:"p"`
	X   string `json:"x"`
	A   int    `json:"a"`
	D   int    `json:"d"`
	Bin bool   `json:"bin,omitempty"`
}

// Numstat is the per-file line counts of one repository's tracked changes.
// State is one of ok, notrepo, toomany, unavailable (a repository with no
// commits has no HEAD to diff, so it is unavailable); Entries is never nil.
// Reason carries git's stderr only for unavailable, so a web handler must not
// forward it as it is.
type Numstat struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	// RootRel is where the requested tree root sits inside the checkout top, as
	// for Status: the web handler sets it, the runner leaves it empty.
	RootRel string     `json:"rootRel"`
	Entries []NumEntry `json:"entries"`
}

// Diff is one file's diff. State is ok or unavailable; Kind is text, binary or unchanged.
type Diff struct {
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Diff      string `json:"diff"`
	Truncated bool   `json:"truncated"`
}

// Runner runs git status, diff, base and numstat lookups in throwaway containers. Zero durations and
// limits take the defaults (10s timeout, 2s reuse, 5m idle, 2 parallel). It is
// safe for concurrent use; its mutex is never held while docker runs.
type Runner struct {
	Docker               clone.Docker
	Image                string
	UID, GID             int
	Timeout, Reuse, Idle time.Duration
	MaxParallel          int
	Now                  func() time.Time
	// AfterFunc schedules SweepLater's sweep; nil is time.AfterFunc.
	AfterFunc func(time.Duration, func())

	mu           sync.Mutex
	cache        map[string]*cached
	flights      map[string]*flight
	baseCache    map[string]*baseCached
	baseFlights  map[string]*baseFlight
	numCache     map[string]*numCached
	numFlights   map[string]*numFlight
	semOnce      sync.Once
	sem          chan struct{}
	sweepPending bool
	sweepAgain   bool
}

type cached struct {
	st       Status
	at       time.Time // when the run finished
	started  time.Time // when the run launched its container (see flight)
	lastUsed time.Time
}

// baseCached is a base answer for one repository at one HEAD. Its Branch is
// empty: the branch can change with HEAD where it is, so every caller reads it
// from its own status.
type baseCached struct {
	bi       BaseInfo
	at       time.Time // when the run finished
	lastUsed time.Time
}

// baseFlight is one base run that every concurrent caller for a key waits on.
type baseFlight struct {
	done chan struct{}
	bi   BaseInfo
}

// numCached is a numstat answer for one repository and scope.
type numCached struct {
	ns       Numstat
	at       time.Time // when the run finished
	lastUsed time.Time
}

// numFlight is one numstat run that every concurrent caller for a key waits on.
type numFlight struct {
	done chan struct{}
	ns   Numstat
}

// flight is one status run that every concurrent caller for a Top waits on.
// started is when its container launched (guarded by Runner.mu); until then
// the run has read nothing, so even a fresh request may still join it.
type flight struct {
	done       chan struct{}
	st         Status
	started    time.Time
	hasStarted bool
}

// readsAfter reports whether the flight's result reflects the disk at or after
// t: it has not launched yet, or it launched at or after t.
func (f *flight) readsAfter(t time.Time) bool {
	return !f.hasStarted || !f.started.Before(t)
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
	return r.status(ctx, t, false)
}

// StatusFresh is Status for a caller that has just changed something on disk:
// it never takes the reuse cache, and joins a run already in flight only if
// that run's container launched at or after this call (or has not launched
// yet), since an earlier run may have read the tree before the change. Fresh
// callers still share one new run among themselves, and the run still takes one
// of the MaxParallel slots. A fresh call that arrives after another fresh run
// launched starts its own: a run is only shared by requests that precede its
// launch.
func (r *Runner) StatusFresh(ctx context.Context, t Target) Status {
	return r.status(ctx, t, true)
}

func (r *Runner) status(ctx context.Context, t Target, fresh bool) Status {
	if why := t.Unsafe(); why != "" {
		return unavailableStatus(t, why)
	}
	key := t.key()
	now := r.now()
	r.mu.Lock()
	r.sweepLocked(now)
	if c := r.cache[key]; !fresh && c != nil && now.Sub(c.at) < orDur(r.Reuse, defaultReuse) {
		c.lastUsed = now
		st := cloneStatus(c.st)
		r.mu.Unlock()
		return st
	}
	f := r.flights[key]
	if f == nil || (fresh && !f.readsAfter(now)) {
		f = &flight{done: make(chan struct{})}
		if r.flights == nil {
			r.flights = map[string]*flight{}
		}
		// A newer flight replaces an older one still running: later callers
		// join the newest, and the older one only answers its own waiters.
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
	for k, c := range r.numCache {
		if now.Sub(c.lastUsed) >= idle {
			delete(r.numCache, k)
		}
	}
	// A base answer past baseReuse is never served again: only idle ones go.
	for k, c := range r.baseCache {
		if now.Sub(c.lastUsed) >= idle || now.Sub(c.at) >= baseReuse {
			delete(r.baseCache, k)
		}
	}
}

func (r *Runner) runStatus(t Target, key string, f *flight) {
	st := r.doStatus(t, f)
	now := r.now()
	r.mu.Lock()
	if !f.hasStarted { // never got a slot
		f.started, f.hasStarted = now, true
	}
	if r.cache == nil {
		r.cache = map[string]*cached{}
	}
	// A slower, older run must not replace the result of a newer one.
	if c := r.cache[key]; c == nil || !c.started.After(f.started) {
		r.cache[key] = &cached{st: st, at: now, started: f.started, lastUsed: now}
	}
	if r.flights[key] == f {
		delete(r.flights, key)
	}
	r.mu.Unlock()
	f.st = st
	close(f.done)
}

func (r *Runner) doStatus(t Target, f *flight) Status {
	ctx, cancel := context.WithTimeout(context.Background(), orDur(r.Timeout, defaultTimeout))
	defer cancel()
	release, err := r.acquire(ctx)
	if err != nil {
		return unavailableStatus(t, "timed out waiting for a free git container")
	}
	defer release()
	r.mu.Lock()
	f.started, f.hasStarted = r.now(), true
	r.mu.Unlock()
	name := newName()
	out := &capBuffer{max: maxStatusBytes}
	err = r.Docker.RunStream(ctx, StatusArgs(t, r.Image, name, r.UID, r.GID), out)
	if err != nil {
		// Killing the docker CLI does not stop the container, so remove it on
		// every error, even one that still yields a result (the output cap).
		r.removeFailed(name)
	}
	switch {
	case out.hit || errors.Is(err, errCapped):
		// Max 0 stops at the first entry, after the header records, which
		// still say which branch and commit this is.
		_, _, _, branch, head := ParseStatus(out.b, 0)
		return Status{State: "toomany", Top: t.Top, Branch: branch, Head: head, Entries: []Entry{}}
	case err != nil:
		if strings.Contains(err.Error(), "not a git repository") {
			return Status{State: "notrepo", Top: t.Top, Entries: []Entry{}}
		}
		return unavailableStatus(t, failReason(ctx, err))
	}
	entries, noHead, tooMany, branch, head := ParseStatus(out.b, maxStatusEntries)
	if tooMany {
		return Status{State: "toomany", Top: t.Top, Branch: branch, Head: head, Entries: []Entry{}}
	}
	if entries == nil {
		entries = []Entry{}
	}
	return Status{State: "ok", Top: t.Top, NoHead: noHead, Branch: branch, Head: head, Entries: entries}
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
	// Exit 1 of --no-index with output is git saying "the files differ": the
	// container has already exited and --rm removes it, so a docker rm -f is
	// pure overhead. Every other error (a cap hit, a timeout, a real failure)
	// may leave the container running, so it is removed.
	differs := untracked && exitCode(err) == 1 && len(out.b) > 0 && !out.hit
	if err != nil && !differs {
		r.removeFailed(name)
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

// Base returns the base branch of t, the merge base of HEAD with it, and the
// checked-out branch. The base is picked by the server from the repository's
// own refs (origin/HEAD's target, else main or master, remote before local):
// no caller names a ref. HEAD and the branch come from Status, which Base
// calls, so they are as fresh as it is. The answer is kept per (t, HEAD), so a
// new commit recomputes it, and for baseReuse at most, since the base ref can
// move on its own. Concurrent calls share one run. Only a settled answer (ok
// or nobase) is kept: a failure is asked again.
func (r *Runner) Base(ctx context.Context, t Target) BaseInfo {
	if why := t.Unsafe(); why != "" {
		return BaseInfo{State: "unavailable", Reason: why}
	}
	st := r.Status(ctx, t)
	switch st.State {
	case "ok", "toomany": // both carry the branch and head
	case "notrepo":
		return BaseInfo{State: "notrepo"}
	default:
		return BaseInfo{State: "unavailable", Reason: st.Reason}
	}
	if st.Head == "" { // nothing to take a merge base of
		return BaseInfo{State: "nobase", Reason: reasonNoCommits, Branch: st.Branch}
	}
	key := t.key() + "\x00" + st.Head
	now := r.now()
	r.mu.Lock()
	r.sweepLocked(now)
	if c := r.baseCache[key]; c != nil && now.Sub(c.at) < baseReuse {
		c.lastUsed = now
		bi := c.bi
		r.mu.Unlock()
		bi.Branch = st.Branch
		return bi
	}
	f := r.baseFlights[key]
	if f == nil {
		f = &baseFlight{done: make(chan struct{})}
		if r.baseFlights == nil {
			r.baseFlights = map[string]*baseFlight{}
		}
		r.baseFlights[key] = f
		// As for status, the run is the flight's, not the first caller's.
		go r.runBase(t, key, st.Head, f)
	}
	r.mu.Unlock()
	select {
	case <-f.done:
		bi := f.bi
		bi.Branch = st.Branch
		return bi
	case <-ctx.Done():
		return BaseInfo{State: "unavailable", Reason: "request cancelled", Branch: st.Branch}
	}
}

func (r *Runner) runBase(t Target, key, head string, f *baseFlight) {
	bi := r.doBase(t, head)
	now := r.now()
	r.mu.Lock()
	if bi.State == "ok" || bi.State == "nobase" {
		if r.baseCache == nil {
			r.baseCache = map[string]*baseCached{}
		}
		r.baseCache[key] = &baseCached{bi: bi, at: now, lastUsed: now}
	}
	if r.baseFlights[key] == f {
		delete(r.baseFlights, key)
	}
	r.mu.Unlock()
	f.bi = bi
	close(f.done)
}

// doBase lists the candidate refs, then, for the one it picks, asks for the
// merge base: two containers, the second only when there is a base. Its
// Branch is empty (see baseCached).
func (r *Runner) doBase(t Target, head string) BaseInfo {
	ctx, cancel := context.WithTimeout(context.Background(), orDur(r.Timeout, defaultTimeout))
	defer cancel()
	out, err := r.runGit(ctx, func(name string) []string { return BaseArgs(t, r.Image, name, r.UID, r.GID) })
	if err != nil {
		return BaseInfo{State: "unavailable", Reason: failReason(ctx, err)}
	}
	ref := pickBase(out)
	if ref == "" {
		return BaseInfo{State: "nobase", Reason: reasonNoBase}
	}
	out, err = r.runGit(ctx, func(name string) []string { return MergeBaseArgs(t, r.Image, name, r.UID, r.GID, ref) })
	if err != nil {
		// Exit 1 with nothing printed is merge-base's "no common ancestor".
		if exitCode(err) == 1 && len(out) == 0 {
			return BaseInfo{State: "nobase", Reason: reasonNoCommon}
		}
		return BaseInfo{State: "unavailable", Reason: failReason(ctx, err)}
	}
	mb := strings.TrimSpace(string(out))
	if !oidRE.MatchString(mb) {
		return BaseInfo{State: "unavailable", Reason: "git printed no merge base"}
	}
	return BaseInfo{State: "ok", BaseRef: ref, BaseName: baseName(ref), MergeBase: mb, Ahead: mb != head}
}

// Numstat returns the per-file line counts of t's tracked changes against sc:
// HEAD, or for Branch the merge base with sc.BaseRef. Untracked files are not
// in it. Concurrent calls for the same repository and scope share one container
// run, and a finished result is reused for Reuse, in a cache of its own beside
// Status's. A Branch scope needs a BaseRef that is a ValidBaseRef (the server's
// own, from Base): any other is "unavailable" with no run. A refused target, a
// docker failure, a timeout or a repository with no commits is "unavailable".
func (r *Runner) Numstat(ctx context.Context, t Target, sc Scope) Numstat {
	if why := t.Unsafe(); why != "" {
		return unavailableNumstat(why)
	}
	key := t.key() + "\x00head"
	if sc.Branch {
		if !ValidBaseRef(sc.BaseRef) {
			return unavailableNumstat("invalid base ref")
		}
		key = t.key() + "\x00branch\x00" + sc.BaseRef
	} else {
		sc.BaseRef = "" // never read outside Branch
	}
	now := r.now()
	r.mu.Lock()
	r.sweepLocked(now)
	if c := r.numCache[key]; c != nil && now.Sub(c.at) < orDur(r.Reuse, defaultReuse) {
		c.lastUsed = now
		ns := cloneNumstat(c.ns)
		r.mu.Unlock()
		return ns
	}
	f := r.numFlights[key]
	if f == nil {
		f = &numFlight{done: make(chan struct{})}
		if r.numFlights == nil {
			r.numFlights = map[string]*numFlight{}
		}
		r.numFlights[key] = f
		// As for status, the run is the flight's, not the first caller's.
		go r.runNumstat(t, sc, key, f)
	}
	r.mu.Unlock()
	select {
	case <-f.done:
		return cloneNumstat(f.ns)
	case <-ctx.Done():
		return unavailableNumstat("request cancelled")
	}
}

func (r *Runner) runNumstat(t Target, sc Scope, key string, f *numFlight) {
	ns := r.doNumstat(t, sc)
	now := r.now()
	r.mu.Lock()
	if r.numCache == nil {
		r.numCache = map[string]*numCached{}
	}
	r.numCache[key] = &numCached{ns: ns, at: now, lastUsed: now}
	if r.numFlights[key] == f {
		delete(r.numFlights, key)
	}
	r.mu.Unlock()
	f.ns = ns
	close(f.done)
}

func (r *Runner) doNumstat(t Target, sc Scope) Numstat {
	name := newName()
	args := NumstatArgs(t, r.Image, name, r.UID, r.GID, sc)
	if args == nil { // NumstatArgs refused the ref: run nothing
		return unavailableNumstat("invalid base ref")
	}
	ctx, cancel := context.WithTimeout(context.Background(), orDur(r.Timeout, defaultTimeout))
	defer cancel()
	release, err := r.acquire(ctx)
	if err != nil {
		return unavailableNumstat(errNoSlot.Error())
	}
	defer release()
	out := &capBuffer{max: maxNumstatBytes}
	err = r.Docker.RunStream(ctx, args, out)
	if err != nil {
		// As for status: remove the container on every error, even one that
		// still yields a result (the output cap).
		r.removeFailed(name)
	}
	switch {
	case out.hit || errors.Is(err, errCapped):
		return Numstat{State: "toomany", Entries: []NumEntry{}}
	case err != nil:
		if strings.Contains(err.Error(), "not a git repository") {
			return Numstat{State: "notrepo", Entries: []NumEntry{}}
		}
		return unavailableNumstat(failReason(ctx, err))
	}
	entries, tooMany := ParseNumstat(out.b, maxStatusEntries)
	if tooMany {
		return Numstat{State: "toomany", Entries: []NumEntry{}}
	}
	if entries == nil {
		entries = []NumEntry{}
	}
	return Numstat{State: "ok", Entries: entries}
}

var (
	// errNoSlot is what runGit returns when ctx ends before a slot is free.
	errNoSlot = errors.New("timed out waiting for a free git container")
	// errRefused is what it returns when the argv builder refused.
	errRefused = errors.New("git argv refused")
)

// runGit runs one git container under a MaxParallel slot and returns what it
// printed, at most maxBaseBytes. argv builds the docker argv for the
// container's name; a nil one (a builder refusing its input) runs nothing. The
// container of a failed run is removed, as for status.
func (r *Runner) runGit(ctx context.Context, argv func(name string) []string) ([]byte, error) {
	name := newName()
	args := argv(name)
	if args == nil {
		return nil, errRefused
	}
	release, err := r.acquire(ctx)
	if err != nil {
		return nil, errNoSlot
	}
	defer release()
	out := &capBuffer{max: maxBaseBytes}
	if err := r.Docker.RunStream(ctx, args, out); err != nil {
		r.removeFailed(name)
		return out.b, err
	}
	return out.b, nil
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

// removeFailed removes the container of a failed run, and schedules a sweep
// for the case the rm cannot see: a docker CLI killed while the daemon was
// still creating the container, which then appears with nobody to start it.
func (r *Runner) removeFailed(name string) {
	r.remove(name)
	r.SweepLater(SweepDelay)
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
	return namePrefix + hex.EncodeToString(b[:])
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
	if strings.Contains(s, "No such image") || strings.Contains(s, "Unable to find image") {
		return ReasonNoImage
	}
	s = strings.ToValidUTF8(strings.TrimSpace(s), "")
	if rs := []rune(s); len(rs) > maxReasonRunes {
		s = string(rs[len(rs)-maxReasonRunes:])
	}
	return s
}

func unavailableStatus(t Target, why string) Status {
	return Status{State: "unavailable", Reason: why, Top: t.Top, Entries: []Entry{}}
}

func unavailableNumstat(why string) Numstat {
	return Numstat{State: "unavailable", Reason: why, Entries: []NumEntry{}}
}

func cloneNumstat(n Numstat) Numstat {
	n.Entries = slices.Clone(n.Entries)
	if n.Entries == nil {
		n.Entries = []NumEntry{}
	}
	return n
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
