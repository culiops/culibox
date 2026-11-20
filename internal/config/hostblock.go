package config

import "strings"

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
// one lowercase clause for the UI and for the refusal. The first rule that
// holds wins, in this order: credential bindings, context paths, the box_only
// lock. It does not look at Runtime: a profile that asks for the box is not
// refused here, only a host run is.
func HostBlock(a AgentConfig) string {
	if k := a.BindingKinds(); len(k) > 0 {
		return "this profile has " + BindingPhrase(k) + ", so it runs only in a box"
	}
	if len(a.ContextPaths) > 0 {
		return "this profile has context paths, which only a box can mount"
	}
	if a.BoxOnly {
		return "this profile is set to box only"
	}
	return ""
}
