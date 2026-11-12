package launcher

import (
	"testing"

	"github.com/culiops/culibox/internal/config"
)

// A session "uses the bypass option" when the command it will run carries
// Claude Code's bypass flag: from the profile's own args, from a profile
// default of an option, or from a per-spawn override the profile accepts. The
// answer comes from the same merge Build uses, so it cannot drift from what
// actually runs.
func TestBypassesPermissions(t *testing.T) {
	defs := []config.OptionDef{
		{Key: "skip_permissions", Label: "Skip permissions", Type: config.OptBool, Flag: "--dangerously-skip-permissions", Bins: []string{"claude"}},
		{Key: "permission_mode", Label: "Permission mode", Type: config.OptEnum, Flag: "--permission-mode",
			Choices: []string{"default", "plan", "bypassPermissions"}, Bins: []string{"claude"}},
		{Key: "model", Label: "Model", Type: config.OptEnum, Flag: "--model", Choices: []string{"opus", "sonnet"}},
	}
	for _, c := range []struct {
		name string
		cfg  config.AgentConfig
		spec SpawnSpec
		want bool
	}{
		{"no options", config.AgentConfig{}, SpawnSpec{}, false},
		{"profile arg, bare flag", config.AgentConfig{Args: []string{"--dangerously-skip-permissions"}}, SpawnSpec{}, true},
		{"profile arg, permission mode as two args", config.AgentConfig{Args: []string{"--permission-mode", "bypassPermissions"}}, SpawnSpec{}, true},
		{"profile arg, permission mode as one arg", config.AgentConfig{Args: []string{"--permission-mode=bypassPermissions"}}, SpawnSpec{}, true},
		{"profile arg, another permission mode", config.AgentConfig{Args: []string{"--permission-mode", "plan"}}, SpawnSpec{}, false},
		{"option default on", config.AgentConfig{Options: map[string]any{"skip_permissions": true}}, SpawnSpec{}, true},
		{"option default off", config.AgentConfig{Options: map[string]any{"skip_permissions": false}}, SpawnSpec{}, false},
		{"enum default bypass", config.AgentConfig{Options: map[string]any{"permission_mode": "bypassPermissions"}}, SpawnSpec{}, true},
		{"enum default plan", config.AgentConfig{Options: map[string]any{"permission_mode": "plan"}}, SpawnSpec{}, false},
		{"unrelated option", config.AgentConfig{Options: map[string]any{"model": "opus"}}, SpawnSpec{}, false},
		{"override turns it on", config.AgentConfig{Options: map[string]any{"skip_permissions": false}},
			SpawnSpec{Options: map[string]any{"skip_permissions": true}}, true},
		{"override turns it off", config.AgentConfig{Options: map[string]any{"skip_permissions": true}},
			SpawnSpec{Options: map[string]any{"skip_permissions": false}}, false},
		{"override the profile does not offer is ignored", config.AgentConfig{},
			SpawnSpec{Options: map[string]any{"skip_permissions": true}}, false},
		{"an invalid enum value is not a bypass", config.AgentConfig{Options: map[string]any{"permission_mode": "bypassPermissions "}}, SpawnSpec{}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.cfg.Name, c.cfg.Bin, c.cfg.Base = "a", "claude", "clean"
			l := New([]config.AgentConfig{c.cfg}, fakeResolver{}, defs)
			spec := c.spec
			spec.Agent = "a"
			if got := l.BypassesPermissions(spec); got != c.want {
				t.Fatalf("BypassesPermissions = %v, want %v", got, c.want)
			}
		})
	}
}

func TestBypassesPermissionsUnknownAgent(t *testing.T) {
	l := New(nil, fakeResolver{}, nil)
	if l.BypassesPermissions(SpawnSpec{Agent: "nope"}) {
		t.Fatal("an unknown agent does not bypass")
	}
}
