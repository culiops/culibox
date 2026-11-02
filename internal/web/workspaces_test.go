package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/box"
)

func TestListWorkspaces(t *testing.T) {
	home := t.TempDir()
	mustMkdir(t, filepath.Join(home, "projectB"))
	mustMkdir(t, filepath.Join(home, "projectA"))
	mustMkdir(t, filepath.Join(home, ".hidden"))
	if err := os.WriteFile(filepath.Join(home, "afile"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	ws := listWorkspaces(home, nil)
	if len(ws) != 3 {
		t.Fatalf("want 3 (home + 2 non-hidden dirs), got %d: %+v", len(ws), ws)
	}
	if ws[0].Path != home {
		t.Fatalf("first entry should be home, got %+v", ws[0])
	}
	if ws[1].Label != "projectA" || ws[2].Label != "projectB" {
		t.Fatalf("subdirs should be sorted non-hidden: %+v", ws[1:])
	}
	for _, w := range ws {
		base := filepath.Base(w.Path)
		if base == ".hidden" || base == "afile" {
			t.Fatalf("hidden dir or file leaked into workspaces: %+v", w)
		}
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

// The workspace choice IS the containment boundary: it is the box's only
// writable mount. Offering home as a choice leads straight into a refused spawn,
// so the list has to say which entries can actually be contained.
func TestHomeIsListedButNotBoxable(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "project"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := listWorkspaces(home, nil)
	if len(got) < 2 {
		t.Fatalf("expected home plus the project, got %+v", got)
	}
	if got[0].Path != home {
		t.Fatalf("home should still be listed first for host-tier use: %+v", got[0])
	}
	if got[0].Boxable {
		t.Fatal("home must be marked not boxable — a box there mounts the whole home")
	}
	if got[0].Reason == "" {
		t.Fatal("a non-boxable entry must say why, or the UI cannot explain it")
	}
	for _, w := range got[1:] {
		if !w.Boxable {
			t.Fatalf("a project directory must be boxable: %+v", w)
		}
	}
}

// The console must fail at boot, not once per spawn, when its default workspace
// can never be contained.
func TestDefaultCwdIsCheckedForBoxability(t *testing.T) {
	home := t.TempDir()
	if reason, ok := UnboxableDefaultCwd(home, home, nil); !ok || reason == "" {
		t.Fatal("a default_cwd equal to home must be reported at startup")
	}
	proj := filepath.Join(home, "project")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := UnboxableDefaultCwd(proj, home, nil); ok {
		t.Fatal("a project directory must pass")
	}
}

// The picker must not offer a directory the box will refuse: docker reads
// --mount as CSV, so a comma in the path is unboxable (box.MountUnsafe).
func TestWorkspaceWithMountMetacharactersIsNotBoxable(t *testing.T) {
	home := t.TempDir()
	mustMkdir(t, filepath.Join(home, "a,b"))
	mustMkdir(t, filepath.Join(home, "fine"))
	odd := []string{"nel\u0085x", "ls\u2028x", "ps\u2029x", "bad\xffutf8"}
	for _, n := range odd {
		mustMkdir(t, filepath.Join(home, n))
	}
	byLabel := map[string]Workspace{}
	for _, w := range listWorkspaces(home, nil) {
		byLabel[w.Label] = w
	}
	if w := byLabel["a,b"]; w.Boxable || w.Reason == "" {
		t.Fatalf("a path with a comma must be unboxable with a reason: %+v", w)
	}
	for _, n := range odd {
		if w := byLabel[n]; w.Boxable || w.Reason == "" {
			t.Fatalf("%q must be unboxable with a reason: %+v", n, w)
		}
	}
	if !byLabel["fine"].Boxable {
		t.Fatalf("an ordinary directory must stay boxable: %+v", byLabel["fine"])
	}
	if reason, bad := UnboxableDefaultCwd(filepath.Join(home, "a,b"), home, nil); !bad || reason == "" {
		t.Fatal("default_cwd with a comma must be reported at save/startup")
	}
}

// The picker and the save-time check mirror the daemon's data-root refusal
// (box.ProtectedOverlap): equal, ancestor, descendant and a symlink into it.
func TestDataRootOverlapIsNotBoxable(t *testing.T) {
	home := t.TempDir()
	parent := filepath.Join(home, "local")
	root := filepath.Join(parent, "ab")
	inside := filepath.Join(root, "vm")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "vmlink")
	if err := os.Symlink(inside, link); err != nil {
		t.Fatal(err)
	}
	mustMkdir(t, filepath.Join(home, "proj"))
	for name, dir := range map[string]string{
		"equal": root, "ancestor": parent, "descendant": inside, "symlink into it": link,
	} {
		if reason, bad := UnboxableDefaultCwd(dir, home, []box.ProtectedDir{box.DataDirProtected(root)}); !bad || !strings.Contains(reason, "data directory") {
			t.Errorf("%s: want a data-root refusal, got %q %v", name, reason, bad)
		}
	}
	byLabel := map[string]Workspace{}
	for _, w := range listWorkspaces(home, []box.ProtectedDir{box.DataDirProtected(root)}) {
		byLabel[w.Label] = w
	}
	for _, l := range []string{"local", "vmlink"} {
		if w := byLabel[l]; w.Boxable || !strings.Contains(w.Reason, "data directory") {
			t.Errorf("picker must mark %s unboxable: %+v", l, w)
		}
	}
	if !byLabel["proj"].Boxable {
		t.Fatalf("an unrelated project must stay boxable: %+v", byLabel["proj"])
	}
}

// A config directory outside the data root (the ~/.config fallback, -config)
// is protected too, and the reason says which directory it is.
func TestConfigDirOutsideDataRootIsNotBoxable(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "data")
	mustMkdir(t, root)
	mustMkdir(t, filepath.Join(home, "proj"))
	cfgDir := filepath.Join(home, "proj", "agent-box") // not created yet
	prot := []box.ProtectedDir{box.DataDirProtected(root), box.ConfigDirProtected(cfgDir)}
	reason, bad := UnboxableDefaultCwd(filepath.Join(home, "proj"), home, prot)
	if !bad || !strings.Contains(reason, "config directory") {
		t.Fatalf("a workspace containing the config dir must be refused, got %q %v", reason, bad)
	}
	for _, w := range listWorkspaces(home, prot) {
		if w.Label == "proj" && (w.Boxable || !strings.Contains(w.Reason, "config directory")) {
			t.Fatalf("picker must mark proj unboxable for the config dir: %+v", w)
		}
	}
}
