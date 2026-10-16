package box

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The config and profiles can live outside the data root (-config, the
// ~/.config fallback, profiles_path). A box overlapping either could rewrite
// the binaries agent-box runs on its next start, so both are refused, and the
// reason names the one it overlaps.
func TestWorkspaceOverlappingConfigOrProfilesDirIsRefused(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	cfgDir := filepath.Join(elsewhere, "cfg", "agent-box") // not created yet
	profDir := filepath.Join(elsewhere, "prof")
	if err := os.MkdirAll(profDir, 0o700); err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{DockerBin: "/d", Image: "i", Instance: "T", StateRoot: root, Client: newFake(),
		Protected: []ProtectedDir{ConfigDirProtected(cfgDir), ProfilesDirProtected(profDir)}})
	if err != nil {
		t.Fatal(err)
	}
	for ws, want := range map[string]string{
		filepath.Join(elsewhere, "cfg"): "it contains agent-box's config directory",
		profDir:                         "it is agent-box's profiles directory",
		elsewhere:                       "it contains agent-box's config directory",
	} {
		_, _, _, err := m.Wrap(context.Background(), ws, "s-1", "", []string{"claude"}, nil, nil)
		if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", ws, want, err)
		}
	}
	other := filepath.Join(elsewhere, "proj")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.Wrap(context.Background(), other, "s-1", "", []string{"claude"}, nil, nil); err != nil {
		t.Fatalf("a sibling of the config and profiles dirs must box: %v", err)
	}
}

// APFS ignores case, so on darwin /x/AB is the protected /x/ab.
func TestProtectedOverlapFoldsCaseOnlyWhenAsked(t *testing.T) {
	dirs := []ProtectedDir{DataDirProtected("/Users/a/.local/share/agent-box")}
	if r := protectedOverlap("/users/A/.Local", dirs, true); !strings.Contains(r, "it contains") {
		t.Fatalf("case-folded ancestor not matched: %q", r)
	}
	if r := protectedOverlap("/USERS/a/.local/share/AGENT-BOX/vm", dirs, true); !strings.Contains(r, "it is inside") {
		t.Fatalf("case-folded descendant not matched: %q", r)
	}
	if r := protectedOverlap("/users/A/.Local", dirs, false); r != "" {
		t.Fatalf("a case-sensitive filesystem must not fold: %q", r)
	}
	if pathWithin("/x/abc", "/x/ab", true) || pathWithin("/x/AB", "/x/abc", true) {
		t.Fatal("components must match whole, folded or not")
	}
	if !pathWithin("/x/ab", "/", false) {
		t.Fatal("everything is within the root")
	}
}

// A protected dir that does not exist yet, under a symlinked parent, is
// compared in its resolved spelling, as the workspace is.
func TestResolveExistingResolvesTheExistingPrefix(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "l")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	r, _ := filepath.EvalSymlinks(real)
	if got := resolveExisting(filepath.Join(link, "a", "b")); got != filepath.Join(r, "a", "b") {
		t.Fatalf("got %q, want %q", got, filepath.Join(r, "a", "b"))
	}
	if got := ProtectedOverlap(real, []ProtectedDir{ConfigDirProtected(filepath.Join(link, "a"))}); !strings.Contains(got, "it contains") {
		t.Fatalf("a not-yet-created dir under a symlink must still match: %q", got)
	}
}

func TestDedupeProtectedKeepsTheFirstMeaning(t *testing.T) {
	d := t.TempDir()
	got := DedupeProtected([]ProtectedDir{DataDirProtected(d), ConfigDirProtected(d + "/"), ProfilesDirProtected(""),
		ProfilesDirProtected(filepath.Join(d, "p"))})
	if len(got) != 2 || got[0].What != "agent-box's data directory" || got[1].What != "agent-box's profiles directory" {
		t.Fatalf("got %+v", got)
	}
}

// A config or profiles directory is protected against a workspace that IS it or
// CONTAINS it: its files sit directly in it, so a box mounted below it cannot
// reach them. Refusing "inside" too would make every workspace under $HOME
// unboxable when profiles.json sits directly in $HOME. The data root keeps
// all three shapes: the VM's files live deep inside it.
func TestConfigAndProfilesDirsRefuseOnlyIsAndContains(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, "proj")
	sub := filepath.Join(home, "cfg", "sub")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	prof := []ProtectedDir{ProfilesDirProtected(home)}
	if r := ProtectedOverlap(proj, prof); r != "" {
		t.Fatalf("a project under a profiles dir in $HOME must stay boxable: %q", r)
	}
	if r := ProtectedOverlap(home, prof); !strings.Contains(r, "it is agent-box's profiles directory") {
		t.Fatalf("the profiles dir itself must be refused: %q", r)
	}
	cfg := []ProtectedDir{ConfigDirProtected(filepath.Join(home, "cfg"))}
	if r := ProtectedOverlap(sub, cfg); r != "" {
		t.Fatalf("a subdir of a config dir must be boxable: %q", r)
	}
	if r := ProtectedOverlap(home, cfg); !strings.Contains(r, "it contains agent-box's config directory") {
		t.Fatalf("an ancestor of the config dir must be refused: %q", r)
	}
	data := []ProtectedDir{DataDirProtected(filepath.Join(home, "cfg"))}
	if r := ProtectedOverlap(sub, data); !strings.Contains(r, "it is inside agent-box's data directory") {
		t.Fatalf("the data root still refuses a workspace inside it: %q", r)
	}
}

// FileDirs follows a symlinked file hop by hop: agent-box reads through the
// link, so the directory of every hop (where the link could be swapped) and of
// the target (where the content can be rewritten) is protected.
func TestFileDirsFollowsTheSymlinkChain(t *testing.T) {
	a, b, c := t.TempDir(), t.TempDir(), t.TempDir()
	target := filepath.Join(c, "config.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(b, "mid.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../"+filepath.Base(b)+"/mid.json", filepath.Join(a, "config.json")); err != nil {
		t.Fatal(err)
	}
	got := FileDirs(filepath.Join(a, "config.json"))
	want := map[string]bool{a: false, b: false, c: false}
	for _, d := range got {
		if _, ok := want[d]; ok {
			want[d] = true
		}
	}
	for d, seen := range want {
		if !seen {
			t.Errorf("FileDirs missed %s: %v", d, got)
		}
	}
	if got := FileDirs(filepath.Join(a, "missing.json")); len(got) != 1 || got[0] != a {
		t.Fatalf("a missing file protects its own dir only: %v", got)
	}
}

// A protected dir reached through a symlinked directory is matched in both
// spellings: the box could swap the link from the directory that holds it.
//
// home itself is reached through a symlink, as t.TempDir() is on macOS
// (/var -> /private/var): the link then sits in a directory whose resolved
// spelling is neither of the link's own two.
func TestProtectedOverlapMatchesTheLinkSpellingToo(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(t.TempDir(), home); err != nil {
		t.Fatal(err)
	}
	dots := filepath.Join(home, "dotfiles", "ab")
	cfgParent := filepath.Join(home, "dotconfig")
	for _, d := range []string{dots, cfgParent} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(cfgParent, "agent-box")
	if err := os.Symlink(dots, link); err != nil {
		t.Fatal(err)
	}
	prot := []ProtectedDir{ConfigDirProtected(link)}
	if r := ProtectedOverlap(cfgParent, prot); !strings.Contains(r, "contains") {
		t.Fatalf("the directory holding the link must be refused: %q", r)
	}
	if r := ProtectedOverlap(dots, prot); !strings.Contains(r, "it is") {
		t.Fatalf("the link's target must be refused: %q", r)
	}
}

// A config dir first does not hide the data root's stricter "inside" rule.
func TestDedupeProtectedNeverDropsTheStricterEntry(t *testing.T) {
	d := t.TempDir()
	got := DedupeProtected([]ProtectedDir{ConfigDirProtected(d), DataDirProtected(d), ProfilesDirProtected(d)})
	if len(got) != 2 || got[1].What != "agent-box's data directory" {
		t.Fatalf("got %+v", got)
	}
	inside := filepath.Join(d, "x")
	if r := ProtectedOverlap(inside, got); !strings.Contains(r, "inside agent-box's data directory") {
		t.Fatalf("inside the data root must stay refused: %q", r)
	}
}
