package vmshares

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/culiops/culibox/internal/box"
)

// Refusal kinds.
const (
	KindUnsafe       = "unsafe"
	KindRoot         = "root"
	KindHome         = "home"
	KindContainsHome = "contains-home"
	KindSensitive    = "sensitive"
	KindData         = "data"
	KindProtected    = "protected"
	KindNotDir       = "notdir"
	KindRelative     = "relative"
)

const (
	reasonUnsafe    = "has a character the box VM config cannot carry (a quote, backslash, comma or control character); rename the folder."
	reasonHome      = "is your home folder, or contains it; share the folders inside it that boxes use instead."
	reasonSensitive = "holds keys and sockets, so it can never be shared with the box VM."
	reasonData      = "overlaps CuliBox's data folder, which is always shared in the parts boxes need."
	reasonNotDir    = "is a file, not a folder."
	reasonRelative  = "Use a full path, like ~/code/project."
)

const reasonProtectedFmt = "holds CuliBox's own %s, so it can never be shared with the box VM."

// Refused is a folder the box VM may not share. Error() is Display(Path)+" "+Reason.
type Refused struct {
	Path, Kind, Reason string
	home               string
}

func (r *Refused) Error() string { return Display(r.Path, r.home) + " " + r.Reason }

// sensitiveDirs are the home-relative folders that hold keys and sockets.
var sensitiveDirs = []string{".aws", ".gnupg", ".kube", "Library", ".config/gcloud", ".config/gh", ".azure", ".terraform.d", ".password-store", ".config/op"}

// resolveSpelling is Resolve; a test swaps it to stand a symlink in for a
// macOS firmlink, an alias that symlink resolution does not see.
var resolveSpelling = Resolve

// Check returns nil or a *Refused for p. The rules run on both p and its
// symlink-resolved form; Path in the error is p as given.
func Check(p string, env Env) error {
	refuse := func(kind, reason string) error {
		return &Refused{Path: p, Kind: kind, Reason: reason, home: env.Home}
	}
	if !filepath.IsAbs(p) {
		return refuse(KindRelative, reasonRelative)
	}
	p = filepath.Clean(p)
	for _, c := range []string{p, resolveSpelling(p)} {
		if box.MountUnsafe(c) != "" || YAMLSafe(c) != nil {
			return refuse(KindUnsafe, reasonUnsafe)
		}
		if c == "/" {
			return refuse(KindRoot, reasonHome)
		}
		// macOS firmlink roots: /System/Volumes/Data holds /Users, so it
		// contains the home although no ancestor of the home names it. Folded
		// on every OS; harmless where /System does not exist.
		if Within(c, "/System", true) {
			return refuse(KindContainsHome, reasonHome)
		}
		if Within(c, env.Home, env.Fold) && Within(env.Home, c, env.Fold) {
			return refuse(KindHome, reasonHome)
		}
		if Within(env.Home, c, env.Fold) {
			return refuse(KindContainsHome, reasonHome)
		}
		if sensitive(c, env) {
			return refuse(KindSensitive, reasonSensitive)
		}
		if env.DataRoot != "" && (Within(c, env.DataRoot, env.Fold) || Within(env.DataRoot, c, env.Fold)) {
			return refuse(KindData, reasonData)
		}
		if what := protectedHit(c, env); what != "" {
			return refuse(KindProtected, fmt.Sprintf(reasonProtectedFmt, what))
		}
	}
	for _, c := range []string{p, resolveSpelling(p)} {
		if kind, reason := identityHit(c, env); kind != "" {
			return refuse(kind, reason)
		}
	}
	if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
		return refuse(KindNotDir, reasonNotDir)
	}
	return nil
}

// identityHit runs the home, sensitive, data-root and protected rules again by
// file identity rather than spelling: a macOS firmlink
// (/System/Volumes/Data/Users/<u> is /Users/<u>) or an APFS Unicode
// normalisation variant names the same folder with a spelling the string rules
// and symlink resolution never see. A rule only fires for a refused directory
// that exists; c is inside it when an existing ancestor-or-self of c is it, and
// contains it when an existing ancestor-or-self of it is c.
func identityHit(c string, env Env) (kind, reason string) {
	ids := idCache{}
	if root := ids.stat("/"); root != nil {
		if fi := ids.stat(c); fi != nil && os.SameFile(fi, root) {
			return KindRoot, reasonHome
		}
	}
	if env.Home != "" {
		hfi, cfi := ids.stat(env.Home), ids.stat(c)
		if hfi != nil && cfi != nil && os.SameFile(hfi, cfi) {
			return KindHome, reasonHome
		}
		if ids.contains(c, env.Home) {
			return KindContainsHome, reasonHome
		}
		dirs := box.HostSocketDirs(env.Home)
		for _, d := range sensitiveDirs {
			dirs = append(dirs, filepath.Join(env.Home, d))
		}
		for _, d := range dirs {
			if _, in := ids.inside(c, d); in || ids.contains(c, d) {
				return KindSensitive, reasonSensitive
			}
		}
		claude := filepath.Join(env.Home, ".claude")
		if ids.contains(c, claude) {
			return KindSensitive, reasonSensitive // equal to or containing ~/.claude
		}
		if rel, in := ids.inside(c, claude); in && !memoryRel(rel, env.Fold) {
			return KindSensitive, reasonSensitive
		}
	}
	if env.DataRoot != "" {
		if _, in := ids.inside(c, env.DataRoot); in || ids.contains(c, env.DataRoot) {
			return KindData, reasonData
		}
	}
	claudeFI := ids.stat(filepath.Join(env.Home, ".claude"))
	for _, d := range env.Protected {
		if d.Path == "" {
			continue
		}
		// sensitive() owns the default account's ~/.claude, as in protectedHit.
		if fi := ids.stat(d.Path); env.Home != "" && fi != nil && claudeFI != nil && os.SameFile(fi, claudeFI) {
			continue
		}
		if ids.contains(c, d.Path) {
			return KindProtected, fmt.Sprintf(reasonProtectedFmt, d.What)
		}
		if _, in := ids.inside(c, d.Path); d.Inside && in {
			return KindProtected, fmt.Sprintf(reasonProtectedFmt, d.What)
		}
	}
	return "", ""
}

// memoryRel reports whether rel (the components below ~/.claude) is
// projects/<slug>/memory or below it: the one part of ~/.claude a share may be.
func memoryRel(rel []string, fold bool) bool {
	return len(rel) >= 3 && eq(rel[0], "projects", fold) && eq(rel[2], "memory", fold)
}

// idCache is os.Stat (following links) per path, for one Check; nil = missing.
type idCache map[string]os.FileInfo

func (ids idCache) stat(p string) os.FileInfo {
	if fi, ok := ids[p]; ok {
		return fi
	}
	fi, err := os.Stat(p)
	if err != nil {
		fi = nil
	}
	ids[p] = fi
	return fi
}

// inside reports whether some existing ancestor-or-self of c is dir, and the
// components of c below that ancestor. They are taken from the real paths
// (links followed), not the spelling: a link below the ancestor can make a
// path that reads as ~/.claude/projects/<slug>/memory land elsewhere in
// ~/.claude. A match whose real path leaves the ancestor is no match.
func (ids idCache) inside(c, dir string) ([]string, bool) {
	dfi := ids.stat(dir)
	if dfi == nil {
		return nil, false
	}
	rc := Resolve(c)
	for a := filepath.Clean(c); ; {
		if fi := ids.stat(a); fi != nil && os.SameFile(fi, dfi) {
			if ra := Resolve(a); Within(rc, ra, false) {
				return parts(rc)[len(parts(ra)):], true
			}
		}
		up := filepath.Dir(a)
		if up == a {
			return nil, false
		}
		a = up
	}
}

// contains reports whether c exists and some existing ancestor-or-self of dir is c.
func (ids idCache) contains(c, dir string) bool {
	cfi := ids.stat(c)
	if cfi == nil {
		return false
	}
	for a := filepath.Clean(dir); ; {
		if fi := ids.stat(a); fi != nil && os.SameFile(fi, cfi) {
			return true
		}
		up := filepath.Dir(a)
		if up == a {
			return false
		}
		a = up
	}
}

// spellings is d as written and, when it differs, d with symlinks resolved:
// ~/.aws may be a link to ~/dotfiles/aws, and sharing the target is sharing it.
func spellings(d string) []string {
	if r := resolveSpelling(d); r != d {
		return []string{d, r}
	}
	return []string{d}
}

func sensitive(c string, env Env) bool {
	dirs := box.HostSocketDirs(env.Home)
	for _, d := range sensitiveDirs {
		dirs = append(dirs, filepath.Join(env.Home, d))
	}
	for _, d := range dirs {
		for _, s := range spellings(d) {
			if Within(c, s, env.Fold) || Within(s, c, env.Fold) {
				return true
			}
		}
	}
	for _, claude := range spellings(filepath.Join(env.Home, ".claude")) {
		if Within(env.Home, c, env.Fold) || !Within(c, claude, env.Fold) && !Within(claude, c, env.Fold) {
			continue
		}
		if !Within(c, claude, env.Fold) || Within(claude, c, env.Fold) {
			return true // equal to or containing ~/.claude
		}
		// Inside ~/.claude: only projects/<slug>/memory[/...] is allowed.
		rel := parts(c)[len(parts(claude)):]
		if !memoryRel(rel, env.Fold) {
			return true
		}
	}
	return false
}

// protectedHit is the What of the first env.Protected directory that c equals
// or contains, or (when the directory sets Inside) sits inside; "" if none. The
// default account's ~/.claude is skipped here: sensitive() owns it, with its
// projects/<slug>/memory exception.
func protectedHit(c string, env Env) string {
	claude := spellings(filepath.Join(env.Home, ".claude"))
	for _, d := range env.Protected {
		if d.Path == "" {
			continue
		}
		spell := spellings(d.Path)
		skip := false
		for _, s := range spell {
			for _, cl := range claude {
				if Within(s, cl, env.Fold) && Within(cl, s, env.Fold) {
					skip = true
				}
			}
		}
		if skip {
			continue
		}
		for _, s := range spell {
			if Within(s, c, env.Fold) || d.Inside && Within(c, s, env.Fold) {
				return d.What
			}
		}
	}
	return ""
}
