package session

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/bundle"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

// mcpLauncher is tokLauncher with an argv that carries the spawn's extra args
// (what the real launcher's Build does), a settable bin and account.
type mcpLauncher struct {
	tokLauncher
	bin  string
	acct account.Resolved
}

func (l mcpLauncher) Build(spec launcher.SpawnSpec) ([]string, []string, error) {
	return append(slices.Clone(l.argv), spec.ExtraArgs...), []string{"TERM=xterm"}, nil
}

func (l mcpLauncher) BinFor(launcher.SpawnSpec) (string, error) { return l.bin, nil }

func (l mcpLauncher) AccountFor(launcher.SpawnSpec) (account.Resolved, error) { return l.acct, nil }

// bundleFlags is what the real claude layout hands the agent.
var bundleFlags = []string{"--mcp-config", "/opt/agent-bundle/mcp.json", "--settings", "/opt/agent-bundle/settings.json",
	"--setting-sources", "user", "--strict-mcp-config"}

func sentryEntry() config.StaticToken {
	return config.StaticToken{Name: "sentry", Value: config.KeyRef{Type: "env", Var: "SENTRY_TOKEN"}, Env: []string{"SENTRY_ACCESS_TOKEN"},
		Egress: []config.TokenEgress{{Host: "sentry.io"}},
		MCP:    &config.TokenMCP{Server: "sentry", Command: "mcp-server-sentry", Extra: []string{"llm-key"}}}
}

func mcpManager(t *testing.T, bin string, acct account.Resolved, tokens ...config.StaticToken) (*Manager, *fakeBoxes, *fakeBrokerHooks) {
	t.Helper()
	l := mcpLauncher{tokLauncher: tokLauncherFor(config.RuntimeBox), bin: bin, acct: acct}
	l.tokens = tokens
	m := NewManager(l, nil, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	hooks := &fakeBrokerHooks{}
	m.SetBroker(hooks)
	m.SetEgress(&fakeEgressHooks{})
	return m, boxes, hooks
}

func spawnTok(t *testing.T, m *Manager) *Session {
	t.Helper()
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "tok", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	return s
}

// argAfter is the value following flag in argv, "" when absent.
func argAfter(argv []string, flag string) string {
	if i := slices.Index(argv, flag); i >= 0 && i+1 < len(argv) {
		return argv[i+1]
	}
	return ""
}

func TestSpawnTokenMCPFlags(t *testing.T) {
	t.Run("default account: replaces the bundle's path and keeps the rest", func(t *testing.T) {
		m, boxes, hooks := mcpManager(t, "claude", account.DefaultResolved(""), grafanaEntry())
		b := &fakeBundles{flags: bundleFlags}
		m.SetBundles(b)
		spawnTok(t, m)
		want := []string{"--mcp-config", "/opt/agent-session-mcp/mcp-deadbeef-u.json", "--settings", "/opt/agent-bundle/settings.json",
			"--setting-sources", "user", "--strict-mcp-config"}
		if got := boxes.lastArgv[len(boxes.lastArgv)-len(want):]; !slices.Equal(got, want) {
			t.Fatalf("argv tail = %q, want %q", got, want)
		}
		if slices.Contains(boxes.lastArgv, "/opt/agent-bundle/mcp.json") {
			t.Fatalf("the shared bundle path is still in argv: %q", boxes.lastArgv)
		}
		if !b.smcpIncludeUser || b.smcpCalls != 1 || len(b.smcpEntries) != 1 {
			t.Fatalf("SessionMCP asked %+v", b)
		}
		e := b.smcpEntries[0]
		if e.Server != "grafana" || e.Entry != "grafana-prod" || e.Command != "mcp-grafana" ||
			!slices.Equal(e.Args, []string{"grafana-prod", "--", "mcp-grafana"}) {
			t.Fatalf("entry server = %+v", e)
		}
		// The document names the user's servers, so it goes to the bundle layer's
		// own directory, never the binding dir that every account's box mounts.
		bk := config.Bindings{Tokens: []config.StaticToken{grafanaEntry()}}.Hash()
		if !slices.Equal(b.writeKeys, []string{bk + "/deadbeef-u"}) || len(hooks.mcpKeys) != 0 {
			t.Fatalf("bundle writes = %q, binding-dir writes = %q", b.writeKeys, hooks.mcpKeys)
		}
		if !slices.Equal(b.mountKeys, []string{bk}) {
			t.Fatalf("mount asks = %q", b.mountKeys)
		}
		var mounted bool
		for _, mt := range boxes.lastRO {
			if mt.Box == bundle.BoxSessionMCPDir && mt.Host == "/b/session-mcp/"+bk && !mt.Optional {
				mounted = true
			}
		}
		if !mounted {
			t.Fatalf("the personal-config dir is not mounted: %+v", boxes.lastRO)
		}
		// The bundle's own flags slice must never be edited in place.
		if bundleFlags[1] != "/opt/agent-bundle/mcp.json" {
			t.Fatalf("the shared flags were modified: %q", bundleFlags)
		}
	})

	t.Run("an account that inherits its personal config", func(t *testing.T) {
		m, boxes, _ := mcpManager(t, "claude", workAccount(true), grafanaEntry())
		b := &fakeBundles{flags: bundleFlags}
		m.SetBundles(b)
		spawnTok(t, m)
		if got := argAfter(boxes.lastArgv, "--mcp-config"); got != "/opt/agent-session-mcp/mcp-deadbeef-u.json" || !b.smcpIncludeUser {
			t.Fatalf("--mcp-config %q includeUser=%v", got, b.smcpIncludeUser)
		}
	})

	t.Run("non-inheriting account: entry and image servers, confined", func(t *testing.T) {
		m, boxes, hooks := mcpManager(t, "claude", workAccount(false), grafanaEntry())
		b := &fakeBundles{flags: bundleFlags}
		m.SetBundles(b)
		spawnTok(t, m)
		if b.calls != 0 {
			t.Fatal("the personal bundle was consulted for a non-inheriting account")
		}
		want := []string{"--mcp-config", "/opt/agentbox/mcp-deadbeef-i.json", "--strict-mcp-config"}
		if got := boxes.lastArgv[len(boxes.lastArgv)-3:]; !slices.Equal(got, want) {
			t.Fatalf("argv tail = %q, want %q", got, want)
		}
		if b.smcpIncludeUser {
			t.Fatal("includeUser must be false")
		}
		// Not personal: the binding dir, and no personal-config mount at all.
		if len(hooks.mcpKeys) != 1 || hooks.mcpKeys[0] != "deadbeef-i" || len(b.writeKeys) != 0 || len(b.mountKeys) != 0 {
			t.Fatalf("binding-dir writes = %q, bundle writes = %q, mounts = %q", hooks.mcpKeys, b.writeKeys, b.mountKeys)
		}
		for _, mt := range boxes.lastRO {
			if mt.Box == bundle.BoxSessionMCPDir {
				t.Fatalf("a non-inheriting box mounts the personal config dir: %+v", mt)
			}
		}
		if slices.Contains(boxes.lastArgv, "--settings") {
			t.Fatalf("a non-inheriting account got the bundle's settings: %q", boxes.lastArgv)
		}
	})

	t.Run("a shell gets no flags", func(t *testing.T) {
		m, boxes, hooks := mcpManager(t, "bash", account.DefaultResolved(""), grafanaEntry())
		b := &fakeBundles{}
		m.SetBundles(b)
		spawnTok(t, m)
		// The box is shared with claude sessions, so it carries the same mount,
		// optional for a shell, and nothing is written.
		var shellMount *box.Mount
		for i := range boxes.lastRO {
			if boxes.lastRO[i].Box == bundle.BoxSessionMCPDir {
				shellMount = &boxes.lastRO[i]
			}
		}
		if shellMount == nil || !shellMount.Optional || len(b.writeKeys) != 0 {
			t.Fatalf("shell mount = %+v, writes = %q", shellMount, b.writeKeys)
		}
		if slices.Contains(boxes.lastArgv, "--mcp-config") || slices.Contains(boxes.lastArgv, "--strict-mcp-config") {
			t.Fatalf("a shell got MCP flags: %q", boxes.lastArgv)
		}
		if b.smcpCalls != 0 || len(hooks.mcpKeys) != 0 {
			t.Fatal("a shell session wrote an MCP config")
		}
	})

	t.Run("no entry declares a server: flags are the bundle's, byte for byte", func(t *testing.T) {
		plain := grafanaEntry()
		plain.MCP = nil
		m, boxes, hooks := mcpManager(t, "claude", account.DefaultResolved(""), plain)
		b := &fakeBundles{flags: bundleFlags}
		m.SetBundles(b)
		spawnTok(t, m)
		if got := boxes.lastArgv[len(boxes.lastArgv)-len(bundleFlags):]; !slices.Equal(got, bundleFlags) {
			t.Fatalf("argv tail = %q", got)
		}
		if b.smcpCalls != 0 || len(hooks.mcpKeys) != 0 {
			t.Fatal("an MCP config was written with nothing to add")
		}
	})

	t.Run("a session without tokens is untouched", func(t *testing.T) {
		m, boxes, hooks := mcpManager(t, "claude", account.DefaultResolved(""))
		b := &fakeBundles{flags: bundleFlags}
		m.SetBundles(b)
		s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = m.Kill(s.ID) })
		if got := boxes.lastArgv[len(boxes.lastArgv)-len(bundleFlags):]; !slices.Equal(got, bundleFlags) {
			t.Fatalf("argv tail = %q", got)
		}
		if b.smcpCalls != 0 || len(hooks.mcpKeys) != 0 {
			t.Fatal("an unbound session asked for an MCP config")
		}
	})
}

// With the bundle off, or failing, the entries are still written: unprobed and
// with no image servers, under the key "nobundle".
func TestSpawnTokenMCPWithoutABundle(t *testing.T) {
	check := func(t *testing.T, boxes *fakeBoxes, hooks *fakeBrokerHooks) {
		t.Helper()
		if len(hooks.mcpKeys) != 1 || hooks.mcpKeys[0] != "nobundle" {
			t.Fatalf("keys = %q", hooks.mcpKeys)
		}
		doc := hooks.mcpDocs[0]
		if !strings.Contains(doc, `"agentbox-env"`) || !strings.Contains(doc, `"grafana-prod"`) {
			t.Fatalf("document = %s", doc)
		}
		want := []string{"--mcp-config", "/opt/agentbox/mcp-nobundle.json", "--strict-mcp-config"}
		if got := boxes.lastArgv[len(boxes.lastArgv)-3:]; !slices.Equal(got, want) {
			t.Fatalf("argv tail = %q, want %q", got, want)
		}
	}
	t.Run("bundle off", func(t *testing.T) {
		m, boxes, hooks := mcpManager(t, "claude", account.DefaultResolved(""), grafanaEntry())
		spawnTok(t, m)
		check(t, boxes, hooks)
	})
	t.Run("bundle failed", func(t *testing.T) {
		m, boxes, hooks := mcpManager(t, "claude", account.DefaultResolved(""), grafanaEntry())
		m.SetBundles(&fakeBundles{err: errors.New("probe failed: /home/claude/.claude boom")})
		s := spawnTok(t, m)
		check(t, boxes, hooks)
		if len(s.BundleDrops) != 1 || s.BundleDrops[0].Reason != bundle.ReasonBundleFailed {
			t.Fatalf("bundle drops = %+v", s.BundleDrops)
		}
	})
}

func TestSpawnTokenMCPExtraUnbound(t *testing.T) {
	const reason = "sentry needs token llm-key, not bound to this profile"
	t.Run("alone: reported, nothing written", func(t *testing.T) {
		m, _, hooks := mcpManager(t, "claude", account.DefaultResolved(""), sentryEntry())
		m.SetBundles(&fakeBundles{flags: bundleFlags})
		s := spawnTok(t, m)
		if !slices.Contains(s.BundleDrops, bundle.Drop{Kind: "mcp", Name: "sentry", Reason: reason}) {
			t.Fatalf("bundle drops = %+v", s.BundleDrops)
		}
		if len(hooks.mcpKeys) != 0 {
			t.Fatal("a config was written for a server that was left out")
		}
	})
	t.Run("beside a server that is used", func(t *testing.T) {
		m, _, _ := mcpManager(t, "claude", account.DefaultResolved(""), grafanaEntry(), sentryEntry())
		b := &fakeBundles{flags: bundleFlags, drops: []bundle.Drop{{Kind: "settings", Name: "hooks", Reason: "x"}}}
		m.SetBundles(b)
		s := spawnTok(t, m)
		want := []bundle.Drop{{Kind: "settings", Name: "hooks", Reason: "x"}, {Kind: "mcp", Name: "sentry", Reason: reason}}
		if !slices.Equal(s.BundleDrops, want) {
			t.Fatalf("bundle drops = %+v, want %+v", s.BundleDrops, want)
		}
		if len(b.smcpEntries) != 1 || b.smcpEntries[0].Server != "grafana" {
			t.Fatalf("entries = %+v", b.smcpEntries)
		}
	})
	t.Run("both bound: the server gets its extra", func(t *testing.T) {
		llm := config.StaticToken{Name: "llm-key", Value: config.KeyRef{Type: "env", Var: "LLM_KEY"}, Env: []string{"OPENAI_API_KEY"}}
		m, _, _ := mcpManager(t, "claude", account.DefaultResolved(""), sentryEntry(), llm)
		b := &fakeBundles{flags: bundleFlags}
		m.SetBundles(b)
		spawnTok(t, m)
		if len(b.smcpEntries) != 1 || !slices.Equal(b.smcpEntries[0].Args, []string{"sentry", "llm-key", "--", "mcp-server-sentry"}) {
			t.Fatalf("entries = %+v", b.smcpEntries)
		}
	})
}

func TestSpawnTokenMCPFailureRefusesTheSpawn(t *testing.T) {
	t.Run("bundle", func(t *testing.T) {
		m, boxes, _ := mcpManager(t, "claude", account.DefaultResolved(""), grafanaEntry())
		m.SetBundles(&fakeBundles{smcpErr: errors.New("boom")})
		_, err := m.Spawn(launcher.SpawnSpec{Agent: "tok", Cwd: t.TempDir()})
		if err == nil || !strings.Contains(err.Error(), "spawn tok: preparing token MCP servers: boom") {
			t.Fatalf("err = %v", err)
		}
		if boxes.wraps != 0 {
			t.Fatal("a box was started for a refused spawn")
		}
	})
	t.Run("write, personal config", func(t *testing.T) {
		m, boxes, _ := mcpManager(t, "claude", account.DefaultResolved(""), grafanaEntry())
		m.SetBundles(&fakeBundles{flags: bundleFlags, writeErr: errors.New("disk full")})
		_, err := m.Spawn(launcher.SpawnSpec{Agent: "tok", Cwd: t.TempDir()})
		if err == nil || !strings.Contains(err.Error(), "preparing token MCP servers: disk full") {
			t.Fatalf("err = %v", err)
		}
		if boxes.wraps != 0 {
			t.Fatal("a box was started for a refused spawn")
		}
	})
	t.Run("write, binding dir", func(t *testing.T) {
		m, boxes, hooks := mcpManager(t, "claude", workAccount(false), grafanaEntry())
		m.SetBundles(&fakeBundles{flags: bundleFlags})
		hooks.mcpErr = errors.New("disk full")
		_, err := m.Spawn(launcher.SpawnSpec{Agent: "tok", Cwd: t.TempDir()})
		if err == nil || !strings.Contains(err.Error(), "preparing token MCP servers: disk full") {
			t.Fatalf("err = %v", err)
		}
		if boxes.wraps != 0 {
			t.Fatal("a box was started for a refused spawn")
		}
	})
}
