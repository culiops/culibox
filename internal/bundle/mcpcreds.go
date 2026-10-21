package bundle

import (
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// credentialReason reports a server whose headers, args or url carry a literal
// credential. Copying it would put that credential into every boxed bundle,
// where the agent can read it: the same leak literal env values are refused for.
// Reasons name the field or flag, never the value, because they are shown in
// the UI and recorded in the lock file.
func credentialReason(srv mcpServer) (string, bool) {
	if h, ok := srv["headers"].(map[string]any); ok && len(h) > 0 {
		names := make([]string, 0, len(h))
		for n := range h {
			names = append(names, n)
		}
		sort.Strings(names)
		return "declares literal headers (" + strings.Join(names, ", ") + "); materializing them would write a secret into the bundle", true
	}
	if h, _ := srv["headersHelper"].(string); h != "" {
		return "builds its headers with a host command (headersHelper); that command is a host path and may embed a secret", true
	}
	if u, _ := srv["url"].(string); u != "" {
		if what, ok := urlCredential(u); ok {
			return "url carries " + what + "; materializing it would write a secret into the bundle", true
		}
	}
	if c, _ := srv["command"].(string); tokenShaped.MatchString(c) {
		return "command contains a value shaped like an API token", true
	}
	args, _ := srv["args"].([]any)
	for i, a := range args {
		s, ok := a.(string)
		if !ok {
			continue
		}
		var next string
		if i+1 < len(args) {
			next, _ = args[i+1].(string)
		}
		if what, ok := argCredential(s, next); ok {
			return "arg " + strconv.Itoa(i) + " carries " + what + "; materializing it would write a secret into the bundle", true
		}
	}
	return "", false
}

// tokenShaped matches the fixed prefixes of common provider credentials. It
// catches a bare token passed positionally, where no flag name gives it away.
var tokenShaped = regexp.MustCompile(`(sk-[A-Za-z0-9_-]{16,}|sk_(live|test)_[A-Za-z0-9]{16,}|gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|glpat-[A-Za-z0-9_-]{20,}|(AKIA|ASIA)[0-9A-Z]{16}|AIza[0-9A-Za-z_-]{35}|eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,})`)

// secretWords are name segments that mark a credential. Matched per segment,
// so --max-tokens or --no-auth are not mistaken for one.
var secretWords = map[string]bool{
	"token": true, "secret": true, "password": true, "passwd": true, "pat": true,
	"apikey": true, "bearer": true, "authorization": true, "cookie": true,
	"credential": true, "credentials": true,
}

// secretName reports whether a flag, variable, header or query name names a
// credential: a secret word as one segment, or api/access/private + key.
func secretName(name string) bool {
	segs := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	})
	for i, s := range segs {
		if secretWords[s] {
			return true
		}
		if s == "key" && i > 0 && (segs[i-1] == "api" || segs[i-1] == "access" || segs[i-1] == "private") {
			return true
		}
	}
	return false
}

var (
	flagArg   = regexp.MustCompile(`^--?([A-Za-z][A-Za-z0-9_.-]*)(=(.*))?$`)
	assignArg = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)=(.+)$`)
	headerArg = regexp.MustCompile(`^\s*([A-Za-z][A-Za-z0-9-]*)\s*:\s*\S`)
	bearerArg = regexp.MustCompile(`(?i)^\s*(bearer|basic)\s+\S`)
)

// argCredential checks one arg; next is the arg after it, the value of a
// "--flag value" pair.
func argCredential(arg, next string) (string, bool) {
	if tokenShaped.MatchString(arg) {
		return "a value shaped like an API token", true
	}
	if bearerArg.MatchString(arg) {
		return "a bearer credential", true
	}
	m := flagArg.FindStringSubmatch(arg)
	if m != nil && m[3] != "" {
		// --header=Authorization: Bearer … hides the credential in the value.
		if what, ok := argCredential(m[3], ""); ok {
			return what, true
		}
	}
	if m != nil && secretName(m[1]) {
		hasValue := m[2] != "" && m[3] != ""
		if m[2] == "" && next != "" && !strings.HasPrefix(next, "-") {
			hasValue = true
		}
		if hasValue {
			return "a value for " + strings.SplitN(arg, "=", 2)[0], true
		}
	}
	if m := assignArg.FindStringSubmatch(arg); m != nil && secretName(m[1]) {
		return "an assignment to " + m[1], true
	}
	if m := headerArg.FindStringSubmatch(arg); m != nil && !strings.Contains(arg, "://") && secretName(m[1]) {
		return "a " + m[1] + " header", true
	}
	if strings.Contains(arg, "://") {
		if what, ok := urlCredential(arg); ok {
			return "a url with " + what, true
		}
	}
	return "", false
}

// urlCredential reports any userinfo (a token is often passed as the bare
// username) or a credential-named query
// parameter (a bare "key" or "sig" counts there: a query has no other context).
func urlCredential(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	if u.User != nil {
		return "userinfo (user:password@)", true
	}
	names := make([]string, 0)
	for n := range u.Query() {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		l := strings.ToLower(n)
		if secretName(n) || l == "key" || l == "sig" || l == "signature" {
			return "a credential in its query (" + n + ")", true
		}
	}
	return "", false
}
