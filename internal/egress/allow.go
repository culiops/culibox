package egress

import (
	"regexp"

	"github.com/culiops/agent-box/internal/config"
)

// DefaultAllow returns, as a fresh slice, every bound box's allowlist before
// its binding's own egress_allow: the set measured in spec §7.1 (2026-10-17)
// under the env the session wiring sets. Claude's API, the AWS APIs (S3
// included; see spec §9), terraform init's registry and provider downloads, and
// GitHub, where the registry's modules and git's ls-remote go. A function, not
// a variable, so no caller can change the policy by writing to it.
func DefaultAllow() []string {
	return []string{
		"api.anthropic.com",
		"*.amazonaws.com",
		"registry.terraform.io",
		"releases.hashicorp.com",
		"github.com",
	}
}

// GitHubAllow returns, as a fresh slice, the hosts a GitHub-bound box adds on
// top of DefaultAllow (github.com is already there). Measured 2026-10-19
// (spec §7.5) with curl -D - against public repos:
//   - the REST API and git archives: api.github.com, codeload.github.com;
//   - GET .../actions/runs/{id}/logs (what `gh run view --log` uses) answers
//     302 to results-receiver.actions.githubusercontent.com, which serves 200;
//   - GET .../actions/jobs/{id}/logs answers 302 to a numbered shard,
//     productionresultssa<N>.blob.core.windows.net (N = 1, 9, 13, 16 seen),
//     which GitHubResultsHost matches.
//
// The shard rule is never *.blob.core.windows.net: that would admit every Azure
// customer's storage account and widen exfiltration. Hosts GitHub adds later
// show up in the denied-host audit.
func GitHubAllow() []string {
	return []string{
		"api.github.com",
		"codeload.github.com",
		"results-receiver.actions.githubusercontent.com",
	}
}

var githubResultsHostRe = regexp.MustCompile(`^productionresultssa[0-9]{1,3}\.blob\.core\.windows\.net$`)

// GitHubResultsHost reports whether host (already normalised) is one of
// GitHub's Actions-log storage shards. The proxy applies it only to GitHub
// bound sessions.
func GitHubResultsHost(host string) bool { return githubResultsHostRe.MatchString(host) }

// Effective is the allowlist for a session with bindings aws and gh:
// DefaultAllow, plus aws.EgressAllow, plus (for gh) GitHubAllow and
// gh.EgressAllow, as a fresh slice that aliases none of them. Nil bindings add
// nothing, so AWS-only and unbound sessions get exactly what they always did.
func Effective(aws *config.AWSBinding, gh *config.GitHubBinding) []string {
	out := DefaultAllow()
	if aws != nil {
		out = append(out, aws.EgressAllow...)
	}
	if gh != nil {
		out = append(out, GitHubAllow()...)
		out = append(out, gh.EgressAllow...)
	}
	return out
}
