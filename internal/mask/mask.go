// Package mask is a DISPLAY mask: it hides credential-shaped strings from what
// a screen share or an OS notification shows. It is not a guarantee. The agent
// already saw the text, Claude Code's transcript keeps it, and the PTY stream
// is untouched. Never apply it to anything that is executed or approved
// (Peek.AskFull is hash-locked to what runs).
package mask

import (
	"regexp"
	"strings"
)

type rule struct {
	re   *regexp.Regexp
	repl string
	// has is a cheap pre-check: the rule runs only when it returns true, so
	// text with no credential-shaped prefix is returned without allocating.
	has func(string) bool
}

func anyOf(needles ...string) func(string) bool {
	return func(s string) bool {
		for _, n := range needles {
			if strings.Contains(s, n) {
				return true
			}
		}
		return false
	}
}

// hasFold reports whether s contains needle (lowercase ASCII), ignoring ASCII
// case, without allocating.
func hasFold(s, needle string) bool {
	n := len(needle)
	for i := 0; i+n <= len(s); i++ {
		if strings.EqualFold(s[i:i+n], needle) {
			return true
		}
	}
	return false
}

// rules run in a fixed order. The PEM block goes first so a key inside it is
// not masked twice.
var rules = []rule{
	{
		re:   regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|\z)`),
		repl: "[masked:private-key]",
		has:  anyOf("-----BEGIN "),
	},
	{
		re:   regexp.MustCompile(`(?:IQoJb3JpZ2lu|FwoGZXIvYXdz)[A-Za-z0-9+/=]{20,}`),
		repl: "[masked:aws-session-token]",
		has:  anyOf("IQoJb3JpZ2lu", "FwoGZXIvYXdz"),
	},
	{
		re:   regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
		repl: "[masked:aws-key-id]",
		has:  anyOf("AKIA", "ASIA"),
	},
	{
		re:   regexp.MustCompile(`(?i)(aws_secret_access_key|secretaccesskey)("?\s*[:=]\s*"?)[A-Za-z0-9/+=]{40}\b`),
		repl: "${1}${2}[masked:aws-secret]",
		has: func(s string) bool {
			return hasFold(s, "aws_secret_access_key") || hasFold(s, "secretaccesskey")
		},
	},
	{
		re:   regexp.MustCompile(`\b(?:gh[po]_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{22,})\b`),
		repl: "[masked:github-token]",
		has:  anyOf("ghp_", "gho_", "github_pat_"),
	},
	{
		re:   regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`),
		repl: "[masked:anthropic-key]",
		has:  anyOf("sk-ant-"),
	},
}

// Display replaces credential-shaped substrings of s with [masked:<kind>].
// It is pure, safe on any input, idempotent, and returns s unchanged (without
// allocating) when nothing matches.
func Display(s string) string {
	for _, r := range rules {
		if r.has(s) {
			s = r.re.ReplaceAllString(s, r.repl)
		}
	}
	return s
}
