package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/config"
)

// permDefs are the two built-in capability options plus the older bypass bool,
// as a profile with a hand-written options block still carries it.
func permDefs() []config.OptionDef {
	return []config.OptionDef{
		{Key: "skip_permissions", Label: "Skip permissions", Type: config.OptBool, Flag: "--dangerously-skip-permissions", Bins: []string{"claude"}},
		{Key: config.PermissionModeKey, Label: "Permission mode", Type: config.OptEnum, Flag: "--permission-mode",
			Choices: []string{"manual", "acceptEdits", "auto", "plan", config.BypassMode}, Bins: []string{"claude"}},
		{Key: config.CriticalRmKey, Label: "Skip the critical-rm prompt", Type: config.OptBool, Env: config.CriticalRmEnv,
			Bins: []string{"claude"}, Tier: config.RuntimeBox},
	}
}

// fakeClaudeOnPath puts an executable named claude first on PATH, so an
// inherit-base profile resolves it.
func fakeClaudeOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func permLauncher(base string, opts map[string]any) *AgentLauncher {
	return New([]config.AgentConfig{{Name: "a", Bin: "claude", Base: base, Options: opts}}, fakeResolver{}, permDefs())
}

func hasArg(argv []string, s string) bool {
	for _, a := range argv {
		if a == s {
			return true
		}
	}
	return false
}

func hasDrop(drops []OptionDrop, key, reason string) bool {
	for _, d := range drops {
		if d.Key == key && d.Reason == reason {
			return true
		}
	}
	return false
}

// Review Focus 1: an untouched legacy profile keeps its flag, with no enum flag.
func TestLegacyBypassArgvUnchanged(t *testing.T) {
	fakeClaudeOnPath(t)
	l := New([]config.AgentConfig{{Name: "a", Bin: "claude", Base: config.BaseInherit,
		Options: map[string]any{"skip_permissions": true}}}, fakeResolver{}, permDefs())
	argv, _, err := l.Build(SpawnSpec{Agent: "a", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	if !hasArg(argv, "--dangerously-skip-permissions") || hasArg(argv, "--permission-mode") {
		t.Fatalf("argv = %v", argv)
	}
}

// Review Focus 4: both forms on one profile give only the enum.
func TestEnumSupersedesLegacy(t *testing.T) {
	fakeClaudeOnPath(t)
	l := New([]config.AgentConfig{{Name: "a", Bin: "claude", Base: config.BaseInherit,
		Options: map[string]any{"skip_permissions": true, "permission_mode": "plan"}}}, fakeResolver{}, permDefs())
	argv, _, err := l.Build(SpawnSpec{Agent: "a", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	if !argvHasPair(argv, "--permission-mode", "plan") || hasArg(argv, "--dangerously-skip-permissions") {
		t.Fatalf("argv = %v", argv)
	}
}

func TestPermissionModeOverrideOnLegacyProfileAccepted(t *testing.T) {
	fakeClaudeOnPath(t)
	l := New([]config.AgentConfig{{Name: "a", Bin: "claude", Base: config.BaseInherit,
		Options: map[string]any{"skip_permissions": true}}}, fakeResolver{}, permDefs())
	spec := SpawnSpec{Agent: "a", Runtime: config.RuntimeHost, Options: map[string]any{"permission_mode": "plan"}}
	if d := l.DroppedOptions(spec); len(d) != 0 {
		t.Fatalf("dropped = %v", d)
	}
	argv, _, err := l.Build(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !argvHasPair(argv, "--permission-mode", "plan") || hasArg(argv, "--dangerously-skip-permissions") {
		t.Fatalf("argv = %v", argv)
	}
}

func TestCriticalRmEnvBoxedBypassOnly(t *testing.T) {
	bypass := map[string]any{"permission_mode": config.BypassMode, "skip_critical_rm_prompt": true}
	l := permLauncher(config.BaseClean, bypass)
	_, env, err := l.Build(SpawnSpec{Agent: "a", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	if got := envMap(env)[config.CriticalRmEnv]; got != "1" {
		t.Fatalf("boxed bypass: env = %q, want 1", got)
	}

	plan := map[string]any{"permission_mode": "plan", "skip_critical_rm_prompt": true}
	l = permLauncher(config.BaseClean, plan)
	spec := SpawnSpec{Agent: "a", Runtime: config.RuntimeBox}
	_, env, err = l.Build(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := envMap(env)[config.CriticalRmEnv]; ok {
		t.Fatal("boxed plan must not carry the critical-rm env")
	}
	if d := l.DroppedOptions(spec); !hasDrop(d, "skip_critical_rm_prompt", "needs Permission mode: Bypass") {
		t.Fatalf("dropped = %v", d)
	}
}

// The older bypass bool is Bypass too: boxed with critical-rm on, the env is injected, and the enum flag stays out.
func TestCriticalRmEnvBoxedLegacyBypass(t *testing.T) {
	legacy := map[string]any{"skip_permissions": true, "skip_critical_rm_prompt": true}
	l := permLauncher(config.BaseClean, legacy)
	spec := SpawnSpec{Agent: "a", Runtime: config.RuntimeBox}
	argv, env, err := l.Build(spec)
	if err != nil {
		t.Fatal(err)
	}
	if got := envMap(env)[config.CriticalRmEnv]; got != "1" {
		t.Fatalf("boxed legacy bypass: env = %q, want 1", got)
	}
	if !hasArg(argv, "--dangerously-skip-permissions") || hasArg(argv, "--permission-mode") {
		t.Fatalf("argv = %v", argv)
	}
	if d := l.DroppedOptions(spec); len(d) != 0 {
		t.Fatalf("dropped = %v", d)
	}
}

// Review Focus 3.
func TestCriticalRmNeverOnHost(t *testing.T) {
	fakeClaudeOnPath(t)
	bypass := map[string]any{"permission_mode": config.BypassMode, "skip_critical_rm_prompt": true}
	l := permLauncher(config.BaseClean, bypass)
	_, env, err := l.Build(SpawnSpec{Agent: "a", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := envMap(env)[config.CriticalRmEnv]; ok {
		t.Fatal("host spawn carries the critical-rm env")
	}

	t.Setenv(config.CriticalRmEnv, "1")
	l = permLauncher(config.BaseInherit, bypass)
	_, env, err = l.Build(SpawnSpec{Agent: "a", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := envMap(env)[config.CriticalRmEnv]; ok {
		t.Fatal("inherited daemon env leaked the critical-rm env to a host spawn")
	}
	// profile env and the secret layer too
	l = New([]config.AgentConfig{{Name: "a", Bin: "claude", Base: config.BaseInherit,
		Env: map[string]string{config.CriticalRmEnv: "1"}, Secrets: []string{config.CriticalRmEnv}}},
		fakeResolver{vals: map[string]string{config.CriticalRmEnv: "1"}}, permDefs())
	_, env, err = l.Build(SpawnSpec{Agent: "a", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := envMap(env)[config.CriticalRmEnv]; ok {
		t.Fatal("profile env/secret carried the critical-rm env onto the host")
	}
}

func TestLoginBaseUnsetsBoxOnlyEnv(t *testing.T) {
	l := permLauncher(config.BaseLogin, nil)
	argv, _, err := l.Build(SpawnSpec{Agent: "a", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	last := argv[len(argv)-1]
	if !strings.HasPrefix(last, "exec env -u "+config.CriticalRmEnv+" ") {
		t.Fatalf("login -c string = %q", last)
	}
}

func TestBoxOnlyOptionFor(t *testing.T) {
	l := permLauncher(config.BaseClean, map[string]any{"skip_critical_rm_prompt": false})
	got, err := l.BoxOnlyOptionFor(SpawnSpec{Agent: "a", Options: map[string]any{"skip_critical_rm_prompt": true}})
	if err != nil || got != `"Skip the critical-rm prompt" runs only in a box` {
		t.Fatalf("got %q, %v", got, err)
	}
	got, err = l.BoxOnlyOptionFor(SpawnSpec{Agent: "a"})
	if err != nil || got != "" {
		t.Fatalf("no override: got %q, %v", got, err)
	}
	// Host runtime must not hide the request: the override is still refused.
	got, err = l.BoxOnlyOptionFor(SpawnSpec{Agent: "a", Runtime: config.RuntimeHost, Options: map[string]any{"skip_critical_rm_prompt": true}})
	if err != nil || got == "" {
		t.Fatalf("host override: got %q, %v", got, err)
	}
	if _, err := l.BoxOnlyOptionFor(SpawnSpec{Agent: "nope"}); err == nil {
		t.Fatal("unknown agent should error")
	}
}

// A profile default is not a request: only the spawn's own overrides count.
func TestBoxOnlyOptionForIgnoresProfileDefault(t *testing.T) {
	l := permLauncher(config.BaseClean, map[string]any{"skip_critical_rm_prompt": true})
	got, err := l.BoxOnlyOptionFor(SpawnSpec{Agent: "a"})
	if err != nil || got != "" {
		t.Fatalf("profile default on, no override: got %q, %v", got, err)
	}
	got, err = l.BoxOnlyOptionFor(SpawnSpec{Agent: "a", Options: map[string]any{"skip_critical_rm_prompt": true}})
	if err != nil || got != `"Skip the critical-rm prompt" runs only in a box` {
		t.Fatalf("override on: got %q, %v", got, err)
	}
}

func TestHostBlockForCriticalRm(t *testing.T) {
	l := permLauncher(config.BaseClean, map[string]any{"skip_critical_rm_prompt": true})
	got, err := l.HostBlockFor("a")
	if err != nil || got != "" {
		t.Fatalf("HostBlockFor = %q, %v, want no lock (the option is a drop)", got, err)
	}
	drops, err := l.HostDropsFor("a")
	if err != nil || len(drops) != 1 || drops[0] != `"Skip the critical-rm prompt" option` {
		t.Fatalf("HostDropsFor = %q, %v", drops, err)
	}
}

func TestAcceptedOptionsHostStripsBoxOnly(t *testing.T) {
	l := permLauncher(config.BaseClean, map[string]any{"skip_critical_rm_prompt": false, "permission_mode": "plan"})
	opts := map[string]any{"skip_critical_rm_prompt": true, "permission_mode": "bypassPermissions"}
	box := l.AcceptedOptions(SpawnSpec{Agent: "a", Runtime: config.RuntimeBox, Options: opts})
	if _, ok := box["skip_critical_rm_prompt"]; !ok {
		t.Fatalf("box keeps the box-only key: %v", box)
	}
	host := l.AcceptedOptions(SpawnSpec{Agent: "a", Runtime: config.RuntimeHost, Options: opts})
	if _, ok := host["skip_critical_rm_prompt"]; ok {
		t.Fatalf("host strips the box-only key: %v", host)
	}
	if host["permission_mode"] != "bypassPermissions" {
		t.Fatalf("host keeps the other overrides: %v", host)
	}
}
