package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad(t *testing.T) {
	p := writeTemp(t, `{"bind_addr":"127.0.0.1:8080","basic_auth_user":"me","basic_auth_pass_env":"AC_PASS","agents":[{"name":"claude","bin":"claude","args":["--foo"]}]}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.BindAddr != "127.0.0.1:8080" || c.BasicAuthUser != "me" || c.BasicAuthPassEnv != "AC_PASS" {
		t.Fatalf("unexpected config: %+v", c)
	}
	if len(c.Agents) != 1 || c.Agents[0].Bin != "claude" || c.Agents[0].Args[0] != "--foo" {
		t.Fatalf("agents parsed wrong: %+v", c.Agents)
	}
}

func TestValidate_RejectsPublicBind(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8080", ":8080", "[::]:8080"} {
		c := Config{BindAddr: addr, Agents: []AgentConfig{{Name: "a", Bin: "a"}}}
		if err := c.Validate(); err == nil {
			t.Errorf("bind %q should be rejected", addr)
		}
	}
}

func TestValidate_AllowsLoopbackAndOverride(t *testing.T) {
	c := Config{BindAddr: "127.0.0.1:8080", Agents: []AgentConfig{{Name: "a", Bin: "a"}}}
	if err := c.Validate(); err != nil {
		t.Errorf("loopback should pass: %v", err)
	}
	c2 := Config{BindAddr: "0.0.0.0:8080", AllowPublic: true, Agents: []AgentConfig{{Name: "a", Bin: "a"}}}
	if err := c2.Validate(); err != nil {
		t.Errorf("allow_public override should pass: %v", err)
	}
}

func TestValidate_RequiresAgent(t *testing.T) {
	c := Config{BindAddr: "127.0.0.1:8080"}
	if err := c.Validate(); err == nil {
		t.Error("empty agents should be rejected")
	}
}

func TestLoad_EnvModelFields(t *testing.T) {
	p := writeTemp(t, `{
      "bind_addr":"127.0.0.1:8080","basic_auth_user":"me","basic_auth_pass_env":"AC_PASS",
      "secret_sources":[{"type":"env"},{"type":"file","path":"/x/secrets.env"}],
      "agents":[
        {"name":"claude","bin":"claude","base":"login","env":{"ANTHROPIC_MODEL":"m"}},
        {"name":"locked","bin":"claude","base":"clean","secrets":["TOK"]}
      ]}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.SecretSources) != 2 || c.SecretSources[1].Type != "file" || c.SecretSources[1].Path != "/x/secrets.env" {
		t.Fatalf("secret_sources parsed wrong: %+v", c.SecretSources)
	}
	if c.Agents[0].Base != "login" || c.Agents[0].Env["ANTHROPIC_MODEL"] != "m" {
		t.Fatalf("agent0 parsed wrong: %+v", c.Agents[0])
	}
	if len(c.Agents[1].Secrets) != 1 || c.Agents[1].Secrets[0] != "TOK" {
		t.Fatalf("agent1 secrets parsed wrong: %+v", c.Agents[1])
	}
}

func TestLoad_AbducoFields(t *testing.T) {
	p := writeTemp(t, `{
      "bind_addr":"127.0.0.1:8080","basic_auth_user":"me","basic_auth_pass_env":"AC_PASS",
      "data_dir":"/var/lib/agent-console","abduco_bin":"/usr/local/bin/abduco",
      "abduco_socket_dir":"/run/agent-console/abduco",
      "agents":[{"name":"claude","bin":"claude"}]}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != "/var/lib/agent-console" {
		t.Fatalf("data_dir=%q", c.DataDir)
	}
	if c.AbducoBin != "/usr/local/bin/abduco" {
		t.Fatalf("abduco_bin=%q", c.AbducoBin)
	}
	if c.AbducoSocketDir != "/run/agent-console/abduco" {
		t.Fatalf("abduco_socket_dir=%q", c.AbducoSocketDir)
	}
}

func TestBasePolicy_DefaultsInherit(t *testing.T) {
	if (AgentConfig{}).BasePolicy() != BaseInherit {
		t.Fatal("empty base should default to inherit")
	}
	if (AgentConfig{Base: "clean"}).BasePolicy() != BaseClean {
		t.Fatal("explicit base should be honored")
	}
}

func TestValidate_RejectsInvalidBase(t *testing.T) {
	c := Config{BindAddr: "127.0.0.1:8080", Agents: []AgentConfig{{Name: "a", Bin: "a", Base: "bogus"}}}
	if err := c.Validate(); err == nil {
		t.Error("invalid base should be rejected")
	}
}

func TestValidate_Options(t *testing.T) {
	mk := func(opts []OptionDef, agents []AgentConfig) Config {
		return Config{BindAddr: "127.0.0.1:8080", Options: opts, Agents: agents}
	}
	good := mk(
		[]OptionDef{{Key: "skip", Type: "bool", Flag: "-x"}, {Key: "eff", Type: "enum", Choices: []string{"hi"}, Flag: "--eff"}},
		[]AgentConfig{{Name: "a", Bin: "a", Options: map[string]any{"skip": true, "eff": "hi"}}},
	)
	if err := good.Validate(); err != nil {
		t.Fatalf("valid options config rejected: %v", err)
	}

	bad := map[string]Config{
		"bad type":          mk([]OptionDef{{Key: "x", Type: "weird", Flag: "-x"}}, []AgentConfig{{Name: "a", Bin: "a"}}),
		"enum no choices":   mk([]OptionDef{{Key: "x", Type: "enum", Flag: "-x"}}, []AgentConfig{{Name: "a", Bin: "a"}}),
		"no flag/env":       mk([]OptionDef{{Key: "x", Type: "bool"}}, []AgentConfig{{Name: "a", Bin: "a"}}),
		"dup key":           mk([]OptionDef{{Key: "x", Type: "bool", Flag: "-a"}, {Key: "x", Type: "bool", Flag: "-b"}}, []AgentConfig{{Name: "a", Bin: "a"}}),
		"bool with choices": mk([]OptionDef{{Key: "x", Type: "bool", Flag: "-x", Choices: []string{"y"}}}, []AgentConfig{{Name: "a", Bin: "a"}}),
		"unknown option":    mk([]OptionDef{{Key: "x", Type: "bool", Flag: "-x"}}, []AgentConfig{{Name: "a", Bin: "a", Options: map[string]any{"nope": true}}}),
		"enum default bad":  mk([]OptionDef{{Key: "x", Type: "enum", Choices: []string{"hi"}, Flag: "-x"}}, []AgentConfig{{Name: "a", Bin: "a", Options: map[string]any{"x": "bad"}}}),
		"env-secret clash":  mk([]OptionDef{{Key: "x", Type: "enum", Choices: []string{"hi"}, Env: "TOK"}}, []AgentConfig{{Name: "a", Bin: "a", Options: map[string]any{"x": "hi"}, Secrets: []string{"TOK"}}}),
	}
	for name, bc := range bad {
		if err := bc.Validate(); err == nil {
			t.Errorf("%s: should be rejected", name)
		}
	}
}

func TestOptionDefAppliesTo(t *testing.T) {
	tests := []struct {
		name string
		bins []string
		bin  string
		want bool
	}{
		{"empty bins applies to all", nil, "bash", true},
		{"exact match", []string{"claude"}, "claude", true},
		{"basename match", []string{"claude"}, "/usr/local/bin/claude", true},
		{"no match", []string{"claude"}, "bash", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := OptionDef{Bins: tt.bins}
			if got := o.AppliesTo(tt.bin); got != tt.want {
				t.Errorf("AppliesTo(%q) bins=%v = %v, want %v", tt.bin, tt.bins, got, tt.want)
			}
		})
	}
}

func TestConfigExampleValidates(t *testing.T) {
	c, err := Load("../../config.example.json")
	if err != nil {
		t.Fatalf("load config.example.json: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("config.example.json must validate: %v", err)
	}
}

func TestValidateProfileBinScoping(t *testing.T) {
	defs := map[string]OptionDef{
		"model": {Key: "model", Label: "Model", Type: OptEnum, Choices: []string{"claude-opus-5"}, Flag: "--model", Bins: []string{"claude"}},
	}
	if err := ValidateProfile(AgentConfig{Name: "b", Bin: "bash", Options: map[string]any{"model": "claude-opus-5"}}, defs); err == nil {
		t.Fatal("expected error: bash may not set a claude-only option")
	}
	if err := ValidateProfile(AgentConfig{Name: "c", Bin: "claude", Options: map[string]any{"model": "claude-opus-5"}}, defs); err != nil {
		t.Fatalf("claude setting model should be valid, got %v", err)
	}
}
