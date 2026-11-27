// Package secretshape finds credential-shaped content in file bytes. It is a
// leaf package (stdlib only). It deliberately does not flag bare AWS access key
// ids (AKIA/ASIA): those are identifiers, not secrets, and CloudTrail data is
// full of them.
package secretshape

import "regexp"

var (
	pemPrivateKey = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)

	// jwt is a three-segment base64url token whose first two segments are JSON.
	jwt = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]+`)

	// token mirrors the fixed provider prefixes of tokenShaped in
	// internal/bundle/mcpcreds.go, minus AKIA/ASIA and the JWT alternative. It is
	// anchored on its left so a prefix inside a longer word (risk-free-tier-...)
	// is not read as a token.
	token = regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])(?:sk-[A-Za-z0-9_-]{16,}|sk_(?:live|test)_[A-Za-z0-9]{16,}|gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|glpat-[A-Za-z0-9_-]{20,}|AIza[0-9A-Za-z_-]{35}|fc-[0-9a-f]{24,}|tvly-[A-Za-z0-9-]{16,})`)

	// secretAssignment is a credential key name followed by : or = and a
	// non-empty value, on the same line. The key may be quoted (JSON).
	secretAssignment = regexp.MustCompile(`(?i)(?:aws_secret_access_key|aws_session_token|secretaccesskey|sessiontoken|refresh_token|access_token|client_secret|password|api_?key)["']?[ \t]*[:=][ \t]*["']?[^\s"']`)
)

// Find returns the first rule that matches b: pem-private-key, jwt, token or
// secret-assignment.
func Find(b []byte) (rule string, ok bool) {
	switch {
	case pemPrivateKey.Match(b):
		return "pem-private-key", true
	case jwt.Match(b):
		return "jwt", true
	case token.Match(b):
		return "token", true
	case secretAssignment.Match(b):
		return "secret-assignment", true
	}
	return "", false
}
