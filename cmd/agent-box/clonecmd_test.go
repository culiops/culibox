package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/clone"
	"github.com/culiops/agent-box/internal/config"
)

func TestParseCloneFetch(t *testing.T) {
	ok := []struct {
		argv []string
		want cliArgs
	}{
		{[]string{"clone", "fetch", "p", "b"}, cliArgs{Cmd: "clone", Sub: "fetch", Profile: "p", Branch: "b", Base: "main"}},
		{[]string{"clone", "fetch", "-base", "dev", "p", "b"}, cliArgs{Cmd: "clone", Sub: "fetch", Profile: "p", Branch: "b", Base: "dev"}},
		{[]string{"clone", "fetch", "-config", "/x.json", "p", "feat/x"},
			cliArgs{Cmd: "clone", Sub: "fetch", ConfigPath: "/x.json", ConfigSet: true, Profile: "p", Branch: "feat/x", Base: "main"}},
	}
	for _, c := range ok {
		got, err := parseArgs(c.argv)
		if err != nil || got != c.want {
			t.Errorf("parseArgs(%q) = %+v,%v want %+v", c.argv, got, err, c.want)
		}
	}
	for _, bad := range [][]string{
		{"clone", "fetch", "p"},                    // missing branch
		{"clone", "fetch", "p", "b", "extra"},      // a third positional
		{"clone", "status"},                        // unknown subcommand
		{"clone"},                                  // no subcommand
		{"clone", "-base", "x"},                    // flag where the subcommand belongs
		{"box", "setup", "-base", "x"},             // -base is for clone only
		{"serve", "-base", "x"},                    // ditto
		{"clone", "fetch", "p", "b", "-base", "x"}, // flags after positionals
		{"clone", "fetch", "-base", "", "p", "b"},  // an empty base
		{"clone", "fetch", "-rebuild", "p", "b"},   // box flags do not apply
	} {
		if _, err := parseArgs(bad); err == nil {
			t.Errorf("parseArgs(%q): want error", bad)
		}
	}
}

// cloneEnv is a data root with a store holding one clone profile and a config
// that seeds another, and a stub for clone.Fetch that records its request.
type cloneEnv struct {
	cfg  config.Config
	home string
	data string
	req  clone.FetchRequest
	res  clone.FetchResult
	err  error
}

func newCloneEnv(t *testing.T, stored []config.AgentConfig, seeds []config.AgentConfig) *cloneEnv {
	t.Helper()
	root := t.TempDir()
	e := &cloneEnv{home: filepath.Join(root, "home"), data: filepath.Join(root, "data")}
	if err := os.MkdirAll(e.data, 0o700); err != nil {
		t.Fatal(err)
	}
	if stored != nil {
		b, _ := json.Marshal(stored)
		if err := os.WriteFile(filepath.Join(e.data, "profiles.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e.cfg = config.Config{DataDir: e.data, Agents: seeds, BoxImage: "culiops/agent-box:20261018", EnvLabel: "DEV"}
	oldFetch, oldBack := cloneFetch, cloneBackends
	cloneFetch = func(_ context.Context, _ clone.Git, _ clone.Docker, r clone.FetchRequest) (clone.FetchResult, error) {
		e.req = r
		return e.res, e.err
	}
	cloneBackends = func(config.Config) (clone.Git, clone.Docker, error) { return nil, nil, nil }
	t.Cleanup(func() { cloneFetch, cloneBackends = oldFetch, oldBack })
	return e
}

func cloneProfile(name, of, dir string) config.AgentConfig {
	return config.AgentConfig{Name: name, Bin: "claude", CloneOf: of, DefaultCwd: dir}
}

func runCloneArgs(e *cloneEnv, profile, branch, base string) (int, string) {
	var out bytes.Buffer
	rc := runClone(cliArgs{Cmd: "clone", Sub: "fetch", Profile: profile, Branch: branch, Base: base}, e.cfg, e.home, nil, &out)
	return rc, out.String()
}

func TestRunCloneUnknownProfile(t *testing.T) {
	e := newCloneEnv(t, nil, nil)
	rc, out := runCloneArgs(e, "nope", "b", "main")
	if rc != 1 || out != "agent-box clone: unknown profile \"nope\"\n" {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
}

func TestRunCloneNoCloneOf(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{{Name: "plain", Bin: "claude"}})
	rc, out := runCloneArgs(e, "plain", "b", "main")
	if rc != 1 || out != "agent-box clone: profile \"plain\" has no clone_of\n" {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
	if e.req.Branch != "" {
		t.Fatal("Fetch must not run for a profile without clone_of")
	}
}

func TestRunCloneInvalidProfile(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{cloneProfile("rel", "relative/path", "/c/d")})
	rc, out := runCloneArgs(e, "rel", "b", "main")
	if rc != 1 || !strings.HasPrefix(out, "agent-box clone: ") || !strings.Contains(out, "clone_of") {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
}

func TestRunCloneStoreOverridesSeed(t *testing.T) {
	stored := []config.AgentConfig{cloneProfile("p", "/src/new", "/clones/new")}
	seeds := []config.AgentConfig{cloneProfile("p", "/src/old", "/clones/old")}
	e := newCloneEnv(t, stored, seeds)
	e.res = clone.FetchResult{Ref: "refs/heads/box/b"}
	rc, out := runCloneArgs(e, "p", "b", "dev")
	if rc != 0 {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
	r := e.req
	if r.Clone.Of != "/src/new" || r.Clone.Dir != "/clones/new" {
		t.Errorf("clone spec = %+v, want the store's", r.Clone)
	}
	if r.Branch != "b" || r.Base != "dev" {
		t.Errorf("branch/base = %q/%q", r.Branch, r.Base)
	}
	if r.Image != "culiops/agent-box:20261018" {
		t.Errorf("image = %q, want the global box_image", r.Image)
	}
	if r.Instance != "DEV" {
		t.Errorf("instance = %q", r.Instance)
	}
	if r.TempDir != filepath.Join(e.data, "clone-fetch") {
		t.Errorf("TempDir = %q, want under the data dir", r.TempDir)
	}
	if r.Home != e.home {
		t.Errorf("Home = %q", r.Home)
	}
	if len(r.Protected) == 0 {
		t.Error("Protected is empty: the data dir at least must be protected")
	}
}

func TestRunCloneFallsBackToSeed(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{cloneProfile("s", "/src/s", "/clones/s")})
	rc, _ := runCloneArgs(e, "s", "b", "main")
	if rc != 0 || e.req.Clone.Of != "/src/s" {
		t.Fatalf("rc=%d req=%+v", rc, e.req)
	}
}

func TestRunCloneOutput(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{cloneProfile("p", "/src/r", "/clones/r")})
	e.res = clone.FetchResult{
		Ref:      "refs/heads/box/feat/x",
		Commits:  []string{"abc1234 add thing", "def5678 fix thing"},
		Warnings: []string{"1 commit carries an attribution trailer"},
	}
	rc, out := runCloneArgs(e, "p", "feat/x", "main")
	want := "fetched box/feat/x (2 commit(s) not on main):\n" +
		"  abc1234 add thing\n" +
		"  def5678 fix thing\n" +
		"WARN: 1 commit carries an attribution trailer\n" +
		"review: git -C /src/r log -p main..box/feat/x; merge per your review policy\n"
	if rc != 0 || out != want {
		t.Fatalf("rc=%d\ngot:\n%s\nwant:\n%s", rc, out, want)
	}
}

func TestRunCloneFetchError(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{cloneProfile("p", "/src/r", "/clones/r")})
	e.err = errors.New("nothing to fetch")
	rc, out := runCloneArgs(e, "p", "b", "main")
	if rc != 1 || out != "agent-box clone: nothing to fetch\n" {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
}

func TestRunCloneBackendError(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{cloneProfile("p", "/src/r", "/clones/r")})
	cloneBackends = func(config.Config) (clone.Git, clone.Docker, error) { return nil, nil, errors.New("git not found") }
	rc, out := runCloneArgs(e, "p", "b", "main")
	if rc != 1 || out != "agent-box clone: git not found\n" {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
}

func TestRunCloneUnreadableStore(t *testing.T) {
	e := newCloneEnv(t, nil, nil)
	if err := os.WriteFile(filepath.Join(e.data, "profiles.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	rc, out := runCloneArgs(e, "p", "b", "main")
	if rc != 1 || !strings.HasPrefix(out, "agent-box clone: ") {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
	// Read-only: a corrupt store is reported, never quarantined or rewritten.
	if b, _ := os.ReadFile(filepath.Join(e.data, "profiles.json")); string(b) != "{not json" {
		t.Fatalf("profiles.json was modified: %q", b)
	}
}

func TestRunCloneOutputQuotesTheRepoPath(t *testing.T) {
	e := newCloneEnv(t, nil, []config.AgentConfig{cloneProfile("p", "/my repos/it's", "/clones/r")})
	e.res = clone.FetchResult{Ref: "refs/heads/box/b", Commits: []string{"abc1234 x"}}
	rc, out := runCloneArgs(e, "p", "b", "main")
	want := "review: git -C '/my repos/it'\\''s' log -p main..box/b; merge per your review policy\n"
	if rc != 0 || !strings.HasSuffix(out, want) {
		t.Fatalf("rc=%d out=%q want suffix %q", rc, out, want)
	}
}
