package vmshares

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/box"
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

// C1: the VM mounts resolved paths, so overlap is judged on them. ~/code is a
// link to a volume folder; its spellings must not give nested or duplicate
// mounts (colima refuses to start with them).
func TestOverlapUsesResolvedPaths(t *testing.T) {
	env := testEnv(t, false)
	vol := filepath.Join(filepath.Dir(filepath.Dir(env.Home)), "vol", "code")
	if err := os.MkdirAll(filepath.Join(vol, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(env.Home, "lcode")
	if err := os.Symlink(vol, link); err != nil {
		t.Fatal(err)
	}
	volProj, linkProj := filepath.Join(vol, "proj"), filepath.Join(link, "proj")

	out, _ := Normalize([]Share{{Path: link, Mode: RW}, {Path: volProj, Mode: RW}}, env)
	if len(out) != 1 || out[0].Path != link {
		t.Errorf("Normalize parent+child spellings = %+v, want only %s", out, link)
	}
	out, _ = Normalize([]Share{{Path: linkProj, Mode: RO}, {Path: volProj, Mode: RW}}, env)
	if len(out) != 1 || out[0].Mode != RW {
		t.Errorf("Normalize two spellings of one folder = %+v, want one rw entry", out)
	}
	add, err := Add([]Share{{Path: link, Mode: RW}}, Share{Path: volProj, Mode: RW}, env)
	if err != nil || add.CoveredBy != link {
		t.Errorf("Add child spelling = %+v, %v; want covered by %s", add, err, link)
	}
	add, err = Add([]Share{{Path: volProj, Mode: RW}}, Share{Path: link, Mode: RW}, env)
	if err != nil || !reflect.DeepEqual(add.Absorbed, []string{volProj}) || len(add.List) != 1 {
		t.Errorf("Add parent spelling = %+v, %v; want %s absorbed", add, err, volProj)
	}
	list := []Share{{Path: linkProj, Mode: RW}}
	if got, err := SetMode(list, volProj, RO, env); err != nil || got[0].Mode != RO || got[0].Path != linkProj {
		t.Errorf("SetMode by the other spelling = %+v, %v", got, err)
	}
	if got, err := Remove(list, volProj, false); err != nil || len(got) != 0 {
		t.Errorf("Remove by the other spelling = %+v, %v", got, err)
	}
}

// #235 item 1: a hand-edited or old list that holds a ~/.claude folder rw is
// read back read-only; the add and the mode switch refuse rw there.
func TestClaudeFoldersAreReadOnlyEverywhere(t *testing.T) {
	env := testEnv(t, false)
	mem := sh(env, ".claude/projects/-a-b/memory", RW)

	out, skipped := Normalize([]Share{mem, sh(env, "code", RW)}, env)
	if len(out) != 2 || out[0].Path != mem.Path || out[0].Mode != RO || out[1].Mode != RW {
		t.Fatalf("Normalize = %+v, want the memory folder ro and code rw", out)
	}
	if len(skipped) != 1 || skipped[0].Path != mem.Path || !strings.Contains(skipped[0].Reason, "read-only") {
		t.Errorf("skipped = %+v, want one note saying why", skipped)
	}
	if _, err := Add(nil, mem, env); kindOf(err) != KindReadOnly {
		t.Errorf("Add rw = %v, want a read-only refusal", err)
	}
	mem.Mode = RO
	add, err := Add(nil, mem, env)
	if err != nil || len(add.List) != 1 || add.List[0].Mode != RO {
		t.Fatalf("Add ro = %+v, %v", add, err)
	}
	if _, err := SetMode(add.List, mem.Path, RW, env); kindOf(err) != KindReadOnly {
		t.Errorf("SetMode rw = %v, want a read-only refusal", err)
	}
	if got, err := SetMode([]Share{sh(env, "code", RW)}, sh(env, "code", RW).Path, RO, env); err != nil || got[0].Mode != RO {
		t.Errorf("SetMode to ro outside .claude = %+v, %v", got, err)
	}
	// Adding the same folder rw over its ro entry goes through SetMode's rule.
	mem.Mode = RW
	if _, err := Add(add.List, mem, env); kindOf(err) != KindReadOnly {
		t.Errorf("Add rw over the ro entry = %v, want a read-only refusal", err)
	}
}

// #235 item 3: a path is resolved once per operation. The value that was
// checked is the one that is compared and merged, so a link retargeted while an
// operation runs cannot give the check one answer and the comparison another.
func TestAddAndNormalizeResolveEachPathOnce(t *testing.T) {
	env := testEnv(t, false)
	link := filepath.Join(env.Home, "lnk")
	if err := os.Symlink(filepath.Join(env.Home, "code"), link); err != nil {
		t.Fatal(err)
	}
	n := 0
	old := resolveSpelling
	resolveSpelling = func(p string) string {
		if p == link {
			n++
		}
		return old(p)
	}
	t.Cleanup(func() { resolveSpelling = old })

	if _, err := Add(nil, Share{Path: link, Mode: RW}, env); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("Add resolved the path %d times, want 1", n)
	}
	n = 0
	Normalize([]Share{{Path: link, Mode: RW}}, env)
	if n != 1 {
		t.Errorf("Normalize resolved the path %d times, want 1", n)
	}
}

// #235 review: a folder that holds a program CuliBox runs may be shared
// read-only, never read & write: a read-only share cannot replace the program.
func TestProgramDirHolderIsShareableOnlyReadOnly(t *testing.T) {
	env := testEnv(t, false)
	prog := filepath.Join(env.Home, "code", "tmp")
	if err := os.MkdirAll(filepath.Join(prog, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	env.Protected = []box.ProtectedDir{box.ProgramDirProtected(prog)}
	code := sh(env, "code", RO)
	work := sh(env, "code/proj", RW)

	if err := Check(code.Path, env); err != nil {
		t.Fatalf("Check (mode-agnostic) = %v", err)
	}
	if err := CheckMode(code.Path, RO, env); err != nil {
		t.Errorf("ro: %v", err)
	}
	err := CheckMode(code.Path, RW, env)
	if kindOf(err) != KindProtected || !strings.Contains(err.Error(), "can only be shared read-only") && !strings.Contains(err.Error(), "cannot be shared read & write") {
		t.Fatalf("rw = %v, want a protected refusal", err)
	}
	if _, err := Add(nil, sh(env, "code", RW), env); kindOf(err) != KindProtected {
		t.Errorf("Add rw = %v", err)
	}
	add, err := Add(nil, code, env)
	if err != nil || len(add.List) != 1 {
		t.Fatalf("Add ro = %+v %v", add, err)
	}
	if _, err := SetMode(add.List, code.Path, RW, env); kindOf(err) != KindProtected {
		t.Errorf("SetMode rw = %v", err)
	}
	// Inside the program folder's parent but not holding it: fine either way.
	if err := CheckMode(work.Path, RW, env); err != nil {
		t.Errorf("a folder beside the program folder, rw: %v", err)
	}
	// A saved rw entry reads back ro.
	out, skipped := Normalize([]Share{sh(env, "code", RW)}, env)
	if len(out) != 1 || out[0].Mode != RO || len(skipped) != 1 {
		t.Errorf("Normalize rw = %+v %+v", out, skipped)
	}
	// The promotion case: a ro parent holding the program folder must not be
	// made rw by a rw child below it.
	out, skipped = Normalize([]Share{code, work}, env)
	if len(out) != 1 || out[0].Path != code.Path || out[0].Mode != RO {
		t.Fatalf("Normalize promotion = %+v, want the parent kept read-only", out)
	}
	if len(skipped) != 1 || skipped[0].Path != work.Path {
		t.Errorf("skipped = %+v, want the child named", skipped)
	}
}
