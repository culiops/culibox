package config

import (
	"encoding/json"
	"fmt"
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
		{"mcp command interpolation", func(e *StaticToken) {
			e.MCP = &TokenMCP{Server: "ok", Command: "${AGENTBOX_BROKER_TOKEN}"}
		}, ".mcp"},
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

func tokenByNameT(t *testing.T, name string) StaticToken {
	t.Helper()
	e, ok := TokenByName(specTokens(), name)
	if !ok {
		t.Fatalf("no spec entry %q", name)
	}
	return e
}

func TestBindingsHashTokens(t *testing.T) {
	sentry, grafana, pat := tokenByNameT(t, "sentry-corp"), tokenByNameT(t, "grafana-cloud"), tokenByNameT(t, "gh-pat-acme")
	base := Bindings{AWS: goodBinding()}
	one := Bindings{AWS: goodBinding(), Tokens: []StaticToken{sentry}}
	two := Bindings{AWS: goodBinding(), Tokens: []StaticToken{sentry, grafana}}
	rev := Bindings{AWS: goodBinding(), Tokens: []StaticToken{grafana, sentry}}

	if one.Hash() == base.Hash() {
		t.Error("adding a token must change the hash")
	}
	if len(one.Hash()) != 12 {
		t.Errorf("hash must be 12 hex, got %q", one.Hash())
	}
	if two.Hash() != rev.Hash() {
		t.Error("token order must not change the hash")
	}
	if two.Hash() == one.Hash() {
		t.Error("a second token must change the hash")
	}

	edited := sentry.Clone()
	edited.Egress[0].Host = "other.corp.example"
	if (Bindings{AWS: goodBinding(), Tokens: []StaticToken{edited}}).Hash() == one.Hash() {
		t.Error("editing egress[0].host must change the hash")
	}

	moved := sentry.Clone()
	moved.Value = KeyRef{Type: "file", Path: "/elsewhere/sentry.token"}
	if (Bindings{AWS: goodBinding(), Tokens: []StaticToken{moved}}).Hash() != one.Hash() {
		t.Error("editing value.path must not change the hash")
	}
	moved.Value = KeyRef{Type: "env", Var: "OTHER_VAR"}
	if (Bindings{AWS: goodBinding(), Tokens: []StaticToken{moved}}).Hash() != one.Hash() {
		t.Error("editing value.var must not change the hash")
	}

	withPAT := Bindings{GitHub: goodGitHub(), PATs: []StaticToken{pat}}
	if withPAT.Hash() == (Bindings{GitHub: goodGitHub()}).Hash() {
		t.Error("a PAT-only binding must differ from the same binding with no PAT entries")
	}
	// A token and a PAT of the same entry are different roles.
	if (Bindings{GitHub: goodGitHub(), Tokens: []StaticToken{pat}}).Hash() == withPAT.Hash() {
		t.Error("the same entry as a token and as a PAT must hash differently")
	}
	if !(Bindings{Tokens: []StaticToken{sentry}}).Any() {
		t.Error("a token-only binding is Any")
	}
	if (Bindings{PATs: []StaticToken{pat}}).Any() {
		t.Error("PATs alone are not a binding: a PAT owner implies GitHub")
	}
}

func TestDefHashCoversEveryFieldButValue(t *testing.T) {
	e := tokenByNameT(t, "sentry-corp")
	base := e.DefHash()
	if len(base) != 12 {
		t.Fatalf("want 12 hex, got %q", base)
	}
	for name, mut := range map[string]func(*StaticToken){
		"label":       func(e *StaticToken) { e.Label = "x" },
		"provider":    func(e *StaticToken) { e.Provider = "x" },
		"prod":        func(e *StaticToken) { e.Prod = true },
		"description": func(e *StaticToken) { e.Description = "x" },
		"env":         func(e *StaticToken) { e.Env = []string{"SENTRY_OTHER"} },
		"config_env":  func(e *StaticToken) { e.ConfigEnv = map[string]string{"SENTRY_HOST": "y"} },
		"mcp":         func(e *StaticToken) { e.MCP = &TokenMCP{Server: "sentry", Command: "other"} },
		"name":        func(e *StaticToken) { e.Name = "other" },
	} {
		c := e.Clone()
		mut(&c)
		if c.DefHash() == base {
			t.Errorf("changing %s must change DefHash", name)
		}
	}
	c := e.Clone()
	c.Value = KeyRef{Type: "env", Var: "ELSEWHERE"}
	if c.DefHash() != base {
		t.Error("value must not be hashed")
	}
}

func TestValidateTokensRequiresBox(t *testing.T) {
	ok := AgentConfig{Name: "p", Bin: "claude", Runtime: RuntimeBox, Tokens: []string{"sentry-corp"}}
	if err := validateTokens(ok); err != nil {
		t.Fatal(err)
	}
	if err := validateTokens(AgentConfig{Name: "p"}); err != nil {
		t.Fatalf("no tokens is fine: %v", err)
	}
	for _, rt := range []string{"", RuntimeHost} {
		a := ok
		a.Runtime = rt
		if err := ValidateProfile(a, nil); err == nil || !strings.Contains(err.Error(), "runtime: box") {
			t.Errorf("runtime %q must be refused, got %v", rt, err)
		}
	}
	many := ok
	for i := 0; i < 17; i++ {
		many.Tokens = append(many.Tokens, fmt.Sprintf("t-%d", i))
	}
	if err := validateTokens(many); err == nil || !strings.Contains(err.Error(), "16") {
		t.Errorf("17 names must be refused, got %v", err)
	}
	dup := ok
	dup.Tokens = []string{"a", "b", "a"}
	if err := validateTokens(dup); err == nil || !strings.Contains(err.Error(), `"a"`) {
		t.Errorf("a duplicate must be refused naming it, got %v", err)
	}
	bad := ok
	bad.Tokens = []string{"Not A Name"}
	if err := validateTokens(bad); err == nil || !strings.Contains(err.Error(), "Not A Name") {
		t.Errorf("a malformed name must be refused, got %v", err)
	}
}

func TestCheckTokens(t *testing.T) {
	entries := append(specTokens(),
		StaticToken{Name: "sentry-staging", Value: KeyRef{Type: "env", Var: "S1"}, Env: []string{"SENTRY_ACCESS_TOKEN"},
			Egress: []TokenEgress{{Host: "sentry.stg.example"}}},
		StaticToken{Name: "sentry-prod", Value: KeyRef{Type: "env", Var: "S2"}, Env: []string{"SENTRY_ACCESS_TOKEN"},
			Egress: []TokenEgress{{Host: "sentry.prd.example"}}},
	)
	opts := map[string]OptionDef{
		"jira":  {Key: "jira", Type: OptBool, Env: "JIRA_URL", Bins: []string{"claude"}},
		"other": {Key: "other", Type: OptBool, Env: "ATLASSIAN_ONLY_FOR_CODEX", Bins: []string{"codex"}},
	}
	prof := func(f func(*AgentConfig)) AgentConfig {
		a := AgentConfig{Name: "p", Bin: "claude", Runtime: RuntimeBox}
		f(&a)
		return a
	}
	cases := []struct {
		name string
		a    AgentConfig
		want string // "" = valid
	}{
		{"nothing bound", prof(func(a *AgentConfig) {}), ""},
		{"unknown name", prof(func(a *AgentConfig) { a.Tokens = []string{"ghost"} }), "ghost"},
		{"empty-egress entry in tokens", prof(func(a *AgentConfig) { a.Tokens = []string{"gh-pat-acme"} }),
			`"gh-pat-acme" has no egress hosts; it can only be a GitHub owner's token`},
		{"empty-egress entry as owner token", prof(func(a *AgentConfig) {
			a.GitHub = &GitHubBinding{Owners: []GitHubOwner{{Owner: "acme", Token: "gh-pat-acme", Repos: []string{"r"}}}}
		}), ""},
		{"owner token undefined", prof(func(a *AgentConfig) {
			a.GitHub = &GitHubBinding{Owners: []GitHubOwner{{Owner: "acme", Token: "ghost-pat", Repos: []string{"r"}}}}
		}), "ghost-pat"},
		{"env collision", prof(func(a *AgentConfig) {
			a.Tokens = []string{"sentry-corp"}
			a.Env = map[string]string{"SENTRY_HOST": "x"}
		}), "token \"sentry-corp\" sets SENTRY_HOST"},
		{"secret collision", prof(func(a *AgentConfig) {
			a.Tokens = []string{"grafana-cloud"}
			a.Secrets = []string{"GRAFANA_SERVICE_ACCOUNT_TOKEN"}
		}), "GRAFANA_SERVICE_ACCOUNT_TOKEN"},
		{"option env collision", prof(func(a *AgentConfig) { a.Tokens = []string{"atlassian"} }), "JIRA_URL"},
		{"option for another bin does not collide", prof(func(a *AgentConfig) {
			a.Tokens = []string{"sentry-corp"}
			a.Bin = "codex"
		}), ""},
		{"alternatives sharing a name are accepted", prof(func(a *AgentConfig) {
			a.Tokens = []string{"sentry-staging", "sentry-prod"}
		}), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckTokens(c.a, entries, opts)
			if c.want == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestCheckTokensCollisionMessage(t *testing.T) {
	a := AgentConfig{Name: "p", Bin: "claude", Runtime: RuntimeBox, Tokens: []string{"sentry-corp"},
		Env: map[string]string{"SENTRY_HOST": "x"}}
	err := CheckTokens(a, specTokens(), nil)
	if err == nil || !strings.HasPrefix(err.Error(), `token "sentry-corp" sets SENTRY_HOST, which the profile also sets as `) {
		t.Fatalf("got %v", err)
	}
}

func TestEntryMCPArgs(t *testing.T) {
	e := StaticToken{Name: "sentry-corp", Value: KeyRef{Type: "env", Var: "S"}, Env: []string{"SENTRY_ACCESS_TOKEN"},
		Egress: []TokenEgress{{Host: "sentry.corp.example"}},
		MCP:    &TokenMCP{Server: "sentry", Command: "sentry-mcp", Extra: []string{"llm-key"}}}
	got, err := EntryMCPArgs(e, map[string]bool{"sentry-corp": true, "llm-key": true})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"sentry-corp", "llm-key", "--", "sentry-mcp"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	e.MCP.Args = []string{"--flag", "v"}
	e.MCP.Extra = nil
	got, err = EntryMCPArgs(e, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"sentry-corp", "--", "sentry-mcp", "--flag", "v"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	e.MCP.Extra = []string{"llm-key"}
	_, err = EntryMCPArgs(e, map[string]bool{"sentry-corp": true})
	if err == nil || err.Error() != "sentry needs token llm-key, not bound to this profile" {
		t.Fatalf("got %v", err)
	}
	if _, err := EntryMCPArgs(StaticToken{Name: "x"}, nil); err == nil {
		t.Fatal("an entry with no mcp has no args")
	}
}

func TestConfigValidateChecksAgentTokens(t *testing.T) {
	agent := AgentConfig{Name: "a", Bin: "claude", Runtime: RuntimeBox, Tokens: []string{"sentry-corp"}}
	c := Config{BindAddr: "127.0.0.1:8080", Agents: []AgentConfig{agent}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "sentry-corp") {
		t.Fatalf("an undefined entry must be refused naming it, got %v", err)
	}
	c.StaticTokens = specTokens()
	if err := c.Validate(); err != nil {
		t.Fatalf("defined entry: %v", err)
	}
	c.Options = []OptionDef{{Key: "host", Type: OptBool, Env: "SENTRY_HOST"}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "SENTRY_HOST") {
		t.Fatalf("an option env colliding with the entry must be refused, got %v", err)
	}
	c.Options = nil
	c.Agents[0].Runtime = RuntimeHost
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "runtime: box") {
		t.Fatalf("host runtime must be refused, got %v", err)
	}
}
