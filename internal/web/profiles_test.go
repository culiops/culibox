package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/account"
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
