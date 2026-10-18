package box

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
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
		_, _, _, err := m.Wrap(context.Background(), ws, "s-1", BoxKey{}, []string{"claude"}, nil, nil)
		if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", ws, want, err)
		}
	}
	other := filepath.Join(elsewhere, "proj")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.Wrap(context.Background(), other, "s-1", BoxKey{}, []string{"claude"}, nil, nil); err != nil {
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

// oracleDirs walks the chain the way the kernel does: each hop's target is
// resolved relative to the directory that CONTAINS the link, after that
// directory's own symlinks are resolved.
func oracleDirs(t *testing.T, file string) []string {
	t.Helper()
	var out []string
	p, err := filepath.Abs(file)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		dir, err := filepath.EvalSymlinks(filepath.Dir(p))
		if err != nil {
			return append(out, deepestExisting(filepath.Dir(p)))
		}
		out = append(out, dir)
		fi, err := os.Lstat(filepath.Join(dir, filepath.Base(p)))
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			return out
		}
		tgt, err := os.Readlink(filepath.Join(dir, filepath.Base(p)))
		if err != nil {
			t.Fatal(err)
		}
		if !filepath.IsAbs(tgt) {
			tgt = filepath.Join(dir, tgt)
		}
		p = tgt
	}
	return out
}

// FileDirs must protect every directory the kernel walks (the oracle) AND the
// directories as spelled (mustInclude): the oracle only produces resolved
// directories, so a spelled one a union could drop needs its own column.
// Rows 4-6 are the three cases the round-4 attempt (832f741) regressed.
func TestFileDirsCoversEveryDirTheKernelWalks(t *testing.T) {
	type link struct {
		at, to string // relative to the temp root
		abs    bool   // to is joined onto the temp root
	}
	cases := []struct {
		name        string
		dirs        []string
		links       []link
		files       []string
		start       string   // appended to the root as TEXT, never cleaned
		skipOracle  bool     // the oracle cleans the path lexically
		mustInclude []string // relative to the root
	}{
		{name: "plain file", dirs: []string{"cfg"}, files: []string{"cfg/config.json"}, start: "cfg/config.json",
			mustInclude: []string{"cfg"}},
		{name: "dir link then relative file link (#130)",
			dirs:  []string{"dotfiles/ab", "dotfiles/shared"},
			links: []link{{"cfgdir", "dotfiles/ab", true}, {"dotfiles/ab/config.json", "../shared/l2", false}},
			files: []string{"dotfiles/shared/l2"}, start: "cfgdir/config.json",
			mustInclude: []string{"cfgdir", "dotfiles/ab", "dotfiles/shared"}},
		{name: "broken chain protects the deepest existing dir",
			dirs:  []string{"a"},
			links: []link{{"a/config.json", "../missing/x", false}}, start: "a/config.json",
			mustInclude: []string{"a", "missing", ""}},
		// 832f741 regression 1: a later hop's spelled directory is itself a
		// directory symlink; only the spelled view still names it.
		{name: "spelled hop dir that is a dir symlink",
			dirs:  []string{"a", "real"},
			links: []link{{"d", "real", true}, {"a/config.json", "../d/mid.json", false}, {"real/mid.json", "tail.json", false}},
			files: []string{"real/tail.json"}, start: "a/config.json",
			mustInclude: []string{"a", "d", "real"}},
		// 832f741 regression 2: fail-open when a hop's directory is missing.
		{name: "hop with a missing dir component",
			dirs:  []string{"a", "x"},
			links: []link{{"a/config.json", "../x/nodir/sub/f", false}}, start: "a/config.json",
			mustInclude: []string{"a", "x/nodir/sub", "x"}},
		// 832f741 regression 3: ".." after a symlinked component. Kernel: lnk
		// -> real/sub, so lnk/../x is real/x; lexically it is x.
		{name: "dot-dot after a symlinked component",
			dirs:  []string{"real/sub", "real/x", "x"},
			links: []link{{"lnk", "real/sub", true}}, files: []string{"real/x/config.json", "x/config.json"},
			start: "lnk/../x/config.json", skipOracle: true,
			mustInclude: []string{"x", "real/x"}},
		// Dangling directory link: the chain ends where the missing part would
		// be created, which is behind the link, not beside it.
		{name: "dangling dir link in the middle of a chain (A)",
			dirs:        []string{"home/a", "elsewhere"},
			links:       []link{{"home/d", "elsewhere/missing", true}, {"home/a/config.json", "../d/x", false}},
			start:       "home/a/config.json",
			mustInclude: []string{"home/a", "home", "elsewhere"}},
		// The #130 setup before the dotfiles target exists.
		{name: "config dir is a link to a target not created yet (B)",
			dirs:        []string{"home/.config", "dotfiles"},
			links:       []link{{"home/.config/agent-box", "dotfiles/ab", true}},
			start:       "home/.config/agent-box/config.json",
			mustInclude: []string{"home/.config", "dotfiles"}},
		// Directory links inside a directory path: each link's parent is a
		// place the link can be swapped.
		{name: "nested directory links in the path (C)",
			dirs: []string{"data/cfg", "data/x2", "data/ab"},
			links: []link{{".config", "data/cfg", true}, {"data/cfg/x", "data/x2", true},
				{"data/x2/agent-box", "data/ab", true}},
			files: []string{"data/ab/config.json"}, start: ".config/x/agent-box/config.json",
			mustInclude: []string{"", "data/cfg", "data/x2", "data/ab"}},
		{name: "file link through three directory links (D)",
			dirs: []string{"a", "m/p1", "m/p2", "m/p3"},
			links: []link{{"a/config.json", "l1/l2/l3/t", false}, {"a/l1", "m/p1", true},
				{"m/p1/l2", "m/p2", true}, {"m/p2/l3", "m/p3", true}},
			files: []string{"m/p3/t"}, start: "a/config.json",
			mustInclude: []string{"a", "m/p1", "m/p2", "m/p3"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range c.dirs {
				if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, f := range c.files {
				if err := os.WriteFile(filepath.Join(root, f), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, l := range c.links {
				to := l.to
				if l.abs {
					to = filepath.Join(root, to)
				}
				if err := os.Symlink(to, filepath.Join(root, l.at)); err != nil {
					t.Fatal(err)
				}
			}
			start := root + string(filepath.Separator) + c.start
			got := FileDirs(start)
			if !c.skipOracle {
				for _, want := range oracleDirs(t, start) {
					if !slices.Contains(got, want) {
						t.Errorf("FileDirs missed %s (the kernel walks it); got %q", want, got)
					}
				}
			}
			for _, rel := range c.mustInclude {
				if want := filepath.Join(root, rel); !slices.Contains(got, want) {
					t.Errorf("FileDirs missed spelled/expected %s; got %q", want, got)
				}
			}
		})
	}
}

// A symlink loop must end the walk (the kernel gives up after 40 links) and
// still protect the directory the loop sits in.
func TestFileDirsTerminatesOnASymlinkLoop(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(root, "a")
	if err := os.Mkdir(a, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("config.json", filepath.Join(a, "config.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "l2"), filepath.Join(a, "d")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "a", "d"), filepath.Join(root, "l2")); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{filepath.Join(a, "config.json"), filepath.Join(a, "d", "config.json")} {
		got := FileDirs(f)
		if !slices.Contains(got, a) {
			t.Errorf("FileDirs(%s) must protect %s even on a loop; got %q", f, a, got)
		}
	}
}

// The personal config dir refuses the dir itself, an ancestor and anything
// inside (hook scripts live in subdirectories), but not a sibling.
func TestPersonalConfigDirRefusesIsContainsAndInside(t *testing.T) {
	home := t.TempDir()
	personal := filepath.Join(home, ".claude")
	hooks := filepath.Join(personal, "hooks")
	proj := filepath.Join(home, "proj")
	for _, d := range []string{hooks, proj} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	dirs := []ProtectedDir{PersonalConfigDirProtected(personal)}
	for ws, want := range map[string]string{
		personal: "it is the personal config directory",
		home:     "it contains the personal config directory",
		hooks:    "it is inside the personal config directory",
	} {
		if r := ProtectedOverlap(ws, dirs); !strings.Contains(r, want) {
			t.Errorf("%s: want %q, got %q", ws, want, r)
		}
	}
	if r := ProtectedOverlap(proj, dirs); r != "" {
		t.Fatalf("a sibling must stay boxable: %q", r)
	}
}
