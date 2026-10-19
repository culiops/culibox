package web

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/session"
)

func TestMetaStaticTokensNoReferences(t *testing.T) {
	pm := NewProfileManager([]config.AgentConfig{{Name: "zsh", Bin: "/usr/bin/zsh"}}, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	pm.SetStaticTokens([]config.StaticToken{
		{Name: "sentry-corp", Value: config.KeyRef{Type: "env", Var: "SENTRY_SECRET_VAR"},
			Env: []string{"SENTRY_ACCESS_TOKEN"}, ConfigEnv: map[string]string{"SENTRY_HOST": "sentry.corp.example", "A_KEY": "cfg-value-1"},
			Egress: []config.TokenEgress{{Host: "sentry.corp.example"}, {Host: "grafana.corp.example", Port: 8443, Private: true, CIDR: "10.0.0.0/8"}},
			MCP:    &config.TokenMCP{Server: "sentry", Command: "npx"}, Provider: "sentry", Label: "Sentry", Prod: true, Description: "corp sentry"},
		{Name: "file-tok", Value: config.KeyRef{Type: "file", Path: "/home/x/secrets/tok.txt"}, Env: []string{"GH_TOKEN"}},
	})
	s := &Server{AllowedHosts: []string{"example.com"}, Profiles: pm}
	got := metaGet(t, s)
	list, _ := got["static_tokens"].([]any)
	if len(list) != 2 {
		t.Fatalf("static_tokens = %v, want 2 entries", got["static_tokens"])
	}
	first, _ := list[0].(map[string]any)
	if first["name"] != "sentry-corp" || first["provider"] != "sentry" || first["prod"] != true || first["mcp_server"] != "sentry" {
		t.Fatalf("first entry = %v", first)
	}
	keys, _ := first["config_env_keys"].([]any)
	if len(keys) != 2 || keys[0] != "A_KEY" || keys[1] != "SENTRY_HOST" {
		t.Fatalf("config_env_keys = %v, want the sorted keys", first["config_env_keys"])
	}
	hosts, _ := first["hosts"].([]any)
	if len(hosts) != 2 {
		t.Fatalf("hosts = %v", first["hosts"])
	}
	h1, _ := hosts[1].(map[string]any)
	if h1["host"] != "grafana.corp.example" || h1["port"] != float64(8443) || h1["private"] != true {
		t.Fatalf("host 1 = %v", h1)
	}
	raw, _ := json.Marshal(got)
	for _, banned := range []string{"SENTRY_SECRET_VAR", "/home/x/secrets", "tok.txt", "cfg-value-1", "10.0.0.0", "\"value\"", "\"var\"", "\"path\""} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("the bootstrap payload carries %q: %s", banned, raw)
		}
	}

	// none defined: [] on the wire, never null
	got = metaGet(t, &Server{AllowedHosts: []string{"example.com"}, Profiles: NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})})
	if l, ok := got["static_tokens"].([]any); !ok || len(l) != 0 {
		t.Fatalf("static_tokens with none defined = %v, want []", got["static_tokens"])
	}
}

func tokenInfoAdapter(t *testing.T, info map[string]session.Info) (AWSStatusAdapter, *broker.Audit) {
	t.Helper()
	a, err := broker.OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return AWSStatusAdapter{
		Get:    func(id string) (session.Info, bool) { i, ok := info[id]; return i, ok },
		Stats:  func(string) (broker.Stats, bool) { return broker.Stats{}, false },
		Recent: func(id string, n int) ([]broker.Row, error) { return a.ForSessionExcept(id, broker.ProviderStatic, n) },
		RecentStatic: func(id string, n int) ([]broker.Row, error) {
			return a.ForSessionProvider(id, broker.ProviderStatic, n)
		},
	}, a
}

func TestAWSStatusTokensOnlySession(t *testing.T) {
	info := map[string]session.Info{"claude-5": {ID: "claude-5", Tokens: []session.TokenInfo{{Name: "sentry-corp", Provider: "sentry"}}}}
	ad, _ := tokenInfoAdapter(t, info)
	st, ok := ad.AWSStatus("claude-5")
	if !ok {
		t.Fatal("a session bound only to tokens must have a status")
	}
	out := st.(map[string]any)
	if _, ok := out["tokens"]; !ok {
		t.Fatalf("no tokens key: %v", out)
	}
	rows, ok := out["static_recent"].([]broker.Row)
	if !ok || rows == nil {
		t.Fatalf("static_recent = %#v, want a non-nil list", out["static_recent"])
	}
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), `"static_recent":[]`) {
		t.Fatalf("static_recent must be [] on the wire: %s", raw)
	}
	// an unbound session stays a miss
	if _, ok := ad.AWSStatus("claude-9"); ok {
		t.Fatal("unknown session reported a status")
	}
}

func TestAWSStatusStaticRowsDoNotCrowdOut(t *testing.T) {
	info := map[string]session.Info{"claude-6": {ID: "claude-6",
		AWS:    &session.AWSInfo{Label: "sandbox", AccountID: "123456789012"},
		Tokens: []session.TokenInfo{{Name: "sentry-corp"}}}}
	ad, a := tokenInfoAdapter(t, info)
	for i := 0; i < 30; i++ {
		if err := a.Record(broker.Row{Time: time.Unix(int64(1000+i), 0), SessionID: "claude-6", Provider: broker.ProviderStatic,
			Subject: "sentry-corp", Outcome: broker.OutcomeFetched}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if err := a.Record(broker.Row{Time: time.Unix(int64(10+i), 0), SessionID: "claude-6", Outcome: broker.OutcomeMinted}); err != nil {
			t.Fatal(err)
		}
	}
	st, _ := ad.AWSStatus("claude-6")
	out := st.(map[string]any)
	recent, _ := out["recent"].([]broker.Row)
	static, _ := out["static_recent"].([]broker.Row)
	if len(recent) != 3 {
		t.Fatalf("recent has %d rows, want the 3 aws rows", len(recent))
	}
	for _, r := range recent {
		if r.Provider == broker.ProviderStatic {
			t.Fatalf("a static row leaked into recent: %+v", r)
		}
	}
	if len(static) != 5 {
		t.Fatalf("static_recent has %d rows, want 5", len(static))
	}
}
