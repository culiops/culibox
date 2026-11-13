// Package vmshares holds what the macOS box VM may share from the host: the
// share type, path expansion and the rules that refuse folders the VM must
// never see. It is pure (no colima or docker calls) so Linux tests cover it.
package vmshares

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/culiops/culibox/internal/box"
)

// Mode is how a share is mounted into the VM.
type Mode string

const (
	RW Mode = "rw"
	RO Mode = "ro"
)

// Share is one folder the user chose to share. Path is absolute and cleaned
// but not symlink-resolved: it is the display spelling.
type Share struct {
	Path string `json:"path"`
	Mode Mode   `json:"mode"`
}

// Env is what Check compares against. Home and DataRoot are symlink-resolved;
// Fold is true on darwin (APFS compares case-insensitively).
type Env struct {
	Home, DataRoot string
	Fold           bool
	// Protected are CuliBox's own host directories (config, profiles, bundle
	// source): a share may never equal, contain or sit inside one. Nil means
	// none known (a caller that cannot build the list).
	Protected []box.ProtectedDir
}

// Expand turns what the user typed into a cleaned absolute path: "~", "~/x" or
// an absolute path. A relative path is refused.
func Expand(typed, home string) (string, error) {
	t := strings.TrimSpace(typed)
	switch {
	case t == "~":
		return filepath.Clean(home), nil
	case strings.HasPrefix(t, "~/"):
		return filepath.Clean(filepath.Join(home, t[2:])), nil
	case filepath.IsAbs(t):
		return filepath.Clean(t), nil
	}
	return "", &Refused{Path: t, Kind: KindRelative, Reason: reasonRelative}
}

// Display shows home as "~" and paths under it as "~/x".
func Display(p, home string) string {
	if home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+"/") {
		return "~/" + p[len(home)+1:]
	}
	return p
}

// Within reports whether child is parent or lies under it, component-wise
// (/a/bc is not within /a/b). fold compares case-insensitively.
func Within(child, parent string, fold bool) bool {
	c, p := parts(child), parts(parent)
	if len(c) < len(p) {
		return false
	}
	for i := range p {
		if !eq(c[i], p[i], fold) {
			return false
		}
	}
	return true
}

func parts(p string) []string {
	p = filepath.Clean(p)
	var out []string
	for _, s := range strings.Split(p, string(filepath.Separator)) {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func eq(a, b string, fold bool) bool {
	if fold {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Resolve follows symlinks in the longest existing prefix of p and keeps the
// rest as written (the path may not exist yet). A relative or unresolvable
// path is returned unchanged.
func Resolve(p string) string {
	if !filepath.IsAbs(p) {
		return p
	}
	p = filepath.Clean(p)
	rest := ""
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// YAMLSafe refuses what a double-quoted YAML scalar would need escaping for or
// could be read differently: a double quote, a backslash, invalid UTF-8, any
// control character (C0, DEL, C1 including U+0085 NEL) and U+2028/U+2029, which
// YAML 1.1 readers treat as line breaks. Refused, not escaped: the same paths
// also reach docker and colima argv.
//
// The paths are not only YAML scalars. colima runs every mount location through
// os.ExpandEnv and lima renders it as a Go text/template, so `$`, `{`, `}` and
// the backtick are refused too: with them a path that passed every check would
// reach the VM as a different host folder (an unset variable drops a component,
// a template can print any path).
func YAMLSafe(p string) error {
	bad := !utf8.ValidString(p)
	for _, r := range p {
		if r == '"' || r == '\\' || unicode.IsControl(r) || r == '\u2028' || r == '\u2029' ||
			r == '$' || r == '{' || r == '}' || r == '`' {
			bad = true
		}
	}
	if bad {
		return fmt.Errorf("path %q contains a character CuliBox will not write into the VM config", p)
	}
	return nil
}
