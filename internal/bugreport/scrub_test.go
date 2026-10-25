package bugreport

import (
	"strings"
	"testing"
)

func TestScrubPatterns(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"ghp", "x ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 y", "x [redacted] y"},
		{"gho", "x gho_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 y", "x [redacted] y"},
		{"ghs", "x ghs_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 y", "x [redacted] y"},
		{"github_pat", "x github_pat_11ABCDEFG0abcdefgh_ijklmnop y", "x [redacted] y"},
		{"akia", "key AKIAIOSFODNN7EXAMPLE end", "key [redacted] end"},
		{"asia", "key ASIAIOSFODNN7EXAMPLE end", "key [redacted] end"},
		{"bearer", "got Bearer abc.def-ghi here", "got Bearer [redacted] here"},
		{"slack", "tok xoxb-1-2-abc done", "tok [redacted] done"},
		{"password kv", "login password=hunter2 failed", "login password=[redacted] failed"},
		{"api_key colon", "API_KEY: s3cr3t", "API_KEY: [redacted]"},
		{"mixed-case key", "MyApiKey=abc123 ok", "MyApiKey=[redacted] ok"},
		{"token in name", "GH_TOKEN=abcdef next", "GH_TOKEN=[redacted] next"},
		{"quoted value", `secret="a b c" tail`, `secret=[redacted] tail`},
		{"passwd", "passwd: x1", "passwd: [redacted]"},
		{"auth basic", "Authorization: Basic Zm9vOmJhcg==", "Authorization: Basic [redacted]"},
		{"auth bearer", "authorization: Bearer abc.def", "authorization: Bearer [redacted]"},
		{"auth token scheme", "Authorization: token ghx123", "Authorization: token [redacted]"},
		{"auth no scheme", "Authorization: rawvalue123", "Authorization: [redacted]"},
		{"plain", "session 3 exited", "session 3 exited"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Scrub(c.in); got != c.want {
				t.Fatalf("Scrub(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestScrubNeverLeavesTheValue(t *testing.T) {
	for _, in := range []string{
		"password=hunter2", "Password: hunter2", "client_secret = hunter2",
		"Authorization: Basic hunter2", "x-api-key=hunter2",
	} {
		if got := Scrub(in); strings.Contains(got, "hunter2") {
			t.Errorf("Scrub(%q) = %q leaks the value", in, got)
		}
	}
}
