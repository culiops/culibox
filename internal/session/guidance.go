package session

import (
	"fmt"
	"slices"
	"strings"

	"github.com/culiops/culibox/internal/config"
)

// cloneGuidance is the one line a boxed claude in a private copy is told at
// start (claude --append-system-prompt): where its work goes. push says the
// binding can push to the clone's origin; autoReturn says the branches come
// home on their own when the session ends.
func cloneGuidance(of string, push, autoReturn bool) string {
	s := "CuliBox: you are working in a private copy of " + of + ". "
	if push {
		s += `Commit on a branch, push it with "git push -u origin <branch>" and open a PR with "gh pr create"; pushing to main may be rejected by branch protection. `
		if autoReturn {
			return s + "Your branches also come home to " + of + " as box/<branch> when this session ends."
		}
		return s + "Your branches reach " + of + " as box/<branch> when the user brings them home."
	}
	s += "It cannot push to GitHub from here: commit on a branch, and your branches "
	if autoReturn {
		return s + "come home to " + of + " as box/<branch> when this session ends."
	}
	return s + "reach " + of + " as box/<branch> when the user brings them home."
}

// maxGuidanceNames caps how many names of one kind credGuidance lists, so a
// profile with many bindings cannot grow the system prompt without bound.
const maxGuidanceNames = 8

// credGuidance is what a boxed agent is told at start about the credentials
// its session holds: the names, which are PROD and how each kind is used. It
// is built only from validated names, GitHub owners and permission keys, never
// a value, an account, a role, a label or a description (free text must not
// become a system instruction). The set is fixed at spawn, so the text cannot
// go stale; live state (sign-in, base role, elevation) stays in agentbox-env
// --list and the broker's errors. It does not claim the env holds no keys:
// legacy secrets are still plain env. "" when nothing is bound.
func credGuidance(bs config.Bindings) string {
	if !bs.Any() {
		return ""
	}
	parts := []string{"CuliBox credentials for this session (fixed when it started; every use goes through CuliBox's credential broker, so never ask the user for keys):"}
	var aws []string
	if bs.AWS != nil {
		aws = append(aws, prodName("agentbox", bs.AWS.Prod))
	}
	for _, c := range bs.AWSConns {
		aws = append(aws, prodName(c.Name, c.Prod))
	}
	if len(aws) > 0 {
		parts = append(parts, "AWS profiles: "+guidanceNames(aws)+" (use aws --profile <name>, never aws configure).")
	}
	if g := bs.GitHub; g != nil {
		owners := make([]string, len(g.Owners))
		for i, o := range g.Owners {
			owners[i] = o.Owner
		}
		var write []string
		for k, v := range g.EffectivePermissions() {
			if v == "write" {
				write = append(write, k)
			}
		}
		perm := "read only"
		if len(write) > 0 {
			slices.Sort(write)
			perm = "write: " + strings.Join(write, ", ")
		}
		parts = append(parts, "GitHub owners: "+guidanceNames(owners)+" (git and gh; "+perm+").")
	}
	if len(bs.Tokens) > 0 {
		toks := make([]string, len(bs.Tokens))
		for i, t := range bs.Tokens {
			toks[i] = prodName(t.Name, t.Prod)
		}
		parts = append(parts, "Tokens: "+guidanceNames(toks)+" (run agentbox-env <name> -- <command>).")
	}
	// The legacy binding alone gets no agentbox-env script, so --list would
	// only say there is nothing to list.
	if len(bs.AWSConns) > 0 || bs.GitHub != nil || len(bs.Tokens) > 0 {
		parts = append(parts, "agentbox-env --list shows accounts, regions, repos and live sign-in state.")
	}
	parts = append(parts, "If a credential error says a human must sign in, stop and tell the user.")
	return strings.Join(parts, " ")
}

func prodName(name string, prod bool) string {
	if prod {
		return name + " (PROD)"
	}
	return name
}

// guidanceNames joins names, listing at most maxGuidanceNames of them.
func guidanceNames(names []string) string {
	if len(names) <= maxGuidanceNames {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:maxGuidanceNames], ", ") + fmt.Sprintf(", and %d more", len(names)-maxGuidanceNames)
}
