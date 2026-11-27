package box

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const keptTestBox = "ab-box-0123456789ab"

var keptTestKey = ConversationsKey("TEST", "/w", "", "p")

// keptStore makes the store of keptTestKey under a fresh state root.
func keptStore(t *testing.T) (stateRoot string, store *os.Root) {
	t.Helper()
	stateRoot = t.TempDir()
	store, err := ensureKeptStore(stateRoot, keptTestKey)
	if err != nil {
		t.Fatalf("ensureKeptStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return stateRoot, store
}

func TestKeptStoreDir(t *testing.T) {
	if got, want := KeptStoreDir("/s", "0123456789ab"), "/s/boxstate/.kept/0123456789ab"; got != want {
		t.Fatalf("KeptStoreDir = %q, want %q", got, want)
	}
	if isBoxName(keptDirName) || ValidConversationsKey(keptBoxesDirName) {
		t.Fatal(".kept reads as a box name, or .boxes as a store key")
	}
}

func TestEnsureKeptStoreLayout(t *testing.T) {
	stateRoot, store := keptStore(t)
	dir := KeptStoreDir(stateRoot, keptTestKey)
	for _, p := range []string{filepath.Dir(dir), dir, filepath.Join(dir, "home"), filepath.Join(dir, "clash")} {
		fi, err := os.Lstat(p)
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v, want a 0700 directory", p, fi, err)
		}
	}
	// The root is the store itself.
	if err := store.WriteFile("probe", []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "probe")); err != nil {
		t.Fatalf("the root is not the store: %v", err)
	}
	// Idempotent, and what is there stays.
	again, err := ensureKeptStore(stateRoot, keptTestKey)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	again.Close()
	if _, err := os.Lstat(filepath.Join(dir, "probe")); err != nil {
		t.Fatalf("content lost: %v", err)
	}
	for _, k := range []string{"", "..", "../../etc", "0123456789a", "0123456789AB", "0123456789ab/x", ".boxes"} {
		if r, err := ensureKeptStore(t.TempDir(), k); err == nil {
			r.Close()
			t.Errorf("key %q accepted", k)
		}
	}
}

func TestEnsureKeptStoreRefusesLink(t *testing.T) {
	for _, at := range []string{".kept", ".kept/" + keptTestKey, ".kept/" + keptTestKey + "/home", ".kept/" + keptTestKey + "/clash"} {
		t.Run(at, func(t *testing.T) {
			stateRoot := t.TempDir()
			state := filepath.Join(stateRoot, "boxstate")
			out := t.TempDir()
			keptWrite(t, out, "keep.txt", "untouched")
			inside := filepath.Join(state, "ab-box-ffffffffffff") // a home: a link that stays inside boxstate
			keptMkdir(t, state, "ab-box-ffffffffffff")
			for _, target := range []string{out, inside} {
				p := filepath.Join(state, filepath.FromSlash(at))
				keptMkdir(t, state, filepath.ToSlash(filepath.Dir(at)))
				os.RemoveAll(p)
				if err := os.Symlink(target, p); err != nil {
					t.Fatal(err)
				}
				if r, err := ensureKeptStore(stateRoot, keptTestKey); err == nil {
					r.Close()
					t.Fatalf("%s as a link to %s accepted", at, target)
				}
				ents, _ := os.ReadDir(target)
				var names []string
				for _, e := range ents {
					names = append(names, e.Name())
				}
				if target == out && !slices.Equal(names, []string{"keep.txt"}) || target == inside && len(names) != 0 {
					t.Fatalf("created through the link at %s: %v", at, names)
				}
				if b, _ := os.ReadFile(filepath.Join(out, "keep.txt")); string(b) != "untouched" {
					t.Fatalf("link target changed: %q", b)
				}
			}
			// A file there is refused too.
			p := filepath.Join(state, filepath.FromSlash(at))
			os.Remove(p)
			if err := os.WriteFile(p, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if r, err := ensureKeptStore(stateRoot, keptTestKey); err == nil {
				r.Close()
				t.Fatalf("%s as a file accepted", at)
			}
		})
	}
}

// The store swapped for a link inside boxstate after it was walked, right
// before os.Root opens it by path (which would follow that link): refused.
func TestEnsureKeptStoreSwapBeforeOpen(t *testing.T) {
	stateRoot := t.TempDir()
	state := filepath.Join(stateRoot, "boxstate")
	keptMkdir(t, state, "ab-box-ffffffffffff")
	store := filepath.Join(state, ".kept", keptTestKey)
	swapped := swapOnOpen(t, func(rel string) bool { return rel == ".kept/"+keptTestKey+"/clash" }, func() {
		if err := os.Rename(store, store+"-old"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../ab-box-ffffffffffff", store); err != nil {
			t.Fatal(err)
		}
	})
	r, err := ensureKeptStore(stateRoot, keptTestKey)
	if !*swapped {
		t.Fatal("the swap never ran")
	}
	if err == nil {
		r.Close()
		t.Fatal("a store swapped for a link was opened")
	}
	if ents, _ := os.ReadDir(filepath.Join(state, "ab-box-ffffffffffff")); len(ents) != 0 {
		t.Fatalf("created in the home the link points to: %v", ents)
	}
}

func TestValidKeptRel(t *testing.T) {
	for _, ok := range []string{
		".culiops", ".config/culiops", ".local/share/culiops", ".local/state/culiops",
		"..x", ".config/.x", ".local/share/x y", ".local/state/ünï",
	} {
		if !validKeptRel(ok) {
			t.Errorf("validKeptRel(%q) = false", ok)
		}
	}
	for _, bad := range []string{
		"", ".", "..", "../x", "/etc", "/home/agent/.culiops", "culiops", "x/.y",
		".config", ".local", ".Config", ".LOCAL", ".local/share", ".local/state", ".local/x",
		".local/share/", ".culiops/", ".culiops/x", ".config/a/b", ".local/share/a/b",
		".config/../.ssh", "./.culiops", ".culiops/.", ".config//x",
		".a,b", ".a:b", `.a"b`, ".a\nb", ".a\tb", ".a b", ".\xff",
		// CuliBox's own, caches and credential stores are never kept.
		".claude", ".Claude", ".agent-box", ".aws", ".AWS", ".ssh", ".config/gh", ".local/share/keyrings",
		".cache", ".npm", ".local/share/pnpm",
	} {
		if validKeptRel(bad) {
			t.Errorf("validKeptRel(%q) = true", bad)
		}
	}
}

func keptTestState() KeptState {
	at := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	return KeptState{
		Profile: "sre", Workspace: "/w", Account: "work",
		Kept: []KeptEntry{
			{Path: ".culiops", KeptAt: at, Bytes: 12, Files: 3, FromBox: keptTestBox, Status: "kept", Announced: true},
			{Path: ".local/share/culiops", KeptAt: at.Add(time.Minute), Bytes: 1 << 33, Files: 9000,
				FromBox: keptTestBox, Status: "held", HeldReason: "looks like credentials: .local/share/culiops/id_rsa"},
		},
		NotKept: []KeptRefusalRow{
			{Path: ".aws", Reason: "credentials", Box: keptTestBox, SeenAt: at},
			{Path: ".config", Reason: "is a link", Box: keptTestBox, SeenAt: at},
		},
		Forgotten: []string{".config/old"},
	}
}

func TestKeptStateRoundTrip(t *testing.T) {
	stateRoot, store := keptStore(t)
	dir := KeptStoreDir(stateRoot, keptTestKey)
	want := keptTestState()
	for range 2 { // the second save replaces the first
		if err := saveKeptState(store, want); err != nil {
			t.Fatalf("saveKeptState: %v", err)
		}
	}
	got, err := loadKeptState(store)
	if err != nil {
		t.Fatalf("loadKeptState: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, want)
	}
	fi, err := os.Lstat(filepath.Join(dir, "state.json"))
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state.json: %v %v, want a 0600 file", fi, err)
	}
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"clash", "home", "state.json"}) {
		t.Fatalf("store holds %v: a temp file was left behind", names)
	}
	// The JSON names the spec gives.
	b, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	for _, k := range []string{`"profile"`, `"workspace"`, `"account"`, `"kept"`, `"kept_at"`, `"from_box"`,
		`"not_kept"`, `"seen_at"`, `"forgotten"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("state.json has no %s: %s", k, b)
		}
	}
}

func TestKeptStateMissingIsEmpty(t *testing.T) {
	_, store := keptStore(t)
	got, err := loadKeptState(store)
	if err != nil || !reflect.DeepEqual(got, KeptState{}) {
		t.Fatalf("no state.json: %+v %v, want an empty state", got, err)
	}
}

// keptWriteState writes raw as the store's state.json, as a box with the VM's
// access could.
func keptWriteState(t *testing.T, stateRoot string, raw any) {
	t.Helper()
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(KeptStoreDir(stateRoot, keptTestKey), "state.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestKeptStateDropsBadPaths(t *testing.T) {
	stateRoot, store := keptStore(t)
	st := keptTestState()
	good := st.Kept[0]
	var kept []KeptEntry
	for _, p := range []string{"../x", "/etc", ".a,b", ".config", ".culiops/../.ssh", ".claude", ""} {
		e := good
		e.Path = p
		kept = append(kept, e)
	}
	kept = append(kept, good)
	dup := good
	dup.Bytes = 99
	kept = append(kept, dup) // the same path twice would be two mounts on one target
	odd := good
	odd.Path, odd.Status = ".config/odd", "mounted" // neither kept nor held
	kept = append(kept, odd)
	fb := good
	fb.Path, fb.FromBox = ".config/fb", "../../etc" // display data only, but a box name or nothing
	kept = append(kept, fb)
	st.Kept = kept
	st.Forgotten = []string{"../x", ".config/old", "/etc", ".config/old"}
	st.NotKept = append(st.NotKept,
		KeptRefusalRow{Path: ".local/share", Reason: "is a link", SeenAt: good.KeptAt}, // a level: data, kept
		KeptRefusalRow{Path: ".a,b", Reason: "name not allowed", SeenAt: good.KeptAt},  // data, kept
		KeptRefusalRow{Path: ".a\nb", Reason: "name not\x1b[31m allowed", SeenAt: good.KeptAt},
		KeptRefusalRow{Path: "", Reason: "credentials", SeenAt: good.KeptAt},
	)
	keptWriteState(t, stateRoot, st)

	got, err := loadKeptState(store)
	if err != nil {
		t.Fatalf("loadKeptState: %v", err)
	}
	fb.FromBox = ""
	if want := []KeptEntry{good, fb}; !reflect.DeepEqual(got.Kept, want) {
		t.Fatalf("kept = %+v, want %+v", got.Kept, want)
	}
	if !slices.Equal(got.Forgotten, []string{".config/old"}) {
		t.Fatalf("forgotten = %q", got.Forgotten)
	}
	var paths []string
	for _, r := range got.NotKept {
		paths = append(paths, r.Path)
		for _, s := range []string{r.Path, r.Reason, r.Box} {
			if strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
				t.Errorf("row %+v holds a control character", r)
			}
		}
	}
	if want := []string{".aws", ".config", ".local/share", ".a,b", strconv.Quote(".a\nb")}; !slices.Equal(paths, want) {
		t.Fatalf("not_kept paths = %q, want %q", paths, want)
	}
}

func TestKeptStateCapsRows(t *testing.T) {
	stateRoot, store := keptStore(t)
	at := time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)
	var st KeptState
	for i := range 60 {
		st.NotKept = append(st.NotKept, KeptRefusalRow{Path: ".p" + strconv.Itoa(i), Reason: "credentials", SeenAt: at.Add(time.Duration(i) * time.Minute)})
	}
	// An older row for a path seen again later: only the latest stays.
	st.NotKept = append(st.NotKept, KeptRefusalRow{Path: ".p59", Reason: "old", SeenAt: at})
	for i := range 40 {
		st.Kept = append(st.Kept, KeptEntry{Path: ".k" + strconv.Itoa(i), Status: "kept"})
	}
	keptWriteState(t, stateRoot, st)
	got, err := loadKeptState(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.NotKept) != 50 || got.NotKept[0].Path != ".p10" || got.NotKept[49].Path != ".p59" || got.NotKept[49].Reason != "credentials" {
		t.Fatalf("not_kept = %d rows, %+v ... %+v; want the latest 50, .p10 to .p59", len(got.NotKept), got.NotKept[0], got.NotKept[len(got.NotKept)-1])
	}
	if len(got.Kept) != 32 || got.Kept[31].Path != ".k31" {
		t.Fatalf("kept = %d entries, want the first 32", len(got.Kept))
	}
	// saveKeptState applies the same caps: the file never holds what load drops.
	if err := saveKeptState(store, st); err != nil {
		t.Fatal(err)
	}
	again, err := loadKeptState(store)
	if err != nil || !reflect.DeepEqual(again, got) {
		t.Fatalf("after save: %v, a different state", err)
	}
}

func TestKeptStateRefusesOddFile(t *testing.T) {
	stateRoot, store := keptStore(t)
	p := filepath.Join(KeptStoreDir(stateRoot, keptTestKey), "state.json")
	out := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(out, []byte(`{"profile":"followed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, p); err != nil {
		t.Fatal(err)
	}
	if got, err := loadKeptState(store); err == nil {
		t.Fatalf("a linked state.json loaded: %+v", got)
	}
	// Saving replaces the link itself, never what it points to.
	if err := saveKeptState(store, keptTestState()); err != nil {
		t.Fatalf("save over a link: %v", err)
	}
	if b, _ := os.ReadFile(out); string(b) != `{"profile":"followed"}` {
		t.Fatalf("the link target was written: %s", b)
	}
	if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("state.json after save: %v %v", fi, err)
	}

	os.Remove(p)
	if err := unix.Mkfifo(p, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := loadKeptState(store); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO state.json loaded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("loadKeptState blocked on a FIFO")
	}

	os.Remove(p)
	if err := os.WriteFile(p, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadKeptState(store); err == nil {
		t.Fatal("broken JSON loaded")
	}
	big := `{"profile":"` + strings.Repeat("x", 2<<20) + `"}`
	if err := os.WriteFile(p, []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadKeptState(store); err == nil {
		t.Fatal("a 2 MiB state.json loaded")
	}
}

func TestBoxRecordRoundTrip(t *testing.T) {
	stateRoot := t.TempDir()
	if _, ok := readKeptBoxRecord(stateRoot, keptTestBox); ok {
		t.Fatal("a record before any write")
	}
	want := keptBoxRecord{Key: keptTestKey, Mounts: []string{".culiops/operator.md", ".claude/CLAUDE.md"}}
	for range 2 {
		if err := writeKeptBoxRecord(stateRoot, keptTestBox, want); err != nil {
			t.Fatalf("writeKeptBoxRecord: %v", err)
		}
	}
	got, ok := readKeptBoxRecord(stateRoot, keptTestBox)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("read = %+v %v, want %+v", got, ok, want)
	}
	dir := filepath.Join(stateRoot, "boxstate", ".kept", ".boxes")
	for p, mode := range map[string]os.FileMode{filepath.Dir(dir): 0o700, dir: 0o700, filepath.Join(dir, keptTestBox): 0o600} {
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode().Perm() != mode {
			t.Fatalf("%s: %v %v, want mode %o", p, fi, err, mode)
		}
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf(".boxes holds %d names: a temp file was left behind", len(ents))
	}
	// No mounts is a record too.
	if err := writeKeptBoxRecord(stateRoot, keptTestBox, keptBoxRecord{Key: keptTestKey}); err != nil {
		t.Fatal(err)
	}
	if got, ok := readKeptBoxRecord(stateRoot, keptTestBox); !ok || got.Key != keptTestKey || len(got.Mounts) != 0 {
		t.Fatalf("read = %+v %v", got, ok)
	}
}

func TestBoxRecordRefusesBad(t *testing.T) {
	stateRoot := t.TempDir()
	for _, r := range []keptBoxRecord{
		{Key: ""}, {Key: "../x"}, {Key: "0123456789AB"},
		{Key: keptTestKey, Mounts: []string{"/etc/passwd"}},
		{Key: keptTestKey, Mounts: []string{"../x"}},
		{Key: keptTestKey, Mounts: []string{"."}},
		{Key: keptTestKey, Mounts: []string{".a,b/x"}},
	} {
		if err := writeKeptBoxRecord(stateRoot, keptTestBox, r); err == nil {
			t.Errorf("record %+v written", r)
		}
	}
	for _, b := range []string{"", "..", "../ab-box-0123456789ab", "ab-box-x", ".boxes"} {
		if err := writeKeptBoxRecord(stateRoot, b, keptBoxRecord{Key: keptTestKey}); err == nil {
			t.Errorf("box name %q written", b)
		}
		if _, ok := readKeptBoxRecord(stateRoot, b); ok {
			t.Errorf("box name %q read", b)
		}
	}

	// What a box with the VM's access writes there is checked again on read.
	if err := writeKeptBoxRecord(stateRoot, keptTestBox, keptBoxRecord{Key: keptTestKey}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(stateRoot, "boxstate", ".kept", ".boxes", keptTestBox)
	for _, raw := range []string{
		`{"key":"../../etc"}`,
		`{"key":"` + keptTestKey + `","mounts":["../.ssh/id_rsa"]}`,
		`{"key":"` + keptTestKey + `","mounts":["/etc"]}`,
		`not json`,
	} {
		if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, ok := readKeptBoxRecord(stateRoot, keptTestBox); ok {
			t.Errorf("record %s read as %+v", raw, got)
		}
	}
	out := filepath.Join(t.TempDir(), "rec")
	if err := os.WriteFile(out, []byte(`{"key":"`+keptTestKey+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Remove(p)
	if err := os.Symlink(out, p); err != nil {
		t.Fatal(err)
	}
	if got, ok := readKeptBoxRecord(stateRoot, keptTestBox); ok {
		t.Fatalf("a linked record read as %+v", got)
	}
	// .boxes itself as a link: neither read nor written through.
	boxes := filepath.Dir(p)
	os.RemoveAll(boxes)
	elsewhere := t.TempDir()
	keptWrite(t, elsewhere, keptTestBox, `{"key":"`+keptTestKey+`"}`)
	if err := os.Symlink(elsewhere, boxes); err != nil {
		t.Fatal(err)
	}
	if got, ok := readKeptBoxRecord(stateRoot, keptTestBox); ok {
		t.Fatalf("a record read through a linked .boxes: %+v", got)
	}
	if err := writeKeptBoxRecord(stateRoot, "ab-box-ffffffffffff", keptBoxRecord{Key: keptTestKey}); err == nil {
		t.Fatal("a record written through a linked .boxes")
	}
	if ents, _ := os.ReadDir(elsewhere); len(ents) != 1 {
		t.Fatalf("written through the link: %v", ents)
	}
}
