package web

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Workspace is a selectable working directory (home + its subfolders).
type Workspace struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

// listWorkspaces returns home first, then each non-hidden subdirectory of home
// (depth 1, symlinked dirs included), sorted by name. Only home's children are
// listed — no arbitrary path traversal is exposed.
func listWorkspaces(home string) []Workspace {
	out := []Workspace{{Label: "~ (home)", Path: home}}
	entries, err := os.ReadDir(home)
	if err != nil {
		return out // still offer home even if it can't be listed
	}
	var dirs []Workspace
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue // hidden
		}
		full := filepath.Join(home, name)
		isDir := e.IsDir()
		if !isDir && e.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(full); err == nil && st.IsDir() {
				isDir = true
			}
		}
		if !isDir {
			continue
		}
		dirs = append(dirs, Workspace{Label: name, Path: full})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Label < dirs[j].Label })
	return append(out, dirs...)
}
