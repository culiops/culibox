package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/boxedws"
	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/clone"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/session"
)

// cloneDirsFile is the durable record of every directory agent-box has made (or
// tried to make) a clone in, under the data dir. Box-ownership must outlive the
// profile that created the clone: editing the profile (dropping clone_of,
// changing default_cwd) or deleting it must not hand a box-written tree to a
// host-tier session. One absolute clean path per line, mode 0600. There is no
// release command: only removing the line releases a dir. Deleting the
// directory does not, because the box-owned check also covers paths that do not
// exist yet.
const cloneDirsFile = "clone-dirs"

// cloneHooks is the daemon's session.CloneHooks: it hands the session manager
// the clone package's Prepare and BoxOwned over the daemon's own paths. clones
// is read on every call, so a profile saved after boot is covered.
type cloneHooks struct {
	env    clone.Env
	clones func() []config.Clone

	// unavailable is why Prepare cannot run (no containment layer, no host git);
	// "" when it can. BoxOwned needs neither, so it always works.
	unavailable string

	// shares reads the box VM's live folder list (macOS), nil without a VM. The
	// VM's docker sees nothing else, so a clone_of or clone dir outside it is
	// refused (checkShared).
	shares box.SharesFunc

	// boxed is the record of boxed workspaces; nil (tests, a CLI run without a
	// data dir) knows no in-place history, so nothing is refused for it.
	boxed *boxedws.Registry

	file    string // <dataDir>/clone-dirs
	mu      sync.Mutex
	prepare func(context.Context, clone.Env, config.Clone, bool) (box.CloneMounts, bool, error) // swappable for tests

	// The spawn-time refresh. listBoxes is the docker-level listing of every
	// instance's boxes; refresh is clone.Refresh, swappable for tests.
	docker                   clone.Docker
	image, instance, dataDir string
	listBoxes                func(context.Context) ([]box.BoxInfo, error)
	refresh                  func(context.Context, clone.Git, clone.Docker, clone.RefreshRequest) clone.RefreshResult
	ret                      func(context.Context, clone.Git, clone.Docker, clone.ReturnRequest) clone.ReturnResult // clone.Return, swappable for tests

	// audit records an automatic merge into the host's checked-out branch (#370)
	// before it happens. nil refuses every merge (fail closed); main sets it to
	// the store audit.
	audit func(broker.CredEvent) error

	// handback records what a returned branch flagged for review (#346); nil
	// records nothing.
	handback *handbackHooks

	// locks holds one single-slot channel per resolved clone dir: a spawn holds
	// its clone's slot from before the refresh checks until its session is
	// published (or the spawn failed), so a second spawn of the same clone
	// waits and then sees the first one as a live session.
	lockMu sync.Mutex
	locks  map[string]chan struct{}
}

func (h *cloneHooks) Prepare(ctx context.Context, c config.Clone, withMemory bool) (box.CloneMounts, bool, error) {
	if h.unavailable != "" {
		return box.CloneMounts{}, false, fmt.Errorf("%w: clone mode is unavailable (%s)", box.ErrBoxUnavailable, h.unavailable)
	}
	if !usableDir(c.Dir) {
		return box.CloneMounts{}, false, fmt.Errorf("%w: clone dir %q is not an absolute clean path", box.ErrUnboxableWorkspace, c.Dir)
	}
	if err := h.checkShared(c); err != nil {
		return box.CloneMounts{}, false, err
	}
	// A clone_of the box can write is a host code-execution path (it plants
	// .git hooks the host's fetch then fires): refuse it before anything is
	// recorded or created.
	if err := h.checkOf(c.Of); err != nil {
		return box.CloneMounts{}, false, err
	}
	// Record BEFORE creating anything, so a crash between creation and record
	// cannot leave a box-written clone that nothing remembers. A record that
	// cannot be written fails the spawn.
	if err := h.record(c.Dir); err != nil {
		return box.CloneMounts{}, false, fmt.Errorf("%w: cannot record the clone dir in %s: %v", box.ErrBoxUnavailable, h.file, err)
	}
	return h.prepare(ctx, h.env, c, withMemory)
}

// lockClone takes the clone's lock, waiting at most until ctx is done; ok is
// false when it timed out. release frees the lock; a second call does nothing.
func (h *cloneHooks) lockClone(ctx context.Context, key string) (release func(), ok bool) {
	h.lockMu.Lock()
	if h.locks == nil {
		h.locks = map[string]chan struct{}{}
	}
	ch := h.locks[key]
	if ch == nil {
		ch = make(chan struct{}, 1)
		h.locks[key] = ch
	}
	h.lockMu.Unlock()
	select {
	case ch <- struct{}{}:
		return sync.OnceFunc(func() { <-ch }), true
	case <-ctx.Done():
		return func() {}, false
	}
}

// hostCheckTimeout bounds the host checks of a refresh skipped after its lock
// wait ran out the caller's deadline.
const hostCheckTimeout = 10 * time.Second

// Refresh refreshes the clone from host base before its box starts, unless a
// box may be using it: a live session of this instance (in-use, asked only
// once the clone's lock is held), a box of another instance or one whose
// labels cannot be trusted (other-instance), or a clone made just now
// (created). An idle box of this instance does not block it. A failed box
// listing fails closed as other-instance, with the error. The skipped cases
// still run the host-side checks. The returned release frees the clone's lock
// and is never nil; the caller calls it once its session is published or the
// spawn failed.
func (h *cloneHooks) Refresh(ctx context.Context, c config.Clone, o session.RefreshOpts) (session.CloneInfo, func()) {
	noop := func() {}
	if h.unavailable != "" {
		return session.CloneInfo{Error: fmt.Sprintf("clone mode is unavailable (%s)", h.unavailable)}, noop
	}
	key := cloneKey(c.Dir)
	release, locked := h.lockClone(ctx, key) // a no-op release when not locked
	// A panic below must not leave the clone locked for every later spawn. This
	// runs only when the function does not return normally, so the panic goes
	// on unrecovered.
	returned := false
	defer func() {
		if !returned {
			release()
		}
	}()

	var skip, listErr string
	switch {
	case !locked:
		skip = clone.SkipInUse
	case o.Created:
		skip = clone.SkipCreated
	case o.LiveSession != nil && o.LiveSession():
		skip = clone.SkipInUse
	default:
		skip, listErr = h.otherInstanceSkip(ctx, key)
	}

	// The lock wait may have used up the caller's deadline; the host checks of
	// a skipped refresh then run on a fresh, short one, never on the expired
	// context (its error text would reach the user).
	rctx := ctx
	if !locked {
		var cancel context.CancelFunc
		rctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), hostCheckTimeout)
		defer cancel()
	}
	// Work the box left in the clone comes home first, under the same lock, so
	// the refresh below sees it as host history. Only when the clone is about
	// to be used and nothing else holds it.
	var ret *session.ReturnSummary
	if skip == "" && o.ReturnBudget > 0 {
		bctx, cancel := context.WithTimeout(rctx, o.ReturnBudget)
		sum := h.doReturn(bctx, c, session.ReturnOnSpawn, returnModeOf(c, session.ReturnOnSpawn))
		cancel()
		ret = &sum
	}
	info := cloneInfoOf(h.refresh(rctx, h.env.Git, h.docker, clone.RefreshRequest{
		Clone: c, Image: h.image, Instance: h.instance, DataDir: h.dataDir, Skip: skip,
	}), ret)
	if listErr != "" {
		if info.Error != "" {
			info.Error += "; "
		}
		info.Error += listErr
	}
	returned = true
	return info, release
}

// cloneKey is what a clone's lock and its box workspace label go by: the dir
// with symlinks resolved, or as given when it cannot be.
func cloneKey(dir string) string {
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		return r
	}
	return dir
}

// otherInstanceSkip decides whether a box of another instance (or one whose
// labels cannot be trusted) may be using the clone. A failed listing fails
// closed, with the reason in listErr.
func (h *cloneHooks) otherInstanceSkip(ctx context.Context, key string) (skip, listErr string) {
	if h.listBoxes == nil {
		return clone.SkipOtherInstance, "cannot check whether another instance's box holds the clone: no box listing"
	}
	boxes, err := h.listBoxes(ctx)
	if err != nil {
		return clone.SkipOtherInstance, "cannot check whether another instance's box holds the clone: " + clone.Sanitize(err.Error())
	}
	if holder, busy := box.OtherInstanceHolds(boxes, key, h.instance); busy {
		log.Printf("clone %s skipped: container %s may be using it", key, clone.Sanitize(holder))
		return clone.SkipOtherInstance, ""
	}
	return "", ""
}

// cloneInfoOf is the session's view of a refresh result; ret is the
// spawn-time return that ran before it, nil when none did.
func cloneInfoOf(r clone.RefreshResult, ret *session.ReturnSummary) session.CloneInfo {
	return session.CloneInfo{
		Refreshed:     r.Refreshed,
		Skipped:       r.Skipped,
		Error:         r.Error,
		Base:          r.Base,
		Branch:        r.Branch,
		Ahead:         r.Report.Ahead,
		Behind:        r.Report.Behind,
		FFFrom:        r.Report.FFFrom,
		FFTo:          r.Report.FFTo,
		FFCount:       r.Report.FFCount,
		FFRefused:     r.Report.FFRefused,
		HeadInHost:    r.HeadInHost,
		StaleBranches: r.Report.Stale,
		Rewritten:     r.Rewritten,
		Unverified:    r.Unverified,
		Return:        ret,
	}
}

// sweepCloneContainers removes this instance's refresh, return-listing and
// `clone fetch` containers a crash or a kill left behind: each can hold a clone
// or a fetch directory mounted. It runs at boot, before any spawn. It returns how many it
// removed; failures are logged and the sweep goes on.
func sweepCloneContainers(ctx context.Context, list func(context.Context, string) ([]string, error), remove func(context.Context, string) error, instance string) int {
	n := 0
	for _, label := range []string{clone.LabelCloneFetch, clone.LabelCloneRefresh, clone.LabelCloneList} {
		names, err := list(ctx, label+"="+instance)
		if err != nil {
			log.Printf("clone: listing leftover %s containers: %s", label, clone.Sanitize(err.Error()))
			continue
		}
		for _, name := range names {
			if err := remove(ctx, name); err != nil {
				log.Printf("clone: removing leftover container %s: %s", clone.Sanitize(name), clone.Sanitize(err.Error()))
				continue
			}
			n++
		}
	}
	return n
}

// bootReconcileAndSweep is the box layer's boot pass (bootReconcile), then the
// sweep of this instance's leftover clone containers, on the same docker. On
// macOS both skip while the VM is stopped: no container runs in it then, and
// one left from before the stop holds nothing until the VM starts (every clone
// container is --rm, which docker also applies when it restarts).
func bootReconcileAndSweep(ctx context.Context, l *boxLayer, instance string) {
	if l.bootReconcile(ctx) {
		sctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if n := sweepCloneContainers(sctx, l.cli.ListByLabel, l.cli.Stop, instance); n > 0 {
			log.Printf("clone: removed %d leftover clone container(s)", n)
		}
	}
}

// HostOrigin reads the host repo's origin URL. `of` passed checkOf in Prepare,
// and is checked again here so this call never runs the host's git in a repo a
// box can write.
func (h *cloneHooks) HostOrigin(ctx context.Context, of string) (string, error) {
	if h.unavailable != "" || h.env.Git == nil {
		return "", fmt.Errorf("clone mode is unavailable (%s)", h.unavailable)
	}
	if err := h.checkOf(of); err != nil {
		return "", err
	}
	return clone.HostOriginURL(ctx, h.env.Git, of)
}

// EstimateCopy sizes a private copy of the repo of and checks the free space
// where dir would go (POST /api/clone/estimate). of gets the same checks
// Prepare gives it first: not box-owned (checkOf), then a real, plain repo
// (clone.CheckOf). Nothing is read before they pass. The estimate itself is
// copyEstimate's: host git runs in of only when no box ever ran there.
func (h *cloneHooks) EstimateCopy(ctx context.Context, of, dir string) (clone.Estimate, error) {
	if h.unavailable != "" || h.env.Git == nil {
		return clone.Estimate{}, fmt.Errorf("%w: clone mode is unavailable (%s)", box.ErrBoxUnavailable, h.unavailable)
	}
	if !usableDir(of) || !usableDir(dir) {
		return clone.Estimate{}, fmt.Errorf("%w: the repo and the copy folder must be absolute clean paths", box.ErrUnboxableWorkspace)
	}
	if err := h.checkOf(of); err != nil {
		return clone.Estimate{}, err
	}
	if err := clone.CheckOf(of, h.env.Home, h.env.Protected); err != nil {
		return clone.Estimate{}, err
	}
	return h.copyEstimate(ctx, of, dir)
}

// copyEstimate is the copy estimate of both the dialog (EstimateCopy) and the
// first-copy disk check of Prepare (clone.Env.Estimate). checkOf lets a folder
// with an unknown in-place history through (the save asks the user about it),
// and the dialog asks for its estimate before that answer: so host git runs in
// of only when the boxed-workspace record knows no box ran there
// (StatusNone). Otherwise the estimate is the .git walk alone, partial, with
// no tree size (#371 S1).
func (h *cloneHooks) copyEstimate(ctx context.Context, of, dir string) (clone.Estimate, error) {
	g := h.env.Git
	if h.boxed != nil {
		if st, _ := h.boxed.InPlaceStatus(of); st != boxedws.StatusNone {
			g = nil
		}
	}
	return clone.EstimateCopy(ctx, g, of, dir)
}

// returnModeOf is how a trigger returns a clone's branches: "now" forces, the
// profile's switch off only records what is not home (every other trigger,
// idle and host included), otherwise fast-forward only.
func returnModeOf(c config.Clone, trigger string) clone.ReturnMode {
	switch {
	case trigger == session.ReturnNow:
		return clone.ReturnForced
	case c.NoAutoReturn:
		return clone.ReturnDry
	}
	return clone.ReturnAuto
}

// doReturn runs one return with the clone's lock already held. Host git runs in
// clone_of, so a clone_of that has become box-owned since the spawn (another
// profile's clone, a dir now in the clone-dirs record, or a record that cannot
// be read) is refused before anything runs: a box could have planted hooks or
// config there. A refused return runs nothing, so the state file is left as it
// was.
func (h *cloneHooks) doReturn(ctx context.Context, c config.Clone, trigger string, mode clone.ReturnMode) session.ReturnSummary {
	if err := h.checkOf(c.Of); err != nil {
		return session.ReturnSummary{Trigger: trigger, At: time.Now(), Profile: c.Profile, Error: clone.Sanitize(err.Error())}
	}
	s := returnSummaryOf(trigger, c.Of, h.handback, h.ret(ctx, h.env.Git, h.docker, clone.ReturnRequest{
		Clone: c, Mode: mode, Image: h.image, Instance: h.instance,
		TempDir: filepath.Join(h.dataDir, "clone-fetch"), Home: h.env.Home, DataDir: h.dataDir, Protected: h.env.Protected,
		MergeAudit: h.mergeAudit(c.Profile),
	}))
	s.Profile = c.Profile
	return s
}

// mergeAudit is the request's audit hook for one profile: it writes the row
// the merge needs before it moves the host's branch. Without an audit sink it
// returns an error, so no merge happens unaudited.
func (h *cloneHooks) mergeAudit(profile string) func(action, detail string) error {
	return func(action, detail string) error {
		if h.audit == nil {
			return errors.New("no audit store")
		}
		return h.audit(broker.CredEvent{Action: action, Kind: "clone", Name: profile, Actor: "culibox", Detail: detail})
	}
}

// mergeNoteOf is the session's independent copy of a merge result, nil for nil.
func mergeNoteOf(m *clone.MergeResult) *session.MergeNote {
	if m == nil {
		return nil
	}
	return &session.MergeNote{
		At: m.At, Branch: m.Branch, From: m.From, To: m.To, Merged: m.Merged, Commits: m.Commits, Files: m.Files,
		Reason: m.Reason, Detail: m.Detail, Paths: slices.Clone(m.Paths),
	}
}

// returnSummaryOf is the session's view of a return result. The clone layer has
// already sanitized every string in it. Each branch that came home has its
// handback report merged into its record (a zero-flag one too, so an earlier
// record of the branch is brought up to date) and, when it flagged anything,
// is listed in Flagged. hb nil records nothing.
func returnSummaryOf(trigger, of string, hb *handbackHooks, r clone.ReturnResult) session.ReturnSummary {
	s := session.ReturnSummary{
		Trigger: trigger, At: r.At, Base: r.Base, Home: r.Home, Warnings: r.Warnings, Error: r.Error,
		UncommittedCut: r.UncommittedCut, Merge: mergeNoteOf(r.Merge),
	}
	if r.Uncommitted != nil {
		n := *r.Uncommitted
		s.Uncommitted = &n
	}
	for _, n := range r.NotHome {
		s.NotHome = append(s.NotHome, session.NotHomeBranch{Branch: n.Branch, Reason: n.Reason, Detail: n.Detail})
	}
	flagged, warns := hb.flaggedOf(of, r.Handback)
	s.Flagged = flagged
	s.Warnings = append(s.Warnings[:len(s.Warnings):len(s.Warnings)], warns...)
	return s
}

// ReturnWork brings a clone's branches home under the clone's lock, the same
// one a spawn's refresh holds, so the two never rewrite a clone's refs
// together. Only an "exit" return skips while another boxed session of this
// instance uses the clone (asked once the lock is held): idle, host and "now"
// returns are fast-forward only or forced and run beside a live session. Every
// trigger skips when a box of another instance may hold it. Never an error: the outcome,
// failures included, is in the summary.
func (h *cloneHooks) ReturnWork(ctx context.Context, c config.Clone, o session.ReturnOpts) session.ReturnSummary {
	sum := session.ReturnSummary{Trigger: o.Trigger, At: time.Now(), Profile: c.Profile}
	if h.unavailable != "" {
		sum.Skipped = "unavailable"
		return sum
	}
	key := cloneKey(c.Dir)
	release, locked := h.lockClone(ctx, key)
	defer release()
	if !locked {
		sum.Skipped = clone.SkipInUse
		return sum
	}
	if o.Trigger == session.ReturnOnExit && o.LiveSession != nil && o.LiveSession() {
		sum.Skipped = clone.SkipInUse
		return sum
	}
	if skip, listErr := h.otherInstanceSkip(ctx, key); skip != "" {
		sum.Skipped, sum.Error = skip, listErr
		return sum
	}
	return h.doReturn(ctx, c, o.Trigger, returnModeOf(c, o.Trigger))
}

// ReturnForRepo brings home the clones whose clone_of is cwd or holds it, before
// a Host session starts there, one summary each in Dir order. Paths compare with
// symlinks resolved. Profile-less entries (the fallback ones for an invalid
// config) have no profile to return for and are skipped. The ctx is one budget
// for the lot: once it is done the clones it did not reach get a "deferred"
// summary, and every clone the budget cut short (skipped as deferred, with a
// branch not home as deferred, or, once the budget is done, one that ended in
// an error or skipped in-use: a check or a lock wait the deadline cut) is
// continued in the background, so the spawn never waits on it. A clone whose
// directory does not exist yet (no box has been spawned) has nothing to return
// and gets no summary. A box of another instance or a docker that is down is
// ReturnWork's "skipped" summary, never a block.
func (h *cloneHooks) ReturnForRepo(ctx context.Context, cwd string) []session.ReturnSummary {
	here := cloneKey(cwd)
	var match []config.Clone
	for _, c := range h.clones() {
		if c.Profile == "" || !pathWithin(here, cloneKey(c.Of)) {
			continue
		}
		// The copy exists only after the profile's first box spawn: with no
		// directory there is nothing to return, and a return would only record
		// an error against a clone that was never made.
		if st, err := os.Lstat(c.Dir); err != nil || !st.IsDir() {
			continue
		}
		match = append(match, c)
	}
	slices.SortStableFunc(match, func(a, b config.Clone) int { return strings.Compare(a.Dir, b.Dir) })
	var sums []session.ReturnSummary
	var later []config.Clone
	for _, c := range match {
		var s session.ReturnSummary
		cut := false
		if ctx.Err() != nil {
			s = session.ReturnSummary{Trigger: session.ReturnOnHost, At: time.Now(), Profile: c.Profile, Skipped: clone.ReasonDeferred}
		} else {
			s = h.ReturnWork(ctx, c, session.ReturnOpts{Trigger: session.ReturnOnHost})
			s.Profile = c.Profile
			// A deadline that cut a listing or host check (an Error) or a lock
			// wait (in-use) left work unchecked: continue it like a deferred one.
			cut = ctx.Err() != nil && (s.Error != "" || s.Skipped == clone.SkipInUse)
		}
		if cut || returnDeferred(s) {
			later = append(later, c)
		}
		sums = append(sums, s)
	}
	if len(later) > 0 {
		go func() {
			for _, c := range later {
				bctx, cancel := context.WithTimeout(context.Background(), session.BackgroundReturnTimeout)
				s := h.ReturnWork(bctx, c, session.ReturnOpts{Trigger: session.ReturnOnHost})
				cancel()
				log.Printf("clone %s: the return continued after a host spawn: %d branch(es) home, %d not home, skipped %q",
					c.Profile, len(s.Home), len(s.NotHome), s.Skipped)
			}
		}()
	}
	return sums
}

// returnDeferred reports that a return was cut short by time: it was skipped as
// deferred, or a branch is not home for that reason.
func returnDeferred(s session.ReturnSummary) bool {
	if s.Skipped == clone.ReasonDeferred {
		return true
	}
	for _, n := range s.NotHome {
		if n.Reason == clone.ReasonDeferred {
			return true
		}
	}
	return false
}

// pathWithin reports whether p is dir or below it, by whole components.
func pathWithin(p, dir string) bool {
	if p == dir {
		return true
	}
	return strings.HasPrefix(p, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator))
}

// NotHome is what the clone's return state says did not come home, for the
// profile row. nil: no state, or nothing to show. A state that cannot be read
// is shown as an error rather than hidden.
func (h *cloneHooks) NotHome(c config.Clone) *session.NotHomeState {
	if h.dataDir == "" {
		return nil
	}
	st, ok, err := clone.ReadReturnState(h.dataDir, c.Dir)
	if err != nil {
		return &session.NotHomeState{Error: "cannot read what is not home: " + clone.Sanitize(err.Error())}
	}
	uncommitted := st.Uncommitted != nil && *st.Uncommitted > 0
	if !ok || (len(st.NotHome) == 0 && st.Error == "" && !uncommitted && st.Merge == nil) {
		return nil
	}
	out := &session.NotHomeState{At: st.At, Base: st.Base, Error: st.Error, Merge: mergeNoteOf(st.Merge)}
	if uncommitted {
		n := *st.Uncommitted
		out.Uncommitted, out.UncommittedCut = &n, st.UncommittedCut
	}
	for _, n := range st.NotHome {
		out.Branches = append(out.Branches, session.NotHomeBranch{Branch: n.Branch, Reason: n.Reason, Detail: n.Detail})
	}
	return out
}

// BoxOwned checks the live profiles' clones plus every recorded dir. An
// unreadable or corrupt record fails closed: everything is reported owned.
func (h *cloneHooks) BoxOwned(cwd string) (string, bool) {
	dirs, err := h.recorded()
	if err != nil {
		return fmt.Sprintf("the clone-dir record %s cannot be read (%v); refusing a host session at %s in case it is inside a clone (fix or delete the file)",
			h.file, err, cwd), true
	}
	all := h.clones()
	for _, d := range dirs {
		all = append(all, config.Clone{Dir: d})
	}
	return clone.BoxOwned(cwd, all)
}

// CopyDirOf is the clone directory (resolved) that real, an already resolved path,
// is at or under: a positive match against the live profiles' clones and the
// recorded dirs, never "owned by default". An unreadable or corrupt record
// contributes nothing, so the answer is no (#371).
func (h *cloneHooks) CopyDirOf(real string) (string, bool) {
	all := h.clones()
	if dirs, err := h.recorded(); err == nil {
		for _, d := range dirs {
			all = append(all, config.Clone{Dir: d})
		}
	}
	return clone.CopyDirOf(real, all)
}

// checkOf refuses a clone_of that is box-owned: another profile's clone dir, a
// dir in the durable clone-dirs record, or anything inside one. The owning box
// can plant .git/hooks or core.hooksPath there, so a host git run in it (the
// clone's origin read, `clone fetch`) would execute box-written code. The same
// ownership logic as the host-tier guard; a corrupt record fails closed.
func (h *cloneHooks) checkOf(of string) error {
	if why, owned := h.BoxOwned(of); owned {
		return fmt.Errorf("%w: clone_of %s is box-owned (%s); a clone must be made from a host-owned repo",
			box.ErrUnboxableWorkspace, of, why)
	}
	// A folder boxed in place while it was not a git repo may hold a .git the box
	// wrote: host git (clone, fetch, origin) would run against it (#371). That
	// "nongit" status is refused here. An entry with no flag (recorded before the
	// flag existed) is asked about at profile save, for a new or changed
	// clone_of only, so a clone profile accepted under the old rules keeps
	// spawning; but its config may be box-written too, and a promisor remote
	// declared there would make the copy's git clone or the bundle import, the
	// two calls that read clone_of through a transport, run a command it names
	// on a git without GIT_NO_LAZY_FETCH. So there, and only there, a partial
	// clone is refused. A folder no box ran in ("none") is the user's own: a
	// partial clone of theirs is a working clone_of.
	if h.boxed != nil {
		switch st, entry := h.boxed.InPlaceStatus(of); st {
		case boxedws.StatusNonGit:
			return fmt.Errorf("%w: clone_of %s: %s", box.ErrUnboxableWorkspace, of, nonGitRelease(of, entry, h.boxed.Path()))
		case boxedws.StatusUnknown:
			if why := clone.PartialCloneWhy(of); why != "" {
				return fmt.Errorf("%w: clone_of %s %s, and %s was boxed in place before CuliBox recorded whether it was a git repo, "+
					"so a box may have written that config; a partial clone there cannot be the source of a private copy",
					box.ErrUnboxableWorkspace, of, why, entry)
			}
		}
	}
	return nil
}

// nonGitRelease says why a clone_of inside a folder boxed in place without git
// is refused, and the one way to release it: the entry's git_guarded set to true
// by hand. Never "delete the entry": the entry is also what keeps the web file
// API contained in that folder.
func nonGitRelease(of, entry, file string) string {
	where := "this folder was"
	if entry != of {
		where = "it is inside " + entry + ", which was"
	}
	return fmt.Sprintf("%s boxed in place when it was not a git repo, so a .git there may be box-written. "+
		"If you are sure the .git in %s is your own, not a box's: stop CuliBox, set %q to true under \"git_guarded\" in %s, and start it again",
		where, of, entry, file)
}

// checkShared refuses a clone_of or clone dir the box VM does not share; with
// no VM (shares nil) docker sees the host's own paths and nothing is refused.
// The VM's docker resolves every mount inside the VM, as spelled, so a path
// outside the shares fails only at box start, after the clone was made, with
// docker's error as the only explanation. Both the spelling and its resolved
// form must be shared: a symlink inside a share may lead out of it. The clone
// is only read (a read-only share suffices); its copy is written.
func (h *cloneHooks) checkShared(c config.Clone) error {
	if h.shares == nil {
		return nil
	}
	view, err := h.shares()
	if err != nil {
		return fmt.Errorf("%w: cannot read the box VM's folder list (%v); run `culibox doctor`", box.ErrUnboxableWorkspace, err)
	}
	for _, f := range []struct {
		path  string
		write bool
	}{{c.Of, false}, {c.Dir, true}} {
		real := f.path
		if r, err := filepath.EvalSymlinks(f.path); err == nil {
			real = r
		}
		for _, p := range []string{f.path, real} {
			if err := view.Check(p, f.write); err != nil {
				return err
			}
		}
	}
	return nil
}

// cloneSaveCheck is the save-time check of a clone profile's repo: the VM
// share check and the disk box-owned clone_of refusal, then the disk checks of
// clone.CheckProfile. h is nil only in tests, which leaves only CheckProfile.
func cloneSaveCheck(h *cloneHooks, home string, protected []box.ProtectedDir) func(config.Clone) error {
	return func(c config.Clone) error {
		if h != nil {
			if err := h.checkShared(c); err != nil {
				return err
			}
			if err := h.checkOf(c.Of); err != nil {
				return err
			}
		}
		return clone.CheckProfile(c, home, protected)
	}
}

// contextSaveCheck is the save-time (and spawn-time) check of one context_paths
// entry: the disk checks of box.CheckContextPath, then the box-owned refusal.
// A clone is written by its box, so another box's context would be read from
// files that box controls. h is nil only in tests, which leaves only
// CheckContextPath. shares reads the box VM's live folder list, nil when there
// is no VM. It returns the identity box.CheckContextPathID recorded, which the
// spawn hands to the box layer.
func contextSaveCheck(h *cloneHooks, home string, protected []box.ProtectedDir, shares box.SharesFunc) func(string) (*box.FileID, error) {
	return func(p string) (*box.FileID, error) {
		var view *box.ShareView
		if shares != nil {
			v, err := shares()
			if err != nil {
				return nil, fmt.Errorf("%w: context_paths %s: cannot read the box VM's folder list (%v); run `culibox doctor`", box.ErrUnboxableWorkspace, p, err)
			}
			view = &v
		}
		id, err := box.CheckContextPathID(p, home, protected, view)
		if err != nil {
			return nil, err
		}
		if h != nil {
			if why, owned := h.BoxOwned(p); owned {
				return nil, fmt.Errorf("context_paths %s is inside a box-owned directory (%s); another box writes there", p, why)
			}
		}
		return id, nil
	}
}

// usableDir reports whether d is an absolute clean path with no control
// character (a newline would split a record line).
func usableDir(d string) bool {
	return filepath.IsAbs(d) && filepath.Clean(d) == d &&
		!strings.ContainsFunc(d, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// recorded reads the record. A missing file is an empty record; a symlink at
// the path, a read error or a malformed line is an error.
func (h *cloneHooks) recorded() ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.readLocked()
}

func (h *cloneHooks) readLocked() ([]string, error) {
	f, err := os.OpenFile(h.file, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if !usableDir(line) {
			return nil, fmt.Errorf("malformed line %q", line)
		}
		out = append(out, line)
	}
	return out, sc.Err()
}

// record adds dir to the record if absent, by writing a fresh 0600 file beside
// it and renaming over it (the rename replaces, never follows, a symlink at the
// final path).
func (h *cloneHooks) record(dir string) error {
	if !usableDir(dir) {
		return fmt.Errorf("%q is not an absolute clean path without control characters", dir)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	dirs, err := h.readLocked()
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if d == dir {
			return nil
		}
	}
	dirs = append(dirs, dir)
	tmp, err := os.CreateTemp(filepath.Dir(h.file), cloneDirsFile+".tmp-*")
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(strings.Join(dirs, "\n") + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), h.file); err != nil {
		return err
	}
	ok = true
	return nil
}

// cloneUnavailableReason is why clone mode cannot run on this instance, or ""
// when it can. Published in /api/meta so the profile editor can say so before a
// session fails. The daemon always has hooks; nil only reads as unavailable.
func cloneUnavailableReason(h *cloneHooks) string {
	if h == nil {
		return "clone mode is not set up on this instance"
	}
	return h.unavailable
}

// cloneEstimateFunc is the hooks' copy estimate for the web server; nil when
// there are no hooks, which leaves the route unregistered.
func cloneEstimateFunc(h *cloneHooks) func(context.Context, string, string) (clone.Estimate, error) {
	if h == nil {
		return nil
	}
	return h.EstimateCopy
}

// newCloneHooks builds the clone-mode hooks, on Linux and macOS alike: what
// differs is the box layer's (its docker flags and VM shares). It ALWAYS
// returns hooks: the host-tier box-owned guard (BoxOwned) needs neither the
// box layer nor git, and must stay on when docker is down or git is missing.
// When Prepare cannot run (no layer, no git) the hooks are still returned, with
// a non-nil error saying why, and Prepare refuses with box.ErrBoxUnavailable.
// memRoot is the config bundle source ("" with the bundle off), absolute when
// set. instance is this daemon's box instance label, which the refresh
// compares boxes by.
func newCloneHooks(layer *boxLayer, home, memRoot, dataDir, instance string, protected []box.ProtectedDir, clones func() []config.Clone) (*cloneHooks, error) {
	h := &cloneHooks{
		env:      clone.Env{Home: home, Protected: protected, MemoryRoot: memRoot, DataDir: dataDir},
		clones:   clones,
		file:     filepath.Join(dataDir, cloneDirsFile),
		prepare:  clone.Prepare,
		instance: instance,
		dataDir:  dataDir,
		refresh:  clone.Refresh,
		ret:      clone.Return,
	}
	if layer == nil {
		h.unavailable = "no containment layer"
		return h, errors.New(h.unavailable)
	}
	// The layer's global flags: on macOS they pin every clone container to the
	// box VM's docker, where the clone is shared; without them a call reaches
	// the user's own daemon.
	h.docker = clone.ExecDocker{Bin: layer.dockerBin, Args: layer.dockerArgs}
	h.shares = layer.sharesFunc()
	if h.shares != nil {
		// The memory folder lives under ~/.claude, which the VM may not share.
		h.env.Visible = func(host string) bool {
			view, err := h.shares()
			if err != nil {
				return false
			}
			if r, err := filepath.EvalSymlinks(host); err == nil {
				host = r
			}
			return view.Check(host, false) == nil
		}
	}
	h.image = layer.image
	cli := layer.cli
	h.listBoxes = func(ctx context.Context) ([]box.BoxInfo, error) {
		return cli.ListBoxes(ctx, box.LabelInstance) // the key alone: every instance
	}
	g, err := clone.NewExecGit()
	if err != nil {
		h.unavailable = err.Error()
		return h, err
	}
	h.env.Git = g
	h.env.Estimate = h.copyEstimate
	return h, nil
}
