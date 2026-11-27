package box

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// keptSeed writes st as store key's state.json and makes each of its kept and
// held paths a folder holding one file in the store. It returns the store's
// home/ on the host.
func keptSeed(t *testing.T, m *Manager, key string, st KeptState) string {
	t.Helper()
	store, err := ensureKeptStore(m.opts.StateRoot, key)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := saveKeptState(store, st); err != nil {
		t.Fatal(err)
	}
	shome := filepath.Join(KeptStoreDir(m.opts.StateRoot, key), keptHomeDirName)
	for _, e := range st.Kept {
		keptWrite(t, shome, e.Path+"/notes.md", "kept")
	}
	return shome
}

// keptEntries are entries with status for paths.
func keptEntries(status string, paths ...string) []KeptEntry {
	var out []KeptEntry
	for _, p := range paths {
		out = append(out, KeptEntry{Path: p, Status: status, KeptAt: time.Now()})
	}
	return out
}

// keptSrc is the host folder of rel in store key.
func keptSrc(m *Manager, key, rel string) string {
	return filepath.Join(KeptStoreDir(m.opts.StateRoot, key), keptHomeDirName, rel)
}

// keptMountArg is the --mount value of rel's kept folder from store key.
func keptMountArg(m *Manager, key, rel string) string {
	return "type=bind,src=" + keptSrc(m, key, rel) + ",dst=" + boxHome + "/" + rel
}

func mountBoxes(ms []Mount) []string {
	var out []string
	for _, mt := range ms {
		out = append(out, mt.Box)
	}
	return out
}

// stopBox ends box name's sessions and removes it as the idle timer would,
// then waits for the keep pass that follows.
func stopBox(t *testing.T, m *Manager, name string) {
	t.Helper()
	for !m.idle(name) {
		m.ReleaseBox(name)
	}
	m.ReleaseBox(name)
	m.stopIfIdle(name)
	m.WaitKeptPasses()
}

func TestRunArgsKeptMounts(t *testing.T) {
	m := newTestManager(t)
	conv := ConversationsDir(m.opts.StateRoot, keptTestKey)
	kept := []Mount{
		{Host: keptSrc(m, keptTestKey, ".culiops"), Box: boxHome + "/.culiops"},
		{Host: keptSrc(m, keptTestKey, ".local/share/culiops"), Box: boxHome + "/.local/share/culiops"},
	}
	got := m.runArgs("ab-box-abc", "/w", nil, false, false, "", DefaultMemory, conv, kept)
	at := slices.Index(got, "type=bind,src="+conv+",dst="+ConversationsBox)
	if at < 0 {
		t.Fatalf("no conversations mount: %q", got)
	}
	for _, rel := range []string{".culiops", ".local/share/culiops"} {
		want := keptMountArg(m, keptTestKey, rel)
		if !strings.Contains(want, "/"+keptDirName+"/"+keptTestKey+"/home/"+rel+",dst=/home/agent/"+rel) {
			t.Fatalf("test value %q is not the store's folder", want)
		}
		i := slices.Index(got, want)
		if i < 0 || got[i-1] != "--mount" || i < at {
			t.Fatalf("want %q read-write after the conversations mount: %q", want, got)
		}
	}
	if j := strings.Join(got, " "); strings.Contains(j, ".culiops,readonly") {
		t.Fatalf("a kept folder mounted read-only: %s", j)
	}
	if n := countFlag(got, "--mount"); n != 6 {
		t.Fatalf("want 6 mounts, got %d: %q", n, got)
	}
}

func TestKeptMountsSkipsHeld(t *testing.T) {
	m := newTestManager(t)
	st := KeptState{Profile: "p", Workspace: "/w"}
	st.Kept = append(keptEntries(keptStatusKept, ".culiops"), keptEntries(keptStatusHeld, ".held")...)
	st.Kept[1].HeldReason = "looks like credentials: .held/x"
	keptSeed(t, m, keptTestKey, st)
	got, refs := m.keptMounts(keptTestKey, nil)
	if !slices.Equal(mountBoxes(got), []string{boxHome + "/.culiops"}) || len(refs) != 0 {
		t.Fatalf("mounts %q refusals %+v, want .culiops alone and no refusal", mountBoxes(got), refs)
	}
	if got[0].Host != keptSrc(m, keptTestKey, ".culiops") || got[0].Optional {
		t.Fatalf("mount = %+v", got[0])
	}
}

// A kept folder, or a folder on its way, swapped for a link (the macOS VM can
// write boxstate) is dropped with a refusal, and what it points at is never
// opened or stat'ed.
func TestKeptMountsSymlinkSwap(t *testing.T) {
	m := newTestManager(t)
	st := KeptState{Profile: "p", Workspace: "/w", Kept: keptEntries(keptStatusKept, ".culiops", ".local/share/app", ".ok")}
	shome := keptSeed(t, m, keptTestKey, st)
	target := t.TempDir()
	keptWrite(t, target, "secret", "x")
	if err := os.RemoveAll(filepath.Join(shome, ".culiops")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(shome, ".culiops")); err != nil {
		t.Fatal(err)
	}
	mid := t.TempDir()
	keptWrite(t, mid, "share/app/notes.md", "x")
	if err := os.RemoveAll(filepath.Join(shome, ".local")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(mid, filepath.Join(shome, ".local")); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var opened []string
	keptHook(t, &keptOpenHook, func(rel string) {
		mu.Lock()
		opened = append(opened, rel)
		mu.Unlock()
	})
	got, refs := m.keptMounts(keptTestKey, nil)
	if !slices.Equal(mountBoxes(got), []string{boxHome + "/.ok"}) {
		t.Fatalf("mounts = %q, want .ok alone", mountBoxes(got))
	}
	for _, rel := range []string{".culiops", ".local/share/app"} {
		if !slices.Contains(refs, keptRefusal{Rel: rel, Reason: keptReasonLink}) {
			t.Fatalf("refusals %+v lack %s %q", refs, rel, keptReasonLink)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range opened {
		if strings.Contains(p, "/home/.culiops") || strings.Contains(p, "/home/.local") {
			t.Fatalf("opened %q, a link or what lies past it", p)
		}
	}
	if !slices.Contains(opened, keptDirName+"/"+keptTestKey+"/"+keptHomeDirName+"/.ok") {
		t.Fatalf("the hook saw no walk at all: %q", opened)
	}
}

// Through a spawn: the link's target is not mounted, nothing is made in it.
func TestEnsureKeptSymlinkSwapNotMounted(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	ws := realTemp(t)
	key := ConversationsKey("TEST", ws, "", "p")
	shome := keptSeed(t, m, key, KeptState{Profile: "p", Workspace: ws, Kept: keptEntries(keptStatusKept, ".culiops")})
	target := t.TempDir()
	if err := os.RemoveAll(filepath.Join(shome, ".culiops")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(shome, ".culiops")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.Wrap(context.Background(), ws, "s1", convKey("p"), []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	j := strings.Join(f.runArgs[0], " ")
	if hasDst(f.runArgs[0], boxHome+"/.culiops") || strings.Contains(j, target) {
		t.Fatalf("the link was mounted: %s", j)
	}
	if ents, err := os.ReadDir(target); err != nil || len(ents) != 0 {
		t.Fatalf("something was made in the link's target: %v %v", ents, err)
	}
}

func TestKeptMountsHoldsImport(t *testing.T) {
	m := newTestManager(t)
	st := KeptState{Profile: "p", Workspace: "/w", Kept: keptEntries(keptStatusKept, ".culiops", ".config/app", ".other")}
	keptSeed(t, m, keptTestKey, st)
	extra := []Mount{
		{Host: "/h/operator.md", Box: boxHome + "/.culiops/operator.md", Optional: true},
		{Host: "/h/cfg", Box: boxHome + "/.config"},
		{Host: "/h/elsewhere", Box: "/opt/x"},
	}
	got, refs := m.keptMounts(keptTestKey, extra)
	if !slices.Equal(mountBoxes(got), []string{boxHome + "/.other"}) {
		t.Fatalf("mounts = %q, want .other alone", mountBoxes(got))
	}
	if !slices.Contains(refs, keptRefusal{Rel: ".culiops", Reason: keptReasonMount}) {
		t.Fatalf("refusals %+v lack .culiops %q", refs, keptReasonMount)
	}
	if !slices.Contains(refs, keptRefusal{Rel: ".config/app", Reason: keptReasonInMount}) {
		t.Fatalf("refusals %+v lack .config/app %q", refs, keptReasonInMount)
	}
	// Letter case is ignored, as everywhere in the store (a macOS home).
	if got, _ := m.keptMounts(keptTestKey, []Mount{{Host: "/h/o.md", Box: boxHome + "/.CULIOPS/o.md"}}); slices.Contains(mountBoxes(got), boxHome+"/.culiops") {
		t.Fatalf("an import under .CULIOPS let .culiops mount: %q", mountBoxes(got))
	}
}

func TestMountsOnlyOwnKey(t *testing.T) {
	m := newTestManager(t)
	keyJ := ConversationsKey("TEST", "/w", "", "q")
	keptSeed(t, m, keptTestKey, KeptState{Profile: "p", Workspace: "/w", Kept: keptEntries(keptStatusKept, ".culiops")})
	keptSeed(t, m, keyJ, KeptState{Profile: "q", Workspace: "/w", Kept: keptEntries(keptStatusKept, ".culiops", ".jonly")})
	got, refs := m.keptMounts(keptTestKey, nil)
	if len(got) != 1 || got[0].Host != keptSrc(m, keptTestKey, ".culiops") || len(refs) != 0 {
		t.Fatalf("mounts %+v refusals %+v, want store K's .culiops alone", got, refs)
	}
	for _, key := range []string{"", "../" + keyJ, strings.ToUpper(keyJ)} {
		if got, refs := m.keptMounts(key, nil); len(got) != 0 || len(refs) != 0 {
			t.Fatalf("key %q mounted %+v", key, got)
		}
	}
	// A profile with no store yet: nothing, and no store made by asking.
	keyN := ConversationsKey("TEST", "/w", "", "new")
	if got, _ := m.keptMounts(keyN, nil); len(got) != 0 {
		t.Fatalf("a store that is not there mounted %+v", got)
	}
	if _, err := os.Lstat(KeptStoreDir(m.opts.StateRoot, keyN)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("keptMounts made a store: %v", err)
	}
}

// A state.json that cannot be read mounts nothing; one with a path that is not
// a kept folder's (the VM can write it) mounts only the valid ones.
func TestKeptMountsUntrustedState(t *testing.T) {
	m := newTestManager(t)
	shome := keptSeed(t, m, keptTestKey, KeptState{Profile: "p", Workspace: "/w"})
	keptWrite(t, shome, ".culiops/notes.md", "x")
	keptWrite(t, shome, ".aws/credentials", "x")
	keptWriteState(t, m.opts.StateRoot, map[string]any{"kept": []map[string]any{
		{"path": ".culiops", "status": "kept"},
		{"path": ".aws", "status": "kept"},
		{"path": "../../etc", "status": "kept"},
		{"path": ".claude", "status": "kept"},
	}})
	if got, _ := m.keptMounts(keptTestKey, nil); !slices.Equal(mountBoxes(got), []string{boxHome + "/.culiops"}) {
		t.Fatalf("mounts = %q, want .culiops alone", mountBoxes(got))
	}
	if err := os.WriteFile(filepath.Join(KeptStoreDir(m.opts.StateRoot, keptTestKey), keptStateName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.keptMounts(keptTestKey, nil); len(got) != 0 {
		t.Fatalf("a broken state mounted %+v", got)
	}
}

func TestEnsureWritesBoxRecord(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	ws := realTemp(t)
	imp := filepath.Join(t.TempDir(), "operator.md")
	if err := os.WriteFile(imp, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	extra := []Mount{
		{Host: imp, Box: boxHome + "/.culiops/operator.md", Optional: true, Yield: true},
		{Host: t.TempDir(), Box: "/opt/elsewhere"},
	}
	_, _, name, err := m.Wrap(context.Background(), ws, "s1", BoxKey{Profile: "p", Account: "default"}, []string{"bash"}, nil, extra)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := readKeptBoxRecord(m.opts.StateRoot, name)
	if !ok {
		t.Fatal("no box record")
	}
	if want := ConversationsKey("TEST", ws, "", "p"); rec.Key != want {
		t.Fatalf("key = %q, want %q", rec.Key, want)
	}
	for _, want := range []string{".culiops/operator.md", ".claude/projects"} {
		if !slices.Contains(rec.Mounts, want) {
			t.Fatalf("mounts %q lack %q", rec.Mounts, want)
		}
	}
	if slices.ContainsFunc(rec.Mounts, func(s string) bool { return strings.Contains(s, "elsewhere") }) {
		t.Fatalf("a mount outside the home recorded: %q", rec.Mounts)
	}
	if rec.Profile != "p" || rec.Workspace != ws || rec.Account != "default" {
		t.Fatalf("record names %q %q %q", rec.Profile, rec.Workspace, rec.Account)
	}
	fi, err := os.Lstat(filepath.Join(m.opts.StateRoot, stateDirName, keptDirName, keptBoxesDirName, name))
	if err != nil || fi.Mode().Perm() != 0o600 || !fi.Mode().IsRegular() {
		t.Fatalf("record file: %v %v", fi, err)
	}
}

func TestEnsureMountpointsForKept(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	ws := realTemp(t)
	key := ConversationsKey("TEST", ws, "", "p")
	keptSeed(t, m, key, KeptState{Profile: "p", Workspace: ws, Kept: keptEntries(keptStatusKept, ".culiops", ".local/share/app", ".linked", ".filed")})
	name, err := m.BoxName(ws, convKey("p"))
	if err != nil {
		t.Fatal(err)
	}
	home := m.stateDir(name)
	keptMkdir(t, home, ".")
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(home, ".linked")); err != nil {
		t.Fatal(err)
	}
	keptWrite(t, home, ".filed", "a file where the mountpoint belongs")
	if _, _, _, err := m.Wrap(context.Background(), ws, "s1", convKey("p"), []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	args := f.runArgs[0]
	for _, rel := range []string{".culiops", ".local/share/app"} {
		fi, err := os.Lstat(filepath.Join(home, rel))
		if err != nil || !fi.IsDir() {
			t.Fatalf("mountpoint %s: %v %v", rel, fi, err)
		}
		if ents, _ := os.ReadDir(filepath.Join(home, rel)); len(ents) != 0 {
			t.Fatalf("mountpoint %s not empty: %v", rel, ents)
		}
		if !containsPair(args, "--mount", keptMountArg(m, key, rel)) {
			t.Fatalf("%s not mounted: %q", rel, args)
		}
	}
	for _, rel := range []string{".linked", ".filed"} {
		if hasDst(args, boxHome+"/"+rel) {
			t.Fatalf("%s mounted over what the box left there: %q", rel, args)
		}
	}
	if ents, _ := os.ReadDir(elsewhere); len(ents) != 0 {
		t.Fatalf("something was made through the link: %v", ents)
	}
	rec, ok := readKeptBoxRecord(m.opts.StateRoot, name)
	if !ok {
		t.Fatal("no box record")
	}
	for _, rel := range []string{".linked", ".filed"} {
		if !slices.ContainsFunc(rec.Refused, func(r KeptRefusalRow) bool {
			return r.Path == rel && r.Reason == keptReasonMountpoint && r.Box == name
		}) {
			t.Fatalf("record refusals %+v lack %s %q", rec.Refused, rel, keptReasonMountpoint)
		}
	}
}

func TestReuseMountsIgnoresKept(t *testing.T) {
	ctx := context.Background()
	m, f := newTestManagerWithFake(t)
	ws := realTemp(t)
	key := ConversationsKey("TEST", ws, "", "p")
	keptSeed(t, m, key, KeptState{Profile: "p", Workspace: ws, Kept: keptEntries(keptStatusKept, ".culiops", ".gone")})
	_, _, name, err := m.Wrap(ctx, ws, "s1", convKey("p"), []string{"bash"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasDst(f.runArgs[0], boxHome+"/.culiops") || !hasDst(f.runArgs[0], boxHome+"/.gone") {
		t.Fatalf("kept folders not mounted: %q", f.runArgs[0])
	}
	// The store changes under the running box: one folder more, one less.
	keptSeed(t, m, key, KeptState{Profile: "p", Workspace: ws, Kept: keptEntries(keptStatusKept, ".culiops", ".newer")})
	if _, _, _, err := m.Wrap(ctx, ws, "s2", convKey("p"), []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(name)
	m.ReleaseBox(name)
	// Idle now: a mount difference would recreate it.
	if _, _, _, err := m.Wrap(ctx, ws, "s3", convKey("p"), []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(name)
	// A spawn with no profile does not ask for them either.
	if _, _, _, err := m.Wrap(ctx, ws, "s4", BoxKey{}, []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if f.runs() != 1 || f.stops() != 0 {
		t.Fatalf("a box recreated for its kept folders: runs=%d stops=%d", f.runs(), f.stops())
	}
	m.ReleaseBox(name)

	// An extra mount at a path a kept folder could have, whose source is not a
	// store's folder, is still an extra mount: the idle box is recreated.
	f.mu.Lock()
	f.mounts[name] = append(f.mounts[name], boxHome+"/.notes.md")
	f.sources[name][boxHome+"/.notes.md"] = "/h/notes.md"
	f.mu.Unlock()
	if _, _, _, err := m.Wrap(ctx, ws, "s5", convKey("p"), []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if f.runs() != 2 || f.stops() != 1 {
		t.Fatalf("an unasked mount kept the box: runs=%d stops=%d", f.runs(), f.stops())
	}
	// The recreated box mounts the store as it is now.
	if !hasDst(f.runArgs[1], boxHome+"/.newer") || hasDst(f.runArgs[1], boxHome+"/.gone") {
		t.Fatalf("recreated box mounts %q", f.runArgs[1])
	}
}

func TestKeptDstIsKeptSource(t *testing.T) {
	m := newTestManager(t)
	for _, c := range []struct {
		dst, src string
		want     bool
	}{
		{boxHome + "/.culiops", keptSrc(m, keptTestKey, ".culiops"), true},
		{boxHome + "/.local/share/app", keptSrc(m, keptTestKey, ".local/share/app"), true},
		{boxHome + "/.culiops", keptSrc(m, keptTestKey, ".other"), false},
		{boxHome + "/.culiops", "/h/.culiops", false},
		{boxHome + "/.culiops", filepath.Join(m.opts.StateRoot, stateDirName, keptDirName, "nothex", keptHomeDirName, ".culiops"), false},
		{boxHome + "/.culiops", filepath.Join(m.opts.StateRoot, stateDirName, keptDirName, keptTestKey, keptClashDirName, ".culiops"), false},
		{boxHome + "/.claude", keptSrc(m, keptTestKey, ".claude"), false},
		{"/opt/.culiops", keptSrc(m, keptTestKey, ".culiops"), false},
	} {
		rel, ok := keptDstRel(c.dst)
		got := ok && m.isKeptSource(c.src, rel)
		if got != c.want {
			t.Fatalf("%s from %s: %v, want %v", c.dst, c.src, got, c.want)
		}
	}
}

// With no profile the feature is off: docker gets exactly the argv it got
// before kept folders existed, and a record a profile's spawn left for the box
// goes, so its next stop keeps nothing.
func TestNoProfileNoKept(t *testing.T) {
	ctx := context.Background()
	m, f := newTestManagerWithFake(t)
	ws := realTemp(t)
	_, _, name, err := m.Wrap(ctx, ws, "s1", BoxKey{}, []string{"bash"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := slices.Clone(f.runArgs[0])
	if n := countFlag(before, "--mount"); n != 3 {
		t.Fatalf("want today's 3 mounts (workspace, home, out dir), got %d: %q", n, before)
	}
	mem, err := m.memoryCap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before, m.runArgs(name, ws, nil, false, false, "", mem, "", nil)) {
		t.Fatalf("argv %q is not runArgs with nothing kept", before)
	}
	stopBox(t, m, name)

	key := ConversationsKey("TEST", ws, "", "p")
	keptSeed(t, m, key, KeptState{Profile: "p", Workspace: ws, Kept: keptEntries(keptStatusKept, ".culiops")})
	if err := writeKeptBoxRecord(m.opts.StateRoot, name, keptBoxRecord{Key: key}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.Wrap(ctx, ws, "s2", BoxKey{}, []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if f.runs() != 2 || !slices.Equal(f.runArgs[1], before) {
		t.Fatalf("argv changed with no profile:\n%q\n%q", before, f.runArgs[1])
	}
	if _, err := os.Lstat(filepath.Join(m.opts.StateRoot, stateDirName, keptDirName, keptBoxesDirName, name)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the record a profile left stays: %v", err)
	}
}

// The store's state names whom it belongs to once the box's first keep pass
// ran, believed by keptStateAttribution (the box_kept audit rows and the
// profile view depend on it), and the folder kept then is mounted next time.
func TestKeptAttributionAfterEnsureAndPass(t *testing.T) {
	for _, account := range []string{"", "default"} {
		t.Run("account="+account, func(t *testing.T) {
			ctx := context.Background()
			m, f := newTestManagerWithFake(t)
			audit := &keptAuditLog{}
			m.opts.Audit = audit.record
			ws := realTemp(t)
			bk := BoxKey{Profile: "p", Account: account}
			_, _, name, err := m.Wrap(ctx, ws, "s1", bk, []string{"bash"}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			keptWrite(t, m.stateDir(name), ".culiops/notes.md", "hello")
			stopBox(t, m, name)
			key := m.ConversationsKeyFor(ws, account, "p")
			store, err := os.OpenRoot(KeptStoreDir(m.opts.StateRoot, key))
			if err != nil {
				t.Fatal(err)
			}
			st, err := loadKeptState(store)
			store.Close()
			if err != nil {
				t.Fatal(err)
			}
			p, w, _, ok := keptStateAttribution(m.opts.Instance, key, st)
			if !ok || p != "p" || w != ws {
				t.Fatalf("attribution = %q %q %v, state %+v", p, w, ok, st)
			}
			if e, ok := keptEntry(st, ".culiops"); !ok || e.Status != keptStatusKept {
				t.Fatalf("not kept: %+v", st)
			}
			if rows := audit.of(keptAuditKept); len(rows) != 1 || rows[0].name != "p" {
				t.Fatalf("audit = %+v, want one row named p", rows)
			}
			if _, _, _, err := m.Wrap(ctx, ws, "s2", bk, []string{"bash"}, nil, nil); err != nil {
				t.Fatal(err)
			}
			if f.runs() != 2 || !containsPair(f.runArgs[1], "--mount", keptMountArg(m, key, ".culiops")) {
				t.Fatalf("the kept folder is not mounted next time: %q", f.runArgs[len(f.runArgs)-1])
			}
		})
	}
}

// The record's names are believed only when they hash to its key, and never
// replace a state that already names its store.
func TestPassAttributionFromRecord(t *testing.T) {
	for _, c := range []struct {
		name      string
		state     KeptState
		rec       keptBoxRecord
		wantProf  string
		wantAttrd bool
	}{
		{"fills an empty state", KeptState{}, keptBoxRecord{Key: keptTestKey, Profile: "p", Workspace: "/w"}, "p", true},
		{"replaces a state that lies", KeptState{Profile: "evil", Workspace: "/w"}, keptBoxRecord{Key: keptTestKey, Profile: "p", Workspace: "/w"}, "p", true},
		{"a record that lies is not believed", KeptState{}, keptBoxRecord{Key: keptTestKey, Profile: "evil", Workspace: "/w"}, "", false},
		{"a good state stays", KeptState{Profile: "p", Workspace: "/w", Account: "default"}, keptBoxRecord{Key: keptTestKey, Profile: "evil", Workspace: "/w"}, "p", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, _ := newTestManagerWithFake(t)
			home := m.stateDir(keptTestBox)
			keptMkdir(t, home, ".")
			keptWrite(t, home, ".culiops/notes.md", "x")
			if err := writeKeptBoxRecord(m.opts.StateRoot, keptTestBox, c.rec); err != nil {
				t.Fatal(err)
			}
			keptSeedState(t, m, c.state)
			mustPass(t, m, keptTestBox)
			st := keptLoad(t, m)
			p, _, _, ok := keptStateAttribution("TEST", keptTestKey, st)
			if ok != c.wantAttrd || p != c.wantProf {
				t.Fatalf("attribution %q %v, want %q %v (state %+v)", p, ok, c.wantProf, c.wantAttrd, st)
			}
		})
	}
}

// A kept folder left out at create is a not-kept row once the box's keep pass
// runs: ensure never writes state.json itself (it holds opMu, which a pass
// takes while holding the store's lock).
func TestKeptMountRefusalBecomesNotKeptRow(t *testing.T) {
	ctx := context.Background()
	m, f := newTestManagerWithFake(t)
	ws := realTemp(t)
	key := ConversationsKey("TEST", ws, "", "p")
	keptSeed(t, m, key, KeptState{Profile: "p", Workspace: ws, Kept: keptEntries(keptStatusKept, ".config/app")})
	cfg := t.TempDir()
	extra := []Mount{{Host: cfg, Box: boxHome + "/.config"}}
	_, _, name, err := m.Wrap(ctx, ws, "s1", convKey("p"), []string{"bash"}, nil, extra)
	if err != nil {
		t.Fatal(err)
	}
	if hasDst(f.runArgs[0], boxHome+"/.config/app") {
		t.Fatalf("a kept folder mounted inside a host folder: %q", f.runArgs[0])
	}
	stopBox(t, m, name)
	store, err := os.OpenRoot(KeptStoreDir(m.opts.StateRoot, key))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	st, err := loadKeptState(store)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := keptRow(st, ".config/app")
	if !ok || r.Reason != keptReasonInMount || r.Box != name {
		t.Fatalf("not-kept rows %+v lack .config/app %q", st.NotKept, keptReasonInMount)
	}
	if e, ok := keptEntry(st, ".config/app"); !ok || e.Status != keptStatusKept {
		t.Fatalf("the folder itself is still kept: %+v", st.Kept)
	}
}

// A record for a box with no profile is removed without following a link.
func TestRemoveKeptBoxRecord(t *testing.T) {
	root := t.TempDir()
	if err := removeKeptBoxRecord(root, keptTestBox); err != nil {
		t.Fatalf("no boxstate: %v", err)
	}
	if err := writeKeptBoxRecord(root, keptTestBox, keptBoxRecord{Key: keptTestKey}); err != nil {
		t.Fatal(err)
	}
	if err := removeKeptBoxRecord(root, keptTestBox); err != nil {
		t.Fatal(err)
	}
	if _, ok := readKeptBoxRecord(root, keptTestBox); ok {
		t.Fatal("record still there")
	}
	if err := removeKeptBoxRecord(root, keptTestBox); err != nil {
		t.Fatalf("removing twice: %v", err)
	}
	// .boxes swapped for a link: nothing behind it is touched.
	elsewhere := t.TempDir()
	keptWrite(t, elsewhere, keptTestBox, "not ours")
	boxes := filepath.Join(root, stateDirName, keptDirName, keptBoxesDirName)
	if err := os.RemoveAll(boxes); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, boxes); err != nil {
		t.Fatal(err)
	}
	if err := removeKeptBoxRecord(root, keptTestBox); err != nil {
		t.Fatalf("a link: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(elsewhere, keptTestBox)); err != nil {
		t.Fatalf("removed through the link: %v", err)
	}
}

// BindSources reads every bind's source from one docker inspect.
func TestBindSourcesReadsEveryBind(t *testing.T) {
	const out = "bind|/w|/h/w\nbind|/home/agent/.culiops|/s/.kept/k/home/.culiops\ntmpfs|/tmp|\nvolume|/v|/var/lib/docker/v"
	bin, log := fakeDockerStderr(t, map[string]string{"inspect": out}, map[string]string{"inspect": ""}, map[string]int{"inspect": 0})
	got, err := CLIClient{Bin: bin}.BindSources(context.Background(), "ab-box-x")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"/w": "/h/w", "/home/agent/.culiops": "/s/.kept/k/home/.culiops"}
	if len(got) != len(want) || got["/w"] != want["/w"] || got["/home/agent/.culiops"] != want["/home/agent/.culiops"] {
		t.Fatalf("got %v, want %v", got, want)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "{{.Source}}") || !strings.HasSuffix(strings.TrimSpace(string(b)), "ab-box-x") {
		t.Fatalf("the inspect must ask for the sources of box ab-box-x: %s", b)
	}
	bin, _ = fakeDockerStderr(t, map[string]string{"inspect": ""}, map[string]string{"inspect": "Cannot connect to the Docker daemon"}, map[string]int{"inspect": 1})
	if _, err := (CLIClient{Bin: bin}).BindSources(context.Background(), "ab-box-x"); err == nil {
		t.Fatal("a daemon that is down answered")
	}
}

// A home copy of a kept folder that the mount hid while the box ran is kept at
// the box's stop, merged into the store: the record does not list the kept
// mounts as CuliBox's own.
func TestKeptHiddenHomeCopyKeptAtStop(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	ws := realTemp(t)
	key := ConversationsKey("TEST", ws, "", "p")
	shome := keptSeed(t, m, key, KeptState{Profile: "p", Workspace: ws, Kept: keptEntries(keptStatusKept, ".culiops")})
	name, err := m.BoxName(ws, convKey("p"))
	if err != nil {
		t.Fatal(err)
	}
	keptWrite(t, m.stateDir(name), ".culiops/older.md", "from before")
	if _, _, _, err := m.Wrap(context.Background(), ws, "s1", convKey("p"), []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !containsPair(f.runArgs[0], "--mount", keptMountArg(m, key, ".culiops")) {
		t.Fatalf("not mounted: %q", f.runArgs[0])
	}
	if rec, ok := readKeptBoxRecord(m.opts.StateRoot, name); !ok || slices.Contains(rec.Mounts, ".culiops") {
		t.Fatalf("record %+v %v lists the kept mount", rec, ok)
	}
	stopBox(t, m, name)
	if b, err := os.ReadFile(filepath.Join(shome, ".culiops", "older.md")); err != nil || string(b) != "from before" {
		t.Fatalf("the hidden home copy was not kept: %q %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(shome, ".culiops", "notes.md")); err != nil || string(b) != "kept" {
		t.Fatalf("the store's copy changed: %q %v", b, err)
	}
}

// A box created while a keep pass re-screens the store, right after its moves
// (the boot pass and the first spawns overlap), mounts what was just moved:
// the pass records the moved folders before it lets go of opMu (ruling R9).
func TestCreateRightAfterMovesMountsMoved(t *testing.T) {
	ctx := context.Background()
	m, f := newTestManagerWithFake(t)
	ws := realTemp(t)
	key := ConversationsKey("TEST", ws, "", "p")
	_, _, name, err := m.Wrap(ctx, ws, "s1", convKey("p"), []string{"bash"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	keptWrite(t, m.stateDir(name), ".culiops/notes.md", "hello")
	var seen KeptState
	keptHook(t, &keptPassMovedHook, func(box string) {
		if box != name {
			return
		}
		store, err := os.OpenRoot(KeptStoreDir(m.opts.StateRoot, key))
		if err != nil {
			t.Error(err)
			return
		}
		seen, err = loadKeptState(store)
		store.Close()
		if err != nil {
			t.Error(err)
		}
		if _, _, _, err := m.Wrap(ctx, ws, "s2", convKey("p"), []string{"bash"}, nil, nil); err != nil {
			t.Error(err)
		}
	})
	stopBox(t, m, name)
	if e, ok := keptEntry(seen, ".culiops"); !ok || e.Status != keptStatusKept || e.FromBox != name {
		t.Fatalf("state right after the moves = %+v, want .culiops kept from %s", seen, name)
	}
	if f.runs() != 2 || !containsPair(f.runArgs[1], "--mount", keptMountArg(m, key, ".culiops")) {
		t.Fatalf("a box created right after the moves does not mount the moved folder: %q", f.runArgs[len(f.runArgs)-1])
	}
	// The re-screen that follows still records it once, with its counts.
	st := func() KeptState {
		store, err := os.OpenRoot(KeptStoreDir(m.opts.StateRoot, key))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		st, err := loadKeptState(store)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}()
	if len(st.Kept) != 1 || st.Kept[0].Status != keptStatusKept || st.Kept[0].Files != 1 || st.Kept[0].Bytes != 5 {
		t.Fatalf("final state = %+v", st.Kept)
	}
}

// A moved folder the store's re-screen refuses goes from kept to held, and is
// audited as held.
func TestMovedThenHeldByRescreen(t *testing.T) {
	m, _, home, audit := keptPassSetup(t)
	keptWrite(t, home, ".culiops/notes.md", "hello")
	var seen KeptState
	keptHook(t, &keptPassMovedHook, func(string) {
		seen = keptLoad(t, m)
		// Changed in the store between the move and its re-screen.
		keptWrite(t, keptStoreHomeDir(m), ".culiops/id_rsa", "x")
	})
	mustPass(t, m, keptTestBox)
	if e, ok := keptEntry(seen, ".culiops"); !ok || e.Status != keptStatusKept {
		t.Fatalf("state right after the move = %+v", seen.Kept)
	}
	e, ok := keptEntry(keptLoad(t, m), ".culiops")
	if !ok || e.Status != keptStatusHeld || !strings.Contains(e.HeldReason, "id_rsa") {
		t.Fatalf("after the re-screen = %+v %v, want held", e, ok)
	}
	if rows := audit.of(keptAuditHeld); len(rows) != 1 {
		t.Fatalf("held audit rows = %+v", rows)
	}
}
