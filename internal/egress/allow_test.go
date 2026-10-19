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
	if got := Effective(bound(), nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("Effective(aws, nil) = %q, want %q", got, want)
	}
	if got := Effective(nil, nil); !reflect.DeepEqual(got, want[:5]) {
		t.Fatalf("Effective(nil, nil) = %q, want %q", got, want[:5])
	}
}

func TestEffectiveGitHub(t *testing.T) {
	gh := &config.GitHubBinding{EgressAllow: []string{"ghe.example.org"}}
	got := Effective(nil, gh)
	for _, h := range append(GitHubAllow(), "ghe.example.org") {
		if !slices.Contains(got, h) {
			t.Errorf("Effective(nil, gh) lacks %q: %q", h, got)
		}
	}
	for _, h := range DefaultAllow() {
		if !slices.Contains(got, h) {
			t.Errorf("Effective(nil, gh) lacks default %q", h)
		}
	}
	both := Effective(bound(), gh)
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

func TestGitHubResultsHost(t *testing.T) {
	cases := map[string]bool{
		"productionresultssa13.blob.core.windows.net":          true,
		"productionresultssa1.blob.core.windows.net":           true,
		"productionresultssa999.blob.core.windows.net":         true,
		"evil.blob.core.windows.net":                           false,
		"productionresultssa.blob.core.windows.net":            false,
		"productionresultssa1234.blob.core.windows.net":        false,
		"productionresultssa13.blob.core.windows.net.evil.com": false,
		"xproductionresultssa13.blob.core.windows.net":         false,
		"": false,
	}
	for host, want := range cases {
		if got := GitHubResultsHost(host); got != want {
			t.Errorf("GitHubResultsHost(%q) = %v, want %v", host, got, want)
		}
	}
}
