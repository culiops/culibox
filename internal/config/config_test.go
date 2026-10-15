package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	p := writeTemp(t, `{"bind_addr":"127.0.0.1:8080","password_env":"AB_PASS","agents":[{"name":"claude","bin":"claude","args":["--foo"]}]}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.BindAddr != "127.0.0.1:8080" || c.PasswordEnv != "AB_PASS" {
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
      "bind_addr":"127.0.0.1:8080","password_env":"AB_PASS",
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
      "bind_addr":"127.0.0.1:8080","password_env":"AB_PASS",
      "data_dir":"/var/lib/agent-box","abduco_bin":"/usr/local/bin/abduco",
      "abduco_socket_dir":"/run/agent-box/abduco",
      "agents":[{"name":"claude","bin":"claude"}]}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != "/var/lib/agent-box" {
		t.Fatalf("data_dir=%q", c.DataDir)
	}
	if c.AbducoBin != "/usr/local/bin/abduco" {
		t.Fatalf("abduco_bin=%q", c.AbducoBin)
	}
	if c.AbducoSocketDir != "/run/agent-box/abduco" {
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

func TestConfigEnvFieldsParse(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(`{"env_label":"PROD","env_color":"#b45309"}`), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.EnvLabel != "PROD" || c.EnvColor != "#b45309" {
		t.Fatalf("got %q/%q, want PROD/#b45309", c.EnvLabel, c.EnvColor)
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

func TestUploadLimitDefaults(t *testing.T) {
	if got := (Config{}).UploadLimit(); got != 64<<20 {
		t.Errorf("zero: got %d, want %d", got, int64(64<<20))
	}
	if got := (Config{MaxUploadBytes: -5}).UploadLimit(); got != 64<<20 {
		t.Errorf("negative: got %d, want default", got)
	}
	if got := (Config{MaxUploadBytes: 123}).UploadLimit(); got != 123 {
		t.Errorf("set: got %d, want 123", got)
	}
}

func TestArchiveLimitsDefaults(t *testing.T) {
	b, n := (Config{}).ArchiveLimits()
	if b != 512<<20 || n != 20000 {
		t.Errorf("defaults = %d, %d; want %d, 20000", b, n, int64(512<<20))
	}
	b, n = (Config{MaxArchiveBytes: 7, MaxArchiveFiles: 9}).ArchiveLimits()
	if b != 7 || n != 9 {
		t.Errorf("set = %d, %d; want 7, 9", b, n)
	}
}

func TestRuntimePolicyDefaultsToBox(t *testing.T) {
	if got := (AgentConfig{Name: "a", Bin: "b"}).RuntimePolicy(); got != RuntimeBox {
		t.Fatalf("default runtime = %q, want %q", got, RuntimeBox)
	}
	if got := (AgentConfig{Name: "a", Bin: "b", Runtime: RuntimeHost}).RuntimePolicy(); got != RuntimeHost {
		t.Fatalf("explicit runtime = %q, want %q", got, RuntimeHost)
	}
}

func TestValidateRejectsUnknownRuntime(t *testing.T) {
	c := Config{BindAddr: "127.0.0.1:1", Agents: []AgentConfig{{Name: "a", Bin: "b", Runtime: "vm"}}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected validation error for runtime \"vm\"")
	}
}

func TestValidateProfileRejectsUnknownRuntime(t *testing.T) {
	err := ValidateProfile(AgentConfig{Name: "p", Bin: "b", Runtime: "vm"}, map[string]OptionDef{})
	if err == nil {
		t.Fatal("profiles must not bypass runtime validation")
	}
}

func TestBoxIdleTTLDefaults(t *testing.T) {
	if got := (Config{}).BoxIdleTTL(); got != 15*time.Minute {
		t.Fatalf("default ttl = %v, want 15m", got)
	}
	if got := (Config{BoxIdleTTLSeconds: 60}).BoxIdleTTL(); got != time.Minute {
		t.Fatalf("ttl = %v, want 1m", got)
	}
}

// The auto-detected shell profile is generated, not configured, so no per-agent
// setting can reach it. Without an instance-level default it is always boxed,
// even on an instance whose every configured agent is host — which made the
// startup warning fire on a correct configuration.
func TestRuntimeDefaultAppliesToAgentsThatDoNotSetOne(t *testing.T) {
	c := Config{DefaultRuntime: RuntimeHost}
	if got := c.RuntimeFor(AgentConfig{Name: "generated"}); got != RuntimeHost {
		t.Fatalf("an agent with no runtime must take the instance default, got %q", got)
	}
	if got := c.RuntimeFor(AgentConfig{Name: "pinned", Runtime: RuntimeBox}); got != RuntimeBox {
		t.Fatalf("an agent that sets its own runtime must keep it, got %q", got)
	}
}

// Unset must still mean contained: the safe tier is the one you get by omission.
func TestRuntimeDefaultIsBoxWhenUnset(t *testing.T) {
	if got := (Config{}).RuntimeFor(AgentConfig{Name: "a"}); got != RuntimeBox {
		t.Fatalf("with no instance default the tier must be box, got %q", got)
	}
}

func TestValidateRejectsUnknownDefaultRuntime(t *testing.T) {
	c := Config{BindAddr: "127.0.0.1:1", DefaultRuntime: "vm",
		Agents: []AgentConfig{{Name: "a", Bin: "b"}}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected validation error for default_runtime \"vm\"")
	}
}

// config.example.json is what docs/MACOS.md points readers at: it must load and
// show allowed_hosts.
func TestExampleConfigLoadsAndShowsAllowedHosts(t *testing.T) {
	c, err := Load("../../config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.AllowedHosts) == 0 {
		t.Fatal("config.example.json must carry an allowed_hosts example")
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("example config must validate: %v", err)
	}
}

func TestBoxVMSizeDefaults(t *testing.T) {
	if c, m := (Config{}).BoxVMSize(); c != 4 || m != 4 {
		t.Fatalf("defaults = %d/%d", c, m)
	}
	if c, m := (Config{BoxVMCPUs: 2, BoxVMMemoryGiB: 6}).BoxVMSize(); c != 2 || m != 6 {
		t.Fatalf("overrides = %d/%d", c, m)
	}
}

func TestLoad_BoxVMFields(t *testing.T) {
	p := writeTemp(t, `{"colima_bin":"/opt/homebrew/bin/colima","box_vm_cpus":2,"box_vm_memory_gib":6,"agents":[{"name":"c","bin":"claude"}]}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.ColimaBin != "/opt/homebrew/bin/colima" || c.BoxVMCPUs != 2 || c.BoxVMMemoryGiB != 6 {
		t.Fatalf("vm fields = %+v", c)
	}
}

func TestBoxImageOrDefault(t *testing.T) {
	if got := (Config{}).BoxImageOrDefault(); got != "culiops/agent-box:base" {
		t.Fatalf("default = %q", got)
	}
	if got := (Config{BoxImage: "me/img:1"}).BoxImageOrDefault(); got != "me/img:1" {
		t.Fatalf("override = %q", got)
	}
}

// An image that starts with "-" is parsed by docker as a flag in
// `docker run … <image>`, `image inspect <image>` and `--entrypoint node <image>`.
func TestValidate_BoxImage(t *testing.T) {
	base := func() Config {
		return Config{BindAddr: "127.0.0.1:8080", Agents: []AgentConfig{{Name: "c", Bin: "claude"}}}
	}
	for _, ok := range []string{"", "culiops/agent-box:base", "registry.example.com:5000/a/b_c-d.e:1.2", "img@sha256:abc123"} {
		c := base()
		c.BoxImage = ok
		if err := c.Validate(); err != nil {
			t.Fatalf("box_image %q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"-v", "--privileged", "a b", "a,b", "a\nb", "img;rm", "img$x", "img\"x", "\u00efmg", "a=b"} {
		c := base()
		c.BoxImage = bad
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "box image") {
			t.Fatalf("box_image %q must be refused for its image, got %v", bad, err)
		}
	}
	// the per-agent override reaches the same docker argv
	c := base()
	c.Agents[0].Box.Image = "-v"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "box image") {
		t.Fatalf("per-agent box.image \"-v\" must be refused: %v", err)
	}
	if err := ValidateProfile(c.Agents[0], nil); err == nil || !strings.Contains(err.Error(), "box image") {
		t.Fatalf("a profile with box.image \"-v\" must be refused: %v", err)
	}
}

func TestBoxInstanceDefaultsToDefault(t *testing.T) {
	if got := (Config{}).BoxInstance(); got != "default" {
		t.Fatalf("got %q", got)
	}
	if got := (Config{EnvLabel: "PROD"}).BoxInstance(); got != "PROD" {
		t.Fatalf("got %q", got)
	}
}
