package handback

import "strings"

// maxPathRunes bounds a stored path, link target or detail. It is longer than
// clone's 200-rune sanitize on purpose: that cut would eat the extension, and
// the extension is what an extension-mismatch flag is about.
const maxPathRunes = 1024

// SanitizePath makes box-written text safe to store and show: C0 controls,
// DEL, C1 controls, U+2028/U+2029 and the bidi controls (U+202A-202E,
// U+2066-2069) become "?". The same rule set as clone's sanitizeN, repeated
// here because clone imports this package. A string over 1,024 runes keeps
// its tail behind a leading "…", so the extension survives.
func SanitizePath(s string) string {
	rs := make([]rune, 0, len(s))
	for _, r := range s { // invalid UTF-8 becomes U+FFFD here
		switch {
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f, r == 0x2028, r == 0x2029,
			r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			r = '?'
		}
		rs = append(rs, r)
	}
	if len(rs) <= maxPathRunes {
		return string(rs)
	}
	var b strings.Builder
	b.WriteRune('…')
	b.WriteString(string(rs[len(rs)-(maxPathRunes-1):]))
	return b.String()
}
