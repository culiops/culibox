package box

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const keptTestClash = "clash/" + keptTestBox

// keptMoveSetup is a box home under a state root and the store of keptTestKey
// beside it, as the keep pass has them.
func keptMoveSetup(t *testing.T) (homeDir string, home *os.Root, storeDir string, store *os.Root) {
	t.Helper()
	stateRoot, store := keptStore(t)
	homeDir = filepath.Join(stateRoot, "boxstate", keptTestBox)
	keptMkdir(t, homeDir, ".")
	home, err := os.OpenRoot(homeDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { home.Close() })
	return homeDir, home, KeptStoreDir(stateRoot, keptTestKey), store
}

// keptWant is the candidate for rel as discovery saw it.
func keptWant(t *testing.T, homeDir, rel string) keptCandidate {
	t.Helper()
	dev, ino := keptDevIno(t, filepath.Join(homeDir, filepath.FromSlash(rel)))
	return keptCandidate{Rel: rel, Dev: dev, Ino: ino}
}

func keptRead(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(b)
}

func keptGone(t *testing.T, dir, rel string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s is still there (%v)", rel, err)
	}
}

// keptTree lists every path under dir with its content (a file), its target
// (a link) or "/" (a directory).
func keptTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			tgt, err := os.Readlink(p)
			if err != nil {
				return err
			}
			out[rel] = "-> " + tgt
		case d.IsDir():
			out[rel] = "/"
		default:
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[rel] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func mustMerge(t *testing.T, home, store *os.Root, want keptCandidate) (int, int) {
	t.Helper()
	moved, clashed, err := mergeMove(home, store, want.Rel, keptTestClash, want)
	if err != nil {
		t.Fatalf("mergeMove(%s): %v", want.Rel, err)
	}
	return moved, clashed
}

func TestMergeMoveFresh(t *testing.T) {
	homeDir, home, storeDir, store := keptMoveSetup(t)
	keptWrite(t, homeDir, ".culiops/a", "A")
	keptWrite(t, homeDir, ".culiops/b/c", "C")
	keptWrite(t, homeDir, ".local/share/culiops/d", "D")
	for _, rel := range []string{".culiops", ".local/share/culiops"} {
		want := keptWant(t, homeDir, rel)
		if moved, clashed := mustMerge(t, home, store, want); moved != 1 || clashed != 0 {
			t.Fatalf("%s: moved %d, clashed %d; want the folder moved whole", rel, moved, clashed)
		}
		keptGone(t, homeDir, rel)
		// The very folder, renamed: no copy.
		if got := keptWant(t, storeDir, "home/"+rel); got.Dev != want.Dev || got.Ino != want.Ino {
			t.Fatalf("%s: the store holds dev/ino %d/%d, want %d/%d", rel, got.Dev, got.Ino, want.Dev, want.Ino)
		}
	}
	if keptRead(t, storeDir, "home/.culiops/a") != "A" || keptRead(t, storeDir, "home/.culiops/b/c") != "C" ||
		keptRead(t, storeDir, "home/.local/share/culiops/d") != "D" {
		t.Fatal("content changed in the move")
	}
	// The store-side parents are the host's own 0700 directories.
	for _, rel := range []string{"home/.local", "home/.local/share"} {
		fi, err := os.Lstat(filepath.Join(storeDir, rel))
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v, want a 0700 directory", rel, fi, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(homeDir, ".local", "share")); err != nil {
		t.Fatalf("the level itself left the home: %v", err)
	}
}

func TestMergeRecursive(t *testing.T) {
	homeDir, home, storeDir, store := keptMoveSetup(t)
	const base = ".local/share/culiops/sre-investigate/data"
	keptWrite(t, storeDir, "home/"+base+"/r1/part.parquet", "r1")
	keptWrite(t, homeDir, base+"/r2/part.parquet", "r2")
	if moved, clashed := mustMerge(t, home, store, keptWant(t, homeDir, ".local/share/culiops")); moved != 1 || clashed != 0 {
		t.Fatalf("moved %d, clashed %d; want r2 moved and no clash", moved, clashed)
	}
	if keptRead(t, storeDir, "home/"+base+"/r1/part.parquet") != "r1" || keptRead(t, storeDir, "home/"+base+"/r2/part.parquet") != "r2" {
		t.Fatal("the store does not hold both runs")
	}
	keptGone(t, homeDir, ".local/share/culiops") // emptied, then removed
}

func TestMergeClashToClashDir(t *testing.T) {
	homeDir, home, storeDir, store := keptMoveSetup(t)
	const budgets = ".local/share/culiops/sre-investigate/budgets"
	keptWrite(t, storeDir, "home/"+budgets+"/ledger.json", `{"from":"store"}`)
	keptWrite(t, homeDir, budgets+"/ledger.json", `{"from":"home"}`)
	keptWrite(t, homeDir, budgets+"/other.json", "other")
	moved, clashed := mustMerge(t, home, store, keptWant(t, homeDir, ".local/share/culiops"))
	if moved != 1 || clashed != 1 {
		t.Fatalf("moved %d, clashed %d; want 1 and 1", moved, clashed)
	}
	if got := keptRead(t, storeDir, "home/"+budgets+"/ledger.json"); got != `{"from":"store"}` {
		t.Fatalf("the store's own copy is %q", got)
	}
	if got := keptRead(t, storeDir, keptTestClash+"/"+budgets+"/ledger.json"); got != `{"from":"home"}` {
		t.Fatalf("the home's copy in clash/ is %q", got)
	}
	if keptRead(t, storeDir, "home/"+budgets+"/other.json") != "other" {
		t.Fatal("other.json lost")
	}
	keptGone(t, homeDir, ".local/share/culiops")

	// Another home of the box with the same file: the earlier clash copy stays,
	// the new one takes a fresh name beside it.
	keptWrite(t, homeDir, budgets+"/ledger.json", `{"from":"home again"}`)
	if moved, clashed := mustMerge(t, home, store, keptWant(t, homeDir, ".local/share/culiops")); moved != 0 || clashed != 1 {
		t.Fatalf("again: moved %d, clashed %d", moved, clashed)
	}
	clash := keptTestClash + "/" + budgets
	if keptRead(t, storeDir, clash+"/ledger.json") != `{"from":"home"}` || keptRead(t, storeDir, clash+"/ledger.json~2") != `{"from":"home again"}` {
		t.Fatalf("clash dir = %v", keptTree(t, filepath.Join(storeDir, clash)))
	}
	if keptRead(t, storeDir, "home/"+budgets+"/ledger.json") != `{"from":"store"}` {
		t.Fatal("the store's copy changed")
	}
}

func TestMergeLinkNotFollowed(t *testing.T) {
	homeDir, home, storeDir, store := keptMoveSetup(t)
	outside := t.TempDir()
	keptWrite(t, outside, "secret/id", "S")
	before := keptTree(t, outside)
	secret := filepath.Join(outside, "secret")

	keptWrite(t, storeDir, "home/.culiops/notes.md", "store notes") // a merge, not a fresh move
	keptWrite(t, storeDir, "home/.culiops/sub/x", "store x")
	keptSymlink(t, outside, storeDir, "home/.culiops/sdir") // a link on the store side
	keptWrite(t, homeDir, ".culiops/new.md", "N")
	keptSymlink(t, secret, homeDir, ".culiops/link")                       // a link to a dir
	keptSymlink(t, filepath.Join(secret, "id"), homeDir, ".culiops/flink") // a link to a file
	keptSymlink(t, secret, homeDir, ".culiops/sub")                        // a link where the store has a dir
	keptWrite(t, homeDir, ".culiops/sdir/h", "H")                          // a dir where the store has a link

	moved, clashed := mustMerge(t, home, store, keptWant(t, homeDir, ".culiops"))
	if moved != 3 || clashed != 2 {
		t.Fatalf("moved %d, clashed %d; want 3 (new.md, link, flink) and 2 (sub, sdir)", moved, clashed)
	}
	got := keptTree(t, storeDir)
	for rel, want := range map[string]string{
		"home/.culiops/new.md":             "N",
		"home/.culiops/link":               "-> " + secret,
		"home/.culiops/flink":              "-> " + filepath.Join(secret, "id"),
		"home/.culiops/sub/x":              "store x",
		"home/.culiops/sdir":               "-> " + outside,
		keptTestClash + "/.culiops/sub":    "-> " + secret,
		keptTestClash + "/.culiops/sdir/h": "H",
		"home/.culiops/notes.md":           "store notes",
	} {
		if got[filepath.FromSlash(rel)] != want {
			t.Errorf("%s = %q, want %q", rel, got[filepath.FromSlash(rel)], want)
		}
	}
	if after := keptTree(t, outside); !mapsEqual(before, after) {
		t.Fatalf("the link target changed:\nbefore %v\nafter  %v", before, after)
	}
	keptGone(t, homeDir, ".culiops")
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// The folder at rel must be the one discovery saw: another directory there,
// none, or a link (even to that very directory) moves nothing.
func TestMergeMoveRefusesChangedFolder(t *testing.T) {
	for name, change := range map[string]func(homeDir string){
		"replaced": func(homeDir string) {
			os.Rename(filepath.Join(homeDir, ".culiops"), filepath.Join(homeDir, ".culiops-old"))
			keptWrite(t, homeDir, ".culiops/b", "new")
		},
		"gone": func(homeDir string) {
			os.Rename(filepath.Join(homeDir, ".culiops"), filepath.Join(homeDir, ".culiops-old"))
		},
		"link to it": func(homeDir string) {
			os.Rename(filepath.Join(homeDir, ".culiops"), filepath.Join(homeDir, ".culiops-old"))
			keptSymlink(t, ".culiops-old", homeDir, ".culiops")
		},
	} {
		t.Run(name, func(t *testing.T) {
			homeDir, home, storeDir, store := keptMoveSetup(t)
			keptWrite(t, homeDir, ".culiops/a", "A")
			want := keptWant(t, homeDir, ".culiops")
			change(homeDir)
			before := keptTree(t, homeDir)
			noRename(t)
			moved, clashed, err := mergeMove(home, store, ".culiops", keptTestClash, want)
			if !errors.Is(err, errKeptChanged) || moved != 0 || clashed != 0 {
				t.Fatalf("mergeMove = %d, %d, %v; want errKeptChanged and nothing moved", moved, clashed, err)
			}
			keptGone(t, storeDir, "home/.culiops")
			if after := keptTree(t, homeDir); !mapsEqual(before, after) {
				t.Fatalf("the home changed:\nbefore %v\nafter  %v", before, after)
			}
		})
	}
}

// A parent of rel swapped for a link is never followed, whether it is swapped
// before the call or while the path resolves: not a link to the home (so that
// .config/.claude would name ~/.claude through os.Root), and not even a link
// to the real parent, where the folder still has the dev/ino screened.
func TestMergeMoveSwappedParentNotFollowed(t *testing.T) {
	for _, c := range []struct {
		name   string
		target string
		timed  bool
	}{
		{"to the home", ".", false},
		{"to the home, timed", ".", true},
		{"to the real parent", ".config-real", false},
		{"to the real parent, timed", ".config-real", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			homeDir, home, storeDir, store := keptMoveSetup(t)
			keptWrite(t, homeDir, ".config/.claude/x", "the box's own folder of that name")
			keptWrite(t, homeDir, ".claude/settings.json", "{}")
			want := keptWant(t, homeDir, ".config/.claude")
			swap := func() {
				if err := os.Rename(filepath.Join(homeDir, ".config"), filepath.Join(homeDir, ".config-real")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(c.target, filepath.Join(homeDir, ".config")); err != nil {
					t.Fatal(err)
				}
			}
			if c.timed {
				swapped := swapOnOpen(t, func(rel string) bool { return rel == ".config" }, swap)
				defer func() {
					if !*swapped {
						t.Error("the swap never ran")
					}
				}()
			} else {
				swap()
			}
			noRename(t)
			moved, _, err := mergeMove(home, store, ".config/.claude", keptTestClash, want)
			if !errors.Is(err, errKeptChanged) || moved != 0 {
				t.Fatalf("mergeMove = %d, %v; want errKeptChanged", moved, err)
			}
			if keptRead(t, homeDir, ".claude/settings.json") != "{}" || keptRead(t, homeDir, ".config-real/.claude/x") == "" {
				t.Fatal("the home changed")
			}
			keptGone(t, storeDir, "home/.config/.claude")
			if got := keptTree(t, storeDir); len(got) > 3 { // ".", home, clash
				t.Fatalf("the store holds %v", got)
			}
		})
	}
}

// noRename fails the test if mergeMove renames anything.
func noRename(t *testing.T) {
	t.Helper()
	keptMoveHook = func(rel string) { t.Errorf("renamed %s", rel) }
	t.Cleanup(func() { keptMoveHook = nil })
}

// A link on the store side, where a parent belongs, is never followed: nothing
// is written where it points and the home keeps its folder.
func TestMergeMoveStoreLinkNotFollowed(t *testing.T) {
	for _, at := range []string{"home", "home/.local", "home/.local/share"} {
		t.Run(at, func(t *testing.T) {
			homeDir, home, storeDir, store := keptMoveSetup(t)
			outside := t.TempDir()
			os.RemoveAll(filepath.Join(storeDir, filepath.FromSlash(at)))
			keptSymlink(t, outside, storeDir, at)
			keptWrite(t, homeDir, ".local/share/x/f", "F")
			before := keptTree(t, homeDir)
			if moved, _, err := mergeMove(home, store, ".local/share/x", keptTestClash, keptWant(t, homeDir, ".local/share/x")); err == nil || moved != 0 {
				t.Fatalf("mergeMove = %d, %v; want a refusal", moved, err)
			}
			if ents, _ := os.ReadDir(outside); len(ents) != 0 {
				t.Fatalf("written through the store link: %v", ents)
			}
			if after := keptTree(t, homeDir); !mapsEqual(before, after) {
				t.Fatal("the home changed")
			}
		})
	}
	// clash/ as a link, when a clash needs it: refused, nothing lost.
	homeDir, home, storeDir, store := keptMoveSetup(t)
	outside := t.TempDir()
	os.RemoveAll(filepath.Join(storeDir, "clash"))
	keptSymlink(t, outside, storeDir, "clash")
	keptWrite(t, storeDir, "home/.culiops/f", "store")
	keptWrite(t, homeDir, ".culiops/f", "home")
	if _, _, err := mergeMove(home, store, ".culiops", keptTestClash, keptWant(t, homeDir, ".culiops")); err == nil {
		t.Fatal("a clash through a linked clash/ succeeded")
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("written through the clash link: %v", ents)
	}
	if keptRead(t, homeDir, ".culiops/f") != "home" || keptRead(t, storeDir, "home/.culiops/f") != "store" {
		t.Fatal("a copy was lost")
	}
}

// Something other than a directory at the store's own rel (a file, or a link)
// is a clash for the whole folder: it goes to clash/, the link is not followed.
func TestMergeMoveTopClash(t *testing.T) {
	for _, kind := range []string{"file", "link"} {
		t.Run(kind, func(t *testing.T) {
			homeDir, home, storeDir, store := keptMoveSetup(t)
			outside := t.TempDir()
			if kind == "file" {
				keptWrite(t, storeDir, "home/.culiops", "a file")
			} else {
				keptSymlink(t, outside, storeDir, "home/.culiops")
			}
			keptWrite(t, homeDir, ".culiops/a", "A")
			if moved, clashed := mustMerge(t, home, store, keptWant(t, homeDir, ".culiops")); moved != 0 || clashed != 1 {
				t.Fatalf("moved %d, clashed %d", moved, clashed)
			}
			if keptRead(t, storeDir, keptTestClash+"/.culiops/a") != "A" {
				t.Fatal("the folder is not in clash/")
			}
			if ents, _ := os.ReadDir(outside); len(ents) != 0 {
				t.Fatalf("written through the link: %v", ents)
			}
			keptGone(t, homeDir, ".culiops")
		})
	}
}

// A cap hit stops the merge with an error; what moved stays moved, the rest
// stays in the home, and nothing is lost.
func TestMergeMoveCaps(t *testing.T) {
	t.Run("entries", func(t *testing.T) {
		setCap(t, &keptScreenMaxEntries, 3)
		homeDir, home, storeDir, store := keptMoveSetup(t)
		keptWrite(t, storeDir, "home/.culiops/keep", "k")
		for i := range 5 {
			keptWrite(t, homeDir, ".culiops/f"+strconv.Itoa(i), strconv.Itoa(i))
		}
		moved, _, err := mergeMove(home, store, ".culiops", keptTestClash, keptWant(t, homeDir, ".culiops"))
		if err == nil {
			t.Fatal("no error past the entry cap")
		}
		if moved > 3 {
			t.Fatalf("moved %d past a cap of 3", moved)
		}
		for i := range 5 {
			n := ".culiops/f" + strconv.Itoa(i)
			s, errS := os.ReadFile(filepath.Join(storeDir, "home", n))
			h, errH := os.ReadFile(filepath.Join(homeDir, n))
			if (errS == nil) == (errH == nil) || string(s)+string(h) != strconv.Itoa(i) {
				t.Fatalf("%s: store %q %v, home %q %v; want it in exactly one", n, s, errS, h, errH)
			}
		}
	})
	t.Run("depth", func(t *testing.T) {
		setCap(t, &keptScreenMaxDepth, 2)
		homeDir, home, storeDir, store := keptMoveSetup(t)
		keptWrite(t, storeDir, "home/.culiops/a/b/c/x", "x")
		keptWrite(t, homeDir, ".culiops/a/b/c/y", "y")
		if _, _, err := mergeMove(home, store, ".culiops", keptTestClash, keptWant(t, homeDir, ".culiops")); err == nil {
			t.Fatal("no error past the depth cap")
		}
		if keptRead(t, homeDir, ".culiops/a/b/c/y") != "y" || keptRead(t, storeDir, "home/.culiops/a/b/c/x") != "x" {
			t.Fatal("a file was lost")
		}
	})
	t.Run("time", func(t *testing.T) {
		setCap(t, &keptScreenTimeout, time.Duration(0))
		homeDir, home, storeDir, store := keptMoveSetup(t)
		keptWrite(t, storeDir, "home/.culiops/x", "x")
		keptWrite(t, homeDir, ".culiops/y", "y")
		if _, _, err := mergeMove(home, store, ".culiops", keptTestClash, keptWant(t, homeDir, ".culiops")); err == nil {
			t.Fatal("no error past the deadline")
		}
		if keptRead(t, homeDir, ".culiops/y") != "y" {
			t.Fatal("y was lost")
		}
	})
}

// The folder swapped right before the rename: what landed in the store is not
// what was screened, so it is put back and the move is refused.
func TestMergeMoveSwapBeforeRenameUndone(t *testing.T) {
	homeDir, home, storeDir, store := keptMoveSetup(t)
	keptWrite(t, homeDir, ".culiops/a", "screened")
	want := keptWant(t, homeDir, ".culiops")
	keptMoveHook = func(rel string) {
		if rel == ".culiops" {
			keptMoveHook = nil
			os.Rename(filepath.Join(homeDir, ".culiops"), filepath.Join(homeDir, ".culiops-screened"))
			keptWrite(t, homeDir, ".culiops/id_rsa", "-----BEGIN OPENSSH PRIVATE KEY-----\n")
		}
	}
	t.Cleanup(func() { keptMoveHook = nil })
	if moved, _, err := mergeMove(home, store, ".culiops", keptTestClash, want); !errors.Is(err, errKeptChanged) || moved != 0 {
		t.Fatalf("mergeMove = %d, %v; want errKeptChanged", moved, err)
	}
	keptGone(t, storeDir, "home/.culiops")
	if keptRead(t, homeDir, ".culiops/id_rsa") == "" || keptRead(t, homeDir, ".culiops-screened/a") != "screened" {
		t.Fatal("the home lost a folder")
	}
}

func TestMergeMoveBadArgs(t *testing.T) {
	homeDir, home, storeDir, store := keptMoveSetup(t)
	keptWrite(t, homeDir, ".culiops/a", "A")
	keptWrite(t, homeDir, ".aws/credentials", "x")
	want := keptWant(t, homeDir, ".culiops")
	other := want
	other.Rel = ".config/x"
	aws := keptWant(t, homeDir, ".aws")
	for _, c := range []struct {
		rel, clash string
		want       keptCandidate
	}{
		{"../x", keptTestClash, want},
		{".aws", keptTestClash, aws},
		{".culiops", keptTestClash, other},
		{".culiops", "clash", want},
		{".culiops", "clash/", want},
		{".culiops", "home/" + keptTestBox, want},
		{".culiops", "clash/../home", want},
		{".culiops", "/clash/" + keptTestBox, want},
		{".culiops", "clash/ab-box-x", want},
		{".culiops", "clash/" + keptTestBox + "/x", want},
	} {
		if moved, clashed, err := mergeMove(home, store, c.rel, c.clash, c.want); err == nil || moved+clashed != 0 {
			t.Errorf("mergeMove(%q, %q, %+v) = %d, %d, %v; want a refusal", c.rel, c.clash, c.want, moved, clashed, err)
		}
	}
	if !slices.Equal(keptNames(t, filepath.Join(storeDir, "home")), nil) {
		t.Fatal("something moved")
	}
}

func keptNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// A clash copy that cannot go under its own path in clash/<box> goes to
// clash/<box>/~<N>/<rel> instead: never a home held for ever. Three ways the
// own path fails: a file where a folder of it belongs, every numbered name
// taken, and a name so long a ~N suffix passes NAME_MAX.
func TestMergeClashFallback(t *testing.T) {
	long := strings.Repeat("x", 254)
	for _, tc := range []struct {
		name, rel, file string
		rig             func(t *testing.T, storeDir string)
	}{
		{"file in the way", ".config/culiops", "f", func(t *testing.T, storeDir string) {
			keptWrite(t, storeDir, keptTestClash+"/.config", "a file, not a folder")
		}},
		{"every name taken", ".culiops", "f", func(t *testing.T, storeDir string) {
			keptWrite(t, storeDir, keptTestClash+"/.culiops/f", "old")
			for i := 2; i <= keptClashTries; i++ {
				keptWrite(t, storeDir, keptTestClash+"/.culiops/f~"+strconv.Itoa(i), "old")
			}
		}},
		{"name too long to number", ".culiops", long, func(t *testing.T, storeDir string) {
			keptWrite(t, storeDir, keptTestClash+"/.culiops/"+long, "old")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			homeDir, home, storeDir, store := keptMoveSetup(t)
			keptWrite(t, storeDir, "home/"+tc.rel+"/"+tc.file, "store")
			keptWrite(t, homeDir, tc.rel+"/"+tc.file, "home")
			tc.rig(t, storeDir)
			if moved, clashed := mustMerge(t, home, store, keptWant(t, homeDir, tc.rel)); moved != 0 || clashed != 1 {
				t.Fatalf("moved %d, clashed %d; want the copy in clash/", moved, clashed)
			}
			if got := keptRead(t, storeDir, keptTestClash+"/~1/"+tc.rel+"/"+tc.file); got != "home" {
				t.Fatalf("the fallback copy is %q", got)
			}
			keptGone(t, homeDir, tc.rel)
			if got := keptRead(t, storeDir, "home/"+tc.rel+"/"+tc.file); got != "store" {
				t.Fatalf("the store's copy changed: %q", got)
			}
			// The next one takes ~2.
			keptWrite(t, homeDir, tc.rel+"/"+tc.file, "home again")
			if _, clashed := mustMerge(t, home, store, keptWant(t, homeDir, tc.rel)); clashed != 1 {
				t.Fatalf("again: clashed %d", clashed)
			}
			if got := keptRead(t, storeDir, keptTestClash+"/~2/"+tc.rel+"/"+tc.file); got != "home again" {
				t.Fatalf("the second fallback copy is %q", got)
			}
		})
	}
}
