package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/bundle"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

// realFilesHooks writes the binding dir and session files for real, into a temp
// state root, so a test can read back what a box would be able to see.
type realFilesHooks struct {
	*fakeBrokerHooks
	root string
}

func (h realFilesHooks) BoxFor(bs config.Bindings, base map[string]awsprobe.Result) ([]box.Mount, []string, error) {
	_, env, err := h.fakeBrokerHooks.BoxFor(bs, base)
	if err != nil {
		return nil, nil, err
	}
	dir, err := broker.EnsureBindingDirWith(h.root, bs, base)
	if err != nil {
		return nil, nil, err
	}
	return []box.Mount{{Host: dir, Box: broker.BoxDir}}, env, nil
}

func (h realFilesHooks) WriteAWSListing(bs config.Bindings, base map[string]awsprobe.Result) error {
	if err := h.fakeBrokerHooks.WriteAWSListing(bs, base); err != nil {
		return err
	}
	return broker.WriteAWSListing(h.root, bs, base)
}

func (h realFilesHooks) WriteSessionMCP(bs config.Bindings, key string, doc []byte) (string, error) {
	return broker.WriteSessionMCP(h.root, bs, key, doc)
}

type everythingResolves struct{}

func (everythingResolves) Resolves(cmds []string) map[string]bool {
	out := map[string]bool{}
	for _, c := range cmds {
		out[c] = true
	}
	return out
}

// Two sessions with identical bindings, one on an account that inherits the
// personal config and one that does not: the box of the second must not be able
// to read a document naming the user's own MCP servers, whichever session wrote
// it and in whichever order. The first still gets entry > host precedence.
func TestTokenMCPNeverLeaksPersonalServersToANonInheritingBox(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	mcpFile := filepath.Join(dir, ".claude.json")
	if err := os.WriteFile(mcpFile, []byte(`{"mcpServers":{"personal-srv":{"command":"npx","args":["p"]},"grafana":{"command":"npx"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bm, err := bundle.New(bundle.Options{Source: src, Root: filepath.Join(dir, "bundle"), MCPFile: mcpFile, Prober: everythingResolves{}})
	if err != nil {
		t.Fatal(err)
	}
	provider := bundle.NewProvider(bm)
	stateRoot := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	spawn := func(acct account.Resolved) (*fakeBoxes, *Session) {
		t.Helper()
		l := mcpLauncher{tokLauncher: tokLauncherFor(config.RuntimeBox), bin: "claude", acct: acct}
		l.tokens = []config.StaticToken{grafanaEntry()}
		m := NewManager(l, nil, 1<<16, time.Minute)
		boxes := &fakeBoxes{}
		m.SetBoxes(boxes)
		m.SetBroker(realFilesHooks{fakeBrokerHooks: &fakeBrokerHooks{}, root: stateRoot})
		m.SetEgress(&fakeEgressHooks{})
		m.SetBundles(provider)
		s, err := m.Spawn(launcher.SpawnSpec{Agent: "tok", Cwd: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = m.Kill(s.ID) })
		return boxes, s
	}
	// Everything a box can read: every host directory it mounts, whole.
	readable := func(boxes *fakeBoxes) map[string]string {
		files := map[string]string{}
		for _, mt := range boxes.lastRO {
			_ = filepath.Walk(mt.Host, func(p string, fi os.FileInfo, err error) error {
				if err == nil && fi.Mode().IsRegular() {
					b, _ := os.ReadFile(p)
					files[mt.Box+strings.TrimPrefix(p, mt.Host)] = string(b)
				}
				return nil
			})
		}
		return files
	}

	inBoxes, _ := spawn(account.DefaultResolved(""))
	inFiles := readable(inBoxes)
	var personal string
	for name, body := range inFiles {
		if strings.HasPrefix(name, bundle.BoxSessionMCPDir+"/mcp-") {
			personal = body
		}
	}
	if !strings.Contains(personal, "personal-srv") {
		t.Fatalf("the inheriting box lacks its personal servers: %v", inFiles)
	}
	var doc struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(personal), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.MCPServers["grafana"]["command"] != "agentbox-env" {
		t.Fatalf("entry must beat the host's grafana: %s", personal)
	}

	outBoxes, _ := spawn(workAccount(false))
	if outBoxes.lastKey.Binding != inBoxes.lastKey.Binding || outBoxes.lastKey.Account == inBoxes.lastKey.Account {
		t.Fatalf("keys %+v / %+v: want one binding, two accounts", inBoxes.lastKey, outBoxes.lastKey)
	}
	outFiles := readable(outBoxes)
	var sawEntry bool
	for name, body := range outFiles {
		if strings.Contains(body, "personal-srv") {
			t.Fatalf("a non-inheriting box can read the user's personal servers in %s:\n%s", name, body)
		}
		if strings.Contains(name, "/mcp-") && strings.Contains(body, "agentbox-env") {
			sawEntry = true
		}
	}
	if !sawEntry {
		t.Fatalf("the non-inheriting box has no entry config: %v", outFiles)
	}
	for _, mt := range outBoxes.lastRO {
		if mt.Box == bundle.BoxSessionMCPDir || mt.Box == bundle.BoxBundleDir {
			t.Fatalf("a non-inheriting box mounts %s", mt.Box)
		}
	}
}
