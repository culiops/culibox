package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culiops-agent-console/internal/config"
)

type fakeResolver struct{ vals map[string]string }

func (f fakeResolver) Resolve(name string) (string, bool, error) {
	v, ok := f.vals[name]
	return v, ok, nil
}

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	return m
}

func TestBuild_UnknownAgent(t *testing.T) {
	l := New(nil, fakeResolver{}, nil)
	if _, _, err := l.Build(SpawnSpec{Agent: "nope"}); err == nil {
		t.Fatal("unknown agent should error")
	}
}

func TestBuild_CleanExcludesDaemonSecrets(t *testing.T) {
	t.Setenv("HOME", "/home/x")
	t.Setenv("DECOY_SECRET", "leaked") // a daemon-only secret
	// absolute bin so this test isolates env composition from PATH resolution
	l := New([]config.AgentConfig{{Name: "a", Bin: "/bin/cat", Base: "clean"}}, fakeResolver{}, nil)
	argv, env, err := l.Build(SpawnSpec{Agent: "a", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	m := envMap(env)
	if m["PATH"] == "" || m["HOME"] != "/home/x" {
		t.Fatalf("curated base missing PATH/HOME: %v", m)
	}
	if _, ok := m["DECOY_SECRET"]; ok {
		t.Fatal("clean base LEAKED a daemon secret")
	}
	if m["TERM"] == "" {
		t.Fatal("TERM not set")
	}
	if len(argv) != 1 || argv[0] != "/bin/cat" {
		t.Fatalf("clean argv should be the absolute bin: %v", argv)
	}
}

func TestBuild_ResolvesBinAgainstComposedPath(t *testing.T) {
	// A tool that exists ONLY on the agent's composed PATH, not the daemon's.
	dir := t.TempDir()
	tool := filepath.Join(dir, "mytool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	l := New([]config.AgentConfig{{
		Name: "a", Bin: "mytool", Base: "clean",
		Env: map[string]string{"PATH": dir},
	}}, fakeResolver{}, nil)
	argv, _, err := l.Build(SpawnSpec{Agent: "a", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatalf("bin should resolve against the composed PATH: %v", err)
	}
	if argv[0] != tool {
		t.Fatalf("argv[0]=%q, want %q (resolved on composed PATH)", argv[0], tool)
	}
}

func TestBuild_InheritStripsMarkersKeepsRest(t *testing.T) {
	t.Setenv("SOME_VAR", "keep")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "abc")
	l := New([]config.AgentConfig{{Name: "a", Bin: "/bin/cat", Base: "inherit"}}, fakeResolver{}, nil)
	_, env, err := l.Build(SpawnSpec{Agent: "a", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	m := envMap(env)
	if m["SOME_VAR"] != "keep" {
		t.Fatal("inherit dropped a normal var")
	}
	if _, ok := m["CLAUDE_CODE_SESSION_ID"]; ok {
		t.Fatal("inherit kept a Claude session marker")
	}
}

func TestBuild_EnvOverlayAndSecrets(t *testing.T) {
	l := New([]config.AgentConfig{{
		Name: "a", Bin: "/bin/cat", Base: "clean",
		Env:     map[string]string{"ANTHROPIC_MODEL": "m", "PATH": "/custom"},
		Secrets: []string{"TOK"},
	}}, fakeResolver{vals: map[string]string{"TOK": "xyz"}}, nil)
	_, env, err := l.Build(SpawnSpec{Agent: "a", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	m := envMap(env)
	if m["ANTHROPIC_MODEL"] != "m" {
		t.Fatal("env overlay not applied")
	}
	if m["PATH"] != "/custom" {
		t.Fatal("env overlay should win over curated PATH")
	}
	if m["TOK"] != "xyz" {
		t.Fatal("declared secret not injected")
	}
}

func TestBuild_UnresolvedSecretErrors(t *testing.T) {
	l := New([]config.AgentConfig{{Name: "a", Bin: "/bin/cat", Base: "clean", Secrets: []string{"MISSING"}}}, fakeResolver{}, nil)
	if _, _, err := l.Build(SpawnSpec{Agent: "a", Runtime: config.RuntimeHost}); err == nil {
		t.Fatal("unresolved declared secret should error (fail loud)")
	}
}

func TestBuild_LoginWrapsArgv(t *testing.T) {
	l := New([]config.AgentConfig{{Name: "a", Bin: "claude", Args: []string{"--foo bar"}, Base: "login"}}, fakeResolver{}, nil)
	l.shell = "/bin/bash"
	argv, _, err := l.Build(SpawnSpec{Agent: "a", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 4 || argv[0] != "/bin/bash" || argv[1] != "-l" || argv[2] != "-c" {
		t.Fatalf("login argv wrong: %v", argv)
	}
	if !strings.HasPrefix(argv[3], "exec ") || !strings.Contains(argv[3], `'claude'`) || !strings.Contains(argv[3], `'--foo bar'`) {
		t.Fatalf("login -c string wrong: %q", argv[3])
	}
}

func TestBuild_LoginZshUsesInteractive(t *testing.T) {
	l := New([]config.AgentConfig{{Name: "a", Bin: "claude", Base: "login"}}, fakeResolver{}, nil)
	l.shell = "/usr/bin/zsh"
	argv, _, err := l.Build(SpawnSpec{Agent: "a", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	// zsh needs -i so ~/.zshrc (toolchain) is sourced
	if len(argv) != 5 || argv[1] != "-l" || argv[2] != "-i" || argv[3] != "-c" {
		t.Fatalf("zsh login argv should include -i: %v", argv)
	}
}

// argvHasPair reports whether argv contains flag immediately followed by value.
func argvHasPair(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

func TestBuild_OptionBoolFlag(t *testing.T) {
	defs := []config.OptionDef{{Key: "skip", Label: "Skip", Type: "bool", Flag: "--dangerously-skip-permissions"}}
	l := New([]config.AgentConfig{{Name: "a", Bin: "/bin/cat", Base: "inherit", Options: map[string]any{"skip": true}}}, fakeResolver{}, defs)

	argv, _, err := l.Build(SpawnSpec{Agent: "a", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(argv, "--dangerously-skip-permissions") {
		t.Fatalf("bool option flag not appended: %v", argv)
	}
	// per-spawn override to false drops the flag
	argv, _, _ = l.Build(SpawnSpec{Agent: "a", Runtime: config.RuntimeHost, Options: map[string]any{"skip": false}})
	if contains(argv, "--dangerously-skip-permissions") {
		t.Fatalf("override=false should drop the flag: %v", argv)
	}
}

func TestBuild_OptionEnumFlagAndEnv(t *testing.T) {
	defs := []config.OptionDef{
		{Key: "effort", Type: "enum", Choices: []string{"high", "low"}, Flag: "--effort"},
		{Key: "model", Type: "enum", Choices: []string{"opus", "sonnet"}, Env: "ANTHROPIC_MODEL"},
	}
	l := New([]config.AgentConfig{{Name: "a", Bin: "/bin/cat", Base: "inherit",
		Options: map[string]any{"effort": "high", "model": "opus"}}}, fakeResolver{}, defs)

	argv, env, err := l.Build(SpawnSpec{Agent: "a", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	if !argvHasPair(argv, "--effort", "high") {
		t.Fatalf("enum flag/value missing: %v", argv)
	}
	if envMap(env)["ANTHROPIC_MODEL"] != "opus" {
		t.Fatal("enum env option not set")
	}
	// per-spawn override wins
	argv, env, _ = l.Build(SpawnSpec{Agent: "a", Runtime: config.RuntimeHost, Options: map[string]any{"effort": "low", "model": "sonnet"}})
	if !argvHasPair(argv, "--effort", "low") || envMap(env)["ANTHROPIC_MODEL"] != "sonnet" {
		t.Fatalf("override not applied: argv=%v env=%v", argv, envMap(env)["ANTHROPIC_MODEL"])
	}
}

func TestBuild_OptionSecurity(t *testing.T) {
	defs := []config.OptionDef{{Key: "effort", Type: "enum", Choices: []string{"high"}, Flag: "--effort"}}
	l := New([]config.AgentConfig{{Name: "a", Bin: "/bin/cat", Base: "inherit",
		Options: map[string]any{"effort": "high"}}}, fakeResolver{}, defs)

	// an invalid enum override is rejected → profile default kept, evil value never injected
	argv, _, _ := l.Build(SpawnSpec{Agent: "a", Runtime: config.RuntimeHost, Options: map[string]any{"effort": "$(evil)"}})
	if contains(argv, "$(evil)") {
		t.Fatalf("invalid enum override injected into argv: %v", argv)
	}
	if !argvHasPair(argv, "--effort", "high") {
		t.Fatalf("invalid override should fall back to default: %v", argv)
	}

	// a request option NOT exposed by the profile is ignored
	l2 := New([]config.AgentConfig{{Name: "b", Bin: "/bin/cat", Base: "inherit"}}, fakeResolver{}, defs)
	argv2, _, _ := l2.Build(SpawnSpec{Agent: "b", Options: map[string]any{"effort": "high"}})
	if argvHasPair(argv2, "--effort", "high") {
		t.Fatalf("option not exposed by profile must be ignored: %v", argv2)
	}
}

// --- boxed runtime ---------------------------------------------------------

func boxedLauncher(t *testing.T, agents []config.AgentConfig) *AgentLauncher {
	t.Helper()
	return New(agents, fakeResolver{vals: map[string]string{"DECLARED_SECRET": "s3cr3t"}}, nil)
}

// base=inherit hands over the daemon's whole env. A boxed spawn forwards env BY
// NAME into the container, so inheriting would carry every daemon secret across
// the boundary. Boxed must compose from the curated base instead.
func TestBoxedSpawnDoesNotInheritTheDaemonEnvironment(t *testing.T) {
	t.Setenv("AC_TEST_DAEMON_SECRET", "leaked-value")
	l := boxedLauncher(t, []config.AgentConfig{{Name: "claude", Bin: "claude", Base: config.BaseInherit}})
	_, env, err := l.Build(SpawnSpec{Agent: "claude", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "AC_TEST_DAEMON_SECRET=") {
			t.Fatalf("boxed env inherited a daemon variable: %q", kv)
		}
	}
}

// The same agent on the host tier keeps inheriting — this is the regression
// guard that containment did not change trusted-host behaviour.
func TestHostSpawnStillInherits(t *testing.T) {
	t.Setenv("AC_TEST_DAEMON_SECRET", "kept-value")
	l := boxedLauncher(t, []config.AgentConfig{{Name: "bash", Bin: "bash", Base: config.BaseInherit}})
	_, env, err := l.Build(SpawnSpec{Agent: "bash", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, kv := range env {
		if kv == "AC_TEST_DAEMON_SECRET=kept-value" {
			found = true
		}
	}
	if !found {
		t.Fatal("host tier must keep inheriting the daemon environment")
	}
}

// A host absolute path is meaningless inside the image, and the host need not
// have the binary at all.
func TestBoxedArgvIsNotResolvedAgainstTheHostPath(t *testing.T) {
	l := boxedLauncher(t, []config.AgentConfig{{Name: "claude", Bin: "some-agent-not-on-this-host"}})
	argv, _, err := l.Build(SpawnSpec{Agent: "claude", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatalf("boxed build must not fail on a host-missing binary: %v", err)
	}
	if argv[0] != "some-agent-not-on-this-host" {
		t.Fatalf("argv[0] = %q, want the bare command for in-container resolution", argv[0])
	}
}

// base=login wraps argv in the HOST shell; that shell need not exist in the box.
func TestBoxedSpawnIsNeverLoginWrapped(t *testing.T) {
	l := boxedLauncher(t, []config.AgentConfig{{Name: "claude", Bin: "claude", Base: config.BaseLogin}})
	argv, _, err := l.Build(SpawnSpec{Agent: "claude", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) > 1 && (argv[1] == "-l" || argv[1] == "-lc") {
		t.Fatalf("boxed argv was login-wrapped: %v", argv)
	}
	if argv[0] != "claude" {
		t.Fatalf("argv[0] = %q, want claude", argv[0])
	}
}

// Declared secrets are intentional and must still reach a boxed agent.
func TestBoxedSpawnStillInjectsDeclaredSecrets(t *testing.T) {
	l := boxedLauncher(t, []config.AgentConfig{{Name: "claude", Bin: "claude", Secrets: []string{"DECLARED_SECRET"}}})
	_, env, err := l.Build(SpawnSpec{Agent: "claude", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, kv := range env {
		if kv == "DECLARED_SECRET=s3cr3t" {
			found = true
		}
	}
	if !found {
		t.Fatalf("declared secret missing from boxed env: %v", env)
	}
}

func TestRuntimeForPrefersTheSpawnOverride(t *testing.T) {
	l := boxedLauncher(t, []config.AgentConfig{{Name: "claude", Bin: "claude"}})
	if got, _ := l.RuntimeFor(SpawnSpec{Agent: "claude"}); got != config.RuntimeBox {
		t.Fatalf("default = %q, want box", got)
	}
	if got, _ := l.RuntimeFor(SpawnSpec{Agent: "claude", Runtime: config.RuntimeHost}); got != config.RuntimeHost {
		t.Fatalf("override = %q, want host", got)
	}
}
