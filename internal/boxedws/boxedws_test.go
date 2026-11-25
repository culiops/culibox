package boxedws

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/culiops/culibox/internal/datacompat"
)

func none(string) (string, bool) { return "", false }

// mkdir makes a real directory under t.TempDir and returns its resolved path.
func mkdir(t *testing.T, name string) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := filepath.EvalSymlinks(d)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestOpenAbsent(t *testing.T) {
	r, seeded, err := Open(t.TempDir(), none)
	if err != nil {
		t.Fatal(err)
	}
	if seeded {
		t.Fatal("seeded = true for an absent file")
	}
	if got := r.List(); len(got) != 0 {
		t.Fatalf("List() = %v, want empty", got)
	}
}

func TestAddPersists(t *testing.T) {
	data := t.TempDir()
	ws := mkdir(t, "ws")
	r, _, err := Open(data, none)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Add(ws); err != nil {
		t.Fatal(err)
	}
	r2, seeded, err := Open(data, none)
	if err != nil {
		t.Fatal(err)
	}
	if seeded {
		t.Fatal("Add alone must not mark the registry seeded")
	}
	if got := r2.List(); !reflect.DeepEqual(got, []string{ws}) {
		t.Fatalf("List() = %v, want [%s]", got, ws)
	}
	fi, err := os.Stat(filepath.Join(data, "boxed-workspaces.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
	ents, _ := os.ReadDir(data)
	if len(ents) != 1 {
		t.Fatalf("data dir has leftovers: %v", ents)
	}
}

func TestAddDedupesDuplicateAndAlias(t *testing.T) {
	data := t.TempDir()
	ws := mkdir(t, "ws")
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(ws, alias); err != nil {
		t.Fatal(err)
	}
	r, _, _ := Open(data, none)
	for _, d := range []string{ws, ws, alias} {
		if err := r.Add(d); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.List(); !reflect.DeepEqual(got, []string{ws}) {
		t.Fatalf("List() = %v, want one entry %s", got, ws)
	}
}

func TestAddSkipsUnboxable(t *testing.T) {
	data := t.TempDir()
	bad, ok := mkdir(t, "bad"), mkdir(t, "ok")
	r, _, _ := Open(data, func(p string) (string, bool) {
		if p == bad {
			return "protected", true
		}
		return "", false
	})
	if err := r.Add(bad); err != nil {
		t.Fatalf("refused entry must be skipped, got error %v", err)
	}
	if err := r.Add(ok); err != nil {
		t.Fatal(err)
	}
	if got := r.List(); !reflect.DeepEqual(got, []string{ok}) {
		t.Fatalf("List() = %v, want [%s]", got, ok)
	}
}

func TestAddSkipsMissingDir(t *testing.T) {
	data := t.TempDir()
	r, _, _ := Open(data, none)
	if err := r.Add(filepath.Join(t.TempDir(), "gone")); err != nil {
		t.Fatalf("missing dir must be skipped, got error %v", err)
	}
	if got := r.List(); len(got) != 0 {
		t.Fatalf("List() = %v, want empty", got)
	}
}

func TestOpenCorruptFails(t *testing.T) {
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, "boxed-workspaces.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(data, none); err == nil {
		t.Fatal("corrupt file must fail closed")
	}
}

func TestOpenUnknownVersionFails(t *testing.T) {
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, "boxed-workspaces.json"), []byte(`{"version":2,"workspaces":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(data, none); err == nil {
		t.Fatal("unknown version must fail closed")
	}
}

// A version-0 file is not "from the future": it keeps the unknown-version
// error, without the newer marker.
func TestOpenVersionZeroIsNotNewer(t *testing.T) {
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, "boxed-workspaces.json"), []byte(`{"version":0,"workspaces":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := Open(data, none)
	if err == nil {
		t.Fatal("version 0 must fail closed")
	}
	if errors.Is(err, datacompat.ErrNewer) {
		t.Fatalf("version 0 must not read as newer: %v", err)
	}
}

// A file from a newer CuliBox is named as such even when its body no longer
// fits this build's types, and it is left untouched.
func TestOpenNewerVersionSaysNewer(t *testing.T) {
	for _, body := range []string{
		`{"version":2,"workspaces":[]}`,
		`{"version":2,"workspaces":{"a":1}}`,
	} {
		data := t.TempDir()
		p := filepath.Join(data, "boxed-workspaces.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := Open(data, none)
		if !errors.Is(err, datacompat.ErrNewer) {
			t.Fatalf("%s: err = %v, want ErrNewer", body, err)
		}
		if after, _ := os.ReadFile(p); string(after) != body {
			t.Fatalf("%s: file was rewritten to %s", body, after)
		}
	}
}

func TestSeedEmptyWritesFile(t *testing.T) {
	data := t.TempDir()
	r, _, _ := Open(data, none)
	if err := r.Seed(nil, true); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(data, "boxed-workspaces.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Compact vs indented is not the contract; the content is.
	if got := string(b); got != "{\"version\":1,\"seeded\":true,\"workspaces\":[]}" && got != "{\"version\":1,\"seeded\":true,\"workspaces\":[]}\n" {
		t.Fatalf("file = %q", got)
	}
	if _, seeded, _ := Open(data, none); !seeded {
		t.Fatal("a complete seed must read back as seeded")
	}
}

func TestSeedAddsAllSorted(t *testing.T) {
	data := t.TempDir()
	a, b := mkdir(t, "a"), mkdir(t, "b")
	r, _, _ := Open(data, none)
	if err := r.Seed([]string{b, a, b}, true); err != nil {
		t.Fatal(err)
	}
	want := []string{a, b}
	if a > b {
		want = []string{b, a}
	}
	if got := r.List(); !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %v, want %v", got, want)
	}
}

func TestListReturnsCopy(t *testing.T) {
	data := t.TempDir()
	ws := mkdir(t, "ws")
	r, _, _ := Open(data, none)
	_ = r.Add(ws)
	l := r.List()
	l[0] = "mutated"
	if got := r.List(); got[0] != ws {
		t.Fatalf("List() exposed internal slice: %v", got)
	}
}

func TestAddDoesNotFollowPlantedSymlink(t *testing.T) {
	// The data dir sits next to box-writable dirs: a symlink planted at the
	// file's path (or the temp name) must not redirect the write outside it.
	data := t.TempDir()
	outside := filepath.Join(t.TempDir(), "victim")
	r, _, err := Open(data, none)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(data, "boxed-workspaces.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(data, fmt.Sprintf("boxed-workspaces.json.tmp.%d", os.Getpid()))); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(mkdir(t, "ws")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(outside); err == nil {
		t.Fatal("write followed a planted symlink out of the data dir")
	}
	if _, _, err := Open(data, none); err != nil {
		t.Fatalf("file must be a regular file again after Add: %v", err)
	}
}

func TestConcurrentAdd(t *testing.T) {
	data := t.TempDir()
	r, _, _ := Open(data, none)
	var dirs []string
	for i := 0; i < 8; i++ {
		dirs = append(dirs, mkdir(t, fmt.Sprintf("ws%d", i)))
	}
	var wg sync.WaitGroup
	for _, d := range dirs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.Add(d); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := r.List(); len(got) != 8 {
		t.Fatalf("List() has %d entries, want 8: %v", len(got), got)
	}
	r2, _, err := Open(data, none)
	if err != nil {
		t.Fatal(err)
	}
	if got := r2.List(); len(got) != 8 {
		t.Fatalf("persisted %d entries, want 8", len(got))
	}
}

func TestIncompleteSeedStaysUnseeded(t *testing.T) {
	// The docker listing failed: record what was found, but a later boot must
	// retry, so the flag is not set and survives re-Open.
	data := t.TempDir()
	a := mkdir(t, "a")
	r, _, _ := Open(data, none)
	if err := r.Seed([]string{a}, false); err != nil {
		t.Fatal(err)
	}
	r2, seeded, err := Open(data, none)
	if err != nil {
		t.Fatal(err)
	}
	if seeded {
		t.Fatal("an incomplete seed must re-Open as unseeded")
	}
	if got := r2.List(); !reflect.DeepEqual(got, []string{a}) {
		t.Fatalf("the partial seed must still be recorded: %v", got)
	}
}

func TestAddPreservesSeededFlag(t *testing.T) {
	data := t.TempDir()
	r, _, _ := Open(data, none)
	if err := r.Seed(nil, true); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(mkdir(t, "ws")); err != nil {
		t.Fatal(err)
	}
	if _, seeded, _ := Open(data, none); !seeded {
		t.Fatal("Add dropped the seeded flag")
	}
}

func TestSeedNeverFlipsTrueToFalse(t *testing.T) {
	data := t.TempDir()
	r, _, _ := Open(data, none)
	if err := r.Seed(nil, true); err != nil {
		t.Fatal(err)
	}
	if err := r.Seed([]string{mkdir(t, "late")}, false); err != nil {
		t.Fatal(err)
	}
	if _, seeded, _ := Open(data, none); !seeded {
		t.Fatal("an incomplete re-seed must not clear seeded")
	}
}

func TestOpenV1WithoutSeededFieldIsUnseeded(t *testing.T) {
	// Files written before the flag existed: re-seed (additive, harmless).
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, "boxed-workspaces.json"), []byte(`{"version":1,"workspaces":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, seeded, err := Open(data, none); err != nil || seeded {
		t.Fatalf("seeded=%v err=%v, want false/nil", seeded, err)
	}
}

// statusOf is InPlaceStatus's status alone.
func statusOf(r *Registry, p string) string {
	st, _ := r.InPlaceStatus(p)
	return st
}

func TestInPlaceStatus(t *testing.T) {
	data := t.TempDir()
	plain, guarded, unknown := mkdir(t, "plain"), mkdir(t, "guarded"), mkdir(t, "unknown")
	r, _, err := Open(data, none)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Add(unknown); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordInPlace(plain, false); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordInPlace(guarded, true); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(plain, "sub", "deep")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(plain, "not-yet", "there")
	for name, tc := range map[string]struct{ p, want string }{
		"equal":               {plain, StatusNonGit},
		"inside":              {inside, StatusNonGit},
		"inside, not on disk": {missing, StatusNonGit},
		"guarded":             {guarded, StatusNone},
		"no flag":             {unknown, StatusUnknown},
		"elsewhere":           {mkdir(t, "elsewhere"), StatusNone},
		"parent of an entry":  {filepath.Dir(plain), StatusNone},
	} {
		if got := statusOf(r, tc.p); got != tc.want {
			t.Errorf("%s: InPlaceStatus(%s) = %q, want %q", name, tc.p, got, tc.want)
		}
	}
	// It survives a restart.
	r2, _, err := Open(data, none)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusOf(r2, plain); got != StatusNonGit {
		t.Fatalf("after reopen: %q", got)
	}
	if got := statusOf(r2, unknown); got != StatusUnknown {
		t.Fatalf("after reopen, no flag: %q", got)
	}
	if got := r2.List(); len(got) != 3 {
		t.Fatalf("List = %v", got)
	}
}

func TestInPlaceStatusSymlinkedSpelling(t *testing.T) {
	ws := mkdir(t, "ws")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(ws, link); err != nil {
		t.Fatal(err)
	}
	r, _, _ := Open(t.TempDir(), none)
	if err := r.RecordInPlace(ws, false); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(r, filepath.Join(link, "x")); got != StatusNonGit {
		t.Fatalf("a symlinked spelling: %q", got)
	}
}

// The strictest status wins: an entry nested in another, one recorded false.
func TestInPlaceStatusNestedEntries(t *testing.T) {
	outer := mkdir(t, "outer")
	inner := filepath.Join(outer, "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	r, _, _ := Open(t.TempDir(), none)
	if err := r.Add(outer); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordInPlace(inner, false); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(r, inner); got != StatusNonGit {
		t.Fatalf("inner: %q", got)
	}
	if got := statusOf(r, outer); got != StatusUnknown {
		t.Fatalf("outer: %q", got)
	}
}

// A false stays false: a later record of true, or a plain Add, does not clear it.
func TestInPlaceFalseIsSticky(t *testing.T) {
	data := t.TempDir()
	ws := mkdir(t, "ws")
	r, _, _ := Open(data, none)
	if err := r.RecordInPlace(ws, false); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordInPlace(ws, true); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(ws); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(r, ws); got != StatusNonGit {
		t.Fatalf("status = %q", got)
	}
	// An entry recorded without a flag takes the flag on a later in-place spawn.
	other := mkdir(t, "other")
	if err := r.Add(other); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordInPlace(other, false); err != nil {
		t.Fatal(err)
	}
	r2, _, _ := Open(data, none)
	if got := statusOf(r2, other); got != StatusNonGit {
		t.Fatalf("after reopen: %q", got)
	}
}

// An older file (no git_guarded map) loads, every entry is unknown, and the
// first write keeps the entries and the seeded flag.
func TestOpenOldFileWithoutGuardMap(t *testing.T) {
	data := t.TempDir()
	ws := mkdir(t, "ws")
	old := fmt.Sprintf(`{"version":1,"seeded":true,"workspaces":[%q]}`, ws)
	if err := os.WriteFile(filepath.Join(data, fileName), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	r, seeded, err := Open(data, none)
	if err != nil || !seeded {
		t.Fatalf("Open: seeded=%v err=%v", seeded, err)
	}
	if got := statusOf(r, ws); got != StatusUnknown {
		t.Fatalf("status = %q, want unknown", got)
	}
	other := mkdir(t, "other")
	if err := r.RecordInPlace(other, false); err != nil {
		t.Fatal(err)
	}
	r2, seeded, err := Open(data, none)
	if err != nil || !seeded {
		t.Fatalf("reopen: seeded=%v err=%v", seeded, err)
	}
	if got := r2.List(); len(got) != 2 {
		t.Fatalf("List = %v", got)
	}
	if statusOf(r2, ws) != StatusUnknown || statusOf(r2, other) != StatusNonGit {
		t.Fatal("flags lost on rewrite")
	}
}

// A newer file's flag shape is not this build's business: a malformed map is a
// parse error (fail closed), as the rest of the file is.
func TestOpenMalformedGuardMapFailsClosed(t *testing.T) {
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, fileName), []byte(`{"version":1,"workspaces":[],"git_guarded":[1]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(data, none); err == nil {
		t.Fatal("a malformed git_guarded map opened")
	}
}

// A flag whose entry is not listed is dropped, never a way to invent an entry.
func TestOpenIgnoresFlagWithoutEntry(t *testing.T) {
	data := t.TempDir()
	ws := mkdir(t, "ws")
	body := fmt.Sprintf(`{"version":1,"workspaces":[],"git_guarded":{%q:false}}`, ws)
	if err := os.WriteFile(filepath.Join(data, fileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	r, _, err := Open(data, none)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusOf(r, ws); got != StatusNone {
		t.Fatalf("status = %q", got)
	}
}

func TestRecordInPlaceSkipsRefused(t *testing.T) {
	ws := mkdir(t, "ws")
	r, _, _ := Open(t.TempDir(), func(string) (string, bool) { return "no", true })
	if err := r.RecordInPlace(ws, false); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(r, ws); got != StatusNone || len(r.List()) != 0 {
		t.Fatalf("a refused entry was recorded: %q %v", got, r.List())
	}
}

// The entry that decided the status comes back with it: the innermost one of
// that status, whatever the map's order. That is the key the refusal tells the
// user to look at (#371 C2).
func TestInPlaceStatusNamesTheEntry(t *testing.T) {
	outer := mkdir(t, "outer")
	mid := filepath.Join(outer, "mid")
	inner := filepath.Join(mid, "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	r, _, _ := Open(t.TempDir(), none)
	if err := r.RecordInPlace(outer, false); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordInPlace(mid, false); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(inner); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if st, e := r.InPlaceStatus(filepath.Join(inner, "repo")); st != StatusNonGit || e != mid {
			t.Fatalf("InPlaceStatus = %q, %q; want nongit and the innermost nongit entry %s", st, e, mid)
		}
	}
	if st, e := r.InPlaceStatus(outer); st != StatusNonGit || e != outer {
		t.Fatalf("outer: %q, %q", st, e)
	}
	if st, e := r.InPlaceStatus(mkdir(t, "elsewhere")); st != StatusNone || e != "" {
		t.Fatalf("elsewhere: %q, %q", st, e)
	}
	r2, _, _ := Open(t.TempDir(), none)
	if err := r2.Add(outer); err != nil {
		t.Fatal(err)
	}
	if st, e := r2.InPlaceStatus(inner); st != StatusUnknown || e != outer {
		t.Fatalf("unknown: %q, %q", st, e)
	}
}

// The release the refusal describes: the user sets the entry's git_guarded to
// true in the file (CuliBox stopped); the next start reads it as checked.
func TestInPlaceReleasedByHandEditedFlag(t *testing.T) {
	data := t.TempDir()
	ws := mkdir(t, "ws")
	r, _, _ := Open(data, none)
	if err := r.RecordInPlace(ws, false); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"version":1,"seeded":false,"workspaces":[%q],"git_guarded":{%q:true}}`, ws, ws)
	if err := os.WriteFile(filepath.Join(data, fileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	r2, _, err := Open(data, none)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := r2.InPlaceStatus(ws); st != StatusNone {
		t.Fatalf("after the hand edit: %q, want none", st)
	}
	if got := r2.List(); len(got) != 1 {
		t.Fatalf("the entry (the fs containment) must stay: %v", got)
	}
}

// On a case-insensitive volume (darwin) another spelling of a recorded folder
// is that folder: folding finds it, and only adds refusals (#371 S2).
func TestInPlaceStatusFoldsCase(t *testing.T) {
	old := foldCase
	t.Cleanup(func() { foldCase = old })
	ws := mkdir(t, "Code")
	r, _, _ := Open(t.TempDir(), none)
	if err := r.RecordInPlace(ws, false); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(filepath.Dir(ws), "code", "App")
	foldCase = true
	if st, e := r.InPlaceStatus(other); st != StatusNonGit || e != ws {
		t.Fatalf("folded: %q, %q; want nongit, %s", st, e, ws)
	}
	if st, _ := r.InPlaceStatus(filepath.Join(filepath.Dir(ws), "codex")); st != StatusNone {
		t.Fatalf("folded, a sibling that shares a prefix: %q", st)
	}
	foldCase = false
	if st, _ := r.InPlaceStatus(other); st != StatusNone {
		t.Fatalf("not folded: %q, want none (another folder on a case-sensitive volume)", st)
	}
}
