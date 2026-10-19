package clone

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/culiops/agent-box/internal/config"
)

// BoxOwned reports whether cwd is at or under any clone's dir, with the message
// a refused host-tier spawn shows (the caller wraps box.ErrBoxOwned). A clone
// belongs to its box, which can rewrite anything in it (its .git config, hooks,
// build files), so host tools must never run there (spec correction 11).
// Nothing else is box-owned: clone_of and the share sources are host trees the
// box can only read.
//
// Both paths are compared as spelled and resolved (a not-yet-existing tail is
// joined onto its deepest existing ancestor), so a symlink to the clone or a
// path under it that does not exist yet is still inside.
func BoxOwned(cwd string, clones []config.Clone) (string, bool) {
	for _, c := range clones {
		if c.Dir == "" {
			continue
		}
		for _, p := range spellings(cwd) {
			for _, d := range spellings(c.Dir) {
				if under(p, d) {
					return cwd + " is inside the clone " + c.Dir +
						" of a clone-mode profile; host tools must never run there (use agent-box clone fetch)", true
				}
			}
		}
	}
	return "", false
}

// spellings is p cleaned, plus its resolved form when that differs.
func spellings(p string) []string {
	clean := filepath.Clean(p)
	if real := resolveExisting(clean); real != clean {
		return []string{clean, real}
	}
	return []string{clean}
}

// resolveExisting resolves p's symlinks; for a path that does not exist (yet),
// it resolves the deepest existing ancestor and joins the rest unchanged.
func resolveExisting(p string) string {
	tail := ""
	for cur := p; ; {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Join(real, tail)
		}
		parent := filepath.Dir(cur)
		if !errors.Is(err, fs.ErrNotExist) || parent == cur {
			return p
		}
		tail = filepath.Join(filepath.Base(cur), tail)
		cur = parent
	}
}

// under reports whether p is parent or below it, by whole components. Both
// must be clean.
func under(p, parent string) bool {
	if p == parent || parent == "/" {
		return true
	}
	return strings.HasPrefix(p, parent+"/")
}
