package boxedws

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
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
