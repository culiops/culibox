package config

import (
	"slices"
	"testing"
)

func optKeys(opts []OptionDef) []string {
	keys := make([]string, 0, len(opts))
	for _, o := range opts {
		keys = append(keys, o.Key)
	}
	return keys
}

func findOpt(opts []OptionDef, key string) (OptionDef, bool) {
	for _, o := range opts {
		if o.Key == key {
			return o, true
		}
	}
	return OptionDef{}, false
}

// A config.json with no options (what culibox init writes) still offers model
// and effort on a claude profile, with the Claude Code aliases and effort levels.
func TestBuiltinOptionsWithoutOptionsBlock(t *testing.T) {
	c, err := parseConfig([]byte(`{"bind_addr":"127.0.0.1:8899","agents":[{"name":"claude","bin":"claude"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	model, ok := findOpt(c.Options, "model")
	if !ok {
		t.Fatalf("no built-in model option: %v", optKeys(c.Options))
	}
	if model.Type != OptEnum || model.Flag != "--model" || !slices.Equal(model.Choices, []string{"fable", "opus", "sonnet", "haiku"}) {
		t.Errorf("model = %+v", model)
	}
	effort, ok := findOpt(c.Options, "effort")
	if !ok {
		t.Fatalf("no built-in effort option: %v", optKeys(c.Options))
	}
	if effort.Type != OptEnum || effort.Flag != "--effort" || !slices.Equal(effort.Choices, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Errorf("effort = %+v", effort)
	}
	a := c.Agents[0]
	if !model.OfferedBy(a) || !effort.OfferedBy(a) {
		t.Error("model/effort not offered unset on a claude profile")
	}
}

// No config file at all gets the same built-ins.
func TestBuiltinOptionsInDefaults(t *testing.T) {
	keys := optKeys(Defaults().Options)
	if !slices.Contains(keys, "model") || !slices.Contains(keys, "effort") {
		t.Errorf("Defaults().Options = %v, want model and effort", keys)
	}
}

// Built-ins never include a capability: skip_permissions stays opt-in.
func TestBuiltinOptionsHaveNoBool(t *testing.T) {
	for _, o := range withBuiltinOptions(nil) {
		if o.Type == OptBool {
			t.Errorf("built-in bool option %q: a bool is a capability, keep it config-only", o.Key)
		}
	}
}

// A built-in does not apply to another agent binary (the quick shell).
func TestBuiltinOptionsClaudeOnly(t *testing.T) {
	for _, o := range withBuiltinOptions(nil) {
		if o.AppliesTo("/bin/bash") {
			t.Errorf("built-in %q applies to bash", o.Key)
		}
		if !o.AppliesTo("/usr/local/bin/claude") {
			t.Errorf("built-in %q does not apply to claude", o.Key)
		}
	}
}

// A declared key replaces the built-in: the user's def is kept as it is and
// listed once, declared options first and in order.
func TestBuiltinOptionsDeclaredKeyWins(t *testing.T) {
	declared := []OptionDef{
		{Key: "skip_permissions", Label: "Skip", Type: OptBool, Flag: "--dangerously-skip-permissions"},
		{Key: "model", Label: "Model", Type: OptEnum, Choices: []string{"claude-opus-4-8"}, Env: "ANTHROPIC_MODEL", Bins: []string{"claude"}},
	}
	got := withBuiltinOptions(declared)
	if want := []string{"skip_permissions", "model", "effort"}; !slices.Equal(optKeys(got), want) {
		t.Fatalf("keys = %v, want %v", optKeys(got), want)
	}
	if m := got[1]; m.Env != "ANTHROPIC_MODEL" || !slices.Equal(m.Choices, []string{"claude-opus-4-8"}) {
		t.Errorf("declared model replaced: %+v", m)
	}
}

// effortLevel fills the same Effort slot in the UI: declaring it must not add
// a second Effort picker.
func TestBuiltinOptionsEffortLevelSuppressesEffort(t *testing.T) {
	declared := []OptionDef{{Key: "effortLevel", Label: "Effort", Type: OptEnum, Choices: []string{"high"}, Env: "CLAUDE_EFFORT"}}
	if keys := optKeys(withBuiltinOptions(declared)); slices.Contains(keys, "effort") {
		t.Errorf("keys = %v: built-in effort added next to a declared effortLevel", keys)
	}
}

// A config that already lists model and effort (config.example.json style)
// loads and validates unchanged: no duplicate key from the merge.
func TestBuiltinOptionsFullyDeclared(t *testing.T) {
	c, err := parseConfig([]byte(`{"bind_addr":"127.0.0.1:8899",
		"options":[{"key":"effort","label":"Effort","type":"enum","choices":["high"],"flag":"--effort","bins":["claude"]},
		           {"key":"model","label":"Model","type":"enum","choices":["opus"],"flag":"--model","bins":["claude"]}],
		"agents":[{"name":"claude","bin":"claude"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if want := []string{"effort", "model"}; !slices.Equal(optKeys(c.Options), want) {
		t.Errorf("keys = %v, want %v", optKeys(c.Options), want)
	}
}

// An agent default for a built-in, with no options block, now validates.
func TestBuiltinOptionsAgentDefault(t *testing.T) {
	c, err := parseConfig([]byte(`{"bind_addr":"127.0.0.1:8899","agents":[{"name":"claude","bin":"claude","options":{"model":"opus","effort":"high"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}
