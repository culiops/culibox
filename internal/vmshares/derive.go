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
	// Every clone profile's original, resolved: the VM mounts resolved paths.
	type origin struct{ name, key string }
	var origins []origin
	for _, p := range in.Profiles {
		if p.Clone && p.CloneOf != "" {
			origins = append(origins, origin{p.Name, Resolve(filepath.Clean(p.CloneOf))})
		}
	}
	var own []Skip
	for _, p := range in.Profiles {
		if p.Clone {
			add(p.CloneOf, RO)
			if p.Workspace == "" {
				continue
			}
			// The copy parent, rw, so new copies appear without an apply. Not
			// when it holds any profile's clone_of (a copy kept beside an
			// original): that would make the original writable, so share the
			// copy itself, created or not yet.
			dir := filepath.Clean(filepath.Dir(p.Workspace))
			rd := Resolve(dir)
			for _, o := range origins {
				if Within(o.key, rd, in.Env.Fold) {
					dir = filepath.Clean(p.Workspace)
					rd = Resolve(dir)
					break
				}
			}
			// A copy kept inside an original: sharing it rw would make
			// Normalize promote that original to rw. Skip it, with the reason.
			inside := ""
			for _, o := range origins {
				if Within(rd, o.key, in.Env.Fold) {
					inside = o.name
					break
				}
			}
			if inside != "" {
				own = append(own, Skip{Path: dir, Reason: "inside the original of profile " + inside +
					", which boxes may only read; share it in Settings › VM folders if you need it"})
				continue
			}
			add(dir, RW)
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
	list, skipped = Normalize(raw, in.Env)
	return list, append(own, skipped...)
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
