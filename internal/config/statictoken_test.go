package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func specTokens() []StaticToken {
	return []StaticToken{
		{Name: "grafana-cloud",
			Value:     KeyRef{Type: "env", Var: "GRAFANA_SA_TOKEN"},
			Env:       []string{"GRAFANA_SERVICE_ACCOUNT_TOKEN"},
			ConfigEnv: map[string]string{"GRAFANA_URL": "https://acme.grafana.net"},
			Egress:    []TokenEgress{{Host: "acme.grafana.net"}},
			MCP:       &TokenMCP{Server: "grafana", Command: "mcp-grafana", Args: []string{"--disable-docs"}}},
		{Name: "atlassian",
			Value: KeyRef{Type: "file", Path: "/home/u/.config/agent-box/atlassian.token"},
			Env:   []string{"JIRA_API_TOKEN", "CONFLUENCE_API_TOKEN"},
			ConfigEnv: map[string]string{"JIRA_URL": "https://acme.atlassian.net", "JIRA_USERNAME": "me@acme.com",
				"CONFLUENCE_URL": "https://acme.atlassian.net/wiki", "CONFLUENCE_USERNAME": "me@acme.com"},
			Egress: []TokenEgress{{Host: "acme.atlassian.net"}},
			MCP:    &TokenMCP{Server: "atlassian", Command: "mcp-atlassian"}},
		{Name: "sentry-corp",
			Value:     KeyRef{Type: "env", Var: "SENTRY_TOKEN"},
			Env:       []string{"SENTRY_ACCESS_TOKEN"},
			ConfigEnv: map[string]string{"SENTRY_HOST": "sentry.corp.example"},
			Egress:    []TokenEgress{{Host: "sentry.corp.example", Private: true, CIDR: "10.20.0.0/16"}},
			MCP:       &TokenMCP{Server: "sentry", Command: "sentry-mcp"}},
		{Name: "gh-pat-acme",
			Value:  KeyRef{Type: "file", Path: "/home/u/.config/agent-box/gh-acme.pat"},
			Env:    []string{"GH_TOKEN"},
			Egress: []TokenEgress{}},
	}
}

func TestStaticTokenValidate(t *testing.T) {
	for _, e := range specTokens() {
		if err := e.Validate(); err != nil {
			t.Errorf("spec entry %q should be valid: %v", e.Name, err)
		}
	}
	many := func(n int, f func(i int) string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = f(i)
		}
		return out
	}
	cases := []struct {
		name string
		mut  func(*StaticToken)
		want string // "" = valid; else a substring naming the field
	}{
		{"private cidr ok", func(e *StaticToken) {
			e.Egress = []TokenEgress{{Host: "a.corp.example", Private: true, CIDR: "10.20.0.0/16"}}
		}, ""},
		{"ula cidr ok", func(e *StaticToken) {
			e.Egress = []TokenEgress{{Host: "a.corp.example", Private: true, CIDR: "fd12:3456::/32"}}
		}, ""},
		{"port ok", func(e *StaticToken) { e.Egress = []TokenEgress{{Host: "a.corp.example", Port: 8443}} }, ""},
		{"uppercase name", func(e *StaticToken) { e.Name = "Grafana" }, ".name"},
		{"64-char name", func(e *StaticToken) { e.Name = strings.Repeat("a", 64) }, ".name"},
		{"no env", func(e *StaticToken) { e.Env = []string{} }, ".env"},
		{"five env", func(e *StaticToken) { e.Env = many(5, func(i int) string { return "TOK_" + string(rune('A'+i)) }) }, ".env"},
		{"bad env name", func(e *StaticToken) { e.Env = []string{"1BAD"} }, ".env"},
		{"duplicate env", func(e *StaticToken) { e.Env = []string{"TOK_A", "TOK_A"} }, ".env"},
		{"env PATH", func(e *StaticToken) { e.Env = []string{"PATH"} }, ".env"},
		{"env https_proxy", func(e *StaticToken) { e.Env = []string{"https_proxy"} }, ".env"},
		{"env AGENTBOX_X", func(e *StaticToken) { e.Env = []string{"AGENTBOX_X"} }, ".env"},
		{"env LD_PRELOAD", func(e *StaticToken) { e.Env = []string{"LD_PRELOAD"} }, ".env"},
		{"env GIT_CONFIG_KEY_0", func(e *StaticToken) { e.Env = []string{"GIT_CONFIG_KEY_0"} }, ".env"},
		{"env AWS_SECRET_ACCESS_KEY", func(e *StaticToken) { e.Env = []string{"AWS_SECRET_ACCESS_KEY"} }, ".env"},
		{"config_env key also in env", func(e *StaticToken) { e.ConfigEnv = map[string]string{"GRAFANA_SERVICE_ACCOUNT_TOKEN": "x"} }, ".config_env"},
		{"config_env denied key", func(e *StaticToken) { e.ConfigEnv = map[string]string{"HOME": "/x"} }, ".config_env"},
		{"config_env bad key", func(e *StaticToken) { e.ConfigEnv = map[string]string{"a-b": "x"} }, ".config_env"},
		{"17 config_env pairs", func(e *StaticToken) {
			e.ConfigEnv = map[string]string{}
			for _, k := range many(17, func(i int) string { return "CFG_" + string(rune('A'+i)) }) {
				e.ConfigEnv[k] = "v"
			}
		}, ".config_env"},
		{"16 config_env pairs ok", func(e *StaticToken) {
			e.ConfigEnv = map[string]string{}
			for _, k := range many(16, func(i int) string { return "CFG_" + string(rune('A'+i)) }) {
				e.ConfigEnv[k] = "v"
			}
		}, ""},
		{"1025-byte value", func(e *StaticToken) { e.ConfigEnv = map[string]string{"CFG": strings.Repeat("a", 1025)} }, ".config_env"},
		{"1024-byte value ok", func(e *StaticToken) { e.ConfigEnv = map[string]string{"CFG": strings.Repeat("a", 1024)} }, ""},
		{"newline in value", func(e *StaticToken) { e.ConfigEnv = map[string]string{"CFG": "a\nb"} }, ".config_env"},
		{"wildcard host", func(e *StaticToken) { e.Egress = []TokenEgress{{Host: "*.grafana.net"}} }, ".egress"},
		{"uppercase host", func(e *StaticToken) { e.Egress = []TokenEgress{{Host: "Acme.Grafana.Net"}} }, ".egress"},
		{"ip host", func(e *StaticToken) { e.Egress = []TokenEgress{{Host: "10.0.0.1"}} }, ".egress"},
		{"17 hosts", func(e *StaticToken) {
			e.Egress = nil
			for i := 0; i < 17; i++ {
				e.Egress = append(e.Egress, TokenEgress{Host: "h" + string(rune('a'+i)) + ".example.com"})
			}
		}, ".egress"},
		{"port 70000", func(e *StaticToken) { e.Egress = []TokenEgress{{Host: "a.example.com", Port: 70000}} }, ".egress"},
		{"port -1", func(e *StaticToken) { e.Egress = []TokenEgress{{Host: "a.example.com", Port: -1}} }, ".egress"},
		{"private without cidr", func(e *StaticToken) { e.Egress = []TokenEgress{{Host: "a.example.com", Private: true}} }, ".egress"},
		{"cidr without private", func(e *StaticToken) { e.Egress = []TokenEgress{{Host: "a.example.com", CIDR: "10.0.0.0/8"}} }, ".egress"},
		{"cgnat cidr", func(e *StaticToken) {
			e.Egress = []TokenEgress{{Host: "a.example.com", Private: true, CIDR: "100.64.0.0/10"}}
		}, ".egress"},
		{"public cidr", func(e *StaticToken) {
			e.Egress = []TokenEgress{{Host: "a.example.com", Private: true, CIDR: "8.8.8.0/24"}}
		}, ".egress"},
		{"too wide cidr", func(e *StaticToken) {
			e.Egress = []TokenEgress{{Host: "a.example.com", Private: true, CIDR: "10.0.0.0/7"}}
		}, ".egress"},
		{"too wide v6 cidr", func(e *StaticToken) {
			e.Egress = []TokenEgress{{Host: "a.example.com", Private: true, CIDR: "fd00::/31"}}
		}, ".egress"},
		{"non-canonical cidr", func(e *StaticToken) {
			e.Egress = []TokenEgress{{Host: "a.example.com", Private: true, CIDR: "10.20.0.1/16"}}
		}, ".egress"},
		{"garbage cidr", func(e *StaticToken) {
			e.Egress = []TokenEgress{{Host: "a.example.com", Private: true, CIDR: "nope"}}
		}, ".egress"},
		{"bad mcp server", func(e *StaticToken) { e.MCP = &TokenMCP{Server: "Bad", Command: "x"} }, ".mcp"},
		{"mcp arg interpolation", func(e *StaticToken) { e.MCP = &TokenMCP{Server: "ok", Command: "x", Args: []string{"${TOKEN}"}} }, ".mcp"},
		{"33 mcp args", func(e *StaticToken) {
			e.MCP = &TokenMCP{Server: "ok", Command: "x", Args: many(33, func(int) string { return "a" })}
		}, ".mcp"},
		{"empty mcp command", func(e *StaticToken) { e.MCP = &TokenMCP{Server: "ok", Command: ""} }, ".mcp"},
		{"five mcp extras", func(e *StaticToken) {
			e.MCP = &TokenMCP{Server: "ok", Command: "x", Extra: []string{"a", "b", "c", "d", "e"}}
		}, ".mcp"},
		{"duplicate mcp extras", func(e *StaticToken) {
			e.MCP = &TokenMCP{Server: "ok", Command: "x", Extra: []string{"a", "a"}}
		}, ".mcp"},
		{"mcp extra names self", func(e *StaticToken) {
			e.MCP = &TokenMCP{Server: "ok", Command: "x", Extra: []string{e.Name}}
		}, ".mcp"},
		{"bad provider", func(e *StaticToken) { e.Provider = "Sentry_Corp" }, ".provider"},
		{"33-char label", func(e *StaticToken) { e.Label = strings.Repeat("a", 33) }, ".label"},
		{"201-char description", func(e *StaticToken) { e.Description = strings.Repeat("a", 201) }, ".description"},
		{"bad value ref", func(e *StaticToken) { e.Value = KeyRef{Type: "store", Path: "x"} }, ".value"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := specTokens()[0].Clone()
			c.mut(&e)
			err := e.Validate()
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("want valid, got %v", err)
			case c.want != "" && err == nil:
				t.Fatalf("want error naming %q, got nil", c.want)
			case c.want != "" && !strings.Contains(err.Error(), c.want):
				t.Fatalf("error %q does not name %q", err, c.want)
			case err != nil && !strings.HasPrefix(err.Error(), `static_tokens["`):
				t.Fatalf("error %q lacks the static_tokens[%%q] prefix", err)
			}
		})
	}
}

func TestStaticTokenEmptyEgressPassesLoad(t *testing.T) {
	e := specTokens()[3]
	if len(e.Egress) != 0 {
		t.Fatal("fixture must have empty egress")
	}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	e.Egress = nil
	if err := e.Validate(); err != nil {
		t.Fatalf("nil egress: %v", err)
	}
}

func TestTokenEnvAllowsTokenNames(t *testing.T) {
	for _, n := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "ANTHROPIC_API_KEY", "SENTRY_ACCESS_TOKEN"} {
		if TokenEnvDenied(n) {
			t.Errorf("%s must be allowed", n)
		}
		e := specTokens()[0].Clone()
		e.Env = []string{n}
		if err := e.Validate(); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
	for _, n := range []string{"HTTP_PROXY", "NpM_config_registry", "GIT_CONFIG_VALUE_3", "TF_CLI_ARGS_plan", "DYLD_X", "BASH_FUNC_x%%", "AB_PASS", "AWS_REGION", "AWS_SESSION_TOKEN", "NODE_OPTIONS", "GH_HOST", "CLAUDE_CONFIG_DIR"} {
		if !TokenEnvDenied(n) {
			t.Errorf("%s must be denied", n)
		}
	}
}

func TestValidateStaticTokensSet(t *testing.T) {
	if err := ValidateStaticTokens(specTokens()); err != nil {
		t.Fatal(err)
	}
	if err := ValidateStaticTokens(nil); err != nil {
		t.Fatal(err)
	}
	mk := func(name, server string, extra ...string) StaticToken {
		return StaticToken{Name: name, Value: KeyRef{Type: "env", Var: "SENTRY_TOKEN"},
			Env: []string{"SENTRY_ACCESS_TOKEN"}, Egress: []TokenEgress{{Host: name + ".example.com"}},
			MCP: &TokenMCP{Server: server, Command: "sentry-mcp", Extra: extra}}
	}
	cases := []struct {
		name    string
		entries []StaticToken
		want    []string // substrings; nil = valid
	}{
		{"duplicate name", []StaticToken{mk("a", "x"), mk("a", "y")}, []string{`"a"`, "duplicate"}},
		{"duplicate mcp server", []StaticToken{mk("a", "x"), mk("b", "x")}, []string{`"x"`, "duplicate"}},
		{"extra names missing entry", []StaticToken{mk("a", "x", "ghost")}, []string{`"ghost"`}},
		{"extra names self", []StaticToken{mk("a", "x", "a")}, []string{".mcp", "extra"}},
		{"extra overlaps names", []StaticToken{mk("sentry-staging", "x", "sentry-prod"), mk("sentry-prod", "y")},
			[]string{"sentry-staging", "sentry-prod", "SENTRY_ACCESS_TOKEN"}},
		{"shared env names without extra is fine", []StaticToken{mk("sentry-staging", "x"), mk("sentry-prod", "y")}, nil},
		{"extra with disjoint names is fine", func() []StaticToken {
			o := mk("other", "y")
			o.Env = []string{"OTHER_TOKEN"}
			return []StaticToken{mk("a", "x", "other"), o}
		}(), nil},
		{"invalid entry surfaces", []StaticToken{{Name: "BAD"}}, []string{".name"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateStaticTokens(c.entries)
			if c.want == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("want error")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
		})
	}
}

func TestStaticTokenNamesCloneAndLookup(t *testing.T) {
	e := specTokens()[1]
	want := []string{"CONFLUENCE_API_TOKEN", "CONFLUENCE_URL", "CONFLUENCE_USERNAME", "JIRA_API_TOKEN", "JIRA_URL", "JIRA_USERNAME"}
	if got := e.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Names = %v, want %v", got, want)
	}
	c := e.Clone()
	if !reflect.DeepEqual(c, e) {
		t.Fatal("clone differs")
	}
	c.Env[0] = "X"
	c.ConfigEnv["JIRA_URL"] = "x"
	c.Egress[0].Host = "x.example.com"
	c.MCP.Command = "x"
	if e.Env[0] == "X" || e.ConfigEnv["JIRA_URL"] == "x" || e.Egress[0].Host == "x.example.com" || e.MCP.Command == "x" {
		t.Fatal("clone shares memory with the original")
	}
	if (TokenEgress{}).EffectivePort() != 443 || (TokenEgress{Port: 8443}).EffectivePort() != 8443 {
		t.Fatal("EffectivePort")
	}
	got, ok := TokenByName(specTokens(), "atlassian")
	if !ok || got.Name != "atlassian" {
		t.Fatal("TokenByName hit")
	}
	if _, ok := TokenByName(specTokens(), "nope"); ok {
		t.Fatal("TokenByName miss")
	}
}

func TestConfigRefusesPasswordEnvAsTokenVar(t *testing.T) {
	c := Config{BindAddr: "127.0.0.1:8080", PasswordEnv: "LOGIN_PASS", Agents: []AgentConfig{{Name: "a", Bin: "a"}}}
	e := specTokens()[0].Clone()
	e.Value = KeyRef{Type: "env", Var: "LOGIN_PASS"}
	c.StaticTokens = []StaticToken{e}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "password_env") {
		t.Fatalf("want password_env refusal, got %v", err)
	}
	c.StaticTokens = specTokens()
	if err := c.Validate(); err != nil {
		t.Fatalf("spec entries with a different password_env: %v", err)
	}
	c.PasswordEnv = ""
	c.StaticTokens = []StaticToken{e}
	if err := c.Validate(); err != nil {
		t.Fatalf("empty password_env must not match: %v", err)
	}
}

func TestConfigRoundTripStaticTokens(t *testing.T) {
	in := specTokens()
	b, err := json.Marshal(Config{StaticTokens: in})
	if err != nil {
		t.Fatal(err)
	}
	var out Config
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out.StaticTokens, in) {
		t.Fatalf("round trip differs:\n%+v\n%+v", out.StaticTokens, in)
	}
	if !strings.Contains(string(b), `"static_tokens"`) {
		t.Fatal("json key")
	}
	if b, _ := json.Marshal(Config{}); strings.Contains(string(b), "static_tokens") {
		t.Fatal("empty StaticTokens must be omitted")
	}
}
