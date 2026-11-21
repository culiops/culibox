package launcher

import (
	"fmt"

	"github.com/culiops/culibox/internal/config"
)

// ArgsBypassPermissions reports whether args turn Claude Code's permission
// checks off. The rule lives in config.ArgsBypass.
func ArgsBypassPermissions(args []string) bool { return config.ArgsBypass(args) }

// BypassesPermissions reports whether a spawn runs its agent with the bypass
// option: the flags come from the same merge Build uses (the profile's args, then
// its option defaults with the per-spawn overrides it accepts), so the answer is
// what will actually run. spec.ExtraArgs are the daemon's own flags (bundle,
// MCP) and never carry it. An unknown agent does not bypass.
//
// The box layer uses it to pre-accept the warning in a new box home: the option
// the user chose for the profile or the spawn is the consent.
func (l *AgentLauncher) BypassesPermissions(spec SpawnSpec) bool {
	cfg, ok := l.agent(spec.Agent)
	if !ok {
		return false
	}
	return config.ProfileBypasses(cfg, l.optionDefs, l.mergeOptions(cfg, spec))
}

// BoxOnlyOptionFor names the box-only option the spawn's merged options turn
// on (config.BoxOnlyOptionReason), "" when there is none. An unknown agent is
// an error, as in HostBlockFor, so a spawn naming one is refused.
func (l *AgentLauncher) BoxOnlyOptionFor(spec SpawnSpec) (string, error) {
	cfg, ok := l.agent(spec.Agent)
	if !ok {
		return "", fmt.Errorf("launcher: unknown agent %q", spec.Agent)
	}
	return config.BoxOnlyOptionReason(l.optionDefs, l.mergeOptions(cfg, spec)), nil
}

// claudeGateOptions deletes the critical-rm option unless the profile runs in
// bypass: the env only means something there, and in any other mode it would
// look like a setting that does nothing. opts is not changed.
func (l *AgentLauncher) claudeGateOptions(cfg config.AgentConfig, opts map[string]any) map[string]any {
	if _, ok := opts[config.CriticalRmKey]; !ok || config.ProfileBypasses(cfg, l.optionDefs, opts) {
		return opts
	}
	out := make(map[string]any, len(opts))
	for k, v := range opts {
		if k != config.CriticalRmKey {
			out[k] = v
		}
	}
	return out
}

// claudeDrops reports the critical-rm option as dropped when the merged
// options (before the gate) have it on and the profile does not bypass.
func (l *AgentLauncher) claudeDrops(cfg config.AgentConfig, merged map[string]any) []OptionDrop {
	def, ok := l.optionDefs[config.CriticalRmKey]
	if !ok || !def.On(merged[config.CriticalRmKey]) || config.ProfileBypasses(cfg, l.optionDefs, merged) {
		return nil
	}
	return []OptionDrop{{Key: config.CriticalRmKey, Reason: config.NeedsBypassReason}}
}

// stripBoxOnlyEnv deletes the env names only a box may set. A host spawn calls
// it after every env layer has been written.
func stripBoxOnlyEnv(env map[string]string) {
	for _, n := range config.BoxOnlyEnv {
		delete(env, n)
	}
}
