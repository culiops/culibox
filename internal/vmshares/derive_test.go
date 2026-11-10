package vmshares

import (
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
