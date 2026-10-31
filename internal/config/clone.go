package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// maxCloneShares bounds clone_share: each entry is one more bind mount and one
// more path the box can read.
const maxCloneShares = 8

// Clone is a boxed profile's clone-mode spec: the host repo it was cloned from,
// the clone's directory (the profile's default_cwd, which is also the box
// workspace), and the host paths shared read-only into the clone.
type Clone struct {
	Of     string   // host repo the clone is made from (clone_of)
	Dir    string   // the clone itself (default_cwd)
	Shares []string // relative paths under Of, mounted read-only at the same path under Dir
	// NoAutoReturn is the clone_no_auto_return switch. It is deliberately left
	// out of Hash: flipping it must not rename the box.
	NoAutoReturn bool
}

// ParseShare validates one clone_share entry and returns its relative path.
// Every share is read-only (D12), so there is no mode suffix: ":rw" is refused
// by name so an old habit fails loudly instead of being silently downgraded.
func ParseShare(s string) (string, error) {
	if strings.HasSuffix(s, ":rw") {
		return "", fmt.Errorf("clone_share %q: shares are read-only (D12); remove \":rw\"", s)
	}
	if strings.Contains(s, ":") {
		return "", fmt.Errorf("clone_share %q: \":\" is not allowed", s)
	}
	if hasControl(s) {
		return "", fmt.Errorf("clone_share %q: control characters are not allowed", s)
	}
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("clone_share %q: must be valid UTF-8", s)
	}
	if s == "" || s == "." {
		return "", fmt.Errorf("clone_share %q: need a path relative to clone_of", s)
	}
	if filepath.IsAbs(s) {
		return "", fmt.Errorf("clone_share %q: must be relative to clone_of", s)
	}
	if filepath.Clean(s) != s {
		return "", fmt.Errorf("clone_share %q: must be a clean path (no \".\", \"..\", doubled or trailing slashes)", s)
	}
	parts := strings.Split(s, "/")
	if slices.Contains(parts, "..") {
		return "", fmt.Errorf("clone_share %q: \"..\" is not allowed", s)
	}
	if parts[0] == ".git" {
		return "", fmt.Errorf("clone_share %q: .git cannot be shared", s)
	}
	return s, nil
}

// Clone returns the profile's clone spec, or nil, nil when it has no clone_of.
// An invalid clone configuration is an error, never a partial spec.
func (a AgentConfig) Clone() (*Clone, error) {
	if a.CloneOf == "" {
		return nil, nil
	}
	if err := validateClone(a); err != nil {
		return nil, err
	}
	return &Clone{Of: a.CloneOf, Dir: a.DefaultCwd, Shares: slices.Clone(a.CloneShare), NoAutoReturn: a.CloneNoAutoReturn}, nil
}

// Hash identifies the clone spec for the box name: two profiles that clone
// different repos, or clone into different dirs, or share different paths, must
// never reuse one box. The share order is irrelevant. 16 hex chars.
func (c Clone) Hash() string {
	shares := slices.Clone(c.Shares)
	slices.Sort(shares)
	h := sha256.New()
	h.Write([]byte(c.Of + "\x00" + c.Dir + "\x00"))
	for _, s := range shares {
		h.Write([]byte(s + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// ShareWarnings returns one line per share that looks like a secret or config
// file. Read-only still means readable, and a box has internet egress, so a
// shared secret is an exfiltrated secret. This warns and does not refuse: it is
// the user's call.
func ShareWarnings(shares []string) []string {
	var out []string
	for _, s := range shares {
		base := filepath.Base(s)
		for _, pat := range []string{".env*", "*.pem", "*.key", "config*.json"} {
			if ok, _ := filepath.Match(pat, base); ok {
				out = append(out, fmt.Sprintf("clone_share %q looks like a secret or config file; the box will be able to read it", s))
				break
			}
		}
	}
	return out
}

// validateClone is the pure, filesystem-free check shared by Config.Validate
// and ValidateProfile. Checks that need the disk (symlinks, MountUnsafe,
// ProtectedOverlap, the effective tier) belong to the spawn path.
func validateClone(a AgentConfig) error {
	if a.CloneOf == "" {
		if len(a.CloneShare) > 0 {
			return errors.New("clone_share requires clone_of")
		}
		if a.CloneNoAutoReturn {
			return errors.New("clone_no_auto_return requires clone_of")
		}
		return nil
	}
	if err := absClean("clone_of", a.CloneOf); err != nil {
		return err
	}
	if a.DefaultCwd == "" {
		return errors.New("clone_of requires default_cwd (the clone's directory)")
	}
	if err := absClean("default_cwd", a.DefaultCwd); err != nil {
		return err
	}
	if pathOverlap(a.CloneOf, a.DefaultCwd) {
		return fmt.Errorf("clone_of %q and default_cwd %q overlap; the clone must live outside the repo it copies", a.CloneOf, a.DefaultCwd)
	}
	if a.Runtime == RuntimeHost {
		return errors.New("clone mode runs only in a box; remove runtime \"host\" or clone_of")
	}
	if len(a.CloneShare) > maxCloneShares {
		return fmt.Errorf("clone_share has %d entries; at most %d", len(a.CloneShare), maxCloneShares)
	}
	rels := make([]string, 0, len(a.CloneShare))
	for _, s := range a.CloneShare {
		rel, err := ParseShare(s)
		if err != nil {
			return err
		}
		for _, prev := range rels {
			if prev == rel {
				return fmt.Errorf("clone_share %q: duplicate", rel)
			}
			if pathOverlap(prev, rel) {
				return fmt.Errorf("clone_share %q and %q nest; share only the outer path", prev, rel)
			}
		}
		rels = append(rels, rel)
	}
	return nil
}

// hasControl reports whether s holds a C0 control character or DEL. Such a
// byte would split a line of the clone-dirs record, make Hash's NUL separator
// ambiguous, or smuggle terminal escapes into messages.
func hasControl(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func absClean(field, p string) error {
	if hasControl(p) {
		return fmt.Errorf("%s %q: control characters are not allowed", field, p)
	}
	if !filepath.IsAbs(p) {
		return fmt.Errorf("%s %q must be an absolute path", field, p)
	}
	if filepath.Clean(p) != p {
		return fmt.Errorf("%s %q must be a clean path (no \"..\", doubled or trailing slashes)", field, p)
	}
	return nil
}

// pathOverlap reports whether a and b are equal or one is inside the other,
// component-wise ("/a/b" and "/a/bc" do not overlap). Both must be clean.
func pathOverlap(a, b string) bool {
	return within(a, b) || within(b, a)
}

func within(parent, p string) bool {
	if parent == p {
		return true
	}
	if !strings.HasSuffix(parent, "/") {
		parent += "/"
	}
	return strings.HasPrefix(p, parent)
}
