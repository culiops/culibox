package launcher

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
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
	defs := []config.OptionDef{
		{Key: "effort", Type: "enum", Choices: []string{"high"}, Flag: "--effort"},
		{Key: "skip", Type: "bool", Flag: "--dangerously-skip-permissions"},
	}
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

	// a bool the profile does not expose is ignored: a profile opts into a capability
	l2 := New([]config.AgentConfig{{Name: "b", Bin: "/bin/cat", Base: "inherit"}}, fakeResolver{}, defs)
	argv2, _, _ := l2.Build(SpawnSpec{Agent: "b", Options: map[string]any{"skip": true}})
	if contains(argv2, "--dangerously-skip-permissions") {
		t.Fatalf("bool option not exposed by profile must be ignored: %v", argv2)
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

// A boxed interactive shell runs through the in-box fallback: the host's
// $SHELL path (/opt/homebrew/bin/zsh on a Mac) need not exist in the image.
func TestBoxedShellRunsThroughTheInBoxFallback(t *testing.T) {
	l := boxedLauncher(t, []config.AgentConfig{{Name: "zsh", Bin: "/opt/homebrew/bin/zsh", Args: []string{"-l"}}})
	argv, _, err := l.Build(SpawnSpec{Agent: "zsh", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"sh", "-c", boxShellFallback, "/opt/homebrew/bin/zsh", "-l"}
	if !slices.Equal(argv, want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
}

// Only shells fall back: a missing agent must fail, never quietly become bash.
func TestBoxedAgentIsNotWrappedInTheShellFallback(t *testing.T) {
	l := boxedLauncher(t, []config.AgentConfig{{Name: "codex", Bin: "/usr/local/bin/codex"}})
	argv, _, err := l.Build(SpawnSpec{Agent: "codex", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != "/usr/local/bin/codex" {
		t.Fatalf("argv = %q, want the agent binary first", argv)
	}
}

// The host tier runs the host's own shell; no fallback there.
func TestHostShellIsNotWrappedInTheShellFallback(t *testing.T) {
	l := New([]config.AgentConfig{{Name: "sh", Bin: "/bin/sh"}}, fakeResolver{}, nil)
	argv, _, err := l.Build(SpawnSpec{Agent: "sh", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != "/bin/sh" {
		t.Fatalf("argv = %q, want /bin/sh first", argv)
	}
}

// The fallback script itself, run by dash (the box's /bin/sh): the path, else
// the basename on PATH, else /bin/bash with a notice.
func TestBoxShellFallbackScript(t *testing.T) {
	dash, err := exec.LookPath("dash")
	if err != nil {
		t.Skip("dash not installed")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "zsh")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho \"stub zsh $*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := func(path string, args ...string) (string, string, error) {
		t.Helper()
		var out, errb strings.Builder
		c := exec.Command(dash, append([]string{"-c", boxShellFallback}, args...)...)
		c.Env = []string{"PATH=" + path}
		c.Stdin = strings.NewReader("echo bash-ran\n")
		c.Stdout, c.Stderr = &out, &errb
		err := c.Run()
		return out.String(), errb.String(), err
	}
	run := func(path string, args ...string) (string, string) {
		t.Helper()
		out, errOut, err := start(path, args...)
		if err != nil {
			t.Fatalf("%v: %v (stderr %q)", args, err, errOut)
		}
		return out, errOut
	}
	if out, _ := run("/usr/bin:/bin", stub, "-l"); out != "stub zsh -l\n" {
		t.Errorf("an existing path: got %q", out)
	}
	if out, _ := run(dir+":/usr/bin:/bin", "/opt/homebrew/bin/zsh", "-l"); out != "stub zsh -l\n" {
		t.Errorf("a missing path whose basename is on PATH: got %q", out)
	}
	notExec := filepath.Join(dir, "fish")
	if err := os.WriteFile(notExec, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errOut := run("/usr/bin:/bin", notExec, "--weird-fish-flag")
	if out != "bash-ran\n" || !strings.Contains(errOut, notExec+" is not in the box image") {
		t.Errorf("no such shell: stdout %q stderr %q, want bash (without the fish args) and a notice", out, errOut)
	}
	// A command run through the missing shell fails; bash must not swallow it.
	for _, flag := range []string{"-c", "-lc", "-ic"} {
		out, errOut, err = start("/usr/bin:/bin", notExec, flag, "claude --resume x")
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 127 || out != "" || !strings.Contains(errOut, "-c command cannot run") {
			t.Errorf("missing shell with %s: err %v stdout %q stderr %q, want exit 127 and no bash", flag, err, out, errOut)
		}
	}
	// Hostile argument text stays data: nothing in it is run.
	marker := filepath.Join(dir, "PWNED")
	run(dir+":/usr/bin:/bin", "/no/zsh", "$(touch "+marker+")", "`touch "+marker+"`", "; touch "+marker)
	if _, err := os.Stat(marker); err == nil {
		t.Error("an argument was executed by the fallback script")
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

func TestInheritStripsAgentBoxDaemonVars(t *testing.T) {
	t.Setenv("AB_PASS", "hunter2")
	t.Setenv("AB_PROD_PASS", "hunter3")
	t.Setenv("MY_LOGIN_PW", "hunter4")
	t.Setenv("AC_TEST_KEEP", "kept")
	l := New([]config.AgentConfig{{Name: "sh", Bin: "bash", Base: config.BaseInherit, Runtime: config.RuntimeHost}},
		fakeResolver{vals: map[string]string{}}, nil)
	l.SetDaemonSecretEnv("MY_LOGIN_PW")
	_, env, err := l.Build(SpawnSpec{Agent: "sh", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	m := envMap(env)
	for _, k := range []string{"AB_PASS", "AB_PROD_PASS", "MY_LOGIN_PW"} {
		if _, ok := m[k]; ok {
			t.Fatalf("inherit base leaked daemon secret %s", k)
		}
	}
	if m["AC_TEST_KEEP"] != "kept" {
		t.Fatalf("inherit base dropped an ordinary variable")
	}
}

func TestProfileEnvMayStillSetAnABVariable(t *testing.T) {
	// The strip applies to the inherited base only; an explicit profile env is the user's choice.
	l := New([]config.AgentConfig{{Name: "sh", Bin: "bash", Base: config.BaseInherit, Runtime: config.RuntimeHost,
		Env: map[string]string{"AB_FEATURE": "on"}}}, fakeResolver{vals: map[string]string{}}, nil)
	_, env, err := l.Build(SpawnSpec{Agent: "sh", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	if envMap(env)["AB_FEATURE"] != "on" {
		t.Fatal("explicit profile env was stripped")
	}
}

func TestAWSForReturnsTheProfileBinding(t *testing.T) {
	b := &config.AWSBinding{SourceProfile: "p", RoleARN: "arn:aws:iam::226198813800:role/a", Region: "ap-southeast-1",
		S3Read: []string{"bucket"}, EgressAllow: []string{"registry.npmjs.org"}}
	l := New([]config.AgentConfig{{Name: "c", Bin: "claude", AWS: b}, {Name: "plain", Bin: "claude"}}, fakeResolver{}, nil)
	if got := l.AWSFor("c"); got == nil || got.RoleARN != b.RoleARN {
		t.Fatalf("got %+v", got)
	}
	if l.AWSFor("plain") != nil || l.AWSFor("missing") != nil {
		t.Fatal("unbound/missing must be nil")
	}
	got := l.AWSFor("c")
	got.Region = "mutated"
	got.S3Read[0] = "mutated"
	got.EgressAllow[0] = "mutated"
	if again := l.AWSFor("c"); again.Region != "ap-southeast-1" || again.S3Read[0] != "bucket" || again.EgressAllow[0] != "registry.npmjs.org" {
		t.Fatal("AWSFor must return a deep copy")
	}
}

func TestAWSForClonesElevate(t *testing.T) {
	b := &config.AWSBinding{SourceProfile: "p", RoleARN: "arn:aws:iam::226198813800:role/a", Region: "ap-southeast-1",
		Elevate: &config.AWSElevate{RoleARN: "arn:aws:iam::226198813800:role/a-write", MaxMinutes: 30}}
	l := New([]config.AgentConfig{{Name: "c", Bin: "claude", AWS: b}}, fakeResolver{}, nil)
	got := l.AWSFor("c")
	got.Elevate.RoleARN = "mutated"
	got.Elevate.MaxMinutes = 60
	again := l.AWSFor("c")
	if again.Elevate == nil || again.Elevate.RoleARN != "arn:aws:iam::226198813800:role/a-write" || again.Elevate.MaxMinutes != 30 {
		t.Fatalf("AWSFor must deep-copy Elevate, got %+v", again.Elevate)
	}
}

func TestCloneForAndClones(t *testing.T) {
	l := New([]config.AgentConfig{
		{Name: "good", Bin: "claude", CloneOf: "/home/u/repo", DefaultCwd: "/home/u/clones/repo", CloneShare: []string{"CLAUDE.md"}},
		{Name: "bad", Bin: "claude", CloneOf: "relative/repo", DefaultCwd: "/home/u/clones/bad"},
		{Name: "plain", Bin: "claude"},
	}, fakeResolver{}, nil)

	cl, err := l.CloneFor("good")
	if err != nil || cl == nil || cl.Of != "/home/u/repo" || cl.Dir != "/home/u/clones/repo" || len(cl.Shares) != 1 {
		t.Fatalf("CloneFor(good) = %+v, %v", cl, err)
	}
	if cl, err := l.CloneFor("plain"); cl != nil || err != nil {
		t.Fatalf("a profile without clone_of must give nil, nil; got %+v, %v", cl, err)
	}
	if cl, err := l.CloneFor("missing"); cl != nil || err != nil {
		t.Fatalf("an unknown profile must give nil, nil; got %+v, %v", cl, err)
	}
	if cl, err := l.CloneFor("bad"); cl != nil || err == nil {
		t.Fatalf("an invalid stored clone must error, got %+v, %v", cl, err)
	}

	// "bad" has an invalid clone_of but an absolute, clean dir: its dir is still
	// box-owned, so Clones covers it even though CloneFor refuses it.
	all := l.Clones()
	if len(all) != 2 || all[0].Dir != "/home/u/clones/bad" || all[1].Dir != "/home/u/clones/repo" {
		t.Fatalf("Clones() = %+v, want bad and good by dir", all)
	}
	if len(all[1].Shares) != 1 {
		t.Fatalf("a valid clone keeps its shares, got %+v", all[1])
	}
	// The copy is the caller's own: a later profile edit or a mutation here
	// must not reach the stored profile.
	cl.Shares[0] = "mutated"
	if again, _ := l.CloneFor("good"); again.Shares[0] != "CLAUDE.md" {
		t.Fatal("CloneFor must return a copy")
	}
}

// Clones feeds the host-tier box-owned guard, so it must cover the dir of a
// profile whose clone spec no longer validates (a hand-edited profiles.json),
// while skipping a dir it cannot trust as a path.
func TestClonesCoverInvalidSpecsWithUsableDir(t *testing.T) {
	l := New([]config.AgentConfig{
		{Name: "badshare", Bin: "claude", CloneOf: "/home/u/repo", DefaultCwd: "/home/u/c/badshare", CloneShare: []string{"../x", "a:rw"}},
		{Name: "reldir", Bin: "claude", CloneOf: "/home/u/repo", DefaultCwd: "c/rel"},
		{Name: "uncleandir", Bin: "claude", CloneOf: "/home/u/repo", DefaultCwd: "/home/u/c/../c/unclean"},
		{Name: "nodir", Bin: "claude", CloneOf: "/home/u/repo"},
		{Name: "plain", Bin: "claude", DefaultCwd: "/home/u/c/plain"},
	}, fakeResolver{}, nil)
	all := l.Clones()
	if len(all) != 1 || all[0].Dir != "/home/u/c/badshare" || all[0].Of != "/home/u/repo" {
		t.Fatalf("Clones() = %+v, want only badshare", all)
	}
	if cl, err := l.CloneFor("badshare"); cl != nil || err == nil {
		t.Fatalf("CloneFor must still refuse the invalid spec, got %+v, %v", cl, err)
	}
}

// RequestedRuntime reports only what was ASKED for — unlike RuntimeFor it never
// invents box as a last resort, so "nothing asked" stays distinguishable.
func TestRequestedRuntime(t *testing.T) {
	cases := []struct {
		name      string
		profile   string
		spec      string
		defaultRT string
		agent     string
		want      string
		wantErr   bool
	}{
		{name: "spec host beats profile box", profile: config.RuntimeBox, spec: config.RuntimeHost, want: config.RuntimeHost},
		{name: "blank spec uses profile box", profile: config.RuntimeBox, want: config.RuntimeBox},
		{name: "blank spec and profile use instance default", defaultRT: config.RuntimeBox, want: config.RuntimeBox},
		{name: "profile beats instance default", profile: config.RuntimeHost, defaultRT: config.RuntimeBox, want: config.RuntimeHost},
		{name: "nothing asked", want: ""},
		{name: "unknown agent", agent: "nope", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := boxedLauncher(t, []config.AgentConfig{{Name: "claude", Bin: "claude", Runtime: c.profile}})
			l.SetDefaultRuntime(c.defaultRT)
			agent := c.agent
			if agent == "" {
				agent = "claude"
			}
			got, err := l.RequestedRuntime(SpawnSpec{Agent: agent, Runtime: c.spec})
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// --- seccomp gate ----------------------------------------------------------

func TestBoxGateRefusesBoxedSpawnOnly(t *testing.T) {
	const want = "Docker seccomp is off: a box could read the unlocked store key. Enable the default seccomp profile."
	initialized := true
	l := boxedLauncher(t, []config.AgentConfig{
		{Name: "claude", Bin: "claude"}, {Name: "bash", Bin: "bash"},
	})
	l.SetBoxGate(SeccompGate(constProbe(false), func() bool { return initialized }))

	_, _, err := l.Build(SpawnSpec{Agent: "claude", Runtime: config.RuntimeBox})
	if !errors.Is(err, box.ErrBoxUnavailable) || !strings.Contains(err.Error(), want) {
		t.Fatalf("boxed Build with seccomp off and a store: %v, want ErrBoxUnavailable containing %q", err, want)
	}
	if _, _, err := l.Build(SpawnSpec{Agent: "bash", Runtime: config.RuntimeHost}); err != nil {
		t.Fatalf("host-tier Build must not be gated: %v", err)
	}
	initialized = false
	if _, _, err := l.Build(SpawnSpec{Agent: "claude", Runtime: config.RuntimeBox}); err != nil {
		t.Fatalf("no store: no refusal, got %v", err)
	}
	initialized = true // `store init` while the daemon runs takes effect at once
	if _, _, err := l.Build(SpawnSpec{Agent: "claude", Runtime: config.RuntimeBox}); err == nil {
		t.Fatal("a store created after start must close the gate")
	}
}

func TestBoxGateOpenWithSeccompOnOrNoGate(t *testing.T) {
	l := boxedLauncher(t, []config.AgentConfig{{Name: "claude", Bin: "claude"}})
	if _, _, err := l.Build(SpawnSpec{Agent: "claude", Runtime: config.RuntimeBox}); err != nil {
		t.Fatalf("nil gate: %v", err)
	}
	l.SetBoxGate(SeccompGate(constProbe(true), func() bool { return true }))
	if _, _, err := l.Build(SpawnSpec{Agent: "claude", Runtime: config.RuntimeBox}); err != nil {
		t.Fatalf("seccomp on: %v", err)
	}
	l.SetBoxGate(nil)
}

func constProbe(on bool) func() (bool, error) { return func() (bool, error) { return on, nil } }

func TestSeccompGateProbesEveryGatedCallAndNeverCaches(t *testing.T) {
	calls := 0
	results := []struct {
		on  bool
		err error
	}{{false, nil}, {false, errors.New("docker hung")}, {true, nil}, {false, nil}}
	g := SeccompGate(func() (bool, error) {
		r := results[calls]
		calls++
		return r.on, r.err
	}, func() bool { return true })
	want := []bool{true, true, false, true} // refused?
	for i, refuse := range want {
		err := g()
		if refuse && !errors.Is(err, box.ErrSeccompOff) {
			t.Fatalf("call %d: want refusal, got %v", i, err)
		}
		if !refuse && err != nil {
			t.Fatalf("call %d: want allowed, got %v", i, err)
		}
		if calls != i+1 {
			t.Fatalf("call %d: probe must run exactly once per gated call, ran %d total", i, calls)
		}
	}
}

func TestBuildProbesOncePerBoxedSpawnAndNeverForHost(t *testing.T) {
	calls := 0
	l := boxedLauncher(t, []config.AgentConfig{{Name: "claude", Bin: "claude"}, {Name: "bash", Bin: "bash"}})
	l.SetBoxGate(SeccompGate(func() (bool, error) { calls++; return true, nil }, func() bool { return true }))
	if _, _, err := l.Build(SpawnSpec{Agent: "bash", Runtime: config.RuntimeHost}); err != nil || calls != 0 {
		t.Fatalf("host tier: err=%v probes=%d", err, calls)
	}
	if _, _, err := l.Build(SpawnSpec{Agent: "claude", Runtime: config.RuntimeBox}); err != nil || calls != 1 {
		t.Fatalf("boxed: err=%v probes=%d, want 1", err, calls)
	}
}

func TestSeccompGateDoesNotProbeWithoutAStore(t *testing.T) {
	calls := 0
	g := SeccompGate(func() (bool, error) { calls++; return false, nil }, func() bool { return false })
	if err := g(); err != nil || calls != 0 {
		t.Fatalf("no store: err=%v probes=%d", err, calls)
	}
}

// SetDaemonSecretEnv replaces the list: it is re-driven on every credential
// save, so a name dropped from the list must stop being scrubbed.
func TestSetDaemonSecretEnvReplaces(t *testing.T) {
	t.Setenv("OLD_TOKEN", "a")
	t.Setenv("NEW_TOKEN", "b")
	l := New([]config.AgentConfig{{Name: "sh", Bin: "bash", Base: config.BaseInherit, Runtime: config.RuntimeHost}},
		fakeResolver{vals: map[string]string{}}, nil)
	env := func() map[string]string {
		_, e, err := l.Build(SpawnSpec{Agent: "sh", Runtime: config.RuntimeHost})
		if err != nil {
			t.Fatal(err)
		}
		return envMap(e)
	}
	l.SetDaemonSecretEnv("OLD_TOKEN")
	if m := env(); m["OLD_TOKEN"] != "" || m["NEW_TOKEN"] != "b" {
		t.Fatalf("first list: %v", m)
	}
	l.SetDaemonSecretEnv("NEW_TOKEN")
	if m := env(); m["OLD_TOKEN"] != "a" || m["NEW_TOKEN"] != "" {
		t.Fatalf("second list must replace the first: %v", m)
	}
}

func TestContextPathsForCopies(t *testing.T) {
	l := New([]config.AgentConfig{
		{Name: "ctx", Bin: "claude", ContextPaths: []string{"/srv/b/docs", "/srv/b/notes"}},
		{Name: "bad", Bin: "claude", ContextPaths: []string{"relative/docs"}},
		{Name: "plain", Bin: "claude"},
	}, fakeResolver{}, nil)

	got, err := l.ContextPathsFor("ctx")
	if err != nil || len(got) != 2 || got[0] != "/srv/b/docs" {
		t.Fatalf("ContextPathsFor(ctx) = %v, %v", got, err)
	}
	if got, err := l.ContextPathsFor("plain"); got != nil || err != nil {
		t.Fatalf("a profile without context_paths must give nil, nil; got %v, %v", got, err)
	}
	if got, err := l.ContextPathsFor("missing"); got != nil || err != nil {
		t.Fatalf("an unknown profile must give nil, nil; got %v, %v", got, err)
	}
	if got, err := l.ContextPathsFor("bad"); got != nil || err == nil {
		t.Fatalf("an invalid stored list must error, got %v, %v", got, err)
	}
	// The copy is the caller's own: mutating it must not reach the profile.
	got[0] = "/mutated"
	if again, _ := l.ContextPathsFor("ctx"); again[0] != "/srv/b/docs" {
		t.Fatal("ContextPathsFor must return a copy")
	}
}

// BoxedDefaultCwds feeds the first-boot seed of the boxed-workspace record: it
// lists the default_cwd of every profile that spawns boxed, by profile name.
func TestBoxedDefaultCwds(t *testing.T) {
	l := boxedLauncher(t, []config.AgentConfig{
		{Name: "z", Bin: "claude", Runtime: config.RuntimeBox, DefaultCwd: "/z"},
		{Name: "a", Bin: "claude", Runtime: config.RuntimeBox, DefaultCwd: "/x"},
		{Name: "b", Bin: "claude", Runtime: config.RuntimeHost, DefaultCwd: "/y"},
		{Name: "c", Bin: "claude"},
		{Name: "d", Bin: "claude", DefaultCwd: "/d"},
	})
	l.SetDefaultRuntime(config.RuntimeHost)
	if got, want := l.BoxedDefaultCwds(), []string{"/x", "/z"}; !slices.Equal(got, want) {
		t.Fatalf("BoxedDefaultCwds() = %v, want %v (host default runtime)", got, want)
	}
	// With the instance default box, d (no runtime of its own) is boxed too; c
	// has no default_cwd so contributes nothing.
	l.SetDefaultRuntime(config.RuntimeBox)
	if got, want := l.BoxedDefaultCwds(), []string{"/x", "/d", "/z"}; !slices.Equal(got, want) {
		t.Fatalf("BoxedDefaultCwds() = %v, want %v (box default runtime)", got, want)
	}
	if got := boxedLauncher(t, nil).BoxedDefaultCwds(); len(got) != 0 {
		t.Fatalf("no profiles: got %v", got)
	}
}

func TestImportsOffFor(t *testing.T) {
	l := New([]config.AgentConfig{
		{Name: "off", Bin: "claude", ImportsOff: true},
		{Name: "plain", Bin: "claude"},
	}, fakeResolver{}, nil)
	if !l.ImportsOffFor("off") {
		t.Fatal("a profile with imports_off must report it")
	}
	if l.ImportsOffFor("plain") || l.ImportsOffFor("missing") {
		t.Fatal("a profile without the switch, or an unknown one, must report false")
	}
}
