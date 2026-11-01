package gitview

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

// maxPath is the longest path ParseStatus accepts, in bytes.
const maxPath = 4096

// Entry is one changed path: P relative to the checkout top (a trailing "/"
// marks a collapsed untracked directory), X one of M A U D C.
type Entry struct {
	P string `json:"p"`
	X string `json:"x"`
}

// ParseStatus parses `git status --porcelain=v2 -z --branch` output. The input
// is untrusted: records that are malformed, or whose path is empty, absolute,
// over 4096 bytes, not valid UTF-8 or has a ".." segment, are dropped. At most max entries are
// returned; one more sets tooMany. noHead is true when the repo has no commits.
func ParseStatus(b []byte, max int) (entries []Entry, noHead bool, tooMany bool) {
	recs := bytes.Split(b, []byte{0})
	add := func(p, x string) bool {
		if !safePath(p) {
			return true
		}
		if len(entries) >= max {
			tooMany = true
			return false
		}
		entries = append(entries, Entry{P: p, X: x})
		return true
	}
	for i := 0; i < len(recs); i++ {
		r := string(recs[i])
		switch {
		case strings.HasPrefix(r, "# "):
			if r == "# branch.oid (initial)" {
				noHead = true
			}
		case strings.HasPrefix(r, "1 "):
			// 1 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <path>: the path is the rest.
			if f := strings.SplitN(r, " ", 9); len(f) == 9 && len(f[1]) == 2 {
				if !add(f[8], code(f[1])) {
					return
				}
			}
		case strings.HasPrefix(r, "2 "):
			// A rename or copy: --no-renames should prevent it. Keep its path as
			// a modification and consume the origPath record that follows, but
			// only when this record is well formed: a malformed one is not known
			// to be followed by an origPath, so the next record is parsed as itself.
			if f := strings.SplitN(r, " ", 10); len(f) == 10 && len(f[1]) == 2 {
				i++
				if !add(f[9], "M") {
					return
				}
			}
		case strings.HasPrefix(r, "u "):
			// u <XY> <sub> <m1> <m2> <m3> <mW> <h1> <h2> <h3> <path>
			if f := strings.SplitN(r, " ", 11); len(f) == 11 {
				if !add(f[10], "C") {
					return
				}
			}
		case strings.HasPrefix(r, "? "):
			if !add(r[2:], "U") {
				return
			}
		}
		// "! " (ignored) and anything else is not shown.
	}
	return
}

// code maps an XY pair of an ordinary record to a badge code.
func code(xy string) string {
	x, y := xy[0], xy[1]
	switch {
	case x == 'U' || y == 'U', xy == "AA", xy == "DD":
		return "C"
	case x == 'D' || y == 'D':
		return "D"
	case x == 'A':
		return "A"
	}
	return "M"
}

func safePath(p string) bool {
	if p == "" || len(p) > maxPath || strings.HasPrefix(p, "/") || strings.ContainsRune(p, 0) || !utf8.ValidString(p) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

// DiffKind classifies diff output: "unchanged" (empty), "binary" (git's
// "Binary files ... differ" line and no hunk), else "text".
func DiffKind(b []byte) string {
	if len(b) == 0 {
		return "unchanged"
	}
	binary, hunk := false, false
	for _, line := range bytes.Split(b, []byte{'\n'}) {
		if bytes.HasPrefix(line, []byte("Binary files ")) {
			binary = true
		}
		if bytes.HasPrefix(line, []byte("@@")) {
			hunk = true
		}
	}
	if binary && !hunk {
		return "binary"
	}
	return "text"
}
