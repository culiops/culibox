package box

// The mount side of kept folders: a box created for a profile gets the
// folders of the profile's store (keptstore.go) read-write at their paths in
// its home, and a record of the box's store key and of what CuliBox mounts in
// it, which the keep pass reads at the box's stop (keptpass.go).
//
// ensure runs all of it under opMu, while the box has no container. A keep
// pass takes the store's lock and then opMu, so nothing here takes the store's
// lock (that would deadlock) or writes state.json: state.json is read without
// the lock (it is replaced whole by a rename, so a read sees one version), and
// what the state must learn (whose store it is, the kept folders left out)
// goes in the box record, which the box's next keep pass folds in under the
// lock.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Reasons a kept folder is not mounted into a box, as they are shown.
const (
	keptReasonInMount    = "is inside a folder CuliBox mounts from the host"
	keptReasonMountpoint = "mountpoint not plain"
)

// keptOwner is who a spawn with a profile is for: its conversations key
// hashes these with the workspace (ConversationsKeyFor), and the box record
// carries them so the store's state can name its profile.
type keptOwner struct{ Profile, Account string }

// keptMounts are the read-write mounts of store key's kept folders for a box
// created now, each at its path in the home, and the kept folders left out
// with why. Only entries with status kept count: a held folder is never
// mounted, and is shown as held already.
//
// extraRO are the other mounts CuliBox gives the box (ensure adds the
// workspace). A kept folder that holds one of their targets is left out
// (keptReasonMount): the mountpoint would then sit in the store, where another
// running box of the profile can swap it. One that lies in one is left out too
// (keptReasonInMount): its mountpoint would be made in a host folder.
//
// Nothing read back is trusted (on macOS the VM can write all of boxstate):
// state.json is read no-follow and cleaned (loadKeptState), each path is
// checked again (validKeptRel), and each folder is walked from boxstate one
// descriptor at a time, every component a real directory and never a link
// (keptOpenPath). Only a folder that passed is handed on, to the mountpoint's
// stat in ensureMountpointsUnder and to docker. A store that is not there, or
// whose state cannot be read, mounts nothing. Nothing is created.
func (m *Manager) keptMounts(key string, extraRO []Mount) ([]Mount, []keptRefusal) {
	if !ValidConversationsKey(key) {
		return nil, nil
	}
	state, err := os.OpenRoot(filepath.Join(m.opts.StateRoot, stateDirName))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("box: kept store %s: not mounted: %v", key, err)
		}
		return nil, nil
	}
	defer state.Close()
	storeRel := keptDirName + "/" + key
	d, st, err := keptOpenPath(state, storeRel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // the profile kept nothing yet
	}
	if err != nil {
		log.Printf("box: kept store %s: not mounted: %v", key, err)
		return nil, nil
	}
	d.Close()
	store, err := state.OpenRoot(storeRel)
	if err != nil {
		log.Printf("box: kept store %s: not mounted: %v", key, err)
		return nil, nil
	}
	defer store.Close()
	if !keptRootIs(store, st) {
		log.Printf("box: kept store %s: not mounted: it changed while it was opened", key)
		return nil, nil
	}
	kst, err := loadKeptState(store)
	if err != nil {
		log.Printf("box: kept store %s: not mounted: %v", key, err)
		return nil, nil
	}
	var targets []string
	for _, mt := range extraRO {
		targets = append(targets, mt.Box)
	}
	cb := keptMountRels(targets)
	var out []Mount
	var refs []keptRefusal
	seen := map[[2]uint64]bool{} // the folders mounted, by device and inode
	for _, e := range kst.Kept {
		rel := e.Path
		if e.Status != keptStatusKept || !validKeptRel(rel) {
			continue
		}
		switch {
		case keptMountInside(rel, cb):
			refs = append(refs, keptRefusal{Rel: rel, Reason: keptReasonMount})
			continue
		case keptMountCovers(rel, cb):
			refs = append(refs, keptRefusal{Rel: rel, Reason: keptReasonInMount})
			continue
		}
		f, fst, err := keptOpenPath(state, storeRel+"/"+keptHomeDirName+"/"+rel)
		switch {
		case err == nil:
			f.Close()
		case errors.Is(err, fs.ErrNotExist):
			// Gone from the store (removed by hand): the next pass drops its entry.
			log.Printf("box: kept store %s: kept folder %q is not there; not mounted", key, rel)
			continue
		case errors.Is(err, errKeptLink):
			refs = append(refs, keptRefusal{Rel: rel, Reason: keptReasonLink})
			continue
		default:
			refs = append(refs, keptRefusal{Rel: rel, Reason: keptReasonUnreadable})
			continue
		}
		mt := Mount{Host: filepath.Join(KeptStoreDir(m.opts.StateRoot, key), keptHomeDirName, rel), Box: boxHome + "/" + rel}
		if MountUnsafe(mt.Host) != "" || MountUnsafe(mt.Box) != "" {
			refs = append(refs, keptRefusal{Rel: rel, Reason: keptReasonName})
			continue
		}
		// Two spellings of one folder on a case-insensitive volume: the second
		// mount would only cover the first. Told apart by the folder, not the
		// name, so two folders whose names differ in case (Linux) both mount.
		id := [2]uint64{uint64(fst.Dev), uint64(fst.Ino)}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, mt)
	}
	return out, refs
}

// keptMountpoints makes the mountpoint of each kept mount in box name's home:
// a plain directory made by the daemon user (docker would make it as root),
// and checked like the conversations folder's nested mountpoints
// (plainMountpointUnder): every component a real directory, read with Lstat.
// One that is not (the box left a link or a file there) is left out and
// returned as a refusal: docker would resolve the mount target through it.
// ensure calls it while the box has no container, so nothing writes in the
// home meanwhile.
func (m *Manager) keptMountpoints(name string, kept []Mount) ([]Mount, []keptRefusal) {
	if len(kept) == 0 {
		return nil, nil
	}
	var refs []keptRefusal
	home, err := os.OpenRoot(m.stateDir(name))
	if err != nil {
		log.Printf("box: %s: kept folders not mounted: opening its home: %v", name, err)
		for _, mt := range kept {
			refs = append(refs, keptRefusal{Rel: keptMountRel(mt), Reason: keptReasonMountpoint})
		}
		return nil, refs
	}
	defer home.Close()
	var out []Mount
	for _, mt := range kept {
		err := ensureMountpointsUnder(home, boxHome, []Mount{mt})
		if err == nil {
			err = plainMountpointUnder(home, boxHome, mt)
		}
		if err != nil {
			rel := keptMountRel(mt)
			log.Printf("box: %s: kept folder %q not mounted: %v", name, rel, err)
			refs = append(refs, keptRefusal{Rel: rel, Reason: keptReasonMountpoint})
			continue
		}
		out = append(out, mt)
	}
	return out, refs
}

// keptMountRel is the home-relative path of a kept mount.
func keptMountRel(mt Mount) string { return strings.TrimPrefix(mt.Box, boxHome+"/") }

// prepareKept is ensure's part for kept folders, for box name about to be
// created: with a profile (key, its conversations key), the kept mounts for
// runArgs, their mountpoints made in the home, and the box record written
// (keptBoxRecord): the key, whose spawn this is, the kept folders left out, and
// the home-relative targets of the mounts CuliBox adds, the workspace and the
// conversations folder among them. Not the kept mounts themselves: at the stop
// the home folder under one is an empty mountpoint (skipped), or a copy the
// home held before the mount hid it, which is kept like any other folder.
// With no profile the feature is off: nothing is mounted, and a record a
// profile's spawn left for the box is removed, so the box's next stop keeps
// nothing.
//
// A record that cannot be written or removed refuses the spawn: without it a
// keep pass could take what this box writes for another store, or keep a
// folder holding one of its mounts.
func (m *Manager) prepareKept(name, key string, owner keptOwner, ws Mount, extraRO []Mount) ([]Mount, error) {
	if key == "" {
		if err := removeKeptBoxRecord(m.opts.StateRoot, name); err != nil {
			return nil, fmt.Errorf("%w: box %s: removing its kept-folder record: %v", ErrBoxUnavailable, name, err)
		}
		return nil, nil
	}
	cb := withWorkspace(ws, extraRO)
	kept, refs := m.keptMounts(key, cb)
	kept, more := m.keptMountpoints(name, kept)
	refs = append(refs, more...)
	targets := []string{ConversationsBox}
	for _, mt := range cb {
		targets = append(targets, mt.Box)
	}
	rels := keptMountRels(targets)
	slices.Sort(rels)
	rec := keptBoxRecord{Key: key, Mounts: slices.Compact(rels), Profile: owner.Profile, Workspace: ws.Box, Account: owner.Account}
	now := time.Now()
	for _, r := range refs {
		rec.Refused = append(rec.Refused, KeptRefusalRow{Path: r.Rel, Reason: r.Reason, Box: name, SeenAt: now})
		if r.Reason != keptReasonMountpoint { // logged with its cause already
			log.Printf("box: %s: kept folder %q not mounted: %s", name, r.Rel, r.Reason)
		}
	}
	if err := writeKeptBoxRecord(m.opts.StateRoot, name, rec); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBoxUnavailable, err)
	}
	return kept, nil
}

// removeKeptBoxRecord removes box's record, if there is one a keep pass could
// read: .kept/.boxes is reached without following a link (as readKeptBoxRecord
// reaches it), and only a file or a link named box is unlinked. A record past a
// link, or a directory in its place, is unreadable as a record already.
func removeKeptBoxRecord(stateRoot, box string) error {
	return removeKeptBoxFile(stateRoot, keptBoxesDirName, box)
}

// removeKeptRetries removes box's retry count (readKeptRetries), the same way.
func removeKeptRetries(stateRoot, box string) error {
	return removeKeptBoxFile(stateRoot, keptRetriesDirName, box)
}

// removeKeptBoxFile removes the file box in .kept/<sub>, as removeKeptBoxRecord
// says.
func removeKeptBoxFile(stateRoot, sub, box string) error {
	if !isBoxName(box) {
		return fmt.Errorf("box: %q is not a box name", box)
	}
	state, err := os.OpenRoot(filepath.Join(stateRoot, stateDirName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer state.Close()
	d, _, err := keptOpenPath(state, keptDirName+"/"+sub)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errKeptLink) || errors.Is(err, errKeptNotDir) {
		return nil
	}
	if err != nil {
		return err
	}
	defer d.Close()
	dfd := int(d.Fd())
	var st unix.Stat_t
	if err := unix.Fstatat(dfd, box, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return &fs.PathError{Op: "lstat", Path: box, Err: err}
	}
	if t := st.Mode & unix.S_IFMT; t != unix.S_IFREG && t != unix.S_IFLNK {
		return nil
	}
	if err := unix.Unlinkat(dfd, box, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return &fs.PathError{Op: "unlink", Path: box, Err: err}
	}
	return nil
}

// pruneOrphanKeptRecords removes the kept-folder records and retry counts
// whose home is gone, so .kept/.boxes and .kept/.retries cannot grow on their
// own: reclaimOne removes both with their home, but a home can go by other
// means (by hand, an older build). It holds opMu, as ensure does when it makes
// a home and writes its record, so a box created meanwhile never loses the
// record it just got. Only box names are looked at, and only a missing home
// (lstat, nothing followed) counts as gone.
func (m *Manager) pruneOrphanKeptRecords(ctx context.Context) []error {
	state, err := os.OpenRoot(filepath.Join(m.opts.StateRoot, stateDirName))
	if err != nil {
		return nil
	}
	defer state.Close()
	var errs []error
	names := map[string][]string{} // sub → box names in it
	for _, sub := range []string{keptBoxesDirName, keptRetriesDirName} {
		d, _, err := keptOpenPath(state, keptDirName+"/"+sub)
		if err != nil {
			continue // none, or not a directory the host made: nothing of ours
		}
		ns, err := keptReadNames(d)
		d.Close()
		if err != nil {
			errs = append(errs, fmt.Errorf("box: listing %s/%s: %w", keptDirName, sub, err))
			continue
		}
		names[sub] = slices.DeleteFunc(ns, func(n string) bool { return !isBoxName(n) })
	}
	if len(names[keptBoxesDirName])+len(names[keptRetriesDirName]) == 0 {
		return errs
	}
	if err := m.lockOp(ctx); err != nil {
		return errs // the reclaim is out of time; the next one prunes
	}
	defer m.opMu.Unlock()
	for _, sub := range []string{keptBoxesDirName, keptRetriesDirName} {
		for _, n := range names[sub] {
			if _, err := os.Lstat(m.stateDir(n)); !errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err := removeKeptBoxFile(m.opts.StateRoot, sub, n); err != nil {
				errs = append(errs, fmt.Errorf("box: removing %s/%s/%s, whose home is gone: %s", keptDirName, sub, n, removalCause(err)))
			}
		}
	}
	return errs
}

// keptDstRel is the home-relative path of the in-box destination dst when a
// kept folder can be mounted there (validKeptRel).
func keptDstRel(dst string) (string, bool) {
	rel, ok := strings.CutPrefix(filepath.ToSlash(filepath.Clean(dst)), boxHome+"/")
	if !ok || !validKeptRel(rel) {
		return "", false
	}
	return rel, true
}

// isKeptSource reports whether src, a bind source docker reports, is the
// folder rel of a kept store under this state root: .kept/<key>/home/<rel> for
// any store key (two profiles that share a box share the first one's folders).
// The state root is matched as configured and with its links resolved, as
// docker may report either; nothing under it is resolved.
func (m *Manager) isKeptSource(src, rel string) bool {
	src = filepath.Clean(src)
	roots := []string{filepath.Join(m.opts.StateRoot, stateDirName, keptDirName)}
	if real, err := filepath.EvalSymlinks(m.opts.StateRoot); err == nil {
		roots = append(roots, filepath.Join(real, stateDirName, keptDirName))
	}
	for _, root := range roots {
		rest, ok := strings.CutPrefix(src, root+string(filepath.Separator))
		if !ok {
			continue
		}
		key, tail, _ := strings.Cut(filepath.ToSlash(rest), "/")
		if ValidConversationsKey(key) && tail == keptHomeDirName+"/"+rel {
			return true
		}
	}
	return false
}

// keptOnly reports whether every in-box destination in ds is a kept folder's
// mount (keptDstRel, isKeptSource): those are applied at create only, so a
// running box is never recreated because its profile's store changed since.
// One docker call reads the sources of all of them; a destination whose
// source cannot be read is not a kept folder's.
func (m *Manager) keptOnly(ctx context.Context, name string, ds []string) bool {
	if len(ds) == 0 {
		return true
	}
	for _, d := range ds {
		if _, ok := keptDstRel(d); !ok {
			return false
		}
	}
	srcs, err := m.opts.Client.BindSources(ctx, name)
	if err != nil {
		log.Printf("box: %s: reading its mounts' sources: %v", name, err)
		return false
	}
	byDst := map[string]string{}
	for d, src := range srcs {
		byDst[filepath.Clean(d)] = src
	}
	for _, d := range ds {
		rel, _ := keptDstRel(d)
		if !m.isKeptSource(byDst[filepath.Clean(d)], rel) {
			return false
		}
	}
	return true
}
