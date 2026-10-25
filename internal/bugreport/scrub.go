package bugreport

import (
	"regexp"
	"strings"
)

// The scrub is a backstop for secrets that reach a log line or a doctor detail
// in a shape nobody planned for. The preview plus a human press is the real
// gate. Plain regexp, compiled once; RE2 has no backtracking blow-ups.
var (
	// Well-known token shapes, redacted whole. The leading group is a boundary
	// that keeps its character: "\b" would miss a token glued to "_".
	tokenShapes = regexp.MustCompile(
		`(^|[^A-Za-z0-9])(?:gh[oprsu]_[A-Za-z0-9]{20,}` +
			`|github_pat_[A-Za-z0-9_]{20,}` +
			`|(?:AKIA|ASIA)[A-Z0-9]{16}` +
			`|xox[a-z]-[A-Za-z0-9-]+)`)

	// scheme://user:pass@host: the userinfo is a credential.
	urlUserinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^\s/@]+@`)

	// An AWS secret access key is 40 base64 characters with no prefix to key
	// on, so it is only matched on a line that mentions aws or a secret.
	awsHint = regexp.MustCompile(`(?i)aws|secret`)
	awsKey  = regexp.MustCompile(`[A-Za-z0-9/+=]{40,}`)

	// "Authorization: <scheme> <value>": keep the header and scheme, drop the
	// value (and the whole value when there is no scheme).
	authHeader = regexp.MustCompile(
		`(?i)(\bauthorization\s*[:=]\s*(?:(?:basic|bearer|token|digest|negotiate)\s+)?)\S+`)

	bearer = regexp.MustCompile(`(?i)(\bbearer\s+)[A-Za-z0-9._~+/=-]+`)

	// key=value / key: value / "key":"value" where the key names a secret.
	// The key may carry a prefix or suffix (GH_TOKEN, client_secret_file).
	secretKV = regexp.MustCompile(
		`(?i)([A-Za-z0-9_.-]*(?:token|secret|password|passwd|api[-_]?key)[A-Za-z0-9_.-]*["']?\s*[:=]\s*)` +
			`("[^"]*"|'[^']*'|\S+)`)
)

const redacted = "[redacted]"

// Scrub replaces secret-looking values with "[redacted]", keeping the
// surrounding text so the line stays readable.
func Scrub(s string) string {
	s = authHeader.ReplaceAllString(s, "${1}"+redacted)
	s = bearer.ReplaceAllString(s, "${1}"+redacted)
	s = tokenShapes.ReplaceAllString(s, "${1}"+redacted)
	s = urlUserinfo.ReplaceAllString(s, "${1}"+redacted+"@")
	s = scrubAWSSecret(s)
	s = secretKV.ReplaceAllString(s, "${1}"+redacted)
	return s
}

func scrubAWSSecret(s string) string {
	if !awsHint.MatchString(s) {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if awsHint.MatchString(l) {
			lines[i] = awsKey.ReplaceAllString(l, redacted)
		}
	}
	return strings.Join(lines, "\n")
}
