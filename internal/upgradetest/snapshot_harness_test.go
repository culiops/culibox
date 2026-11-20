//go:build upgrade

package upgradetest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// keepData bounds the file bytes a snapshot keeps for rule checks that compare
// content (sessions.json, settings.json): the hash covers everything else.
const keepData = 1 << 20

// Entry is one path in a snapshot. Data holds the bytes of a regular file up
// to keepData; it is not compared (SHA256 is).
type Entry struct {
	Type   string // "file", "dir", "symlink", "socket" or "other"
	Mode   fs.FileMode
	Size   int64
	SHA256 string
	Target string
	Data   []byte
}

// same reports whether two entries are equal in every compared field.
func (e Entry) same(o Entry) bool {
	return e.Type == o.Type && e.Mode == o.Mode && e.Size == o.Size && e.SHA256 == o.SHA256 && e.Target == o.Target
}

func (e Entry) String() string {
	switch e.Type {
	case "file":
		return fmt.Sprintf("file %v %d %.12s", e.Mode, e.Size, e.SHA256)
	case "symlink":
		return "symlink -> " + e.Target
	default:
		return fmt.Sprintf("%s %v", e.Type, e.Mode)
	}
}

// Snapshot maps a slash path relative to the snapshot base to its entry.
type Snapshot map[string]Entry

// Take records every path under base without following symlinks. Directory
// and file modes are kept; mtimes are not (a rewrite with the same bytes is
// not a change).
func Take(base string) (Snapshot, error) {
	s := Snapshot{}
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == base {
			return nil
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		e := Entry{Mode: fi.Mode().Perm()}
		switch m := fi.Mode(); {
		case m.IsRegular():
			e.Type, e.Size = "file", fi.Size()
			if e.SHA256, e.Data, err = hashFile(p, fi.Size()); err != nil {
				return err
			}
		case m.IsDir():
			e.Type = "dir"
		case m&fs.ModeSymlink != 0:
			e.Type = "symlink"
			if e.Target, err = os.Readlink(p); err != nil {
				return err
			}
		case m&fs.ModeSocket != 0:
			e.Type = "socket"
		default:
			e.Type = "other"
		}
		s[filepath.ToSlash(rel)] = e
		return nil
	})
	return s, err
}

func hashFile(p string, size int64) (string, []byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	h := sha256.New()
	var data []byte
	if size <= keepData {
		if data, err = io.ReadAll(f); err != nil {
			return "", nil, err
		}
		h.Write(data)
	} else if _, err = io.Copy(h, f); err != nil {
		return "", nil, err
	}
	return hex.EncodeToString(h.Sum(nil)), data, nil
}

// Rule allows a difference at the paths Pattern matches. Pattern is matched
// segment by segment with path.Match; a final "**" matches one or more
// remaining segments. A nil Check allows any change of an entry that exists on
// both sides; a non-nil Check decides. AllowNew and AllowVanish allow the path
// to appear or disappear.
type Rule struct {
	Pattern     string
	Why         string
	Check       func(before, after *Entry) error
	AllowNew    bool
	CheckNew    func(after *Entry) error // when set, a new entry the rule allows must also pass this
	AllowVanish bool
}

// DiffOptions are the allowlist and the path mapping for one comparison.
// MapBefore turns a before-path into the path it must have after (the S2 root
// move); nil is the identity. Ignore lists patterns the caller checks itself.
type DiffOptions struct {
	Rules     []Rule
	MapBefore func(rel string) string
	Ignore    []string
}

// Diff returns one line per difference no rule allows, "<path>: <what>",
// sorted by path.
func Diff(before, after Snapshot, o DiffOptions) []string {
	mapped := make(Snapshot, len(before))
	for rel, e := range before {
		if o.MapBefore != nil {
			rel = o.MapBefore(rel)
		}
		mapped[rel] = e
	}
	var out []string
	for rel, b := range mapped {
		if ignored(rel, o.Ignore) {
			continue
		}
		a, ok := after[rel]
		r := ruleFor(rel, o.Rules)
		switch {
		case !ok:
			if r == nil || !r.AllowVanish {
				out = append(out, fmt.Sprintf("%s: vanished (was %s)", rel, b))
			}
		case b.same(a):
		case r == nil:
			out = append(out, fmt.Sprintf("%s: changed (%s -> %s)", rel, b, a))
		case r.Check != nil:
			bb, aa := b, a
			if err := r.Check(&bb, &aa); err != nil {
				out = append(out, fmt.Sprintf("%s: %v", rel, err))
			}
		}
	}
	for rel, a := range after {
		if _, ok := mapped[rel]; ok || ignored(rel, o.Ignore) {
			continue
		}
		r := ruleFor(rel, o.Rules)
		switch {
		case r == nil || !r.AllowNew:
			out = append(out, fmt.Sprintf("%s: new %s", rel, a))
		case r.CheckNew != nil:
			aa := a
			if err := r.CheckNew(&aa); err != nil {
				out = append(out, fmt.Sprintf("%s: new: %v", rel, err))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		pi, _, _ := strings.Cut(out[i], ": ")
		pj, _, _ := strings.Cut(out[j], ": ")
		return pi < pj
	})
	return out
}

func ignored(rel string, pats []string) bool {
	for _, p := range pats {
		if match(p, rel) {
			return true
		}
	}
	return false
}

func ruleFor(rel string, rules []Rule) *Rule {
	for i := range rules {
		if match(rules[i].Pattern, rel) {
			return &rules[i]
		}
	}
	return nil
}

// match reports whether rel matches pattern, segment by segment.
func match(pattern, rel string) bool {
	ps, rs := strings.Split(pattern, "/"), strings.Split(rel, "/")
	for i, p := range ps {
		if p == "**" && i == len(ps)-1 {
			return len(rs) > i
		}
		if i >= len(rs) {
			return false
		}
		if ok, err := path.Match(p, rs[i]); err != nil || !ok {
			return false
		}
	}
	return len(rs) == len(ps)
}
