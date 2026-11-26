package clone

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// noManagedSettings points the managed Claude Code settings at a file that is
// not there, so the host's own never joins a test.
func noManagedSettings(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "managed-settings.json")
	old := mergeManagedSettings
	mergeManagedSettings = p
	t.Cleanup(func() { mergeManagedSettings = old })
	return p
}

func (x *mergeFx) runHome(home string) *MergeResult {
	x.t.Helper()
	return MergeHome(context.Background(), newRec(x.t), MergeRequest{Of: x.of, Home: home, Audit: (&auditRec{}).fn, CloneTips: x.tips()})
}

func userSettingsHook(cmd string) string { return hookSettings(cmd) }

// project is the ~/.claude.json of a user with one project.
func claudeJSONProject(key, servers string) string {
	return `{"numStartups":3,"projects":{"` + key + `":{"allowedTools":[],"mcpServers":` + servers + `}}}`
}

const userServers = `{"s":{"command":"node","args":["./tools/mcp/server.js"]}}`

// The user's own Claude Code config runs repo files too: the project's entry in
// ~/.claude.json, its user-scope servers, ~/.claude/settings*.json and the
// managed settings (#370).
func TestMergeHomeUserClaudeConfigs(t *testing.T) {
	big := strings.Repeat("x", 3<<20)
	cases := []struct {
		name    string
		home    map[string]string // under the user's home; <OF> is the repo path
		managed string            // the managed settings file, "" none
		change  string
		want    string // MergeGuarded, MergeFailed or "" for merged
	}{
		{"project mcp server", map[string]string{".claude.json": claudeJSONProject("<OF>", userServers)}, "", "tools/mcp/lib/x.js", MergeGuarded},
		{"project mcp server, its folder", map[string]string{".claude.json": claudeJSONProject("<OF>", userServers)}, "", "tools/mcp/other.json", MergeGuarded},
		{"project mcp server, a sibling is free", map[string]string{".claude.json": claudeJSONProject("<OF>", userServers)}, "", "tools/other/x.js", ""},
		{"project mcp server, README still merges", map[string]string{".claude.json": claudeJSONProject("<OF>", userServers)}, "", "README.md", ""},
		{"project key with a trailing slash", map[string]string{".claude.json": claudeJSONProject("<OF>/", userServers)}, "", "tools/mcp/lib/x.js", MergeGuarded},
		{"another project's servers", map[string]string{".claude.json": claudeJSONProject("/elsewhere/repo", userServers)}, "", "tools/mcp/lib/x.js", ""},
		{"project env and cwd", map[string]string{".claude.json": claudeJSONProject("<OF>", `{"s":{"command":"srv","cwd":"w9","env":{"NODE_OPTIONS":"--require ./hooks/r.js"}}}`)}, "", "w9/a.js", MergeGuarded},
		{"project env value", map[string]string{".claude.json": claudeJSONProject("<OF>", `{"s":{"command":"srv","env":{"X":"./hooks/r.js"}}}`)}, "", "hooks/r.js", MergeGuarded},
		{"user-scope servers", map[string]string{".claude.json": `{"mcpServers":` + userServers + `}`}, "", "tools/mcp/lib/x.js", MergeGuarded},
		{"user-scope servers, after other keys", map[string]string{".claude.json": `{"a":[1,{"b":2}],"mcpServers":` + userServers + `,"z":null}`}, "", "tools/mcp/lib/x.js", MergeGuarded},
		{"a big file, the project at the end", map[string]string{".claude.json": `{"projects":{"/other":{"history":"` + big + `"},"<OF>":{"mcpServers":` + userServers + `}}}`}, "", "tools/mcp/lib/x.js", MergeGuarded},
		{"a big file, nothing for this project", map[string]string{".claude.json": `{"projects":{"/other":{"history":"` + big + `"}}}`}, "", "tools/mcp/lib/x.js", ""},
		{"projects null, mcpServers null", map[string]string{".claude.json": `{"projects":null,"mcpServers":null}`}, "", "tools/mcp/lib/x.js", ""},
		{"projects an array", map[string]string{".claude.json": `{"projects":[1,[2],{"a":3}],"mcpServers":` + userServers + `}`}, "", "tools/mcp/lib/x.js", MergeGuarded},
		{"project entry without servers", map[string]string{".claude.json": `{"projects":{"<OF>":{"x":1}}}`}, "", "tools/mcp/lib/x.js", ""},
		{"user settings hook", map[string]string{".claude/settings.json": userSettingsHook("bash scripts/format.sh")}, "", "scripts/format.sh", MergeGuarded},
		{"user settings hook, its folder", map[string]string{".claude/settings.json": userSettingsHook("bash scripts/format.sh")}, "", "scripts/other.sh", MergeGuarded},
		{"user settings hook, another folder is free", map[string]string{".claude/settings.json": userSettingsHook("bash scripts/format.sh")}, "", "docs/x.md", ""},
		{"user settings hook, project dir", map[string]string{".claude/settings.json": userSettingsHook("$CLAUDE_PROJECT_DIR/hk/run.sh")}, "", "hk/other.sh", MergeGuarded},
		{"user settings hook, npm run", map[string]string{".claude/settings.json": userSettingsHook("npm run lint")}, "", "package.json", MergeGuarded},
		{"user settings hook in the home is no repo path", map[string]string{".claude/settings.json": userSettingsHook("~/bin/fmt.sh")}, "", "bin/fmt.sh", ""},
		{"user settings.local.json", map[string]string{".claude/settings.local.json": userSettingsHook("bash scripts/format.sh")}, "", "scripts/other.sh", MergeGuarded},
		{"user statusLine", map[string]string{".claude/settings.json": `{"statusLine":{"type":"command","command":"./bin/status.sh"}}`}, "", "bin/more.sh", MergeGuarded},
		{"user apiKeyHelper", map[string]string{".claude/settings.json": `{"apiKeyHelper":"./keys/get.sh"}`}, "", "keys/get.sh", MergeGuarded},
		{"user settings mcpServers", map[string]string{".claude/settings.json": `{"mcpServers":` + userServers + `}`}, "", "tools/mcp/lib/x.js", MergeGuarded},
		{"user settings, not an object", map[string]string{".claude/settings.json": `[1,2]`}, "", "tools/mcp/lib/x.js", ""},
		{"managed settings hook", nil, userSettingsHook("bash scripts/format.sh"), "scripts/other.sh", MergeGuarded},
		{"managed settings mcp", nil, `{"mcpServers":` + userServers + `}`, "tools/mcp/lib/x.js", MergeGuarded},
		// What cannot be read or parsed refuses the merge.
		{"malformed .claude.json", map[string]string{".claude.json": `{"projects":{"<OF>":{"mcpServers":`}, "", "README.md", MergeFailed},
		{".claude.json that is not an object", map[string]string{".claude.json": `[1]`}, "", "README.md", MergeFailed},
		{".claude.json that is null", map[string]string{".claude.json": `null`}, "", "README.md", MergeFailed},
		{"empty .claude.json", map[string]string{".claude.json": ``}, "", "README.md", MergeFailed},
		{"malformed user settings.json", map[string]string{".claude/settings.json": `{"hooks":`}, "", "README.md", MergeFailed},
		{"malformed user settings.local.json", map[string]string{".claude/settings.local.json": `nope`}, "", "README.md", MergeFailed},
		{"malformed managed settings", nil, `{`, "README.md", MergeFailed},
		{".claude.json is a folder", map[string]string{".claude.json/x": "y"}, "", "README.md", MergeFailed},
		{"user settings.json is a folder", map[string]string{".claude/settings.json/x": "y"}, "", "README.md", MergeFailed},
		{"a .claude file where the folder should be is nothing", map[string]string{".claude": "x"}, "", "README.md", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noSystemConfig(t)
			home := t.TempDir()
			x := newMergeFx(t, "main")
			managed := noManagedSettings(t) // after the fixture, which redirects it too
			x.commit("host", map[string]string{"scripts/format.sh": "h\n", "tools/mcp/server.js": "h\n"})
			for rel, content := range c.home {
				p := filepath.Join(home, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(strings.ReplaceAll(content, "<OF>", x.of)), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if c.managed != "" {
				if err := os.WriteFile(managed, []byte(c.managed), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			before := x.tip()
			x.box("main", map[string]string{c.change: "box\n"})
			a := &auditRec{}
			got := MergeHome(context.Background(), newRec(t), MergeRequest{Of: x.of, Home: home, Audit: a.fn, CloneTips: x.tips()})
			switch c.want {
			case "":
				wantMerged(t, got)
			case MergeGuarded:
				wantMergeReason(t, got, MergeGuarded)
				if !slices.Contains(got.Paths, c.change) {
					t.Fatalf("Paths = %q, want %q", got.Paths, c.change)
				}
			case MergeFailed:
				wantMergeReason(t, got, MergeFailed)
				if !strings.Contains(got.Detail, "cannot read the Claude Code config") {
					t.Fatalf("Detail = %q", got.Detail)
				}
				if got.Attempted {
					t.Fatal("a failed read is not an attempted merge")
				}
			}
			if c.want != "" && (x.tip() != before || len(a.calls) != 0) {
				t.Fatalf("main moved or audit written: %q", a.calls)
			}
		})
	}
}

// A file past 32 MiB, or one that cannot be opened, refuses the merge.
func TestMergeHomeUserClaudeConfigUnreadable(t *testing.T) {
	t.Run("over the cap", func(t *testing.T) {
		noSystemConfig(t)
		noManagedSettings(t)
		home := t.TempDir()
		f, err := os.Create(filepath.Join(home, ".claude.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(mergeUserJSONBytes + 1); err != nil {
			t.Skip("no sparse files")
		}
		f.Close()
		x := newMergeFx(t, "main")
		x.box("main", map[string]string{"README.md": "box\n"})
		got := x.runHome(home)
		wantMergeReason(t, got, MergeFailed)
		if !strings.Contains(got.Detail, "cannot read the Claude Code config") {
			t.Fatalf("Detail = %q", got.Detail)
		}
	})
	t.Run("just under the cap, nothing in it", func(t *testing.T) {
		noSystemConfig(t)
		noManagedSettings(t)
		home := t.TempDir()
		pad := strings.Repeat(" ", 2<<20)
		if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{"+pad+"}"), 0o644); err != nil {
			t.Fatal(err)
		}
		x := newMergeFx(t, "main")
		x.box("main", map[string]string{"README.md": "box\n"})
		wantMerged(t, x.runHome(home))
	})
	t.Run("no permission", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads anything")
		}
		noSystemConfig(t)
		noManagedSettings(t)
		home := t.TempDir()
		p := filepath.Join(home, ".claude.json")
		if err := os.WriteFile(p, []byte("{}"), 0o000); err != nil {
			t.Fatal(err)
		}
		x := newMergeFx(t, "main")
		x.box("main", map[string]string{"README.md": "box\n"})
		wantMergeReason(t, x.runHome(home), MergeFailed)
	})
	t.Run("a FIFO does not block", func(t *testing.T) {
		noSystemConfig(t)
		noManagedSettings(t)
		home := t.TempDir()
		if err := syscall.Mkfifo(filepath.Join(home, ".claude.json"), 0o644); err != nil {
			t.Skip("no mkfifo")
		}
		x := newMergeFx(t, "main")
		x.box("main", map[string]string{"README.md": "box\n"})
		wantMergeReason(t, x.runHome(home), MergeFailed)
	})
	t.Run("no home reads no user file", func(t *testing.T) {
		noSystemConfig(t)
		noManagedSettings(t)
		x := newMergeFx(t, "main")
		x.box("main", map[string]string{"README.md": "box\n"})
		wantMerged(t, x.runHome(""))
	})
}

// The project's key in ~/.claude.json is the path Claude Code was started in:
// the spelling the clone is known by or the one it resolves to.
func TestMergeHomeUserClaudeJSONProjectKey(t *testing.T) {
	setup := func(t *testing.T) (x *mergeFx, link, home string) {
		noSystemConfig(t)
		noManagedSettings(t)
		x = newMergeFx(t, "main")
		x.commit("host", map[string]string{"tools/mcp/server.js": "h\n"})
		link = filepath.Join(t.TempDir(), "lnk")
		if err := os.Symlink(filepath.Dir(x.of), link); err != nil {
			t.Fatal(err)
		}
		return x, filepath.Join(link, filepath.Base(x.of)), t.TempDir()
	}
	run := func(x *mergeFx, of, home string) *MergeResult {
		return MergeHome(context.Background(), newRec(x.t), MergeRequest{Of: of, Home: home, Audit: (&auditRec{}).fn, CloneTips: x.tips()})
	}
	t.Run("the resolved path, clone_of through a link", func(t *testing.T) {
		x, via, home := setup(t)
		os.WriteFile(filepath.Join(home, ".claude.json"), []byte(claudeJSONProject(x.of, userServers)), 0o644)
		x.box("main", map[string]string{"tools/mcp/lib/x.js": "box\n"})
		wantMergeReason(t, run(x, via, home), MergeGuarded)
	})
	t.Run("the spelled path, clone_of through a link", func(t *testing.T) {
		x, via, home := setup(t)
		os.WriteFile(filepath.Join(home, ".claude.json"), []byte(claudeJSONProject(via, userServers)), 0o644)
		x.box("main", map[string]string{"tools/mcp/lib/x.js": "box\n"})
		wantMergeReason(t, run(x, via, home), MergeGuarded)
	})
	t.Run("another path", func(t *testing.T) {
		x, via, home := setup(t)
		os.WriteFile(filepath.Join(home, ".claude.json"), []byte(claudeJSONProject(via+"2", userServers)), 0o644)
		x.box("main", map[string]string{"tools/mcp/lib/x.js": "box\n"})
		wantMerged(t, run(x, via, home))
	})
}

func TestManagedSettingsPath(t *testing.T) {
	for goos, want := range map[string]string{
		"linux":   "/etc/claude-code/managed-settings.json",
		"darwin":  "/Library/Application Support/ClaudeCode/managed-settings.json",
		"windows": "",
		"freebsd": "",
	} {
		if got := managedSettingsPath(goos); got != want {
			t.Errorf("managedSettingsPath(%q) = %q, want %q", goos, got, want)
		}
	}
}
