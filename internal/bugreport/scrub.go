package bugreport

import "regexp"

// The scrub is a backstop for secrets that reach a log line or a doctor detail
// in a shape nobody planned for. The preview plus a human press is the real
// gate. Plain regexp, compiled once; RE2 has no backtracking blow-ups.
var (
	// Well-known token shapes, redacted whole.
	tokenShapes = regexp.MustCompile(
		`\bgh[pos]_[A-Za-z0-9]{20,}` +
			`|\bgithub_pat_[A-Za-z0-9_]{20,}` +
			`|\b(?:AKIA|ASIA)[A-Z0-9]{16}\b` +
			`|\bxox[a-z]-[A-Za-z0-9-]+`)

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
	s = tokenShapes.ReplaceAllString(s, redacted)
	s = secretKV.ReplaceAllString(s, "${1}"+redacted)
	return s
}
