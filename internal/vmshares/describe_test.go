package vmshares

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestDescribeOrigins(t *testing.T) {
	env := testEnv(t, false)
	profiles, rec := m91(env)
	list, _ := Derive(DeriveInput{Profiles: profiles, Recorded: rec, Env: env})
	rows := Describe(list, profiles, rec, env.Home, false)
	type want struct {
		display, origin string
		used            []string
	}
	exp := []want{
		{"~/box-clones", "Where culibox-clone keeps its copies", []string{"culibox-clone"}},
		{"~/code/culibox", "Workspace of profile culibox; culibox-clone copies it", []string{"culibox", "culibox-clone", "ops"}},
		{"~/code/infra", "A boxed session ran here; no profile uses it", nil},
		{"~/notes/runbooks", "Context path of profile ops", []string{"ops"}},
	}
	if len(rows) != len(exp) {
		t.Fatalf("%v", rows)
	}
	for i, e := range exp {
		r := rows[i]
		if r.Display != e.display || r.Origin != e.origin || !reflect.DeepEqual(r.UsedBy, e.used) || r.Share != list[i] {
			t.Errorf("row %d: %+v", i, r)
		}
	}
}

func TestDescribeOriginalAndAddedByYou(t *testing.T) {
	env := testEnv(t, false)
	j := func(r string) string { return env.Home + "/" + r }
	profiles := []Profile{{Name: "c", Clone: true, CloneOf: j("orig"), Workspace: j("copies/x")}}
	rows := Describe([]Share{{j("orig"), RO}, {j("mine"), RW}}, profiles, nil, env.Home, false)
	if rows[0].Origin != "Original of profile c" || rows[1].Origin != "Added by you" || len(rows[1].UsedBy) != 0 {
		t.Fatalf("%+v", rows)
	}
}

func TestWholeHomeLine(t *testing.T) {
	want := "the box VM shares your whole home folder; apply the folder list in Settings › VM folders (or `culibox box share apply`) so it sees only 4 folders"
	if got := WholeHomeLine(4); got != want {
		t.Fatalf("%q", got)
	}
}

func TestDescribeSeveralCopiersUsePluralVerb(t *testing.T) {
	env := testEnv(t, false)
	j := func(r string) string { return filepath.Join(env.Home, r) }
	profiles := []Profile{
		{Name: "a", Workspace: j("code/x")},
		{Name: "b", Clone: true, CloneOf: j("code/x"), Workspace: j("bc/b")},
		{Name: "c", Clone: true, CloneOf: j("code/x"), Workspace: j("bc/c")},
	}
	list := []Share{{Path: j("code/x"), Mode: RW}}
	rows := Describe(list, profiles, nil, env.Home, false)
	if want := "Workspace of profile a; b, c copy it"; rows[0].Origin != want {
		t.Errorf("origin = %q, want %q", rows[0].Origin, want)
	}
	profiles = profiles[:2]
	rows = Describe(list, profiles, nil, env.Home, false)
	if want := "Workspace of profile a; b copies it"; rows[0].Origin != want {
		t.Errorf("origin = %q, want %q", rows[0].Origin, want)
	}
}
