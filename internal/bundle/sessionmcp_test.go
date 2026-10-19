package bundle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/config"
)

func grafanaServer() EntryServer {
	return EntryServer{Server: "grafana", Entry: "grafana-cloud", Command: "mcp-grafana",
		Args: []string{"grafana-cloud", "--", "mcp-grafana", "--disable-docs"}}
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestComposePrecedence(t *testing.T) {
	user := map[string]any{"grafana": map[string]any{"command": "old-grafana"}, "keep": map[string]any{"command": "npx"}}
	image := map[string]any{"playwright": map[string]any{"command": "playwright-mcp"}}

	t.Run("entry replaces a host server", func(t *testing.T) {
		got, drops := ComposeSessionMCP(user, image, nil, nil, []EntryServer{grafanaServer()})
		if got["grafana"].(map[string]any)["command"] != "agentbox-env" {
			t.Fatalf("grafana = %v", got["grafana"])
		}
		want := []Drop{{Kind: "mcp", Name: "grafana", Reason: "replaced by token entry grafana-cloud"}}
		if !reflect.DeepEqual(drops, want) {
			t.Fatalf("drops = %+v", drops)
		}
		if _, ok := got["keep"]; !ok {
			t.Fatal("an unrelated host server was lost")
		}
	})

	t.Run("image replaces an entry", func(t *testing.T) {
		e := EntryServer{Server: "playwright", Entry: "pw", Command: "mcp-pw", Args: []string{"pw", "--", "mcp-pw"}}
		got, drops := ComposeSessionMCP(nil, image, nil, nil, []EntryServer{e})
		if got["playwright"].(map[string]any)["command"] != "playwright-mcp" {
			t.Fatalf("playwright = %v", got["playwright"])
		}
		want := []Drop{{Kind: "mcp", Name: "playwright", Reason: "replaced by the image's playwright"}}
		if !reflect.DeepEqual(drops, want) {
			t.Fatalf("drops = %+v", drops)
		}
	})

	t.Run("a host ${VAR} drop of the same name is replaced by the entry drop", func(t *testing.T) {
		in := []Drop{
			{Kind: "mcp", Name: "grafana", Reason: "uses ${VAR} substitution"},
			{Kind: "mcp", Name: "other", Reason: "x"},
		}
		_, drops := ComposeSessionMCP(nil, nil, nil, in, []EntryServer{grafanaServer()})
		want := []Drop{
			{Kind: "mcp", Name: "other", Reason: "x"},
			{Kind: "mcp", Name: "grafana", Reason: "replaced by token entry grafana-cloud"},
		}
		if !reflect.DeepEqual(drops, want) {
			t.Fatalf("drops = %+v", drops)
		}
		if in[0].Name != "grafana" || len(in) != 2 {
			t.Fatalf("the caller's drops were modified: %+v", in)
		}
	})

	t.Run("no host server of that name: no replacement row", func(t *testing.T) {
		_, drops := ComposeSessionMCP(nil, nil, nil, nil, []EntryServer{grafanaServer()})
		if len(drops) != 0 {
			t.Fatalf("drops = %+v", drops)
		}
	})
}

func TestComposeProbe(t *testing.T) {
	e := grafanaServer()
	t.Run("command missing", func(t *testing.T) {
		user := map[string]any{"grafana": map[string]any{"command": "old"}}
		got, drops := ComposeSessionMCP(user, nil, map[string]bool{"agentbox-env": true, "mcp-grafana": false}, nil, []EntryServer{e})
		want := []Drop{{Kind: "mcp", Name: "grafana", Reason: "command mcp-grafana does not exist inside the box image"}}
		if !reflect.DeepEqual(drops, want) {
			t.Fatalf("drops = %+v", drops)
		}
		// A dropped entry replaces nothing: the host's server stays.
		if got["grafana"].(map[string]any)["command"] != "old" {
			t.Fatalf("grafana = %v", got["grafana"])
		}
	})
	t.Run("agentbox-env missing drops every entry", func(t *testing.T) {
		other := EntryServer{Server: "sentry", Entry: "sentry", Command: "npx", Args: []string{"sentry", "--", "npx"}}
		got, drops := ComposeSessionMCP(nil, nil, map[string]bool{"mcp-grafana": true, "npx": true}, nil, []EntryServer{e, other})
		reason := "the box image has no agentbox-env; update box_image"
		want := []Drop{{Kind: "mcp", Name: "grafana", Reason: reason}, {Kind: "mcp", Name: "sentry", Reason: reason}}
		if !reflect.DeepEqual(drops, want) || len(got) != 0 {
			t.Fatalf("servers = %v drops = %+v", got, drops)
		}
	})
	t.Run("nil resolves is unprobed", func(t *testing.T) {
		got, drops := ComposeSessionMCP(nil, nil, nil, nil, []EntryServer{e})
		if len(got) != 1 || len(drops) != 0 {
			t.Fatalf("servers = %v drops = %+v", got, drops)
		}
	})
}

func TestComposeServerShape(t *testing.T) {
	got, _ := ComposeSessionMCP(nil, nil, nil, nil, []EntryServer{grafanaServer()})
	want := `{"grafana":{"args":["grafana-cloud","--","mcp-grafana","--disable-docs"],"command":"agentbox-env","type":"stdio"}}`
	if s := marshal(t, got); s != want {
		t.Fatalf("got  %s\nwant %s", s, want)
	}
}

// entryManager is imageManager with entry commands and a source that has one
// kept host server and an image that declares one server.
func entryManager(t *testing.T, resolves map[string]bool, cmds []string) (*Manager, *fakeImageProbe) {
	t.Helper()
	p := &fakeImageProbe{id: "sha256:a", facts: ImageFacts{HasMCP: true,
		MCP:      []byte(`{"mcpServers":{"cbm":{"command":"codebase-memory-mcp"}}}`),
		Resolves: resolves}}
	dir := t.TempDir()
	src := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	mcpFile := filepath.Join(dir, ".claude.json")
	if err := os.WriteFile(mcpFile, []byte(`{"mcpServers":{"mine":{"command":"npx","args":["x"]},"grafana":{"command":"npx"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{Source: src, Root: filepath.Join(dir, "bundles"), MCPFile: mcpFile, Prober: p, EntryCommands: cmds})
	if err != nil {
		t.Fatal(err)
	}
	return m, p
}

func TestSessionMCPNonInheriting(t *testing.T) {
	m, _ := entryManager(t, map[string]bool{"npx": true, "agentbox-env": true, "mcp-grafana": true}, []string{"agentbox-env", "mcp-grafana"})
	p := NewProvider(m)

	inherit, err := p.SessionMCP(true, nil, []EntryServer{grafanaServer()})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(inherit.Doc, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.MCPServers["mine"]; !ok || doc.MCPServers["grafana"]["command"] != "agentbox-env" || doc.MCPServers["cbm"] == nil {
		t.Fatalf("inheriting servers = %s", inherit.Doc)
	}
	if !strings.HasSuffix(inherit.Key, "-u") {
		t.Fatalf("key = %q", inherit.Key)
	}

	solo, err := p.SessionMCP(false, nil, []EntryServer{grafanaServer()})
	if err != nil {
		t.Fatal(err)
	}
	doc.MCPServers = nil
	if err := json.Unmarshal(solo.Doc, &doc); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for n := range doc.MCPServers {
		names[n] = true
	}
	if !reflect.DeepEqual(names, map[string]bool{"grafana": true, "cbm": true}) {
		t.Fatalf("non-inheriting servers = %s", solo.Doc)
	}
	if !strings.HasSuffix(solo.Key, "-i") || strings.TrimSuffix(solo.Key, "-i") != strings.TrimSuffix(inherit.Key, "-u") {
		t.Fatalf("keys %q / %q", inherit.Key, solo.Key)
	}
	if len(solo.Drops) != 0 {
		t.Fatalf("non-inheriting drops = %+v", solo.Drops)
	}
	// The host's grafana was replaced by the entry (inheriting) and is not
	// reported at all when the host's servers are not in play.
	if len(inherit.Drops) != 1 || inherit.Drops[0].Reason != "replaced by token entry grafana-cloud" {
		t.Fatalf("inheriting drops = %+v", inherit.Drops)
	}
}

func TestSessionMCPUsesTheBundleProbe(t *testing.T) {
	// mcp-grafana is absent from the image: the lock recorded it, so the session
	// config leaves the entry out without a second probe.
	m, probe := entryManager(t, map[string]bool{"npx": true, "agentbox-env": true}, []string{"agentbox-env", "mcp-grafana"})
	p := NewProvider(m)
	got, err := p.SessionMCP(true, nil, []EntryServer{grafanaServer()})
	if err != nil {
		t.Fatal(err)
	}
	if probe.probes != 1 {
		t.Fatalf("probes = %d, want one per build", probe.probes)
	}
	var found bool
	for _, d := range got.Drops {
		if d.Name == "grafana" && d.Reason == "command mcp-grafana does not exist inside the box image" {
			found = true
		}
	}
	if !found {
		t.Fatalf("drops = %+v", got.Drops)
	}
	if strings.Contains(string(got.Doc), "agentbox-env") {
		t.Fatalf("a dropped entry is in the config: %s", got.Doc)
	}
}

func TestBundleWritesImageMCP(t *testing.T) {
	m, _ := entryManager(t, map[string]bool{"npx": true, "agentbox-env": true}, nil)
	b := mustEnsure(t, m)
	raw, err := os.ReadFile(filepath.Join(b.Dir, imageMCPName))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.MCPServers) != 1 || doc.MCPServers["cbm"] == nil {
		t.Fatalf("image-mcp.json = %s", raw)
	}
	if _, ok := b.Lock.Files[imageMCPName]; !ok {
		t.Fatalf("lock files = %v", b.Lock.Files)
	}
	if b.Lock.EntryResolves != nil {
		t.Fatalf("no entry commands were asked: %v", b.Lock.EntryResolves)
	}
	// The merged file keeps both.
	if s := bundleServers(t, b); s["cbm"] == nil || s["mine"] == nil {
		t.Fatalf("mcp.json = %v", s)
	}

	// No image file: an empty set, never absent.
	p := &fakeImageProbe{id: "sha256:a", facts: ImageFacts{Resolves: map[string]bool{}}}
	m2, _ := imageManager(t, `{"mcpServers":{}}`, p)
	b2 := mustEnsure(t, m2)
	raw, err = os.ReadFile(filepath.Join(b2.Dir, imageMCPName))
	if err != nil || !strings.Contains(string(raw), `"mcpServers": {}`) {
		t.Fatalf("image-mcp.json = %q, %v", raw, err)
	}
}

func TestEntryCommandsChangeTheSourceHash(t *testing.T) {
	m, _ := entryManager(t, nil, nil)
	h0, _, _ := m.sourceHash(false)
	m.opts.EntryCommands = []string{"mcp-grafana", "agentbox-env"}
	h1, _, _ := m.sourceHash(false)
	m.opts.EntryCommands = []string{"agentbox-env", "mcp-grafana", "agentbox-env"}
	h2, _, _ := m.sourceHash(false)
	m.opts.EntryCommands = []string{"agentbox-env"}
	h3, _, _ := m.sourceHash(false)
	if h0 == h1 || h1 == h3 || h0 == h3 {
		t.Fatalf("entry commands do not move the hash: %s %s %s", h0, h1, h3)
	}
	if h1 != h2 {
		t.Fatalf("order and duplicates must not matter: %s vs %s", h1, h2)
	}
}

// The session's file names servers and commands, never a value, a variable or
// where one lives.
func TestSessionMCPFileHasNoValue(t *testing.T) {
	m, _ := entryManager(t, map[string]bool{"npx": true, "agentbox-env": true, "mcp-grafana": true}, []string{"agentbox-env", "mcp-grafana"})
	entry := config.StaticToken{Name: "grafana-cloud", Value: config.KeyRef{Type: "file", Path: "/secret/path/grafana.token"},
		Env: []string{"GRAFANA_SA_TOKEN_VAR"}, ConfigEnv: map[string]string{"GRAFANA_URL": "https://g.example.com"},
		Egress: []config.TokenEgress{{Host: "g.example.com"}},
		MCP:    &config.TokenMCP{Server: "grafana", Command: "mcp-grafana", Args: []string{"--disable-docs"}}}
	args, err := config.EntryMCPArgs(entry, map[string]bool{"grafana-cloud": true})
	if err != nil {
		t.Fatal(err)
	}
	sm, err := NewProvider(m).SessionMCP(true, nil, []EntryServer{{Server: "grafana", Entry: "grafana-cloud", Command: "mcp-grafana", Args: args}})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	bs := config.Bindings{Tokens: []config.StaticToken{entry}}
	boxPath, err := broker.WriteSessionMCP(root, bs, sm.Key, sm.Doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(boxPath, broker.BoxDir+"/mcp-") {
		t.Fatalf("box path = %s", boxPath)
	}
	var files int
	_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !strings.HasPrefix(fi.Name(), "mcp-") {
			return nil
		}
		files++
		b, _ := os.ReadFile(p)
		for _, bad := range []string{"tok-SECRET-1234", "GRAFANA_SA_TOKEN_VAR", "/secret/path", "g.example.com"} {
			if strings.Contains(string(b), bad) {
				t.Fatalf("%s holds %q:\n%s", fi.Name(), bad, b)
			}
		}
		if !strings.Contains(string(b), `"agentbox-env"`) {
			t.Fatalf("%s lacks the launcher:\n%s", fi.Name(), b)
		}
		return nil
	})
	if files != 1 {
		t.Fatalf("session mcp files = %d", files)
	}
}
