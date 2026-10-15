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
type ProtectedDir struct {
	Path string // as configured; resolved when compared
	What string // e.g. "agent-box's data directory"
	Why  string // what a box could do there, e.g. "holds the box VM's controls"
}

// DataDirProtected is the data root: the box VM's colima.yaml (its provision
// script runs as root in the VM), lima's SSH key for the VM user, and by
// default the config and profiles.
func DataDirProtected(p string) ProtectedDir {
	return ProtectedDir{p, "agent-box's data directory",
		"holds the box VM's controls, its SSH key and, by default, agent-box's config and profiles"}
}

// ConfigDirProtected is a directory agent-box reads its config file from. The
// config names the binaries agent-box runs (docker_bin, colima_bin, agents).
func ConfigDirProtected(p string) ProtectedDir {
	return ProtectedDir{p, "agent-box's config directory",
		"holds the config that names the binaries agent-box runs"}
}

// ProfilesDirProtected is the directory of profiles.json, which names the
// binaries sessions run and their tier.
func ProfilesDirProtected(p string) ProtectedDir {
	return ProtectedDir{p, "agent-box's profiles directory",
		"holds the profiles that name the binaries sessions run and whether they are contained"}
}

// DedupeProtected drops later entries that resolve to an earlier entry's
// directory, so one directory is reported once, under its first meaning.
func DedupeProtected(dirs []ProtectedDir) []ProtectedDir {
	var out []ProtectedDir
	seen := map[string]bool{}
	for _, d := range dirs {
		if d.Path == "" {
			continue
		}
		key := resolveExisting(d.Path)
		if runtime.GOOS == "darwin" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, d)
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
		p := resolveExisting(d.Path)
		var how string
		switch {
		case pathWithin(ws, p, fold) && pathWithin(p, ws, fold):
			how = "it is "
		case pathWithin(p, ws, fold):
			how = "it contains "
		case pathWithin(ws, p, fold):
			how = "it is inside "
		default:
			continue
		}
		return how + d.What + " (" + p + "), which " + d.Why
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
