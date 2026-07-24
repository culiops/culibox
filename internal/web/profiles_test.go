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
