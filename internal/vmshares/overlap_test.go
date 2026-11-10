package vmshares

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func sh(env Env, rel string, m Mode) Share {
	return Share{Path: filepath.Join(env.Home, rel), Mode: m}
}

func TestAddCovered(t *testing.T) {
	env := testEnv(t, false)
	list := []Share{sh(env, "code", RW)}
	out, err := Add(list, sh(env, "code/x", RO), env)
	if err != nil || out.CoveredBy != list[0].Path || !reflect.DeepEqual(out.List, list) {
		t.Fatalf("rw covers ro child: %+v %v", out, err)
	}
	out, err = Add(list, sh(env, "code/x", RW), env)
	if err != nil || out.CoveredBy != list[0].Path {
		t.Fatalf("rw covers rw child: %+v %v", out, err)
	}
	ro := []Share{sh(env, "n", RO)}
	out, err = Add(ro, sh(env, "n/d", RO), env)
	if err != nil || out.CoveredBy != ro[0].Path || !reflect.DeepEqual(out.List, ro) {
		t.Fatalf("ro covers ro: %+v %v", out, err)
	}
}

func TestAddConflictRwInsideRo(t *testing.T) {
	env := testEnv(t, false)
	list := []Share{sh(env, "notes/runbooks", RO)}
	_, err := Add(list, sh(env, "notes/runbooks/drafts", RW), env)
	var c *Conflict
	if !errors.As(err, &c) {
		t.Fatalf("want *Conflict, got %v", err)
	}
	want := "~/notes/runbooks/drafts needs write access but ~/notes/runbooks is shared read-only; make ~/notes/runbooks read-write or remove it"
	if c.Error() != want {
		t.Fatalf("got %q", c.Error())
	}
}

func TestAddAbsorbsChildren(t *testing.T) {
	env := testEnv(t, false)
	a, b := sh(env, "code/a", RW), sh(env, "code/b", RO)
	out, err := Add([]Share{a, b}, sh(env, "code", RW), env)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out.List, []Share{sh(env, "code", RW)}) || !reflect.DeepEqual(out.Absorbed, []string{a.Path, b.Path}) {
		t.Fatalf("%+v", out)
	}
	_, err = Add([]Share{a}, sh(env, "code", RO), env)
	var c *Conflict
	if !errors.As(err, &c) || c.Child != a.Path || c.Parent != sh(env, "code", RO).Path {
		t.Fatalf("want conflict, got %v", err)
	}
	// an ro parent absorbing ro children is fine
	out, err = Add([]Share{b}, sh(env, "code", RO), env)
	if err != nil || len(out.List) != 1 || len(out.Absorbed) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestAddSamePathChangesMode(t *testing.T) {
	env := testEnv(t, false)
	out, err := Add([]Share{sh(env, "code", RW), sh(env, "n", RO)}, sh(env, "code", RO), env)
	if err != nil || out.CoveredBy != "" || !reflect.DeepEqual(out.List, []Share{sh(env, "code", RO), sh(env, "n", RO)}) {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestAddRefusedPassesThroughCheck(t *testing.T) {
	env := testEnv(t, false)
	_, err := Add(nil, sh(env, ".aws", RO), env)
	var r *Refused
	if !errors.As(err, &r) {
		t.Fatalf("want *Refused, got %v", err)
	}
}

func TestAddCoveredFoldsCase(t *testing.T) {
	env := testEnv(t, true)
	list := []Share{sh(env, "code", RW)}
	out, err := Add(list, Share{Path: filepath.Join(env.Home, "Code", "x"), Mode: RW}, env)
	if err != nil || out.CoveredBy != list[0].Path {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestNormalizePromotesRoParent(t *testing.T) {
	env := testEnv(t, false)
	in := []Share{sh(env, "r", RO), sh(env, "r/w", RW), sh(env, ".ssh", RW)}
	out, skipped := Normalize(in, env)
	if !reflect.DeepEqual(out, []Share{sh(env, "r", RW)}) {
		t.Fatalf("%v", out)
	}
	if len(skipped) != 2 {
		t.Fatalf("%v", skipped)
	}
	var promo, refused bool
	for _, s := range skipped {
		if s.Path == sh(env, "r", RO).Path && strings.Contains(s.Reason, "made read & write because "+sh(env, "r/w", RW).Path+" needs write access") {
			promo = true
		}
		if s.Path == sh(env, ".ssh", RW).Path {
			refused = true
		}
	}
	if !promo || !refused {
		t.Fatalf("%v", skipped)
	}
}

func TestNormalizeMergesDuplicatesSorted(t *testing.T) {
	env := testEnv(t, true)
	in := []Share{sh(env, "z", RO), {Path: filepath.Join(env.Home, "Z"), Mode: RW}, sh(env, "a", RO), sh(env, "a/b", RO)}
	out, _ := Normalize(in, env)
	if len(out) != 2 || out[0] != sh(env, "a", RO) || out[1].Mode != RW {
		t.Fatalf("%v", out)
	}
}

func TestSetModeAndRemoveNotListed(t *testing.T) {
	env := testEnv(t, false)
	list := []Share{sh(env, "code", RW)}
	if _, err := SetMode(list, sh(env, "x", RW).Path, RO, env); !errors.Is(err, ErrNotListed) {
		t.Fatal(err)
	}
	if _, err := Remove(list, sh(env, "x", RW).Path, false); !errors.Is(err, ErrNotListed) {
		t.Fatal(err)
	}
	got, err := SetMode(list, list[0].Path, RO, env)
	if err != nil || got[0].Mode != RO || list[0].Mode != RW {
		t.Fatalf("%v %v (input must not be mutated)", got, err)
	}
	got, err = Remove(list, list[0].Path, false)
	if err != nil || len(got) != 0 || got == nil {
		t.Fatalf("%v %v", got, err)
	}
}
