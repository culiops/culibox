package config

// claudeBuiltinOptions are the launch options every claude profile offers
// without an options block in config.json: how the agent thinks (model, effort)
// and, since #342 (founder 2026-11-19), the two capability options
// permission_mode and skip_critical_rm_prompt. Neither capability is offered
// unset (see unsetOfferKeys), and the critical-rm option is box-only.
// The model choices are Claude Code's aliases, which always name the latest
// model of each family, so the list does not go stale between releases.
var claudeBuiltinOptions = []OptionDef{
	{Key: "model", Label: "Model", Type: OptEnum, Choices: []string{"fable", "opus", "sonnet", "haiku"}, Flag: "--model", Bins: []string{claudeBin}},
	{Key: "effort", Label: "Effort", Type: OptEnum, Choices: []string{"low", "medium", "high", "xhigh", "max"}, Flag: "--effort", Bins: []string{claudeBin}},
	{Key: PermissionModeKey, Label: "Permission mode", Type: OptEnum, Choices: []string{"manual", "acceptEdits", "auto", "plan", BypassMode}, Flag: "--permission-mode", Bins: []string{claudeBin}},
	{Key: CriticalRmKey, Label: "Skip the critical-rm prompt", Type: OptBool, Env: CriticalRmEnv, Bins: []string{claudeBin}, Tier: RuntimeBox},
}

// claudeSameSlot: effortLevel (the settings.json name) and effort fill the same
// Effort slot in the UI.
var claudeSameSlot = map[string]string{"effortLevel": "effort", "effort": "effortLevel"}
