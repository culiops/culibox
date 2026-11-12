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
//
// enabledPlugins is kept here but only reaches a box together with the plugin
// mounts: without them the box git-clones the marketplace and installs unpinned
// plugins from the network (measured 2026-10-19). materialize strips it unless
// pluginMounts approved the set.
var settingsKeepList = []string{
	"model",
	"effortLevel",
	"statusLine",
	"outputStyle",
	"includeCoAuthoredBy",
	"cleanupPeriodDays",
	"enabledPlugins",
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

// applyAttributionOff makes every box commit and open PRs without Claude Code's
// AI attribution (the Co-Authored-By trailer and the "Generated with" text). It
// edits settings, the keep-list-filtered map, in place and always wins over the
// host's value: a box has no commit-msg hook (clone boxes run with
// core.hooksPath=/dev/null on purpose) and public repos allow no AI attribution,
// so a host that wants it on does not get it in a box.
//
// Both spellings, because Claude Code 2.1.311 (the box image's pin) honours
// both: attribution.commit/pr set to "" hide the text, and the deprecated
// includeCoAuthoredBy:false is what older settings files and versions read.
// The image also pins the same two keys as managed policy
// (docker/agent-box/Dockerfile) for sessions that get no bundle.
func applyAttributionOff(settings map[string]any, lock *Lock) {
	if v, had := settings["includeCoAuthoredBy"]; had && v != false {
		lock.Dropped = append(lock.Dropped, Drop{Kind: "settings", Name: "includeCoAuthoredBy",
			Reason: "replaced: boxes never add AI attribution to commits"})
	}
	settings["includeCoAuthoredBy"] = false
	settings["attribution"] = map[string]any{"commit": "", "pr": ""}
}
