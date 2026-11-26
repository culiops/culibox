package session

import (
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

// boundOwnLoginKinds is the host_own_login kinds a profile binds, in the fixed
// order github, aws. AWS is bound by the legacy inline binding or any named
// connection; GitHub by its binding or any PAT (a machine login for GitHub
// drops both).
func boundOwnLoginKinds(aws *config.AWSBinding, conns []config.AWSConnection, gh *config.GitHubBinding, pats []config.StaticToken) []string {
	var out []string
	if gh != nil || len(pats) > 0 {
		out = append(out, launcher.OwnLoginGitHub)
	}
	if aws != nil || len(conns) > 0 {
		out = append(out, launcher.OwnLoginAWS)
	}
	return out
}
