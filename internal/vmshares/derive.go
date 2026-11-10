package vmshares

import (
	"os"
	"path/filepath"
	"sort"
)

// Profile is a boxed profile as far as shares care. The caller maps
// config.AgentConfig through RuntimeFor; host profiles are left out.
type Profile struct {
	Name, Workspace, CloneOf string
	Clone                    bool
	ContextPaths             []string
}

// DeriveInput is everything the first-run list is made from.
type DeriveInput struct {
	Profiles   []Profile
	Recorded   []string // workspaces boxed sessions ran in (boxedws)
	MemoryDirs []string // project-memory dirs; kept only if they exist
	Env        Env
}

// Derive builds the narrowest list that keeps existing profiles working.
func Derive(in DeriveInput) (list []Share, skipped []Skip) {
	var raw []Share
	add := func(p string, m Mode) {
		if p != "" {
			raw = append(raw, Share{Path: filepath.Clean(p), Mode: m})
		}
	}
	for _, p := range in.Profiles {
		if !p.Clone {
			add(p.Workspace, RW)
		}
	}
	for _, r := range in.Recorded {
		add(r, RW)
	}
	for _, p := range in.Profiles {
		if p.Clone {
			add(p.CloneOf, RO)
			if p.Workspace != "" {
				// The copy parent, rw, so new copies appear without an apply.
				// Not when it holds clone_of (a copy kept beside its original):
				// that would make the original writable, so share the copy
				// itself, created or not yet.
				dir := filepath.Dir(p.Workspace)
				if p.CloneOf != "" && Within(filepath.Clean(p.CloneOf), dir, in.Env.Fold) {
					dir = p.Workspace
				}
				add(dir, RW)
			}
		}
	}
	for _, p := range in.Profiles {
		for _, c := range p.ContextPaths {
			add(c, RO)
		}
	}
	for _, m := range in.MemoryDirs {
		if fi, err := os.Lstat(m); err == nil && fi.IsDir() {
			add(m, RO)
		}
	}
	return Normalize(raw, in.Env)
}

// CopyParents are the directories clone profiles keep their copies in. The
// caller creates them (0700) before saving, so they exist when mounted.
func CopyParents(profiles []Profile) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, p := range profiles {
		if p.Clone && p.Workspace != "" {
			d := filepath.Dir(p.Workspace)
			if !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	sort.Strings(out)
	return out
}
