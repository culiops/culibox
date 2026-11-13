package box

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
)

// Guard locks (#96, macOS). A Mac volume compares names case-insensitively by
// default, and the box VM shares it over virtiofs: the in-place guard's
// read-only bind of a single file covers one spelling of its name, and another
// (claude.md, .MCP.JSON, .GIT) reaches the very same Mac file (measured on a Mac,
// 2026-11-10). Directory guards hold under every spelling (measured).
//
// So on a case-insensitive volume, while a box that was spawned in place runs,
// CuliBox sets the user-immutable flag (UF_IMMUTABLE, `chflags uchg`) on each
// guarded single file that exists: nothing in the box or the VM can write,
// remove, rename, link, chmod it or clear the flag (measured, 2026-11-11). The
// flag is set before the box starts and cleared once the last box that holds it
// is gone: a box's background processes outlive its sessions, so the holder is
// the box (its container), not a session. A box that holds a lock is therefore
// removed when its last session ends, instead of idling (Manager.idleTTL).
//
// A flag the user had set already is recorded as not ours and never cleared.
// The records live in the data root (guardLocksFile), so a crash or restart
// clears what it left behind (Manager.Reconcile, Sweep, SweepGuardLocksNoBoxes).
//
// The logic is OS-neutral over flagOps, so the refcount, persistence, user-flag
// rule and sweep are tested on Linux with a fake; the real flags are darwin's
// (guardlock_darwin.go). Everywhere else there are none, and nothing here runs.

// guardLocksFile is the lock records' file in the data root.
const guardLocksFile = "inplace-locks.json"

// guardLocksVersion is the records' format: one record per file identity (2).
// Version 1 (the first #96 build, never released) kept one per folder
// spelling; it loads merged by identity (mergeByFile).
const guardLocksVersion = 2

// flagOps is what the guard locks need from the OS.
type flagOps interface {
	// caseSensitive reports whether names in directory dir compare
	// case-sensitively. An error is treated as "no" (lock: fail closed).
	caseSensitive(dir string) (bool, error)
	// immutable reports whether f carries the user-immutable flag.
	immutable(f *os.File) (bool, error)
	// setImmutable sets (on) or clears it on f, keeping every other flag.
	setImmutable(f *os.File, on bool) error
}

// GuardLocksSupported reports whether this OS has the file flags the guard
// locks use (macOS).
func GuardLocksSupported() bool { return platformFlagOps() != nil }

// GuardLock is one locked file as recorded in the data root. A record is a
// file, by identity: two spellings of one folder (~/code/Repo and ~/code/repo
// on a case-insensitive volume) are two boxes but one file, so one lock.
type GuardLock struct {
	// Workspace is the in-place workspace (absolute, symlinks resolved) the file
	// was first locked through; File a name directly in it.
	Workspace string `json:"workspace"`
	File      string `json:"file"`
	// Also are the other spellings boxes reached this same file by, tried in
	// turn when it is unlocked.
	Also []LockPath `json:"also,omitempty"`
	// Dev and Ino are the file's identity on the host when it was locked: the
	// record's key, and a lock is only ever cleared on that very file.
	Dev uint64 `json:"dev"`
	Ino uint64 `json:"ino"`
	// Ours is false when the file already carried the flag before CuliBox
	// locked it: the user set it, so it is never cleared.
	Ours bool `json:"ours"`
	// Boxes hold the lock until their container is gone. Sessions are the
	// sessions started in them, for display only.
	Boxes    []string      `json:"boxes,omitempty"`
	Sessions []LockSession `json:"sessions,omitempty"`
}

// LockPath is one spelling a locked file was reached by.
type LockPath struct {
	Workspace string `json:"workspace"`
	File      string `json:"file"`
}

// paths are every spelling of the record's file, the first one first.
func (l GuardLock) paths() []LockPath {
	return append([]LockPath{{l.Workspace, l.File}}, l.Also...)
}

// LockSession is a session that runs in a box holding a lock.
type LockSession struct {
	Box string `json:"box"`
	ID  string `json:"id"`
}

// Path is the locked file's full path.
func (l GuardLock) Path() string { return filepath.Join(l.Workspace, l.File) }

type guardLockDoc struct {
	Version int         `json:"version"`
	Locks   []GuardLock `json:"locks"`
}

// guardLocks owns the records and the flags. Its mutex is taken after
// Manager.mu, never before: no method here calls back into the Manager.
type guardLocks struct {
	mu   sync.Mutex
	ops  flagOps
	path string
	recs []GuardLock
}

// errLockGone: the recorded file is not at its path any more (missing, or
// another file): there is nothing of ours to clear.
var errLockGone = errors.New("the locked file is gone")

// newGuardLocks loads the records under root. Unreadable records are logged
// and dropped: a flag CuliBox set then reads as the user's and is never
// cleared, which is the safe direction (doctor names the file to unlock).
func newGuardLocks(root string, ops flagOps) *guardLocks {
	g := &guardLocks{ops: ops, path: filepath.Join(root, guardLocksFile)}
	recs, err := ReadGuardLocks(root)
	if err != nil {
		log.Printf("box: in-place lock records: %v (starting with none; a file CuliBox locked before stays locked until you run chflags nouchg on it)", err)
	}
	g.recs = recs
	return g
}

// ReadGuardLocks reads the lock records in the data root dataDir: none when the
// file does not exist.
func ReadGuardLocks(dataDir string) ([]GuardLock, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, guardLocksFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc guardLockDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", guardLocksFile, err)
	}
	if doc.Version != 1 && doc.Version != guardLocksVersion {
		return nil, fmt.Errorf("%s: unknown version %d", guardLocksFile, doc.Version)
	}
	for _, r := range doc.Locks {
		for _, lp := range r.paths() {
			if !filepath.IsAbs(lp.Workspace) || lp.File == "" || lp.File == "." || lp.File == ".." ||
				strings.ContainsRune(lp.File, '/') {
				return nil, fmt.Errorf("%s: a record names %q in %q, not a file directly in a folder", guardLocksFile, lp.File, lp.Workspace)
			}
		}
	}
	return mergeByFile(doc.Locks), nil
}

// mergeByFile folds records of one file identity into one. Only version 1
// could hold several (one per folder spelling); there a duplicate may have
// read CuliBox's own flag as the user's, so the file is ours when any record
// says so (CuliBox records "not ours" only for a file that was flagged
// already, and set that flag itself if any record is ours).
func mergeByFile(rs []GuardLock) []GuardLock {
	var out []GuardLock
	for _, r := range rs {
		i := slices.IndexFunc(out, func(o GuardLock) bool { return o.Dev == r.Dev && o.Ino == r.Ino })
		if i < 0 {
			out = append(out, r)
			continue
		}
		o := &out[i]
		o.Ours = o.Ours || r.Ours
		for _, lp := range r.paths() {
			o.addPath(lp)
		}
		for _, b := range r.Boxes {
			if !slices.Contains(o.Boxes, b) {
				o.Boxes = append(o.Boxes, b)
			}
		}
		for _, s := range r.Sessions {
			if !slices.Contains(o.Sessions, s) {
				o.Sessions = append(o.Sessions, s)
			}
		}
	}
	return out
}

// addPath records lp as a spelling of the record's file, once.
func (l *GuardLock) addPath(lp LockPath) {
	if !slices.Contains(l.paths(), lp) {
		l.Also = append(l.Also, lp)
	}
}

// save writes the records atomically, owner-only; no records removes the file.
// Caller holds g.mu.
func (g *guardLocks) save() error {
	if len(g.recs) == 0 {
		if err := os.Remove(g.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	b, err := json.MarshalIndent(guardLockDoc{Version: guardLocksVersion, Locks: g.recs}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(g.path), "."+guardLocksFile+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
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
	if err := os.Rename(name, g.path); err != nil {
		return err
	}
	ok = true
	return nil
}

func cloneLocks(rs []GuardLock) []GuardLock {
	out := make([]GuardLock, len(rs))
	for i, r := range rs {
		r.Also = slices.Clone(r.Also)
		r.Boxes = slices.Clone(r.Boxes)
		r.Sessions = slices.Clone(r.Sessions)
		out[i] = r
	}
	return out
}

// lockRefusal is the spawn refusal for a file that cannot be locked: it names
// the file and the ways out, and wraps ErrUnboxableWorkspace so the user sees it.
func lockRefusal(p, why string) error {
	return refuseInPlace("%s cannot be locked on your Mac (%s). A box started in place on this case-insensitive disk "+
		"could otherwise change it through another letter case (such as %s), so it is not started. Make sure you own the "+
		"file and can read it, or remove it, or use a clone-mode profile", p, why, otherCase(filepath.Base(p)))
}

// setRefusal is the refusal for a flag that could not be set on p. A volume
// without file flags (exFAT, a network share) answers ENOTSUP or EOPNOTSUPP:
// no owner or permission fix helps there, only clone mode.
func setRefusal(p string, err error) error {
	if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EOPNOTSUPP) {
		return refuseInPlace("%s cannot be locked on your Mac: its volume does not support the lock (%v; exFAT and "+
			"network shares do not). A box started in place on this case-insensitive disk could otherwise change it "+
			"through another letter case (such as %s), so it is not started. Use a clone-mode profile for this folder",
			p, err, otherCase(filepath.Base(p)))
	}
	return lockRefusal(p, err.Error())
}

// otherCase is an example spelling of name in another letter case: all lower
// case when it has an upper-case letter (claude.md), else all upper (.MCP.JSON).
func otherCase(name string) string {
	if strings.ToLower(name) != name {
		return strings.ToLower(name)
	}
	return strings.ToUpper(name)
}

// openLocked opens name in root without blocking (a FIFO swapped in must not
// hang the spawn) and returns it with its identity, only when it is a regular
// file. os.Root keeps the open inside root.
func openLocked(root *os.Root, name string) (*os.File, FileID, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, FileID{}, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, FileID{}, err
	}
	id := hostFileID(fi)
	if id == nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, FileID{}, errLockGone
	}
	return f, *id, nil
}

// hold locks each of files (the guarded single files of workspace ws, from
// inPlaceGuardsAt) for box, with session started in it, and returns the names
// CuliBox holds locked for box, sorted. Nothing is locked on a case-sensitive
// volume. Each file is opened inside ws and must be the very file the guards
// were derived from (identity), or the spawn is refused: a name swapped since is
// never the one flagged. Every failure is a refusal (fail closed) and undoes
// what this call did.
func (g *guardLocks) hold(ws string, files []guardFile, box, session string) ([]string, error) {
	if len(files) == 0 {
		return g.lockedFor(box), nil
	}
	if cs, err := g.ops.caseSensitive(ws); err == nil && cs {
		return g.lockedFor(box), nil
	}
	root, err := os.OpenRoot(ws)
	if err != nil {
		return nil, lockRefusal(ws, fmt.Sprintf("the folder cannot be opened: %v", err))
	}
	defer root.Close()

	type opened struct {
		name string
		f    *os.File
		id   FileID
		imm  bool
	}
	var all []opened
	defer func() {
		for _, o := range all {
			o.f.Close()
		}
	}()
	for _, gf := range files {
		p := filepath.Join(ws, gf.Name)
		f, id, err := openLocked(root, gf.Name)
		if errors.Is(err, errLockGone) {
			return nil, lockRefusal(p, "it is no longer a regular file")
		}
		if err != nil {
			return nil, lockRefusal(p, err.Error())
		}
		if want := hostFileID(gf.Info); want == nil || *want != id {
			f.Close()
			return nil, lockRefusal(p, "it changed after it was checked; spawn again")
		}
		imm, err := g.ops.immutable(f)
		if err != nil {
			f.Close()
			return nil, lockRefusal(p, fmt.Sprintf("its flags cannot be read: %v", err))
		}
		all = append(all, opened{gf.Name, f, id, imm})
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	before := cloneLocks(g.recs)
	var set []opened
	for _, o := range all {
		// By identity, not by name: another spelling of the folder reaches the
		// same file, and a file already flagged by a record of ours is joined
		// (still ours), never taken for the user's flag. A record left at this
		// name for another file is not this one's; it is unlocked only on its
		// own file (unlock checks the identity).
		i := g.find(o.id)
		if i < 0 {
			g.recs = append(g.recs, GuardLock{Workspace: ws, File: o.name, Dev: o.id.Dev, Ino: o.id.Ino, Ours: !o.imm})
			i = len(g.recs) - 1
		}
		r := &g.recs[i]
		r.addPath(LockPath{ws, o.name})
		if !o.imm {
			// Unflagged: never locked, cleared by hand, or a crash between the
			// record and the flag. Setting it now makes it ours.
			r.Ours = true
			set = append(set, o)
		}
		if !slices.Contains(r.Boxes, box) {
			r.Boxes = append(r.Boxes, box)
		}
		if session != "" && !slices.Contains(r.Sessions, LockSession{box, session}) {
			r.Sessions = append(r.Sessions, LockSession{box, session})
		}
	}
	// Recorded before any flag is set: a crash in between leaves a record of
	// an unflagged file, which the sweep drops.
	if err := g.save(); err != nil {
		g.recs = before
		return nil, lockRefusal(filepath.Join(ws, all[0].name), fmt.Sprintf("the lock record cannot be written: %v", err))
	}
	for k, o := range set {
		if err := g.ops.setImmutable(o.f, true); err != nil {
			for _, d := range set[:k] {
				_ = g.ops.setImmutable(d.f, false)
			}
			g.recs = before
			if serr := g.save(); serr != nil {
				log.Printf("box: in-place lock records: %v", serr)
			}
			return nil, setRefusal(filepath.Join(ws, o.name), err)
		}
	}
	return g.lockedForLocked(box), nil
}

// find is the record of the file with identity id, or -1. Caller holds g.mu.
func (g *guardLocks) find(id FileID) int {
	return slices.IndexFunc(g.recs, func(r GuardLock) bool { return r.Dev == id.Dev && r.Ino == id.Ino })
}

// lockedFor names the files CuliBox holds locked for box, sorted.
func (g *guardLocks) lockedFor(box string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lockedForLocked(box)
}

func (g *guardLocks) lockedForLocked(box string) []string {
	var out []string
	for _, r := range g.recs {
		if r.Ours && slices.Contains(r.Boxes, box) && !slices.Contains(out, r.File) {
			out = append(out, r.File)
		}
	}
	sort.Strings(out)
	return out
}

// holdsOurs reports whether box holds a lock CuliBox set.
func (g *guardLocks) holdsOurs(box string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.ContainsFunc(g.recs, func(r GuardLock) bool { return r.Ours && slices.Contains(r.Boxes, box) })
}

// releaseBox drops box from every record: its container is gone. A file no box
// holds any more is unlocked.
func (g *guardLocks) releaseBox(box string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.dropBoxes(func(b string) bool { return b != box }) {
		g.settle(true)
	}
}

// forgetSession drops a session that ended from the display list. The box
// keeps holding: it may still run what the session started.
func (g *guardLocks) forgetSession(box, session string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	changed := false
	for i := range g.recs {
		n := len(g.recs[i].Sessions)
		g.recs[i].Sessions = slices.DeleteFunc(g.recs[i].Sessions, func(s LockSession) bool { return s == LockSession{box, session} })
		changed = changed || len(g.recs[i].Sessions) != n
	}
	if changed {
		if err := g.save(); err != nil {
			log.Printf("box: in-place lock records: %v", err)
		}
	}
}

// sweep keeps only the boxes in keep (running, or with live sessions) and
// unlocks what no box holds any more, retrying a clear that failed before.
func (g *guardLocks) sweep(keep map[string]bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.settle(g.dropBoxes(func(b string) bool { return keep[b] }))
}

// dropBoxes keeps the boxes (and their sessions) for which keep is true and
// reports whether anything was dropped. Caller holds g.mu.
func (g *guardLocks) dropBoxes(keep func(string) bool) bool {
	changed := false
	for i := range g.recs {
		r := &g.recs[i]
		n := len(r.Boxes)
		r.Boxes = slices.DeleteFunc(r.Boxes, func(b string) bool { return !keep(b) })
		r.Sessions = slices.DeleteFunc(r.Sessions, func(s LockSession) bool { return !slices.Contains(r.Boxes, s.Box) })
		changed = changed || len(r.Boxes) != n
	}
	return changed
}

// settle unlocks every record no box holds and saves when anything changed
// (changed: the caller changed the records already). A record whose clear
// failed stays, with no box, as a leftover: doctor names it and the next sweep
// tries again. A sweep that changes nothing writes nothing. Caller holds g.mu.
func (g *guardLocks) settle(changed bool) {
	n := len(g.recs)
	g.recs = slices.DeleteFunc(g.recs, func(r GuardLock) bool {
		if len(r.Boxes) > 0 {
			return false
		}
		if !r.Ours {
			return true // the user's flag: never cleared
		}
		err := g.unlock(r)
		if err != nil && !errors.Is(err, errLockGone) {
			log.Printf("box: unlocking %s: %v (left locked; run `chflags nouchg` on it to unlock it now)", r.Path(), err)
			return false
		}
		return true
	})
	if !changed && len(g.recs) == n {
		return
	}
	if err := g.save(); err != nil {
		log.Printf("box: in-place lock records: %v", err)
	}
}

// unlock clears the flag on the recorded file through the first of its
// spellings that still reaches it, only if it is still that very file and
// still flagged. errLockGone when no spelling reaches anything of ours to
// clear; any other failure is returned (the record stays, to retry).
func (g *guardLocks) unlock(r GuardLock) error {
	var errs []error
	for _, lp := range r.paths() {
		err := g.unlockAt(r, lp)
		if err == nil {
			return nil
		}
		if !errors.Is(err, errLockGone) {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return errLockGone
}

// unlockAt is unlock through one spelling lp of r's file.
func (g *guardLocks) unlockAt(r GuardLock, lp LockPath) error {
	root, err := os.OpenRoot(lp.Workspace)
	if errors.Is(err, fs.ErrNotExist) {
		return errLockGone
	}
	if err != nil {
		return err
	}
	defer root.Close()
	f, id, err := openLocked(root, lp.File)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errLockGone) {
		return errLockGone
	}
	if err != nil {
		return err
	}
	defer f.Close()
	if id.Dev != r.Dev || id.Ino != r.Ino {
		return errLockGone
	}
	imm, err := g.ops.immutable(f)
	if err != nil {
		return err
	}
	if !imm {
		return errLockGone
	}
	return g.ops.setImmutable(f, false)
}

// all is a copy of the records.
func (g *guardLocks) all() []GuardLock {
	g.mu.Lock()
	defer g.mu.Unlock()
	return cloneLocks(g.recs)
}

// lockReq is what a spawn asks the guard locks for (Wrap → ensure).
type lockReq struct {
	ws      string
	files   []guardFile
	session string
}

// holdLocks takes the guard locks lk asks for, for box name. Caller holds opMu.
func (m *Manager) holdLocks(name string, lk *lockReq) error {
	if m.locks == nil || lk == nil {
		return nil
	}
	_, err := m.locks.hold(lk.ws, lk.files, name, lk.session)
	return err
}

// boxGone is called once a box's container was removed: the locks it held are
// released, and a file no other box holds is unlocked. Caller holds opMu.
func (m *Manager) boxGone(name string) {
	m.mu.Lock()
	delete(m.mismatched, name)
	delete(m.guardsChecked, name)
	m.mu.Unlock()
	if m.locks != nil {
		m.locks.releaseBox(name)
	}
}

// sweepLocks lifts the guard locks of every box that is neither in present
// (containers that may still run) nor counted as live. Caller holds opMu.
func (m *Manager) sweepLocks(present map[string]bool) {
	if m.locks == nil {
		return
	}
	keep := map[string]bool{}
	for n := range present {
		keep[n] = true
	}
	m.mu.Lock()
	for n, c := range m.active {
		if c > 0 {
			keep[n] = true
		}
	}
	m.mu.Unlock()
	m.locks.sweep(keep)
}

// SweepGuardLocksNoBoxes is the boot sweep of the guard locks while the box VM
// is stopped: no container can run, so every lock no live session holds is
// lifted (a Mac restart must not leave the user's files locked until the next
// boxed spawn). A no-op without guard locks.
func (m *Manager) SweepGuardLocksNoBoxes() {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.sweepLocks(nil)
}

// LockedFiles names the files CuliBox keeps locked on the host while box runs
// (macOS guard locks), sorted; nil without any.
func (m *Manager) LockedFiles(box string) []string {
	if m.locks == nil || box == "" {
		return nil
	}
	return m.locks.lockedFor(box)
}

// SessionEnded drops an ended session from the guard locks' display. The box
// keeps its locks until its container is gone.
func (m *Manager) SessionEnded(box, sessionID string) {
	if m.locks == nil || box == "" {
		return
	}
	m.locks.forgetSession(box, sessionID)
}
