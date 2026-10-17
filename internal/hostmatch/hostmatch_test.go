package hostmatch

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	ok := map[string]string{
		"API.Anthropic.com":  "api.anthropic.com",
		"api.anthropic.com.": "api.anthropic.com",
		"a-b.c0.io":          "a-b.c0.io",
	}
	for in, want := range ok {
		if got, good := Normalize(in); !good || got != want {
			t.Errorf("Normalize(%q) = %q,%v want %q", in, got, good, want)
		}
	}
	for _, bad := range []string{"", ".", "com", "1.1.1.1", "127.1", "0x7f.0.0.1", "[::1]", "::1", "a..b",
		"-a.com", "a-.com", "a.com..", "user@a.com", "a.com:443", "a%2eb.com", "a b.com", "a.com\r\n",
		"xn--.com", strings.Repeat("a", 64) + ".com", strings.Repeat("a.", 127) + "com", "a.123",
		// Beyond the plan's rows: Unicode case folding must not smuggle a non-ASCII byte in
		// (U+212A KELVIN SIGN lowercases to 'k'), a hex-looking last label is ambiguous to a
		// libc resolver (inet_aton reads "0x7f.0x1" as 127.0.0.1), and a lone dot after a
		// trailing dot is still an empty label.
		"aK.com", "café.com", "0x7f.0x1", "1.0x1", "a.com.\x00", "\x00.com", "a.com.\t"} {
		if got, good := Normalize(bad); good {
			t.Errorf("Normalize(%q) accepted as %q", bad, got)
		}
	}
	// Longest legal name (253 bytes) is accepted, one more is not.
	long := strings.Repeat(strings.Repeat("a", 61)+".", 4) + "com" // 4*62+3 = 251
	if _, good := Normalize(long); !good {
		t.Errorf("251-byte name refused")
	}
	if _, good := Normalize(strings.Repeat(strings.Repeat("a", 62)+".", 4) + "com"); good { // 255
		t.Errorf("255-byte name accepted")
	}
}

func TestValidPattern(t *testing.T) {
	for _, p := range []string{"api.anthropic.com", "*.amazonaws.com", "*.s3.ap-southeast-1.amazonaws.com"} {
		if err := ValidPattern(p); err != nil {
			t.Errorf("%q: %v", p, err)
		}
	}
	for _, p := range []string{"*", "*.com", "*.", "a.*.com", "**.a.com", "*a.com", "API.anthropic.com", "1.2.3.4", "a.com.",
		"", "*.*.a.com", "*.A.com", "*.a.com.", "* .a.com", "*.1.2.3.4", "*.[::1]"} {
		if err := ValidPattern(p); err == nil {
			t.Errorf("%q accepted", p)
		}
	}
}

func TestMatch(t *testing.T) {
	pats := []string{"api.anthropic.com", "*.amazonaws.com"}
	for _, h := range []string{"api.anthropic.com", "sts.amazonaws.com", "s3.ap-southeast-1.amazonaws.com"} {
		if !Match(pats, h) {
			t.Errorf("%q not matched", h)
		}
	}
	for _, h := range []string{"amazonaws.com", "evilamazonaws.com", "anthropic.com", "x.api.anthropic.com", "amazonaws.com.evil.io",
		// Not normalised, or not a name at all: refused even if the suffix would fit.
		".amazonaws.com", "..amazonaws.com", "STS.amazonaws.com", "sts.amazonaws.com.", "API.anthropic.com", "", "1.1.1.1"} {
		if Match(pats, h) {
			t.Errorf("%q matched", h)
		}
	}
	// Invalid patterns never match anything (fail closed).
	for _, p := range []string{"*", "*.com", "*.", "", "1.1.1.1", "API.anthropic.com"} {
		for _, h := range []string{"a.com", "a.b.com", "com", "1.1.1.1", "api.anthropic.com"} {
			if Match([]string{p}, h) {
				t.Errorf("invalid pattern %q matched %q", p, h)
			}
		}
	}
	if Match(nil, "api.anthropic.com") {
		t.Errorf("empty pattern list matched")
	}
}
