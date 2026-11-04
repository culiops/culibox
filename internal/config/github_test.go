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
		{"contents write", func(b *GitHubBinding) { b.Permissions = map[string]string{"contents": "write"} }, ""},
		{"pull_requests and issues write", func(b *GitHubBinding) {
			b.Permissions = map[string]string{"pull_requests": "write", "issues": "write", "metadata": "read"}
		}, ""},
		{"metadata write", func(b *GitHubBinding) { b.Permissions = map[string]string{"metadata": "write"} }, "github.permissions"},
		{"actions write", func(b *GitHubBinding) { b.Permissions = map[string]string{"actions": "write"} }, "github.permissions"},
		{"statuses write", func(b *GitHubBinding) { b.Permissions = map[string]string{"statuses": "write"} }, "github.permissions"},
		{"admin level", func(b *GitHubBinding) { b.Permissions = map[string]string{"contents": "admin"} }, "github.permissions"},
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
		{"empty app", func(b *GitHubBinding) { b.Owners[0].App = "" }, "github.owners[0]: set exactly one of app or token"},
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
		{"env type", mut(func(a *GitHubApp) { a.PrivateKey = KeyRef{Type: "env", Var: "X"} }), "private_key.type"},
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

func TestKeyRefValidate(t *testing.T) {
	bad := []KeyRef{
		{Type: "store"},
		{Type: "store", ID: "x", Path: "/a"},
		{Type: ""},
		{Type: "file", Path: "relative"},
		{Type: "file", Path: ""},
		{Type: "file", Path: "/a", Var: "X"},
		{Type: "env", Var: "1BAD"},
		{Type: "env", Var: ""},
		{Type: "env", Var: "AB_X"},
		{Type: "env", Var: "X", Path: "/a"},
	}
	for _, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("%+v: want error", r)
		}
	}
	if err := (KeyRef{Type: "x"}).Validate(); err == nil || err.Error() != `unsupported reference type "x" (want "file", "env" or "store")` {
		t.Errorf("unknown type message: %v", err)
	}
	for _, r := range []KeyRef{{Type: "file", Path: "/a/b"}, {Type: "env", Var: "MY_TOKEN"}} {
		if err := r.Validate(); err != nil {
			t.Errorf("%+v: %v", r, err)
		}
	}
	if s := (KeyRef{Type: "file", Path: "/a/b"}).String(); s != "file /a/b" {
		t.Errorf("String = %q", s)
	}
	if s := (KeyRef{Type: "env", Var: "MY_TOKEN"}).String(); s != "env MY_TOKEN" {
		t.Errorf("String = %q", s)
	}
	if !ValidEnvName("A_b1") || ValidEnvName("1A") || ValidEnvName("") || ValidEnvName("A-B") {
		t.Error("ValidEnvName")
	}
}

// The hashes below were captured before Bindings gained token fields. A session
// with no tokens must keep its box key and binding directory across the upgrade.
func TestBindingsHashGoldenWithoutTokens(t *testing.T) {
	cases := []struct {
		name string
		b    Bindings
		want string
	}{
		{"aws only", Bindings{AWS: goodBinding()}, "e7e58e9b264b"},
		{"github only", Bindings{GitHub: goodGitHub()}, "448d3a22b6f6"},
		{"aws and github", Bindings{AWS: goodBinding(), GitHub: goodGitHub()}, "198429c07173"},
	}
	for _, c := range cases {
		got := c.b.Hash()
		if got != c.want {
			t.Errorf("%s: Hash = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestGitHubOwnerExactlyOne(t *testing.T) {
	cases := []struct {
		name string
		o    GitHubOwner
		want string // "" = valid
	}{
		{"app only", GitHubOwner{Owner: "acme", App: "main", Repos: []string{"r"}}, ""},
		{"token only", GitHubOwner{Owner: "acme", Token: "gh-pat-acme", Repos: []string{"r"}}, ""},
		{"both", GitHubOwner{Owner: "acme", App: "main", Token: "gh-pat-acme", Repos: []string{"r"}}, "github.owners[0]: set exactly one of app or token"},
		{"neither", GitHubOwner{Owner: "acme", Repos: []string{"r"}}, "github.owners[0]: set exactly one of app or token"},
		{"bad token name", GitHubOwner{Owner: "acme", Token: "Not A Name", Repos: []string{"r"}}, "github.owners[0].token"},
	}
	for _, c := range cases {
		err := (&GitHubBinding{Owners: []GitHubOwner{c.o}}).Validate()
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: want valid, got %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", c.name, c.want, err)
		}
	}
	// {owner, token, repos} round-trips with no "app" collision and an App-only
	// owner still marshals without a token key.
	b, _ := json.Marshal(GitHubOwner{Owner: "a", App: "m", Repos: []string{"r"}})
	if strings.Contains(string(b), "token") {
		t.Errorf("App-only owner must not marshal a token key: %s", b)
	}
}

func TestCheckGitHubAppsIgnoresTokenOwners(t *testing.T) {
	a := AgentConfig{Name: "c", GitHub: &GitHubBinding{Owners: []GitHubOwner{
		{Owner: "acme", Token: "gh-pat-acme", Repos: []string{"r"}},
	}}}
	if err := CheckGitHubApps(a, nil); err != nil {
		t.Fatalf("a token owner names no App: %v", err)
	}
	a.GitHub.Owners = append(a.GitHub.Owners, GitHubOwner{Owner: "other", App: "ghost", Repos: []string{"r"}})
	if err := CheckGitHubApps(a, nil); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("an App owner is still checked, got %v", err)
	}
}

func TestGitHubWriteRefusalNamesWritableKeys(t *testing.T) {
	b := goodGitHub()
	b.Permissions = map[string]string{"metadata": "write"}
	err := b.Validate()
	if err == nil {
		t.Fatal("want error")
	}
	for _, k := range []string{"contents", "pull_requests", "issues"} {
		if !strings.Contains(err.Error(), k) {
			t.Fatalf("error %q does not name %q", err, k)
		}
	}
}

func TestGitHubWritablePermissionsAndAllowed(t *testing.T) {
	w := GitHubWritablePermissions()
	if strings.Join(w, ",") != "contents,pull_requests,issues" {
		t.Fatalf("writable = %v", w)
	}
	w[0] = "x"
	if GitHubWritablePermissions()[0] != "contents" {
		t.Fatal("GitHubWritablePermissions must return a fresh slice")
	}
	cases := []struct {
		key, level string
		want       bool
	}{
		{"contents", "read", true}, {"contents", "write", true},
		{"pull_requests", "write", true}, {"issues", "write", true},
		{"metadata", "read", true}, {"metadata", "write", false},
		{"actions", "write", false}, {"checks", "write", false}, {"statuses", "write", false},
		{"contents", "admin", false}, {"contents", "", false},
		{"administration", "read", false},
	}
	for _, tc := range cases {
		if got := GitHubPermissionAllowed(tc.key, tc.level); got != tc.want {
			t.Errorf("GitHubPermissionAllowed(%q, %q) = %v, want %v", tc.key, tc.level, got, tc.want)
		}
	}
}

// A binding with no write keeps its stored shape and therefore its box key.
func TestGitHubHashUnchangedForReadOnly(t *testing.T) {
	const golden = "0f4522b2154b" // computed on ea70639, before the write rule
	if got := goodGitHub().Hash(); got != golden {
		t.Fatalf("Hash() = %s, want %s", got, golden)
	}
	b := goodGitHub()
	b.Permissions = DefaultGitHubPermissions()
	if got := b.Hash(); got != golden {
		t.Fatalf("Hash() with explicit defaults = %s, want %s", got, golden)
	}
}

func TestGitHubCanPushAndRoute(t *testing.T) {
	pat := GitHubBinding{Owners: []GitHubOwner{{Owner: "Acme", Token: "gh-pat", Repos: []string{"Widget"}}}}
	appRead := GitHubBinding{Owners: []GitHubOwner{ghOwner("Acme", "Widget")}}
	appWrite := GitHubBinding{Owners: []GitHubOwner{ghOwner("Acme", "Widget")}, Permissions: map[string]string{"contents": "write", "metadata": "read"}}
	appPRs := GitHubBinding{Owners: []GitHubOwner{ghOwner("Acme", "Widget")}, Permissions: map[string]string{"pull_requests": "write"}}
	mixed := GitHubBinding{Owners: []GitHubOwner{ghOwner("Acme", "Widget"), {Owner: "Zed", Token: "gh-pat", Repos: []string{"q"}}}}
	cases := []struct {
		name        string
		b           GitHubBinding
		owner, repo string
		canPush     bool
		route, pat  bool
	}{
		{"PAT bound repo", pat, "Acme", "Widget", true, true, true},
		{"PAT unbound repo", pat, "Acme", "Other", false, true, true},
		{"PAT unbound owner", pat, "Nobody", "Widget", false, true, true},
		{"App defaults", appRead, "Acme", "Widget", false, false, false},
		{"App contents write", appWrite, "Acme", "Widget", true, true, false},
		{"App contents write, case-insensitive", appWrite, "acme", "widget", true, true, false},
		{"App contents write, unbound repo", appWrite, "Acme", "Other", false, true, false},
		{"App pull_requests write only", appPRs, "Acme", "Widget", false, false, false},
		{"mixed: App owner without write", mixed, "Acme", "Widget", false, true, true},
		{"mixed: PAT owner", mixed, "zed", "Q", true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.b.CanPush(tc.owner, tc.repo); got != tc.canPush {
				t.Errorf("CanPush = %v, want %v", got, tc.canPush)
			}
			if got := tc.b.PushRoute(); got != tc.route {
				t.Errorf("PushRoute = %v, want %v", got, tc.route)
			}
			if got := tc.b.HasPAT(); got != tc.pat {
				t.Errorf("HasPAT = %v, want %v", got, tc.pat)
			}
		})
	}
}

func TestValidateProfileEgressAllow(t *testing.T) {
	base := func() AgentConfig { return AgentConfig{Name: "p", Bin: "claude", Runtime: RuntimeBox} }
	a := base()
	a.EgressAllow = []string{"*.example.com", "pkg.dev"}
	if err := ValidateProfile(a, nil); err != nil {
		t.Fatalf("valid policy refused: %v", err)
	}
	a.EgressAllow = []string{"https://x"}
	if err := ValidateProfile(a, nil); err == nil || !strings.Contains(err.Error(), "egress_allow[0]") {
		t.Fatalf("https://x: err = %v, want egress_allow[0]", err)
	}
	a.EgressAllow = nil
	for i := 0; i < maxEgressAllow+1; i++ {
		a.EgressAllow = append(a.EgressAllow, fmt.Sprintf("h%d.example.com", i))
	}
	if err := ValidateProfile(a, nil); err == nil || !strings.Contains(err.Error(), "egress_allow") {
		t.Fatalf("too many entries: err = %v", err)
	}
	for _, mut := range []func(*AgentConfig){
		func(a *AgentConfig) { a.EgressAllow = []string{"a.example"} },
		func(a *AgentConfig) { a.EgressOpen = true },
	} {
		h := base()
		h.Runtime = RuntimeHost
		mut(&h)
		if err := ValidateProfile(h, nil); err == nil || !strings.Contains(err.Error(), "egress_allow and egress_open need runtime: box") {
			t.Fatalf("host runtime: err = %v", err)
		}
	}
}

func TestBindingsHashEgressPolicy(t *testing.T) {
	mk := func() Bindings { return Bindings{GitHub: goodGitHub()} }
	plain := mk().Hash()
	zero := mk()
	zero.EgressAllow = []string{}
	if zero.Hash() != plain {
		t.Fatal("an unset policy must keep today's hash")
	}
	one := mk()
	one.EgressAllow = []string{"a.example"}
	if one.Hash() == plain {
		t.Fatal("egress_allow must change the hash")
	}
	ab, ba := mk(), mk()
	ab.EgressAllow = []string{"a", "b"}
	ba.EgressAllow = []string{"b", "a"}
	if ab.Hash() != ba.Hash() {
		t.Fatal("egress_allow order must not change the hash")
	}
	if got := ba.EgressAllow[0]; got != "b" {
		t.Fatalf("Hash reordered the caller's slice: %q", got)
	}
	open := mk()
	open.EgressOpen = true
	if open.Hash() == plain || open.Hash() == one.Hash() {
		t.Fatal("egress_open must differ from unset and from egress_allow")
	}
	none := Bindings{EgressAllow: []string{"a.example"}, EgressOpen: true}
	if none.Any() {
		t.Fatal("an egress policy alone must not make a session bound")
	}
}

func TestEgressWarning(t *testing.T) {
	a := AgentConfig{Name: "p", Bin: "claude", Runtime: RuntimeBox}
	if a.EgressWarning() != "" {
		t.Fatal("no policy: no warning")
	}
	a.EgressOpen = true
	if !strings.Contains(a.EgressWarning(), "without one the box already reaches any public host") {
		t.Fatalf("unbound with policy: %q", a.EgressWarning())
	}
	a.Tokens = []string{"t"}
	if a.EgressWarning() != "" {
		t.Fatal("bound: no warning")
	}
}
