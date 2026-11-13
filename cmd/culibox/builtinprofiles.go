package main

import "github.com/culiops/culibox/internal/config"

// builtinProfileNames names the profiles CuliBox added itself, for the profile
// list's "source" and so the first-run welcome. configured is cfg.Agents as read
// from the config file (or Defaults()); effective is that list plus the detected
// shell (config.WithDetectedShell), what the launcher runs.
//
// With no config file (src.Default) every effective agent is built in: the user
// has written nothing. With one, the agents in it are the user's own, so only
// what the detected-shell seed added on top is built in; that is nothing when a
// configured agent already runs the shell or every seed name is taken.
func builtinProfileNames(src config.Source, configured, effective []config.AgentConfig) []string {
	own := map[string]bool{}
	if !src.Default {
		for _, a := range configured {
			own[a.Name] = true
		}
	}
	var out []string
	for _, a := range effective {
		if !own[a.Name] {
			out = append(out, a.Name)
		}
	}
	return out
}
