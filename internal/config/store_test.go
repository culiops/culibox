package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestKeyRefStore(t *testing.T) {
	if err := (KeyRef{Type: "store", ID: "gh-key"}).Validate(); err != nil {
		t.Fatalf("valid store ref: %v", err)
	}
	for _, r := range []KeyRef{
		{Type: "store"},
		{Type: "store", ID: "x", Path: "/a"},
		{Type: "store", ID: "x", Var: "V"},
		{Type: "store", ID: "-bad"},
		{Type: "store", ID: "has space"},
		{Type: "store", ID: strings.Repeat("a", 65)},
		{Type: "file", Path: "/a", ID: "x"},
		{Type: "env", Var: "X", ID: "x"},
	} {
		if err := r.Validate(); err == nil {
			t.Errorf("%+v: want error", r)
		}
	}
	if got := (KeyRef{Type: "store", ID: "gh-key"}).String(); got != "store gh-key" {
		t.Errorf("String = %q", got)
	}
	err := (KeyRef{Type: "x"}).Validate()
	if err == nil || err.Error() != `unsupported reference type "x" (want "file", "env" or "store")` {
		t.Errorf("unknown type message: %v", err)
	}
}

func TestGitHubAppPrivateKeyTypes(t *testing.T) {
	app := func(k KeyRef) []GitHubApp {
		return []GitHubApp{{Name: "a", ClientID: "cid", PrivateKey: k}}
	}
	for _, k := range []KeyRef{{Type: "file", Path: "/k.pem"}, {Type: "store", ID: "gh-key"}} {
		if err := ValidateGitHubApps(app(k)); err != nil {
			t.Errorf("%+v: %v", k, err)
		}
	}
	err := ValidateGitHubApps(app(KeyRef{Type: "env", Var: "X"}))
	if err == nil || err.Error() != `github_apps[0].private_key.type "env" is not supported (want "file" or "store")` {
		t.Errorf("env message: %v", err)
	}
	if err := ValidateGitHubApps(app(KeyRef{Type: "store"})); err == nil {
		t.Error("store without id must fail")
	}
	if err := ValidateGitHubApps(app(KeyRef{Type: "file", Path: "rel"})); err == nil {
		t.Error("relative file path must fail")
	}
}

func TestStoreUnlockTTL(t *testing.T) {
	var c Config
	if got := c.StoreUnlockTTL(); got != 168*time.Hour {
		t.Errorf("default = %v", got)
	}
	c.Store.UnlockTTL = "24h"
	if got := c.StoreUnlockTTL(); got != 24*time.Hour {
		t.Errorf("24h = %v", got)
	}
	for _, v := range []string{"1h", "720h", "168h"} {
		if err := validStore(StoreConfig{UnlockTTL: v}); err != nil {
			t.Errorf("%s: %v", v, err)
		}
	}
	for _, v := range []string{"30m", "721h", "abc", "-5h", "0"} {
		if err := validStore(StoreConfig{UnlockTTL: v}); err == nil {
			t.Errorf("%q: want error", v)
		}
	}
	if err := validStore(StoreConfig{}); err != nil {
		t.Errorf("empty: %v", err)
	}
}

func TestConfigValidateStoreTTL(t *testing.T) {
	c := Config{Agents: []AgentConfig{{Name: "a", Bin: "/bin/true"}}, Store: StoreConfig{UnlockTTL: "30m"}}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "store.unlock_ttl") {
		t.Fatalf("Validate = %v", err)
	}
}

func TestStoreOmittedFromJSONWhenZero(t *testing.T) {
	b, err := json.Marshal(Config{Agents: []AgentConfig{{Name: "a", Bin: "b"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "store") {
		t.Fatalf("zero Store must not appear: %s", b)
	}
	var c Config
	if err := json.Unmarshal([]byte(`{"store":{"unlock_ttl":"24h"},"agents":[]}`), &c); err != nil || c.StoreUnlockTTL() != 24*time.Hour {
		t.Fatalf("parse: %v %v", err, c.Store)
	}
}

func TestSecretSourceStore(t *testing.T) {
	base := func(ss ...SecretSource) Config {
		return Config{BindAddr: "127.0.0.1:8080", SecretSources: ss,
			Agents: []AgentConfig{{Name: "a", Bin: "claude"}}}
	}
	if err := base(SecretSource{Type: "store"}, SecretSource{Type: "env"}).Validate(); err != nil {
		t.Fatalf("store source: %v", err)
	}
	if err := base(SecretSource{Type: "store", Path: "/x"}).Validate(); err == nil {
		t.Error("store with a path must be refused")
	}
	err := base(SecretSource{Type: "store"}, SecretSource{Type: "store"}).Validate()
	if err == nil || err.Error() != "config: at most one store secret_source" {
		t.Errorf("two stores: %v", err)
	}
}
