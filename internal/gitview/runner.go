package gitview

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"os/exec"
	"regexp"
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
	defaultSlotWait    = 5 * time.Second
	// baseReuse is how long a base ref answer is fresh. Which ref is the base
	// does not depend on HEAD, but a fetch can retarget origin/HEAD or move the
	// ref. An older answer is still served at once while one refresh runs.
	baseReuse = 30 * time.Second
	// refRefreshDelay is how long that refresh waits before it asks for a slot,
	// so the requests that found the answer stale take theirs first.
	refRefreshDelay = 500 * time.Millisecond

	maxStatusEntries = 5000
	// maxIgnored is the most gitignored paths a status carries; past it the tree dims nothing. They are kept apart from
	// maxStatusEntries, so a repository that ignores a lot never turns the tree's status off.
	maxIgnored = 1000
	// maxStatusBytes bounds the porcelain buffer. Hitting it means far more than
	// maxStatusEntries paths, so it reads as "toomany".
	maxStatusBytes = 8 << 20
	// maxNumstatBytes bounds the diff --raw --numstat buffer, which holds each
	// path twice and some 100 bytes per file; hitting it reads as "toomany".
	maxNumstatBytes = 8 << 20
	maxDiffBytes    = 2 << 20
	// maxBlobBytes bounds the text of a file at a revision (the editor's own
	// text read cap); maxRawBlobBytes bounds the raw bytes of an image.
	maxBlobBytes    = 2 << 20
	maxRawBlobBytes = 10 << 20
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
	RootRel string `json:"rootRel"`
	// WorktreeOf is the main checkout of a linked worktree ("" for a main checkout, a submodule, a bare repository's
	// worktree). Like RootRel the web handler sets it per request, from the files git keeps; the runner leaves it empty.
	WorktreeOf string  `json:"worktreeOf,omitempty"`
	NoHead     bool    `json:"noHead,omitempty"`
	Entries    []Entry `json:"entries"`
	// Ignored is the gitignored paths (ParseIgnored), relative to Top, a trailing "/" for a folder: nil when there are
	// none, more than maxIgnored, or the output was cut. Only the tree's dimming reads it.
	Ignored []string `json:"ignored,omitempty"`
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
	Head      string `json:"head,omitempty"`
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
	RootRel string `json:"rootRel"`
	// Scope is the scope the entries are for, "head" or "branch": the web
	// handler sets it (for an "auto" request, the one it picked).
	Scope   string     `json:"scope,omitempty"`
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

// Blob is one file's content at a revision. State is one of ok, notrepo,
// unavailable. Missing marks an ok answer for a path the revision does not have
// (an untracked or new file, or any file in a repository with no commit yet),
// whose Content is empty. Content is cut at 2 MiB and marked Truncated. Reason
// carries git's stderr only for unavailable, so a web handler must not forward
// it as it is.
type Blob struct {
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
	Missing   bool   `json:"missing,omitempty"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

// Runner runs git status, diff, base, numstat and file-at-revision lookups in throwaway containers. Zero durations and
// limits take the defaults (10s timeout, 2s reuse, 5m idle, 2 parallel). It is
// safe for concurrent use; its mutex is never held while docker runs.
type Runner struct {
	Docker               clone.Docker
	Image                string
	UID, GID             int
	Timeout, Reuse, Idle time.Duration
	// SlotWait bounds how long a run waits for one of the MaxParallel slots;
	// its Timeout starts only once it has one. Zero is 5s.
	SlotWait    time.Duration
	MaxParallel int
	Now         func() time.Time
	// AfterFunc schedules SweepLater's sweep; nil is time.AfterFunc.
	AfterFunc func(time.Duration, func())

	mu           sync.Mutex
	cache        map[string]*cached
	flights      map[string]*flight
	refCache     map[string]*refCached
	refFlights   map[string]*refFlight
	baseCache    map[string]*baseCached
	baseFlights  map[string]*baseFlight
	numCache     map[string]*numCached
	numFlights   map[string]*numFlight
	slotMu       sync.Mutex
	slotInit     bool
	slotFree     int
	slotQueue    [3][]chan struct{} // waiters per prio, oldest first
	sweepPending bool
	sweepAgain   bool
}

type cached struct {
	st       Status
	at       time.Time // when the run finished
	started  time.Time // when the run launched its container (see flight)
	lastUsed time.Time
}

// refInfo is the base branch of a repository: State ok (Ref, and OID the
// commit it points at), nobase, notrepo or unavailable (Reason, git's stderr).
type refInfo struct {
	State, Reason, Ref, OID string
}

// refCached is a base ref answer for one repository (ok or nobase).
type refCached struct {
	ri       refInfo
	at       time.Time // when the run finished
	lastUsed time.Time
}

// refFlight is one for-each-ref run that every concurrent caller for a
// repository waits on.
type refFlight struct {
	done chan struct{}
	ri   refInfo
}

// baseCached is a merge base answer for one repository, HEAD, base ref and
// base ref commit: those fix it, so it is kept until nobody asks for Idle. Its
// Branch and Head are empty: every caller sets them from its own status.
type baseCached struct {
	bi       BaseInfo
	lastUsed time.Time
}

// baseFlight is one merge-base run that every concurrent caller for a key waits on.
type baseFlight struct {
	done chan struct{}
	bi   BaseInfo
}

// numCached is a numstat answer for one repository and scope.
type numCached struct {
	ns       Numstat
	at       time.Time // when the run finished
	started  time.Time // when the run launched its container (see numFlight)
	lastUsed time.Time
}

// numFlight is one numstat run that every concurrent caller for a key waits on.
// started and hasStarted are flight's: when its container launched (guarded by
// Runner.mu), before which the run has read nothing.
type numFlight struct {
	done       chan struct{}
	ns         Numstat
	started    time.Time
	hasStarted bool
}

// readsAfter reports whether the flight's result reflects the disk at or after
// t: it has not launched yet, or it launched at or after t.
func (f *numFlight) readsAfter(t time.Time) bool {
	return !f.hasStarted || !f.started.Before(t)
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

// prio orders the waiters for a slot: every prioList waiter is served before
// any prioView one, and each class first come, first served.
type prio int

const (
	// prioList is the tree's and the Changes list's git: status, numstat and
	// the base lookups.
	prioList prio = iota
	// prioView is one file's git for the editor: its diff and its text at a
	// revision (the gutter, the Changes view, All changes).
	prioView
	// prioIdle is work nobody waits for: refreshing a stale base ref.
	prioIdle
)

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
// statusJoinWait is how long StatusShared gives a status asked at the same
// time to start before it starts its own.
const statusJoinWait = 100 * time.Millisecond

// StatusShared is Status for a reader that needs a status about as new as its
// own, longer run (numstat's untracked files): with nothing reused or in
// flight, it first waits up to statusJoinWait for a status asked beside it (the
// client sends one with each numstat) and joins that one, since a fresh status
// that arrives after this one's run launched could not join it and would cost
// a second run. The wait is free for its caller, whose own run takes longer.
func (r *Runner) StatusShared(ctx context.Context, t Target) Status {
	key := t.key()
	for deadline := time.Now().Add(statusJoinWait); time.Now().Before(deadline); {
		r.mu.Lock()
		c, f := r.cache[key], r.flights[key]
		r.mu.Unlock()
		if f != nil || (c != nil && r.now().Sub(c.at) < orDur(r.Reuse, defaultReuse)) {
			break
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			return unavailableStatus(t, "request cancelled")
		}
	}
	return r.Status(ctx, t)
}

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
	for k, c := range r.baseCache {
		if now.Sub(c.lastUsed) >= idle {
			delete(r.baseCache, k)
		}
	}
	for k, c := range r.refCache {
		if now.Sub(c.lastUsed) >= idle {
			delete(r.refCache, k)
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
	ctx, done, err := r.slot(context.Background(), prioList)
	if err != nil {
		return unavailableStatus(t, errNoSlot.Error())
	}
	defer done()
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
	capped := out.hit || errors.Is(err, errCapped)
	ignored, seen := ParseIgnored(out.b, maxIgnored)
	switch {
	case capped && seen:
		// Cut inside the ignored records, which come last: every change is above the cut. The status stands; only the
		// dimming goes (the cut list is not the whole list).
		ignored = nil
	case capped:
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
	return Status{State: "ok", Top: t.Top, NoHead: noHead, Branch: branch, Head: head, Entries: entries, Ignored: ignored}
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
	ctx, done, err := r.slot(ctx, prioView)
	if err != nil {
		return Diff{State: "unavailable", Reason: errNoSlot.Error()}
	}
	defer done()
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

// missingRE is the end of what git cat-file says for a path the revision does
// not have: "does not exist in" for a path nobody has, "exists on disk, but not
// in" for the untracked file the mount does hold. It is anchored at the end so a
// path that merely contains those words (a directory, which ends the message in
// ": bad file") is not taken for one.
var missingRE = regexp.MustCompile(`(does not exist in|exists on disk, but not in) '(HEAD|[0-9a-f]{40}|[0-9a-f]{64})'\s*$`)

// noHeadRE is what git says for HEAD in a repository with no commit yet.
var noHeadRE = regexp.MustCompile(`invalid object name 'HEAD'\.?\s*$`)

// isMissing reports whether err is git (exit 128) saying that rev has no such
// path, or, for HEAD, that there is no HEAD to have one.
func isMissing(rev string, err error) bool {
	if exitCode(err) != 128 {
		return false
	}
	msg := strings.TrimSpace(err.Error())
	return missingRE.MatchString(msg) || (rev == "HEAD" && noHeadRE.MatchString(msg))
}

// Show returns the text of the file rel (relative to t.Top) as it is in rev,
// which must be "HEAD" or a full object id, such as the MergeBase of Base: the
// caller derives it, a client never names one. The blob is read as stored, with
// no filter or textconv. It is not cached, since the editor asks for it when it
// opens a file and again after the file or HEAD moved. Content past 2 MiB is
// cut at a whole character and marked Truncated. A path the revision does not
// have is ok and Missing. A refused target, rev or path, a docker failure or a
// timeout is "unavailable".
func (r *Runner) Show(ctx context.Context, t Target, rev, rel string) Blob {
	data, b := r.cat(ctx, t, rev, rel, maxBlobBytes)
	if b.Truncated {
		data = trimPartialRune(data)
	}
	b.Content = string(data)
	return b
}

// ShowRaw is Show for a file that is not text (an image): it returns the bytes
// as they are, up to 10 MiB, and a Blob that has everything but Content. Past
// the cap the bytes are cut and the Blob is Truncated, which no caller should
// serve as the file.
func (r *Runner) ShowRaw(ctx context.Context, t Target, rev, rel string) ([]byte, Blob) {
	return r.cat(ctx, t, rev, rel, maxRawBlobBytes)
}

// cat runs one cat-file container and returns the (at most limit) bytes it
// printed with a Blob that has everything but Content.
func (r *Runner) cat(ctx context.Context, t Target, rev, rel string, limit int) ([]byte, Blob) {
	if why := t.Unsafe(); why != "" {
		return nil, Blob{State: "unavailable", Reason: why}
	}
	if !validRev(rev) {
		return nil, Blob{State: "unavailable", Reason: "invalid revision"}
	}
	if !safePath(rel) {
		return nil, Blob{State: "unavailable", Reason: "invalid path"}
	}
	ctx, done, err := r.slot(ctx, prioView)
	if err != nil {
		return nil, Blob{State: "unavailable", Reason: errNoSlot.Error()}
	}
	defer done()
	name := newName()
	out := &capBuffer{max: limit}
	err = r.Docker.RunStream(ctx, ShowArgs(t, r.Image, name, r.UID, r.GID, rev, rel), out)
	// A path the revision lacks is git exiting on its own: the container is gone
	// (--rm), so removing it would be pure overhead on what is the common case
	// of opening a new file. Every other error (a cap hit, a timeout, a real
	// failure) may leave the container running, so it is removed.
	missing := err != nil && isMissing(rev, err)
	if err != nil && !missing {
		r.removeFailed(name)
	}
	switch {
	case out.hit || errors.Is(err, errCapped):
		return out.b, Blob{State: "ok", Truncated: true}
	case missing:
		return nil, Blob{State: "ok", Missing: true}
	case err != nil:
		if strings.Contains(err.Error(), "not a git repository") {
			return nil, Blob{State: "notrepo"}
		}
		return nil, Blob{State: "unavailable", Reason: failReason(ctx, err)}
	}
	return out.b, Blob{State: "ok"}
}

// Base returns the base branch of t, the merge base of HEAD with it, and the
// checked-out branch. The base is picked by the server from the repository's
// own refs (origin/HEAD's target, else main or master, remote before local):
// no caller names a ref. HEAD and the branch are the latest status's, at any
// age (a client asks for the base after a status that moved HEAD, and that
// status is the latest); only with no status at all does Base run one, beside
// the ref lookup. The ref is baseRef's; the merge base is kept per HEAD, ref
// and the ref's commit. Concurrent calls share each run. Only a settled answer
// (ok or nobase) is kept: a failure is asked again.
func (r *Runner) Base(ctx context.Context, t Target) BaseInfo {
	if why := t.Unsafe(); why != "" {
		return BaseInfo{State: "unavailable", Reason: why}
	}
	st, known := r.lastStatus(t)
	var refc chan refInfo
	if !known {
		refc = make(chan refInfo, 1) // the lookup may end unobserved
		go func() { refc <- r.baseRef(ctx, t) }()
		st = r.Status(ctx, t)
	}
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
	var ri refInfo
	if refc != nil {
		ri = <-refc
	} else {
		ri = r.baseRef(ctx, t)
	}
	var bi BaseInfo
	switch ri.State {
	case "ok":
		bi = r.mergeBase(ctx, t, st.Head, ri)
	case "nobase":
		bi = BaseInfo{State: "nobase", Reason: reasonNoBase}
	case "notrepo":
		return BaseInfo{State: "notrepo"}
	default:
		bi = BaseInfo{State: "unavailable", Reason: ri.Reason}
	}
	bi.Branch, bi.Head = st.Branch, st.Head
	return bi
}

// BaseRef returns the base branch of t alone (State ok with BaseRef and
// BaseName, nobase, notrepo or unavailable), what a Branch numstat needs: no
// status and no merge base is run for it.
func (r *Runner) BaseRef(ctx context.Context, t Target) BaseInfo {
	if why := t.Unsafe(); why != "" {
		return BaseInfo{State: "unavailable", Reason: why}
	}
	ri := r.baseRef(ctx, t)
	switch ri.State {
	case "ok":
		return BaseInfo{State: "ok", BaseRef: ri.Ref, BaseName: baseName(ri.Ref)}
	case "nobase":
		return BaseInfo{State: "nobase", Reason: reasonNoBase}
	case "notrepo":
		return BaseInfo{State: "notrepo"}
	}
	return BaseInfo{State: "unavailable", Reason: ri.Reason}
}

// lastStatus is the latest finished status of t, whatever its age, when it
// says which branch and commit (ok or toomany).
func (r *Runner) lastStatus(t Target) (Status, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.cache[t.key()]
	if c == nil || (c.st.State != "ok" && c.st.State != "toomany") {
		return Status{}, false
	}
	c.lastUsed = r.now()
	return c.st, true
}

// baseRef is the base ref of t. A cached answer is served at once; past
// baseReuse it also starts one refresh, which runs after refRefreshDelay at
// prioIdle, so it never holds back the requests that found it stale. With no
// answer, callers share one for-each-ref run.
func (r *Runner) baseRef(ctx context.Context, t Target) refInfo {
	key := t.key()
	now := r.now()
	r.mu.Lock()
	r.sweepLocked(now)
	if c := r.refCache[key]; c != nil {
		c.lastUsed = now
		ri := c.ri
		if now.Sub(c.at) >= baseReuse && r.refFlights[key] == nil {
			f := r.newRefFlightLocked(key)
			r.after(refRefreshDelay, func() { r.runRef(t, key, f, prioIdle) })
		}
		r.mu.Unlock()
		return ri
	}
	f := r.refFlights[key]
	if f == nil {
		f = r.newRefFlightLocked(key)
		go r.runRef(t, key, f, prioList)
	}
	r.mu.Unlock()
	select {
	case <-f.done:
		return f.ri
	case <-ctx.Done():
		return refInfo{State: "unavailable", Reason: "request cancelled"}
	}
}

func (r *Runner) newRefFlightLocked(key string) *refFlight {
	f := &refFlight{done: make(chan struct{})}
	if r.refFlights == nil {
		r.refFlights = map[string]*refFlight{}
	}
	r.refFlights[key] = f
	return f
}

// after runs fn once d has passed (AfterFunc, else time.AfterFunc).
func (r *Runner) after(d time.Duration, fn func()) {
	if r.AfterFunc != nil {
		r.AfterFunc(d, fn)
		return
	}
	time.AfterFunc(d, fn)
}

func (r *Runner) runRef(t Target, key string, f *refFlight, p prio) {
	ri := r.doRef(t, p)
	now := r.now()
	r.mu.Lock()
	if ri.State == "ok" || ri.State == "nobase" {
		if r.refCache == nil {
			r.refCache = map[string]*refCached{}
		}
		r.refCache[key] = &refCached{ri: ri, at: now, lastUsed: now}
	}
	if r.refFlights[key] == f {
		delete(r.refFlights, key)
	}
	r.mu.Unlock()
	f.ri = ri
	close(f.done)
}

// doRef lists the candidate refs in one container and picks the base.
func (r *Runner) doRef(t Target, p prio) refInfo {
	out, err := r.runGitAt(context.Background(), p, func(name string) []string { return BaseArgs(t, r.Image, name, r.UID, r.GID) })
	if err != nil {
		if strings.Contains(err.Error(), "not a git repository") {
			return refInfo{State: "notrepo"}
		}
		return refInfo{State: "unavailable", Reason: failReasonText(err.Error())}
	}
	ref, oid := pickBase(out)
	if ref == "" {
		return refInfo{State: "nobase"}
	}
	return refInfo{State: "ok", Ref: ref, OID: oid}
}

// mergeBase is the merge base of head with the base ref of ri, kept per
// (t, head, ref, ref commit). Its Branch and Head are empty.
func (r *Runner) mergeBase(ctx context.Context, t Target, head string, ri refInfo) BaseInfo {
	key := t.key() + "\x00" + head + "\x00" + ri.Ref + "\x00" + ri.OID
	now := r.now()
	r.mu.Lock()
	r.sweepLocked(now)
	if c := r.baseCache[key]; c != nil {
		c.lastUsed = now
		bi := c.bi
		r.mu.Unlock()
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
		go r.runMergeBase(t, key, head, ri.Ref, f)
	}
	r.mu.Unlock()
	select {
	case <-f.done:
		return f.bi
	case <-ctx.Done():
		return BaseInfo{State: "unavailable", Reason: "request cancelled"}
	}
}

func (r *Runner) runMergeBase(t Target, key, head, ref string, f *baseFlight) {
	bi := r.doMergeBase(t, head, ref)
	now := r.now()
	r.mu.Lock()
	if bi.State == "ok" || bi.State == "nobase" {
		if r.baseCache == nil {
			r.baseCache = map[string]*baseCached{}
		}
		r.baseCache[key] = &baseCached{bi: bi, lastUsed: now}
	}
	if r.baseFlights[key] == f {
		delete(r.baseFlights, key)
	}
	r.mu.Unlock()
	f.bi = bi
	close(f.done)
}

// doMergeBase asks git for the merge base of head and ref in one container.
func (r *Runner) doMergeBase(t Target, head, ref string) BaseInfo {
	out, err := r.runGit(context.Background(), func(name string) []string { return MergeBaseArgs(t, r.Image, name, r.UID, r.GID, head, ref) })
	if err != nil {
		// Exit 1 with nothing printed is merge-base's "no common ancestor".
		if exitCode(err) == 1 && len(out) == 0 {
			return BaseInfo{State: "nobase", Reason: reasonNoCommon}
		}
		return BaseInfo{State: "unavailable", Reason: failReasonText(err.Error())}
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
	return r.numstat(ctx, t, sc, false)
}

// NumstatFresh is Numstat for a caller that has just changed something on
// disk, with the semantics of StatusFresh: it never takes the reuse cache, and
// joins a run already in flight only if that run's container launched at or
// after this call (or has not launched yet), since an earlier run may have read
// the tree before the change. Fresh callers still share one new run among
// themselves, and the run still takes one of the MaxParallel slots.
func (r *Runner) NumstatFresh(ctx context.Context, t Target, sc Scope) Numstat {
	return r.numstat(ctx, t, sc, true)
}

func (r *Runner) numstat(ctx context.Context, t Target, sc Scope, fresh bool) Numstat {
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
	if c := r.numCache[key]; !fresh && c != nil && now.Sub(c.at) < orDur(r.Reuse, defaultReuse) {
		c.lastUsed = now
		ns := cloneNumstat(c.ns)
		r.mu.Unlock()
		return ns
	}
	f := r.numFlights[key]
	if f == nil || (fresh && !f.readsAfter(now)) {
		f = &numFlight{done: make(chan struct{})}
		if r.numFlights == nil {
			r.numFlights = map[string]*numFlight{}
		}
		// As for status, a newer flight replaces an older one still running,
		// and the run is the flight's, not the first caller's.
		r.numFlights[key] = f
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
	ns := r.doNumstat(t, sc, f)
	now := r.now()
	r.mu.Lock()
	if !f.hasStarted { // never got a slot
		f.started, f.hasStarted = now, true
	}
	if r.numCache == nil {
		r.numCache = map[string]*numCached{}
	}
	// A slower, older run must not replace the result of a newer one.
	if c := r.numCache[key]; c == nil || !c.started.After(f.started) {
		r.numCache[key] = &numCached{ns: ns, at: now, started: f.started, lastUsed: now}
	}
	if r.numFlights[key] == f {
		delete(r.numFlights, key)
	}
	r.mu.Unlock()
	f.ns = ns
	close(f.done)
}

func (r *Runner) doNumstat(t Target, sc Scope, f *numFlight) Numstat {
	name := newName()
	args := NumstatArgs(t, r.Image, name, r.UID, r.GID, sc)
	if args == nil { // NumstatArgs refused the ref: run nothing
		return unavailableNumstat("invalid base ref")
	}
	ctx, done, err := r.slot(context.Background(), prioList)
	if err != nil {
		return unavailableNumstat(errNoSlot.Error())
	}
	defer done()
	r.mu.Lock()
	f.started, f.hasStarted = r.now(), true
	r.mu.Unlock()
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

// errTimedOut is what runGit returns for a run that hit its Timeout.
var errTimedOut = errors.New("git timed out")

// runGit runs one list-priority git container (see slot) and returns what it
// printed, at most maxBaseBytes. argv builds the docker argv for the
// container's name; a nil one (a builder refusing its input) runs nothing. The
// container of a failed run is removed, as for status. A run that hit its
// Timeout is errTimedOut.
func (r *Runner) runGit(parent context.Context, argv func(name string) []string) ([]byte, error) {
	return r.runGitAt(parent, prioList, argv)
}

// runGitAt is runGit at priority p.
func (r *Runner) runGitAt(parent context.Context, p prio, argv func(name string) []string) ([]byte, error) {
	name := newName()
	args := argv(name)
	if args == nil {
		return nil, errRefused
	}
	ctx, done, err := r.slot(parent, p)
	if err != nil {
		return nil, errNoSlot
	}
	defer done()
	out := &capBuffer{max: maxBaseBytes}
	if err := r.Docker.RunStream(ctx, args, out); err != nil {
		r.removeFailed(name)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return out.b, errTimedOut
		}
		return out.b, err
	}
	return out.b, nil
}

// slot waits for one of the MaxParallel slots, for at most SlotWait and while
// parent lives, and then gives the run a context of its own Timeout, which
// starts now: time spent queueing never eats a run's budget. done cancels that
// context and frees the slot. A waiter that gives up takes no slot.
func (r *Runner) slot(parent context.Context, p prio) (ctx context.Context, done func(), err error) {
	wctx, wcancel := context.WithTimeout(parent, orDur(r.SlotWait, defaultSlotWait))
	err = r.acquire(wctx, p)
	wcancel()
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(parent, orDur(r.Timeout, defaultTimeout))
	return ctx, func() { cancel(); r.releaseSlot() }, nil
}

// acquire takes a slot, or fails when ctx ends first. With none free it queues
// in its class; releaseSlot hands a freed slot straight to the next waiter.
func (r *Runner) acquire(ctx context.Context, p prio) error {
	r.slotMu.Lock()
	if !r.slotInit {
		r.slotInit = true
		r.slotFree = r.MaxParallel
		if r.slotFree <= 0 {
			r.slotFree = defaultMaxParallel
		}
	}
	if r.slotFree > 0 {
		r.slotFree--
		r.slotMu.Unlock()
		return nil
	}
	w := make(chan struct{})
	r.slotQueue[p] = append(r.slotQueue[p], w)
	r.slotMu.Unlock()
	select {
	case <-w:
		return nil
	case <-ctx.Done():
	}
	r.slotMu.Lock()
	if i := slices.Index(r.slotQueue[p], w); i >= 0 {
		r.slotQueue[p] = slices.Delete(r.slotQueue[p], i, i+1)
		r.slotMu.Unlock()
		return ctx.Err()
	}
	r.slotMu.Unlock()
	// Granted while it was giving up: the slot goes to the next one.
	r.releaseSlot()
	return ctx.Err()
}

// releaseSlot gives a slot to the oldest waiter of the first class that has
// one, else back to the free count.
func (r *Runner) releaseSlot() {
	r.slotMu.Lock()
	defer r.slotMu.Unlock()
	for p := range r.slotQueue {
		if q := r.slotQueue[p]; len(q) > 0 {
			r.slotQueue[p] = q[1:]
			close(q[0])
			return
		}
	}
	r.slotFree++
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
	s.Ignored = slices.Clone(s.Ignored)
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
