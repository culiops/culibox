package web

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListWorkspaces(t *testing.T) {
	home := t.TempDir()
	mustMkdir(t, filepath.Join(home, "projectB"))
	mustMkdir(t, filepath.Join(home, "projectA"))
	mustMkdir(t, filepath.Join(home, ".hidden"))
	if err := os.WriteFile(filepath.Join(home, "afile"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	ws := listWorkspaces(home)
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
