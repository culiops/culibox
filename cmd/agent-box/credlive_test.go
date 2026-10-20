package main

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/ghapp"
	"github.com/culiops/agent-box/internal/launcher"
	"github.com/culiops/agent-box/internal/store"
	"github.com/culiops/agent-box/internal/web"
)

type noResolver struct{}

func (noResolver) Resolve(string) (string, bool, error) { return "", false, nil }

func liveFixture(t *testing.T) *credLive {
	t.Helper()
	agents := []config.AgentConfig{{Name: "sh", Bin: "bash", Base: config.BaseInherit, Runtime: config.RuntimeHost, Tokens: []string{"b"}}}
	lnch := launcher.New(agents, noResolver{}, nil)
	st, err := store.Open(filepath.Join(t.TempDir(), "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	pm := web.NewProfileManager(agents, nil, "", nil, st, lnch.SetAgents)
	return &credLive{lnch: lnch, pm: pm, gh: ghapp.NewMinter(nil, ghapp.Client{}), tokens: &tokenSet{}, passwordEnv: "AB_TEST_PW"}
}

func tok(name string) config.StaticToken {
	return config.StaticToken{Name: name, Value: config.KeyRef{Type: "env", Var: strings.ToUpper(name) + "_TOKEN"}}
}

func TestApplyUpdatesDefinedTokens(t *testing.T) {
	c := liveFixture(t)
	keep := definedTokens(c.tokens)
	in := []config.StaticToken{{Name: "a"}, {Name: "b"}}

	c.Apply(config.Config{StaticTokens: []config.StaticToken{tok("a")}})
	if got := keep(in); len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("after first Apply: %+v", got)
	}
	c.Apply(config.Config{StaticTokens: []config.StaticToken{tok("a"), tok("b")}})
	if got := keep(in); len(got) != 2 || got[1].Name != "b" {
		t.Fatalf("after second Apply the session's b must be kept: %+v", got)
	}
	c.Apply(config.Config{})
	if keep(in) != nil {
		t.Fatal("a deleted entry must drop from the session view")
	}
}

func TestApplyRedrivesDaemonSecretEnv(t *testing.T) {
	t.Setenv("FOO_TOKEN", "hunter2")
	t.Setenv("AB_TEST_PW", "pw")
	t.Setenv("AC_KEEP", "kept")
	c := liveFixture(t)
	build := func() map[string]string {
		t.Helper()
		_, env, err := c.lnch.Build(launcher.SpawnSpec{Agent: "sh", Runtime: config.RuntimeHost})
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		for _, kv := range env {
			if k, v, ok := strings.Cut(kv, "="); ok {
				m[k] = v
			}
		}
		return m
	}

	c.Apply(config.Config{})
	if build()["FOO_TOKEN"] != "hunter2" {
		t.Fatal("precondition: a variable no entry names is inherited")
	}
	c.Apply(config.Config{StaticTokens: []config.StaticToken{
		{Name: "foo", Value: config.KeyRef{Type: "env", Var: "FOO_TOKEN"}}}})
	m := build()
	if _, leaked := m["FOO_TOKEN"]; leaked {
		t.Fatal("a token entry's env var reached a session after Apply")
	}
	if _, leaked := m["AB_TEST_PW"]; leaked || m["AC_KEEP"] != "kept" {
		t.Fatalf("password env must stay stripped and ordinary vars kept: %v", m)
	}
	// Deleting the entry stops scrubbing a variable that is no longer a secret.
	c.Apply(config.Config{})
	if build()["FOO_TOKEN"] != "hunter2" {
		t.Fatal("scrub list must follow the current definitions")
	}
	if _, leaked := build()["AB_TEST_PW"]; leaked {
		t.Fatal("password env must stay stripped after every Apply")
	}
}

func TestApplyStoreInUse(t *testing.T) {
	c := liveFixture(t)
	c.Apply(config.Config{StaticTokens: []config.StaticToken{tok("a")}})
	if c.InUse() {
		t.Fatal("no store refs: InUse must be false")
	}
	c.Apply(config.Config{StaticTokens: []config.StaticToken{
		{Name: "a", Value: config.KeyRef{Type: "store", ID: "a"}}}})
	if !c.InUse() {
		t.Fatal("a store-backed token: InUse must be true")
	}
	c.Apply(config.Config{})
	if c.InUse() {
		t.Fatal("InUse must fall back to false")
	}
}

func TestApplyPushesLauncherProfilesAndMinter(t *testing.T) {
	c := liveFixture(t)
	if _, _, err := c.lnch.TokensFor("sh"); err == nil {
		t.Fatal("precondition: profile names token b, which is not defined yet")
	}
	if err := c.gh.Check("gh1"); err == nil {
		t.Fatal("precondition: App gh1 unknown")
	}
	conn := config.AWSConnection{Name: "sbx"}
	c.Apply(config.Config{
		StaticTokens:   []config.StaticToken{tok("b")},
		GitHubApps:     []config.GitHubApp{{Name: "gh1", ClientID: "Iv1.x", PrivateKey: config.KeyRef{Type: "file", Path: "/nonexistent"}}},
		AWSConnections: []config.AWSConnection{conn},
	})
	if toks, _, err := c.lnch.TokensFor("sh"); err != nil || len(toks) != 1 || toks[0].Name != "b" {
		t.Fatalf("launcher tokens: %v %v", toks, err)
	}
	if names := c.pm.GitHubAppNames(); len(names) != 1 || names[0] != "gh1" {
		t.Fatalf("profile manager apps: %v", names)
	}
	if conns := c.pm.AWSConnectionMetaList(); len(conns) != 1 || conns[0].Name != "sbx" {
		t.Fatalf("profile manager connections: %v", conns)
	}
	// The minter now knows gh1 (its key file is missing, which is a different error).
	if err := c.gh.Check("gh1"); err == nil || strings.Contains(err.Error(), "unknown GitHub App") {
		t.Fatalf("minter did not learn gh1: %v", err)
	}
}

func TestApplyToleratesMissingHolders(t *testing.T) {
	c := liveFixture(t)
	c.pm, c.gh, c.tokens = nil, nil, nil // boot calls Apply before the broker and profile manager exist
	c.Apply(config.Config{StaticTokens: []config.StaticToken{tok("a")}})
	if definedTokens(c.tokens)([]config.StaticToken{{Name: "a"}}) != nil {
		t.Fatal("a nil token set keeps nothing")
	}
}

// Apply runs from a web save while spawns and broker lookups are in flight.
func TestApplyConcurrentWithReaders(t *testing.T) {
	c := liveFixture(t)
	keep := definedTokens(c.tokens)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if i%2 == 0 {
					c.Apply(config.Config{StaticTokens: []config.StaticToken{tok("a"), tok("b")}})
					c.Apply(config.Config{StaticTokens: []config.StaticToken{tok("b")}})
				} else {
					_ = keep([]config.StaticToken{{Name: "a"}, {Name: "b"}})
					_, _, _ = c.lnch.TokensFor("sh")
					_ = c.InUse()
					_ = c.pm.AWSConnectionMetaList()
				}
			}
		}(i)
	}
	wg.Wait()
}
