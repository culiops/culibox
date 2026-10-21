package bundle

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// credentialReason reports a server whose headers, args, url or any other key
// carry a literal credential. Copying it would put that credential into every
// boxed bundle, where the agent can read it: the same leak literal env values
// are refused for. Reasons name the field or flag, never the value, because
// they are recorded in the lock file the box mounts.
//
// This is a heuristic over shapes, not a proof. A secret with no name, no known
// prefix and no random-looking shape (a short opaque url path, say) still gets
// through; keep credentials out of the MCP config rather than relying on it.
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
	if c, _ := srv["command"].(string); c != "" {
		if what, ok := wordsCredential(c); ok {
			return "command carries " + what + "; materializing it would write a secret into the bundle", true
		}
	}
	args, _ := srv["args"].([]any)
	for i, a := range args {
		s := argString(a)
		var next string
		if i+1 < len(args) {
			next = argString(args[i+1])
		}
		if what, ok := argCredential(s, next); ok {
			return "arg " + strconv.Itoa(i) + " carries " + what + "; materializing it would write a secret into the bundle", true
		}
		// sh -c "npx pkg --token X" hides flags inside one arg.
		if strings.ContainsAny(s, " \t") {
			if what, ok := wordsCredential(s); ok {
				return "arg " + strconv.Itoa(i) + " carries " + what + "; materializing it would write a secret into the bundle", true
			}
		}
	}
	// Keys this filter has no rule for (oauth.clientSecret, a misspelled
	// Headers) are copied as-is, so walk everything else too.
	for _, k := range sortedKeys(srv) {
		switch k {
		case "url", "command", "args", "env", "headers", "headersHelper":
			continue
		}
		if what, ok := deepCredential(k, srv[k]); ok {
			return "key " + k + " carries " + what + "; materializing it would write a secret into the bundle", true
		}
	}
	return "", false
}

func argString(a any) string {
	switch t := a.(type) {
	case string:
		return t
	case float64, bool:
		return fmt.Sprint(t)
	}
	return ""
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// deepCredential walks a value of an unhandled key: a credential-named key with
// a non-empty value, or any token-shaped string, anywhere inside it.
func deepCredential(key string, v any) (string, bool) {
	switch t := v.(type) {
	case string:
		if t != "" && secretName(key) {
			return "a value for " + key, true
		}
		if tokenShaped.MatchString(t) {
			return "a value shaped like an API token", true
		}
	case []any:
		for _, e := range t {
			if what, ok := deepCredential(key, e); ok {
				return what, true
			}
		}
	case map[string]any:
		if strings.EqualFold(key, "headers") && len(t) > 0 {
			return "literal headers", true
		}
		for _, k := range sortedKeys(t) {
			if what, ok := deepCredential(k, t[k]); ok {
				return what, true
			}
		}
	}
	return "", false
}

// tokenShaped matches the fixed prefixes of common provider credentials. It
// catches a bare token passed positionally, where no flag name gives it away.
var tokenShaped = regexp.MustCompile(`(sk-[A-Za-z0-9_-]{16,}|sk_(live|test)_[A-Za-z0-9]{16,}|gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|glpat-[A-Za-z0-9_-]{20,}|(AKIA|ASIA)[0-9A-Z]{16}|AIza[0-9A-Za-z_-]{35}|fc-[0-9a-f]{24,}|tvly-[A-Za-z0-9-]{16,}|eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,})`)

// secretWords are name segments that mark a credential. Matched per segment,
// so --max-tokens is not mistaken for one.
var secretWords = map[string]bool{
	"token": true, "secret": true, "password": true, "passwd": true, "pat": true,
	"apikey": true, "bearer": true, "authorization": true, "cookie": true,
	"credential": true, "credentials": true,
}

// pointerWords after a secret word make the name about the secret, not the
// secret itself: --token-limit, --password-file, --cookie-jar.
var pointerWords = map[string]bool{
	"limit": true, "limits": true, "type": true, "budget": true, "count": true,
	"file": true, "path": true, "dir": true, "name": true, "jar": true, "id": true,
}

// nameSegments splits on - _ . and camelCase boundaries, lowercased.
func nameSegments(name string) []string {
	var b strings.Builder
	prev := rune(0)
	for _, r := range name {
		if unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsDigit(prev)) {
			b.WriteByte('-')
		}
		b.WriteRune(unicode.ToLower(r))
		prev = r
	}
	return strings.FieldsFunc(b.String(), func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	})
}

// secretName reports whether a flag, variable, header or key name names a
// credential: a secret word as one segment, or api/access/private + key, unless
// a pointer word follows it.
func secretName(name string) bool {
	segs := nameSegments(name)
	for i, s := range segs {
		hit := secretWords[s] ||
			(s == "key" && i > 0 && (segs[i-1] == "api" || segs[i-1] == "access" || segs[i-1] == "private"))
		if hit && !(i+1 < len(segs) && pointerWords[segs[i+1]]) {
			return true
		}
	}
	return false
}

// secretFlag also counts a bare --key or --auth: as a whole flag name they
// have no other plausible meaning.
func secretFlag(name string) bool {
	l := strings.ToLower(name)
	return secretName(name) || l == "key" || l == "auth"
}

// pointerValue is a value that names where a secret lives rather than being
// one: a path. Pointing a flag at a file is the safe practice. A number is not
// exempt: --password 123456 is a secret.
func pointerValue(v string) bool {
	return strings.HasPrefix(v, "/") || strings.HasPrefix(v, "~") || strings.HasPrefix(v, "./") || strings.HasPrefix(v, "../")
}

var (
	flagArg   = regexp.MustCompile(`^--?([A-Za-z][A-Za-z0-9_.-]*)(=(.*))?$`)
	assignArg = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*)=(.+)$`)
	headerArg = regexp.MustCompile(`^\s*([A-Za-z][A-Za-z0-9-]*)\s*:\s*\S`)
	schemeArg = regexp.MustCompile(`(?i)^\s*(bearer|basic|token)\s+\S`)
	urlArg    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)
)

// argCredential checks one arg; next is the arg after it, the value of a
// "--flag value" pair.
func argCredential(arg, next string) (string, bool) {
	if tokenShaped.MatchString(arg) {
		return "a value shaped like an API token", true
	}
	if schemeArg.MatchString(arg) {
		return "an authorization scheme value", true
	}
	m := flagArg.FindStringSubmatch(arg)
	if m != nil && m[3] != "" {
		// --header=Authorization: Bearer … hides the credential in the value.
		if what, ok := argCredential(m[3], ""); ok {
			return what, true
		}
	}
	if m != nil && secretFlag(m[1]) {
		value := m[3]
		if m[2] == "" && !strings.HasPrefix(next, "-") {
			value = next
		}
		if value != "" && !pointerValue(value) {
			return "a value for " + strings.SplitN(arg, "=", 2)[0], true
		}
	}
	if m == nil {
		if a := assignArg.FindStringSubmatch(arg); a != nil && secretName(a[1]) && !pointerValue(a[2]) {
			return "an assignment to " + a[1], true
		}
	}
	if h := headerArg.FindStringSubmatch(arg); h != nil && !strings.Contains(arg, "://") &&
		(secretName(h[1]) || strings.EqualFold(h[1], "x-key")) {
		return "the " + h[1] + " header", true
	}
	// Only url-shaped pieces are parsed (a list like a,b of origins is split),
	// so the parse-failure refusal is not tripped by prose that mentions ://.
	for _, piece := range strings.FieldsFunc(arg, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		if urlArg.MatchString(piece) {
			if what, ok := urlCredential(piece); ok {
				return "a url with " + what, true
			}
		}
	}
	return "", false
}

// wordsCredential runs argCredential over the words of a shell-ish string,
// each with the word after it as its value.
func wordsCredential(s string) (string, bool) {
	words := strings.Fields(s)
	for i, w := range words {
		if w == "export" {
			continue
		}
		var next string
		if i+1 < len(words) {
			next = words[i+1]
		}
		if what, ok := argCredential(strings.Trim(w, `"';`), strings.Trim(next, `"';`)); ok {
			return what, true
		}
	}
	return "", false
}

// randomSegment matches a long url path segment mixing letters and digits: the
// shape of a secret embedded in the path (mcp.zapier.com/api/mcp/s/<secret>/).
var randomSegment = regexp.MustCompile(`^[A-Za-z0-9_-]{24,}$`)
var uuidSegment = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// urlCredential reports a url that carries a credential: userinfo of any kind
// (a token is often the bare username), a credential-named query or fragment
// parameter, a token-shaped string anywhere, or a random-looking path segment.
// A url that does not parse is refused: a check that can't see is not a pass.
func urlCredential(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if tokenShaped.MatchString(raw) {
		return "a value shaped like an API token", true
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "a url that could not be parsed", true
	}
	if u.User != nil {
		return "userinfo (user:password@)", true
	}
	for _, part := range []string{u.RawQuery, u.Fragment} {
		if n, ok := paramCredential(part); ok {
			return "a credential in its query (" + n + ")", true
		}
	}
	for _, seg := range strings.Split(u.EscapedPath(), "/") {
		if randomSegment.MatchString(seg) && !uuidSegment.MatchString(seg) &&
			strings.ContainsAny(seg, "0123456789") && strings.IndexFunc(seg, unicode.IsLetter) >= 0 {
			return "a random-looking path segment", true
		}
	}
	return "", false
}

// queryWords are matched as substrings of a query parameter name: a query name
// has no other context, so tavilyApiKey or access_token both count.
var queryWords = []string{"token", "secret", "key", "passw", "auth", "cred", "sig", "session"}

// paramCredential splits a raw query (or fragment) itself, on & and ;, so a pair
// url.Query would silently skip (bad escape, ; separator) is still seen.
func paramCredential(raw string) (string, bool) {
	for _, pair := range strings.FieldsFunc(raw, func(r rune) bool { return r == '&' || r == ';' }) {
		name := strings.SplitN(pair, "=", 2)[0]
		if un, err := url.QueryUnescape(name); err == nil {
			name = un
		}
		l := strings.ToLower(name)
		for _, w := range queryWords {
			if strings.Contains(l, w) {
				return name, true
			}
		}
	}
	return "", false
}
