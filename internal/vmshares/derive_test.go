package vmshares

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func m91(env Env) ([]Profile, []string) {
	j := func(r string) string { return filepath.Join(env.Home, r) }
	return []Profile{
		{Name: "culibox", Workspace: j("code/culibox")},
		{Name: "culibox-clone", Clone: true, CloneOf: j("code/culibox"), Workspace: j("box-clones/culibox")},
		{Name: "ops", Workspace: j("code/culibox"), ContextPaths: []string{j("notes/runbooks")}},
	}, []string{j("code/infra")}
}

func TestDeriveM91Example(t *testing.T) {
	env := testEnv(t, false)
	profiles, rec := m91(env)
	got, skipped := Derive(DeriveInput{Profiles: profiles, Recorded: rec, Env: env})
	want := []Share{sh(env, "box-clones", RW), sh(env, "code/culibox", RW), sh(env, "code/infra", RW), sh(env, "notes/runbooks", RO)}
	if !reflect.DeepEqual(got, want) || len(skipped) != 0 {
		t.Fatalf("%v %v", got, skipped)
	}
	if cp := CopyParents(profiles); !reflect.DeepEqual(cp, []string{sh(env, "box-clones", RW).Path}) {
		t.Fatalf("%v", cp)
	}
}

func TestDeriveSkipsRefused(t *testing.T) {
	env := testEnv(t, false)
	profiles := []Profile{
		{Name: "bad", Workspace: env.Home, ContextPaths: []string{filepath.Join(env.Home, ".aws")}},
		{Name: "ok", Workspace: filepath.Join(env.Home, "code")},
	}
	got, skipped := Derive(DeriveInput{Profiles: profiles, Env: env})
	if !reflect.DeepEqual(got, []Share{sh(env, "code", RW)}) || len(skipped) != 2 {
		t.Fatalf("%v %v", got, skipped)
	}
	for _, s := range skipped {
		if s.Reason == "" {
			t.Fatalf("empty reason: %v", s)
		}
	}
}

func TestDeriveMemoryOnlyIfExists(t *testing.T) {
	env := testEnv(t, false)
	have := filepath.Join(env.Home, ".claude/projects/-a-b/memory")
	missing := filepath.Join(env.Home, ".claude/projects/-x-y/memory")
	got, _ := Derive(DeriveInput{MemoryDirs: []string{have, missing}, Env: env})
	if !reflect.DeepEqual(got, []Share{{Path: have, Mode: RO}}) {
		t.Fatalf("%v", got)
	}
}

func TestDeriveEmpty(t *testing.T) {
	got, skipped := Derive(DeriveInput{Env: testEnv(t, false)})
	if got == nil || len(got) != 0 || len(skipped) != 0 {
		t.Fatalf("%#v %v", got, skipped)
	}
}

// A memory folder that is a symlink is not offered (clone.MemoryMount refuses
// one too), even when it leads to a directory.
func TestDeriveMemoryDirSymlinkIsSkipped(t *testing.T) {
	env := testEnv(t, false)
	target := filepath.Join(env.Home, "elsewhere")
	link := filepath.Join(env.Home, ".claude/projects/-l-k/memory")
	for _, d := range []string{target, filepath.Dir(link)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	got, _ := Derive(DeriveInput{MemoryDirs: []string{link}, Env: env})
	if len(got) != 0 {
		t.Fatalf("a symlinked memory dir was offered: %v", got)
	}
}

// S5: a copy kept beside its original (~/code/proj-box next to ~/code/proj)
// would share ~/code rw, which holds clone_of and makes it writable. The copy
// dir itself is shared rw instead, existing or not.
func TestDeriveSharesTheCopyWhenItsParentHoldsCloneOf(t *testing.T) {
	for _, fold := range []bool{false, true} {
		env := testEnv(t, fold)
		of := filepath.Join(env.Home, "code", "proj")
		if err := os.MkdirAll(of, 0o755); err != nil {
			t.Fatal(err)
		}
		ofSpelled := of
		if fold {
			ofSpelled = filepath.Join(env.Home, "Code", "proj") // APFS: the same folder
		}
		copyDir := filepath.Join(env.Home, "code", "proj-box")
		got, _ := Derive(DeriveInput{Env: env, Profiles: []Profile{{Name: "c", Clone: true, CloneOf: ofSpelled, Workspace: copyDir}}})
		want := []Share{{Path: ofSpelled, Mode: RO}, {Path: copyDir, Mode: RW}}
		if !reflect.DeepEqual(sorted(got, fold), sorted(want, fold)) {
			t.Errorf("fold=%v: Derive = %+v, want %+v", fold, got, want)
		}
	}
}

// S5 (a): one clone profile's copy parent can hold ANOTHER profile's clone_of;
// that parent is not shared rw either, the copy dir is.
func TestDeriveCopyParentHoldingAnotherProfilesOriginal(t *testing.T) {
	env := testEnv(t, false)
	j := func(r string) string { return filepath.Join(env.Home, r) }
	if err := os.MkdirAll(j("work/b"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := j("wlink") // a second spelling of ~/work: compared resolved
	if err := os.Symlink(j("work"), link); err != nil {
		t.Fatal(err)
	}
	profiles := []Profile{
		{Name: "a-clone", Clone: true, CloneOf: j("code/a"), Workspace: filepath.Join(link, "a-box")},
		{Name: "b-clone", Clone: true, CloneOf: j("work/b"), Workspace: j("clones/b")},
	}
	got, _ := Derive(DeriveInput{Env: env, Profiles: profiles})
	want := []Share{{Path: j("clones"), Mode: RW}, {Path: j("code/a"), Mode: RO}, {Path: filepath.Join(link, "a-box"), Mode: RW}, {Path: j("work/b"), Mode: RO}}
	if !reflect.DeepEqual(got, sorted(want, false)) {
		t.Fatalf("Derive = %+v\nwant %+v", got, sorted(want, false))
	}
}

// S5 (b): a copy kept inside another profile's original is not shared rw (it
// would make that original rw through Normalize); it is skipped with a reason.
func TestDeriveSkipsACopyInsideAnOriginal(t *testing.T) {
	env := testEnv(t, false)
	j := func(r string) string { return filepath.Join(env.Home, r) }
	profiles := []Profile{
		{Name: "a-clone", Clone: true, CloneOf: j("code/a"), Workspace: j("code/a/copies/a-box")},
	}
	got, skipped := Derive(DeriveInput{Env: env, Profiles: profiles})
	if want := []Share{{Path: j("code/a"), Mode: RO}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Derive = %+v, want %+v", got, want)
	}
	want := Skip{Path: j("code/a/copies"), Reason: "inside the original of profile a-clone, which boxes may only read; share it in Settings › VM folders if you need it"}
	if len(skipped) != 1 || skipped[0] != want {
		t.Fatalf("skipped = %+v, want %+v", skipped, want)
	}
}
