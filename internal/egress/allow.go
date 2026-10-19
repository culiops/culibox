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
// customer's storage account and widen exfiltration. Nor is it "any number":
// DNS measured 2026-10-19 from a test host shows N = 0..199 resolving except 22,
// while 200, 250, 999 and leading-zero spellings (01, 007) are NXDOMAIN. Names
// nobody owns in Azure's shared storage namespace are registrable by anyone,
// so admitting them would hand an agent an exfiltration path. Shards GitHub
// adds later (N >= 200) show up in the denied-host audit and can be added by
// re-measuring.
func GitHubAllow() []string {
	return []string{
		"api.github.com",
		"codeload.github.com",
		"results-receiver.actions.githubusercontent.com",
	}
}

// The number is 0, or 1-99, or 100-199: no leading zeros, nothing above 199.
var githubResultsHostRe = regexp.MustCompile(`^productionresultssa(0|[1-9][0-9]?|1[0-9]{2})\.blob\.core\.windows\.net$`)

// missingResultsShard is the one number in 0..199 that does not exist in DNS
// (measured 2026-10-19), so it is registrable by anyone and must stay denied.
const missingResultsShard = "22"

// GitHubResultsHost reports whether host (already normalised) is one of
// GitHub's Actions-log storage shards: productionresultssa0 to
// productionresultssa199 except 22 (see GitHubAllow). The proxy applies it only
// to GitHub bound sessions.
func GitHubResultsHost(host string) bool {
	m := githubResultsHostRe.FindStringSubmatch(host)
	return m != nil && m[1] != missingResultsShard
}

// Effective is the allowlist for a session with bindings bs: DefaultAllow, plus
// AWS.EgressAllow, plus (for GitHub) GitHubAllow and GitHub.EgressAllow, plus
// every host of every bound token entry (Tokens, then PATs), as a fresh slice
// that aliases none of them. Nothing bound adds nothing, so AWS-only and
// unbound sessions get exactly what they always did. Entry hosts are exact
// names, so they never widen into a wildcard.
func Effective(bs config.Bindings) []string {
	out := DefaultAllow()
	if bs.AWS != nil {
		out = append(out, bs.AWS.EgressAllow...)
	}
	if bs.GitHub != nil {
		out = append(out, GitHubAllow()...)
		out = append(out, bs.GitHub.EgressAllow...)
	}
	for _, list := range [][]config.StaticToken{bs.Tokens, bs.PATs} {
		for _, e := range list {
			for _, h := range e.Egress {
				out = append(out, h.Host)
			}
		}
	}
	return out
}
