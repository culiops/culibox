package session

import (
	"fmt"
	"slices"
	"strings"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

// cloneGuidance is the one line a boxed claude in a private copy is told at
// start (claude --append-system-prompt): where its work goes. push says the
// binding can push to the clone's origin; autoReturn says the branches come
// home on their own after each turn and when the session ends. Every variant
// ends by saying CuliBox never commits: uncommitted files do not come home.
func cloneGuidance(of string, push, autoReturn bool) string {
	const commit = " CuliBox never commits for you: commit your work on a branch when you finish a task; uncommitted files do not come home."
	s := "CuliBox: you are working in a private copy of " + of + ". "
	if push {
		s += `Commit on a branch, push it with "git push -u origin <branch>" and open a PR with "gh pr create"; pushing to main may be rejected by branch protection. `
		if autoReturn {
			return s + "Your branches also come home to " + of + " as box/<branch> after each turn and when this session ends." + commit
		}
		return s + "Your branches reach " + of + " as box/<branch> when the user brings them home." + commit
	}
	s += "It cannot push to GitHub from here: commit on a branch, and your branches "
	if autoReturn {
		return s + "come home to " + of + " as box/<branch> after each turn and when this session ends." + commit
	}
	return s + "reach " + of + " as box/<branch> when the user brings them home." + commit
}

// maxGuidanceNames caps how many names of one kind credGuidance lists, so a
// profile with many bindings cannot grow the system prompt without bound.
const maxGuidanceNames = 8

// credGuidance is what a boxed agent is told at start about the credentials
// its session holds: the names, which are PROD and how each kind is used. It
// is built only from validated names, GitHub owners and permission keys, never
// a value, an account, a role, a label or a description (free text must not
// become a system instruction). The set is the one at spawn (a token entry
// deleted on the host later is refused by the broker, not added back); live
// state (sign-in, base role, elevation) stays in agentbox-env --list and the
// broker's errors. It does not claim the env holds no keys: legacy secrets are
// still plain env. A PAT owner never gets the App permissions: CuliBox does
// not narrow a personal access token. "" when nothing is bound.
func credGuidance(bs config.Bindings) string {
	if !bs.Any() {
		return ""
	}
	parts := []string{"CuliBox credentials for this session, as set up when it started (every use goes through CuliBox's credential broker: never ask the user to paste a key, and if one you need is missing, tell the user to bind it in CuliBox):"}
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
		var apps, pats []string
		for _, o := range g.Owners {
			if o.Token != "" {
				pats = append(pats, o.Owner)
			} else {
				apps = append(apps, o.Owner)
			}
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
		if len(apps) > 0 {
			parts = append(parts, "GitHub owners: "+guidanceNames(apps)+" (git and gh; "+perm+").")
		}
		if len(pats) > 0 {
			parts = append(parts, "GitHub owners with a personal access token: "+guidanceNames(pats)+" (git and gh; the token's own scopes, not narrowed by CuliBox).")
		}
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
		s := "Run agentbox-env --list for the details"
		switch {
		case len(bs.AWSConns) > 0:
			s += ", including live AWS sign-in state"
		case bs.AWS != nil: // exclusive with AWSConns; it has no aws.json
			s += "; profile agentbox is not in it"
		}
		parts = append(parts, s+".")
	}
	parts = append(parts, "If a credential error says a human must sign in, stop and tell the user.")
	return strings.Join(parts, " ")
}

// hostCredLine ends the credential note of a Host session (#360, D5): its
// credentials are the same as a box's, but it is not in one, so it must also
// be told they are a default and the machine's own logins are not to be used.
const hostCredLine = "This session runs on the host, not in a box: these are the default credentials, not a limit. Use them, not this machine's own logins."

// hostNoPushLine is added to a Host note whose GitHub binding cannot push: the
// user's way to a push is this machine's own login (#375).
func hostNoPushLine(contents string) string {
	if contents == "" {
		contents = "none"
	}
	return "GitHub refuses a push through the GitHub binding (contents: " + contents + "); to push from Host, the user can start a session with \"Use this machine's login\" for GitHub."
}

// hostOwnLoginLine names the kinds the user took from this machine's own login
// for this session (own: "github" / "aws").
func hostOwnLoginLine(own []string) string {
	var labels []string
	for _, k := range own {
		switch k {
		case launcher.OwnLoginGitHub:
			labels = append(labels, "GitHub")
		case launcher.OwnLoginAWS:
			labels = append(labels, "AWS")
		}
	}
	return "Exception to that, for this session only: for " + strings.Join(labels, " and ") + " the user chose this machine's own login, so use it."
}

// hostCredGuidance is credGuidance for a bound Host session: the same note and
// hostCredLine, then the kinds on this machine's own login, then (a GitHub
// binding that cannot push) hostNoPushLine. "" when nothing is bound.
func hostCredGuidance(bs config.Bindings, own []string) string {
	s := credGuidance(bs)
	if s == "" {
		return ""
	}
	s += " " + hostCredLine
	if len(own) > 0 {
		s += " " + hostOwnLoginLine(own)
	}
	if bs.GitHub != nil && !bs.GitHub.PushRoute() {
		s += " " + hostNoPushLine(bs.GitHub.EffectivePermissions()["contents"])
	}
	return s
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
