package config

import (
	"fmt"
	"sort"
)

// Claude Code's permission rules (#342). Generic option code never checks "is
// Claude"; it calls these.
const (
	PermissionModeKey = "permission_mode"
	BypassMode        = "bypassPermissions"
	CriticalRmKey     = "skip_critical_rm_prompt"
	CriticalRmEnv     = "CLAUDE_CODE_EVEN_MORE_DANGEROUSLY_SKIP_PERMISSIONS"
	NeedsBypassReason = "needs Permission mode: Bypass"

	legacyBypassFlag = "--dangerously-skip-permissions"
)

// BoxOnlyEnv lists the env names that only a box may set: a host process must
// never see them.
var BoxOnlyEnv = []string{CriticalRmEnv}

// ArgsBypass reports whether args turn Claude Code's permission checks off: the
// bare --dangerously-skip-permissions flag, or --permission-mode
// bypassPermissions (two args or one). Claude Code shows its one-time "Bypass
// Permissions mode" warning for either. An unrecognised spelling reads as "not
// bypassing", so the answer errs toward the dialog staying, never toward
// consent that was not given.
func ArgsBypass(args []string) bool {
	for i, a := range args {
		switch {
		case a == legacyBypassFlag, a == "--permission-mode="+BypassMode:
			return true
		case a == "--permission-mode" && i+1 < len(args) && args[i+1] == BypassMode:
			return true
		}
	}
	return false
}

// IsLegacyBypassDef reports whether d is the older single-bool form of bypass:
// recognised by its flag, never by its key name.
func IsLegacyBypassDef(d OptionDef) bool {
	return d.Type == OptBool && d.Flag == legacyBypassFlag
}

// EnumOwnsLegacyBypass reports whether the Permission mode control owns the
// older bypass bool for bin: the permission_mode def applies to bin and can say
// Bypass. A def whose choices omit bypassPermissions cannot stand in for the
// bool, so the bool stays a normal option of its own.
func EnumOwnsLegacyBypass(defs map[string]OptionDef, bin string) bool {
	d, ok := defs[PermissionModeKey]
	return ok && d.AppliesTo(bin) && contains(d.Choices, BypassMode)
}

// LegacyBypassKey returns the first (sorted) key the profile sets, to any
// value, whose def is a legacy bypass bool that applies to the profile's bin,
// while the Permission mode control owns it (EnumOwnsLegacyBypass). "" when
// there is none.
func LegacyBypassKey(defs map[string]OptionDef, a AgentConfig) string {
	if !EnumOwnsLegacyBypass(defs, a.Bin) {
		return ""
	}
	keys := make([]string, 0, len(a.Options))
	for k := range a.Options {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if d, ok := defs[k]; ok && IsLegacyBypassDef(d) && d.AppliesTo(a.Bin) {
			return k
		}
	}
	return ""
}

// OptionOffered reports whether the profile offers d at spawn time: OfferedBy,
// and also permission_mode on a profile that carries the legacy bypass bool the
// enum owns, so the older form can be replaced by the enum.
func OptionOffered(d OptionDef, a AgentConfig, defs map[string]OptionDef) bool {
	if d.OfferedBy(a) {
		return true
	}
	return d.Key == PermissionModeKey && d.AppliesTo(a.Bin) && LegacyBypassKey(defs, a) != ""
}

// SupersedeLegacyBypass drops the legacy bypass bools when permission_mode is
// on, so the enum wins. Otherwise opts is returned as it is. The input is
// never changed.
func SupersedeLegacyBypass(defs map[string]OptionDef, opts map[string]any) map[string]any {
	mode, ok := defs[PermissionModeKey]
	if !ok || !mode.On(opts[PermissionModeKey]) {
		return opts
	}
	out := make(map[string]any, len(opts))
	for k, v := range opts {
		if d, ok := defs[k]; ok && IsLegacyBypassDef(d) {
			continue
		}
		out[k] = v
	}
	return out
}

// ProfileBypasses reports whether the profile's command would run with bypass:
// its own args, then the flags of opts (with the legacy form superseded by
// permission_mode, as the launcher merges them).
func ProfileBypasses(a AgentConfig, defs map[string]OptionDef, opts map[string]any) bool {
	cmd := append([]string{}, a.Args...)
	cmd = append(cmd, OptionFlags(defs, SupersedeLegacyBypass(defs, opts))...)
	return ArgsBypass(cmd)
}

// BoxOnlyEnvReason names the first BoxOnlyEnv name the profile sets in Env or
// lists in Secrets. "" when there is none.
func BoxOnlyEnvReason(a AgentConfig) string {
	for _, n := range BoxOnlyEnv {
		if _, ok := a.Env[n]; ok || contains(a.Secrets, n) {
			return "this profile sets " + n + ", which runs only in a box"
		}
	}
	return ""
}

// ValidateBoxOnlyEnvDef refuses a config option def whose env is one only a box
// may set unless the def is box tier: without the tier the host would be offered
// an option that puts the env into a host process.
func ValidateBoxOnlyEnvDef(d OptionDef) error {
	if d.Tier != RuntimeBox && contains(BoxOnlyEnv, d.Env) {
		return fmt.Errorf("config: option %q sets %s, which runs only in a box; add \"tier\": \"box\"", d.Key, d.Env)
	}
	return nil
}

// validateClaudePermission refuses the critical-rm option on a profile that does
// not bypass permissions, and on one that pins the host (the option is box tier).
func validateClaudePermission(a AgentConfig, defs map[string]OptionDef) error {
	const field = "options." + CriticalRmKey
	if !defs[CriticalRmKey].On(a.Options[CriticalRmKey]) {
		return nil
	}
	if !ProfileBypasses(a, defs, a.Options) {
		return WithField(field, fmt.Errorf("profile %q: %s", a.Name, NeedsBypassReason))
	}
	if a.Runtime == RuntimeHost {
		reason := BoxOnlyOptionReason(defs, a.Options)
		if reason == "" {
			// A config def that replaced the built-in without tier "box" is not named by the tier rule.
			label := defs[CriticalRmKey].Label
			if label == "" {
				label = CriticalRmKey
			}
			reason = fmt.Sprintf("%q runs only in a box", label)
		}
		return WithField(field, fmt.Errorf("profile %q: %s", a.Name, reason))
	}
	return nil
}
