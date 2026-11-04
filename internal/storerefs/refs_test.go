package storerefs

import (
	"reflect"
	"testing"

	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/secretstore"
)

func fullConfig() config.Config {
	return config.Config{
		GitHubApps: []config.GitHubApp{
			{Name: "ci", PrivateKey: config.KeyRef{Type: "store", ID: "gh-ci-key"}},
			{Name: "other", PrivateKey: config.KeyRef{Type: "file", Path: "/x/key.pem"}},
		},
		StaticTokens: []config.StaticToken{
			{Name: "grafana", Value: config.KeyRef{Type: "store", ID: "grafana-tok"}},
			{Name: "envtok", Value: config.KeyRef{Type: "env", Var: "X"}},
		},
		SecretSources: []config.SecretSource{{Type: "store"}, {Type: "env"}},
		Agents: []config.AgentConfig{
			{Name: "claude", Secrets: []string{"CLAUDE_CODE_OAUTH_TOKEN"}},
			{Name: "plain"},
		},
	}
}

func TestRefsAllFour(t *testing.T) {
	accts := []account.Account{{Name: "work", Provider: "claude", TokenSecret: "WORK_TOKEN"}, {Name: "none", Provider: "claude"}}
	got := Refs(fullConfig(), accts)
	want := map[string][]string{
		"gh-ci-key":               {"github_apps/ci"},
		"grafana-tok":             {"static_tokens/grafana"},
		"CLAUDE_CODE_OAUTH_TOKEN": {"agents/claude.secrets"},
		"WORK_TOKEN":              {"accounts/work.token_secret"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Refs = %v, want %v", got, want)
	}
}

func TestRefsSharedIDSorted(t *testing.T) {
	cfg := fullConfig()
	cfg.Agents = append(cfg.Agents, config.AgentConfig{Name: "a2", Secrets: []string{"CLAUDE_CODE_OAUTH_TOKEN"}})
	accts := []account.Account{{Name: "w", Provider: "claude", TokenSecret: "CLAUDE_CODE_OAUTH_TOKEN"}}
	got := Refs(cfg, accts)["CLAUDE_CODE_OAUTH_TOKEN"]
	want := []string{"accounts/w.token_secret", "agents/a2.secrets", "agents/claude.secrets"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("refs = %v, want %v", got, want)
	}
}

func TestRefsSecretNamesOnlyWithStoreSource(t *testing.T) {
	cfg := fullConfig()
	cfg.SecretSources = []config.SecretSource{{Type: "env"}}
	accts := []account.Account{{Name: "work", Provider: "claude", TokenSecret: "WORK_TOKEN"}}
	got := Refs(cfg, accts)
	if _, ok := got["CLAUDE_CODE_OAUTH_TOKEN"]; ok {
		t.Fatalf("agent secret mapped without a store source: %v", got)
	}
	if _, ok := got["WORK_TOKEN"]; ok {
		t.Fatalf("account token mapped without a store source: %v", got)
	}
	if len(got) != 2 {
		t.Fatalf("Refs = %v, want only the two explicit store refs", got)
	}
}

func TestInUse(t *testing.T) {
	if InUse(config.Config{SecretSources: []config.SecretSource{{Type: "env"}}}) {
		t.Fatal("InUse true for a config with no store")
	}
	if !InUse(fullConfig()) {
		t.Fatal("InUse false for a store config")
	}
	onlyGH := config.Config{GitHubApps: []config.GitHubApp{{Name: "a", PrivateKey: config.KeyRef{Type: "store", ID: "k"}}}}
	if !InUse(onlyGH) {
		t.Fatal("InUse false for a store-backed GitHub key")
	}
	onlyTok := config.Config{StaticTokens: []config.StaticToken{{Name: "a", Value: config.KeyRef{Type: "store", ID: "k"}}}}
	if !InUse(onlyTok) {
		t.Fatal("InUse false for a store-backed token")
	}
	if !InUse(config.Config{SecretSources: []config.SecretSource{{Type: "store"}}}) {
		t.Fatal("InUse false for a store secret_source")
	}
	if InUse(config.Config{}) {
		t.Fatal("InUse true for the zero config")
	}
}

// Claims counts secret names as store ids without a store source too: a value
// written under one today becomes that definition's value once a source is added.
func TestClaimsCountNamesWithoutStoreSource(t *testing.T) {
	cfg := fullConfig()
	cfg.SecretSources = []config.SecretSource{{Type: "env"}}
	accts := []account.Account{{Name: "work", Provider: "claude", TokenSecret: "WORK_TOKEN"}}
	got := Claims(cfg, accts)
	want := map[string][]string{
		"gh-ci-key":               {"github_apps/ci"},
		"grafana-tok":             {"static_tokens/grafana"},
		"CLAUDE_CODE_OAUTH_TOKEN": {"agents/claude.secrets"},
		"WORK_TOKEN":              {"accounts/work.token_secret"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Claims = %v, want %v", got, want)
	}
	cfg.SecretSources = []config.SecretSource{{Type: "store"}}
	if s := Claims(cfg, accts); !reflect.DeepEqual(s, Refs(cfg, accts)) {
		t.Fatalf("with a store source Claims = %v, Refs = %v", s, Refs(cfg, accts))
	}
}

// An account's own pasted token (account.TokenID) is read straight from the
// store even without a store secret_source, so it is a store reference then
// too: the value cannot be removed while the account still uses it.
func TestRefsOwnAccountTokenWithoutStoreSource(t *testing.T) {
	cfg := config.Config{SecretSources: []config.SecretSource{{Type: "env"}}}
	accts := []account.Account{
		{Name: "client-a", Provider: "claude", TokenSecret: "ACCT_CLIENT_A_TOKEN"},
		{Name: "work", Provider: "claude", TokenSecret: "ACCT_OTHER_TOKEN"}, // not its own id
	}
	got := Refs(cfg, accts)
	want := map[string][]string{"ACCT_CLIENT_A_TOKEN": {"accounts/client-a.token_secret"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Refs = %v, want %v", got, want)
	}
}

type fakeIDs []string

func (f fakeIDs) List() ([]secretstore.Entry, error) {
	out := make([]secretstore.Entry, len(f))
	for i, id := range f {
		out[i] = secretstore.Entry{ID: id}
	}
	return out, nil
}

type failIDs struct{}

func (failIDs) List() ([]secretstore.Entry, error) { return nil, secretstore.ErrNotInitialized }

func TestAccountTokensStored(t *testing.T) {
	own := []account.Account{{Name: "work", Provider: "claude", TokenSecret: "ACCT_WORK_TOKEN"}}
	if !AccountTokensStored(own, fakeIDs{"x", "ACCT_WORK_TOKEN"}) {
		t.Fatal("a stored own token must count")
	}
	if AccountTokensStored(own, fakeIDs{"x"}) {
		t.Fatal("an own token not in the store must not count")
	}
	foreign := []account.Account{{Name: "work", Provider: "claude", TokenSecret: "GH_KEY"}}
	if AccountTokensStored(foreign, fakeIDs{"GH_KEY"}) {
		t.Fatal("a foreign token_secret is not the store fallback's business")
	}
	if AccountTokensStored(own, failIDs{}) || AccountTokensStored(nil, fakeIDs{"ACCT_WORK_TOKEN"}) {
		t.Fatal("no store or no accounts: false")
	}
}
