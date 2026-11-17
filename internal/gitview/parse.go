package gitview

import (
	"bytes"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxPath is the longest path ParseStatus accepts, in bytes.
const maxPath = 4096

// maxBranch is the longest branch name ParseStatus keeps, in bytes: the most a
// filesystem allows one path component, which is what a loose ref is.
const maxBranch = 255

// oidRE is a full object id, SHA-1 or SHA-256, as git prints it.
var oidRE = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

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
// branch is the checked-out branch ("" when HEAD is detached) and head the
// commit it is at ("" with no commits); each is "" too when git's text is not
// a well-formed name or id. Git prints both before any entry, so tooMany still
// leaves them set.
func ParseStatus(b []byte, max int) (entries []Entry, noHead bool, tooMany bool, branch, head string) {
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
			switch {
			case r == "# branch.oid (initial)":
				noHead = true
			case strings.HasPrefix(r, "# branch.oid "):
				if o := r[len("# branch.oid "):]; oidRE.MatchString(o) {
					head = o
				}
			case strings.HasPrefix(r, "# branch.head "):
				branch = safeBranch(r[len("# branch.head "):])
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

// safeBranch is the branch name git printed, or "" for a detached HEAD or a
// name that is too long, not UTF-8 or holds a control character: the name is
// agent-shaped text that a client shows.
func safeBranch(b string) string {
	if b == "(detached)" || len(b) > maxBranch || !utf8.ValidString(b) {
		return ""
	}
	for _, c := range b {
		if unicode.IsControl(c) {
			return ""
		}
	}
	return b
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

// pickBase reads the output of BaseArgs' for-each-ref and returns the base
// branch: origin/HEAD's symref target when git listed origin/HEAD (it lists
// only a ref that resolves), else the first of baseCandidates git listed.
// The output is untrusted: a record not made of exactly git's two NUL
// terminators is skipped, and a symref target that is not a ValidBaseRef is
// never returned, so what comes back is "" or safe to put in an argv.
func pickBase(b []byte) string {
	refs := map[string]string{} // listed ref -> its symref target, "" when it is not a symref
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Split(line, "\x00"); len(f) == 3 && f[2] == "" {
			refs[f[0]] = f[1]
		}
	}
	if sym := refs[originHead]; ValidBaseRef(sym) {
		return sym
	}
	for _, c := range baseCandidates {
		if _, ok := refs[c]; ok {
			return c
		}
	}
	return ""
}

// baseName is the short form of a base ref shown to the user: "main" for a
// local branch, "origin/main" for a remote-tracking one.
func baseName(ref string) string {
	if s, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return s
	}
	return strings.TrimPrefix(ref, "refs/remotes/")
}
