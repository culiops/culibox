package box

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func mkfifo(p string) error { return syscall.Mkfifo(p, 0o644) }

// fakeFlags stands for the Mac's file flags: the immutable flag per file
// identity, so it follows the file, not its name.
type fakeFlags struct {
	mu        sync.Mutex
	sensitive bool
	senseErr  error
	imm       map[FileID]bool
	setErr    map[string]error // by file base name
	sets      int
	onSet     func() // called before each set or clear, unlocked
}

func newFakeFlags() *fakeFlags {
	return &fakeFlags{imm: map[FileID]bool{}, setErr: map[string]error{}}
}

func (f *fakeFlags) caseSensitive(string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sensitive, f.senseErr
}

func fileIDOf(t testing.TB, fi os.FileInfo) FileID {
	id := hostFileID(fi)
	if id == nil {
		t.Fatal("no identity")
	}
	return *id
}

func (f *fakeFlags) immutable(file *os.File) (bool, error) {
	fi, err := file.Stat()
	if err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.imm[*hostFileID(fi)], nil
}

func (f *fakeFlags) setImmutable(file *os.File, on bool) error {
	fi, err := file.Stat()
	if err != nil {
		return err
	}
	if f.onSet != nil {
		f.onSet()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.setErr[filepath.Base(file.Name())]; e != nil {
		return e
	}
	f.sets++
	f.imm[*hostFileID(fi)] = on
	return nil
}

// flagged reports whether the file at p carries the fake flag.
func (f *fakeFlags) flagged(t *testing.T, p string) bool {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.imm[fileIDOf(t, fi)]
}

func (f *fakeFlags) userSets(t *testing.T, p string) {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.imm[fileIDOf(t, fi)] = true
}

// lockMgr is a guarded manager with fake macOS flags.
func lockMgr(t *testing.T) (*Manager, *fakeClient, *fakeFlags) {
	t.Helper()
	m, f := guardMgr(t, true)
	ff := newFakeFlags()
	m.locks = newGuardLocks(m.opts.StateRoot, ff)
	return m, f, ff
}

// lockWS is a workspace with every kind of guarded single file: the
// instruction and MCP files, and .git as a worktree's gitfile.
func lockWS(t *testing.T) string {
	t.Helper()
	tmp := realTemp(t)
	gitdir := filepath.Join(tmp, "main", ".git", "worktrees", "wt")
	mkdirs(t, gitdir)
	writeFile(t, filepath.Join(tmp, "main", ".git", "config"), "")
	writeFile(t, filepath.Join(gitdir, "commondir"), "../..\n")
	ws := filepath.Join(tmp, "ws")
	writeFile(t, filepath.Join(ws, ".git"), "gitdir: "+gitdir+"\n")
	writeFile(t, filepath.Join(ws, "CLAUDE.md"), "x")
	writeFile(t, filepath.Join(ws, ".mcp.json"), "{}")
	return ws
}

func readLocks(t *testing.T, m *Manager) []GuardLock {
	t.Helper()
	rs, err := ReadGuardLocks(m.opts.StateRoot)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func TestGuardLocksFilesOnlyAndBeforeTheBoxStarts(t *testing.T) {
	m, f, ff := lockMgr(t)
	ws := lockWS(t)
	files := []string{".git", ".mcp.json", "CLAUDE.md"}
	var atRun []bool
	f.runHook = func(args []string) (string, bool, error) {
		if slices.Contains(args, "--name") {
			for _, n := range files {
				atRun = append(atRun, ff.flagged(t, filepath.Join(ws, n)))
			}
		}
		return "", false, nil
	}
	_, _, name, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(atRun) != len(files) || slices.Contains(atRun, false) {
		t.Fatalf("every guarded file must be locked before docker run, got %v", atRun)
	}
	if got := m.LockedFiles(name); !slices.Equal(got, files) {
		t.Fatalf("LockedFiles = %v, want %v", got, files)
	}
	// .claude is a directory guard: its bind holds under every spelling.
	if fi, _ := os.Lstat(filepath.Join(ws, ".claude")); ff.imm[fileIDOf(t, fi)] {
		t.Fatal("a directory must never be locked")
	}
	rs := readLocks(t, m)
	if len(rs) != 3 {
		t.Fatalf("want 3 records, got %+v", rs)
	}
	for _, r := range rs {
		if r.Workspace != ws || !r.Ours || !slices.Equal(r.Boxes, []string{name}) ||
			!slices.Equal(r.Sessions, []LockSession{{name, "s1"}}) {
			t.Fatalf("record %+v", r)
		}
		fi, _ := os.Lstat(r.Path())
		if id := fileIDOf(t, fi); id.Dev != r.Dev || id.Ino != r.Ino {
			t.Fatalf("record identity %+v is not the file's %+v", r, id)
		}
	}
	fi, err := os.Stat(filepath.Join(m.opts.StateRoot, guardLocksFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("records must be owner-only: %v %v", fi, err)
	}
	if left, _ := filepath.Glob(filepath.Join(m.opts.StateRoot, "."+guardLocksFile+".*")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestGuardLocksGitDirectoryIsNotLocked(t *testing.T) {
	m, _, _ := lockMgr(t)
	ws := realTemp(t)
	gitRepo(t, ws, "")
	writeFile(t, filepath.Join(ws, "AGENTS.md"), "x")
	_, _, name, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"bash"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.LockedFiles(name); !slices.Equal(got, []string{"AGENTS.md"}) {
		t.Fatalf("a .git directory must not be locked: %v", got)
	}
}

func TestGuardLocksSkipCaseSensitiveVolume(t *testing.T) {
	m, f, ff := lockMgr(t)
	ff.sensitive = true
	ws := lockWS(t)
	if _, _, _, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if ff.sets != 0 || f.runs() != 1 {
		t.Fatalf("a case-sensitive volume needs no lock: sets=%d runs=%d", ff.sets, f.runs())
	}
	if _, err := os.Stat(filepath.Join(m.opts.StateRoot, guardLocksFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no record without a lock: %v", err)
	}
}

func TestGuardLocksProbeErrorLocks(t *testing.T) {
	m, _, ff := lockMgr(t)
	ff.sensitive, ff.senseErr = true, errors.New("pathconf: EINVAL")
	ws := lockWS(t)
	if _, _, _, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !ff.flagged(t, filepath.Join(ws, "CLAUDE.md")) {
		t.Fatal("an unknown case sensitivity must lock (fail closed)")
	}
}

func TestGuardLocksFailClosed(t *testing.T) {
	m, f, ff := lockMgr(t)
	ws := lockWS(t)
	ff.setErr["CLAUDE.md"] = errors.New("operation not permitted")
	_, _, _, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil)
	if !errors.Is(err, ErrUnboxableWorkspace) {
		t.Fatalf("want a user-facing refusal, got %v", err)
	}
	for _, want := range []string{filepath.Join(ws, "CLAUDE.md"), "operation not permitted", "clone-mode", "claude.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err, want)
		}
	}
	if f.runs() != 0 {
		t.Fatal("a box must not start when a lock cannot be set")
	}
	for _, n := range []string{".git", ".mcp.json", "CLAUDE.md"} {
		if ff.flagged(t, filepath.Join(ws, n)) {
			t.Fatalf("%s: what this spawn locked must be undone", n)
		}
	}
	if rs := readLocks(t, m); len(rs) != 0 {
		t.Fatalf("no record may remain: %+v", rs)
	}
}

func TestGuardLocksRefuseASwappedFile(t *testing.T) {
	_, _, ff := lockMgr(t)
	g := newGuardLocks(t.TempDir(), ff)
	ws := lockWS(t)
	other := filepath.Join(ws, "other")
	writeFile(t, other, "x")
	fi, err := os.Lstat(other) // what the guard derived: another file than CLAUDE.md now
	if err != nil {
		t.Fatal(err)
	}
	_, err = g.hold(ws, []guardFile{{"CLAUDE.md", fi}}, "b1", "s1")
	if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "changed after it was checked") {
		t.Fatalf("want a changed-file refusal, got %v", err)
	}
	if ff.sets != 0 || len(g.all()) != 0 {
		t.Fatal("nothing may be locked or recorded")
	}
	// A FIFO at the name must neither hang nor be locked.
	fifo := filepath.Join(ws, "AGENTS.md")
	if err := mkfifo(fifo); err != nil {
		t.Skip("no mkfifo:", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := g.hold(ws, []guardFile{{"AGENTS.md", fi}}, "b1", "s1")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrUnboxableWorkspace) {
			t.Fatalf("want a refusal for a FIFO, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hold blocked on a FIFO")
	}
}

// #246: a flag this instance did not set (the user's, another instance's, or
// one whose record was lost) is never relied on, so it is never lifted under a
// box either: the spawn is refused, naming the file, and the flag stays.
func TestGuardLocksRefuseAFlagNotOurs(t *testing.T) {
	_, _, ff := lockMgr(t)
	g := newGuardLocks(t.TempDir(), ff)
	ws := lockWS(t)
	p := filepath.Join(ws, "CLAUDE.md")
	ff.userSets(t, p)
	fi, _ := os.Lstat(p)
	_, err := g.hold(ws, []guardFile{{"CLAUDE.md", fi}}, "b1", "s1")
	if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), p) ||
		!strings.Contains(err.Error(), "is locked already") {
		t.Fatalf("want a refusal naming %s, got %v", p, err)
	}
	if len(g.all()) != 0 || g.holdsOurs("b1") {
		t.Fatalf("a refused file is not recorded: %+v", g.all())
	}
	if !ff.flagged(t, p) {
		t.Fatal("the flag CuliBox did not set must never be cleared")
	}
	g.releaseBox("b1")
	if !ff.flagged(t, p) {
		t.Fatal("the flag CuliBox did not set must never be cleared")
	}
}

func TestGuardLocksRefcountAcrossBoxes(t *testing.T) {
	_, _, ff := lockMgr(t)
	g := newGuardLocks(t.TempDir(), ff)
	ws := lockWS(t)
	p := filepath.Join(ws, "CLAUDE.md")
	fi, _ := os.Lstat(p)
	gf := []guardFile{{"CLAUDE.md", fi}}
	if _, err := g.hold(ws, gf, "b1", "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.hold(ws, gf, "b1", "s2"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.hold(ws, gf, "b2", "s3"); err != nil {
		t.Fatal(err)
	}
	g.forgetSession("b1", "s1")
	if rs := g.all(); !slices.Equal(rs[0].Sessions, []LockSession{{"b1", "s2"}, {"b2", "s3"}}) {
		t.Fatalf("sessions %+v", rs[0].Sessions)
	}
	g.releaseBox("b1")
	if !ff.flagged(t, p) {
		t.Fatal("another box still holds the lock")
	}
	if rs := g.all(); !slices.Equal(rs[0].Boxes, []string{"b2"}) || !slices.Equal(rs[0].Sessions, []LockSession{{"b2", "s3"}}) {
		t.Fatalf("record %+v", rs[0])
	}
	g.releaseBox("b2")
	if ff.flagged(t, p) {
		t.Fatal("the last box gone must unlock the file")
	}
	if _, err := os.Stat(g.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("no records: no file")
	}
}

// The flag is lifted when the box is gone, and a box holding a lock is removed
// as soon as its last session ends (no idle time with the files locked).
func TestGuardLocksLastSessionRemovesBoxAndUnlocks(t *testing.T) {
	m, f, ff := lockMgr(t)
	ws := lockWS(t)
	p := filepath.Join(ws, "CLAUDE.md")
	_, _, name, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.Wrap(context.Background(), ws, "s2", BoxKey{}, []string{"claude"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	m.SessionEnded(name, "s1")
	m.ReleaseBox(name)
	time.Sleep(20 * time.Millisecond)
	if !ff.flagged(t, p) || f.stoppedAny(name) {
		t.Fatal("one session still runs: the box and the lock stay")
	}
	m.SessionEnded(name, "s2")
	m.ReleaseBox(name)
	waitFor(t, 5*time.Second, func() bool { return f.stoppedAny(name) })
	waitFor(t, 5*time.Second, func() bool { return !ff.flagged(t, p) })
	// The flags are cleared, then the records saved: wait for the save too.
	waitFor(t, 5*time.Second, func() bool { return len(readLocks(t, m)) == 0 })
	if rs := readLocks(t, m); len(rs) != 0 {
		t.Fatalf("records left: %+v", rs)
	}
}

func TestGuardLocksBootSweepAfterRestart(t *testing.T) {
	m, f, ff := lockMgr(t)
	ws := lockWS(t)
	p := filepath.Join(ws, "CLAUDE.md")
	_, _, gone, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ws2 := lockWS(t)
	p2 := filepath.Join(ws2, "CLAUDE.md")
	_, _, alive, err := m.Wrap(context.Background(), ws2, "s2", BoxKey{}, []string{"claude"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// The daemon restarts: a new manager reads the records. The session in the
	// second box survived (adopted); the first box's container is gone.
	m2, err := New(m.opts)
	if err != nil {
		t.Fatal(err)
	}
	m2.locks = newGuardLocks(m.opts.StateRoot, ff)
	m2.AdoptBox(alive)
	f.existing = []string{alive}
	_ = gone
	if err := m2.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ff.flagged(t, p) {
		t.Fatal("a lock whose box is gone must be lifted at boot")
	}
	if !ff.flagged(t, p2) {
		t.Fatal("a box with a live session keeps its lock")
	}
	if got := m2.LockedFiles(alive); !slices.Contains(got, "CLAUDE.md") {
		t.Fatalf("the adopted box still holds its locks: %v", got)
	}
}

func TestGuardLocksSweepNoBoxesAndPeriodic(t *testing.T) {
	m, f, ff := lockMgr(t)
	ws := lockWS(t)
	p := filepath.Join(ws, "CLAUDE.md")
	_, _, name, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A running box with a session keeps its lock through a sweep.
	if _, err := m.Sweep(context.Background(), map[string]int{name: 1}); err != nil {
		t.Fatal(err)
	}
	if !ff.flagged(t, p) {
		t.Fatal("a running box keeps its lock")
	}
	// The VM restarted under it: the container is gone and the session with it.
	delete(f.running, name)
	m.mu.Lock()
	delete(m.active, name)
	m.mu.Unlock()
	if _, err := m.Sweep(context.Background(), map[string]int{}); err != nil {
		t.Fatal(err)
	}
	if ff.flagged(t, p) {
		t.Fatal("a sweep lifts the lock of a box docker no longer runs")
	}

	// Boot with the VM stopped.
	_, _, name, err = m.Wrap(context.Background(), ws, "s2", BoxKey{}, []string{"claude"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m3, _ := New(m.opts)
	m3.locks = newGuardLocks(m.opts.StateRoot, ff)
	m3.SweepGuardLocksNoBoxes()
	if ff.flagged(t, p) {
		t.Fatal("with the VM stopped no box runs: the lock is lifted")
	}
	_ = name
}

func TestGuardLocksClearOnlyTheRecordedFile(t *testing.T) {
	_, _, ff := lockMgr(t)
	g := newGuardLocks(t.TempDir(), ff)
	ws := lockWS(t)
	p := filepath.Join(ws, "CLAUDE.md")
	fi, _ := os.Lstat(p)
	if _, err := g.hold(ws, []guardFile{{"CLAUDE.md", fi}}, "b1", ""); err != nil {
		t.Fatal(err)
	}
	// Replaced since (by hand, with the lock lifted), and the user flags the new one.
	if err := os.Rename(p, p+".old"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "new")
	ff.userSets(t, p)
	g.sweep(map[string]bool{})
	if !ff.flagged(t, p) {
		t.Fatal("a file that is not the recorded one must never be unlocked")
	}
	if !ff.flagged(t, p+".old") {
		t.Fatal("the recorded file is no longer at the path: nothing is cleared there")
	}
	if len(g.all()) != 0 {
		t.Fatal("a record whose file is gone is dropped")
	}
}

func TestGuardLocksFailedClearStaysAsLeftover(t *testing.T) {
	_, _, ff := lockMgr(t)
	g := newGuardLocks(t.TempDir(), ff)
	ws := lockWS(t)
	p := filepath.Join(ws, "CLAUDE.md")
	fi, _ := os.Lstat(p)
	if _, err := g.hold(ws, []guardFile{{"CLAUDE.md", fi}}, "b1", ""); err != nil {
		t.Fatal(err)
	}
	ff.setErr["CLAUDE.md"] = errors.New("EPERM")
	g.releaseBox("b1")
	rs := g.all()
	if len(rs) != 1 || len(rs[0].Boxes) != 0 {
		t.Fatalf("a failed clear stays recorded with no box: %+v", rs)
	}
	delete(ff.setErr, "CLAUDE.md")
	g.sweep(nil)
	if ff.flagged(t, p) || len(g.all()) != 0 {
		t.Fatal("the next sweep retries the clear")
	}
}

func TestGuardLocksOffByDefault(t *testing.T) {
	m, _ := guardMgr(t, true)
	ws := lockWS(t)
	if _, _, name, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil); err != nil {
		t.Fatal(err)
	} else if m.LockedFiles(name) != nil {
		t.Fatal("no locks without LockGuards")
	}
	if _, err := os.Stat(filepath.Join(m.opts.StateRoot, guardLocksFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("no record file without LockGuards")
	}
	opts := m.opts
	opts.LockGuards = true
	_, err := New(opts)
	if platformFlagOps() == nil && err == nil {
		t.Fatal("LockGuards must be refused on an OS without file flags")
	}
}

func TestGuardLocksUnreadableRecordsStartEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, guardLocksFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadGuardLocks(dir); err == nil {
		t.Fatal("want a read error")
	}
	if g := newGuardLocks(dir, newFakeFlags()); len(g.all()) != 0 {
		t.Fatal("unreadable records start empty")
	}
	b, _ := json.Marshal(guardLockDoc{Version: 99})
	_ = os.WriteFile(filepath.Join(dir, guardLocksFile), b, 0o600)
	if _, err := ReadGuardLocks(dir); err == nil {
		t.Fatal("an unknown version is not read")
	}
}

func TestOtherCase(t *testing.T) {
	if got := otherCase("CLAUDE.md"); got != "claude.md" {
		t.Fatal(got)
	}
	if got := otherCase(".git"); got != ".GIT" {
		t.Fatal(got)
	}
}

// spellings are two names of one workspace folder, as ~/code/Repo and
// ~/code/repo are on a case-insensitive Mac volume (each a different box):
// a is a symlink to the folder, b the folder's own path in another case. On a
// case-insensitive volume b already reaches the folder (the very case the lock
// guards); elsewhere it is made a second symlink. b is not a case variant of a:
// on such a volume that would be a's own link, gone when a test removes a.
func spellings(t *testing.T) (ws, a, b string) {
	t.Helper()
	ws = lockWS(t)
	a = filepath.Join(realTemp(t), "Repo")
	if err := os.Symlink(ws, a); err != nil {
		t.Fatal(err)
	}
	b = filepath.Join(filepath.Dir(ws), strings.ToUpper(filepath.Base(ws)))
	wfi, err := os.Stat(ws)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(b); err == nil && os.SameFile(fi, wfi) {
		return ws, a, b
	}
	if err := os.Symlink(ws, b); err != nil {
		t.Fatal(err)
	}
	return ws, a, b
}

func claudeFile(t *testing.T, ws string) []guardFile {
	t.Helper()
	fi, err := os.Lstat(filepath.Join(ws, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	return []guardFile{{"CLAUDE.md", fi}}
}

// Review #96 (1): the lock is the file's, not the folder spelling's. A second
// box on another spelling joins the lock CuliBox set (it is not the user's
// flag), and the flag stays until neither box holds it, in either order.
func TestGuardLocksTwoSpellingsShareOneLock(t *testing.T) {
	for _, firstGone := range []string{"b1", "b2"} {
		t.Run(firstGone+" ends first", func(t *testing.T) {
			_, _, ff := lockMgr(t)
			g := newGuardLocks(t.TempDir(), ff)
			ws, a, b := spellings(t)
			p := filepath.Join(ws, "CLAUDE.md")
			if _, err := g.hold(a, claudeFile(t, a), "b1", "s1"); err != nil {
				t.Fatal(err)
			}
			got, err := g.hold(b, claudeFile(t, b), "b2", "s2")
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, []string{"CLAUDE.md"}) || !g.holdsOurs("b2") {
				t.Fatalf("the second spelling's box holds CuliBox's lock: locked=%v holdsOurs=%v", got, g.holdsOurs("b2"))
			}
			rs := g.all()
			if len(rs) != 1 || !rs[0].Ours || !slices.Equal(rs[0].Boxes, []string{"b1", "b2"}) {
				t.Fatalf("want one record of ours held by both boxes: %+v", rs)
			}
			g.releaseBox(firstGone)
			if !ff.flagged(t, p) {
				t.Fatal("a box on the other spelling still runs: the file must stay locked")
			}
			g.releaseBox(map[string]string{"b1": "b2", "b2": "b1"}[firstGone])
			if ff.flagged(t, p) || len(g.all()) != 0 {
				t.Fatalf("no box holds it: unlocked, no record (flag=%v recs=%+v)", ff.flagged(t, p), g.all())
			}
		})
	}
}

// The file is unlocked through any spelling a holder used: the first may be
// gone by then.
func TestGuardLocksUnlockThroughAnotherSpelling(t *testing.T) {
	_, _, ff := lockMgr(t)
	g := newGuardLocks(t.TempDir(), ff)
	ws, a, b := spellings(t)
	if _, err := g.hold(a, claudeFile(t, a), "b1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := g.hold(b, claudeFile(t, b), "b2", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	g.releaseBox("b1")
	g.releaseBox("b2")
	if ff.flagged(t, filepath.Join(ws, "CLAUDE.md")) || len(g.all()) != 0 {
		t.Fatal("the lock must be lifted through the spelling that still reaches the file")
	}
}

// Records written by the first #96 build (one per folder spelling) load merged
// by file: a duplicate that read CuliBox's own flag as the user's is ours.
func TestGuardLocksLoadMergesRecordsOfOneFile(t *testing.T) {
	_, _, ff := lockMgr(t)
	dir := t.TempDir()
	ws, a, b := spellings(t)
	p := filepath.Join(ws, "CLAUDE.md")
	fi, _ := os.Lstat(p)
	id := fileIDOf(t, fi)
	ff.userSets(t, p) // CuliBox's flag, set by the box on spelling a
	v1 := guardLockDoc{Version: 1, Locks: []GuardLock{
		{Workspace: a, File: "CLAUDE.md", Dev: id.Dev, Ino: id.Ino, Ours: true, Boxes: []string{"b1"}},
		{Workspace: b, File: "CLAUDE.md", Dev: id.Dev, Ino: id.Ino, Ours: false, Boxes: []string{"b2"},
			Sessions: []LockSession{{"b2", "s2"}}},
	}}
	raw, _ := json.Marshal(v1)
	if err := os.WriteFile(filepath.Join(dir, guardLocksFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	g := newGuardLocks(dir, ff)
	rs := g.all()
	if len(rs) != 1 || !rs[0].Ours || !slices.Equal(rs[0].Boxes, []string{"b1", "b2"}) ||
		!slices.Equal(rs[0].Sessions, []LockSession{{"b2", "s2"}}) {
		t.Fatalf("want one merged record of ours: %+v", rs)
	}
	g.releaseBox("b1")
	if !ff.flagged(t, p) {
		t.Fatal("b2 still holds it")
	}
	g.releaseBox("b2")
	if ff.flagged(t, p) {
		t.Fatal("the merged lock is lifted with its last box")
	}
}

// A record file that is not exactly what CuliBox writes is not read at all,
// and then a flag already on a file is never taken for CuliBox's (fail closed:
// it stays locked rather than be cleared on a guess).
func TestGuardLocksBadRecordsFailClosed(t *testing.T) {
	for name, rec := range map[string]GuardLock{
		"relative folder": {Workspace: "code/repo", File: "CLAUDE.md", Ours: true},
		"path in file":    {Workspace: "/Users/u/repo", File: "../x/CLAUDE.md", Ours: true},
		"dot file name":   {Workspace: "/Users/u/repo", File: "..", Ours: true},
		"no file":         {Workspace: "/Users/u/repo", Ours: true},
	} {
		dir := t.TempDir()
		raw, _ := json.Marshal(guardLockDoc{Version: guardLocksVersion, Locks: []GuardLock{rec}})
		_ = os.WriteFile(filepath.Join(dir, guardLocksFile), raw, 0o600)
		if _, err := ReadGuardLocks(dir); err == nil {
			t.Errorf("%s: a bad record must fail the read", name)
		}
	}

	_, _, ff := lockMgr(t)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, guardLocksFile), []byte("{"), 0o600)
	ws := lockWS(t)
	p := filepath.Join(ws, "CLAUDE.md")
	ff.userSets(t, p) // flagged by CuliBox before the records went bad
	g := newGuardLocks(dir, ff)
	if _, err := g.hold(ws, claudeFile(t, ws), "b1", ""); !errors.Is(err, ErrUnboxableWorkspace) ||
		!strings.Contains(err.Error(), p) {
		t.Fatalf("a flag with no record of ours refuses the spawn, naming the file: %v", err)
	}
	g.releaseBox("b1")
	if !ff.flagged(t, p) {
		t.Fatal("with unreadable records an existing flag must never be cleared")
	}
}

// Review #96 (3): the periodic sweep lifts a lock only for a box it removed or
// that docker no longer lists; a box in any other state whose removal failed
// still holds its lock.
func TestGuardLocksSweepKeepsABoxItCouldNotRemove(t *testing.T) {
	for _, state := range []string{"paused", "restarting", "exited"} {
		t.Run(state, func(t *testing.T) {
			m, f, ff := lockMgr(t)
			ws := lockWS(t)
			p := filepath.Join(ws, "CLAUDE.md")
			_, _, name, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			delete(m.active, name)
			m.mu.Unlock()
			f.mu.Lock()
			f.existing = []string{name}
			f.states = map[string]string{name: state}
			f.stopFails = 1
			f.mu.Unlock()
			if _, err := m.Sweep(context.Background(), map[string]int{}); err == nil {
				t.Fatal("want the failed removal reported")
			}
			if !ff.flagged(t, p) {
				t.Fatalf("a %s box that could not be removed may still run: its lock stays", state)
			}
			if _, err := m.Sweep(context.Background(), map[string]int{}); err != nil {
				t.Fatal(err)
			}
			if ff.flagged(t, p) {
				t.Fatal("removed on the next sweep: the lock is lifted")
			}
		})
	}
}

// Review #96 (5): a sweep that changes nothing does not rewrite the records.
func TestGuardLocksSweepSavesOnlyOnChange(t *testing.T) {
	_, _, ff := lockMgr(t)
	g := newGuardLocks(t.TempDir(), ff)
	ws := lockWS(t)
	if _, err := g.hold(ws, claudeFile(t, ws), "b1", ""); err != nil {
		t.Fatal(err)
	}
	stat := func() os.FileInfo {
		fi, err := os.Stat(g.path)
		if err != nil {
			t.Fatal(err)
		}
		return fi
	}
	before := stat()
	g.sweep(map[string]bool{"b1": true})
	if !os.SameFile(before, stat()) {
		t.Fatal("a sweep with nothing to change rewrote the records")
	}
	// A leftover whose clear keeps failing is not rewritten on every sweep either.
	ff.setErr["CLAUDE.md"] = errors.New("EPERM")
	g.sweep(nil)
	before = stat()
	g.sweep(nil)
	if !os.SameFile(before, stat()) {
		t.Fatal("a sweep that only failed a clear again rewrote the records")
	}
	g.forgetSession("b1", "none")
	if !os.SameFile(before, stat()) {
		t.Fatal("forgetting an unknown session rewrote the records")
	}
}

// awaitIdleRetry waits until the box was asked to stop stops times and the
// stop in flight has released opMu. A box that failed to start or was refused
// while it holds guard locks gets an idle TTL of 0 (idleTTL), so rearmIfIdle
// fires stopIfIdle at once on another goroutine, which removes it again. A
// test that fails only the first Stop races that retry: it removes the box,
// rightly lifts the lock, and the test's check sees it unlocked (CI run
// 42973277692). Tests of "a box that may still run keeps its lock" therefore
// fail every Stop and check after this.
func awaitIdleRetry(t *testing.T, m *Manager, f *fakeClient, stops int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); f.stops() < stops; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("stops=%d; a box holding locks with no session must be removed again at once", f.stops())
		}
	}
	// stopIfIdle holds opMu across its Stop and the release that follows.
	m.opMu.Lock()
	m.opMu.Unlock()
}

// Review #96 (6): a box that never came to be holds no lock; one docker left
// behind keeps it while it may still run (every removal fails here).
func TestGuardLocksFailedRunUnlocks(t *testing.T) {
	for _, left := range []bool{false, true} {
		m, f, ff := lockMgr(t)
		ws := lockWS(t)
		p := filepath.Join(ws, "CLAUDE.md")
		f.runHook = func(args []string) (string, bool, error) {
			if !slices.Contains(args, "--name") {
				return "", false, nil
			}
			if left {
				f.mu.Lock()
				f.running[args[slices.Index(args, "--name")+1]] = true
				f.mu.Unlock()
			}
			return "", true, errors.New("docker run: failed")
		}
		if left {
			f.stopFails = math.MaxInt
		}
		if _, _, _, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil); err == nil {
			t.Fatal("want the run failure")
		}
		if got := ff.flagged(t, p); got != left {
			t.Fatalf("left behind=%v: flagged=%v", left, got)
		}
		if left {
			awaitIdleRetry(t, m, f, 1)
			if !ff.flagged(t, p) {
				t.Fatal("left behind, its removal failed: the lock stays")
			}
		}
	}
}

// wrongStat answers the in-box identity check with one no mount has.
func wrongStat(args []string) (string, bool, error) {
	if len(args) > 2 && args[0] == "exec" && args[2] == "/usr/bin/stat" {
		return strings.Repeat("1:1\n", 8), true, nil
	}
	return "", false, nil
}

// refuseUnverified and refuseMountChanged lift the lock of the box they
// removed, and keep it when the removal failed.
//
// A failing removal fails every time (see awaitIdleRetry): both the refusal
// and the idle retry after it must keep the lock while the box may still run.
func TestGuardLocksRefusalsUnlockOnlyARemovedBox(t *testing.T) {
	cases := map[string]func(m *Manager, f *fakeClient){
		"unverified": func(m *Manager, f *fakeClient) {
			m.opts.Verify = func(context.Context, string) error { return errors.New("reached loopback") }
		},
		// The host reads the identity: Linux only (workspaceID is nil elsewhere).
		"mount changed": func(m *Manager, f *fakeClient) {
			f.stdoutHook = wrongStat
		},
		// The box VM reads it (Options.VMIDs), as on macOS: runs on every OS.
		"mount changed in the VM": func(m *Manager, f *fakeClient) {
			m.opts.VMIDs = func(_ context.Context, paths []string) ([]FileID, error) {
				ids := make([]FileID, len(paths))
				for i := range ids {
					ids[i] = FileID{Dev: 7, Ino: uint64(100 + i)}
				}
				return ids, nil
			}
			f.stdoutHook = wrongStat
		},
	}
	for name, setup := range cases {
		for _, stopFails := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s stop fails %v", name, stopFails), func(t *testing.T) {
				if name == "mount changed" && runtime.GOOS != "linux" {
					t.Skip("the host reads mount identities on Linux only; the VM case covers this OS")
				}
				m, f, ff := lockMgr(t)
				setup(m, f)
				if stopFails {
					f.stopFails = math.MaxInt
				}
				ws := lockWS(t)
				p := filepath.Join(ws, "CLAUDE.md")
				if _, _, _, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil); err == nil {
					t.Fatal("want the refusal")
				}
				if f.stops() == 0 {
					t.Fatal("the refusal must try to remove the box")
				}
				if got := ff.flagged(t, p); got != stopFails {
					t.Fatalf("flagged=%v; want it locked only while the box may still run", got)
				}
				if !stopFails {
					return
				}
				awaitIdleRetry(t, m, f, 2)
				if !ff.flagged(t, p) {
					t.Fatal("the idle retry failed to remove the box too: its lock stays")
				}
			})
		}
	}
}

// stopIfIdle lifts the lock only once the box is removed.
func TestGuardLocksIdleStopKeepsLockWhenRemovalFails(t *testing.T) {
	m, f, ff := lockMgr(t)
	ws := lockWS(t)
	p := filepath.Join(ws, "CLAUDE.md")
	_, _, name, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	delete(m.active, name)
	m.mu.Unlock()
	f.stopFails = 1
	m.stopIfIdle(name)
	if !ff.flagged(t, p) || !slices.Equal(m.LockedFiles(name), []string{".git", ".mcp.json", "CLAUDE.md"}) {
		t.Fatal("a box whose removal failed may still run: its locks stay")
	}
	m.stopIfIdle(name)
	if ff.flagged(t, p) {
		t.Fatal("removed: the lock is lifted")
	}
}

// Review #96 (7): a volume without file flags (exFAT, a network share) says so
// and points at clone mode, not at the file's owner.
func TestGuardLocksUnsupportedVolumeRefusal(t *testing.T) {
	for _, e := range []error{syscall.ENOTSUP, syscall.EOPNOTSUPP} {
		m, _, ff := lockMgr(t)
		ws := lockWS(t)
		ff.setErr["CLAUDE.md"] = e
		_, _, _, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"claude"}, nil, nil)
		if !errors.Is(err, ErrUnboxableWorkspace) {
			t.Fatalf("want a refusal, got %v", err)
		}
		msg := err.Error()
		if !strings.Contains(msg, "does not support") || !strings.Contains(msg, "clone-mode") || strings.Contains(msg, "own the file") {
			t.Fatalf("refusal %q must say the volume does not support the lock and point to clone mode", msg)
		}
	}
}

// #246: two instances (separate data roots) never hold one file at once. The
// second is refused while the first's flag is on, so the first lifting its own
// lock can never unprotect a box of the second; once it is lifted, the second
// locks the file as its own.
func TestGuardLocksSecondInstanceIsRefused(t *testing.T) {
	_, _, ff := lockMgr(t)
	x, y := newGuardLocks(t.TempDir(), ff), newGuardLocks(t.TempDir(), ff)
	ws := lockWS(t)
	p := filepath.Join(ws, "CLAUDE.md")
	if _, err := x.hold(ws, claudeFile(t, ws), "bx", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := y.hold(ws, claudeFile(t, ws), "by", ""); !errors.Is(err, ErrUnboxableWorkspace) {
		t.Fatalf("the second instance must be refused, got %v", err)
	}
	if len(y.all()) != 0 {
		t.Fatalf("the refused instance records nothing: %+v", y.all())
	}
	x.releaseBox("bx")
	if ff.flagged(t, p) {
		t.Fatal("the first instance lifts its own lock when its box ends")
	}
	got, err := y.hold(ws, claudeFile(t, ws), "by", "")
	if err != nil || !slices.Equal(got, []string{"CLAUDE.md"}) || !ff.flagged(t, p) {
		t.Fatalf("with the first's lock gone the second locks it as its own: %v %v", got, err)
	}
	if _, err := x.hold(ws, claudeFile(t, ws), "bx", ""); !errors.Is(err, ErrUnboxableWorkspace) {
		t.Fatalf("now the first instance is refused, got %v", err)
	}
}

// #246: the read-flag, set-flag, record step is serialised across processes
// by an exclusive flock on the guarded file: while another process holds it,
// the spawn waits, then is refused; once it is free, the lock is taken.
func TestGuardLocksWaitForAnotherLocker(t *testing.T) {
	old := guardFlockWait
	guardFlockWait = 200 * time.Millisecond
	t.Cleanup(func() { guardFlockWait = old })
	_, _, ff := lockMgr(t)
	g := newGuardLocks(t.TempDir(), ff)
	ws := lockWS(t)
	p := filepath.Join(ws, "CLAUDE.md")
	other, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := syscall.Flock(int(other.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	_, err = g.hold(ws, claudeFile(t, ws), "b1", "")
	if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "being locked") {
		t.Fatalf("want a refusal while another process locks it, got %v", err)
	}
	if ff.flagged(t, p) || len(g.all()) != 0 {
		t.Fatal("a refused hold sets and records nothing")
	}
	if err := syscall.Flock(int(other.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if _, err := g.hold(ws, claudeFile(t, ws), "b1", ""); err != nil || !ff.flagged(t, p) {
		t.Fatalf("free again: locked, got %v", err)
	}
}

// #247 (3): the flag is set before the record says ours, so a crash between
// the two can never leave a record of ours on an unflagged file (whose flag,
// set later by the user, the next sweep would clear). A record that cannot be
// written undoes the flags this call set and refuses.
func TestGuardLocksFlagBeforeRecord(t *testing.T) {
	_, _, ff := lockMgr(t)
	dir := t.TempDir()
	g := newGuardLocks(dir, ff)
	ws := lockWS(t)
	p := filepath.Join(ws, "CLAUDE.md")
	ff.onSet = func() {
		rs, err := ReadGuardLocks(dir)
		if err != nil || len(rs) != 0 {
			t.Errorf("a record was written before the flag was set: %+v %v", rs, err)
		}
	}
	if _, err := g.hold(ws, claudeFile(t, ws), "b1", ""); err != nil {
		t.Fatal(err)
	}
	ff.onSet = nil
	if rs := readLocksAt(t, dir); len(rs) != 1 || !rs[0].Ours || !ff.flagged(t, p) {
		t.Fatalf("after the hold: flagged and recorded as ours: %+v", rs)
	}
	g.releaseBox("b1")

	// The record cannot be written: the flag just set is cleared again.
	if err := os.Mkdir(filepath.Join(dir, guardLocksFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := g.hold(ws, claudeFile(t, ws), "b1", ""); !errors.Is(err, ErrUnboxableWorkspace) {
		t.Fatalf("an unwritable record refuses: %v", err)
	}
	if ff.flagged(t, p) || len(g.all()) != 0 {
		t.Fatalf("a refused hold leaves no flag and no record: %+v", g.all())
	}
}

func readLocksAt(t *testing.T, dir string) []GuardLock {
	t.Helper()
	rs, err := ReadGuardLocks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}
