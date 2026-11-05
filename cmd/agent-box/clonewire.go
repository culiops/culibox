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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/culiops/culibox/internal/box"
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

	// shared is the box VM's shared directories (macOS), nil without a VM. The
	// VM's docker sees nothing else, so a clone_of or clone dir outside them is
	// refused (checkShared).
	shared []string

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

// returnModeOf is how a trigger returns a clone's branches: "now" forces, the
// profile's switch off only records what is not home, otherwise fast-forward only.
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
		return session.ReturnSummary{Trigger: trigger, At: time.Now(), Error: clone.Sanitize(err.Error())}
	}
	return returnSummaryOf(trigger, h.ret(ctx, h.env.Git, h.docker, clone.ReturnRequest{
		Clone: c, Mode: mode, Image: h.image, Instance: h.instance,
		TempDir: filepath.Join(h.dataDir, "clone-fetch"), Home: h.env.Home, DataDir: h.dataDir, Protected: h.env.Protected,
	}))
}

// returnSummaryOf is the session's view of a return result. The clone layer has
// already sanitized every string in it.
func returnSummaryOf(trigger string, r clone.ReturnResult) session.ReturnSummary {
	s := session.ReturnSummary{
		Trigger: trigger, At: r.At, Base: r.Base, Home: r.Home, Warnings: r.Warnings, Error: r.Error,
	}
	for _, n := range r.NotHome {
		s.NotHome = append(s.NotHome, session.NotHomeBranch{Branch: n.Branch, Reason: n.Reason, Detail: n.Detail})
	}
	return s
}

// ReturnWork brings a clone's branches home under the clone's lock, the same
// one a spawn's refresh holds, so the two never rewrite a clone's refs
// together. Except for "now" it skips while another boxed session of this
// instance uses the clone (asked once the lock is held); every trigger skips
// when a box of another instance may hold it. Never an error: the outcome,
// failures included, is in the summary.
func (h *cloneHooks) ReturnWork(ctx context.Context, c config.Clone, o session.ReturnOpts) session.ReturnSummary {
	sum := session.ReturnSummary{Trigger: o.Trigger, At: time.Now()}
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
	if o.Trigger != session.ReturnNow && o.LiveSession != nil && o.LiveSession() {
		sum.Skipped = clone.SkipInUse
		return sum
	}
	if skip, listErr := h.otherInstanceSkip(ctx, key); skip != "" {
		sum.Skipped, sum.Error = skip, listErr
		return sum
	}
	return h.doReturn(ctx, c, o.Trigger, returnModeOf(c, o.Trigger))
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
	if !ok || (len(st.NotHome) == 0 && st.Error == "") {
		return nil
	}
	out := &session.NotHomeState{At: st.At, Base: st.Base, Error: st.Error}
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
	return nil
}

// checkShared refuses a clone_of or clone dir the box VM does not share; with
// no VM (shared nil) docker sees the host's own paths and nothing is refused.
// The VM's docker resolves every mount inside the VM, as spelled, so a path
// outside the shares fails only at box start, after the clone was made, with
// docker's error as the only explanation. Both the spelling and its resolved
// form must be shared: a symlink inside a share may lead out of it.
func (h *cloneHooks) checkShared(c config.Clone) error {
	if h.shared == nil {
		return nil
	}
	for _, f := range []struct{ field, path string }{{"clone_of", c.Of}, {"default_cwd", c.Dir}} {
		real := f.path
		if r, err := filepath.EvalSymlinks(f.path); err == nil {
			real = r
		}
		if !box.UnderMount(f.path, h.shared) || !box.UnderMount(real, h.shared) {
			return fmt.Errorf("%w: %s %s is not under the box VM's shared directories (%s), so docker cannot see it; keep the repo and its copy under your home directory",
				box.ErrUnboxableWorkspace, f.field, f.path, strings.Join(h.shared, ", "))
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
// CheckContextPath. shared is the box VM's shared directories, nil when there
// is no VM. It returns the identity box.CheckContextPathID recorded, which the
// spawn hands to the box layer.
func contextSaveCheck(h *cloneHooks, home string, protected []box.ProtectedDir, shared []string) func(string) (*box.FileID, error) {
	return func(p string) (*box.FileID, error) {
		id, err := box.CheckContextPathID(p, home, protected, shared)
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
	if layer.vm != nil {
		h.shared = layer.vm.Config().Mounts()
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
	return h, nil
}
