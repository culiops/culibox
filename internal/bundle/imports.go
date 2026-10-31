package bundle

import (
	"regexp"
	"strings"
)

// importRe matches an @-import: "@" at line start or after whitespace, then a
// token running to the next unescaped whitespace (`\ ` is an escaped space).
var importRe = regexp.MustCompile(`(^|\s)@((?:\\ |\S)+)`)

// parseImports returns the @-import spellings in a CLAUDE.md, in document
// order, without the "@" and with "\ " unescaped. It skips fenced code blocks,
// inline code spans, HTML comments (which may span lines) and tokens that start
// with a quote, the way Claude Code does. It only extracts spellings; resolving
// them to files is the caller's job.
func parseImports(md []byte) []string {
	out := []string{}
	var (
		fenceChar byte // 0 when outside a fence
		fenceLen  int
		inComment bool
	)
	for _, line := range strings.Split(string(md), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if !inComment {
			if c, n, rest, ok := fenceMarker(line); ok {
				switch {
				case fenceChar == 0:
					fenceChar, fenceLen = c, n
					continue
				case c == fenceChar && n >= fenceLen && strings.TrimSpace(rest) == "":
					fenceChar = 0
					continue
				}
			}
			if fenceChar != 0 {
				continue
			}
		}
		var clean string
		clean, inComment = stripCodeAndComments(line, inComment)
		for _, m := range importRe.FindAllStringSubmatch(clean, -1) {
			tok := strings.ReplaceAll(m[2], `\ `, " ")
			if tok[0] == '"' || tok[0] == '\'' {
				continue
			}
			out = append(out, tok)
		}
	}
	return out
}

// fenceMarker reports whether line opens or closes a fenced block: up to three
// spaces of indent, then three or more backticks or tildes. rest is the text
// after the marker run.
func fenceMarker(line string) (char byte, n int, rest string, ok bool) {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 || len(t) < 3 || (t[0] != '`' && t[0] != '~') {
		return 0, 0, "", false
	}
	c := t[0]
	for n < len(t) && t[n] == c {
		n++
	}
	if n < 3 {
		return 0, 0, "", false
	}
	return c, n, t[n:], true
}

// stripCodeAndComments blanks inline code spans and HTML comments in one line,
// scanning left to right. inComment carries an open comment in from the
// previous line; the returned bool carries one out. An unmatched backtick run
// is literal text.
func stripCodeAndComments(line string, inComment bool) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(line); {
		if inComment {
			j := strings.Index(line[i:], "-->")
			if j < 0 {
				return b.String(), true
			}
			i += j + len("-->")
			inComment = false
			b.WriteByte(' ')
			continue
		}
		switch {
		case strings.HasPrefix(line[i:], "<!--"):
			inComment = true
			i += len("<!--")
		case line[i] == '`':
			n := 0
			for i+n < len(line) && line[i+n] == '`' {
				n++
			}
			if end := closingRun(line, i+n, n); end >= 0 {
				i = end
				b.WriteByte(' ')
			} else {
				b.WriteString(line[i : i+n])
				i += n
			}
		default:
			b.WriteByte(line[i])
			i++
		}
	}
	return b.String(), inComment
}

// closingRun finds a backtick run of exactly n starting at or after from, and
// returns the index just past it, or -1.
func closingRun(line string, from, n int) int {
	for i := from; i < len(line); {
		if line[i] != '`' {
			i++
			continue
		}
		m := 0
		for i+m < len(line) && line[i+m] == '`' {
			m++
		}
		if m == n {
			return i + m
		}
		i += m
	}
	return -1
}
