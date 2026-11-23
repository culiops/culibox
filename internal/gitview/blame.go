package gitview

import (
	"bytes"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// BlameCommit is one commit as the editor's blame shows it. Every string has
// passed cleanText: git prints what the repository holds, and an agent (or
// whoever wrote the history) chose it. Ref is a pull or merge request number
// the summary names, "#123" or "!123", or "".
type BlameCommit struct {
	SHA      string `json:"sha"`
	Author   string `json:"author"`
	Email    string `json:"email"`
	Time     int64  `json:"time"`
	Summary  string `json:"summary"`
	Ref      string `json:"ref,omitempty"`
	Boundary bool   `json:"boundary,omitempty"`
}

// The bounds of a blame field, in runes.
const (
	maxAuthorRunes  = 128
	maxEmailRunes   = 254
	maxSummaryRunes = 256
	// maxBlameLines is more lines than a 2 MiB file of empty lines holds once
	// the output cap (maxBlameBytes) is applied; anything past it is not git.
	maxBlameLines = 2_000_000
)

var errBadBlame = errors.New("unexpected blame output")

// ZeroSHA reports whether sha is git's id for "not committed yet".
func ZeroSHA(sha string) bool {
	return sha != "" && strings.Trim(sha, "0") == ""
}

// cleanText makes s safe to show as one line of plain text: valid UTF-8, with
// no control characters and no bidirectional controls (which would let a name
// or summary reorder the text around it on screen), cut to max runes with an
// ellipsis.
func cleanText(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r):
			return -1
		case r == 0x061C, r == 0x200E, r == 0x200F, r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max-1]) + "…"
	}
	return s
}

var (
	refParenRE = regexp.MustCompile(`\(([#!])([1-9][0-9]{0,8})\)\s*$`)
	refMergeRE = regexp.MustCompile(`^Merge pull request #([1-9][0-9]{0,8})(\s|$)`)
)

// refOf is the pull or merge request a summary names: "Title (#12)" (GitHub's
// squash), "Merge pull request #12 from …" (GitHub's merge), "Title (!12)"
// (GitLab), as "#12" or "!12"; "" otherwise. Which host gets a link for which
// form is the client's choice (GitLab's "#12" is an issue).
func refOf(summary string) string {
	if m := refParenRE.FindStringSubmatch(summary); m != nil {
		return m[1] + m[2]
	}
	if m := refMergeRE.FindStringSubmatch(summary); m != nil {
		return "#" + m[1]
	}
	return ""
}

// validSHA reports whether s is a full lowercase object id (SHA-1 or SHA-256).
func validSHA(s string) bool { return oidRE.MatchString(s) }

func positive(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n > 0 && n <= maxBlameLines
}

// ParseBlame reads git blame --porcelain into the commits it names, in the
// order first seen, and the file's lines as runs of [commit index, line count]
// in line order. Output that does not follow the format exactly (final lines
// not 1, 2, 3…, a group whose header count and content lines differ, a commit
// without its metadata, an id that is not one) is refused whole: the caller
// shows no blame rather than a wrong one.
func ParseBlame(b []byte) ([]BlameCommit, [][2]int, error) {
	commits := []BlameCommit{}
	lines := [][2]int{}
	index := map[string]int{}
	seenTime := map[int]bool{}
	cur := -1        // the commit of the current line
	pending := false // a header was read, its content line not yet
	fresh := false   // the current group is the first to name its commit
	left := 0        // lines left in the current group, counting the pending one
	next := 1        // the final line number the next header must carry
	for len(b) > 0 {
		var line []byte
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			line, b = b[:i], b[i+1:]
		} else {
			line, b = b, nil
		}
		if len(line) > 0 && line[0] == '\t' {
			if !pending {
				return nil, nil, errBadBlame
			}
			pending = false
			left--
			if n := len(lines); n > 0 && lines[n-1][0] == cur {
				lines[n-1][1]++
			} else {
				lines = append(lines, [2]int{cur, 1})
			}
			continue
		}
		if pending {
			// Metadata of the commit just named, before its content line.
			key, val, _ := strings.Cut(string(line), " ")
			if validSHA(key) {
				// A header where metadata or a content line must be: a name or summary holding a newline.
				return nil, nil, errBadBlame
			}
			if !fresh {
				continue // git prints a commit's metadata once, in the group that first names it
			}
			c := &commits[cur]
			switch key {
			case "author":
				c.Author = cleanText(val, maxAuthorRunes)
			case "author-mail":
				c.Email = cleanText(strings.TrimSuffix(strings.TrimPrefix(val, "<"), ">"), maxEmailRunes)
			case "author-time":
				t, err := strconv.ParseInt(val, 10, 64)
				if err != nil {
					return nil, nil, errBadBlame
				}
				c.Time = t
				seenTime[cur] = true
			case "summary":
				c.Summary = cleanText(val, maxSummaryRunes)
				c.Ref = refOf(c.Summary)
			case "boundary":
				c.Boundary = true
			}
			continue
		}
		// A header: "<sha> <orig> <final> <count>" to open a group, "<sha> <orig> <final>" inside one.
		f := strings.Fields(string(line))
		if len(f) != 3 && len(f) != 4 || !validSHA(f[0]) {
			return nil, nil, errBadBlame
		}
		if _, ok := positive(f[1]); !ok {
			return nil, nil, errBadBlame
		}
		if final, ok := positive(f[2]); !ok || final != next {
			return nil, nil, errBadBlame
		}
		if len(f) == 4 {
			n, ok := positive(f[3])
			if !ok || left != 0 {
				return nil, nil, errBadBlame
			}
			left = n
		} else if left == 0 {
			return nil, nil, errBadBlame
		}
		i, seen := index[f[0]]
		if !seen {
			i = len(commits)
			index[f[0]] = i
			commits = append(commits, BlameCommit{SHA: f[0]})
		} else if len(f) == 3 && i != cur {
			return nil, nil, errBadBlame
		}
		fresh = !seen
		cur = i
		pending = true
		next++
		if next > maxBlameLines+1 {
			return nil, nil, errBadBlame
		}
	}
	if pending || left != 0 {
		return nil, nil, errBadBlame
	}
	for i := range commits {
		if !seenTime[i] {
			return nil, nil, errBadBlame
		}
	}
	return commits, lines, nil
}

// ParseLogLast reads LogLastArgs' record: five NUL-terminated fields and git's
// newline. Empty output (no commit touched the path) is nil, nil; anything else
// that is not exactly that record is refused.
func ParseLogLast(b []byte) (*BlameCommit, error) {
	b = bytes.TrimSuffix(b, []byte("\n"))
	if len(b) == 0 {
		return nil, nil
	}
	f := strings.Split(string(b), "\x00")
	if len(f) != 6 || f[5] != "" || !validSHA(f[0]) {
		return nil, errBadBlame
	}
	t, err := strconv.ParseInt(f[3], 10, 64)
	if err != nil {
		return nil, errBadBlame
	}
	c := &BlameCommit{SHA: f[0], Author: cleanText(f[1], maxAuthorRunes), Email: cleanText(f[2], maxEmailRunes), Time: t,
		Summary: cleanText(f[4], maxSummaryRunes)}
	c.Ref = refOf(c.Summary)
	return c, nil
}
