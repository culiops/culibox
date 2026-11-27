package box

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// keptAuditLog records what a pass hands Options.Audit.
type keptAuditLog struct {
	mu   sync.Mutex
	rows []keptAuditRow
	err  error // returned by every call
}

type keptAuditRow struct{ action, name, detail, actor string }

func (a *keptAuditLog) record(actor, action, name, detail string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rows = append(a.rows, keptAuditRow{action, name, detail, actor})
	return a.err
}

func (a *keptAuditLog) of(action string) []keptAuditRow {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []keptAuditRow
	for _, r := range a.rows {
		if r.action == action {
			out = append(out, r)
		}
	}
	return out
}

// keptPassBox makes the home of box, with a fresh marker, and its record with
// keptTestKey.
func keptPassBox(t *testing.T, m *Manager, box string) string {
	t.Helper()
	home := m.stateDir(box)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	m.touchMarker(box)
	if err := writeKeptBoxRecord(m.opts.StateRoot, box, keptBoxRecord{Key: keptTestKey}); err != nil {
		t.Fatal(err)
	}
	return home
}

// keptPassSetup is a Manager over the fake client with an audit log, and the
// home of keptTestBox with its record. The store's state names profile "p".
func keptPassSetup(t *testing.T) (*Manager, *fakeClient, string, *keptAuditLog) {
	t.Helper()
	m, f := newTestManagerWithFake(t)
	audit := &keptAuditLog{}
	m.opts.Audit = audit.record
	home := keptPassBox(t, m, keptTestBox)
	keptSeedState(t, m, KeptState{Profile: "p", Workspace: "/w"})
	return m, f, home, audit
}

// keptSeedState writes st as the store's state.json.
func keptSeedState(t *testing.T, m *Manager, st KeptState) {
	t.Helper()
	store, err := ensureKeptStore(m.opts.StateRoot, keptTestKey)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := saveKeptState(store, st); err != nil {
		t.Fatal(err)
	}
}

func keptLoad(t *testing.T, m *Manager) KeptState {
	t.Helper()
	store, err := os.OpenRoot(KeptStoreDir(m.opts.StateRoot, keptTestKey))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	st, err := loadKeptState(store)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// keptStoreHomeDir is the store's home/ on the host.
func keptStoreHomeDir(m *Manager) string {
	return filepath.Join(KeptStoreDir(m.opts.StateRoot, keptTestKey), keptHomeDirName)
}

func keptEntry(st KeptState, rel string) (KeptEntry, bool) {
	for _, e := range st.Kept {
		if e.Path == rel {
			return e, true
		}
	}
	return KeptEntry{}, false
}

func keptRow(st KeptState, rel string) (KeptRefusalRow, bool) {
	for _, r := range st.NotKept {
		if r.Path == rel {
			return r, true
		}
	}
	return KeptRefusalRow{}, false
}

func mustPass(t *testing.T, m *Manager, box string) int {
	t.Helper()
	n, err := m.keepPass(context.Background(), box)
	if err != nil {
		t.Fatalf("keepPass: %v", err)
	}
	return n
}

func keptHook[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

func TestPassKeepsAndRecords(t *testing.T) {
	m, _, home, audit := keptPassSetup(t)
	keptWrite(t, home, ".culiops/x", "hello")
	keptWrite(t, home, ".config/culiops/y", "yy")
	if n := mustPass(t, m, keptTestBox); n != 2 {
		t.Fatalf("kept %d, want 2", n)
	}
	if got := keptRead(t, keptStoreHomeDir(m), ".culiops/x"); got != "hello" {
		t.Fatalf("store .culiops/x = %q", got)
	}
	if got := keptRead(t, keptStoreHomeDir(m), ".config/culiops/y"); got != "yy" {
		t.Fatalf("store .config/culiops/y = %q", got)
	}
	keptGone(t, home, ".culiops")
	keptGone(t, home, ".config/culiops")
	st := keptLoad(t, m)
	e, ok := keptEntry(st, ".culiops")
	if !ok || e.Status != keptStatusKept || e.Bytes != 5 || e.Files != 1 || e.FromBox != keptTestBox || e.KeptAt.IsZero() || e.Announced {
		t.Fatalf("entry = %+v %v, want kept, 5 bytes, 1 file, from %s", e, ok, keptTestBox)
	}
	if st.Profile != "p" {
		t.Fatalf("profile = %q, want it kept as p", st.Profile)
	}
	rows := audit.of(keptAuditKept)
	want := keptAuditRow{keptAuditKept, "p", "paths=.config/culiops,.culiops box=" + keptTestBox, keptActorCuliBox}
	if len(rows) != 1 || rows[0] != want {
		t.Fatalf("audit = %+v, want one %+v", audit.rows, want)
	}
	fi, err := os.Lstat(filepath.Join(KeptStoreDir(m.opts.StateRoot, keptTestKey), keptStateName))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state.json: %v %v, want 0600", fi, err)
	}
	// A second pass has nothing new to keep and audits nothing more.
	if n := mustPass(t, m, keptTestBox); n != 0 {
		t.Fatalf("second pass kept %d", n)
	}
	if rows := audit.of(keptAuditKept); len(rows) != 1 {
		t.Fatalf("second pass audited again: %+v", rows)
	}
}

func TestPassSkipsRunningBox(t *testing.T) {
	m, f, home, audit := keptPassSetup(t)
	keptWrite(t, home, ".culiops/x", "x")
	f.running[keptTestBox] = true
	if n := mustPass(t, m, keptTestBox); n != 0 {
		t.Fatalf("kept %d from a running box", n)
	}
	if got := keptRead(t, home, ".culiops/x"); got != "x" {
		t.Fatal("the home changed")
	}
	delete(f.running, keptTestBox)
	m.acquire(keptTestBox) // a live session, the container not yet listed
	if n := mustPass(t, m, keptTestBox); n != 0 {
		t.Fatalf("kept %d from a box with a live session", n)
	}
	keptGone(t, keptStoreHomeDir(m), ".culiops")
	if len(audit.rows) != 0 {
		t.Fatalf("audited %+v", audit.rows)
	}
}

// A box that cannot be inspected may be running: nothing is touched, and the
// pass says so, so a reclaim keeps the home.
func TestPassInspectErrorIsAnError(t *testing.T) {
	m, f, home, _ := keptPassSetup(t)
	keptWrite(t, home, ".culiops/x", "x")
	f.inspectErr = errors.New("daemon gone")
	if _, err := m.keepPass(context.Background(), keptTestBox); err == nil {
		t.Fatal("no error when the box's state is not known")
	}
	if got := keptRead(t, home, ".culiops/x"); got != "x" {
		t.Fatal("the home changed")
	}
}

func TestPassInodeChangedNoMove(t *testing.T) {
	m, _, home, _ := keptPassSetup(t)
	keptWrite(t, home, ".culiops/x", "old")
	aside := filepath.Join(m.opts.StateRoot, "aside") // outside the home, so its inode stays taken
	keptHook(t, &keptPassLockHook, func(string) {
		if err := os.Rename(filepath.Join(home, ".culiops"), aside); err != nil {
			t.Error(err)
		}
		keptWrite(t, home, ".culiops/y", "new")
	})
	n, err := m.keepPass(context.Background(), keptTestBox)
	if n != 0 || !errors.Is(err, errKeptChanged) {
		t.Fatalf("pass = %d, %v; want 0 and the changed error", n, err)
	}
	if strings.Contains(err.Error(), "\n") || !strings.Contains(err.Error(), `".culiops"`) {
		t.Fatalf("the error must name the folder quoted, on one line: %q", err)
	}
	keptGone(t, keptStoreHomeDir(m), ".culiops")
	if got := keptRead(t, home, ".culiops/y"); got != "new" {
		t.Fatal("the swapped-in folder was touched")
	}
	st := keptLoad(t, m)
	if len(st.Kept) != 0 || len(st.NotKept) != 0 {
		t.Fatalf("state = %+v, want no entry and no not-kept row", st)
	}
	keptPassLockHook = nil
	if n := mustPass(t, m, keptTestBox); n != 1 {
		t.Fatalf("the next pass kept %d, want 1", n)
	}
	if got := keptRead(t, keptStoreHomeDir(m), ".culiops/y"); got != "new" {
		t.Fatalf("store .culiops/y = %q", got)
	}
}

func TestPassCapNoMove(t *testing.T) {
	m, _, home, _ := keptPassSetup(t)
	setCap(t, &keptScreenMaxEntries, 5)
	for i := range 10 {
		keptWrite(t, home, ".big/f"+strconv.Itoa(i), "z")
	}
	if n := mustPass(t, m, keptTestBox); n != 0 {
		t.Fatalf("kept %d", n)
	}
	keptGone(t, keptStoreHomeDir(m), ".big")
	if got := keptRead(t, home, ".big/f0"); got != "z" {
		t.Fatal("the home changed")
	}
	r, ok := keptRow(keptLoad(t, m), ".big")
	if !ok || r.Reason != keptReasonTooMany || r.Box != keptTestBox || r.SeenAt.IsZero() {
		t.Fatalf("row = %+v %v, want %q from %s", r, ok, keptReasonTooMany, keptTestBox)
	}
}

// A home that is a link (the macOS VM can write boxstate) is not a home: it is
// never followed, and nothing it points at moves.
func TestPassHomeLinkNotFollowed(t *testing.T) {
	m, _, home, _ := keptPassSetup(t)
	target := t.TempDir()
	keptWrite(t, target, ".culiops/x", "x")
	if err := os.Remove(home); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, home); err != nil {
		t.Fatal(err)
	}
	if n := mustPass(t, m, keptTestBox); n != 0 {
		t.Fatalf("kept %d through a linked home", n)
	}
	if got := keptRead(t, target, ".culiops/x"); got != "x" {
		t.Fatal("the link's target changed")
	}
	keptGone(t, keptStoreHomeDir(m), ".culiops")
}

func TestPassNoRecordNoop(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	const box = "ab-box-aaaaaaaaaaaa"
	home := m.stateDir(box)
	keptWrite(t, home, ".culiops/x", "x")
	if n := mustPass(t, m, box); n != 0 {
		t.Fatalf("kept %d", n)
	}
	if _, err := os.Lstat(filepath.Join(m.opts.StateRoot, stateDirName, keptDirName)); !os.IsNotExist(err) {
		t.Fatalf(".kept exists: %v", err)
	}
	if got := keptRead(t, home, ".culiops/x"); got != "x" {
		t.Fatal("the home changed")
	}
}

func TestPassRescreenHolds(t *testing.T) {
	m, _, _, audit := keptPassSetup(t)
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	keptSeedState(t, m, KeptState{Profile: "p", Workspace: "/w", Kept: []KeptEntry{{Path: ".culiops", KeptAt: at, Bytes: 1, Files: 1, Status: keptStatusKept}}})
	shome := keptStoreHomeDir(m)
	keptWrite(t, shome, ".culiops/notes", "n")
	keptWrite(t, shome, ".culiops/id_rsa", "k")
	mustPass(t, m, keptTestBox)
	e, _ := keptEntry(keptLoad(t, m), ".culiops")
	if e.Status != keptStatusHeld || !strings.Contains(e.HeldReason, "looks like credentials: .culiops/id_rsa") || !e.KeptAt.Equal(at) {
		t.Fatalf("entry = %+v, want held for .culiops/id_rsa, kept_at unchanged", e)
	}
	rows := audit.of(keptAuditHeld)
	want := keptAuditRow{keptAuditHeld, "p", "path=.culiops reason=looks like credentials: .culiops/id_rsa", keptActorCuliBox}
	if len(rows) != 1 || rows[0] != want {
		t.Fatalf("held audit = %+v, want one %+v", rows, want)
	}
	// Still held: no second row.
	mustPass(t, m, keptTestBox)
	if rows := audit.of(keptAuditHeld); len(rows) != 1 {
		t.Fatalf("a folder still held was audited again: %+v", rows)
	}
	if err := os.Remove(filepath.Join(shome, ".culiops", "id_rsa")); err != nil {
		t.Fatal(err)
	}
	mustPass(t, m, keptTestBox)
	e, _ = keptEntry(keptLoad(t, m), ".culiops")
	if e.Status != keptStatusKept || e.HeldReason != "" || e.Bytes != 1 || e.Files != 1 {
		t.Fatalf("entry = %+v, want kept again with the new counts", e)
	}
	if got := keptRead(t, shome, ".culiops/notes"); got != "n" {
		t.Fatal("a held folder lost a file")
	}
}

// A kept folder that is gone from the store (removed by hand) is dropped.
func TestPassDropsMissingStoreFolder(t *testing.T) {
	m, _, _, _ := keptPassSetup(t)
	keptSeedState(t, m, KeptState{Profile: "p", Workspace: "/w", Kept: []KeptEntry{{Path: ".gone", Status: keptStatusKept}}})
	mustPass(t, m, keptTestBox)
	if st := keptLoad(t, m); len(st.Kept) != 0 {
		t.Fatalf("kept = %+v, want the missing folder dropped", st.Kept)
	}
}

func TestPassAdoptsUnrecordedStoreFolder(t *testing.T) {
	m, _, _, audit := keptPassSetup(t)
	keptWrite(t, keptStoreHomeDir(m), ".foo/a", "1")
	keptWrite(t, keptStoreHomeDir(m), ".bar/id_rsa", "k")
	mustPass(t, m, keptTestBox)
	st := keptLoad(t, m)
	e, ok := keptEntry(st, ".foo")
	if !ok || e.Status != keptStatusKept || e.Bytes != 1 || e.Files != 1 || e.KeptAt.IsZero() {
		t.Fatalf("entry = %+v %v, want .foo adopted as kept", e, ok)
	}
	if e, ok := keptEntry(st, ".bar"); !ok || e.Status != keptStatusHeld {
		t.Fatalf("entry = %+v %v, want .bar adopted as held", e, ok)
	}
	if rows := audit.of(keptAuditHeld); len(rows) != 1 || !strings.HasPrefix(rows[0].detail, "path=.bar ") {
		t.Fatalf("held audit = %+v", rows)
	}
}

func TestPassRefusalRowsCapped(t *testing.T) {
	m, _, home, _ := keptPassSetup(t)
	old := time.Now().Add(-time.Hour)
	var rows []KeptRefusalRow
	for i := range 30 {
		rows = append(rows, KeptRefusalRow{Path: ".o" + strconv.Itoa(i), Reason: keptReasonLink, Box: keptTestBox, SeenAt: old.Add(time.Duration(i) * time.Second)})
	}
	keptSeedState(t, m, KeptState{Profile: "p", Workspace: "/w", NotKept: rows})
	target := t.TempDir()
	for i := range 30 {
		keptSymlink(t, target, home, ".l"+strconv.Itoa(i))
	}
	mustPass(t, m, keptTestBox)
	st := keptLoad(t, m)
	if len(st.NotKept) != 50 {
		t.Fatalf("%d rows, want 50", len(st.NotKept))
	}
	for i := range 30 {
		if _, ok := keptRow(st, ".l"+strconv.Itoa(i)); !ok {
			t.Fatalf("new row .l%d dropped", i)
		}
	}
	for i := range 30 {
		_, ok := keptRow(st, ".o"+strconv.Itoa(i))
		if want := i >= 10; ok != want {
			t.Fatalf("old row .o%d kept=%v, want %v (the latest 50)", i, ok, want)
		}
	}
}

// Ruling R2: a forgotten folder is neither screened nor kept, silently.
func TestPassSkipsForgotten(t *testing.T) {
	m, _, home, _ := keptPassSetup(t)
	keptSeedState(t, m, KeptState{Profile: "p", Workspace: "/w", Forgotten: []string{".culiops", ".gone"}})
	keptWrite(t, home, ".culiops/x", "x")
	keptSymlink(t, t.TempDir(), home, ".gone")                  // refused as a link, were it not forgotten
	keptWrite(t, keptStoreHomeDir(m), ".culiops/leftover", "l") // a forget that could not remove it
	opens := watchOpens(t)
	if n := mustPass(t, m, keptTestBox); n != 0 {
		t.Fatalf("kept %d", n)
	}
	if slices.Contains(opens(), ".culiops/x") || slices.Contains(opens(), ".culiops/leftover") {
		t.Fatalf("a forgotten folder was screened: %q", opens())
	}
	st := keptLoad(t, m)
	if len(st.Kept) != 0 || len(st.NotKept) != 0 {
		t.Fatalf("state = %+v, want nothing for a forgotten folder", st)
	}
}

func TestPassTooManyKeptFolders(t *testing.T) {
	m, _, home, _ := keptPassSetup(t)
	setCap(t, &keptMaxPaths, 2)
	keptSeedState(t, m, KeptState{Profile: "p", Workspace: "/w", Kept: []KeptEntry{{Path: ".a", Status: keptStatusKept}}})
	keptWrite(t, keptStoreHomeDir(m), ".a/old", "o")
	keptWrite(t, home, ".a/new", "n") // merges: needs no slot
	keptWrite(t, home, ".b/x", "b")   // the last slot
	keptWrite(t, home, ".c/x", "c")   // none left
	opens := watchOpens(t)
	if n := mustPass(t, m, keptTestBox); n != 2 {
		t.Fatalf("kept %d, want 2 (.a merged, .b)", n)
	}
	if slices.Contains(opens(), ".c/x") {
		t.Fatal("a folder past the cap was screened")
	}
	st := keptLoad(t, m)
	if len(st.Kept) != 2 {
		t.Fatalf("kept = %+v", st.Kept)
	}
	if r, ok := keptRow(st, ".c"); !ok || r.Reason != keptReasonFull {
		t.Fatalf("row = %+v %v, want %q", r, ok, keptReasonFull)
	}
	if got := keptRead(t, home, ".c/x"); got != "c" {
		t.Fatal(".c left the home")
	}
	if got := keptRead(t, keptStoreHomeDir(m), ".a/new"); got != "n" {
		t.Fatal(".a was not merged")
	}
	if e, _ := keptEntry(st, ".a"); e.Files != 2 {
		t.Fatalf(".a = %+v, want the merged count", e)
	}
}

// A pass out of time leaves what it did not screen in the home, with no row:
// it is an error, so a reclaim keeps the home.
func TestPassOutOfTime(t *testing.T) {
	m, _, home, _ := keptPassSetup(t)
	keptWrite(t, home, ".culiops/x", "x")
	setCap(t, &keptPassBudget, time.Nanosecond)
	if _, err := m.keepPass(context.Background(), keptTestBox); err == nil {
		t.Fatal("no error for a folder never checked")
	}
	if got := keptRead(t, home, ".culiops/x"); got != "x" {
		t.Fatal("the home changed")
	}
}

func TestPassAuditFailureStillKeeps(t *testing.T) {
	m, _, home, audit := keptPassSetup(t)
	audit.err = errors.New("audit store down")
	keptWrite(t, home, ".culiops/x", "x")
	if n := mustPass(t, m, keptTestBox); n != 1 {
		t.Fatalf("kept %d, want 1", n)
	}
	keptGone(t, home, ".culiops")
}

func TestPassNilAudit(t *testing.T) {
	m, _, home, _ := keptPassSetup(t)
	m.opts.Audit = nil
	keptWrite(t, home, ".culiops/x", "x")
	if n := mustPass(t, m, keptTestBox); n != 1 {
		t.Fatalf("kept %d, want 1", n)
	}
}

// Two passes of one store never interleave: neither loses the other's entry.
func TestPassSameStoreSerialized(t *testing.T) {
	m, _, homeA, _ := keptPassSetup(t)
	const boxB = "ab-box-bbbbbbbbbbbb"
	homeB := keptPassBox(t, m, boxB)
	keptWrite(t, homeA, ".a/x", "a")
	keptWrite(t, homeB, ".b/x", "b")
	aIn, bDone := make(chan struct{}), make(chan struct{})
	var once sync.Once
	keptHook(t, &keptPassLockHook, func(box string) {
		if box != keptTestBox {
			return
		}
		once.Do(func() { close(aIn) })
		select { // B would finish here if the passes interleaved
		case <-bDone:
		case <-time.After(300 * time.Millisecond):
		}
	})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = m.keepPass(context.Background(), keptTestBox) }()
	go func() {
		defer wg.Done()
		<-aIn
		_, _ = m.keepPass(context.Background(), boxB)
		close(bDone)
	}()
	wg.Wait()
	st := keptLoad(t, m)
	if _, ok := keptEntry(st, ".a"); !ok {
		t.Fatalf("lost .a: %+v", st.Kept)
	}
	if _, ok := keptEntry(st, ".b"); !ok {
		t.Fatalf("lost .b: %+v", st.Kept)
	}
}

// On a case-insensitive volume (macOS), a home that spells a kept folder in
// another case merges into the store's folder: it is the same folder, so it
// gets no second entry (which would be mounted twice).
func TestPassNoDuplicateEntryForSameFolder(t *testing.T) {
	m, _, home, _ := keptPassSetup(t)
	keptWrite(t, home, "probe", "p")
	if _, err := os.Lstat(filepath.Join(home, "PROBE")); err != nil {
		t.Skip("case-sensitive volume: the case-folded alias cannot be made here")
	}
	if err := os.Remove(filepath.Join(home, "probe")); err != nil {
		t.Fatal(err)
	}
	shome := keptStoreHomeDir(m)
	keptWrite(t, shome, ".culiops/old", "o")
	keptSeedState(t, m, KeptState{Profile: "p", Workspace: "/w", Kept: []KeptEntry{{Path: ".culiops", Status: keptStatusKept}}})
	keptWrite(t, home, ".Culiops/new", "n")
	mustPass(t, m, keptTestBox)
	st := keptLoad(t, m)
	if len(st.Kept) != 1 || st.Kept[0].Path != ".culiops" || st.Kept[0].Files != 2 {
		t.Fatalf("kept = %+v, want the one .culiops entry with both files", st.Kept)
	}
}

func TestKeepStoppedHomes(t *testing.T) {
	m, f, homeA, _ := keptPassSetup(t)
	const boxB, boxC = "ab-box-bbbbbbbbbbbb", "ab-box-cccccccccccc"
	homeB := keptPassBox(t, m, boxB)
	homeC := m.stateDir(boxC) // no record
	keptWrite(t, homeA, ".a/x", "a")
	keptWrite(t, homeB, ".b/x", "b")
	keptWrite(t, homeC, ".c/x", "c")
	f.running[boxB] = true
	m.KeepStoppedHomes(context.Background())
	if got := keptRead(t, keptStoreHomeDir(m), ".a/x"); got != "a" {
		t.Fatal(".a not kept")
	}
	keptGone(t, homeA, ".a")
	if got := keptRead(t, homeB, ".b/x"); got != "b" {
		t.Fatal("a running box's home changed")
	}
	if got := keptRead(t, homeC, ".c/x"); got != "c" {
		t.Fatal("a home with no record changed")
	}
}

// keptAgeHome backdates box's marker and home by age.
func keptAgeHome(t *testing.T, m *Manager, box string, age time.Duration) {
	t.Helper()
	when := time.Now().Add(-age)
	if err := os.Chtimes(m.markerPath(box), when, when); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(m.stateDir(box), when, when); err != nil {
		t.Fatal(err)
	}
}

func TestReclaimKeepsBeforeRemove(t *testing.T) {
	m, _, home, _ := keptPassSetup(t)
	keptWrite(t, home, ".culiops/x", "k")
	keptAgeHome(t, m, keptTestBox, 40*24*time.Hour)
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || done[0].Kept || done[0].Box != keptTestBox {
		t.Fatalf("done = %+v, want the home reclaimed", done)
	}
	if _, err := os.Lstat(home); !os.IsNotExist(err) {
		t.Fatalf("home still there: %v", err)
	}
	if got := keptRead(t, keptStoreHomeDir(m), ".culiops/x"); got != "k" {
		t.Fatal(".culiops was not kept before the home went")
	}

	// A move that fails keeps the home, and says why.
	const boxB = "ab-box-bbbbbbbbbbbb"
	homeB := keptPassBox(t, m, boxB)
	keptWrite(t, homeB, ".other/y", "y")
	keptAgeHome(t, m, boxB, 40*24*time.Hour)
	keptHook(t, &keptMergeMove, func(*os.Root, *os.Root, string, string, keptCandidate) (int, int, error) {
		return 0, 0, errors.New("injected")
	})
	done, err = m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || !done[0].Kept || done[0].Box != boxB || !strings.Contains(done[0].KeptWhy, `".other"`) {
		t.Fatalf("done = %+v, want %s kept with a reason naming .other", done, boxB)
	}
	if got := keptRead(t, homeB, ".other/y"); got != "y" {
		t.Fatal("the home went with a folder to keep in it")
	}
}

// keptRecordThere reports whether box has a kept-folder record.
func keptRecordThere(t *testing.T, m *Manager, box string) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(m.opts.StateRoot, stateDirName, keptDirName, keptBoxesDirName, box))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return err == nil
}

// A home that goes takes its box's record with it, and a record whose home is
// gone by any other means goes at the next reclaim: records never pile up.
func TestReclaimRemovesBoxRecords(t *testing.T) {
	m, _, home, _ := keptPassSetup(t)
	keptAgeHome(t, m, keptTestBox, 40*24*time.Hour)
	const stale, live = "ab-box-eeeeeeeeeeee", "ab-box-dddddddddddd"
	for _, b := range []string{stale, live} {
		if err := writeKeptBoxRecord(m.opts.StateRoot, b, keptBoxRecord{Key: keptTestKey}); err != nil {
			t.Fatal(err)
		}
	}
	keptMkdir(t, m.stateDir(live), ".")
	m.touchMarker(live)
	if _, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(home); !os.IsNotExist(err) {
		t.Fatalf("home still there: %v", err)
	}
	if keptRecordThere(t, m, keptTestBox) {
		t.Error("the reclaimed home's record is still there")
	}
	if keptRecordThere(t, m, stale) {
		t.Error("a record whose home is gone is still there")
	}
	if !keptRecordThere(t, m, live) {
		t.Error("the record of a home that is there was removed")
	}
}

func TestReclaimCredentialDirDoesNotHoldHome(t *testing.T) {
	m, _, home, _ := keptPassSetup(t)
	keptWrite(t, home, ".aws/credentials", "[default]\n")
	keptAgeHome(t, m, keptTestBox, 40*24*time.Hour)
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || done[0].Kept {
		t.Fatalf("done = %+v, want the home reclaimed", done)
	}
	if _, err := os.Lstat(home); !os.IsNotExist(err) {
		t.Fatalf("home still there: %v", err)
	}
	keptGone(t, keptStoreHomeDir(m), ".aws")
}

// A home with no record reclaims as before.
func TestReclaimNoRecordUnchanged(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	dir := seedHome(t, m, "ab-box-aaaaaaaaaaaa", 40*24*time.Hour)
	keptWrite(t, dir, ".culiops/x", "x")
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL)
	if err != nil || len(done) != 1 || done[0].Kept {
		t.Fatalf("done = %+v %v", done, err)
	}
	if _, err := os.Lstat(filepath.Join(m.opts.StateRoot, stateDirName, keptDirName)); !os.IsNotExist(err) {
		t.Fatalf(".kept exists: %v", err)
	}
}

func TestStoppedRunsPass(t *testing.T) {
	m, f, home, _ := keptPassSetup(t)
	keptWrite(t, home, ".culiops/x", "s")
	f.running[keptTestBox] = true
	m.stopIfIdle(keptTestBox) // the idle stop: Stop, then stopped()
	m.keptPasses.Wait()
	if got := keptRead(t, keptStoreHomeDir(m), ".culiops/x"); got != "s" {
		t.Fatal("the stop did not keep .culiops")
	}
	keptGone(t, home, ".culiops")
}

// --- fix round 1 ---

// keptCountOpens counts the screen's opens of rel, from now on.
func keptCountOpens(t *testing.T, rel string) func() int {
	t.Helper()
	var mu sync.Mutex
	n := 0
	keptHook(t, &keptOpenHook, func(p string) {
		if p == rel {
			mu.Lock()
			n++
			mu.Unlock()
		}
	})
	return func() int { mu.Lock(); defer mu.Unlock(); return n }
}

// One boot run re-screens a store's kept folders once, whatever the number
// of its homes: N homes must not cost N walks of the store.
func TestKeepStoppedHomesRescreensStoreOnce(t *testing.T) {
	m, _, _, _ := keptPassSetup(t)
	keptPassBox(t, m, "ab-box-bbbbbbbbbbbb")
	keptPassBox(t, m, "ab-box-cccccccccccc")
	keptSeedState(t, m, KeptState{Profile: "p", Workspace: "/w", Kept: []KeptEntry{{Path: ".kept1", Status: keptStatusKept}}})
	keptWrite(t, keptStoreHomeDir(m), ".kept1/f", "f")
	opens := keptCountOpens(t, ".kept1/f")
	m.KeepStoppedHomes(context.Background())
	if n := opens(); n != 1 {
		t.Fatalf("the store's kept folder was screened %d times over 3 homes, want 1", n)
	}
	// A folder a later home merges into it is screened again: it changed.
	keptWrite(t, m.stateDir("ab-box-cccccccccccc"), ".kept1/g", "g")
	opens = keptCountOpens(t, ".kept1/g")
	m.KeepStoppedHomes(context.Background())
	if n := opens(); n != 2 {
		t.Fatalf(".kept1/g opened %d times, want 2 (the home's screen, then the store's)", n)
	}
	if got := keptRead(t, keptStoreHomeDir(m), ".kept1/g"); got != "g" {
		t.Fatal(".kept1 was not merged")
	}
}

// The boot trigger runs in the background: it returns before the pass ends.
func TestStartKeepStoppedHomesDoesNotBlock(t *testing.T) {
	m, _, home, _ := keptPassSetup(t)
	keptWrite(t, home, ".culiops/x", "x")
	release := make(chan struct{})
	keptHook(t, &keptPassLockHook, func(string) { <-release })
	done := make(chan struct{})
	go func() { m.StartKeepStoppedHomes(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("StartKeepStoppedHomes waited for the pass")
	}
	close(release)
	m.WaitKeptPasses()
	if got := keptRead(t, keptStoreHomeDir(m), ".culiops/x"); got != "x" {
		t.Fatal("the background pass did not keep .culiops")
	}
}

func TestKeptStateAttribution(t *testing.T) {
	good := KeptState{Profile: "p", Workspace: "/w"}
	if p, w, a, ok := keptStateAttribution("TEST", keptTestKey, good); !ok || p != "p" || w != "/w" || a != "" {
		t.Fatalf("good = %q %q %q %v", p, w, a, ok)
	}
	// "default" is the default account, as ConversationsKey has it.
	if _, _, _, ok := keptStateAttribution("TEST", keptTestKey, KeptState{Profile: "p", Workspace: "/w", Account: "default"}); !ok {
		t.Fatal("the default account spelled out does not attribute")
	}
	for _, st := range []KeptState{
		{Profile: "evil", Workspace: "/w"},
		{Profile: "p", Workspace: "/other"},
		{Profile: "p", Workspace: "/w", Account: "work"},
		{},
	} {
		if p, w, a, ok := keptStateAttribution("TEST", keptTestKey, st); ok || p != "" || w != "" || a != "" {
			t.Fatalf("%+v attributed: %q %q %q %v", st, p, w, a, ok)
		}
	}
	if _, _, _, ok := keptStateAttribution("PROD", keptTestKey, good); ok {
		t.Fatal("another instance's key attributed")
	}
}

// A state.json that names a profile the store's key does not hash to (the
// macOS VM can write it) is not believed: the audit row says "unknown".
func TestPassAuditNameUnknownWhenStateLies(t *testing.T) {
	m, _, home, audit := keptPassSetup(t)
	keptSeedState(t, m, KeptState{Profile: "someone-else", Workspace: "/w"})
	keptWrite(t, home, ".culiops/x", "x")
	mustPass(t, m, keptTestBox)
	rows := audit.of(keptAuditKept)
	if len(rows) != 1 || rows[0].name != "unknown" {
		t.Fatalf("audit = %+v, want one row named unknown", rows)
	}
}

// A merge whose every file clashes leaves the state as it was, yet data left
// the home: it is audited.
func TestPassAuditsClashOnlyMove(t *testing.T) {
	m, _, home, audit := keptPassSetup(t)
	keptSeedState(t, m, KeptState{Profile: "p", Workspace: "/w", Kept: []KeptEntry{{Path: ".culiops", Bytes: 1, Files: 1, Status: keptStatusKept}}})
	keptWrite(t, keptStoreHomeDir(m), ".culiops/a", "1")
	keptWrite(t, home, ".culiops/a", "2")
	if n := mustPass(t, m, keptTestBox); n != 1 {
		t.Fatalf("kept %d, want 1", n)
	}
	keptGone(t, home, ".culiops")
	if got := keptRead(t, filepath.Join(KeptStoreDir(m.opts.StateRoot, keptTestKey), keptTestClash), ".culiops/a"); got != "2" {
		t.Fatalf("clash copy = %q", got)
	}
	if rows := audit.of(keptAuditKept); len(rows) != 1 || rows[0].detail != "paths=.culiops box="+keptTestBox {
		t.Fatalf("audit = %+v, want the clash-only move audited", audit.rows)
	}
}

// A record that is there but cannot be read is an error, so reclaim keeps
// the home rather than taking its folders with it.
func TestPassRecordUnreadableIsError(t *testing.T) {
	m, _, home, _ := keptPassSetup(t)
	keptWrite(t, home, ".culiops/x", "x")
	rec := filepath.Join(m.opts.StateRoot, stateDirName, keptDirName, keptBoxesDirName, keptTestBox)
	if err := os.WriteFile(rec, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.keepPass(context.Background(), keptTestBox); err == nil {
		t.Fatal("no error for an unreadable record")
	}
	keptAgeHome(t, m, keptTestBox, 40*24*time.Hour)
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL)
	if err != nil || len(done) != 1 || !done[0].Kept || done[0].KeptWhy == "" {
		t.Fatalf("done = %+v %v, want the home kept with a reason", done, err)
	}
	if got := keptRead(t, home, ".culiops/x"); got != "x" {
		t.Fatal("the home went")
	}
}

// The wait for opMu ends with the pass's context: a pass never waits forever
// behind a stuck spawn.
func TestPassLockWaitEndsWithContext(t *testing.T) {
	m, _, home, _ := keptPassSetup(t)
	keptWrite(t, home, ".culiops/x", "x")
	m.opMu.Lock()
	defer m.opMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := m.keepPass(ctx, keptTestBox); err == nil {
		t.Fatal("no error when opMu could not be had in time")
	}
	if got := keptRead(t, home, ".culiops/x"); got != "x" {
		t.Fatal("the home changed")
	}
}

// Moving a top-level folder out does not make a marker-less home look used:
// its mtime is its age.
func TestPassKeepsHomeMtime(t *testing.T) {
	m, _, home, _ := keptPassSetup(t)
	keptWrite(t, home, ".culiops/x", "x")
	if err := os.Remove(m.markerPath(keptTestBox)); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-40 * 24 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(home, old, old); err != nil {
		t.Fatal(err)
	}
	if n := mustPass(t, m, keptTestBox); n != 1 {
		t.Fatalf("kept %d, want 1", n)
	}
	fi, err := os.Lstat(home)
	if err != nil || !fi.ModTime().Equal(old) {
		t.Fatalf("home mtime %v %v, want %v", fi.ModTime(), err, old)
	}
}

// Ruling R8: at reclaim, a folder whose screen ran out of time keeps the home
// for the next reclaim; one that hit the entry cap goes with it, not kept.
func TestReclaimScreenTimeCapKeepsHome(t *testing.T) {
	m, _, home, _ := keptPassSetup(t)
	keptWrite(t, home, ".culiops/x", "x")
	keptWrite(t, home, ".culiops/y", "y") // the cap is checked between entries
	keptAgeHome(t, m, keptTestBox, 40*24*time.Hour)
	setCap(t, &keptScreenTimeout, time.Millisecond)
	slow := func(p string) {
		if strings.HasPrefix(p, ".culiops/") {
			time.Sleep(20 * time.Millisecond)
		}
	}
	keptHook(t, &keptOpenHook, slow)
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL)
	if err != nil || len(done) != 1 || !done[0].Kept || !strings.Contains(done[0].KeptWhy, `".culiops"`) {
		t.Fatalf("done = %+v %v, want the home kept for .culiops", done, err)
	}
	if got := keptRead(t, home, ".culiops/x"); got != "x" {
		t.Fatal("the home went")
	}
	// A stop pass treats the same time cap as not kept, with a row.
	keptOpenHook = slow
	if _, err := m.keepPass(context.Background(), keptTestBox); err != nil {
		t.Fatalf("stop pass: %v", err)
	}
	if r, ok := keptRow(keptLoad(t, m), ".culiops"); !ok || r.Reason != "too slow to check" {
		t.Fatalf("row = %+v %v, want too slow to check", r, ok)
	}
}

func TestReclaimScreenEntryCapDoesNotHoldHome(t *testing.T) {
	m, _, home, _ := keptPassSetup(t)
	setCap(t, &keptScreenMaxEntries, 2)
	for i := range 5 {
		keptWrite(t, home, ".big/f"+strconv.Itoa(i), "z")
	}
	keptAgeHome(t, m, keptTestBox, 40*24*time.Hour)
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL)
	if err != nil || len(done) != 1 || done[0].Kept {
		t.Fatalf("done = %+v %v, want the home reclaimed", done, err)
	}
	if _, err := os.Lstat(home); !os.IsNotExist(err) {
		t.Fatalf("home still there: %v", err)
	}
}
