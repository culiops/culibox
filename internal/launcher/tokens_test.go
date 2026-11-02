package launcher

import (
	"reflect"
	"testing"

	"github.com/culiops/culibox/internal/config"
)

func tokenEntries() []config.StaticToken {
	return []config.StaticToken{
		{Name: "zeta", Value: config.KeyRef{Type: "env", Var: "ZETA_TOKEN"}, Env: []string{"ZETA_TOKEN"}, Egress: []config.TokenEgress{{Host: "z.example.com"}}},
		{Name: "grafana", Value: config.KeyRef{Type: "env", Var: "GRAFANA_SA_TOKEN"}, Env: []string{"GRAFANA_SA_TOKEN"},
			ConfigEnv: map[string]string{"GRAFANA_URL": "https://g.example.com"}, Egress: []config.TokenEgress{{Host: "g.example.com"}},
			MCP: &config.TokenMCP{Server: "grafana", Command: "mcp-grafana", Args: []string{"-t", "stdio"}}},
		{Name: "gh-pat-b", Value: config.KeyRef{Type: "file", Path: "/x/b"}, Env: []string{"GH_TOKEN"}},
		{Name: "gh-pat-a", Value: config.KeyRef{Type: "file", Path: "/x/a"}, Env: []string{"GH_TOKEN"}},
		{Name: "file-vals", Value: config.KeyRef{Type: "file", Path: "/x/f"}, Env: []string{"F_TOKEN"}, Egress: []config.TokenEgress{{Host: "f.example.com"}}},
	}
}

// A host-tier profile on an inherited base must not carry a token's value
// variable into its env: the daemon holds it for the broker to read.
func TestLauncherStripsTokenValueVars(t *testing.T) {
	t.Setenv("GRAFANA_SA_TOKEN", "tok-SECRET-1234")
	t.Setenv("AC_TEST_KEEP", "kept")
	l := New([]config.AgentConfig{{Name: "sh", Bin: "bash", Base: config.BaseInherit, Runtime: config.RuntimeHost}},
		fakeResolver{vals: map[string]string{}}, nil)
	l.SetDaemonSecretEnv("MY_LOGIN_PW", "GRAFANA_SA_TOKEN")
	_, env, err := l.Build(SpawnSpec{Agent: "sh", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	m := envMap(env)
	if v, ok := m["GRAFANA_SA_TOKEN"]; ok {
		t.Fatalf("inherit base leaked a token's value variable: %q", v)
	}
	if m["AC_TEST_KEEP"] != "kept" {
		t.Fatal("inherit base dropped an ordinary variable")
	}
}

func TestLauncherTokensForClones(t *testing.T) {
	l := New([]config.AgentConfig{
		{Name: "c", Bin: "claude", Runtime: config.RuntimeBox, Tokens: []string{"zeta", "grafana"}},
		{Name: "plain", Bin: "claude"},
		{Name: "bad", Bin: "claude", Runtime: config.RuntimeBox, Tokens: []string{"gone"}},
		{Name: "badpat", Bin: "claude", Runtime: config.RuntimeBox, GitHub: &config.GitHubBinding{Owners: []config.GitHubOwner{{Owner: "acme", Token: "gone2", Repos: []string{"r"}}}}},
	}, fakeResolver{}, nil)
	l.SetStaticTokens(tokenEntries())

	tokens, pats, err := l.TokensFor("c")
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 || tokens[0].Name != "grafana" || tokens[1].Name != "zeta" || pats != nil {
		t.Fatalf("tokens=%+v pats=%+v, want grafana then zeta (sorted), no pats", tokens, pats)
	}
	tokens[0].Env[0] = "MUTATED"
	tokens[0].ConfigEnv["GRAFANA_URL"] = "mutated"
	tokens[0].Egress[0].Host = "mutated"
	tokens[0].MCP.Args[0] = "mutated"
	tokens[0].Name = "mutated"
	again, _, _ := l.TokensFor("c")
	want := tokenEntries()[1]
	if !reflect.DeepEqual(again[0], want) {
		t.Fatalf("TokensFor must return deep copies, got %+v", again[0])
	}
	// SetStaticTokens clones too: editing the caller's slice changes nothing.
	src := tokenEntries()
	l.SetStaticTokens(src)
	src[1].Env[0] = "MUTATED"
	if got, _, _ := l.TokensFor("c"); got[0].Env[0] != "GRAFANA_SA_TOKEN" {
		t.Fatal("SetStaticTokens kept the caller's backing arrays")
	}

	if tk, p, err := l.TokensFor("plain"); tk != nil || p != nil || err != nil {
		t.Fatalf("unbound profile = %v %v %v", tk, p, err)
	}
	if tk, p, err := l.TokensFor("missing"); tk != nil || p != nil || err != nil {
		t.Fatalf("unknown profile = %v %v %v", tk, p, err)
	}
	_, _, err = l.TokensFor("bad")
	if err == nil || err.Error() != `profile "bad" names token entry "gone", which is not defined in static_tokens` {
		t.Fatalf("err = %v", err)
	}
	_, _, err = l.TokensFor("badpat")
	if err == nil || err.Error() != `profile "badpat" names token entry "gone2", which is not defined in static_tokens` {
		t.Fatalf("err = %v", err)
	}
}

// PAT entries come back deduped by name and sorted by name, whatever the
// owners' order, because Bindings.Hash trusts its caller for both.
func TestLauncherTokensForPATsDedupedAndSorted(t *testing.T) {
	g := &config.GitHubBinding{Owners: []config.GitHubOwner{
		{Owner: "b-org", Token: "gh-pat-b", Repos: []string{"r"}},
		{Owner: "app-org", App: "main", Repos: []string{"r"}},
		{Owner: "a-org", Token: "gh-pat-a", Repos: []string{"r"}},
		{Owner: "b2-org", Token: "gh-pat-b", Repos: []string{"r"}},
	}}
	l := New([]config.AgentConfig{{Name: "p", Bin: "claude", Runtime: config.RuntimeBox, GitHub: g,
		Tokens: []string{"grafana", "file-vals"}}}, fakeResolver{}, nil)
	l.SetStaticTokens(tokenEntries())
	tokens, pats, err := l.TokensFor("p")
	if err != nil {
		t.Fatal(err)
	}
	if len(pats) != 2 || pats[0].Name != "gh-pat-a" || pats[1].Name != "gh-pat-b" {
		t.Fatalf("pats = %+v", pats)
	}
	if len(tokens) != 2 || tokens[0].Name != "file-vals" || tokens[1].Name != "grafana" {
		t.Fatalf("tokens = %+v", tokens)
	}
}

func TestLauncherTokensForNoEntriesNoTokens(t *testing.T) {
	l := New([]config.AgentConfig{{Name: "plain", Bin: "claude"}}, fakeResolver{}, nil)
	if tk, p, err := l.TokensFor("plain"); tk != nil || p != nil || err != nil {
		t.Fatalf("got %v %v %v", tk, p, err)
	}
}
