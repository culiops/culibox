package session

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/launcher"
)

// tokLauncher adds static-token bindings to awsLauncher: profile "tok" binds
// the grafana entry, "pat" is a GitHub profile whose owner uses a PAT entry,
// "bad" has a TokensFor error. Its built env carries what a careless profile
// might set: the entry's own variables and a secret value.
type tokLauncher struct {
	awsLauncher
	tokens []config.StaticToken
	pats   []config.StaticToken
	err    error
}

func (l tokLauncher) Build(launcher.SpawnSpec) ([]string, []string, error) {
	return l.argv, []string{"TERM=xterm", "GRAFANA_URL=x", "GRAFANA_SA_TOKEN=tok-SECRET-1234", "KEEP_ME=1"}, nil
}

func (l tokLauncher) AWSFor(string) *config.AWSBinding { return nil }

func (l tokLauncher) GitHubFor(agent string) *config.GitHubBinding {
	if agent != "pat" {
		return nil
	}
	return &config.GitHubBinding{Owners: []config.GitHubOwner{{Owner: "acme", Token: "gh-pat-acme", Repos: []string{"api"}}}}
}

func (l tokLauncher) TokensFor(agent string) ([]config.StaticToken, []config.StaticToken, error) {
	switch agent {
	case "tok":
		return cloneTokens(l.tokens), nil, nil
	case "pat":
		return nil, cloneTokens(l.pats), nil
	case "bad":
		return nil, nil, l.err
	}
	return nil, nil, nil
}

func cloneTokens(in []config.StaticToken) []config.StaticToken {
	var out []config.StaticToken
	for _, e := range in {
		out = append(out, e.Clone())
	}
	return out
}

func grafanaEntry() config.StaticToken {
	return config.StaticToken{Name: "grafana-prod", Value: config.KeyRef{Type: "env", Var: "GRAFANA_SA_TOKEN"},
		Env: []string{"GRAFANA_SA_TOKEN"}, ConfigEnv: map[string]string{"GRAFANA_URL": "https://g.example.com"},
		Egress: []config.TokenEgress{{Host: "g.example.com"}}, Provider: "grafana", Label: "Grafana", Prod: true,
		MCP: &config.TokenMCP{Server: "grafana", Command: "mcp-grafana"}}
}

func patEntry() config.StaticToken {
	return config.StaticToken{Name: "gh-pat-acme", Value: config.KeyRef{Type: "file", Path: "/x/pat"}, Env: []string{"GH_TOKEN"}}
}

func tokLauncherFor(runtime string) tokLauncher {
	return tokLauncher{awsLauncher: awsLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: runtime}},
		tokens: []config.StaticToken{grafanaEntry()}, pats: []config.StaticToken{patEntry()},
		err: errors.New(`profile "bad" names token entry "gone", which is not defined in static_tokens`)}
}

func tokManager(t *testing.T, runtime string) (*Manager, *fakeBoxes, *fakeBrokerHooks, *fakeEgressHooks) {
	t.Helper()
	m := NewManager(tokLauncherFor(runtime), nil, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	hooks := &fakeBrokerHooks{}
	m.SetBroker(hooks)
	eg := &fakeEgressHooks{}
	m.SetEgress(eg)
	return m, boxes, hooks, eg
}

func TestSpawnTokensOnlyIsBound(t *testing.T) {
	m, boxes, hooks, eg := tokManager(t, config.RuntimeBox)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "tok", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })

	bs := config.Bindings{Tokens: []config.StaticToken{grafanaEntry()}}
	if boxes.lastKey.Binding != bs.Hash() || bs.Hash() == "" {
		t.Fatalf("box key binding = %q, want %q", boxes.lastKey.Binding, bs.Hash())
	}
	if len(hooks.lastBS.Tokens) != 1 || hooks.lastBS.AWS != nil || hooks.lastBS.GitHub != nil {
		t.Fatalf("BoxFor got %+v", hooks.lastBS)
	}
	env := boxes.lastEnv
	if got := envNamed(env, "AGENTBOX_BROKER_TOKEN"); !reflect.DeepEqual(got, []string{"AGENTBOX_BROKER_TOKEN=tok-" + s.ID + "." + fakeNonce}) {
		t.Fatalf("token env = %q", got)
	}
	if got := envNamed(env, "HTTPS_PROXY"); len(got) != 1 {
		t.Fatalf("egress env missing: %q", env)
	}
	if eg.spawnCalls() != 1 {
		t.Fatalf("egress SpawnEnv calls = %d", eg.spawnCalls())
	}
	// The entry's own variables are the broker's to deliver: a profile value is
	// stripped, and the secret never rides in the env.
	if got := envNamed(env, "GRAFANA_URL"); len(got) != 0 {
		t.Fatalf("GRAFANA_URL kept: %q", got)
	}
	if got := envNamed(env, "KEEP_ME"); len(got) != 1 {
		t.Fatalf("an unrelated variable was stripped: %q", env)
	}
	for _, kv := range env {
		if strings.Contains(kv, "tok-SECRET-1234") || strings.HasPrefix(kv, "GRAFANA_SA_TOKEN=") {
			t.Fatalf("a token value reached the env: %q", kv)
		}
	}
	if len(s.Tokens) != 1 || s.Tokens[0].Name != "grafana-prod" || len(s.PATs) != 0 {
		t.Fatalf("session Tokens=%+v PATs=%+v", s.Tokens, s.PATs)
	}
	if !s.Bindings().Any() || !reflect.DeepEqual(s.Bindings(), config.Bindings{Tokens: s.Tokens}) {
		t.Fatalf("Bindings() = %+v", s.Bindings())
	}
}

func TestSpawnPATOwnerIsBound(t *testing.T) {
	m, boxes, hooks, _ := tokManager(t, config.RuntimeBox)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "pat", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if len(hooks.lastBS.PATs) != 1 || hooks.lastBS.PATs[0].Name != "gh-pat-acme" || hooks.lastBS.GitHub == nil {
		t.Fatalf("BoxFor got %+v", hooks.lastBS)
	}
	if boxes.lastKey.Binding != hooks.lastBS.Hash() {
		t.Fatalf("box key binding = %q, want %q", boxes.lastKey.Binding, hooks.lastBS.Hash())
	}
	if len(s.PATs) != 1 || len(s.Tokens) != 0 {
		t.Fatalf("session Tokens=%+v PATs=%+v", s.Tokens, s.PATs)
	}
}

func TestSpawnTokensRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(m *Manager)
		spec  launcher.SpawnSpec
	}{
		{"host tier", func(*Manager) {}, launcher.SpawnSpec{Runtime: config.RuntimeHost}},
		{"no broker", func(m *Manager) { m.SetBroker(nil) }, launcher.SpawnSpec{}},
		{"no egress", func(m *Manager) { m.SetEgress(nil) }, launcher.SpawnSpec{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, boxes, hooks, _ := tokManager(t, config.RuntimeBox)
			c.setup(m)
			c.spec.Agent, c.spec.Cwd = "tok", t.TempDir()
			s, err := m.Spawn(c.spec)
			if err == nil {
				_ = m.Kill(s.ID)
				t.Fatal("a token-bound profile spawned")
			}
			if !strings.Contains(err.Error(), "a token binding") {
				t.Fatalf("err = %v, want it to name a token binding", err)
			}
			if n, tk, bf := hooks.calls(); n+tk+bf != 0 {
				t.Fatalf("refused spawn called the broker: nonce=%d token=%d boxFor=%d", n, tk, bf)
			}
			if boxes.prepares != 0 || boxes.wraps != 0 {
				t.Fatalf("refused spawn touched the box layer: prepares=%d wraps=%d", boxes.prepares, boxes.wraps)
			}
		})
	}
}

func TestSpawnTokensForErrorRefuses(t *testing.T) {
	m, boxes, _, _ := tokManager(t, config.RuntimeBox)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bad", Cwd: t.TempDir()})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("a profile naming an undefined entry spawned")
	}
	if !errors.Is(err, box.ErrBoxUnavailable) || !strings.Contains(err.Error(), `names token entry "gone"`) {
		t.Fatalf("err = %v", err)
	}
	if boxes.prepares != 0 || boxes.wraps != 0 {
		t.Fatalf("refused spawn touched the box layer")
	}
}

func TestBindingKindNamesTokens(t *testing.T) {
	aws, gh, tk := testBinding(), testGitHub(), []config.StaticToken{grafanaEntry()}
	cases := []struct {
		bs         config.Bindings
		kind, name string
	}{
		{config.Bindings{AWS: aws}, "an AWS binding", "AWS"},
		{config.Bindings{GitHub: gh}, "a GitHub binding", "GitHub"},
		{config.Bindings{Tokens: tk}, "a token binding", "token"},
		{config.Bindings{AWS: aws, GitHub: gh}, "AWS and GitHub bindings", "AWS and GitHub"},
		{config.Bindings{AWS: aws, Tokens: tk}, "AWS and token bindings", "AWS and token"},
		{config.Bindings{GitHub: gh, Tokens: tk}, "GitHub and token bindings", "GitHub and token"},
		{config.Bindings{AWS: aws, GitHub: gh, Tokens: tk}, "AWS, GitHub and token bindings", "AWS, GitHub and token"},
	}
	for _, c := range cases {
		if got := bindingKind(c.bs); got != c.kind {
			t.Errorf("bindingKind(%+v) = %q, want %q", c.bs, got, c.kind)
		}
		if got := bindingNames(c.bs); got != c.name {
			t.Errorf("bindingNames(%+v) = %q, want %q", c.bs, got, c.name)
		}
	}
}

func TestSidecarRoundTripsTokens(t *testing.T) {
	l := tokLauncherFor(config.RuntimeBox)
	path := filepath.Join(t.TempDir(), "sessions.json")
	fb := newFakeBackend()
	m := NewManager(l, fb, 1<<16, time.Minute)
	if err := m.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	m.SetBoxes(&fakeBoxes{})
	m.SetBroker(&fakeBrokerHooks{})
	m.SetEgress(&fakeEgressHooks{})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "tok", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	md := sidecarEntry(t, path, s.ID)
	if len(md.Tokens) != 1 || md.Tokens[0].Name != "grafana-prod" || md.BrokerNonce != fakeNonce {
		t.Fatalf("sidecar entry Tokens=%+v nonce=%q", md.Tokens, md.BrokerNonce)
	}
	raw, _ := json.Marshal(md)
	if !strings.Contains(string(raw), `"tokens":[`) || strings.Contains(string(raw), `"pats"`) {
		t.Fatalf("sidecar JSON = %s", raw)
	}

	fb.seedLive(s.ID)
	m2 := NewManager(l, fb, 1<<16, time.Minute)
	if err := m2.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	m2.SetBoxes(&fakeBoxes{})
	if err := m2.Reconcile(); err != nil {
		t.Fatal(err)
	}
	r, ok := m2.Get(s.ID)
	if !ok {
		t.Fatal("session not reattached")
	}
	t.Cleanup(func() { _ = m2.Kill(r.ID) })
	if !reflect.DeepEqual(r.Tokens, s.Tokens) || r.PATs != nil {
		t.Fatalf("restored Tokens=%+v PATs=%+v", r.Tokens, r.PATs)
	}
	if r.Info().Egress != "allowlist" || len(r.Info().Tokens) != 1 {
		t.Fatalf("restored Info = %+v", r.Info())
	}
}

// A record written before static tokens existed has no tokens/pats keys, and
// its wire shape does not grow them either.
func TestSidecarOldRecordHasNoTokens(t *testing.T) {
	var md sessionMeta
	old := `{"agent":"gh","created":"2026-10-01T00:00:00Z","runtime":"box","github":{"owners":[{"owner":"acme","app":"main","repos":["api"]}]},"aws_nonce":"n1"}`
	if err := json.Unmarshal([]byte(old), &md); err != nil {
		t.Fatal(err)
	}
	if md.Tokens != nil || md.PATs != nil || md.GitHub == nil {
		t.Fatalf("old record decoded to %+v", md)
	}
	raw, _ := json.Marshal(md)
	if strings.Contains(string(raw), `"tokens"`) || strings.Contains(string(raw), `"pats"`) {
		t.Fatalf("an old record re-encoded with token keys: %s", raw)
	}
}

func TestInfoTokens(t *testing.T) {
	m, _, _, _ := tokManager(t, config.RuntimeBox)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "tok", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	in := s.Info()
	want := []TokenInfo{{Name: "grafana-prod", Provider: "grafana", Label: "Grafana", Prod: true, MCPServer: "grafana"}}
	if !reflect.DeepEqual(in.Tokens, want) {
		t.Fatalf("Info.Tokens = %+v, want %+v", in.Tokens, want)
	}
	if in.Egress != "allowlist" {
		t.Fatalf("Info.Egress = %q", in.Egress)
	}
	b, _ := json.Marshal(in)
	for _, bad := range []string{"GRAFANA_SA_TOKEN", "tok-SECRET", `"value"`, fakeNonce, "g.example.com"} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("Info leaks %q: %s", bad, b)
		}
	}
	if !strings.Contains(string(b), `"tokens":[{"name":"grafana-prod","provider":"grafana","label":"Grafana","prod":true,"mcp_server":"grafana"}]`) {
		t.Fatalf("Info JSON = %s", b)
	}
}

// Without tokens, Info's wire shape does not change.
func TestInfoWithoutTokensHasNoKey(t *testing.T) {
	m, _, _, _ := ghManager(t, config.RuntimeBox)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "gh", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	b, _ := json.Marshal(s.Info())
	if strings.Contains(string(b), `"tokens"`) {
		t.Fatalf("Info grew a tokens key: %s", b)
	}
}
