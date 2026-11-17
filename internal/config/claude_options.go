package config

// claudeBuiltinOptions are the launch options every claude profile offers
// without an options block in config.json: how the agent thinks, never what it
// may do (no capability such as skip_permissions here; that stays opt-in).
// The model choices are Claude Code's aliases, which always name the latest
// model of each family, so the list does not go stale between releases.
var claudeBuiltinOptions = []OptionDef{
	{Key: "model", Label: "Model", Type: OptEnum, Choices: []string{"fable", "opus", "sonnet", "haiku"}, Flag: "--model", Bins: []string{claudeBin}},
	{Key: "effort", Label: "Effort", Type: OptEnum, Choices: []string{"low", "medium", "high", "xhigh", "max"}, Flag: "--effort", Bins: []string{claudeBin}},
}

// claudeSameSlot: effortLevel (the settings.json name) and effort fill the same
// Effort slot in the UI.
var claudeSameSlot = map[string]string{"effortLevel": "effort", "effort": "effortLevel"}
