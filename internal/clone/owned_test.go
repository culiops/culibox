package clone

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/config"
)

func TestBoxOwned(t *testing.T) {
	base := t.TempDir()
	of := filepath.Join(base, "repos", "of")
	dir := filepath.Join(base, "box-clones", "work")
	mustMkdir(t, filepath.Join(of, "docs", "superpowers"))
	mustMkdir(t, filepath.Join(dir, "internal", "clone"))
	other := filepath.Join(base, "other")
	mustMkdir(t, other)
	link := filepath.Join(base, "to-clone")
	mustSymlink(t, dir, link)
	clones := []config.Clone{
		{Of: "/unrelated/of", Dir: filepath.Join(base, "box-clones", "never-created")},
		{Of: of, Dir: dir, Shares: []string{"docs/superpowers"}},
	}

	cases := []struct {
		name  string
		cwd   string
		owned bool
	}{
		{"the clone", dir, true},
		{"inside the clone", filepath.Join(dir, "internal", "clone"), true},
		{"a not-yet-existing path under the clone", filepath.Join(dir, "new", "deeper"), true},
		{"a symlink to the clone", link, true},
		{"under a symlink to the clone", filepath.Join(link, "internal"), true},
		{"a not-yet-created clone dir", filepath.Join(base, "box-clones", "never-created", "x"), true},
		{"clone_of", of, false},
		{"a share source under clone_of", filepath.Join(of, "docs", "superpowers"), false},
		{"an ancestor of clone_of", filepath.Join(base, "repos"), false},
		{"the clones' parent", filepath.Join(base, "box-clones"), false},
		{"a sibling with a shared prefix", dir + "2", false},
		{"an unrelated dir", other, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, owned := BoxOwned(tc.cwd, clones)
			if owned != tc.owned {
				t.Fatalf("BoxOwned(%q) = %v (%q), want %v", tc.cwd, owned, msg, tc.owned)
			}
			if owned && (!strings.Contains(msg, tc.cwd) || !strings.Contains(msg, "host tools must never run there (use agent-box clone fetch)")) {
				t.Errorf("message %q does not name the cwd and the remedy", msg)
			}
		})
	}
}

// TestBoxOwnedFailsClosed: a path that cannot be resolved (other than a tail
// that does not exist yet) may be inside the clone through a link nobody can
// read, so it is reported as owned, never as "not owned".
func TestBoxOwnedFailsClosed(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "box-clones", "work")
	mustMkdir(t, filepath.Join(dir, "sub"))
	clones := []config.Clone{{Of: filepath.Join(base, "of"), Dir: dir}}

	loop := filepath.Join(base, "loop")
	mustSymlink(t, "loop", loop) // ELOOP
	file := filepath.Join(base, "file")
	mustWrite(t, file, "x") // ENOTDIR below it
	dangling := filepath.Join(base, "dangling")
	mustSymlink(t, filepath.Join(dir, "new"), dangling) // into the clone, target absent
	chain := filepath.Join(base, "chain")
	mustSymlink(t, "dangling", chain) // a relative link to the dangling one

	cases := []struct {
		name, cwd, msg string
	}{
		{"a self-loop symlink", filepath.Join(loop, "x"), "cannot be resolved"},
		{"a path below a regular file", filepath.Join(file, "x"), "cannot be resolved"},
		{"a dangling symlink into the clone", dangling, "is inside the box-owned clone"},
		{"under a dangling symlink into the clone", filepath.Join(dangling, "deeper"), "is inside the box-owned clone"},
		{"a relative link to a dangling link into the clone", chain, "is inside the box-owned clone"},
	}
	if os.Geteuid() != 0 { // root reads a mode-0 dir, so EACCES needs a non-root run
		locked := filepath.Join(base, "locked")
		mustMkdir(t, filepath.Join(locked, "inner"))
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
		cases = append(cases, struct{ name, cwd, msg string }{"an unreadable component", filepath.Join(locked, "inner"), "cannot be resolved"})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, owned := BoxOwned(tc.cwd, clones)
			if !owned {
				t.Fatalf("BoxOwned(%q) = not owned, want owned", tc.cwd)
			}
			if !strings.Contains(msg, tc.cwd) || !strings.Contains(msg, tc.msg) {
				t.Errorf("message %q does not name %q and %q", msg, tc.cwd, tc.msg)
			}
		})
	}

	t.Run("an unresolvable clone dir", func(t *testing.T) {
		bad := []config.Clone{{Of: "/x", Dir: filepath.Join(loop, "clone")}}
		msg, owned := BoxOwned(filepath.Join(base, "box-clones"), bad)
		if !owned || !strings.Contains(msg, "cannot be resolved") {
			t.Fatalf("BoxOwned = %v %q, want owned: the clone dir cannot be resolved", owned, msg)
		}
	})
	t.Run("an unrelated path is still not owned", func(t *testing.T) {
		if msg, owned := BoxOwned(filepath.Join(base, "box-clones"), clones); owned {
			t.Fatalf("BoxOwned = owned (%q)", msg)
		}
	})
}

// The message must be accurate for a dir known only from the clone-dirs record
// (no profile, so no "clone-mode profile" to speak of).
func TestBoxOwnedMessageNamesTheClone(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "recorded")
	mustMkdir(t, dir)
	msg, owned := BoxOwned(filepath.Join(dir, "sub"), []config.Clone{{Dir: dir}})
	if !owned || !strings.Contains(msg, "inside the box-owned clone "+dir) {
		t.Fatalf("message = %q, owned %v; want it to name the box-owned clone", msg, owned)
	}
	if strings.Contains(msg, "profile") {
		t.Errorf("message %q mentions a profile; a recorded dir has none", msg)
	}
}
