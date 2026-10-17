package egress

import "github.com/culiops/agent-box/internal/config"

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

// Effective is the allowlist for a session with binding b: DefaultAllow plus
// b.EgressAllow, as a fresh slice that aliases neither (a nil b gives
// DefaultAllow alone).
func Effective(b *config.AWSBinding) []string {
	out := DefaultAllow()
	if b != nil {
		out = append(out, b.EgressAllow...)
	}
	return out
}
