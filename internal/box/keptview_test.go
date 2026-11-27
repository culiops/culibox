package box

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// keptSeedStore writes st as the state.json of store key, made fresh.
func keptSeedStore(t *testing.T, stateRoot, key string, st KeptState) {
	t.Helper()
	store, err := ensureKeptStore(stateRoot, key)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := saveKeptState(store, st); err != nil {
		t.Fatal(err)
	}
}

func keptViewEntry(rel string, bytes int64) KeptEntry {
	return KeptEntry{Path: rel, KeptAt: time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC), Bytes: bytes, Files: 3, Status: keptStatusKept}
}

func viewKeys(vs []KeptStoreView) []string {
	var out []string
	for _, v := range vs {
		out = append(out, v.Key)
	}
	return out
}

// Only stores whose state hashes to their key are a profile's; one that names
// "p" without hashing to its key is listed for doctor with no profile.
func TestKeptStoresFilter(t *testing.T) {
	m := newTestManager(t)
	root := m.opts.StateRoot
	keptSeedStore(t, root, keptTestKey, KeptState{Profile: "p", Workspace: "/w",
		Kept: []KeptEntry{keptViewEntry(".culiops", 100), keptViewEntry(".config/culiops", 23)}})
	other := ConversationsKey("TEST", "/w2", "", "p")
	keptSeedStore(t, root, other, KeptState{Profile: "p", Workspace: "/w2"})
	q := ConversationsKey("TEST", "/w", "", "q")
	keptSeedStore(t, root, q, KeptState{Profile: "q", Workspace: "/w", Kept: []KeptEntry{keptViewEntry(".q", 1)}})
	liar := "0123456789ab"
	keptSeedStore(t, root, liar, KeptState{Profile: "p", Workspace: "/w", Kept: []KeptEntry{keptViewEntry(".evil", 1)}})

	got := m.KeptStores("p")
	want := []string{keptTestKey, other}
	slices.Sort(want)
	if !slices.Equal(viewKeys(got), want) {
		t.Fatalf("KeptStores(p) keys %v, want %v", viewKeys(got), want)
	}
	for _, v := range got {
		if v.Profile != "p" {
			t.Errorf("store %s: profile %q, want p", v.Key, v.Profile)
		}
		if v.Key == keptTestKey {
			if v.TotalBytes != 123 || len(v.Kept) != 2 || v.Workspace != "/w" {
				t.Errorf("store %s: %+v, want 2 kept, 123 bytes, workspace /w", v.Key, v)
			}
		}
		if v.Kept == nil || v.NotKept == nil || v.Clashes == nil {
			t.Errorf("store %s: nil slices %+v: JSON must carry [] not null", v.Key, v)
		}
	}
	if got := m.KeptStores("q"); !slices.Equal(viewKeys(got), []string{q}) {
		t.Fatalf("KeptStores(q) keys %v, want [%s]", viewKeys(got), q)
	}
	if got := m.KeptStores(""); len(got) != 0 {
		t.Fatalf(`KeptStores("") = %v, want none`, viewKeys(got))
	}

	all, err := ReadKeptStores(root, "TEST")
	if err != nil {
		t.Fatalf("ReadKeptStores: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("ReadKeptStores: %d stores, want 4", len(all))
	}
	for _, v := range all {
		if v.Key == liar && (v.Profile != "" || v.Workspace != "") {
			t.Fatalf("store %s does not hash to its key, yet names profile %q workspace %q", liar, v.Profile, v.Workspace)
		}
	}

	if _, ok := m.KeptStore("p", keptTestKey); !ok {
		t.Fatal("KeptStore(p, own key) not found")
	}
	for _, k := range []string{q, liar, "nothex", ConversationsKey("TEST", "/none", "", "p")} {
		if _, ok := m.KeptStore("p", k); ok {
			t.Errorf("KeptStore(p, %q) found, want refused", k)
		}
	}
}

// At most keptViewMaxStores stores are read.
func TestKeptStoresBounded(t *testing.T) {
	keptHook(t, &keptViewMaxStores, 2)
	m := newTestManager(t)
	for _, ws := range []string{"/a", "/b", "/c"} {
		keptSeedStore(t, m.opts.StateRoot, ConversationsKey("TEST", ws, "", "p"), KeptState{Profile: "p", Workspace: ws})
	}
	all, err := ReadKeptStores(m.opts.StateRoot, "TEST")
	if len(all) != 2 || err == nil {
		t.Fatalf("ReadKeptStores: %d stores, err %v; want 2 and an error saying more are there", len(all), err)
	}
}

// The view reads state.json, never a kept folder: sizes come from the state.
func TestKeptStoresNoWalk(t *testing.T) {
	m := newTestManager(t)
	keptSeedStore(t, m.opts.StateRoot, keptTestKey, KeptState{Profile: "p", Workspace: "/w",
		Kept: []KeptEntry{keptViewEntry(".culiops", 1<<30)}})
	shome := keptStoreHomeDir(m)
	keptWrite(t, shome, ".culiops/a/b/c.txt", "small")
	var lists, opens []string
	keptHook(t, &keptViewListHook, func(rel string) { lists = append(lists, rel) })
	keptHook(t, &keptOpenHook, func(rel string) { opens = append(opens, rel) })
	keptHook(t, &openDirHook, func(rel string) { opens = append(opens, rel) })
	got := m.KeptStores("p")
	if len(got) != 1 || got[0].TotalBytes != 1<<30 {
		t.Fatalf("KeptStores: %+v, want one store of 1 GiB (from the state)", got)
	}
	for _, p := range opens {
		if p == keptHomeDirName || strings.HasPrefix(p, keptHomeDirName+"/") || strings.Contains(p, "/"+keptHomeDirName) {
			t.Errorf("the view opened %q under the store's home/", p)
		}
	}
	for _, l := range lists {
		if l != keptDirName && l != keptTestKey+"/"+keptClashDirName {
			t.Errorf("the view listed %q; only .kept and the store's clash/ may be listed", l)
		}
	}
}

// A clash folder of a kept path is listed, by its store-relative path.
func TestKeptStoresClashes(t *testing.T) {
	m := newTestManager(t)
	keptSeedStore(t, m.opts.StateRoot, keptTestKey, KeptState{Profile: "p", Workspace: "/w",
		Kept: []KeptEntry{keptViewEntry(".culiops", 1), keptViewEntry(".config/x", 1)}})
	store := KeptStoreDir(m.opts.StateRoot, keptTestKey)
	keptWrite(t, store, "clash/"+keptTestBox+"/.culiops/ledger.json", "old")
	keptWrite(t, store, "clash/ab-box-aaaaaaaaaaaa/.config/x/y", "old")
	keptWrite(t, store, "clash/not-a-box/.culiops/z", "junk")
	keptWrite(t, store, "clash/ab-box-bbbbbbbbbbbb/.culiops", "a file, not a folder")
	got := m.KeptStores("p")
	if len(got) != 1 {
		t.Fatalf("KeptStores: %d stores", len(got))
	}
	want := []string{"clash/ab-box-aaaaaaaaaaaa/.config/x", "clash/" + keptTestBox + "/.culiops"}
	slices.Sort(want)
	if !slices.Equal(got[0].Clashes, want) {
		t.Fatalf("Clashes %v, want %v", got[0].Clashes, want)
	}
}

func TestKeptSplitPath(t *testing.T) {
	for _, c := range []struct{ in, folder, inner string }{
		{".culiops", ".culiops", "."},
		{".culiops/a/b.txt", ".culiops", "a/b.txt"},
		{".config/culiops/c", ".config/culiops", "c"},
		{".local/share/x", ".local/share/x", "."},
		{".local/state/x/y/z", ".local/state/x", "y/z"},
		{"clash/" + keptTestBox + "/.culiops/l.json", "clash/" + keptTestBox + "/.culiops", "l.json"},
	} {
		f, in, ok := KeptSplitPath(c.in)
		if !ok || f != c.folder || in != c.inner {
			t.Errorf("KeptSplitPath(%q) = %q, %q, %v; want %q, %q", c.in, f, in, ok, c.folder, c.inner)
		}
	}
	for _, bad := range []string{"", ".", "/abs", "../x", ".culiops/../.ssh", ".config", ".local/share", ".ssh/x",
		".claude", "clash/x/.culiops", "clash/" + keptTestBox, "clash/" + keptTestBox + "/.config", "culiops", ".culiops//a", ".culiops/"} {
		if f, in, ok := KeptSplitPath(bad); ok {
			t.Errorf("KeptSplitPath(%q) = %q, %q; want refused", bad, f, in)
		}
	}
}

func TestKeptOpenRootRefusesBadRel(t *testing.T) {
	m := newTestManager(t)
	keptSeedStore(t, m.opts.StateRoot, keptTestKey, KeptState{Profile: "p", Workspace: "/w"})
	store := KeptStoreDir(m.opts.StateRoot, keptTestKey)
	shome := filepath.Join(store, keptHomeDirName)
	keptWrite(t, shome, ".culiops/a.txt", "ok")
	keptWrite(t, store, "clash/"+keptTestBox+"/.culiops/old.txt", "old")
	outside := t.TempDir()
	keptWrite(t, outside, "foo/secret", "SECRET")
	keptSymlink(t, outside, shome, ".config")        // a link in a middle component
	keptSymlink(t, outside+"/foo", shome, ".linked") // a link as the folder
	keptMkdir(t, shome, ".inner")
	keptSymlink(t, ".culiops", shome, ".swapped") // a link inside the store
	keptWrite(t, shome, "inroot/share/x/f", "x")
	keptSymlink(t, "inroot", shome, ".local") // an in-store link in a middle component

	r, err := m.KeptOpenRoot(keptTestKey, ".culiops")
	if err != nil {
		t.Fatalf("KeptOpenRoot(.culiops): %v", err)
	}
	if b, err := r.ReadFile("a.txt"); err != nil || string(b) != "ok" {
		t.Fatalf("read through the root: %q %v", b, err)
	}
	r.Close()
	r, err = m.KeptOpenRoot(keptTestKey, "clash/"+keptTestBox+"/.culiops")
	if err != nil {
		t.Fatalf("KeptOpenRoot(clash): %v", err)
	}
	r.Close()

	for _, rel := range []string{"", ".", "/etc", "../x", ".culiops/../.config", ".config", ".config/foo", ".linked",
		".swapped", ".ssh", ".claude", ".culiops/sub", "clash/x/.culiops", "clash/" + keptTestBox + "/.config",
		"clash/" + keptTestBox + "/../" + keptTestBox + "/.culiops", ".missing", ".local/share/x"} {
		if r, err := m.KeptOpenRoot(keptTestKey, rel); err == nil {
			r.Close()
			t.Errorf("KeptOpenRoot(%q) opened, want refused", rel)
		}
	}
	for _, key := range []string{"", "../x", "ZZZZZZZZZZZZ", ConversationsKey("TEST", "/none", "", "p")} {
		if r, err := m.KeptOpenRoot(key, ".culiops"); err == nil {
			r.Close()
			t.Errorf("KeptOpenRoot(key %q) opened, want refused", key)
		}
	}

	// The store itself replaced by a link is refused too.
	m2 := newTestManager(t)
	real := filepath.Join(m2.opts.StateRoot, "elsewhere")
	keptWrite(t, real, "home/.culiops/a.txt", "x")
	keptMkdir(t, m2.opts.StateRoot, stateDirName+"/"+keptDirName)
	keptSymlink(t, real, m2.opts.StateRoot, stateDirName+"/"+keptDirName+"/"+keptTestKey)
	if r, err := m2.KeptOpenRoot(keptTestKey, ".culiops"); err == nil {
		r.Close()
		t.Fatal("KeptOpenRoot through a linked store opened, want refused")
	}
}

// keptForgetSetup is a store with .culiops kept, its files and clash copies,
// and the record of keptTestBox with the store's key.
func keptForgetSetup(t *testing.T) (*Manager, *fakeClient, *keptAuditLog, string) {
	t.Helper()
	m, f, _, audit := keptPassSetup(t)
	keptSeedState(t, m, KeptState{Profile: "p", Workspace: "/w",
		Kept:    []KeptEntry{keptViewEntry(".culiops", 5), keptViewEntry(".config/other", 1)},
		NotKept: []KeptRefusalRow{{Path: ".culiops", Reason: "a secret was found", Box: keptTestBox, SeenAt: time.Now()}}})
	store := KeptStoreDir(m.opts.StateRoot, keptTestKey)
	keptWrite(t, store, "home/.culiops/a.txt", "hello")
	keptWrite(t, store, "home/.config/other/b.txt", "stay")
	keptWrite(t, store, "clash/"+keptTestBox+"/.culiops/ledger.json", "old")
	keptWrite(t, store, "clash/"+keptTestBox+"/.culiops~2/ledger.json", "older")
	keptWrite(t, store, "clash/"+keptTestBox+"/.culiops-not-a-copy/x", "stay")
	keptWrite(t, store, "clash/ab-box-aaaaaaaaaaaa/.culiops/z", "old")
	keptWrite(t, store, "clash/"+keptTestBox+"/~1/.culiops/f", "a copy with no room under its own path")
	keptWrite(t, store, "clash/"+keptTestBox+"/~1/.config/other/f", "stay")
	return m, f, audit, store
}

// keptFolderThere fails the test when a refused forget touched the folder or
// the forgotten list.
func keptFolderThere(t *testing.T, m *Manager, store string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(store, "home/.culiops/a.txt")); err != nil {
		t.Fatalf("a refused forget removed the folder: %v", err)
	}
	if st := keptLoad(t, m); len(st.Forgotten) != 0 {
		t.Fatalf("a refused forget recorded %v as forgotten", st.Forgotten)
	}
}

// Only a live session refuses a forget; the box it runs in is left alone.
func TestKeptForgetRefusesSessions(t *testing.T) {
	m, f, _, store := keptForgetSetup(t)
	f.mu.Lock()
	f.running[keptTestBox] = true
	f.mu.Unlock()
	m.mu.Lock()
	m.active[keptTestBox] = 1
	m.mu.Unlock()
	err := m.KeptForget(context.Background(), keptTestKey, ".culiops", "web")
	if !errors.Is(err, ErrKeptSessions) || !errors.Is(err, ErrKeptInUse) {
		t.Fatalf("KeptForget with a session in a box of the store: %v, want ErrKeptSessions (and ErrKeptInUse)", err)
	}
	keptFolderThere(t, m, store)
	f.mu.Lock()
	stopped := slices.Clone(f.stopped)
	f.mu.Unlock()
	if len(stopped) != 0 {
		t.Fatalf("a forget refused for a session stopped %v", stopped)
	}

	m.mu.Lock()
	m.active[keptTestBox] = 0
	m.mu.Unlock()
	f.mu.Lock()
	f.inspectErr = errors.New("docker is down")
	f.mu.Unlock()
	if err := m.KeptForget(context.Background(), keptTestKey, ".culiops", "web"); !errors.Is(err, ErrKeptRunUnknown) {
		t.Fatalf("KeptForget when docker cannot say: %v, want ErrKeptRunUnknown", err)
	}
	keptFolderThere(t, m, store)
}

// A box that only outlives its last session (the idle TTL) is removed, as a
// spawn removes an idle box in its way, and the forget goes on. The box's
// keep pass then runs after the forget, and does not keep the folder again.
func TestKeptForgetRemovesIdleBox(t *testing.T) {
	m, f, audit, store := keptForgetSetup(t)
	keptWrite(t, m.stateDir(keptTestBox), ".culiops/in-home", "x")
	f.mu.Lock()
	f.running[keptTestBox] = true
	f.mu.Unlock()
	// A running box of another store is not this forget's.
	otherBox := "ab-box-cccccccccccc"
	if err := writeKeptBoxRecord(m.opts.StateRoot, otherBox, keptBoxRecord{Key: "0123456789ab"}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.running[otherBox] = true
	f.mu.Unlock()

	done := make(chan error, 1)
	go func() { done <- m.KeptForget(context.Background(), keptTestKey, ".culiops", "web") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("KeptForget with an idle box of the store: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("KeptForget did not return: the removed box's stop waits on the store's lock")
	}
	f.mu.Lock()
	stopped, otherRuns := slices.Clone(f.stopped), f.running[otherBox]
	f.mu.Unlock()
	if !slices.Equal(stopped, []string{keptTestBox}) || !otherRuns {
		t.Fatalf("stopped %v (another store's box running: %v); want only %s removed", stopped, otherRuns, keptTestBox)
	}
	if _, err := os.Lstat(filepath.Join(store, "home/.culiops")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the folder is still in the store: %v", err)
	}
	passed := make(chan struct{})
	go func() { m.WaitKeptPasses(); close(passed) }()
	select {
	case <-passed:
	case <-time.After(10 * time.Second):
		t.Fatal("the removed box's keep pass never finished")
	}
	if _, err := os.Lstat(filepath.Join(store, "home/.culiops")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the stopped box's keep pass kept the forgotten folder again: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.stateDir(keptTestBox), ".culiops/in-home")); err != nil {
		t.Fatalf("the keep pass moved the forgotten folder out of the home: %v", err)
	}
	if rows := audit.of(keptAuditForget); len(rows) != 1 || rows[0].actor != "web" {
		t.Fatalf("forget audit rows %+v, want one by web", rows)
	}
}

// An idle box that will not go refuses the forget, with nothing changed.
func TestKeptForgetIdleBoxWillNotStop(t *testing.T) {
	m, f, _, store := keptForgetSetup(t)
	f.mu.Lock()
	f.running[keptTestBox] = true
	f.stopFails = 1
	f.mu.Unlock()
	err := m.KeptForget(context.Background(), keptTestKey, ".culiops", "web")
	if !errors.Is(err, ErrKeptStopping) || !errors.Is(err, ErrKeptInUse) {
		t.Fatalf("KeptForget when the idle box will not stop: %v, want ErrKeptStopping (and ErrKeptInUse)", err)
	}
	keptFolderThere(t, m, store)
}

// A record that cannot be read could be the store's: its box counts as one.
func TestKeptForgetUnreadableRecordCounts(t *testing.T) {
	m, f, _, store := keptForgetSetup(t)
	bad := "ab-box-dddddddddddd"
	keptWrite(t, filepath.Join(m.opts.StateRoot, stateDirName, keptDirName, keptBoxesDirName), bad, "{not json")
	f.mu.Lock()
	f.running[bad] = true
	f.mu.Unlock()
	m.mu.Lock()
	m.active[bad] = 1
	m.mu.Unlock()
	if err := m.KeptForget(context.Background(), keptTestKey, ".culiops", "web"); !errors.Is(err, ErrKeptSessions) {
		t.Fatalf("KeptForget with a session in a box whose record is unreadable: %v, want ErrKeptSessions", err)
	}
	keptFolderThere(t, m, store)
	m.mu.Lock()
	m.active[bad] = 0
	m.mu.Unlock()
	if err := m.KeptForget(context.Background(), keptTestKey, ".culiops", "web"); err != nil {
		t.Fatalf("KeptForget with that box idle: %v", err)
	}
	f.mu.Lock()
	stopped := slices.Clone(f.stopped)
	f.mu.Unlock()
	if !slices.Equal(stopped, []string{bad}) {
		t.Fatalf("stopped %v, want the idle box %s removed", stopped, bad)
	}
	m.WaitKeptPasses()
}

// Records of boxes long gone cost a forget nothing: one docker listing, and
// no inspect for a box that is not in it.
func TestKeptForgetStaleRecordsListOnce(t *testing.T) {
	m, f, _, _ := keptForgetSetup(t)
	for i := range 500 {
		name := fmt.Sprintf("ab-box-%012x", 0xe00000000000+i)
		if err := writeKeptBoxRecord(m.opts.StateRoot, name, keptBoxRecord{Key: keptTestKey}); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	f.listCalls, f.inspectCalls = 0, 0
	f.mu.Unlock()
	if err := m.KeptForget(context.Background(), keptTestKey, ".culiops", "web"); err != nil {
		t.Fatalf("KeptForget: %v", err)
	}
	f.mu.Lock()
	lists, inspects := f.listCalls, f.inspectCalls
	f.mu.Unlock()
	if lists > 1 || inspects != 0 {
		t.Fatalf("a forget with 500 records of gone boxes listed %d times and inspected %d boxes, want <= 1 and 0", lists, inspects)
	}

	// A listing that fails is a run that cannot be told.
	f.mu.Lock()
	f.listErr = errors.New("docker is down")
	f.mu.Unlock()
	if err := m.KeptForget(context.Background(), keptTestKey, ".config/other", "web"); !errors.Is(err, ErrKeptRunUnknown) {
		t.Fatalf("KeptForget when the listing fails: %v, want ErrKeptRunUnknown", err)
	}
}

// Every box of the store is inspected before any is removed: when one cannot
// be read, the others are left as they are.
func TestKeptForgetRunUnknownRemovesNothing(t *testing.T) {
	m, f, _, store := keptForgetSetup(t)
	const boxB = "ab-box-ffffffffffff"
	if err := writeKeptBoxRecord(m.opts.StateRoot, boxB, keptBoxRecord{Key: keptTestKey}); err != nil {
		t.Fatal(err)
	}
	// Whichever box is inspected first runs; the next inspect fails.
	inspects := 0
	f.mu.Lock()
	f.running[keptTestBox], f.running[boxB] = true, true
	f.inspectHook = func(string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		inspects++
		f.inspectErr = nil
		if inspects > 1 {
			f.inspectErr = errors.New("docker did not answer")
		}
	}
	f.mu.Unlock()
	if err := m.KeptForget(context.Background(), keptTestKey, ".culiops", "web"); !errors.Is(err, ErrKeptRunUnknown) {
		t.Fatalf("KeptForget when one box cannot be inspected: %v, want ErrKeptRunUnknown", err)
	}
	f.mu.Lock()
	stopped, aRuns, bRuns := slices.Clone(f.stopped), f.running[keptTestBox], f.running[boxB]
	f.mu.Unlock()
	if len(stopped) != 0 || !aRuns || !bRuns {
		t.Fatalf("stopped %v (running: %v, %v), want nothing removed", stopped, aRuns, bRuns)
	}
	keptFolderThere(t, m, store)
}

func TestKeptForgetRemovesAndRemembers(t *testing.T) {
	m, _, audit, store := keptForgetSetup(t)
	// A read-only tree (a Go module cache) needs makeRemovable's retry.
	keptWrite(t, store, "home/.culiops/ro/sub/f", "x")
	if err := os.Chmod(filepath.Join(store, "home/.culiops/ro/sub"), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(store, "home/.culiops/ro"), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := m.KeptForget(context.Background(), keptTestKey, ".culiops", "web"); err != nil {
		t.Fatalf("KeptForget: %v", err)
	}
	for _, p := range []string{"home/.culiops", "clash/" + keptTestBox + "/.culiops", "clash/" + keptTestBox + "/.culiops~2",
		"clash/ab-box-aaaaaaaaaaaa/.culiops", "clash/" + keptTestBox + "/~1/.culiops"} {
		if _, err := os.Lstat(filepath.Join(store, p)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s still there after forget (%v)", p, err)
		}
	}
	for _, p := range []string{"home/.config/other/b.txt", "clash/" + keptTestBox + "/.culiops-not-a-copy/x",
		"clash/" + keptTestBox + "/~1/.config/other/f"} {
		if _, err := os.Lstat(filepath.Join(store, p)); err != nil {
			t.Errorf("%s removed by the forget of .culiops: %v", p, err)
		}
	}
	st := keptLoad(t, m)
	if _, ok := keptEntry(st, ".culiops"); ok {
		t.Fatal("the forgotten folder is still a kept entry")
	}
	if _, ok := keptEntry(st, ".config/other"); !ok {
		t.Fatal("the other kept folder lost its entry")
	}
	if _, ok := keptRow(st, ".culiops"); ok {
		t.Fatal("the forgotten folder still has a not-kept row")
	}
	if !slices.Equal(st.Forgotten, []string{".culiops"}) {
		t.Fatalf("Forgotten %v, want [.culiops]", st.Forgotten)
	}
	rows := audit.of(keptAuditForget)
	if want := `key="` + keptTestKey + `" path=".culiops"`; len(rows) != 1 || rows[0].name != "p" || rows[0].detail != want || rows[0].actor != "web" {
		t.Fatalf("audit rows %+v, want one box_kept_forget by web for p, %s", rows, want)
	}

	// The next pass leaves the folder in the box home: it is never kept again.
	home := m.stateDir(keptTestBox)
	keptWrite(t, home, ".culiops/again", "new")
	mustPass(t, m, keptTestBox)
	if _, err := os.Lstat(filepath.Join(store, "home/.culiops")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a pass after forget kept .culiops again (%v)", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".culiops/again")); err != nil {
		t.Fatalf("the pass moved the forgotten folder out of the home: %v", err)
	}
	if st := keptLoad(t, m); !slices.Equal(st.Forgotten, []string{".culiops"}) {
		t.Fatalf("Forgotten after the pass %v", st.Forgotten)
	}

	// Forgetting it again finds nothing.
	if err := m.KeptForget(context.Background(), keptTestKey, ".culiops", "web"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("second forget: %v, want fs.ErrNotExist", err)
	}
}

// Forget removes links as links: nothing outside the store is touched.
func TestKeptForgetNoFollow(t *testing.T) {
	m, _, _, store := keptForgetSetup(t)
	outside := t.TempDir()
	keptWrite(t, outside, "keep.txt", "OUTSIDE")
	keptWrite(t, outside, "foo/keep.txt", "OUTSIDE")
	keptWrite(t, outside, ".culiops/keep.txt", "OUTSIDE")
	keptSymlink(t, outside, store, "home/.culiops/out")         // a link inside the kept folder
	keptSymlink(t, outside, store, "clash/ab-box-eeeeeeeeeeee") // a clash folder that is a link
	if err := os.RemoveAll(filepath.Join(store, "home/.config")); err != nil {
		t.Fatal(err)
	}
	keptSymlink(t, outside, store, "home/.config") // a link where a level should be
	if err := m.KeptForget(context.Background(), keptTestKey, ".culiops", "web"); err != nil {
		t.Fatalf("KeptForget: %v", err)
	}
	if err := m.KeptForget(context.Background(), keptTestKey, ".config/foo", "web"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("KeptForget through a linked level: %v, want fs.ErrNotExist", err)
	}
	for _, p := range []string{"keep.txt", "foo/keep.txt", ".culiops/keep.txt"} {
		if b, err := os.ReadFile(filepath.Join(outside, p)); err != nil || string(b) != "OUTSIDE" {
			t.Fatalf("outside %s touched by forget: %q %v", p, b, err)
		}
	}
}

// Bad keys and paths are refused before any lock or docker call.
func TestKeptForgetRefusesBadRel(t *testing.T) {
	m, f, _, _ := keptForgetSetup(t)
	f.mu.Lock()
	f.inspectErr = errors.New("must not be asked")
	f.mu.Unlock()
	for _, c := range []struct{ key, rel string }{{keptTestKey, "../x"}, {keptTestKey, ".config"}, {keptTestKey, ".ssh"},
		{keptTestKey, "clash/" + keptTestBox + "/.culiops"}, {"../etc", ".culiops"}, {"", ".culiops"}} {
		if err := m.KeptForget(context.Background(), c.key, c.rel, "web"); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("KeptForget(%q, %q): %v, want fs.ErrNotExist", c.key, c.rel, err)
		}
	}
}

// TakeKeptNews hands each newly kept folder out once: the first call returns
// the paths not yet announced and marks them, the second returns none.
func TestTakeKeptNewsOnce(t *testing.T) {
	m := newTestManager(t)
	seen := keptViewEntry(".old", 1)
	seen.Announced = true
	keptSeedStore(t, m.opts.StateRoot, keptTestKey, KeptState{Profile: "p", Workspace: "/w",
		Kept: []KeptEntry{keptViewEntry(".culiops", 100), seen, keptViewEntry(".local/share/culiops", 5)}})

	got := m.TakeKeptNews(keptTestKey)
	if !slices.Equal(got, []string{".culiops", ".local/share/culiops"}) {
		t.Fatalf("first TakeKeptNews = %v, want the two unannounced paths", got)
	}
	if again := m.TakeKeptNews(keptTestKey); len(again) != 0 {
		t.Fatalf("second TakeKeptNews = %v, want none", again)
	}
	// The marks are on disk, not only in memory.
	store, err := keptOpenStore(m.opts.StateRoot, keptTestKey)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	st, err := loadKeptState(store)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range st.Kept {
		if !e.Announced {
			t.Errorf("%s still not announced after TakeKeptNews", e.Path)
		}
	}
	// A bad key or an unknown store answers none and creates nothing.
	if got := m.TakeKeptNews("nope"); len(got) != 0 {
		t.Fatalf("bad key: %v", got)
	}
	if got := m.TakeKeptNews(ConversationsKey("TEST", "/none", "", "p")); len(got) != 0 {
		t.Fatalf("unknown store: %v", got)
	}
}

// It never waits for a forget that holds the store's lock: a spawn must not
// stall behind a big removal. The news stays for the next spawn.
func TestTakeKeptNewsDoesNotWaitOnStoreLock(t *testing.T) {
	old := keptNewsWait
	keptNewsWait = 50 * time.Millisecond
	defer func() { keptNewsWait = old }()
	m := newTestManager(t)
	keptSeedStore(t, m.opts.StateRoot, keptTestKey, KeptState{Profile: "p", Workspace: "/w",
		Kept: []KeptEntry{keptViewEntry(".culiops", 100)}})
	mu := m.keptStoreLock(keptTestKey)
	mu.Lock()
	start := time.Now()
	got := m.TakeKeptNews(keptTestKey)
	mu.Unlock()
	if len(got) != 0 || time.Since(start) > time.Second {
		t.Fatalf("got %v after %v with the lock held", got, time.Since(start))
	}
	if got := m.TakeKeptNews(keptTestKey); !slices.Equal(got, []string{".culiops"}) {
		t.Fatalf("after the lock is free: %v, want the news still there", got)
	}
}

// Forget on an older-copies row removes the copies of that folder alone,
// every form toClash makes, and keeps the folder, its entry and the forgotten
// list as they are. No box is in its way: copies are never mounted.
func TestKeptForgetClashes(t *testing.T) {
	m, f, audit, store := keptForgetSetup(t)
	keptWrite(t, store, "clash/"+keptTestBox+"/~1/.culiops/fallback.json", "fallback")
	keptWrite(t, store, "clash/"+keptTestBox+"/.config/other/z", "stay")
	f.mu.Lock()
	f.running[keptTestBox] = true // a box of the store runs: copies go all the same
	f.mu.Unlock()
	m.mu.Lock()
	m.active[keptTestBox] = 1
	m.mu.Unlock()
	if err := m.KeptForgetClashes(context.Background(), keptTestKey, ".culiops", "web"); err != nil {
		t.Fatalf("KeptForgetClashes: %v", err)
	}
	for _, p := range []string{"clash/" + keptTestBox + "/.culiops", "clash/" + keptTestBox + "/.culiops~2",
		"clash/ab-box-aaaaaaaaaaaa/.culiops", "clash/" + keptTestBox + "/~1/.culiops"} {
		if _, err := os.Lstat(filepath.Join(store, p)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s still there (%v)", p, err)
		}
	}
	for _, p := range []string{"home/.culiops/a.txt", "clash/" + keptTestBox + "/.culiops-not-a-copy/x", "clash/" + keptTestBox + "/.config/other/z"} {
		if _, err := os.Lstat(filepath.Join(store, p)); err != nil {
			t.Errorf("%s removed: %v", p, err)
		}
	}
	st := keptLoad(t, m)
	if _, ok := keptEntry(st, ".culiops"); !ok || len(st.Forgotten) != 0 {
		t.Fatalf("entry or forgotten list changed: %+v", st)
	}
	f.mu.Lock()
	stopped := slices.Clone(f.stopped)
	f.mu.Unlock()
	if len(stopped) != 0 {
		t.Fatalf("a forget of older copies stopped %v", stopped)
	}
	rows := audit.of(keptAuditForget)
	want := `key="` + keptTestKey + `" path=".culiops" clash=1`
	if len(rows) != 1 || rows[0].detail != want || rows[0].actor != "web" || rows[0].name != "p" {
		t.Fatalf("audit rows %+v, want one by web for p: %s", rows, want)
	}
	// None left: nothing to forget.
	if err := m.KeptForgetClashes(context.Background(), keptTestKey, ".culiops", "web"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("again: %v, want fs.ErrNotExist", err)
	}
	for _, rel := range []string{"", ".config", "../x", ".ssh"} {
		if err := m.KeptForgetClashes(context.Background(), keptTestKey, rel, "web"); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("KeptForgetClashes(%q): %v, want fs.ErrNotExist", rel, err)
		}
	}
}

// Audit details quote every value: a folder name with a space and "=" cannot
// read as another field.
func TestKeptAuditDetailsQuoted(t *testing.T) {
	m, _, home, audit := keptPassSetup(t)
	const odd = ".x box=ab-box-000000000000"
	keptWrite(t, home, odd+"/f", "x")
	mustPass(t, m, keptTestBox)
	rows := audit.of(keptAuditKept)
	want := `key="` + keptTestKey + `" paths=[".x box=ab-box-000000000000"] box="` + keptTestBox + `"`
	if len(rows) != 1 || rows[0].detail != want {
		t.Fatalf("box_kept rows %+v, want %s", rows, want)
	}
	if err := m.KeptForget(context.Background(), keptTestKey, odd, "web"); err != nil {
		t.Fatal(err)
	}
	frows := audit.of(keptAuditForget)
	fwant := `key="` + keptTestKey + `" path=".x box=ab-box-000000000000"`
	if len(frows) != 1 || frows[0].detail != fwant {
		t.Fatalf("forget rows %+v, want %s", frows, fwant)
	}
}

// The toast tells only of folders that are kept: a held one is not mounted,
// so it is not news until it is kept.
func TestTakeKeptNewsOnlyKept(t *testing.T) {
	m, _, _, _ := keptPassSetup(t)
	st := KeptState{Profile: "p", Workspace: "/w", Kept: []KeptEntry{
		{Path: ".held", KeptAt: time.Now(), Status: keptStatusHeld, HeldReason: "looks like credentials: .held/x"},
		{Path: ".ok", KeptAt: time.Now(), Status: keptStatusKept}}}
	keptSeedState(t, m, st)
	if got := m.TakeKeptNews(keptTestKey); !slices.Equal(got, []string{".ok"}) {
		t.Fatalf("news %q, want [.ok]", got)
	}
	if e, _ := keptEntry(keptLoad(t, m), ".held"); e.Announced {
		t.Fatal("a held folder was marked told")
	}
	st.Kept[0].Status, st.Kept[0].HeldReason, st.Kept[1].Announced = keptStatusKept, "", true
	keptSeedState(t, m, st)
	if got := m.TakeKeptNews(keptTestKey); !slices.Equal(got, []string{".held"}) {
		t.Fatalf("news once kept %q, want [.held]", got)
	}
}
