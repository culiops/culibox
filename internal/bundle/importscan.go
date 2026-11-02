package bundle

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/culiops/culibox/internal/box"
)

// importNameGlobs are lowercase base-name globs that mark a file as a secret
// beyond box.SecretFilePatterns: ~/.claude holds secrets.zsh, gh_key and cf_key.
var importNameGlobs = []string{"*secret*", "*token*", "*password*", "*_key"}

var (
	// boundedToken is tokenShaped anchored on its left, so a prefix inside a
	// longer word (risk-free-tier-limits-apply) is not read as a token.
	boundedToken = regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])` + tokenShaped.String())
	pemHeader    = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)
	shellAssign  = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=(\S+)`)
)

// importWarnings lists what is worth telling the user before a bundled import
// goes into a box: a secret-looking file name, and the first line whose content
// looks like a credential. It warns, never blocks, and never echoes a value.
func importWarnings(name string, content []byte) []string {
	var out []string
	if importNameLooksSecret(name) {
		out = append(out, name+" has a name that looks like a secret; the box can read and send it")
	}
	for i, line := range strings.Split(string(content), "\n") {
		if what := credentialLine(line); what != "" {
			out = append(out, fmt.Sprintf("%s line %d looks like a credential (%s); the box can read and send it", name, i+1, what))
			break
		}
	}
	return out
}

func importNameLooksSecret(name string) bool {
	if box.LooksSecret(name) {
		return true
	}
	l := strings.ToLower(name)
	for _, g := range importNameGlobs {
		if ok, _ := filepath.Match(g, l); ok {
			return true
		}
	}
	return false
}

// credentialLine says what a line looks like, or "" when it looks like nothing.
func credentialLine(line string) string {
	switch {
	case boundedToken.MatchString(line):
		return "a value shaped like an API token"
	case pemHeader.MatchString(line):
		return "a private key"
	}
	if m := shellAssign.FindStringSubmatch(line); m != nil && secretName(m[1]) {
		v := strings.Trim(m[2], `"'`)
		if v != "" && !strings.HasPrefix(v, "$") && !pointerValue(v) {
			return "an assignment to " + m[1]
		}
	}
	return ""
}
