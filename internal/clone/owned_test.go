package clone

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/config"
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
