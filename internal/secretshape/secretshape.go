// Package secretshape finds credential-shaped content in file bytes. It is a
// leaf package (stdlib only). It deliberately does not flag bare AWS access key
// ids (AKIA/ASIA): those are identifiers, not secrets, and CloudTrail data is
// full of them.
package secretshape

import (
	"bytes"
	"regexp"
	"strings"
	"sync"
)

// tokenAlts mirror the fixed provider prefixes of tokenShaped in
// internal/bundle/mcpcreds.go, minus AKIA/ASIA and the JWT alternative.
var tokenAlts = []string{
	`sk-[A-Za-z0-9_-]{16,}`,
	`sk_(?:live|test)_[A-Za-z0-9]{16,}`,
	`gh[pousr]_[A-Za-z0-9]{30,}`,
	`github_pat_[A-Za-z0-9_]{20,}`,
	`xox[abprs]-[A-Za-z0-9-]{10,}`,
	`glpat-[A-Za-z0-9_-]{20,}`,
	`AIza[0-9A-Za-z_-]{35}`,
	`fc-[0-9a-f]{24,}`,
	`tvly-[A-Za-z0-9-]{16,}`,
}

// keyNames are the credential key names of the secret-assignment rule, in
// lower case. The rule matches them in any case.
var keyNames = []string{
	"aws_secret_access_key", "aws_session_token", "secretaccesskey", "sessiontoken", "refresh_token",
	"access_token", "client_secret", "password", "api_key", "apikey",
}

// assignTail is what follows a key name in the secret-assignment rule: : or =
// and a non-empty value, on the same line. The key may be quoted (JSON).
const assignTail = `["']?[ \t]*[:=][ \t]*["']?[^\s"']`

var (
	pemPrivateKey = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)

	// jwt is a three-segment base64url token whose first two segments are JSON.
	jwt = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]+`)

	// tokenAt is the token rule at the very start of its input. The rule is a
	// token not right after a token character (so a prefix inside a longer
	// word, risk-free-tier-..., is not read as one): hasToken checks that.
	tokenAt = regexp.MustCompile(`^(?:` + strings.Join(tokenAlts, "|") + `)`)

	// assignTailAt is assignTail at the very start of its input.
	assignTailAt = regexp.MustCompile(`^` + assignTail)

	// assignAt is the whole secret-assignment rule at the very start of its
	// input, with the rule's case folding.
	assignAt = regexp.MustCompile(`(?i)^(?:` + quoteAll(keyNames) + `)` + assignTail)
)

// tokenStems are the literal starts of the token alternatives: every token
// begins with one of them.
var tokenStems = func() literals {
	var ss []string
	for _, a := range tokenAlts {
		p, _ := regexp.MustCompile(a).LiteralPrefix()
		if p == "" {
			panic("secretshape: token alternative with no literal start: " + a)
		}
		ss = append(ss, p)
	}
	return newLiterals(ss)
}()

// keyLits are keyNames, searched for in a lower-case copy.
var keyLits = newLiterals(keyNames)

// tokenWindow is how many bytes from a token's start decide whether it is one:
// every alternative matches iff its shortest form does, and the longest of
// those (AIza and 35 characters) is 39 bytes.
const tokenWindow = 64

// foldOdd are the two runes that case folding (the rule's (?i)) takes for an
// ASCII letter: U+017F for s and U+212A for k. A key name spelt with one is
// not in the lower-case copy, so the rule is tried near each of them.
var foldOdd = [][]byte{[]byte("ſ"), []byte("K")}

// maxNameBytes bounds how far before an odd rune a key name can start: every
// letter of it as a 3-byte rune.
var maxNameBytes = func() int {
	n := 0
	for _, k := range keyNames {
		n = max(n, 3*len(k))
	}
	return n
}()

// lowBufs holds the lower-case copies the key-name search reads.
var lowBufs = sync.Pool{New: func() any { return new([]byte) }}

// lowerASCII maps A-Z to a-z and every other byte to itself.
var lowerASCII = func() (t [256]byte) {
	for i := range t {
		t[i] = byte(i)
		if 'A' <= i && i <= 'Z' {
			t[i] += 'a' - 'A'
		}
	}
	return t
}()

// Find returns the first rule that matches b: pem-private-key, jwt, token or
// secret-assignment.
//
// The token and secret-assignment rules have no fixed start a regexp scan can
// skip to, so a scan with them costs about 30 ms per 64 KiB: too slow for a
// folder of data files. Find looks for their literal parts first (a token's
// prefix, a key name in an ASCII lower-case copy) and runs the rule only where
// one is, anchored there. It says what the rules say on every input
// (TestFindSameAsRules).
func Find(b []byte) (rule string, ok bool) {
	switch {
	case pemPrivateKey.Match(b):
		return "pem-private-key", true
	case jwt.Match(b):
		return "jwt", true
	case hasToken(b):
		return "token", true
	case hasAssignment(b):
		return "secret-assignment", true
	}
	return "", false
}

// hasToken reports whether a token starts in b at a byte that is not right
// after a token character. A byte at or above 0x80 before it belongs to a rune
// that is not one: the stems are ASCII, so their start is a rune boundary.
func hasToken(b []byte) bool {
	return tokenStems.each(b, func(_ []byte, p int) bool {
		return (p == 0 || !tokenChar(b[p-1])) && tokenAt.Match(b[p:min(len(b), p+tokenWindow)])
	})
}

func tokenChar(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '-'
}

// hasAssignment reports whether a key name with a value follows in b. A name
// spelt in ASCII is found in a lower-case copy, at the same offset as in b;
// one spelt with an odd rune (foldOdd) is tried with the whole rule from every
// byte up to maxNameBytes before that rune. The start of a match is an ASCII
// byte or a UTF-8 lead byte, a rune boundary either way; from any other byte
// the rule fails at once.
func hasAssignment(b []byte) bool {
	lp := lowBufs.Get().(*[]byte)
	defer lowBufs.Put(lp)
	if cap(*lp) < len(b) {
		*lp = make([]byte, len(b))
	}
	low := (*lp)[:len(b)]
	for i, c := range b {
		low[i] = lowerASCII[c]
	}
	if keyLits.each(low, func(name []byte, p int) bool { return assignTailAt.Match(b[p+len(name):]) }) {
		return true
	}
	for _, odd := range foldOdd {
		for off := 0; ; {
			i := bytes.Index(b[off:], odd)
			if i < 0 {
				break
			}
			q := off + i
			for s := max(0, q-maxNameBytes); s <= q; s++ {
				if assignAt.Match(b[s:]) {
					return true
				}
			}
			off = q + 1
		}
	}
	return false
}

// literals are fixed strings, found by their rarest byte. bytes.Index scans for
// a needle's first byte and checks each place it is: for a first byte as common
// as the a of access_token, that is thousands of checks per 64 KiB of text.
// Strings with the same rarest byte share one scan for it.
type literals []struct {
	c    byte // the byte scanned for
	lits []literal
}

type literal struct {
	s  []byte
	at int // the offset of c in s
}

// rarity is the letters from the rarest to the most common in English text,
// each case; any other byte ranks after them. It only decides speed.
const rarity = "ZQXJzqxjKkVvBbPpYyGgFfWwMmUuCcLlDdRrHhSsNnIiOoAaTtEe"

func newLiterals(ss []string) literals {
	rank := func(c byte) int {
		if i := strings.IndexByte(rarity, c); i >= 0 {
			return i
		}
		return len(rarity)
	}
	var out literals
	for _, s := range ss {
		at := 0
		for i := range len(s) {
			if rank(s[i]) < rank(s[at]) {
				at = i
			}
		}
		l := literal{s: []byte(s), at: at}
		found := false
		for i := range out {
			if out[i].c == s[at] {
				out[i].lits, found = append(out[i].lits, l), true
			}
		}
		if !found {
			out = append(out, struct {
				c    byte
				lits []literal
			}{s[at], []literal{l}})
		}
	}
	return out
}

// each calls f with every literal found in b and its offset, until f says true.
func (ls literals) each(b []byte, f func(s []byte, p int) bool) bool {
	for _, g := range ls {
		for off := 0; ; {
			i := bytes.IndexByte(b[off:], g.c)
			if i < 0 {
				break
			}
			q := off + i
			off = q + 1
			for _, l := range g.lits {
				if p := q - l.at; p >= 0 && p+len(l.s) <= len(b) && bytes.Equal(b[p:p+len(l.s)], l.s) && f(l.s, p) {
					return true
				}
			}
		}
	}
	return false
}

func quoteAll(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = regexp.QuoteMeta(s)
	}
	return strings.Join(q, "|")
}
