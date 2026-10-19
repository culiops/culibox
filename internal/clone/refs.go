package clone

import (
	"fmt"
	"strings"
)

// maxBranchLen bounds a branch name; the name is spliced into a ref path, a
// docker argv and a refspec.
const maxBranchLen = 200

// ValidBranch accepts the branch names agent-box will fetch or use as a base.
// It is deliberately stricter than `git check-ref-format`, so that no extra
// host git exec is needed to vet a name: 1-200 bytes of [A-Za-z0-9._/-]
// starting with a letter or digit (so never an option), none of ".." "//" "/."
// "@{", no trailing "/", "." or ".lock", and no ".lock" component.
func ValidBranch(b string) error {
	bad := func(why string) error { return fmt.Errorf("branch %q: %s", sanitize(b), why) }
	if b == "" {
		return bad("is empty")
	}
	if len(b) > maxBranchLen {
		return bad(fmt.Sprintf("is longer than %d bytes", maxBranchLen))
	}
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case i > 0 && (c == '.' || c == '_' || c == '/' || c == '-'):
		default:
			return bad("may only contain letters, digits and . _ / -, and must start with a letter or digit")
		}
	}
	for _, s := range []string{"..", "//", "/.", "@{"} {
		if strings.Contains(b, s) {
			return bad(fmt.Sprintf("must not contain %q", s))
		}
	}
	if strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".") {
		return bad("must not end with \"/\" or \".\"")
	}
	for _, part := range strings.Split(b, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return bad("has a component that starts with \".\" or ends with \".lock\"")
		}
	}
	return nil
}

// sanitize makes box-written text safe to print on the host terminal: C0
// controls, DEL, C1 controls, the U+2028/U+2029 line separators and the bidi
// controls (U+202A-202E, U+2066-2069) become "?", and at most 200 runes are kept. Every string a box can influence (commit
// subjects, branch names, bundle-container stderr) goes through it.
func sanitize(s string) string { return sanitizeN(s, 200) }

func sanitizeN(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n == max {
			break
		}
		switch {
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f, r == 0x2028, r == 0x2029,
			r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			r = '?'
		}
		b.WriteRune(r) // invalid UTF-8 became U+FFFD in the range loop
		n++
	}
	return b.String()
}
