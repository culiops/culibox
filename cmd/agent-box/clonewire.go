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

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/clone"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/session"
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

	file    string // <dataDir>/clone-dirs
	mu      sync.Mutex
	prepare func(context.Context, clone.Env, config.Clone, bool) (box.CloneMounts, bool, error) // swappable for tests

	// The spawn-time refresh. listBoxes is the docker-level listing of every
	// instance's boxes; refresh is clone.Refresh, swappable for tests.
	docker                   clone.Docker
	image, instance, dataDir string
	listBoxes                func(context.Context) ([]box.BoxInfo, error)
	refresh                  func(context.Context, clone.Git, clone.Docker, clone.RefreshRequest) clone.RefreshResult

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
	key := c.Dir
	if r, err := filepath.EvalSymlinks(c.Dir); err == nil {
		key = r
	}
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
	case h.listBoxes == nil:
		skip, listErr = clone.SkipOtherInstance, "cannot check whether another instance's box holds the clone: no box listing"
	default:
		boxes, err := h.listBoxes(ctx)
		if err != nil {
			skip = clone.SkipOtherInstance
			listErr = "cannot check whether another instance's box holds the clone: " + clone.Sanitize(err.Error())
		} else if holder, busy := box.OtherInstanceHolds(boxes, key, h.instance); busy {
			skip = clone.SkipOtherInstance
			log.Printf("clone refresh of %s skipped: container %s may be using it", key, clone.Sanitize(holder))
		}
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
	info := cloneInfoOf(h.refresh(rctx, h.env.Git, h.docker, clone.RefreshRequest{
		Clone: c, Image: h.image, Instance: h.instance, DataDir: h.dataDir, Skip: skip,
	}))
	if listErr != "" {
		if info.Error != "" {
			info.Error += "; "
		}
		info.Error += listErr
	}
	returned = true
	return info, release
}

// cloneInfoOf is the session's view of a refresh result.
func cloneInfoOf(r clone.RefreshResult) session.CloneInfo {
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
	}
}

// sweepCloneContainers removes this instance's refresh and `clone fetch`
// containers a crash or a kill left behind: either can hold a clone or a fetch
// directory mounted. It runs at boot, before any spawn. It returns how many it
// removed; failures are logged and the sweep goes on.
func sweepCloneContainers(ctx context.Context, list func(context.Context, string) ([]string, error), remove func(context.Context, string) error, instance string) int {
	n := 0
	for _, label := range []string{clone.LabelCloneFetch, clone.LabelCloneRefresh} {
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

// cloneSaveCheck is the save-time check of a clone profile's repo: the disk
// box-owned clone_of refusal, then the disk checks of clone.CheckProfile. h is nil
// where clone mode does not exist (not Linux), which leaves only CheckProfile.
func cloneSaveCheck(h *cloneHooks, home string, protected []box.ProtectedDir) func(config.Clone) error {
	return func(c config.Clone) error {
		if h != nil {
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
// files that box controls. h is nil where clone mode does not exist (not
// Linux), which leaves only CheckContextPath. shared is the box VM's shared
// directories, nil when there is no VM. It returns the identity
// box.CheckContextPathID recorded, which the spawn hands to the box layer.
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
// session fails. Clone mode is Linux-only, so a non-Linux host has no hooks.
func cloneUnavailableReason(goos string, h *cloneHooks) string {
	if goos != "linux" || h == nil {
		return "private copies need Linux for now"
	}
	return h.unavailable
}

// newCloneHooks builds the clone-mode hooks. Clone mode is Linux-only in
// Phase A, so it returns nil, nil elsewhere. On Linux it ALWAYS returns hooks:
// the host-tier box-owned guard (BoxOwned) needs neither the box layer nor git,
// and must stay on when docker is down or git is missing. When Prepare cannot
// run (no layer, no git) the hooks are still returned, with a non-nil error
// saying why, and Prepare refuses with box.ErrBoxUnavailable. memRoot is the
// config bundle source ("" with the bundle off), absolute when set. instance
// is this daemon's box instance label, which the refresh compares boxes by.
func newCloneHooks(goos string, layer *boxLayer, home, memRoot, dataDir, instance string, protected []box.ProtectedDir, clones func() []config.Clone) (*cloneHooks, error) {
	if goos != "linux" {
		return nil, nil
	}
	h := &cloneHooks{
		env:      clone.Env{Home: home, Protected: protected, MemoryRoot: memRoot, DataDir: dataDir},
		clones:   clones,
		file:     filepath.Join(dataDir, cloneDirsFile),
		prepare:  clone.Prepare,
		instance: instance,
		dataDir:  dataDir,
		refresh:  clone.Refresh,
	}
	if layer == nil {
		h.unavailable = "no containment layer"
		return h, errors.New(h.unavailable)
	}
	h.docker = clone.ExecDocker{Bin: layer.dockerBin}
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
