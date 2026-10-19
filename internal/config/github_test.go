package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func ghOwner(owner string, repos ...string) GitHubOwner {
	return GitHubOwner{Owner: owner, App: "main", Repos: repos}
}

func goodGitHub() *GitHubBinding {
	return &GitHubBinding{Owners: []GitHubOwner{
		ghOwner("culiops", "agent-box", "culiops-agent"),
		ghOwner("chiplonton", "dotfiles"),
	}}
}

func TestGitHubBindingValidate(t *testing.T) {
	many := make([]string, 101)
	for i := range many {
		many[i] = fmt.Sprintf("repo-%d", i)
	}
	cases := []struct {
		name string
		mut  func(*GitHubBinding)
		want string // "" = valid; else a substring naming the field
	}{
		{"default permissions, two owners", func(b *GitHubBinding) {}, ""},
		{"explicit default permissions", func(b *GitHubBinding) { b.Permissions = DefaultGitHubPermissions() }, ""},
		{"statuses is allowed", func(b *GitHubBinding) { b.Permissions = map[string]string{"statuses": "read"} }, ""},
		{"empty non-nil permissions", func(b *GitHubBinding) { b.Permissions = map[string]string{} }, "github.permissions: empty"},
		{"nil permissions are the defaults", func(b *GitHubBinding) { b.Permissions = nil }, ""},
		{"write permission", func(b *GitHubBinding) { b.Permissions = map[string]string{"contents": "write"} }, "github.permissions"},
		{"unknown permission", func(b *GitHubBinding) { b.Permissions = map[string]string{"administration": "read"} }, "github.permissions"},
		{"bad owner", func(b *GitHubBinding) { b.Owners[0].Owner = "-bad" }, "github.owners[0].owner"},
		{"bad repo", func(b *GitHubBinding) { b.Owners[0].Repos = []string{"a/b"} }, "github.owners[0].repos[0]"},
		{"dot repo", func(b *GitHubBinding) { b.Owners[0].Repos = []string{".."} }, "github.owners[0].repos[0]"},
		{"single dot repo", func(b *GitHubBinding) { b.Owners[0].Repos = []string{"."} }, "github.owners[0].repos[0]"},
		{"101 repos", func(b *GitHubBinding) { b.Owners[0].Repos = many }, "github.owners[0].repos"},
		{"duplicate owner, case-insensitive", func(b *GitHubBinding) {
			b.Owners = []GitHubOwner{ghOwner("CuliOps", "a"), ghOwner("culiops", "b")}
		}, "github.owners[1].owner"},
		{"empty repos", func(b *GitHubBinding) { b.Owners[1].Repos = nil }, "github.owners[1].repos"},
		{"duplicate repo", func(b *GitHubBinding) { b.Owners[0].Repos = []string{"a", "a"} }, "github.owners[0].repos[1]"},
		{"zero owners", func(b *GitHubBinding) { b.Owners = nil }, "github.owners"},
		{"empty app", func(b *GitHubBinding) { b.Owners[0].App = "" }, "github.owners[0].app"},
		{"bad egress", func(b *GitHubBinding) { b.EgressAllow = []string{"not a host"} }, "github.egress_allow[0]"},
		{"too many egress", func(b *GitHubBinding) {
			for i := 0; i < maxEgressAllow+1; i++ {
				b.EgressAllow = append(b.EgressAllow, fmt.Sprintf("h%d.example.com", i))
			}
		}, "github.egress_allow"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := goodGitHub()
			tc.mut(b)
			err := b.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error naming %q, got %v", tc.want, err)
			}
		})
	}
}

func TestGitHubBindingHashStable(t *testing.T) {
	a := GitHubBinding{
		Owners:      []GitHubOwner{ghOwner("culiops", "x", "y"), ghOwner("Zed", "q")},
		EgressAllow: []string{"a.example.com", "b.example.com"},
	}
	b := GitHubBinding{
		Owners:      []GitHubOwner{ghOwner("Zed", "q"), ghOwner("culiops", "y", "x")},
		EgressAllow: []string{"b.example.com", "a.example.com"},
	}
	if a.Hash() != b.Hash() {
		t.Fatalf("reordering must not change the hash: %s vs %s", a.Hash(), b.Hash())
	}
	if len(a.Hash()) != 12 {
		t.Fatalf("hash must be 12 hex, got %q", a.Hash())
	}
	withDefaults := a
	withDefaults.Permissions = DefaultGitHubPermissions()
	if a.Hash() != withDefaults.Hash() {
		t.Fatal("empty permissions and explicit defaults must hash alike")
	}
	more := GitHubBinding{
		Owners:      []GitHubOwner{ghOwner("culiops", "x", "y", "z"), ghOwner("Zed", "q")},
		EgressAllow: a.EgressAllow,
	}
	if a.Hash() == more.Hash() {
		t.Fatal("adding a repo must change the hash")
	}
	// Hash must not reorder the caller's slices.
	if a.Owners[0].Owner != "culiops" || a.Owners[0].Repos[0] != "x" {
		t.Fatal("Hash mutated its receiver")
	}
}

func TestBindingsHashEqualsAWSWhenNoGitHub(t *testing.T) {
	b := *goodBinding()
	if got, want := (Bindings{AWS: &b}).Hash(), b.Hash(); got != want {
		t.Fatalf("AWS-only Bindings hash %s != AWSBinding hash %s", got, want)
	}
	if (Bindings{}).Any() || (Bindings{}).Hash() != "" {
		t.Fatal("empty Bindings must be !Any and hash to \"\"")
	}
	gh := goodGitHub()
	both := Bindings{AWS: &b, GitHub: gh}
	if !both.Any() {
		t.Fatal("both set must be Any")
	}
	if both.Hash() == b.Hash() || both.Hash() == gh.Hash() || len(both.Hash()) != 12 {
		t.Fatalf("combined hash must be a new 12-hex value, got %s", both.Hash())
	}
	if (Bindings{GitHub: gh}).Hash() == both.Hash() {
		t.Fatal("adding AWS must change the combined hash")
	}
}

func TestValidateGitHubRequiresBox(t *testing.T) {
	p := AgentConfig{Name: "c", Bin: "claude", Runtime: RuntimeHost, GitHub: goodGitHub()}
	if err := ValidateProfile(p, nil); err == nil || !strings.Contains(err.Error(), "box") {
		t.Fatalf("host runtime must be refused, got %v", err)
	}
	p.Runtime = ""
	if err := ValidateProfile(p, nil); err == nil || !strings.Contains(err.Error(), "runtime: box") {
		t.Fatalf("empty runtime must be refused, got %v", err)
	}
	p.Runtime = RuntimeBox
	if err := ValidateProfile(p, nil); err != nil {
		t.Fatal(err)
	}
}

func TestValidateGitHubApps(t *testing.T) {
	ok := GitHubApp{Name: "main", ClientID: "Iv1.abc123", PrivateKey: KeyRef{Type: "file", Path: "/etc/ab/app.pem"}}
	if err := ValidateGitHubApps([]GitHubApp{ok}); err != nil {
		t.Fatal(err)
	}
	mut := func(f func(*GitHubApp)) []GitHubApp { a := ok; f(&a); return []GitHubApp{a} }
	cases := []struct {
		name string
		apps []GitHubApp
		want string
	}{
		{"duplicate name", []GitHubApp{ok, ok}, "duplicate"},
		{"relative path", mut(func(a *GitHubApp) { a.PrivateKey.Path = "app.pem" }), "absolute"},
		{"store type", mut(func(a *GitHubApp) { a.PrivateKey = KeyRef{Type: "store"} }), "private_key.type"},
		{"empty client_id", mut(func(a *GitHubApp) { a.ClientID = "" }), "client_id"},
		{"bad client_id", mut(func(a *GitHubApp) { a.ClientID = "a b" }), "client_id"},
		{"bad name", mut(func(a *GitHubApp) { a.Name = "a b" }), "name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateGitHubApps(tc.apps)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestCheckGitHubApps(t *testing.T) {
	a := AgentConfig{Name: "c", GitHub: goodGitHub()}
	apps := []GitHubApp{{Name: "main", ClientID: "x", PrivateKey: KeyRef{Type: "file", Path: "/k"}}}
	if err := CheckGitHubApps(a, apps); err != nil {
		t.Fatal(err)
	}
	a.GitHub.Owners[1].App = "ghost"
	err := CheckGitHubApps(a, apps)
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want error naming the missing app, got %v", err)
	}
	if err := CheckGitHubApps(AgentConfig{Name: "n"}, nil); err != nil {
		t.Fatalf("no binding is fine: %v", err)
	}
}

func TestConfigValidateChecksGitHub(t *testing.T) {
	app := GitHubApp{Name: "main", ClientID: "x", PrivateKey: KeyRef{Type: "file", Path: "/k"}}
	cfg := Config{BindAddr: "127.0.0.1:0", PasswordEnv: "X", GitHubApps: []GitHubApp{app},
		Agents: []AgentConfig{{Name: "c", Bin: "claude", Runtime: RuntimeBox, GitHub: goodGitHub()}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.GitHubApps = nil
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), `agent "c"`) {
		t.Fatalf("unknown app must be refused naming the agent, got %v", err)
	}
	cfg.GitHubApps = []GitHubApp{app, app}
	if err := cfg.Validate(); err == nil {
		t.Fatal("duplicate app names must be refused")
	}
}

func TestConfigRoundTripGitHub(t *testing.T) {
	in := Config{BindAddr: "127.0.0.1:0", PasswordEnv: "X",
		GitHubApps: []GitHubApp{{Name: "main", ClientID: "Iv1.x", PrivateKey: KeyRef{Type: "file", Path: "/k.pem"}}},
		Agents:     []AgentConfig{{Name: "c", Bin: "claude", Runtime: RuntimeBox, GitHub: goodGitHub()}}}
	j, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Config
	if err := json.Unmarshal(j, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.GitHubApps) != 1 || out.GitHubApps[0] != in.GitHubApps[0] {
		t.Fatalf("apps lost: %+v", out.GitHubApps)
	}
	if out.Agents[0].GitHub == nil || out.Agents[0].GitHub.Hash() != in.Agents[0].GitHub.Hash() {
		t.Fatalf("binding lost: %+v", out.Agents[0].GitHub)
	}
	if err := out.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubCloneIsDeep(t *testing.T) {
	b := goodGitHub()
	b.Permissions = DefaultGitHubPermissions()
	b.EgressAllow = []string{"a.example.com"}
	c := b.Clone()
	c.Owners[0].Repos[0] = "changed"
	c.Permissions["contents"] = "write"
	c.EgressAllow[0] = "z"
	if b.Owners[0].Repos[0] == "changed" || b.Permissions["contents"] != "read" || b.EgressAllow[0] != "a.example.com" {
		t.Fatal("Clone shares memory with the original")
	}
}

func TestGitHubOwnerEntryFoldsCase(t *testing.T) {
	b := goodGitHub()
	if e, ok := b.OwnerEntry("CULIOPS"); !ok || e.Owner != "culiops" {
		t.Fatalf("got %+v %v", e, ok)
	}
	if _, ok := b.OwnerEntry("nobody"); ok {
		t.Fatal("unexpected hit")
	}
}

func TestParseGitHubRepo(t *testing.T) {
	ok := []struct{ in, owner, repo string }{
		{"https://github.com/culiops/agent-box", "culiops", "agent-box"},
		{"https://github.com/culiops/agent-box.git", "culiops", "agent-box"},
		{"https://github.com/culiops/agent-box/", "culiops", "agent-box"},
		{"https://github.com/culiops/agent-box.git/", "culiops", "agent-box"},
		{"https://GitHub.com/culiops/agent-box", "culiops", "agent-box"},
		{"git@github.com:culiops/agent-box.git", "culiops", "agent-box"},
		{"git@github.com:culiops/agent-box", "culiops", "agent-box"},
		{"ssh://git@github.com/culiops/agent-box.git", "culiops", "agent-box"},
		{"ssh://git@github.com/culiops/agent-box", "culiops", "agent-box"},
	}
	for _, c := range ok {
		o, r, good := ParseGitHubRepo(c.in)
		if !good || o != c.owner || r != c.repo {
			t.Errorf("ParseGitHubRepo(%q) = %q, %q, %v; want %q, %q, true", c.in, o, r, good, c.owner, c.repo)
		}
	}
	bad := []string{
		"",
		"/home/u/repo",
		"https://gitlab.com/culiops/agent-box",
		"git@gitlab.com:culiops/agent-box.git",
		"https://github.com.evil.com/culiops/agent-box",
		"https://evil.com/github.com/culiops/agent-box",
		"git@github.com.evil.com:culiops/agent-box.git",
		"https://github.com/-bad/agent-box",
		"https://github.com/culiops/agent box",
		"https://github.com/culiops",
		"https://github.com/culiops/agent-box/extra",
		"https://github.com/culiops/agent-box/pulls",
		"https://github.com/culiops/..",
		"https://github.com/culiops/.git",
		"https://user:pw@github.com/culiops/agent-box",
		"https://token@github.com/culiops/agent-box",
		"https://github.com:8443/culiops/agent-box",
		"https://github.com/culiops/agent-box?x=1",
		"https://github.com/culiops/agent-box#frag",
		"http://github.com/culiops/agent-box",
		"ssh://git@github.com:22/culiops/agent-box",
		"ssh://root@github.com/culiops/agent-box",
		"git@github.com:culiops/agent-box/extra",
		"git@github.com:/culiops/agent-box",
		"root@github.com:culiops/agent-box",
		"file:///srv/git/github.com/culiops/agent-box",
		"https://github.com/culiops/agent-box\n",
	}
	for _, in := range bad {
		if o, r, good := ParseGitHubRepo(in); good {
			t.Errorf("ParseGitHubRepo(%q) = %q, %q, true; want refusal", in, o, r)
		}
	}
}
