// Package wsroots holds the rule for which folders may never become a
// workspace root or a box workspace. It is a leaf package (standard library
// only) so the box layer and the web layer can both use the one rule.
package wsroots

import (
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// foldCase makes the comparisons ignore case, as macOS volumes do by default
// (box/protected.go folds the same way). A variable so a test can set it.
var foldCase = runtime.GOOS == "darwin"

// evalSymlinks is filepath.EvalSymlinks; a variable so a test can count calls.
var evalSymlinks = filepath.EvalSymlinks

// Env is what the rule needs from the running machine.
type Env struct {
	// Home is the user's home directory, symlinks resolved. Empty adds no
	// credential rules.
	Home string
	// DataDir is CuliBox's own data directory. Empty adds no rule.
	DataDir string
}

// systemWhole are the system trees refused when the candidate is one, is
// inside one, or contains one.
var systemWhole = []string{"/etc", "/proc", "/sys", "/dev", "/boot", "/usr", "/System", "/Library"}

// systemIsOrContains are refused when the candidate is one or contains one;
// a project inside them (/var/www/app) is allowed.
var systemIsOrContains = []string{"/var"}

// credentialDirs are the credential folders under home, refused as a whole
// tree. A folder that contains home contains these, so home's ancestors are
// refused through this rule (it is lexical: .ssh need not exist).
var credentialDirs = []string{".ssh", ".aws", ".gnupg"}

// The system folders with their resolved forms, resolved once: they are
// fixed, and Refusal runs per root on every policy build and per folder in
// the "Other places" scan.
var (
	systemOnce                   sync.Once
	systemWholeV, systemIsOrConV []string
)

func systemVariants() (whole, isOrContains []string) {
	systemOnce.Do(func() {
		systemWholeV, systemIsOrConV = variants(systemWhole), variants(systemIsOrContains)
	})
	return systemWholeV, systemIsOrConV
}

// Refusal reports why abs can never be a workspace root, or "" when it can.
// abs must be absolute and symlink-resolved. The reason is plain words and
// names only a class of folder. On darwin case is ignored (foldCase).
func Refusal(abs string, env Env) string {
	abs = filepath.Clean(abs)
	// "/" first: it contains everything, /etc included.
	if abs == string(filepath.Separator) {
		return "it is the filesystem root"
	}
	whole, isOrContains := systemVariants()
	if reason := refusalIn(abs, whole, isOrContains); reason != "" {
		return reason
	}
	if env.Home != "" {
		var creds []string
		for _, d := range credentialDirs {
			creds = append(creds, filepath.Join(env.Home, d))
		}
		creds = variants(creds)
		if matches(abs, creds, true) {
			return "it holds credentials"
		}
		if contains(abs, creds) {
			return "it contains your credential folders"
		}
	}
	if env.DataDir != "" {
		if d := variants([]string{env.DataDir}); matches(abs, d, true) || contains(abs, d) {
			return "it holds CuliBox's own data"
		}
	}
	return ""
}

// refusalWith is the system-folder part of Refusal, with the bases given:
// whole are refused when abs is, is inside or contains one; isOrContains when
// abs is or contains one (inside is allowed).
func refusalWith(abs string, whole, isOrContains []string) string {
	return refusalIn(abs, variants(whole), variants(isOrContains))
}

// refusalIn is refusalWith over bases already expanded by variants.
func refusalIn(abs string, whole, isOrContains []string) string {
	if matches(abs, whole, true) || matches(abs, isOrContains, false) {
		return "it is a system folder"
	}
	if contains(abs, whole) || contains(abs, isOrContains) {
		return "it contains a system folder"
	}
	return ""
}

// variants returns each base as spelled and, when it resolves to something
// else, as its symlink-resolved path (macOS: /etc -> /private/etc).
func variants(bases []string) []string {
	out := make([]string, 0, len(bases)*2)
	for _, b := range bases {
		b = filepath.Clean(b)
		out = append(out, b)
		if real, err := evalSymlinks(b); err == nil && real != b {
			out = append(out, real)
		}
	}
	return out
}

// within reports whether p is b or lies inside it, component-wise.
func within(p, b string) bool {
	return p == b || strings.HasPrefix(p, b+string(filepath.Separator))
}

// foldKey is p as Refusal compares it: lower case where case is ignored.
func foldKey(p string) string {
	if foldCase {
		return strings.ToLower(p)
	}
	return p
}

// matches reports whether p is one of the bases (already expanded by
// variants), or, with inside, lies inside one.
func matches(p string, bases []string, inside bool) bool {
	p = foldKey(p)
	for _, b := range bases {
		b = foldKey(b)
		if p == b || (inside && within(p, b)) {
			return true
		}
	}
	return false
}

// contains reports whether p is a strict ancestor of one of the bases (already
// expanded by variants).
func contains(p string, bases []string) bool {
	p = foldKey(p)
	for _, b := range bases {
		b = foldKey(b)
		if p != b && within(b, p) {
			return true
		}
	}
	return false
}
