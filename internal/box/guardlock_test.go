package box

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

func TestGuardLocksNeverClearAUserFlag(t *testing.T) {
	_, _, ff := lockMgr(t)
	g := newGuardLocks(t.TempDir(), ff)
	ws := lockWS(t)
	p := filepath.Join(ws, "CLAUDE.md")
	ff.userSets(t, p)
	fi, _ := os.Lstat(p)
	got, err := g.hold(ws, []guardFile{{"CLAUDE.md", fi}}, "b1", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a file the user locked is not reported as CuliBox's: %v", got)
	}
	if rs := g.all(); len(rs) != 1 || rs[0].Ours {
		t.Fatalf("want one not-ours record: %+v", rs)
	}
	if g.holdsOurs("b1") {
		t.Fatal("a box holding only the user's flag must keep its idle time")
	}
	g.releaseBox("b1")
	if !ff.flagged(t, p) {
		t.Fatal("the user's own flag must never be cleared")
	}
	if len(g.all()) != 0 {
		t.Fatal("the record must go with its last box")
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
	b, _ := json.Marshal(guardLockDoc{Version: 2})
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
