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

func TestBasePolicy_DefaultsClean(t *testing.T) {
	if (AgentConfig{}).BasePolicy() != BaseClean {
		t.Fatal("empty base should default to clean")
	}
	if (AgentConfig{Base: "login"}).BasePolicy() != BaseLogin {
		t.Fatal("explicit base should be honored")
	}
}

func TestValidate_RejectsInvalidBase(t *testing.T) {
	c := Config{BindAddr: "127.0.0.1:8080", Agents: []AgentConfig{{Name: "a", Bin: "a", Base: "bogus"}}}
	if err := c.Validate(); err == nil {
		t.Error("invalid base should be rejected")
	}
}
