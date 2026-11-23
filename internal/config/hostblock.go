package config

import (
	"fmt"
	"slices"
	"strings"
)

// BindingKinds lists the credential bindings the profile declares, in a fixed
// order: "AWS" (inline or connections), "GitHub", "token". Empty for an
// unbound profile.
func (a AgentConfig) BindingKinds() []string {
	var k []string
	if a.AWS != nil || len(a.AWSConnections) > 0 {
		k = append(k, "AWS")
	}
	if a.GitHub != nil {
		k = append(k, "GitHub")
	}
	if len(a.Tokens) > 0 {
		k = append(k, "token")
	}
	return k
}

// JoinKinds reads a list as prose: "AWS", "AWS and GitHub", "AWS, GitHub and token".
func JoinKinds(k []string) string {
	if len(k) <= 1 {
		return strings.Join(k, "")
	}
	return strings.Join(k[:len(k)-1], ", ") + " and " + k[len(k)-1]
}

// BindingPhrase names a set of binding kinds for a refusal: "an AWS binding",
// "a GitHub binding", "a token binding", or for several "AWS and GitHub
// bindings" / "AWS, GitHub and token bindings".
func BindingPhrase(k []string) string {
	switch len(k) {
	case 0:
		// Only an empty set gets here; keep the historical wording.
		return "an AWS binding"
	case 1:
		if k[0] == "AWS" {
			return "an AWS binding"
		}
		return "a " + k[0] + " binding"
	default:
		return JoinKinds(k) + " bindings"
	}
}

// HostBlock says why the profile may not run on the host: "" when it may, else
// one lowercase clause for the UI and for the refusal. Only the box_only lock
// refuses a host run. Bindings, context paths and box-only env or options do
// not: a host run drops them (see HostDrops) and the spawn must acknowledge
// that. It does not look at Runtime: a profile that asks for the box is not
// refused here, only a host run is.
func HostBlock(a AgentConfig) string {
	if a.BoxOnly {
		return "this profile is set to box only"
	}
	return ""
}

// HostDrops lists what a host run of the profile would drop, one short noun
// phrase per item, in a fixed order: each binding kind ("AWS binding",
// "GitHub binding", "token binding"), "context paths", each box-only env the
// profile sets ("<NAME> setting"), then each box-only option it turns on
// (`"<Label>" option`, options in key order). nil when a host run drops nothing.
// It is not a lock: HostBlock is.
func HostDrops(a AgentConfig, defs map[string]OptionDef) []string {
	var out []string
	for _, k := range a.BindingKinds() {
		out = append(out, k+" binding")
	}
	if len(a.ContextPaths) > 0 {
		out = append(out, "context paths")
	}
	for _, n := range BoxOnlyEnv {
		if _, ok := a.Env[n]; ok || contains(a.Secrets, n) {
			out = append(out, n+" setting")
		}
	}
	for _, l := range boxOnlyOptionLabels(defs, a.Options) {
		out = append(out, fmt.Sprintf("%q option", l))
	}
	return out
}

// HostKeeps lists the profile's own secrets and env variables that a host run
// still hands the agent, as "<NAME> secret" (in the profile's order) then
// "<NAME> env" (sorted), each name once (a secret wins over an env of the same
// name, as at spawn). Box-only names are left out: HostDrops lists them. nil
// when there are none. The names come from the user's own profile, never a
// value.
func HostKeeps(a AgentConfig) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range a.Secrets {
		if n == "" || seen[n] || slices.Contains(BoxOnlyEnv, n) {
			continue
		}
		seen[n] = true
		out = append(out, n+" secret")
	}
	envs := make([]string, 0, len(a.Env))
	for n := range a.Env {
		if !seen[n] && !slices.Contains(BoxOnlyEnv, n) {
			envs = append(envs, n)
		}
	}
	slices.Sort(envs)
	for _, n := range envs {
		out = append(out, n+" env")
	}
	return out
}
