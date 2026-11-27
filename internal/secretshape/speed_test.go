package secretshape

import (
	"math/rand"
	"regexp"
	"regexp/syntax"
	"strings"
	"testing"
	"time"
)

// The rules as they were first written, one regexp each. Find must say what
// these say, on any input: the speed-ups only decide where to look.
var (
	refPEM    = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)
	refJWT    = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]+`)
	refToken  = regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])(?:sk-[A-Za-z0-9_-]{16,}|sk_(?:live|test)_[A-Za-z0-9]{16,}|gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|glpat-[A-Za-z0-9_-]{20,}|AIza[0-9A-Za-z_-]{35}|fc-[0-9a-f]{24,}|tvly-[A-Za-z0-9-]{16,})`)
	refAssign = regexp.MustCompile(`(?i)(?:aws_secret_access_key|aws_session_token|secretaccesskey|sessiontoken|refresh_token|access_token|client_secret|password|api_?key)["']?[ \t]*[:=][ \t]*["']?[^\s"']`)
)

func refFind(b []byte) (string, bool) {
	switch {
	case refPEM.Match(b):
		return "pem-private-key", true
	case refJWT.Match(b):
		return "jwt", true
	case refToken.Match(b):
		return "token", true
	case refAssign.Match(b):
		return "secret-assignment", true
	}
	return "", false
}

// randHead is a random mix of the pieces the rules are made of (prefixes, key
// names in any case, separators, quotes, runs of token characters) and of
// bytes that are not: UTF-8 runes ("é", the long s and the Kelvin sign, which
// (?i) folds with s and k), invalid bytes, newlines.
func randHead(r *rand.Rand) []byte {
	pieces := []string{
		"sk-", "sk_live_", "sk_test_", "ghp_", "gho_", "ghx_", "github_pat_", "xoxb-", "xoxz-", "glpat-",
		"AIza", "fc-", "tvly-", "eyJ", ".", "eyJhbGciOiJIUzI1NiJ9.", "eyJzdWIiOiIxMjM0NTY3ODkwIn0.", "-----BEGIN RSA PRIVATE KEY-----",
		"password", "PassWord", "paſſword", "api_key", "APIKEY", "api_Key", "apikey",
		"aws_secret_access_key", "AWS_SESSION_TOKEN", "SecretAccessKey", "sessiontoken", "Refresh_Token",
		"access_token", "client_secret", "aws_ſecret_acceſſ_Key",
		":", "=", " ", "\t", "\"", "'", "\n", "x", "_", "-", "é", "\xc5", "\xbf", "\xe2\x84", "\xff",
	}
	alnum := "abcdefABCDEF0123456789_-"
	var sb strings.Builder
	for n := r.Intn(12); n >= 0; n-- {
		if r.Intn(3) == 0 {
			for k := r.Intn(45); k >= 0; k-- {
				sb.WriteByte(alnum[r.Intn(len(alnum))])
			}
			continue
		}
		sb.WriteString(pieces[r.Intn(len(pieces))])
	}
	return []byte(sb.String())
}

func TestFindSameAsRules(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	hits := map[string]int{}
	for i := range 60000 {
		b := randHead(r)
		want, wok := refFind(b)
		got, gok := Find(b)
		if got != want || gok != wok {
			t.Fatalf("case %d %q: Find = (%q,%v), the rules say (%q,%v)", i, b, got, gok, want, wok)
		}
		hits[want]++
	}
	// The mix must reach every rule and plenty of misses, or it tests nothing.
	for _, rule := range []string{"", "jwt", "token", "secret-assignment", "pem-private-key"} {
		if hits[rule] < 100 {
			t.Errorf("only %d cases for %q: %v", hits[rule], rule, hits)
		}
	}
}

func TestFindFoldedKeyNames(t *testing.T) {
	// (?i) folds s with U+017F and k with U+212A: the rule catches these, so Find must.
	for _, in := range []string{"paſſword: x", "api_Key=1", "xéaws_ſecret_access_key = abc"} {
		if got, ok := Find([]byte(in)); !ok || got != "secret-assignment" {
			t.Errorf("%q: got (%q,%v), want secret-assignment", in, got, ok)
		}
	}
	// A token right after a non-ASCII rune is still on its own; after a letter it is not.
	tok := "ghp_" + strings.Repeat("aB3", 12)
	if _, ok := Find([]byte("é" + tok)); !ok {
		t.Error("token after é: no hit")
	}
	if _, ok := Find([]byte("\xff" + tok)); !ok {
		t.Error("token after an invalid byte: no hit")
	}
	if _, ok := Find([]byte("x" + tok)); ok {
		t.Error("token inside a word: hit")
	}
}

func cloudTrailHead(n int) []byte {
	var sb strings.Builder
	for sb.Len() < n {
		sb.WriteString(`{"eventVersion":"1.08","userIdentity":{"type":"AssumedRole","accessKeyId":"ASIAXXXXXXXXXXXXXXXX","arn":"arn:aws:sts::123456789012:assumed-role/x/y","sessionContext":{"attributes":{"mfaAuthenticated":"false"}}},"eventName":"DescribeInstances","sourceIPAddress":"10.0.0.1","userAgent":"aws-cli/2.15","requestParameters":{"instancesSet":{"items":[{"instanceId":"i-0123456789abcdef0"}]}}}` + "\n")
	}
	return []byte(sb.String()[:n])
}

// The screen reads the first 64 KiB of every file of a folder within 10 s. A
// folder of Parquet data or CloudTrail logs is hundreds of files: Find must
// cost well under a millisecond per head, not the 30 ms a scan with the rules'
// regexps takes.
func TestFindSpeed(t *testing.T) {
	if testing.Short() {
		t.Skip("speed test")
	}
	r := rand.New(rand.NewSource(1))
	heads := make([][]byte, 0, 1000)
	for i := range 1000 {
		b := make([]byte, 64<<10)
		if i%2 == 0 {
			r.Read(b)
		} else {
			b = cloudTrailHead(64 << 10)
		}
		heads = append(heads, b)
	}
	start := time.Now()
	for _, b := range heads {
		if rule, ok := Find(b); ok {
			t.Fatalf("hit %q on data with no credential", rule)
		}
	}
	took := time.Since(start)
	limit := 2 * time.Second * slowFactor
	t.Logf("1000 heads of 64 KiB: %v (limit %v)", took, limit)
	if took > limit {
		t.Fatalf("1000 heads of 64 KiB took %v, want under %v", took, limit)
	}
}

// minLen is the fewest runes re matches.
func minLen(re *syntax.Regexp) int {
	switch re.Op {
	case syntax.OpLiteral:
		return len(re.Rune)
	case syntax.OpCharClass, syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		return 1
	case syntax.OpCapture, syntax.OpPlus:
		return minLen(re.Sub[0])
	case syntax.OpRepeat:
		return re.Min * minLen(re.Sub[0])
	case syntax.OpConcat:
		n := 0
		for _, s := range re.Sub {
			n += minLen(s)
		}
		return n
	case syntax.OpAlternate:
		n := -1
		for _, s := range re.Sub {
			if m := minLen(s); n < 0 || m < n {
				n = m
			}
		}
		return n
	}
	return 0
}

// A token is decided by its first tokenWindow bytes: every alternative is a
// run of ASCII classes, so it matches iff its shortest form does.
func TestTokenWindowCoversEveryAlternative(t *testing.T) {
	for _, a := range tokenAlts {
		re, err := syntax.Parse(a, syntax.Perl)
		if err != nil {
			t.Fatal(err)
		}
		if n := minLen(re); n > tokenWindow || n < 10 {
			t.Errorf("%s: shortest match %d bytes, window %d", a, n, tokenWindow)
		}
	}
}

// A head made of the runes case folding takes for s and k (or of those and
// ASCII letters) is tried from each byte at most once, not up to 64 times.
func TestFindSpeedFoldRunes(t *testing.T) {
	if testing.Short() {
		t.Skip("speed test")
	}
	fill := func(unit string) []byte {
		return []byte(strings.Repeat(unit, (64<<10)/len(unit)))
	}
	heads := map[string][]byte{"ſ": fill("ſ"), "K": fill("K"), "aſ": fill("aſ"), "pa_K": fill("pa_K")}
	for name, b := range heads {
		if want, wok := refFind(b); wok {
			t.Fatalf("%s: the rules hit (%q): pick another head", name, want)
		}
		start := time.Now()
		for range 25 {
			if rule, ok := Find(b); ok {
				t.Fatalf("%s: hit %q", name, rule)
			}
		}
		took := time.Since(start)
		// 20 ms a head, loose for a slow CI runner or a busy test binary
		// (74 ms for 25 heads was measured against a 4 ms bound), and still
		// about 50x under the 0.96 s a head this test exists to catch (the
		// first version of this search; the rules' regexps took about 25 ms).
		limit := 500 * time.Millisecond * slowFactor
		t.Logf("25 heads of 64 KiB of %s: %v (limit %v)", name, took, limit)
		if took > limit {
			t.Fatalf("25 heads of 64 KiB of %s took %v, want under %v", name, took, limit)
		}
	}
}
