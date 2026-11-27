package box

// The keep pass: it moves the app folders a box wrote in its home into the
// profile's kept store (keptscan.go finds and screens them, keptmove.go moves
// them), and screens again what the store already holds. It runs on quiet
// homes only, never on a running box's:
//
//   - after this process stops a box (stopped, in its own goroutine);
//   - before reclaim removes a home past its TTL (ReclaimState);
//   - at boot, for homes stopped while the daemon was down (KeepStoppedHomes).
//
// A spawn never runs it: a folder a box writes is kept at that box's next stop.
//
// The screens run outside opMu: under it they would block every spawn on every
// profile for as long as they take. The moves run under opMu, once stillUnused
// says the box is still unused, and each moves only the very directory that
// was screened (mergeMove compares device and inode by descriptor). Then the
// store side is screened: the folders just moved (so whatever changed in one
// between its screen and its move is seen), what a pass left in the store
// unrecorded (a stop between a move and its save), and every kept folder, since
// the profile's boxes mount them read-write. A hit there holds the folder: it
// stays on disk and is not mounted until a later screen passes.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Pass bounds. Variables so tests can lower them.
var (
	// keptPassBudget bounds one pass, its docker calls included. The home's
	// screens stop at half of it, so a home full of folders cannot take the
	// time the store's re-screen needs.
	keptPassBudget = 60 * time.Second
	// keptPassMoveBudget bounds the moves under opMu: no move starts after it
	// (one that has started stops at its own keptScreenTimeout).
	keptPassMoveBudget = 10 * time.Second
)

// Test hooks. keptPassLockHook, when set, runs between the home's screens and
// the moves, with no lock but the store's held. keptMergeMove is mergeMove,
// replaced by a test to fail a move.
var (
	keptPassLockHook func(box string)
	keptMergeMove    = mergeMove
)

// Audit actions of the pass.
const (
	keptAuditKept = "box_kept"      // detail: paths=<a,b> box=<name>
	keptAuditHeld = "box_kept_held" // detail: path=<p> reason=<r>
)

// keptReasonFull is the not-kept reason past keptMaxPaths.
const keptReasonFull = "too many kept folders"

var (
	errKeptBusy      = errors.New("its box is in use, or whether it is could not be read")
	errKeptOutOfTime = errors.New("not checked in time")
)

// keptFolderErr is why one home folder was not moved. Its text names the
// folder quoted and the cause without any path: an error from below a box home
// can carry names the box chose.
type keptFolderErr struct {
	rel string
	err error
}

func (e *keptFolderErr) Error() string { return strconv.Quote(e.rel) + ": " + keptFolderCause(e.err) }
func (e *keptFolderErr) Unwrap() error { return e.err }

func keptFolderCause(err error) string {
	var errno syscall.Errno
	switch {
	case errors.Is(err, errKeptChanged):
		return "it changed while it was checked; the next pass looks again"
	case errors.Is(err, errKeptBusy):
		return "not moved: " + errKeptBusy.Error()
	case errors.Is(err, errKeptOutOfTime):
		return errKeptOutOfTime.Error() + "; the next pass does it"
	case errors.Is(err, errKeptTooMany):
		return "too many entries to move"
	case errors.As(err, &errno):
		return "could not be moved: " + errno.Error()
	}
	return "could not be moved"
}

// keptErrs is several errors on one line.
type keptErrs []error

func (e keptErrs) Error() string {
	s := make([]string, len(e))
	for i, err := range e {
		s[i] = err.Error()
	}
	return strings.Join(s, "; ")
}

func (e keptErrs) Unwrap() []error { return e }

// keptStoreLock is the lock of the store key: held by a pass from reading its
// state.json to writing it back. Take it before opMu, never while holding it.
func (m *Manager) keptStoreLock(key string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.keptMu == nil {
		m.keptMu = map[string]*sync.Mutex{}
	}
	mu := m.keptMu[key]
	if mu == nil {
		mu = &sync.Mutex{}
		m.keptMu[key] = mu
	}
	return mu
}

// runKeepPass is keepPass for the stop and boot triggers, which log what it
// left in the home (reclaim reports it instead).
func (m *Manager) runKeepPass(ctx context.Context, box string) {
	if _, err := m.keepPass(ctx, box); err != nil {
		log.Printf("box: %s: a folder to keep stays in its home for now: %v", box, err)
	}
}

// KeepStoppedHomes runs the keep pass on every box home that has a store
// record and whose box does not run (keepPass checks both): at boot, after
// Reconcile, for the homes of boxes stopped while the daemon was down. ctx
// bounds the whole run; each pass is bounded by keptPassBudget too.
func (m *Manager) KeepStoppedHomes(ctx context.Context) {
	entries, err := os.ReadDir(filepath.Join(m.opts.StateRoot, stateDirName))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("box: keeping the folders of stopped boxes: %v", err)
		}
		return
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		if e.IsDir() && isBoxName(e.Name()) {
			m.runKeepPass(ctx, e.Name())
		}
	}
}

// keepPass keeps the app folders of box's home into its profile's store and
// screens again what the store holds. It does nothing for a box with no store
// record (no profile, or made before the store existed), a box with a session
// or a running container, or a home that is gone.
//
// keptN is how many home folders it moved into the store. The error lists the
// keepable folders it could not move (they stay in the home), or says the
// pass could not tell whether the box runs or could not read the store: a
// caller about to remove the home must keep it. A folder that is not kept
// (credentials, a cap, a read error) is never an error: its not-kept row says
// why, and it goes with the home.
func (m *Manager) keepPass(ctx context.Context, box string) (keptN int, err error) {
	// Most boxes have no record: one lstat (never followed) settles it before
	// any root is opened.
	if !isBoxName(box) {
		return 0, nil
	}
	if _, err := os.Lstat(filepath.Join(m.opts.StateRoot, stateDirName, keptDirName, keptBoxesDirName, box)); err != nil {
		return 0, nil
	}
	rec, ok := readKeptBoxRecord(m.opts.StateRoot, box)
	if !ok {
		return 0, nil
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, keptPassBudget)
	defer cancel()
	if !m.idle(box) {
		return 0, nil
	}
	if ctx.Err() != nil {
		return 0, fmt.Errorf("nothing kept: %w", errKeptOutOfTime)
	}
	st, err := m.opts.Client.Inspect(ctx, box)
	if err != nil {
		return 0, fmt.Errorf("could not tell whether the box runs, so nothing was kept: %w", err)
	}
	if st == StateRunning {
		return 0, nil
	}
	home, err := keptOpenHome(m.opts.StateRoot, box)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errKeptLink) || errors.Is(err, errKeptNotDir) {
		return 0, nil // no home, or a link or file where one belongs: nothing that is a home
	}
	if err != nil {
		return 0, fmt.Errorf("opening the home: %w", err)
	}
	defer home.Close()

	mu := m.keptStoreLock(rec.Key)
	mu.Lock()
	defer mu.Unlock()
	store, err := ensureKeptStore(m.opts.StateRoot, rec.Key)
	if err != nil {
		return 0, err
	}
	defer store.Close()
	shome, err := keptStoreHome(store)
	if err != nil {
		return 0, fmt.Errorf("kept store %s: %w", rec.Key, err)
	}
	defer shome.Close()
	// A state that cannot be read is never written over: its forgotten list
	// among the rest is not known, so nothing moves either.
	before, err := loadKeptState(store)
	if err != nil {
		return 0, err
	}
	p := &keptPass{m: m, box: box, home: home, store: store, shome: shome, now: time.Now(), st: keptCopy(before)}
	if err := p.plan(rec.Mounts); err != nil {
		return 0, err
	}
	p.screenHome(ctx, start.Add(keptPassBudget/2))
	if keptPassLockHook != nil {
		keptPassLockHook(box)
	}
	p.move(ctx)
	p.screenStore(ctx)
	p.finish(before)
	if len(p.errs) > 0 {
		return len(p.moved), p.errs
	}
	return len(p.moved), nil
}

// keptOpenHome opens box's home as a root: a real directory in boxstate,
// reached without following a link (on macOS the VM can write boxstate), and
// checked to be the directory walked. fs.ErrNotExist when there is none,
// errKeptLink or errKeptNotDir when a link or a file is there instead.
func keptOpenHome(stateRoot, box string) (*os.Root, error) {
	state, err := os.OpenRoot(filepath.Join(stateRoot, stateDirName))
	if err != nil {
		return nil, err
	}
	defer state.Close()
	d, st, err := keptOpenPath(state, box)
	if err != nil {
		return nil, err
	}
	d.Close()
	home, err := state.OpenRoot(box)
	if err != nil {
		return nil, err
	}
	if !keptRootIs(home, st) {
		home.Close()
		return nil, errors.New("the home changed while it was opened")
	}
	return home, nil
}

// keptStoreHome opens the store's home/ as a root, refusing a link there.
func keptStoreHome(store *os.Root) (*os.Root, error) {
	d, st, err := keptOpenPath(store, keptHomeDirName)
	if err != nil {
		return nil, err
	}
	d.Close()
	r, err := store.OpenRoot(keptHomeDirName)
	if err != nil {
		return nil, err
	}
	if !keptRootIs(r, st) {
		r.Close()
		return nil, errors.New("its home folder changed while it was opened")
	}
	return r, nil
}

// keptCopy is st with slices of its own.
func keptCopy(st KeptState) KeptState {
	st.Kept, st.NotKept, st.Forgotten = slices.Clone(st.Kept), slices.Clone(st.NotKept), slices.Clone(st.Forgotten)
	return st
}

// keptFolderID is the device and inode of the directory rel under root,
// reached without following a link.
func keptFolderID(root *os.Root, rel string) ([2]uint64, error) {
	d, st, err := keptOpenPath(root, rel)
	if err != nil {
		return [2]uint64{}, err
	}
	d.Close()
	return [2]uint64{uint64(st.Dev), uint64(st.Ino)}, nil
}

// keptPass is one pass over one home and its store.
type keptPass struct {
	m                  *Manager
	box                string
	home, store, shome *os.Root // the box home, the store, the store's home/
	now                time.Time
	st                 KeptState // the state being built, saved by finish

	adopt []string         // store folders state.json does not list
	todo  []keptCandidate  // home folders to screen
	ready []keptCandidate  // home folders that passed, to move
	moved []string         // home folders moved into the store
	rows  []KeptRefusalRow // new not-kept rows
	errs  keptErrs         // keepable home folders not moved
	kept  []string         // folders the store now records, or kept again
	held  []KeptEntry      // entries that became held
	drop  map[int]bool     // entries whose folder is gone from the store
	// unchecked counts the store folders not screened in time.
	unchecked int
}

func (p *keptPass) row(rel, reason, box string) {
	p.rows = append(p.rows, KeptRefusalRow{Path: rel, Reason: reason, Box: box, SeenAt: p.now})
}

func (p *keptPass) fail(rel string, err error) {
	p.errs = append(p.errs, &keptFolderErr{rel: rel, err: err})
}

// plan lists the store's unrecorded folders and the home's candidates, and
// gives each new path one of the store's keptMaxPaths slots. A candidate the
// store already has merges into it and needs none. Forgotten paths are dropped
// unscreened (ruling R2). Past the cap, a path gets the not-kept row
// keptReasonFull and is never screened.
func (p *keptPass) plan(mounts []string) error {
	inStore := map[string]bool{}
	for _, e := range p.st.Kept {
		inStore[e.Path] = true
	}
	free := keptMaxPaths - len(p.st.Kept)
	scands, _, err := keptCandidates(p.shome, nil)
	if err != nil {
		return fmt.Errorf("listing the kept store: %w", err)
	}
	for _, c := range scands {
		if inStore[c.Rel] || keptFoldIn(c.Rel, p.st.Forgotten) || !validKeptRel(c.Rel) {
			continue
		}
		if free <= 0 {
			p.row(c.Rel, keptReasonFull, "")
			continue
		}
		free--
		inStore[c.Rel] = true
		p.adopt = append(p.adopt, c.Rel)
	}
	cands, refs, err := keptCandidates(p.home, mounts)
	if err != nil {
		return err
	}
	for _, r := range refs {
		if !keptFoldIn(r.Rel, p.st.Forgotten) { // forgotten: silent, a row included
			p.row(r.Rel, r.Reason, p.box)
		}
	}
	for _, c := range cands {
		switch {
		case keptFoldIn(c.Rel, p.st.Forgotten):
		case inStore[c.Rel]:
			p.todo = append(p.todo, c)
		case free > 0:
			free--
			p.todo = append(p.todo, c)
		default:
			p.row(c.Rel, keptReasonFull, p.box)
		}
	}
	return nil
}

// notChecked records the home folders left unscreened: they stay in the home.
func (p *keptPass) notChecked(cs []keptCandidate) {
	for _, c := range cs {
		p.fail(c.Rel, errKeptOutOfTime)
	}
}

// screenHome screens the home's candidates, until the deadline. A refusal is a
// not-kept row; a folder that is not the directory discovery saw is not moved.
func (p *keptPass) screenHome(ctx context.Context, until time.Time) {
	ctx, cancel := context.WithDeadline(ctx, until)
	defer cancel()
	for i, c := range p.todo {
		if ctx.Err() != nil {
			p.notChecked(p.todo[i:])
			return
		}
		r := screenKept(ctx, p.home, c.Rel)
		switch {
		case ctx.Err() != nil:
			p.notChecked(p.todo[i:]) // cut short: its result is no verdict
			return
		case r.Reason != "":
			p.row(c.Rel, r.Reason, p.box)
		case r.Dev != c.Dev || r.Ino != c.Ino:
			p.fail(c.Rel, errKeptChanged)
		default:
			p.ready = append(p.ready, c)
		}
	}
}

// move moves the screened folders into the store under opMu, once the box is
// known to be still unused. mergeMove moves only the directory screened (the
// candidate's device and inode, checked by descriptor).
func (p *keptPass) move(ctx context.Context) {
	if len(p.ready) == 0 {
		return
	}
	m := p.m
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if _, ok := m.stillUnused(ctx, p.box, 0); !ok {
		for _, c := range p.ready {
			p.fail(c.Rel, errKeptBusy)
		}
		return
	}
	until := time.Now().Add(keptPassMoveBudget)
	clash := keptClashDirName + "/" + p.box
	for i, c := range p.ready {
		if ctx.Err() != nil || !time.Now().Before(until) {
			p.notChecked(p.ready[i:])
			return
		}
		if _, _, err := keptMergeMove(p.home, p.store, c.Rel, clash, c); err != nil {
			p.fail(c.Rel, err)
			continue
		}
		p.moved = append(p.moved, c.Rel)
	}
}

// screenStore screens the store's folders and sets each entry's status from
// it. Kept folders come first, in a new order each pass (a pass out of time
// leaves a different few for the next), then the folders new to the state,
// then the held ones, which no box mounts.
func (p *keptPass) screenStore(ctx context.Context) {
	p.drop = map[int]bool{}
	var kept, held []int
	inState := map[string]bool{}
	ids := map[[2]uint64]bool{} // the entries' folders
	for i, e := range p.st.Kept {
		if e.Status == keptStatusHeld {
			held = append(held, i)
		} else {
			kept = append(kept, i)
		}
		inState[e.Path] = true
		if id, err := keptFolderID(p.shome, e.Path); err == nil {
			ids[id] = true
		}
	}
	rand.Shuffle(len(kept), func(i, j int) { kept[i], kept[j] = kept[j], kept[i] })
	var fresh []string
	for _, rel := range slices.Concat(p.moved, p.adopt) {
		if !inState[rel] && !slices.Contains(fresh, rel) {
			fresh = append(fresh, rel)
		}
	}
	type item struct {
		idx int // an entry's index, or -1 for a fresh path
		rel string
	}
	var items []item
	for _, i := range kept {
		items = append(items, item{idx: i})
	}
	for _, rel := range fresh {
		items = append(items, item{idx: -1, rel: rel})
	}
	for _, i := range held {
		items = append(items, item{idx: i})
	}
	for n, it := range items {
		done := false
		if ctx.Err() == nil {
			if it.idx >= 0 {
				done = p.screenEntry(ctx, it.idx)
			} else {
				done = p.screenFresh(ctx, it.rel, ids)
			}
		}
		if !done {
			p.unchecked = len(items) - n
			return
		}
	}
}

// screenEntry screens entry i's folder in the store. false: out of time, the
// entry stays as it was.
func (p *keptPass) screenEntry(ctx context.Context, i int) bool {
	e := &p.st.Kept[i]
	if _, err := keptFolderID(p.shome, e.Path); errors.Is(err, fs.ErrNotExist) {
		p.drop[i] = true // removed from the store by hand: nothing to show or mount
		return true
	}
	r := screenKept(ctx, p.shome, e.Path)
	if ctx.Err() != nil {
		return false
	}
	if r.Reason == "" {
		if e.Status == keptStatusHeld {
			p.kept = append(p.kept, e.Path)
		}
		e.Status, e.HeldReason, e.Bytes, e.Files = keptStatusKept, "", r.Bytes, r.Files
		return true
	}
	// Held: its last counts stay, as a refusal's are only what was seen.
	wasKept := e.Status != keptStatusHeld
	e.Status, e.HeldReason = keptStatusHeld, r.Reason
	if wasKept {
		p.held = append(p.held, *e)
	}
	return true
}

// screenFresh screens a store folder state.json does not list yet (moved now,
// or by a pass that stopped before its save) and records it. One that is an
// entry's folder under another spelling (a case-insensitive volume) is not a
// new folder. false: out of time, it stays unrecorded for the next pass.
func (p *keptPass) screenFresh(ctx context.Context, rel string, ids map[[2]uint64]bool) bool {
	id, err := keptFolderID(p.shome, rel)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && ids[id]) {
		return true
	}
	r := screenKept(ctx, p.shome, rel)
	if ctx.Err() != nil {
		return false
	}
	e := KeptEntry{Path: rel, KeptAt: p.now, Bytes: r.Bytes, Files: r.Files, Status: keptStatusKept}
	if slices.Contains(p.moved, rel) {
		e.FromBox = p.box
	}
	if r.Reason != "" {
		e.Status, e.HeldReason = keptStatusHeld, r.Reason
		p.held = append(p.held, e)
	}
	p.st.Kept = append(p.st.Kept, e)
	p.kept = append(p.kept, rel)
	if err == nil {
		ids[id] = true
	}
	return true
}

// finish saves the state when the pass changed it, then audits and logs what
// it kept and held. The rows go after the save: a save that fails leaves the
// moved folders unrecorded, and the next pass records (and audits) them.
func (p *keptPass) finish(before KeptState) {
	if p.unchecked > 0 {
		log.Printf("box: %s: %d folder(s) in the kept store not checked again in time; the next pass does it", p.box, p.unchecked)
	}
	if len(p.drop) > 0 {
		var kept []KeptEntry
		for i, e := range p.st.Kept {
			if !p.drop[i] {
				kept = append(kept, e)
			}
		}
		p.st.Kept = kept
	}
	// What this box's pass kept supersedes the rows this box had for it.
	p.st.NotKept = slices.DeleteFunc(p.st.NotKept, func(r KeptRefusalRow) bool {
		return r.Box == p.box && slices.Contains(p.moved, r.Path)
	})
	p.st.NotKept = append(p.st.NotKept, p.rows...)
	if reflect.DeepEqual(cleanKeptState(before), cleanKeptState(p.st)) {
		return
	}
	if err := saveKeptState(p.store, p.st); err != nil {
		log.Printf("box: %s: the kept store's state was not saved (%v); the next pass records what moved", p.box, err)
		return
	}
	var paths []string
	for _, rel := range slices.Concat(p.moved, p.kept) {
		if !slices.Contains(paths, rel) {
			paths = append(paths, rel)
		}
	}
	slices.Sort(paths)
	if len(paths) > 0 {
		p.audit(keptAuditKept, "paths="+strings.Join(paths, ",")+" box="+p.box)
		q := make([]string, len(paths))
		for i, rel := range paths {
			q[i] = strconv.Quote(rel)
		}
		log.Printf("box: %s: kept %s for its profile", p.box, strings.Join(q, ", "))
	}
	for _, e := range p.held {
		p.audit(keptAuditHeld, "path="+e.Path+" reason="+e.HeldReason)
		log.Printf("box: %s: kept folder %q is held, not mounted into the profile's boxes until a later check passes: %q",
			p.box, e.Path, e.HeldReason)
	}
}

// audit writes one row, named after the store's profile. A failure is logged:
// the move it records has happened either way.
func (p *keptPass) audit(action, detail string) {
	if p.m.opts.Audit == nil {
		return
	}
	if err := p.m.opts.Audit(action, p.st.Profile, detail); err != nil {
		log.Printf("box: %s: audit row %s not written: %v", p.box, action, err)
	}
}
