package box

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ProtectedDir is a host directory a boxed workspace must not be, contain or
// sit inside: the box's workspace is a writable mount, and each of these holds
// something that runs code on the host or in the VM once rewritten.
//
// A workspace that IS or CONTAINS a protected directory is always refused. One
// INSIDE it is refused only when Inside is set: the data root's sensitive files
// sit deep in it, while a config or profiles directory's file sits directly in
// it, out of reach of a box mounted below it (and refusing "inside" there would
// make all of $HOME unboxable when profiles.json sits directly in $HOME).
type ProtectedDir struct {
	Path   string // as configured; compared both as spelled and resolved
	What   string // e.g. "agent-box's data directory"
	Why    string // what a box could do there, e.g. "holds the box VM's controls"
	Inside bool   // also refuse a workspace below Path
}

// DataDirProtected is the data root: the box VM's colima.yaml (its provision
// script runs as root in the VM), lima's SSH key for the VM user, and by
// default the config and profiles.
func DataDirProtected(p string) ProtectedDir {
	return ProtectedDir{p, "agent-box's data directory",
		"holds the box VM's controls, its SSH key and, by default, agent-box's config and profiles", true}
}

// ConfigDirProtected is a directory agent-box reads its config file from. The
// config names the binaries agent-box runs (docker_bin, colima_bin, agents).
func ConfigDirProtected(p string) ProtectedDir {
	return ProtectedDir{p, "agent-box's config directory",
		"holds the config that names the binaries agent-box runs", false}
}

// ProfilesDirProtected is the directory of profiles.json, which names the
// binaries sessions run and their tier.
func ProfilesDirProtected(p string) ProtectedDir {
	return ProtectedDir{p, "agent-box's profiles directory",
		"holds the profiles that name the binaries sessions run and whether they are contained", false}
}

// DedupeProtected drops later entries that name an earlier entry's directory
// (same spelling and same resolved path), so one directory is reported once,
// under its first meaning. An entry that refuses more (Inside) is never
// dropped in favour of one that refuses less.
func DedupeProtected(dirs []ProtectedDir) []ProtectedDir {
	var out []ProtectedDir
	seen := map[string]bool{}
	for _, d := range dirs {
		if d.Path == "" {
			continue
		}
		key := filepath.Clean(d.Path) + "\x00" + resolveExisting(d.Path)
		if runtime.GOOS == "darwin" {
			key = strings.ToLower(key)
		}
		if (d.Inside && seen[key+"\x00inside"]) || (!d.Inside && seen[key]) {
			continue
		}
		seen[key] = true
		if d.Inside {
			seen[key+"\x00inside"] = true
		}
		out = append(out, d)
	}
	return out
}

// FileDirs is the directory of file and of every hop of its symlink chain,
// ending at the target's. agent-box reads such a file through its links, so a
// box that could write any of these directories could swap a link or rewrite
// the content. A missing file gives its own directory only.
func FileDirs(file string) []string {
	p, err := filepath.Abs(file)
	if err != nil {
		return nil
	}
	out := []string{filepath.Dir(p)}
	for hop := 0; hop < 40; hop++ {
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			break
		}
		t, err := os.Readlink(p)
		if err != nil {
			break
		}
		if !filepath.IsAbs(t) {
			t = filepath.Join(filepath.Dir(p), t)
		}
		p = filepath.Clean(t)
		out = append(out, filepath.Dir(p))
	}
	// A directory component can be a link too; the resolved file settles where
	// the content really is.
	if real, err := filepath.EvalSymlinks(file); err == nil {
		if r, err := filepath.Abs(real); err == nil {
			out = append(out, filepath.Dir(r))
		}
	}
	return out
}

// ProtectedOverlap reports why workspace cannot be boxed because it is,
// contains or sits inside one of dirs, or "" when it overlaps none. On macOS
// the comparison ignores case: APFS does, so /Users/a/AB is /Users/a/ab.
// The box manager and the web layer both call it, so they cannot drift.
func ProtectedOverlap(workspace string, dirs []ProtectedDir) string {
	return protectedOverlap(workspace, dirs, runtime.GOOS == "darwin")
}

func protectedOverlap(workspace string, dirs []ProtectedDir, fold bool) string {
	ws := resolveExisting(workspace)
	for _, d := range dirs {
		if d.Path == "" {
			continue
		}
		// Both spellings: the resolved one is where the files are; the one as
		// spelled matters when a link in it could be swapped from a parent.
		spellings := []string{resolveExisting(d.Path)}
		if abs, err := filepath.Abs(d.Path); err == nil && abs != spellings[0] {
			spellings = append(spellings, abs)
		}
		for _, p := range spellings {
			var how string
			switch {
			case pathWithin(ws, p, fold) && pathWithin(p, ws, fold):
				how = "it is "
			case pathWithin(p, ws, fold):
				how = "it contains "
			case d.Inside && pathWithin(ws, p, fold):
				how = "it is inside "
			default:
				continue
			}
			return how + d.What + " (" + p + "), which " + d.Why
		}
	}
	return ""
}

// pathWithin reports whether child is parent or below it, comparing whole
// components (so /x/ab is not within /x/a), ignoring case when fold is set.
func pathWithin(child, parent string, fold bool) bool {
	c := splitPath(child)
	p := splitPath(parent)
	if len(c) < len(p) {
		return false
	}
	for i := range p {
		if c[i] != p[i] && !(fold && strings.EqualFold(c[i], p[i])) {
			return false
		}
	}
	return true
}

func splitPath(p string) []string {
	p = filepath.Clean(p)
	if p == string(filepath.Separator) {
		return nil
	}
	return strings.Split(strings.TrimPrefix(p, string(filepath.Separator)), string(filepath.Separator))
}

// resolveExisting is p made absolute with symlinks resolved in its longest
// existing prefix, so a directory that does not exist yet (a config dir before
// its first file) is still spelled the way a resolved workspace would be.
func resolveExisting(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	rest := ""
	cur := abs
	for {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(real, rest)
		} else if !os.IsNotExist(err) {
			return abs
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}
