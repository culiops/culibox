package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
	"github.com/culiops/culibox/internal/session"
	"github.com/culiops/culibox/internal/store"
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

// With no box layer a session that asks for box is refused, and one that asks for
// nothing runs on this machine (session.runtimeFor). What a session asks for is
// the profile's own tier, else the raw instance default_runtime, else nothing
// (launcher.RequestedRuntime), and Runtime above cannot say it: it ends in box
// when nobody named a tier. So the view carries it as RuntimeAsked, for the page
// to tell "In a box, as nobody said otherwise" from "In a box, by name".
func TestViewsExposeTheTierASessionAsksFor(t *testing.T) {
	seeds := []config.AgentConfig{
		{Name: "unset", Bin: "claude"},
		{Name: "boxed", Bin: "claude", Runtime: config.RuntimeBox},
	}
	fs := newFakeStore()
	fs.m["hosted"] = config.AgentConfig{Name: "hosted", Bin: "claude", Runtime: config.RuntimeHost}
	asked := func(def string) map[string]string {
		m := NewProfileManager(seeds, nil, def, nil, fs, func([]config.AgentConfig) {})
		got := map[string]string{}
		for _, v := range m.Views() {
			got[v.Name] = v.RuntimeAsked
		}
		return got
	}
	for _, tc := range []struct{ def, unset, boxed, hosted string }{
		{"", "", config.RuntimeBox, config.RuntimeHost},
		{config.RuntimeBox, config.RuntimeBox, config.RuntimeBox, config.RuntimeHost},
		{config.RuntimeHost, config.RuntimeHost, config.RuntimeBox, config.RuntimeHost},
	} {
		got := asked(tc.def)
		if got["unset"] != tc.unset || got["boxed"] != tc.boxed || got["hosted"] != tc.hosted {
			t.Errorf("default_runtime %q: asked unset=%q boxed=%q hosted=%q, want unset=%q boxed=%q hosted=%q",
				tc.def, got["unset"], got["boxed"], got["hosted"], tc.unset, tc.boxed, tc.hosted)
		}
	}
}

// A profile that asks for nothing leaves the key out, so an older page and every
// existing payload read exactly as before.
func TestProfileViewLeavesRuntimeAskedOutWhenNothingIsAsked(t *testing.T) {
	m := NewProfileManager([]config.AgentConfig{{Name: "a", Bin: "claude"}}, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	b, err := json.Marshal(m.Views()[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "runtime_asked") {
		t.Errorf("runtime_asked must be omitted when nothing is asked: %s", b)
	}
}

// The server half of the editor's tier fix: a profile stored as "box" stays box
// on an instance whose default is host. The editor now always sends the tier it
// shows, so this is what makes an explicit "box" survive the default.
func TestViewsStoredBoxBeatsHostDefault(t *testing.T) {
	fs := newFakeStore()
	fs.m["p"] = config.AgentConfig{Name: "p", Bin: "claude", Runtime: config.RuntimeBox}
	m := NewProfileManager(nil, nil, config.RuntimeHost, nil, fs, func([]config.AgentConfig) {})
	for _, v := range m.Views() {
		if v.Name == "p" && v.Runtime != config.RuntimeBox {
			t.Errorf("a stored box must beat the host default, got %q", v.Runtime)
		}
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

// TestViewsOfferUnsetEnums covers #35: a profile saved from the editor with
// Model left "(not set)" still offers Model at spawn, blank = the agent's own
// default. An unset bool is not offered: it can be a capability (bypass).
func TestViewsOfferUnsetEnums(t *testing.T) {
	opts := []config.OptionDef{
		{Key: "skip", Label: "Skip", Type: config.OptBool, Flag: "-x", Bins: []string{"claude"}},
		{Key: "model", Label: "Model", Type: config.OptEnum, Choices: []string{"claude-opus-5"}, Flag: "--model", Bins: []string{"claude"}},
	}
	fs := newFakeStore()
	_ = fs.Save(config.AgentConfig{Name: "fresh", Bin: "claude"})
	_ = fs.Save(config.AgentConfig{Name: "sh", Bin: "bash"})
	m := NewProfileManager(nil, opts, "", nil, fs, func([]config.AgentConfig) {})
	got := map[string][]ProfileOption{}
	for _, v := range m.Views() {
		got[v.Name] = v.Options
	}
	if o := got["fresh"]; len(o) != 1 || o[0].Key != "model" || o[0].Value != "" {
		t.Fatalf("fresh claude profile options = %+v, want model only, blank", o)
	}
	if len(got["sh"]) != 0 {
		t.Fatalf("bash profile offered claude options: %+v", got["sh"])
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

type fakeAccountsWeb map[string]account.Resolved

func (f fakeAccountsWeb) Resolve(n string) (account.Resolved, error) {
	if n == "" || n == account.Default {
		return account.DefaultResolved("/home/u/.claude"), nil
	}
	r, ok := f[n]
	if !ok {
		return account.Resolved{}, account.ErrUnknown
	}
	return r, nil
}

func TestProfileSaveAccountRules(t *testing.T) {
	pm := NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	pm.SetAccounts(fakeAccountsWeb{
		"work": account.Resolved{Account: account.Account{Name: "work", Provider: "claude"}},
		"gpt":  account.Resolved{Account: account.Account{Name: "gpt", Provider: "codex"}},
	})
	ok := []config.AgentConfig{
		{Name: "a", Bin: "claude", Account: "work"},
		{Name: "b", Bin: "claude"},                                               // default
		{Name: "c", Bin: "claude", Secrets: []string{"CLAUDE_CODE_OAUTH_TOKEN"}}, // default keeps the workaround
		{Name: "h", Bin: "/usr/local/bin/codex", Account: "gpt"},
	}
	for _, p := range ok {
		if err := pm.Save(p); err != nil {
			t.Errorf("Save(%s) = %v", p.Name, err)
		}
	}
	bad := []config.AgentConfig{
		{Name: "d", Bin: "claude", Account: "nope"},
		{Name: "e", Bin: "/bin/bash", Account: "work"},
		{Name: "f", Bin: "claude", Account: "work", Secrets: []string{"CLAUDE_CODE_OAUTH_TOKEN"}},
		{Name: "g", Bin: "claude", Env: map[string]string{"CLAUDE_CONFIG_DIR": "/x"}},
		{Name: "i", Bin: "claude", Account: "gpt"}, // codex account on a claude bin
		{Name: "j", Bin: "codex", Account: "work"}, // claude account on a codex bin
		{Name: "k", Bin: "claude", Account: "work", Env: map[string]string{"ANTHROPIC_API_KEY": "x"}},
	}
	for _, p := range bad {
		if err := pm.Save(p); err == nil {
			t.Errorf("Save(%s) accepted", p.Name)
		}
	}
	for _, v := range pm.Views() {
		if v.Name == "a" && v.Account != "work" {
			t.Errorf("view a Account = %q, want work", v.Account)
		}
		if v.Name == "b" && v.Account != "" {
			t.Errorf("view b Account = %q, want empty", v.Account)
		}
	}
}

func TestProfileSaveAccountWithoutResolver(t *testing.T) {
	pm := NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	if err := pm.Save(config.AgentConfig{Name: "a", Bin: "claude", Account: "work"}); err == nil {
		t.Error("named account accepted with no resolver")
	}
	if err := pm.Save(config.AgentConfig{Name: "b", Bin: "claude", Account: "default"}); err != nil {
		t.Errorf("explicit default rejected: %v", err)
	}
}

func cloneProfile(runtime string) config.AgentConfig {
	return config.AgentConfig{
		Name: "cl", Bin: "claude", Runtime: runtime,
		CloneOf: "/home/u/repo", DefaultCwd: "/home/u/clones/repo",
	}
}

func TestSaveCloneProfileNeedsBoxTier(t *testing.T) {
	fs := newFakeStore()
	pm := NewProfileManager(nil, nil, config.RuntimeHost, nil, fs, func([]config.AgentConfig) {})
	pm.home = t.TempDir()

	err := pm.Save(cloneProfile(""))
	if err == nil || !strings.Contains(err.Error(), `clone mode needs runtime "box" (this instance defaults to host)`) {
		t.Fatalf("a clone profile on a host-default instance with no runtime must be refused, got %v", err)
	}
	if _, ok := fs.Get("cl"); ok {
		t.Fatal("a refused profile must not be stored")
	}
	if err := pm.Save(cloneProfile(config.RuntimeBox)); err != nil {
		t.Fatalf("an explicit box clone profile must save: %v", err)
	}
	if _, ok := fs.Get("cl"); !ok {
		t.Fatal("the box clone profile was not stored")
	}
}

func TestSaveCloneProfileRunsCloneCheck(t *testing.T) {
	fs := newFakeStore()
	pm := NewProfileManager(nil, nil, config.RuntimeBox, nil, fs, func([]config.AgentConfig) {})
	pm.home = t.TempDir()

	var got []config.Clone
	boom := errors.New("repo is not cloneable")
	pm.SetCloneCheck(func(c config.Clone) error { got = append(got, c); return boom })
	p := cloneProfile("")
	p.CloneShare = []string{"docs"}
	if err := pm.Save(p); !errors.Is(err, boom) {
		t.Fatalf("the check's error must be returned, got %v", err)
	}
	if len(got) != 1 || got[0].Of != "/home/u/repo" || got[0].Dir != "/home/u/clones/repo" || len(got[0].Shares) != 1 || got[0].Shares[0] != "docs" {
		t.Fatalf("the check must get the parsed clone, got %+v", got)
	}
	if _, ok := fs.Get("cl"); ok {
		t.Fatal("a profile the check refused must not be stored")
	}

	// A passing check stores; a non-clone profile never calls the check.
	pm.SetCloneCheck(func(c config.Clone) error { return nil })
	if err := pm.Save(p); err != nil {
		t.Fatal(err)
	}
	pm.SetCloneCheck(func(c config.Clone) error { t.Fatal("check called for a non-clone profile"); return nil })
	if err := pm.Save(config.AgentConfig{Name: "plain", Bin: "bash"}); err != nil {
		t.Fatal(err)
	}
	// nil = pure validation only.
	pm.SetCloneCheck(nil)
	if err := pm.Save(p); err != nil {
		t.Fatal(err)
	}
}

func TestProfileSaveRefusesBoxImage(t *testing.T) {
	fs := newFakeStore()
	m := NewProfileManager(nil, nil, "", nil, fs, func([]config.AgentConfig) {})
	err := m.Save(config.AgentConfig{Name: "p", Bin: "bash", Box: config.BoxConfig{Image: "x:1"}})
	if err == nil || !strings.Contains(err.Error(), "box.image is not per-profile") {
		t.Fatalf("got %v, want the box.image refusal", err)
	}
	if _, ok := fs.m["p"]; ok {
		t.Fatal("a refused profile must not reach the store")
	}
}

func TestProfileSaveDropsBlankBoxImage(t *testing.T) {
	fs := newFakeStore()
	m := NewProfileManager(nil, nil, "", nil, fs, func([]config.AgentConfig) {})
	if err := m.Save(config.AgentConfig{Name: "p", Bin: "bash", Box: config.BoxConfig{Image: "  "}}); err != nil {
		t.Fatalf("Save with a blank box.image: %v", err)
	}
	if p := fs.m["p"]; p.Box != (config.BoxConfig{}) {
		t.Fatalf("stored Box = %+v, want it cleared", p.Box)
	}
}

func TestLegacyBoxImageLoadsIgnoredAndLogged(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	fs := newFakeStore()
	fs.m["old"] = config.AgentConfig{Name: "old", Bin: "bash", Box: config.BoxConfig{Image: "x:1"}}
	seeds := []config.AgentConfig{{Name: "seed", Bin: "bash", Box: config.BoxConfig{Image: "y:2"}}}
	var applied []config.AgentConfig
	m := NewProfileManager(seeds, nil, "", nil, fs, func(a []config.AgentConfig) { applied = a })

	for _, want := range []string{
		`profile "old": ignoring box.image "x:1"; boxes run the instance-wide box_image`,
		`profile "seed": ignoring box.image "y:2"; boxes run the instance-wide box_image`,
	} {
		if n := strings.Count(buf.String(), want); n != 1 {
			t.Errorf("want exactly one log line %q, got %d in %q", want, n, buf.String())
		}
	}
	if p, ok := m.Get("old"); !ok || p.Box != (config.BoxConfig{}) {
		t.Fatalf("Get(old) = %+v, %v; want Box cleared", p, ok)
	}
	if len(applied) != 2 {
		t.Fatalf("apply got %d profiles, want 2", len(applied))
	}
	for _, p := range applied {
		if p.Box != (config.BoxConfig{}) {
			t.Errorf("apply profile %q kept Box %+v", p.Name, p.Box)
		}
	}
	if v := m.Views(); len(v) != 2 {
		t.Fatalf("Views = %+v, want both profiles listed", v)
	}
	// Later merges (any Get/Views/Save) do not repeat the line.
	if n := strings.Count(buf.String(), "ignoring box.image"); n != 2 {
		t.Errorf("log lines after later reads = %d, want 2 total", n)
	}
}

func TestProfileSaveChecksTokens(t *testing.T) {
	pm := NewProfileManager(nil, nil, config.RuntimeBox, nil, newFakeStore(), func([]config.AgentConfig) {})
	s := &Server{Profiles: pm}
	body := `{"name":"p","bin":"claude","runtime":"box","tokens":["sentry-corp"]}`

	rec := httptest.NewRecorder()
	s.handleProfiles(rec, httptest.NewRequest("POST", "/api/profiles", strings.NewReader(body)))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "sentry-corp") {
		t.Fatalf("an undefined entry must be a 400 naming it, got %d %s", rec.Code, rec.Body)
	}

	pm.SetStaticTokens([]config.StaticToken{{Name: "sentry-corp", Value: config.KeyRef{Type: "env", Var: "S"},
		Env: []string{"SENTRY_ACCESS_TOKEN"}, Egress: []config.TokenEgress{{Host: "sentry.corp.example"}}}})
	rec = httptest.NewRecorder()
	s.handleProfiles(rec, httptest.NewRequest("POST", "/api/profiles", strings.NewReader(body)))
	if rec.Code != 201 {
		t.Fatalf("a defined entry should save, got %d %s", rec.Code, rec.Body)
	}

	// the manager keeps its own copy of the entries
	entries := []config.StaticToken{{Name: "x", Env: []string{"A"}}}
	pm.SetStaticTokens(entries)
	entries[0].Env[0] = "CHANGED"
	if pm.tokens[0].Env[0] != "A" {
		t.Fatal("SetStaticTokens must clone")
	}
}

func TestProfileSaveChecksAWSConnections(t *testing.T) {
	pm := NewProfileManager(nil, nil, config.RuntimeBox, nil, newFakeStore(), func([]config.AgentConfig) {})
	s := &Server{Profiles: pm}
	body := `{"name":"p","bin":"claude","runtime":"box","aws_connections":["acme-prod-read"]}`

	rec := httptest.NewRecorder()
	s.handleProfiles(rec, httptest.NewRequest("POST", "/api/profiles", strings.NewReader(body)))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "acme-prod-read") {
		t.Fatalf("an undefined connection must be a 400 naming it, got %d %s", rec.Code, rec.Body)
	}

	conns := []config.AWSConnection{{Name: "acme-prod-read", AWSBinding: config.AWSBinding{
		SourceProfile: "p", RoleARN: "arn:aws:iam::123456789012:role/r", Region: "us-east-1"}}}
	pm.SetAWSConnections(conns)
	rec = httptest.NewRecorder()
	s.handleProfiles(rec, httptest.NewRequest("POST", "/api/profiles", strings.NewReader(body)))
	if rec.Code != 201 {
		t.Fatalf("a defined connection should save, got %d %s", rec.Code, rec.Body)
	}

	// the manager keeps its own copy
	conns[0].Region = "changed"
	if pm.awsConns[0].Region != "us-east-1" {
		t.Fatal("SetAWSConnections must clone")
	}
}

func contextProfile(runtime string, paths ...string) config.AgentConfig {
	return config.AgentConfig{Name: "ctx", Bin: "claude", Runtime: runtime, ContextPaths: paths}
}

func TestSaveRefusesContextPathsOnHost(t *testing.T) {
	fs := newFakeStore()
	pm := NewProfileManager(nil, nil, config.RuntimeHost, nil, fs, func([]config.AgentConfig) {})
	pm.home = t.TempDir()

	err := pm.Save(contextProfile("", "/x/ctx"))
	if err == nil || !strings.Contains(err.Error(), `needs runtime "box"`) {
		t.Fatalf("a no-runtime profile on a host-default instance must be refused, got %v", err)
	}
	if _, ok := fs.Get("ctx"); ok {
		t.Fatal("a refused profile must not be stored")
	}
	if err := pm.Save(contextProfile(config.RuntimeBox, "/x/ctx")); err != nil {
		t.Fatalf("an explicit box profile must save: %v", err)
	}
}

func TestSaveRunsContextCheck(t *testing.T) {
	fs := newFakeStore()
	pm := NewProfileManager(nil, nil, config.RuntimeBox, nil, fs, func([]config.AgentConfig) {})
	pm.home = t.TempDir()

	var got []string
	boom := errors.New("context_paths /x/bad: does not exist")
	pm.SetContextCheck(func(p string) error {
		got = append(got, p)
		if p == "/x/bad" {
			return boom
		}
		return nil
	})
	if err := pm.Save(contextProfile("", "/x/good", "/x/bad")); !errors.Is(err, boom) {
		t.Fatalf("the check's error must be returned, got %v", err)
	}
	if !slices.Equal(got, []string{"/x/good", "/x/bad"}) {
		t.Fatalf("the check must run once per path, in order, got %v", got)
	}
	if _, ok := fs.Get("ctx"); ok {
		t.Fatal("a profile the check refused must not be stored")
	}

	got = nil
	if err := pm.Save(contextProfile("", "/x/good", "/x/fine")); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("passing check: want 2 calls, got %v", got)
	}
	// A profile without context_paths never calls the check; nil = pure validation only.
	pm.SetContextCheck(func(string) error { t.Fatal("check called without context_paths"); return nil })
	if err := pm.Save(config.AgentConfig{Name: "plain", Bin: "bash"}); err != nil {
		t.Fatal(err)
	}
	pm.SetContextCheck(nil)
	if err := pm.Save(contextProfile("", "/x/good")); err != nil {
		t.Fatal(err)
	}
}

func TestContextWarnings(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("K=v"), 0o600); err != nil {
		t.Fatal(err)
	}
	clean := t.TempDir()

	fs := newFakeStore()
	pm := NewProfileManager(nil, nil, config.RuntimeBox, nil, fs, func([]config.AgentConfig) {})
	pm.home = t.TempDir()
	p := contextProfile("", dir, clean)

	got := pm.ContextWarnings(p)
	if len(got) != 1 || !strings.Contains(got[0], "look like secrets") || !strings.Contains(got[0], ".env") || !strings.Contains(got[0], dir) {
		t.Fatalf("want one secrets line naming .env, got %q", got)
	}

	// An in-place boxed profile working in the dir: that box can write what this one reads.
	if err := fs.Save(config.AgentConfig{Name: "writer", Bin: "claude", Runtime: config.RuntimeBox, DefaultCwd: dir}); err != nil {
		t.Fatal(err)
	}
	got = pm.ContextWarnings(p)
	want := fmt.Sprintf("context_paths %q overlaps the workspace of boxed profile %q; that box can write what this box reads", dir, "writer")
	if len(got) != 2 || got[1] != want {
		t.Fatalf("want the overlap line second, got %q", got)
	}

	// A profile never overlaps itself.
	p.Name, p.DefaultCwd = "writer", ""
	if got = pm.ContextWarnings(p); len(got) != 1 {
		t.Fatalf("a profile must not warn about its own workspace, got %q", got)
	}

	// Host profiles and clone profiles do not write the dir.
	fs2 := newFakeStore()
	pm2 := NewProfileManager(nil, nil, config.RuntimeBox, nil, fs2, func([]config.AgentConfig) {})
	for _, o := range []config.AgentConfig{
		{Name: "host", Bin: "claude", Runtime: config.RuntimeHost, DefaultCwd: dir},
		{Name: "cl", Bin: "claude", Runtime: config.RuntimeBox, CloneOf: "/home/u/repo", DefaultCwd: dir},
	} {
		if err := fs2.Save(o); err != nil {
			t.Fatal(err)
		}
	}
	if got = pm2.ContextWarnings(contextProfile("", dir)); len(got) != 1 || strings.Contains(got[0], "overlaps") {
		t.Fatalf("host and clone profiles must not produce an overlap line, got %q", got)
	}

	// A path whose own name looks like a secret is flagged without touching the disk.
	if got = pm2.ContextWarnings(contextProfile("", "/x/.env")); len(got) != 1 || !strings.Contains(got[0], "looks like a secret or config file") {
		t.Fatalf("want the name warning, got %q", got)
	}

	// The list of names is capped at 5.
	many := t.TempDir()
	for _, n := range []string{".env", ".env.a", ".env.b", ".env.c", ".env.d", ".env.e", ".env.f"} {
		if err := os.WriteFile(filepath.Join(many, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got = pm2.ContextWarnings(contextProfile("", many))
	if len(got) != 1 || !strings.HasSuffix(strings.TrimSuffix(got[0], "; the box will be able to read and send them"), ", …)") {
		t.Fatalf("more than 5 names must end with an ellipsis, got %q", got)
	}
}

func TestViewsCarryAutoReturnAndNotHome(t *testing.T) {
	on := cloneProfile(config.RuntimeBox)
	off := cloneProfile(config.RuntimeBox)
	off.Name, off.CloneNoAutoReturn = "off", true
	plain := config.AgentConfig{Name: "plain", Bin: "claude", Runtime: config.RuntimeBox}
	pm := NewProfileManager([]config.AgentConfig{on, off, plain}, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	state := &session.NotHomeState{Error: "boom"}
	var asked []config.Clone
	pm.SetNotHome(func(c config.Clone) *session.NotHomeState { asked = append(asked, c); return state })
	got := map[string]ProfileView{}
	for _, v := range pm.Views() {
		got[v.Name] = v
	}
	if !got["cl"].AutoReturn || got["cl"].NotHome != state {
		t.Errorf("switch on: auto_return %v not_home %v", got["cl"].AutoReturn, got["cl"].NotHome)
	}
	if got["off"].AutoReturn || got["off"].NotHome != state {
		t.Errorf("switch off: auto_return %v not_home %v", got["off"].AutoReturn, got["off"].NotHome)
	}
	if got["plain"].AutoReturn || got["plain"].NotHome != nil {
		t.Errorf("non-clone profile carries clone fields: %+v", got["plain"])
	}
	if len(asked) != 2 || asked[0].Dir != "/home/u/clones/repo" {
		t.Errorf("the hook must be asked only for clone profiles, got %+v", asked)
	}
	b, _ := json.Marshal(got["cl"])
	if !strings.Contains(string(b), `"auto_return":true`) || !strings.Contains(string(b), `"not_home":{`) {
		t.Errorf("on-switch clone JSON = %s", b)
	}
	b, _ = json.Marshal(got["plain"])
	if strings.Contains(string(b), "auto_return") || strings.Contains(string(b), "not_home") {
		t.Errorf("non-clone JSON must omit both fields: %s", b)
	}
	// A nil hook reports no not_home; the switch still reports.
	pm.SetNotHome(nil)
	for _, v := range pm.Views() {
		if v.Name == "cl" && (v.NotHome != nil || !v.AutoReturn) {
			t.Errorf("nil hook: %+v", v)
		}
	}
}

func TestProfileViewTouches(t *testing.T) {
	seeds := []config.AgentConfig{
		{
			Name: "ops", Bin: "claude", Runtime: config.RuntimeBox,
			AWS:            nil,
			AWSConnections: []string{"sandbox", "prod-ro"},
			GitHub: &config.GitHubBinding{Owners: []config.GitHubOwner{
				{Owner: "acme", App: "acme-app", Repos: []string{"web"}},
				{Owner: "culiops", Token: "pat-secret-name"},
			}},
			Tokens:       []string{"sentry-read"},
			ContextPaths: []string{"/srv/docs"},
			CloneOf:      "/home/u/repo",
			DefaultCwd:   "/home/u/repo-clone",
		},
		{Name: "plain", Bin: "claude", Runtime: config.RuntimeBox},
		{Name: "hosted", Bin: "bash", Runtime: config.RuntimeHost},
	}
	pm := NewProfileManager(seeds, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	pm.SetEgressAll(true)
	got := map[string]ProfileView{}
	for _, v := range pm.Views() {
		got[v.Name] = v
	}
	v := got["ops"]
	if v.Bin != "claude" || !slices.Equal(v.AWSConnections, []string{"sandbox", "prod-ro"}) ||
		!slices.Equal(v.GitHubOwners, []string{"acme", "culiops"}) || !slices.Equal(v.Tokens, []string{"sentry-read"}) ||
		v.Network != "allowlist" || !slices.Equal(v.ContextPaths, []string{"/srv/docs"}) || v.CloneOf != "/home/u/repo" {
		t.Fatalf("ops view = %+v", v)
	}
	if got["plain"].Network != "open" {
		t.Errorf("an unbound boxed profile's network = %q, want open", got["plain"].Network)
	}
	if got["hosted"].Network != "" {
		t.Errorf("a host-tier profile's network = %q, want empty", got["hosted"].Network)
	}
	// Without mandatory egress an unbound box has no egress control (macOS).
	pm.SetEgressAll(false)
	for _, pv := range pm.Views() {
		switch pv.Name {
		case "plain":
			if pv.Network != "" {
				t.Errorf("unbound box without egress control: network = %q", pv.Network)
			}
		case "ops":
			if pv.Network != "allowlist" {
				t.Errorf("bound box: network = %q", pv.Network)
			}
		}
	}

	// Names only: nothing secret-shaped rides along.
	b, err := json.Marshal(pm.Views())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"role_arn", `"value"`, "secret", "arn:aws", "pat-secret-name", "acme-app"} {
		if strings.Contains(string(b), bad) {
			t.Errorf("profile JSON contains %q: %s", bad, b)
		}
	}
}

// The read view lists what a profile reaches: GitHub repos per owner, the
// effective GitHub permissions, extra domains and the clone's shared paths.
// Names, repos, paths and hosts only: no App name, no token reference.
func TestProfileViewReadViewFields(t *testing.T) {
	seeds := []config.AgentConfig{
		{
			Name: "ops", Bin: "claude", Runtime: config.RuntimeBox,
			GitHub: &config.GitHubBinding{
				Owners: []config.GitHubOwner{
					{Owner: "acme", App: "acme-app", Repos: []string{"web", "api"}},
					{Owner: "culiops", Token: "pat-secret-name", Repos: []string{"box"}},
				},
				Permissions: map[string]string{"contents": "write", "issues": "read", "metadata": "read"},
			},
			EgressAllow: []string{"api.example.com", "*.example.org"},
			CloneOf:     "/home/u/repo", DefaultCwd: "/home/u/repo-clone", CloneShare: []string{"CLAUDE.md"},
		},
		{
			Name: "defaults", Bin: "claude", Runtime: config.RuntimeBox,
			GitHub: &config.GitHubBinding{Owners: []config.GitHubOwner{{Owner: "acme", App: "acme-app", Repos: []string{"web"}}}},
		},
		{Name: "plain", Bin: "claude", Runtime: config.RuntimeBox},
	}
	pm := NewProfileManager(seeds, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	got := map[string]ProfileView{}
	for _, v := range pm.Views() {
		got[v.Name] = v
	}
	v := got["ops"]
	wantRepos := []ProfileGitHubRepos{{Owner: "acme", Repos: []string{"web", "api"}}, {Owner: "culiops", Repos: []string{"box"}}}
	if !reflect.DeepEqual(v.GitHubRepos, wantRepos) {
		t.Errorf("github_repos = %+v, want %+v", v.GitHubRepos, wantRepos)
	}
	if want := map[string]string{"contents": "write", "issues": "read", "metadata": "read"}; !reflect.DeepEqual(v.GitHubPermissions, want) {
		t.Errorf("github_permissions = %v, want %v", v.GitHubPermissions, want)
	}
	if !slices.Equal(v.EgressAllow, []string{"api.example.com", "*.example.org"}) {
		t.Errorf("egress_allow = %v", v.EgressAllow)
	}
	if !slices.Equal(v.CloneShare, []string{"CLAUDE.md"}) {
		t.Errorf("clone_share = %v", v.CloneShare)
	}
	// With no permissions set, the effective (default) set is what is listed.
	if d := got["defaults"].GitHubPermissions; !reflect.DeepEqual(d, config.DefaultGitHubPermissions()) {
		t.Errorf("defaults: github_permissions = %v, want %v", d, config.DefaultGitHubPermissions())
	}
	// A profile with no GitHub binding carries none of it, on the wire either.
	if p := got["plain"]; p.GitHubRepos != nil || p.GitHubPermissions != nil || p.EgressAllow != nil || p.CloneShare != nil {
		t.Errorf("plain profile carries read-view fields: %+v", p)
	}
	raw, err := json.Marshal(got["plain"])
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"github_repos", "github_permissions", "egress_allow", "clone_share"} {
		if strings.Contains(string(raw), key) {
			t.Errorf("plain profile JSON carries %q: %s", key, raw)
		}
	}
	// Names only: no App name, token reference or value rides along.
	all, err := json.Marshal(pm.Views())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"acme-app", "pat-secret-name", "role_arn", `"value"`, "arn:aws"} {
		if strings.Contains(string(all), bad) {
			t.Errorf("profile JSON contains %q: %s", bad, all)
		}
	}
	// The slices are copies: a caller changing the view cannot reach the profile.
	v.GitHubRepos[0].Repos[0] = "changed"
	v.EgressAllow[0] = "changed"
	if again, _ := pm.Get("ops"); again.GitHub.Owners[0].Repos[0] != "web" || again.EgressAllow[0] != "api.example.com" {
		t.Errorf("the view aliases the stored profile: %+v", again)
	}
}

// A legacy inline AWS binding reaches the view by name only: its label (or
// "aws" when it has none, as the Access view names it), the prod flag and the
// elevate window. Never the source profile, role ARNs, region, bucket list or
// egress hosts.
func TestProfileViewSurfacesTheInlineAWSBinding(t *testing.T) {
	seeds := []config.AgentConfig{
		{Name: "legacy", Bin: "claude", Runtime: config.RuntimeBox, AWS: &config.AWSBinding{
			SourceProfile: "src-profile-x", RoleARN: "arn:aws:iam::123456789012:role/ReadOnlyX", Region: "eu-west-1",
			S3Read: []string{"secret-bucket-x"}, Label: "legacy-prod", Prod: true, EgressAllow: []string{"internal.example"},
			Elevate: &config.AWSElevate{RoleARN: "arn:aws:iam::123456789012:role/WriteX", MaxMinutes: 30},
		}},
		{Name: "bare", Bin: "claude", Runtime: config.RuntimeBox, AWS: &config.AWSBinding{
			SourceProfile: "p", RoleARN: "arn:aws:iam::123456789012:role/R", Region: "eu-west-1"}},
		{Name: "none", Bin: "claude", Runtime: config.RuntimeBox},
	}
	pm := NewProfileManager(seeds, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	got := map[string]ProfileView{}
	for _, v := range pm.Views() {
		got[v.Name] = v
	}
	l := got["legacy"].AWSInline
	if l == nil || l.Name != "legacy-prod" || !l.Prod || l.ElevateMaxMinutes != 30 {
		t.Fatalf("legacy inline = %+v", l)
	}
	b := got["bare"].AWSInline
	if b == nil || b.Name != "aws" || b.Prod || b.ElevateMaxMinutes != 0 {
		t.Fatalf("bare inline = %+v", b)
	}
	if got["none"].AWSInline != nil {
		t.Errorf("a profile with no inline binding carries one: %+v", got["none"].AWSInline)
	}
	raw, err := json.Marshal(pm.Views())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"src-profile-x", "arn:aws", "ReadOnlyX", "WriteX", "eu-west-1", "secret-bucket-x", "internal.example", "role_arn", "source_profile", "s3_read"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("profile JSON contains %q: %s", bad, raw)
		}
	}
	if !strings.Contains(string(raw), `"aws_inline":{"name":"legacy-prod","prod":true,"elevate_max_minutes":30}`) {
		t.Errorf("wire shape: %s", raw)
	}
}

func TestProfileViewCarriesTheInlineSSODirectMode(t *testing.T) {
	seeds := []config.AgentConfig{
		{Name: "direct", Bin: "claude", Runtime: config.RuntimeBox, AWS: &config.AWSBinding{
			SourceProfile: "p", Region: "eu-west-1", Mode: config.AWSModeSSODirect, Label: "sbx"}},
		{Name: "plain", Bin: "claude", Runtime: config.RuntimeBox, AWS: &config.AWSBinding{
			SourceProfile: "p", RoleARN: "arn:aws:iam::123456789012:role/R", Region: "eu-west-1"}},
	}
	pm := NewProfileManager(seeds, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	got := map[string]ProfileView{}
	for _, v := range pm.Views() {
		got[v.Name] = v
	}
	if d := got["direct"].AWSInline; d == nil || d.Mode != config.AWSModeSSODirect {
		t.Fatalf("direct inline = %+v", d)
	}
	if p := got["plain"].AWSInline; p == nil || p.Mode != "" {
		t.Fatalf("plain inline = %+v", p)
	}
	raw, err := json.Marshal(got["plain"])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"mode"`) {
		t.Errorf("an assume_role inline binding must not emit mode: %s", raw)
	}
}

func TestProfileViewNetworkReflectsEgressOpen(t *testing.T) {
	seeds := []config.AgentConfig{
		{Name: "closed", Bin: "claude", Runtime: config.RuntimeBox, Tokens: []string{"t"}},
		{Name: "open", Bin: "claude", Runtime: config.RuntimeBox, Tokens: []string{"t"}, EgressOpen: true},
	}
	pm := NewProfileManager(seeds, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	pm.SetEgressAll(true)
	got := map[string]string{}
	for _, v := range pm.Views() {
		got[v.Name] = v.Network
	}
	if got["closed"] != "allowlist" || got["open"] != "open" {
		t.Fatalf("networks = %v, want closed=allowlist open=open", got)
	}
}

// TestViewsSourceTellsBuiltinFromConfigFromRuntime is #15: a profile view says
// where the profile came from, so the first-run welcome can tell a built-in
// profile from one the user wrote. A stored profile is "runtime" even when it
// overrides a seed; a seed named by SetBuiltin is "builtin"; any other seed is
// "config". Only a runtime profile is editable.
func TestViewsSourceTellsBuiltinFromConfigFromRuntime(t *testing.T) {
	seeds := []config.AgentConfig{
		{Name: "claude", Bin: "claude"},
		{Name: "bash", Bin: "bash"},
		{Name: "ops", Bin: "claude"},
	}
	fs := newFakeStore()
	fs.m["mine"] = config.AgentConfig{Name: "mine", Bin: "bash"}
	fs.m["ops"] = config.AgentConfig{Name: "ops", Bin: "claude", Args: []string{"--x"}}
	m := NewProfileManager(seeds, nil, "", nil, fs, func([]config.AgentConfig) {})
	m.SetBuiltin([]string{"claude"})

	want := map[string]struct {
		source   string
		editable bool
	}{
		"claude": {ProfileSourceBuiltin, false},
		"bash":   {ProfileSourceConfig, false},
		"ops":    {ProfileSourceRuntime, true},
		"mine":   {ProfileSourceRuntime, true},
	}
	got := map[string]ProfileView{}
	for _, v := range m.Views() {
		got[v.Name] = v
	}
	for name, w := range want {
		v, ok := got[name]
		if !ok {
			t.Errorf("%s: no view", name)
			continue
		}
		if v.Source != w.source || v.Editable != w.editable {
			t.Errorf("%s: source=%q editable=%v, want source=%q editable=%v", name, v.Source, v.Editable, w.source, w.editable)
		}
		if v.Editable != (v.Source == ProfileSourceRuntime) {
			t.Errorf("%s: editable %v disagrees with source %q", name, v.Editable, v.Source)
		}
	}

	s := &Server{Profiles: m}
	rec := httptest.NewRecorder()
	s.handleProfiles(rec, httptest.NewRequest("GET", "/api/profiles", nil))
	body := rec.Body.String()
	for _, frag := range []string{`"source":"builtin"`, `"source":"config"`, `"source":"runtime"`} {
		if !strings.Contains(body, frag) {
			t.Errorf("GET /api/profiles lacks %s: %s", frag, body)
		}
	}
}

// A manager nobody called SetBuiltin on has no built-in profile: a seed is the
// user's own (config).
func TestViewsSourceWithoutSetBuiltinIsConfig(t *testing.T) {
	m := NewProfileManager([]config.AgentConfig{{Name: "claude", Bin: "claude"}}, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	vs := m.Views()
	if len(vs) != 1 || vs[0].Source != ProfileSourceConfig {
		t.Fatalf("a seed with no SetBuiltin call must be %q, got %+v", ProfileSourceConfig, vs)
	}
	m.SetBuiltin(nil)
	if vs := m.Views(); vs[0].Source != ProfileSourceConfig {
		t.Fatalf("SetBuiltin(nil) must leave a seed %q, got %q", ProfileSourceConfig, vs[0].Source)
	}
}

func TestSaveChecksDefaultCwdAgainstTheLiveShares(t *testing.T) {
	home := resolvedDir(t)
	pm := NewProfileManager(nil, nil, config.RuntimeBox, nil, newFakeStore(), func([]config.AgentConfig) {})
	pm.home = home
	shared, other := filepath.Join(home, "shared"), filepath.Join(home, "other")
	for _, d := range []string{shared, other} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pm.SetShares(func() (box.ShareView, error) {
		return box.ShareView{Applied: []box.VMShare{{Path: shared, Writable: true}}, Home: home}, nil
	})
	p := config.AgentConfig{Name: "p", Bin: "claude", Runtime: config.RuntimeBox, DefaultCwd: other}
	err := pm.Save(p)
	if err == nil || !strings.Contains(err.Error(), "not shared with the box VM: add it in Settings › VM folders") {
		t.Fatalf("an unshared default_cwd must be refused with the share reason, got %v", err)
	}
	p.DefaultCwd = shared
	if err := pm.Save(p); err != nil {
		t.Fatalf("a shared default_cwd should save: %v", err)
	}
}

func resolvedDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestMergeProfilesMatchesManagerAll(t *testing.T) {
	seeds := []config.AgentConfig{{Name: "claude", Bin: "claude", Options: map[string]any{"skip": true}}, {Name: "bash", Bin: "bash"}}
	opts := []config.OptionDef{{Key: "skip", Label: "Skip", Type: "bool", Flag: "-x"}}
	fs := newFakeStore()
	fs.m["bash"] = config.AgentConfig{Name: "bash", Bin: "bash", DefaultCwd: "/w"}
	fs.m["zed"] = config.AgentConfig{Name: "zed", Bin: "bash"}
	fs.m["amy"] = config.AgentConfig{Name: "amy", Bin: "bash"}
	pm := NewProfileManager(seeds, opts, "", nil, fs, func([]config.AgentConfig) {})
	got := MergeProfiles(seeds, fs.List(), opts)
	if want := pm.All(); !reflect.DeepEqual(got, want) {
		t.Fatalf("MergeProfiles\n got %+v\nwant %+v", got, want)
	}
}

// #291: a profiles.json from a newer CuliBox. The store loads empty, so a
// config seed would otherwise run from its plain config values, ignoring a
// stored override the user may have tightened. Every spawn is refused; a normal
// file spawns as before.
func newerGateLauncher(t *testing.T, profiles string) *launcher.AgentLauncher {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.json")
	if profiles != "" {
		if err := os.WriteFile(path, []byte(profiles), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, parseErr, err := store.OpenOrReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	seeds := []config.AgentConfig{{Name: "claude", Bin: "/bin/cat", Base: "clean"}}
	l := launcher.New(seeds, nil, nil)
	l.SetProfilesGate(launcher.ProfilesNewerGate(parseErr))
	NewProfileManager(seeds, nil, config.RuntimeHost, nil, st, l.SetAgents)
	return l
}

func TestNewerProfilesFileRefusesConfigSeedSpawn(t *testing.T) {
	l := newerGateLauncher(t, `{"version": 2, "profiles": [{"name": "claude", "runtime": "box"}]}`)
	_, _, err := l.Build(launcher.SpawnSpec{Agent: "claude", Runtime: config.RuntimeHost})
	if !errors.Is(err, launcher.ErrProfilesNewer) {
		t.Fatalf("Build = %v, want ErrProfilesNewer", err)
	}
	for _, want := range []string{"profiles.json", "format version 2", "this build knows 1", "docs/UPGRADING.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q lacks %q", err.Error(), want)
		}
	}
	rec := httptest.NewRecorder()
	writeSpawnErr(rec, "claude", err)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "docs/UPGRADING.md") {
		t.Errorf("API: %d %s", rec.Code, rec.Body.String())
	}
	// The wrapped error names the host path of profiles.json; the response must not.
	if strings.Contains(rec.Body.String(), string(filepath.Separator)+"profiles.json") || strings.Contains(rec.Body.String(), os.TempDir()) {
		t.Errorf("API body names a host path: %s", rec.Body.String())
	}
}

func TestNormalProfilesFileStillSpawnsConfigSeed(t *testing.T) {
	for name, content := range map[string]string{
		"no file":          "",
		"plain list":       `[{"name": "claude", "bin": "/bin/cat", "base": "clean"}]`,
		"explicit version": `{"version": 1, "profiles": [{"name": "x", "bin": "/bin/cat"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			l := newerGateLauncher(t, content)
			if _, _, err := l.Build(launcher.SpawnSpec{Agent: "claude", Runtime: config.RuntimeHost}); err != nil {
				t.Fatalf("Build = %v", err)
			}
		})
	}
}

// #342: a profile that carries only the legacy bypass bool is offered the
// Permission mode enum (blank), and the view names the legacy key for the UI.
func TestViewLegacyProfileOffersPermissionMode(t *testing.T) {
	opts := append([]config.OptionDef{
		{Key: "skip_permissions", Label: "Skip permissions", Type: config.OptBool, Flag: "--dangerously-skip-permissions", Bins: []string{"claude"}},
	}, config.Defaults().Options...)
	seeds := []config.AgentConfig{
		{Name: "legacy", Bin: "claude", Options: map[string]any{"skip_permissions": true}},
		{Name: "plain", Bin: "claude"},
	}
	m := NewProfileManager(seeds, opts, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	views := map[string]ProfileView{}
	for _, v := range m.Views() {
		views[v.Name] = v
	}
	l := views["legacy"]
	if l.LegacyBypassKey != "skip_permissions" {
		t.Errorf("legacy_bypass_key = %q, want skip_permissions", l.LegacyBypassKey)
	}
	var mode *ProfileOption
	for i := range l.Options {
		if l.Options[i].Key == config.PermissionModeKey {
			mode = &l.Options[i]
		}
	}
	if mode == nil {
		t.Fatalf("legacy profile does not offer permission_mode: %+v", l.Options)
	}
	if mode.Value != "" {
		t.Errorf("permission_mode value = %v, want blank", mode.Value)
	}
	if p := views["plain"]; p.LegacyBypassKey != "" {
		t.Errorf("plain profile legacy_bypass_key = %q, want empty", p.LegacyBypassKey)
	}
	for _, o := range views["plain"].Options {
		if o.Key == config.PermissionModeKey {
			t.Errorf("an unset plain profile must not offer permission_mode")
		}
	}
}

// #342: a config.json permission_mode def without bypassPermissions does not own the
// legacy bool: no legacy key, no Permission mode offered, and the bool is a marked
// option of its own (the UI counts a ticked one as Bypass by the mark).
func TestViewLegacyBoolNotOwnedByEnumWithoutBypass(t *testing.T) {
	opts := []config.OptionDef{
		{Key: "skip_permissions", Label: "Skip permissions", Type: config.OptBool, Flag: "--dangerously-skip-permissions", Bins: []string{"claude"}},
		{Key: config.PermissionModeKey, Label: "Permission mode", Type: config.OptEnum, Flag: "--permission-mode",
			Choices: []string{"default", "acceptEdits", "plan"}, Bins: []string{"claude"}},
	}
	seeds := []config.AgentConfig{{Name: "legacy", Bin: "claude", Options: map[string]any{"skip_permissions": true}}}
	m := NewProfileManager(seeds, opts, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	v := m.Views()[0]
	if v.LegacyBypassKey != "" {
		t.Errorf("legacy_bypass_key = %q, want empty", v.LegacyBypassKey)
	}
	if len(v.Options) != 1 || v.Options[0].Key != "skip_permissions" || !v.Options[0].Bypass || v.Options[0].Value != true {
		t.Errorf("options = %+v, want only the marked legacy bool", v.Options)
	}
}

// #342: the critical-rm row carries its tier, and the host block names it.
func TestViewCriticalRmTierAndHostBlock(t *testing.T) {
	seeds := []config.AgentConfig{{Name: "c", Bin: "claude", Options: map[string]any{
		"permission_mode": "bypassPermissions", "skip_critical_rm_prompt": true,
	}}}
	m := NewProfileManager(seeds, config.Defaults().Options, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	vs := m.Views()
	if len(vs) != 1 {
		t.Fatalf("views = %d", len(vs))
	}
	var row *ProfileOption
	for i := range vs[0].Options {
		if vs[0].Options[i].Key == config.CriticalRmKey {
			row = &vs[0].Options[i]
		}
	}
	if row == nil {
		t.Fatalf("critical-rm option not offered: %+v", vs[0].Options)
	}
	if row.Tier != "box" {
		t.Errorf("tier = %q, want box", row.Tier)
	}
	if want := `"Skip the critical-rm prompt" runs only in a box`; vs[0].HostBlock != want {
		t.Errorf("host_block = %q, want %q", vs[0].HostBlock, want)
	}
	for _, o := range vs[0].Options {
		if o.Key != config.CriticalRmKey && o.Tier != "" {
			t.Errorf("option %s has tier %q, want none", o.Key, o.Tier)
		}
	}
}

// #342 Review Focus 5: a config.json permission_mode def replaces the built-in.
// The option list is built the production way (config.Load adds the built-ins),
// so one permission_mode def exists and it is the user's.
func TestViewUserPermissionModeDefWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfgJSON := `{"options":[{"key":"permission_mode","label":"Mode","type":"enum",
		"choices":["default","plan","bypassPermissions"],"flag":"--permission-mode","bins":["claude"]}]}`
	if err := os.WriteFile(path, []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	seeds := []config.AgentConfig{{Name: "c", Bin: "claude", Options: map[string]any{"permission_mode": "plan"}}}
	m := NewProfileManager(seeds, cfg.Options, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	views := m.Views()
	if len(views) != 1 {
		t.Fatalf("views = %d", len(views))
	}
	n := 0
	for _, o := range views[0].Options {
		if o.Key != "permission_mode" {
			continue
		}
		n++
		if got := strings.Join(o.Choices, ","); got != "default,plan,bypassPermissions" {
			t.Errorf("choices = %s, want the user's three", got)
		}
		if o.Label != "Mode" {
			t.Errorf("label = %q, want the user's Mode", o.Label)
		}
	}
	if n != 1 {
		t.Fatalf("permission_mode rows = %d, want exactly 1", n)
	}
}
