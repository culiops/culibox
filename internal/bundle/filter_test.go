package bundle

import (
	"os"
	"path/filepath"
	"testing"
)

func writeMCP(t *testing.T, doc string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".claude.json")
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Classification is by SHAPE. These assertions deliberately describe shapes, not
// server names — the same rule has to work for configs this project has never
// seen, on machines it was never written on.
func TestMCPClassificationByShape(t *testing.T) {
	path := writeMCP(t, `{"mcpServers":{
      "remote-typed":   {"type":"http","url":"https://example.invalid/mcp"},
      "remote-untyped": {"url":"https://example.invalid/other"},
      "present-cmd":    {"type":"stdio","command":"npx","args":["-y","pkg@1.2.3"]},
      "absent-cmd":     {"command":"uvx","args":["thing"]},
      "container-cmd":  {"command":"docker","args":["run","-i","img"]},
      "abs-host-path":  {"command":"/opt/local/bin/thing"},
      "substituted":    {"command":"npx","args":["--token","${SOME_TOKEN}"]},
      "literal-env":    {"command":"npx","env":{"API_KEY":"sk-literal"}},
      "neither":        {"args":["x"]}
    }}`)
	kept, drops := filterMCP(path, fakeProber{"npx": true})

	for _, want := range []string{"remote-typed", "remote-untyped", "present-cmd"} {
		if _, ok := kept[want]; !ok {
			t.Errorf("%s should have been kept; drops=%v", want, drops)
		}
	}
	for _, want := range []string{"absent-cmd", "container-cmd", "abs-host-path", "substituted", "literal-env", "neither"} {
		if _, ok := kept[want]; ok {
			t.Errorf("%s should have been dropped", want)
		}
	}
	// Every drop must carry a reason, so losing a server is explainable.
	for _, d := range drops {
		if d.Reason == "" {
			t.Errorf("drop %q has no reason", d.Name)
		}
	}
}

// A remote server with no explicit type is valid; the transport is inferred.
// Rejecting it would silently lose a working server.
func TestUntypedRemoteServerIsKept(t *testing.T) {
	path := writeMCP(t, `{"mcpServers":{"r":{"url":"https://example.invalid/mcp"}}}`)
	kept, _ := filterMCP(path, DenyAllProber{})
	if _, ok := kept["r"]; !ok {
		t.Fatal("an untyped remote server must be kept")
	}
}

// With no way to probe the image, no stdio server may be assumed to work:
// failing closed keeps a broken server out instead of producing a session that
// dies confusingly.
func TestWithoutAProberNoStdioServerSurvives(t *testing.T) {
	path := writeMCP(t, `{"mcpServers":{"s":{"command":"npx"}}}`)
	kept, drops := filterMCP(path, DenyAllProber{})
	if len(kept) != 0 {
		t.Fatalf("expected no servers, got %v", kept)
	}
	if len(drops) == 0 {
		t.Fatal("the drop must be recorded")
	}
}

func TestMissingMCPFileIsRecordedNotFatal(t *testing.T) {
	kept, drops := filterMCP(filepath.Join(t.TempDir(), "nope.json"), DenyAllProber{})
	if len(kept) != 0 {
		t.Fatal("no servers expected")
	}
	if len(drops) != 1 || drops[0].Kind != "mcp" {
		t.Fatalf("a missing config must be recorded, got %+v", drops)
	}
}

// settings.json is the more dangerous file: hooks run commands, env can hold
// literal tokens, apiKeyHelper points at a host script. A keep-list means a
// future key stays out until someone decides otherwise.
func TestSettingsKeepListDropsEverythingDangerous(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(p, []byte(`{
      "model":"claude-opus-5",
      "effortLevel":"high",
      "hooks":{"SessionStart":[{"command":"curl evil.example"}]},
      "env":{"TOKEN":"sk-literal"},
      "apiKeyHelper":"/home/someone/bin/key.sh",
      "awsAuthRefresh":"/home/someone/bin/aws.sh",
      "permissions":{"defaultMode":"bypassPermissions"},
      "enableAllProjectMcpServers":true
    }`), 0o600); err != nil {
		t.Fatal(err)
	}
	kept, drops := filterSettings(p)

	if kept["model"] != "claude-opus-5" || kept["effortLevel"] != "high" {
		t.Fatalf("keep-listed settings were lost: %v", kept)
	}
	for _, bad := range []string{"hooks", "env", "apiKeyHelper", "awsAuthRefresh", "permissions", "enableAllProjectMcpServers"} {
		if _, ok := kept[bad]; ok {
			t.Errorf("%q must not cross into the box", bad)
		}
		var named bool
		for _, d := range drops {
			if d.Name == bad {
				named = true
			}
		}
		if !named {
			t.Errorf("dropping %q must be recorded in the lock", bad)
		}
	}
}

// Account settings inheritance copies hooks/env/permissions/enabledPlugins into
// HOST-tier account dirs only. The boxed bundle must stay exactly the keep-list:
// pin the bytes so any widening shows up as a diff rather than a surprise.
func TestBoxedSettingsOutputIsByteIdenticalDespiteInheritableKeys(t *testing.T) {
	src := t.TempDir()
	in := `{
      "model":"opus","effortLevel":"high",
      "statusLine":{"type":"command","command":"/home/u/.claude/statusline-command.sh"},
      "hooks":{"PreToolUse":[{"matcher":"Bash"}]},
      "env":{"CLAUDE_CODE_SUBAGENT_MODEL":"sonnet"},
      "permissions":{"deny":["Read(.ssh/**)"]},
      "enabledPlugins":{"superpowers@official":true},
      "deny":["Read(.env*)"]}`
	if err := os.WriteFile(filepath.Join(src, "settings.json"), []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	b := buildForTest(t, src)
	got, err := os.ReadFile(filepath.Join(b.Dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{
  "effortLevel": "high",
  "model": "opus",
  "statusLine": {
    "command": "AGENTBOX_STATUSLINE_DELEGATE_FILE=/opt/agent-bundle/statusline-delegate /opt/agent-bundle/statusline-ratelimits.sh",
    "type": "command"
  }
}
`
	if string(got) != want {
		t.Fatalf("boxed settings.json changed:\n%s", got)
	}
}

// enabledPlugins crosses the filter; whether it reaches the box is decided
// separately, by whether the plugin set is actually mounted (see plugins.go).
func TestEnabledPluginsSurvivesTheSettingsFilter(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(p, []byte(`{"enabledPlugins":{"superpowers@official":true},"hooks":{"Stop":[]},"env":{"T":"x"},"permissions":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	kept, drops := filterSettings(p)
	if _, ok := kept["enabledPlugins"]; !ok {
		t.Fatalf("enabledPlugins must be kept: %v", kept)
	}
	for _, d := range drops {
		if d.Name == "enabledPlugins" {
			t.Fatalf("enabledPlugins must not be reported as dropped: %+v", d)
		}
	}
	for _, bad := range []string{"hooks", "env", "permissions"} {
		if _, ok := kept[bad]; ok {
			t.Errorf("%q must still be dropped", bad)
		}
	}
}
