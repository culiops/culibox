package bundle

import (
	"encoding/json"
	"os"
	"sort"
)

// settingsKeepList is what may cross into a box, and nothing else.
//
// A keep-list rather than a deny-list, deliberately. settings.json is the more
// dangerous of the two config files: `hooks` runs arbitrary commands, `env` can
// hold literal tokens, `apiKeyHelper` and `awsAuthRefresh` point at host scripts
// that do not exist in the box, and `permissions` can switch off the agent's own
// prompts. Copying it verbatim would both hand those to the contained agent and
// write any secret into the bundle in plaintext. With a deny-list, a future
// settings key would leak by default; with a keep-list it stays out until
// someone decides otherwise.
var settingsKeepList = []string{
	"model",
	"effortLevel",
	"statusLine",
	"outputStyle",
	"includeCoAuthoredBy",
	"cleanupPeriodDays",
}

// filterSettings returns the keep-listed subset plus a Drop for everything else,
// so a missing setting is traceable instead of mysterious.
func filterSettings(path string) (map[string]any, []Drop) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}, nil // absent settings is normal
	}
	var all map[string]any
	if err := json.Unmarshal(raw, &all); err != nil {
		return map[string]any{}, []Drop{{Kind: "settings", Name: "*", Reason: "settings.json is not valid JSON"}}
	}
	keep := map[string]bool{}
	for _, k := range settingsKeepList {
		keep[k] = true
	}
	out := map[string]any{}
	var dropped []string
	for k, v := range all {
		if keep[k] {
			out[k] = v
			continue
		}
		dropped = append(dropped, k)
	}
	sort.Strings(dropped)
	var drops []Drop
	for _, k := range dropped {
		drops = append(drops, Drop{Kind: "settings", Name: k,
			Reason: "not on the bundle keep-list; it could carry host paths, literal secrets or permission overrides into the box"})
	}
	return out, drops
}
