package egress

import (
	"reflect"
	"slices"
	"testing"

	"github.com/culiops/agent-box/internal/config"
)

// TestEffectiveAWSOnlyUnchanged pins the pre-change result: an AWS-only (or
// unbound) session's allowlist must not change.
func TestEffectiveAWSOnlyUnchanged(t *testing.T) {
	want := []string{
		"api.anthropic.com",
		"*.amazonaws.com",
		"registry.terraform.io",
		"releases.hashicorp.com",
		"github.com",
		"extra.example.org",
	}
	if got := Effective(config.Bindings{AWS: bound()}); !reflect.DeepEqual(got, want) {
		t.Fatalf("Effective(aws) = %q, want %q", got, want)
	}
	if got := Effective(config.Bindings{}); !reflect.DeepEqual(got, want[:5]) {
		t.Fatalf("Effective(config.Bindings{}) = %q, want %q", got, want[:5])
	}
}

func TestEffectiveGitHub(t *testing.T) {
	gh := &config.GitHubBinding{EgressAllow: []string{"ghe.example.org"}}
	got := Effective(config.Bindings{GitHub: gh})
	for _, h := range append(GitHubAllow(), "ghe.example.org") {
		if !slices.Contains(got, h) {
			t.Errorf("Effective(config.Bindings{GitHub: gh}) lacks %q: %q", h, got)
		}
	}
	for _, h := range DefaultAllow() {
		if !slices.Contains(got, h) {
			t.Errorf("Effective(config.Bindings{GitHub: gh}) lacks default %q", h)
		}
	}
	both := Effective(config.Bindings{AWS: bound(), GitHub: gh})
	if !slices.Contains(both, "extra.example.org") || !slices.Contains(both, "api.github.com") {
		t.Fatalf("Effective(aws, gh) = %q", both)
	}
	// Fresh slices: writing to a result never changes the policy or binding.
	g := GitHubAllow()
	g[0] = "mutated"
	got[0] = "mutated"
	if GitHubAllow()[0] != "api.github.com" || gh.EgressAllow[0] != "ghe.example.org" {
		t.Fatal("a returned allowlist aliases the policy or the binding")
	}
}

func TestEffectiveTokens(t *testing.T) {
	tok := config.StaticToken{Name: "sentry", Egress: []config.TokenEgress{
		{Host: "sentry.io"}, {Host: "sentry.corp.example", Port: 8443, Private: true, CIDR: "10.20.0.0/16"}}}
	pat := config.StaticToken{Name: "gh-pat-acme", Egress: []config.TokenEgress{{Host: "ghe.acme.example"}}}
	got := Effective(config.Bindings{AWS: bound(), Tokens: []config.StaticToken{tok}, PATs: []config.StaticToken{pat}})
	for _, h := range []string{"sentry.io", "sentry.corp.example", "ghe.acme.example", "extra.example.org"} {
		if !slices.Contains(got, h) {
			t.Errorf("Effective lacks %q: %q", h, got)
		}
	}
	// Order as today, token hosts last.
	want := append(append(DefaultAllow(), "extra.example.org"), "sentry.io", "sentry.corp.example", "ghe.acme.example")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Effective = %q, want %q", got, want)
	}
	// Tokens alone make no GitHub binding: no GitHub hosts.
	if slices.Contains(got, "api.github.com") {
		t.Fatalf("tokens must not add GitHubAllow: %q", got)
	}
}

func TestGitHubResultsHost(t *testing.T) {
	host := func(n string) string { return "productionresultssa" + n + ".blob.core.windows.net" }
	// Measured 2026-10-19: 0..199 resolve except 22; the rest are NXDOMAIN.
	for _, n := range []string{"0", "1", "9", "13", "16", "21", "23", "99", "100", "199"} {
		if !GitHubResultsHost(host(n)) {
			t.Errorf("GitHubResultsHost(%q) = false, want true", host(n))
		}
	}
	for _, n := range []string{"22", "200", "250", "999", "01", "007", "00", "1000", "1234", "-1", "1a", ""} {
		if GitHubResultsHost(host(n)) {
			t.Errorf("GitHubResultsHost(%q) = true, want false", host(n))
		}
	}
	for _, h := range []string{
		"evil.blob.core.windows.net",
		"productionresultssa13.blob.core.windows.net.evil.com",
		"xproductionresultssa13.blob.core.windows.net",
		"",
	} {
		if GitHubResultsHost(h) {
			t.Errorf("GitHubResultsHost(%q) = true, want false", h)
		}
	}
}
