package box

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
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

	// ContextHint, when set, is appended to a context_paths refusal that names
	// this directory (never to a workspace refusal): where to go instead.
	ContextHint string
}

// DataDirProtected is the data root: the box VM's colima.yaml (its provision
// script runs as root in the VM), lima's SSH key for the VM user, and by
// default the config and profiles.
func DataDirProtected(p string) ProtectedDir {
	return ProtectedDir{Path: p, What: "CuliBox's data directory",
		Why:    "holds the box VM's controls, its SSH key and, by default, CuliBox's config and profiles",
		Inside: true}
}

// ConfigDirProtected is a directory agent-box reads its config file from. The
// config names the binaries agent-box runs (docker_bin, colima_bin, agents).
func ConfigDirProtected(p string) ProtectedDir {
	return ProtectedDir{Path: p, What: "CuliBox's config directory",
		Why: "holds the config that names the binaries CuliBox runs"}
}

// ProfilesDirProtected is the directory of profiles.json, which names the
// binaries sessions run and their tier.
func ProfilesDirProtected(p string) ProtectedDir {
	return ProtectedDir{Path: p, What: "CuliBox's profiles directory",
		Why: "holds the profiles that name the binaries sessions run and whether they are contained"}
}

// PersonalConfigDirProtected is the default account's config dir (~/.claude).
// Its settings.json hooks and hook scripts run on the host in every host-tier
// session, and inheriting agent accounts copy that settings.json. Inside is
// set: the scripts a hook names sit in subdirectories.
func PersonalConfigDirProtected(p string) ProtectedDir {
	return ProtectedDir{Path: p, What: "the personal config directory",
		Why:         "holds the hooks, scripts and settings that host sessions and inheriting accounts run",
		Inside:      true,
		ContextHint: "to give the box files your CLAUDE.md imports, use imports (on by default)"}
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
//
// The result is a UNION of three views, deduplicated in order. Dropping any
// one reopened a hole in an earlier attempt, so none is a fallback for another:
//
//  1. the hops as SPELLED: a relative target joined onto the link's own
//     directory as text (where a link can be swapped from a parent as written);
//  2. the hops as the KERNEL walks them (walkChain): every component resolved
//     one at a time, the real parent of each link followed (directory links
//     too) and of each hop's final name, a relative target taken from that
//     REAL directory, ".." applied to the resolved prefix, never lexically;
//  3. where the chain breaks, the real directory in which the missing part
//     would be created, found by following dangling links too.
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
	out = append(out, kernelWalkDirs(file)...)
	var uniq []string
	for _, d := range out {
		if !slices.Contains(uniq, d) {
			uniq = append(uniq, d)
		}
	}
	return uniq
}

// kernelWalkDirs is views 2 and 3 of FileDirs: the directories walkChain
// records for file, taken from the raw (never cleaned) path.
func kernelWalkDirs(file string) []string {
	raw := file
	if !filepath.IsAbs(raw) {
		wd, err := os.Getwd()
		if err != nil {
			return nil
		}
		raw = wd + string(filepath.Separator) + raw
	}
	dirs, _ := walkChain(raw)
	return dirs
}

// maxLinks caps the symlinks one walk follows, like the kernel's ELOOP limit.
const maxLinks = 40

// walkChain resolves raw one component at a time, as the kernel does, and
// returns the real directories a box could write to change what it reaches:
//
//   - the real parent of every symlink it follows, directory links included
//     (that is where the link can be swapped);
//   - the real parent of the final name of the path and of each spliced link
//     target (where the file or the next link lives);
//   - when a component is missing after a link was followed, or a directory
//     component is missing at all, the real directory it would be created in
//     (a dangling link does not end the chain: creating its target completes
//     it). A plain missing final file with no link adds nothing: its spelled
//     directory is already protected.
//
// ".." applies to the already-resolved directory, never lexically. end is the
// real path reached, or the real directory the first missing component would
// be created in. A loop of more than maxLinks links, or an unreadable
// component, records the directory reached and stops, which fails closed.
func walkChain(raw string) (dirs []string, end string) {
	sep := string(filepath.Separator)
	split := func(p string) []string {
		var c []string
		for _, n := range strings.Split(p, sep) {
			if n != "" && n != "." {
				c = append(c, n)
			}
		}
		return c
	}
	resolved := sep
	queue := split(raw)
	links := 0
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if name == ".." {
			resolved = filepath.Dir(resolved)
			continue
		}
		last := len(queue) == 0
		cand := filepath.Join(resolved, name)
		fi, err := os.Lstat(cand)
		switch {
		case err != nil:
			if !os.IsNotExist(err) || !last || links > 0 {
				dirs = append(dirs, resolved)
			}
			return dirs, resolved
		case fi.Mode()&os.ModeSymlink != 0:
			links++
			dirs = append(dirs, resolved)
			t, err := os.Readlink(cand)
			if links > maxLinks || err != nil {
				return dirs, resolved
			}
			if filepath.IsAbs(t) {
				resolved = sep
			}
			queue = append(split(t), queue...)
		default:
			if last {
				dirs = append(dirs, resolved)
			}
			resolved = cand
		}
	}
	return dirs, resolved
}

// deepestExisting follows p link by link and returns where it ends: its real
// path, or the real directory in which its first missing component would be
// created.
func deepestExisting(p string) string {
	_, end := walkChain(p)
	return end
}

// ProtectedOverlap reports why workspace cannot be boxed because it is,
// contains or sits inside one of dirs, or "" when it overlaps none. On macOS
// the comparison ignores case: APFS does, so /Users/a/AB is /Users/a/ab.
// The box manager and the web layer both call it, so they cannot drift.
func ProtectedOverlap(workspace string, dirs []ProtectedDir) string {
	return protectedOverlap(workspace, dirs, runtime.GOOS == "darwin")
}

// ProtectedOverlapHint is ProtectedOverlap for a context_paths entry: it also
// returns the matched directory's ContextHint ("" when it has none), so the
// refusal can say where to go instead. A workspace refusal does not use it.
func ProtectedOverlapHint(p string, dirs []ProtectedDir) (reason, hint string) {
	return protectedOverlapHint(p, dirs, runtime.GOOS == "darwin")
}

func protectedOverlap(workspace string, dirs []ProtectedDir, fold bool) string {
	reason, _ := protectedOverlapHint(workspace, dirs, fold)
	return reason
}

func protectedOverlapHint(workspace string, dirs []ProtectedDir, fold bool) (string, string) {
	return protectedOverlapHintAt(resolveExisting(workspace), dirs, fold)
}

// protectedOverlapResolved is ProtectedOverlap for a workspace the caller has
// already resolved (see resolveWorkspace): ws is compared as given and is not
// resolved again, so the answer is about the very path the caller goes on to
// name, guard and mount. Called with an unresolved path it would compare the
// spelling, not the directory; ProtectedOverlap is the one that resolves.
func protectedOverlapResolved(ws string, dirs []ProtectedDir) string {
	reason, _ := protectedOverlapHintAt(ws, dirs, runtime.GOOS == "darwin")
	return reason
}

// protectedOverlapHintAt is protectedOverlapHint for a ws that is already
// absolute and resolved. The protected directories themselves are still
// resolved here: they are configuration, not the workspace.
func protectedOverlapHintAt(ws string, dirs []ProtectedDir, fold bool) (string, string) {
	for _, d := range dirs {
		if d.Path == "" {
			continue
		}
		// Both spellings: the resolved one is where the files are; the one as
		// spelled matters when a link in it could be swapped from a parent.
		// A third: the link in its resolved parent, which is where a workspace
		// that holds it is spelled when a parent is itself a link (macOS /var).
		spellings := []string{resolveExisting(d.Path)}
		if abs, err := filepath.Abs(d.Path); err == nil {
			inParent := filepath.Join(resolveExisting(filepath.Dir(abs)), filepath.Base(abs))
			for _, p := range []string{abs, inParent} {
				if !slices.Contains(spellings, p) {
					spellings = append(spellings, p)
				}
			}
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
			return how + d.What + " (" + p + "), which " + d.Why, d.ContextHint
		}
	}
	return "", ""
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

// ProtectedDirsFor is every host directory a boxed workspace or a box VM share
// must not overlap, built the same way for every caller (serve, the box and
// clone CLIs, doctor): the data root, the default account's config dir
// (personalDir, ~/.claude), the config bundle's source when it is another dir
// (bundleSrc, "" with the bundle off), and the directory of each config file
// (cfgFiles) and of the profiles file, every symlink hop included (FileDirs).
func ProtectedDirsFor(dataDir string, cfgFiles []string, profilesPath, personalDir, bundleSrc string) []ProtectedDir {
	out := []ProtectedDir{DataDirProtected(dataDir), PersonalConfigDirProtected(personalDir)}
	if bundleSrc != "" && !sameDir(bundleSrc, personalDir) {
		out = append(out, PersonalConfigDirProtected(bundleSrc))
	}
	for _, f := range cfgFiles {
		for _, d := range FileDirs(f) {
			out = append(out, ConfigDirProtected(d))
		}
	}
	for _, d := range FileDirs(profilesPath) {
		out = append(out, ProfilesDirProtected(d))
	}
	return DedupeProtected(out)
}
