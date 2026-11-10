package vmshares

import (
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

// Refused is a folder the box VM may not share. Error() is Display(Path)+" "+Reason.
type Refused struct {
	Path, Kind, Reason string
	home               string
}

func (r *Refused) Error() string { return Display(r.Path, r.home) + " " + r.Reason }

// sensitiveDirs are the home-relative folders that hold keys and sockets.
var sensitiveDirs = []string{".aws", ".gnupg", ".kube", "Library", ".config/gcloud", ".config/gh", ".azure", ".terraform.d", ".password-store", ".config/op"}

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
	for _, c := range []string{p, Resolve(p)} {
		if box.MountUnsafe(c) != "" || YAMLSafe(c) != nil {
			return refuse(KindUnsafe, reasonUnsafe)
		}
		if c == "/" {
			return refuse(KindRoot, reasonHome)
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
	}
	if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
		return refuse(KindNotDir, reasonNotDir)
	}
	return nil
}

// spellings is d as written and, when it differs, d with symlinks resolved:
// ~/.aws may be a link to ~/dotfiles/aws, and sharing the target is sharing it.
func spellings(d string) []string {
	if r := Resolve(d); r != d {
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
		if !(len(rel) >= 3 && eq(rel[0], "projects", env.Fold) && eq(rel[2], "memory", env.Fold)) {
			return true
		}
	}
	return false
}
