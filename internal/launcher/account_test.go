package launcher

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/account"
	"github.com/culiops/agent-box/internal/config"
)

type fakeAccounts map[string]account.Resolved

func (f fakeAccounts) Resolve(n string) (account.Resolved, error) {
	if n == "" || n == account.Default {
		return account.DefaultResolved("/home/u/.claude"), nil
	}
	r, ok := f[n]
	if !ok {
		return account.Resolved{}, account.ErrUnknown
	}
	return r, nil
}

func workAcct(dir string) account.Resolved {
	return account.Resolved{Account: account.Account{Name: "work", Provider: account.ProviderClaude}, ConfigDir: dir}
}

func TestBuild_DefaultAccountUnchanged(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "personal")
	t.Setenv("CLAUDE_CONFIG_DIR", "") // the daemon's own value must pass through untouched
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	agents := []config.AgentConfig{{Name: "c", Bin: "/usr/bin/claude", Base: "inherit"}}
	plain := New(agents, fakeResolver{}, nil)
	a1, e1, err := plain.Build(SpawnSpec{Agent: "c", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	withAccts := New(agents, fakeResolver{}, nil)
	withAccts.SetAccounts(fakeAccounts{})
	a2, e2, err := withAccts.Build(SpawnSpec{Agent: "c", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a1, a2) || !reflect.DeepEqual(envMap(e1), envMap(e2)) {
		t.Fatal("default account must build byte-identical argv/env")
	}
	m := envMap(e2)
	if m["CLAUDE_CODE_OAUTH_TOKEN"] != "personal" {
		t.Fatal("default account keeps inheriting today's token")
	}
	// Both builds run the new code, so also pin the absolute shape: nothing the
	// account feature adds may appear for the default account.
	if v, ok := m["CLAUDE_CONFIG_DIR"]; ok {
		t.Fatalf("default account got CLAUDE_CONFIG_DIR=%q", v)
	}
	for _, a := range a2 {
		if a == "--mcp-config" {
			t.Fatalf("default account argv has --mcp-config: %v", a2)
		}
	}
}

func TestBuild_NonDefaultStripsInheritedAuthAndSetsConfigDir(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "personal")
	t.Setenv("ANTHROPIC_API_KEY", "personal-key")
	t.Setenv("CLAUDE_CONFIG_DIR", "/home/u/.claude")
	l := New([]config.AgentConfig{{Name: "c", Bin: "/usr/bin/claude", Base: "inherit", Account: "work"}}, fakeResolver{}, nil)
	l.SetAccounts(fakeAccounts{"work": workAcct("/data/accounts/work/claude")})
	_, env, err := l.Build(SpawnSpec{Agent: "c", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	m := envMap(env)
	for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if _, ok := m[k]; ok {
			t.Fatalf("%s leaked into a non-default account", k)
		}
	}
	if m["CLAUDE_CONFIG_DIR"] != "/data/accounts/work/claude" {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q", m["CLAUDE_CONFIG_DIR"])
	}
}

func TestBuild_NonDefaultBoxedHasNoConfigDirAndNoAuth(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "personal")
	t.Setenv("CLAUDE_CONFIG_DIR", "/home/u/.claude")
	l := boxedLauncher(t, []config.AgentConfig{{Name: "c", Bin: "claude", Base: config.BaseInherit}})
	l.SetAccounts(fakeAccounts{"work": workAcct("/data/accounts/work/claude")})
	_, env, err := l.Build(SpawnSpec{Agent: "c", Account: "work", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	m := envMap(env)
	for _, k := range account.AuthEnv {
		if _, ok := m[k]; ok {
			t.Fatalf("boxed non-default account env carries %s", k)
		}
	}
}

func TestBuild_SpawnOverrideRefusesProfileAuth(t *testing.T) {
	// Review Focus 1: the profile carries the personal token; overriding the account at spawn must fail.
	l := New([]config.AgentConfig{
		{Name: "sec", Bin: "/usr/bin/claude", Secrets: []string{"CLAUDE_CODE_OAUTH_TOKEN"}},
		{Name: "env", Bin: "/usr/bin/claude", Env: map[string]string{"ANTHROPIC_API_KEY": "k"}},
	}, fakeResolver{vals: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "personal"}}, nil)
	l.SetAccounts(fakeAccounts{"work": workAcct("/d")})
	for _, a := range []string{"sec", "env"} {
		_, _, err := l.Build(SpawnSpec{Agent: a, Account: "work", Runtime: config.RuntimeHost})
		if !errors.Is(err, ErrAccountConflict) {
			t.Fatalf("profile %s: auth-carrying profile spawned under another account (err = %v)", a, err)
		}
		// Same profile with no override stays spawnable: today's behaviour.
		if _, _, err := l.Build(SpawnSpec{Agent: a, Runtime: config.RuntimeHost}); err != nil {
			t.Fatalf("profile %s under the default account: %v", a, err)
		}
	}
}

func TestBuild_ProfileAccountWithOwnAuthRefused(t *testing.T) {
	// The same conflict when the profile itself names the account.
	l := New([]config.AgentConfig{
		{Name: "c", Bin: "/usr/bin/claude", Account: "work", Secrets: []string{"OPENAI_API_KEY"}},
	}, fakeResolver{vals: map[string]string{"OPENAI_API_KEY": "k"}}, nil)
	l.SetAccounts(fakeAccounts{"work": workAcct("/d")})
	if _, _, err := l.Build(SpawnSpec{Agent: "c", Runtime: config.RuntimeHost}); !errors.Is(err, ErrAccountConflict) {
		t.Fatalf("err = %v", err)
	}
}

func TestBuild_LoginBaseRefusedForNonDefaultAccount(t *testing.T) {
	// $SHELL -l -i re-sources rc files after the env strip and can re-export the
	// default login's token, so a login base cannot isolate an account.
	l := New([]config.AgentConfig{{Name: "c", Bin: "/usr/bin/claude", Base: config.BaseLogin}}, fakeResolver{}, nil)
	l.SetAccounts(fakeAccounts{"work": workAcct("/d")})
	if _, _, err := l.Build(SpawnSpec{Agent: "c", Account: "work", Runtime: config.RuntimeHost}); !errors.Is(err, ErrAccountConflict) {
		t.Fatalf("host login base under a non-default account: err = %v", err)
	}
	// The default account is unaffected.
	if _, _, err := l.Build(SpawnSpec{Agent: "c", Runtime: config.RuntimeHost}); err != nil {
		t.Fatalf("default account with login base: %v", err)
	}
	// A boxed spawn never login-wraps (Build forces the clean base), so it is fine.
	if _, _, err := l.Build(SpawnSpec{Agent: "c", Account: "work", Runtime: config.RuntimeBox}); err != nil {
		t.Fatalf("boxed spawn of a login-base profile: %v", err)
	}
}

func TestBuild_TokenSecretInjected(t *testing.T) {
	acct := workAcct("/d")
	acct.TokenSecret = "WORK_TOK"
	l := New([]config.AgentConfig{{Name: "c", Bin: "/usr/bin/claude", Base: "clean"}},
		fakeResolver{vals: map[string]string{"WORK_TOK": "w"}}, nil)
	l.SetAccounts(fakeAccounts{"work": acct})
	_, env, err := l.Build(SpawnSpec{Agent: "c", Account: "work", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	if envMap(env)["CLAUDE_CODE_OAUTH_TOKEN"] != "w" {
		t.Fatal("token secret not injected as CLAUDE_CODE_OAUTH_TOKEN")
	}
	acct.TokenSecret = "MISSING"
	l.SetAccounts(fakeAccounts{"work": acct})
	if _, _, err := l.Build(SpawnSpec{Agent: "c", Account: "work", Runtime: config.RuntimeHost}); err == nil {
		t.Fatal("unresolvable token secret must fail the spawn")
	}
}

func TestBuild_AccountErrors(t *testing.T) {
	codex := account.Resolved{Account: account.Account{Name: "cx", Provider: account.ProviderCodex}, ConfigDir: "/d"}
	l := New([]config.AgentConfig{
		{Name: "c", Bin: "/usr/bin/claude"},
		{Name: "sh", Bin: "/bin/bash"},
	}, fakeResolver{}, nil)
	l.SetAccounts(fakeAccounts{"work": workAcct("/d"), "cx": codex})
	if _, _, err := l.Build(SpawnSpec{Agent: "c", Account: "nope"}); !errors.Is(err, account.ErrUnknown) {
		t.Fatalf("unknown account err = %v", err)
	}
	if _, _, err := l.Build(SpawnSpec{Agent: "c", Account: "cx"}); !errors.Is(err, ErrCodexAccount) {
		t.Fatalf("codex err = %v", err)
	}
	if _, _, err := l.Build(SpawnSpec{Agent: "sh", Account: "work"}); !errors.Is(err, ErrAccountConflict) {
		t.Fatalf("a non-claude bin must not run under a claude account: err = %v", err)
	}
}

func TestAccountFor_WithoutResolverOnlyDefaultExists(t *testing.T) {
	l := New([]config.AgentConfig{{Name: "c", Bin: "/usr/bin/claude"}}, fakeResolver{}, nil)
	if r, err := l.AccountFor(SpawnSpec{Agent: "c"}); err != nil || !r.IsDefault {
		t.Fatalf("default without resolver = %+v, %v", r, err)
	}
	if _, err := l.AccountFor(SpawnSpec{Agent: "c", Account: "work"}); !errors.Is(err, account.ErrUnknown) {
		t.Fatalf("named account without resolver err = %v", err)
	}
	if _, err := l.AccountFor(SpawnSpec{Agent: "nope"}); err == nil {
		t.Fatal("unknown agent must error")
	}
}

func TestAccountFor_SpawnOverrideBeatsProfile(t *testing.T) {
	l := New([]config.AgentConfig{{Name: "c", Bin: "/usr/bin/claude", Account: "work"}}, fakeResolver{}, nil)
	other := workAcct("/o")
	other.Name = "other"
	l.SetAccounts(fakeAccounts{"work": workAcct("/w"), "other": other})
	if r, _ := l.AccountFor(SpawnSpec{Agent: "c"}); r.Name != "work" {
		t.Fatalf("profile account = %q", r.Name)
	}
	if r, _ := l.AccountFor(SpawnSpec{Agent: "c", Account: "other"}); r.Name != "other" {
		t.Fatalf("override account = %q", r.Name)
	}
	if r, _ := l.AccountFor(SpawnSpec{Agent: "c", Account: account.Default}); !r.IsDefault {
		t.Fatalf("explicit default override = %+v", r)
	}
}

func TestBuild_InheritAddsMCPConfigOnHostOnly(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, account.MCPFileName), []byte(`{"mcpServers":{}}`), 0o600)
	acct := workAcct(dir)
	acct.InheritPersonal = true
	l := New([]config.AgentConfig{{Name: "c", Bin: "/usr/bin/claude"}}, fakeResolver{}, nil)
	l.SetAccounts(fakeAccounts{"work": acct})
	argv, _, err := l.Build(SpawnSpec{Agent: "c", Account: "work", Runtime: config.RuntimeHost, Prompt: "hi there"})
	if err != nil {
		t.Fatal(err)
	}
	j := strings.Join(argv, " ")
	if !strings.Contains(j, "--mcp-config "+filepath.Join(dir, account.MCPFileName)) {
		t.Fatalf("argv lacks --mcp-config: %v", argv)
	}
	if strings.Index(j, "--mcp-config") > strings.Index(j, "-- hi there") {
		t.Fatal("--mcp-config is variadic; it must come before the prompt separator")
	}
	// Boxed: the file path is a host path the container cannot see.
	bargv, _, err := l.Build(SpawnSpec{Agent: "c", Account: "work", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range bargv {
		if a == "--mcp-config" {
			t.Fatalf("boxed argv must not carry --mcp-config: %v", bargv)
		}
	}
	// No file, no flag.
	acct.ConfigDir = t.TempDir()
	l.SetAccounts(fakeAccounts{"work": acct})
	nargv, _, err := l.Build(SpawnSpec{Agent: "c", Account: "work", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range nargv {
		if a == "--mcp-config" {
			t.Fatalf("missing MCP file must not add the flag: %v", nargv)
		}
	}
}

// Login mode (Settings → Log in): a profile that carries the personal login can
// still host the sign-in session, because Build drops its auth instead of
// refusing it.
func TestBuild_LoginModeDropsProfileAuth(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "daemon-key")
	l := New([]config.AgentConfig{{
		Name: "claude", Bin: "/usr/bin/claude", Base: config.BaseInherit,
		Secrets: []string{"CLAUDE_CODE_OAUTH_TOKEN", "KEEP_SECRET"},
		Env:     map[string]string{"ANTHROPIC_API_KEY": "profile-key", "KEEP_ENV": "e"},
	}}, fakeResolver{vals: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "personal", "KEEP_SECRET": "s"}}, nil)
	l.SetAccounts(fakeAccounts{"work": workAcct("/data/accounts/work/claude")})
	_, env, err := l.Build(SpawnSpec{Agent: "claude", Account: "work", Runtime: config.RuntimeHost, Login: true})
	if err != nil {
		t.Fatalf("login-mode spawn refused: %v", err)
	}
	m := envMap(env)
	for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if v, ok := m[k]; ok {
			t.Fatalf("%s=%q reached the login session", k, v)
		}
	}
	if m["CLAUDE_CONFIG_DIR"] != "/data/accounts/work/claude" {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q", m["CLAUDE_CONFIG_DIR"])
	}
	if m["KEEP_SECRET"] != "s" || m["KEEP_ENV"] != "e" {
		t.Fatalf("non-auth secret/env dropped: %q %q", m["KEEP_SECRET"], m["KEEP_ENV"])
	}
}

func TestBuild_LoginModeKeepsAccountTokenOnly(t *testing.T) {
	acct := workAcct("/d")
	acct.TokenSecret = "WORK_TOK"
	l := New([]config.AgentConfig{{Name: "claude", Bin: "/usr/bin/claude", Secrets: []string{"CLAUDE_CODE_OAUTH_TOKEN"}}},
		fakeResolver{vals: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "personal", "WORK_TOK": "w"}}, nil)
	l.SetAccounts(fakeAccounts{"work": acct})
	_, env, err := l.Build(SpawnSpec{Agent: "claude", Account: "work", Runtime: config.RuntimeHost, Login: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := envMap(env)["CLAUDE_CODE_OAUTH_TOKEN"]; got != "w" {
		t.Fatalf("token = %q, want the account's", got)
	}
}

func TestBuild_LoginModeOnlyForHostClaudeNamedAccount(t *testing.T) {
	codex := account.Resolved{Account: account.Account{Name: "cx", Provider: account.ProviderCodex}, ConfigDir: "/d"}
	l := New([]config.AgentConfig{
		{Name: "claude", Bin: "/usr/bin/claude"},
		{Name: "sh", Bin: "/bin/bash"},
		{Name: "lg", Bin: "/usr/bin/claude", Base: config.BaseLogin},
	}, fakeResolver{}, nil)
	l.SetAccounts(fakeAccounts{"work": workAcct("/d"), "cx": codex})
	cases := []struct {
		name string
		spec SpawnSpec
		want error
	}{
		{"default account", SpawnSpec{Agent: "claude", Runtime: config.RuntimeHost, Login: true}, ErrAccountConflict},
		{"explicit default", SpawnSpec{Agent: "claude", Account: account.Default, Runtime: config.RuntimeHost, Login: true}, ErrAccountConflict},
		{"boxed", SpawnSpec{Agent: "claude", Account: "work", Runtime: config.RuntimeBox, Login: true}, ErrAccountConflict},
		{"codex account", SpawnSpec{Agent: "claude", Account: "cx", Runtime: config.RuntimeHost, Login: true}, ErrCodexAccount},
		{"non-claude bin", SpawnSpec{Agent: "sh", Account: "work", Runtime: config.RuntimeHost, Login: true}, ErrAccountConflict},
		{"login base still refused", SpawnSpec{Agent: "lg", Account: "work", Runtime: config.RuntimeHost, Login: true}, ErrAccountConflict},
	}
	for _, c := range cases {
		if _, _, err := l.Build(c.spec); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
}

func TestBuild_SameProfileWithoutLoginStillRefused(t *testing.T) {
	l := New([]config.AgentConfig{{Name: "claude", Bin: "/usr/bin/claude", Secrets: []string{"CLAUDE_CODE_OAUTH_TOKEN"}}},
		fakeResolver{vals: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "personal"}}, nil)
	l.SetAccounts(fakeAccounts{"work": workAcct("/d")})
	if _, _, err := l.Build(SpawnSpec{Agent: "claude", Account: "work", Runtime: config.RuntimeHost}); !errors.Is(err, ErrAccountConflict) {
		t.Fatalf("err = %v", err)
	}
}

type refreshingAccounts struct {
	fakeAccounts
	refreshed []string
	err       error
}

func (r *refreshingAccounts) Refresh(n string) error {
	r.refreshed = append(r.refreshed, n)
	return r.err
}

func TestBuild_RefreshesHostAccountOncePerSpawn(t *testing.T) {
	l := New([]config.AgentConfig{{Name: "c", Bin: "/usr/bin/claude", Account: "work"}}, fakeResolver{}, nil)
	ra := &refreshingAccounts{fakeAccounts: fakeAccounts{"work": workAcct(t.TempDir())}}
	l.SetAccounts(ra)
	// AccountFor is also called by the session manager before Build; it must
	// not refresh on its own.
	if _, err := l.AccountFor(SpawnSpec{Agent: "c", Runtime: config.RuntimeHost}); err != nil {
		t.Fatal(err)
	}
	if len(ra.refreshed) != 0 {
		t.Fatalf("AccountFor refreshed: %v", ra.refreshed)
	}
	if _, _, err := l.Build(SpawnSpec{Agent: "c", Runtime: config.RuntimeHost}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ra.refreshed, []string{"work"}) {
		t.Fatalf("refreshed = %v, want exactly one", ra.refreshed)
	}
	// The default account has nothing to refresh.
	if _, _, err := l.Build(SpawnSpec{Agent: "c", Account: account.Default, Runtime: config.RuntimeHost}); err != nil {
		t.Fatal(err)
	}
	if len(ra.refreshed) != 1 {
		t.Fatalf("default account refreshed: %v", ra.refreshed)
	}
	// A failing refresh must not block the spawn.
	ra.err = errors.New("disk full")
	if _, _, err := l.Build(SpawnSpec{Agent: "c", Runtime: config.RuntimeHost}); err != nil {
		t.Fatalf("refresh failure blocked the spawn: %v", err)
	}
}

func TestBuild_BoxedSpawnDoesNotRefreshTheAccount(t *testing.T) {
	l := New([]config.AgentConfig{{Name: "c", Bin: "/usr/bin/claude", Account: "work"}}, fakeResolver{}, nil)
	ra := &refreshingAccounts{fakeAccounts: fakeAccounts{"work": workAcct(t.TempDir())}}
	l.SetAccounts(ra)
	if _, _, err := l.Build(SpawnSpec{Agent: "c", Runtime: config.RuntimeBox}); err != nil {
		t.Fatal(err)
	}
	if len(ra.refreshed) != 0 {
		t.Fatalf("a boxed spawn refreshed the host-side account dir: %v", ra.refreshed)
	}
}
