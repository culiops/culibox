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
		_, _, _, err := m.Wrap(context.Background(), ws, "s-1", []string{"claude"}, nil, nil)
		if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", ws, want, err)
		}
	}
	other := filepath.Join(elsewhere, "proj")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.Wrap(context.Background(), other, "s-1", []string{"claude"}, nil, nil); err != nil {
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
