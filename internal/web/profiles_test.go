package web

import (
	"testing"

	"github.com/culiops/culiops-agent-console/internal/config"
	"github.com/culiops/culiops-agent-console/internal/store"
)

type fakeStore struct{ m map[string]config.AgentConfig }

func newFakeStore() *fakeStore { return &fakeStore{m: map[string]config.AgentConfig{}} }
func (f *fakeStore) List() []config.AgentConfig {
	out := []config.AgentConfig{}
	for _, p := range f.m {
		out = append(out, p)
	}
	return out
}
func (f *fakeStore) Get(name string) (config.AgentConfig, bool) { p, ok := f.m[name]; return p, ok }
func (f *fakeStore) Save(p config.AgentConfig) error            { f.m[p.Name] = p; return nil }
func (f *fakeStore) Delete(name string) error {
	if _, ok := f.m[name]; !ok {
		return store.ErrNotFound
	}
	delete(f.m, name)
	return nil
}

func TestProfileManager(t *testing.T) {
	seeds := []config.AgentConfig{
		{Name: "claude", Bin: "claude", Options: map[string]any{"skip": true}},
		{Name: "bash", Bin: "bash"},
	}
	opts := []config.OptionDef{{Key: "skip", Label: "Skip", Type: "bool", Flag: "-x"}}
	fs := newFakeStore()
	var lastApplied []config.AgentConfig
	apply := func(a []config.AgentConfig) { lastApplied = a }

	pm := NewProfileManager(seeds, opts, fs, apply)
	if len(lastApplied) != 2 {
		t.Fatalf("init should push 2 seeds to the launcher, got %d", len(lastApplied))
	}

	vs := pm.Views()
	if len(vs) != 2 || vs[0].Name != "claude" || vs[0].Editable || len(vs[0].Options) != 1 {
		t.Fatalf("initial views wrong: %+v", vs)
	}

	// create a runtime profile → merged=3, editable, launcher refreshed
	if err := pm.Save(config.AgentConfig{Name: "myclaude", Bin: "claude", Base: "clean"}); err != nil {
		t.Fatal(err)
	}
	if len(lastApplied) != 3 {
		t.Fatalf("save should refresh launcher to 3, got %d", len(lastApplied))
	}
	editable := map[string]bool{}
	for _, v := range pm.Views() {
		editable[v.Name] = v.Editable
	}
	if !editable["myclaude"] || editable["bash"] {
		t.Fatalf("editable flags wrong: %v", editable)
	}

	// invalid profile rejected
	if err := pm.Save(config.AgentConfig{Name: "bad", Bin: "x", Base: "nope"}); err == nil {
		t.Fatal("invalid base should be rejected")
	}

	// store overrides a same-named seed → that name becomes editable
	if err := pm.Save(config.AgentConfig{Name: "claude", Bin: "claude", Base: "clean"}); err != nil {
		t.Fatal(err)
	}
	for _, v := range pm.Views() {
		if v.Name == "claude" && !v.Editable {
			t.Fatal("overridden seed should be editable")
		}
	}

	// delete stored ok; deleting a config-only seed → ErrNotFound
	if err := pm.Delete("myclaude"); err != nil {
		t.Fatal(err)
	}
	if err := pm.Delete("bash"); err != store.ErrNotFound {
		t.Fatalf("deleting a config seed should be ErrNotFound, got %v", err)
	}
}

func TestViewsFilterOptionsByBin(t *testing.T) {
	opts := []config.OptionDef{
		{Key: "model", Label: "Model", Type: config.OptEnum, Choices: []string{"claude-opus-5"}, Flag: "--model", Bins: []string{"claude"}},
	}
	seeds := []config.AgentConfig{
		{Name: "claude", Bin: "claude", Options: map[string]any{"model": "claude-opus-5"}},
		// a bash agent that (wrongly) carries a claude-only option value in its map
		{Name: "shell", Bin: "bash", Options: map[string]any{"model": "claude-opus-5"}},
	}
	fs := newFakeStore()
	m := NewProfileManager(seeds, opts, fs, func([]config.AgentConfig) {})
	views := m.Views()
	got := map[string]int{}
	for _, v := range views {
		got[v.Name] = len(v.Options)
	}
	if got["claude"] != 1 {
		t.Errorf("claude should expose the model option, got %d", got["claude"])
	}
	if got["shell"] != 0 {
		t.Errorf("bash agent must not expose the claude-only model option, got %d", got["shell"])
	}
}

// TestMergedStripsInapplicableOptions covers a legacy stored profile (created
// via an older editor, before bin-scoping was enforced) that still carries a
// claude-only option on a bash bin. merged() must strip it before it reaches
// the launcher's apply callback, Views(), or Get() — bin-scoping must hold on
// load, not just on Save/UI.
func TestMergedStripsInapplicableOptions(t *testing.T) {
	opts := []config.OptionDef{
		{Key: "model", Label: "Model", Type: config.OptEnum, Choices: []string{"claude-opus-5"}, Flag: "--model", Bins: []string{"claude"}},
	}
	fs := newFakeStore()
	// Seed the store directly (bypassing ProfileManager.Save's ValidateProfile)
	// to simulate a legacy profile that predates bin-scoping enforcement.
	fs.m["legacy"] = config.AgentConfig{Name: "legacy", Bin: "bash", Options: map[string]any{"model": "claude-opus-5"}}

	var applied []config.AgentConfig
	apply := func(a []config.AgentConfig) { applied = a }
	m := NewProfileManager(nil, opts, fs, apply)

	found := false
	for _, a := range applied {
		if a.Name == "legacy" {
			found = true
			if _, ok := a.Options["model"]; ok {
				t.Fatal("inapplicable option 'model' must be stripped from the launcher-facing profile for a bash profile")
			}
		}
	}
	if !found {
		t.Fatal("expected the 'legacy' profile to be pushed to the launcher")
	}

	// Also verify Get() (used by the profile editor) never surfaces it.
	got, ok := m.Get("legacy")
	if !ok {
		t.Fatal("Get(\"legacy\") should find the stored profile")
	}
	if _, ok := got.Options["model"]; ok {
		t.Fatal("inapplicable option 'model' must be stripped from Get() as well")
	}
}
