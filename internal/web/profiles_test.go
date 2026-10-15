package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/store"
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

	pm := NewProfileManager(seeds, opts, "", nil, fs, apply)
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

// The New-session form preselects the tier a profile will actually run under, so
// /api/profiles has to carry it. Resolution happens here, once, the same way the
// launcher resolves it: the profile's own tier → the instance default → box.
// Without it the client cannot tell a host profile from a boxed one and has to
// guess, which is what silently overrode every profile's tier at spawn.
func TestViewsExposeEffectiveRuntime(t *testing.T) {
	seeds := []config.AgentConfig{
		{Name: "boxed", Bin: "claude", Runtime: config.RuntimeBox},
		{Name: "unset", Bin: "claude"},
	}
	fs := newFakeStore()
	fs.m["hosted"] = config.AgentConfig{Name: "hosted", Bin: "claude", Runtime: config.RuntimeHost}

	m := NewProfileManager(seeds, nil, config.RuntimeHost, nil, fs, func([]config.AgentConfig) {})

	got := map[string]string{}
	for _, v := range m.Views() {
		got[v.Name] = v.Runtime
	}
	if got["hosted"] != config.RuntimeHost {
		t.Errorf("a stored profile's own tier must win, got %q", got["hosted"])
	}
	if got["boxed"] != config.RuntimeBox {
		t.Errorf("a seed's own tier must be reported, got %q", got["boxed"])
	}
	if got["unset"] != config.RuntimeHost {
		t.Errorf("a profile naming no tier must report the instance default, got %q", got["unset"])
	}
}

// Containment must not be reachable by omission: an instance that configures no
// default_runtime still reports box, matching config.AgentConfig.RuntimePolicy.
func TestViewsRuntimeFallsBackToBox(t *testing.T) {
	m := NewProfileManager([]config.AgentConfig{{Name: "a", Bin: "claude"}}, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	if rt := m.Views()[0].Runtime; rt != config.RuntimeBox {
		t.Errorf("with no instance default the view must report box, got %q", rt)
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
	m := NewProfileManager(seeds, opts, "", nil, fs, func([]config.AgentConfig) {})
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
	m := NewProfileManager(nil, opts, "", nil, fs, apply)

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

func TestViewsEmitDefaultCwd(t *testing.T) {
	fs := newFakeStore()
	_ = fs.Save(config.AgentConfig{Name: "p", Bin: "claude", DefaultCwd: "/home/me/proj"})
	pm := NewProfileManager(nil, nil, config.RuntimeHost, nil, fs, func([]config.AgentConfig) {})

	var got string
	for _, v := range pm.Views() {
		if v.Name == "p" {
			got = v.DefaultCwd
		}
	}
	if got != "/home/me/proj" {
		t.Fatalf("Views should propagate default_cwd, got %q", got)
	}
}

func TestSaveRejectsUnboxableDefaultCwd(t *testing.T) {
	home := t.TempDir()
	pm := NewProfileManager(nil, nil, config.RuntimeBox, nil, newFakeStore(), func([]config.AgentConfig) {})
	pm.home = home

	// cwd == home is un-boxable for a box-tier profile → reject.
	if err := pm.Save(config.AgentConfig{Name: "p", Bin: "claude", Runtime: config.RuntimeBox, DefaultCwd: home}); err == nil {
		t.Fatal("expected rejection: home as a boxed default_cwd")
	}

	// A subdirectory is boxable → accept.
	sub := filepath.Join(home, "proj")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := pm.Save(config.AgentConfig{Name: "p", Bin: "claude", Runtime: config.RuntimeBox, DefaultCwd: sub}); err != nil {
		t.Fatalf("boxable subdir should save: %v", err)
	}
}

func TestSaveAllowsHomeCwdForHostTier(t *testing.T) {
	home := t.TempDir()
	pm := NewProfileManager(nil, nil, config.RuntimeHost, nil, newFakeStore(), func([]config.AgentConfig) {})
	pm.home = home

	// Host tier is uncontained; any path is fine, including home.
	if err := pm.Save(config.AgentConfig{Name: "p", Bin: "bash", Runtime: config.RuntimeHost, DefaultCwd: home}); err != nil {
		t.Fatalf("host tier should accept any default_cwd: %v", err)
	}
}

func TestProfileViewCarriesControlFields(t *testing.T) {
	seeds := []config.AgentConfig{
		{Name: "claude", Bin: "claude", Replyable: true},
		{Name: "yolo", Bin: "/usr/local/bin/claude", Args: []string{"--permission-mode", "bypassPermissions"}},
		{Name: "bash", Bin: "bash", Replyable: true},
	}
	pm := NewProfileManager(seeds, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	got := map[string]ProfileView{}
	for _, v := range pm.Views() {
		got[v.Name] = v
	}
	if v := got["claude"]; !v.AcceptsPrompt || !v.Claude || !v.Replyable || v.BypassArgs {
		t.Errorf("claude view = %+v", v)
	}
	if v := got["yolo"]; !v.Claude || v.Replyable || !v.BypassArgs {
		t.Errorf("yolo view = %+v", v)
	}
	if v := got["bash"]; v.AcceptsPrompt || v.Claude || v.Replyable {
		t.Errorf("bash view = %+v (replies/prompt are claude-only)", v)
	}
}

func TestArgsBypassPermissions(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"--dangerously-skip-permissions"}, true},
		{[]string{"--permission-mode", "bypassPermissions"}, true},
		{[]string{"--permission-mode=bypassPermissions"}, true},
		{[]string{"--permission-mode", "default"}, false},
		{[]string{"--permission-mode"}, false},
		{nil, false},
	} {
		if got := argsBypassPermissions(c.args); got != c.want {
			t.Errorf("%q: %v want %v", c.args, got, c.want)
		}
	}
}

func TestSaveRejectsDataRootDefaultCwd(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "ab")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	pm := NewProfileManager(nil, nil, config.RuntimeBox, []box.ProtectedDir{box.DataDirProtected(root)}, newFakeStore(), func([]config.AgentConfig) {})
	pm.home = home
	err := pm.Save(config.AgentConfig{Name: "p", Bin: "claude", Runtime: config.RuntimeBox, DefaultCwd: root})
	if err == nil || !strings.Contains(err.Error(), "data directory") {
		t.Fatalf("a boxed default_cwd at the data root must be refused, got %v", err)
	}
}
