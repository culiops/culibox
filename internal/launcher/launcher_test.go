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
	l := New(nil, fakeResolver{})
	if _, _, err := l.Build(SpawnSpec{Agent: "nope"}); err == nil {
		t.Fatal("unknown agent should error")
	}
}

func TestBuild_CleanExcludesDaemonSecrets(t *testing.T) {
	t.Setenv("HOME", "/home/x")
	t.Setenv("DECOY_SECRET", "leaked") // a daemon-only secret
	// absolute bin so this test isolates env composition from PATH resolution
	l := New([]config.AgentConfig{{Name: "a", Bin: "/bin/cat", Base: "clean"}}, fakeResolver{})
	argv, env, err := l.Build(SpawnSpec{Agent: "a"})
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
	}}, fakeResolver{})
	argv, _, err := l.Build(SpawnSpec{Agent: "a"})
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
	l := New([]config.AgentConfig{{Name: "a", Bin: "/bin/cat", Base: "inherit"}}, fakeResolver{})
	_, env, err := l.Build(SpawnSpec{Agent: "a"})
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
	}}, fakeResolver{vals: map[string]string{"TOK": "xyz"}})
	_, env, err := l.Build(SpawnSpec{Agent: "a"})
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
	l := New([]config.AgentConfig{{Name: "a", Bin: "/bin/cat", Base: "clean", Secrets: []string{"MISSING"}}}, fakeResolver{})
	if _, _, err := l.Build(SpawnSpec{Agent: "a"}); err == nil {
		t.Fatal("unresolved declared secret should error (fail loud)")
	}
}

func TestBuild_LoginWrapsArgv(t *testing.T) {
	l := New([]config.AgentConfig{{Name: "a", Bin: "claude", Args: []string{"--foo bar"}, Base: "login"}}, fakeResolver{})
	l.shell = "/bin/bash"
	argv, _, err := l.Build(SpawnSpec{Agent: "a"})
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
	l := New([]config.AgentConfig{{Name: "a", Bin: "claude", Base: "login"}}, fakeResolver{})
	l.shell = "/usr/bin/zsh"
	argv, _, err := l.Build(SpawnSpec{Agent: "a"})
	if err != nil {
		t.Fatal(err)
	}
	// zsh needs -i so ~/.zshrc (toolchain) is sourced
	if len(argv) != 5 || argv[1] != "-l" || argv[2] != "-i" || argv[3] != "-c" {
		t.Fatalf("zsh login argv should include -i: %v", argv)
	}
}
