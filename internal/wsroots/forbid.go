// Package wsroots holds the rule for which folders may never become a
// workspace root or a box workspace. It is a leaf package (standard library
// only) so the box layer and the web layer can both use the one rule.
package wsroots

import (
	"path/filepath"
	"strings"
)

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

// Refusal reports why abs can never be a workspace root, or "" when it can.
// abs must be absolute and symlink-resolved. The reason is plain words and
// names only a class of folder.
func Refusal(abs string, env Env) string {
	abs = filepath.Clean(abs)
	// "/" first: it contains everything, /etc included.
	if abs == string(filepath.Separator) {
		return "it is the filesystem root"
	}
	if reason := refusalWith(abs, systemWhole, systemIsOrContains); reason != "" {
		return reason
	}
	if env.Home != "" {
		var creds []string
		for _, d := range credentialDirs {
			creds = append(creds, filepath.Join(env.Home, d))
		}
		if matches(abs, creds, true) {
			return "it holds credentials"
		}
		if contains(abs, creds) {
			return "it contains your credential folders"
		}
	}
	if env.DataDir != "" {
		if d := []string{env.DataDir}; matches(abs, d, true) || contains(abs, d) {
			return "it holds CuliBox's own data"
		}
	}
	return ""
}

// refusalWith is the system-folder part of Refusal, with the bases given:
// whole are refused when abs is, is inside or contains one; isOrContains when
// abs is or contains one (inside is allowed).
func refusalWith(abs string, whole, isOrContains []string) string {
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
		if real, err := filepath.EvalSymlinks(b); err == nil && real != b {
			out = append(out, real)
		}
	}
	return out
}

// within reports whether p is b or lies inside it, component-wise.
func within(p, b string) bool {
	return p == b || strings.HasPrefix(p, b+string(filepath.Separator))
}

// matches reports whether p is one of the bases, or, with inside, lies inside
// one.
func matches(p string, bases []string, inside bool) bool {
	for _, b := range variants(bases) {
		if p == b || (inside && within(p, b)) {
			return true
		}
	}
	return false
}

// contains reports whether p is a strict ancestor of one of the bases.
func contains(p string, bases []string) bool {
	for _, b := range variants(bases) {
		if p != b && within(b, p) {
			return true
		}
	}
	return false
}
