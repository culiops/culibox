package clone

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

const mcpNode = `{"mcpServers":{"s":{"command":"node","args":["./tools/mcp/server.js"]}}}`

func hookSettings(cmd string) string {
	return `{"hooks":{"PostToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"` + cmd + `"}]}]}}`
}

// The files a host agent, direnv or mise runs or loads from the repo are
// guarded with their folders, and the manifest of a task runner a config calls
// (#370).
func TestMergeHomeConfigRefsDenied(t *testing.T) {
	cases := []struct {
		name    string
		host    map[string]string // committed on the host first
		untrack map[string]string // written in the host work tree, not committed
		change  string            // the file the box changes
		guarded bool
	}{
		// .mcp.json
		{"mcp server script", map[string]string{".mcp.json": mcpNode}, nil, "tools/mcp/server.js", true},
		{"mcp server neighbour", map[string]string{".mcp.json": mcpNode}, nil, "tools/mcp/lib/x.js", true},
		{"mcp server, folder level", map[string]string{".mcp.json": mcpNode}, nil, "tools/mcp/other.json", true},
		{"mcp server, a sibling folder is free", map[string]string{".mcp.json": mcpNode}, nil, "tools/other/x.js", false},
		{"mcp node runs package.json", map[string]string{".mcp.json": mcpNode}, nil, "package.json", true},
		{"mcp unrelated source", map[string]string{".mcp.json": mcpNode}, nil, "src/app.js", false},
		{"mcp uv script", map[string]string{".mcp.json": `{"mcpServers":{"s":{"command":"uv","args":["run","tools/mcp.py"]}}}`}, nil, "tools/helper.py", true},
		{"mcp uv runs pyproject", map[string]string{".mcp.json": `{"mcpServers":{"s":{"command":"uv","args":["run","tools/mcp.py"]}}}`}, nil, "pyproject.toml", true},
		{"mcp npx runs package.json and .npmrc", map[string]string{".mcp.json": `{"mcpServers":{"s":{"command":"npx","args":["-y","some-server"]}}}`}, nil, "package.json", true},
		{"mcp flag value", map[string]string{".mcp.json": `{"mcpServers":{"s":{"command":"srv","args":["--config=./conf/s.json"]}}}`}, nil, "conf/s.json", true},
		{"mcp env value", map[string]string{".mcp.json": `{"mcpServers":{"s":{"command":"srv","env":{"NODE_OPTIONS":"--require ./hooks/r.js"}}}}`}, nil, "hooks/r.js", true},
		{"mcp top-level script names only itself", map[string]string{".mcp.json": `{"mcpServers":{"s":{"command":"bash","args":["server.sh"]}}}`}, nil, "server.sh", true},
		{"mcp top-level script, README still merges", map[string]string{".mcp.json": `{"mcpServers":{"s":{"command":"bash","args":["server.sh"]}}}`}, nil, "README.md", false},
		{"mcp absolute path inside the repo", map[string]string{".mcp.json": `{"mcpServers":{"s":{"command":"bash","args":["<OF>/srv/run.sh"]}}}`}, nil, "srv/run.sh", true},
		{"mcp path outside the repo", map[string]string{".mcp.json": `{"mcpServers":{"s":{"command":"/opt/tool/run.sh","args":["../outside/x.js"]}}}`}, nil, "tool/run.sh", false},
		{"mcp untracked .mcp.json", nil, map[string]string{".mcp.json": mcpNode}, "tools/mcp/lib/x.js", true},
		// .claude/settings*.json
		{"hook script", map[string]string{".claude/settings.json": hookSettings("bash scripts/format.sh")}, nil, "scripts/format.sh", true},
		{"hook script folder", map[string]string{".claude/settings.json": hookSettings("bash scripts/format.sh")}, nil, "scripts/other.sh", true},
		{"hook script, other folder", map[string]string{".claude/settings.json": hookSettings("bash scripts/format.sh")}, nil, "docs/x.md", false},
		{"hook npm run", map[string]string{".claude/settings.json": hookSettings("npm run lint")}, nil, "package.json", true},
		{"hook npm run, other manifests are free", map[string]string{".claude/settings.json": hookSettings("npm run lint")}, nil, "Cargo.toml", false},
		{"hook make", map[string]string{".claude/settings.json": hookSettings("make fmt")}, nil, "Makefile", true},
		{"hook just", map[string]string{".claude/settings.json": hookSettings("just fmt")}, nil, "justfile", true},
		{"hook task", map[string]string{".claude/settings.json": hookSettings("task fmt")}, nil, "Taskfile.yml", true},
		{"hook cargo", map[string]string{".claude/settings.json": hookSettings("cargo fmt")}, nil, "build.rs", true},
		{"hook go", map[string]string{".claude/settings.json": hookSettings("go vet ./...")}, nil, "go.mod", true},
		{"hook project dir variable", map[string]string{".claude/settings.json": hookSettings(`\"$CLAUDE_PROJECT_DIR\"/tools/h/run.py`)}, nil, "tools/h/run.py", true},
		{"hook project dir variable, braces", map[string]string{".claude/settings.json": hookSettings("${CLAUDE_PROJECT_DIR}/tools/h/run.py")}, nil, "tools/h/other.py", true},
		{"statusLine", map[string]string{".claude/settings.json": `{"statusLine":{"type":"command","command":"./bin/status.sh"}}`}, nil, "bin/status.sh", true},
		{"statusLine neighbour", map[string]string{".claude/settings.json": `{"statusLine":{"type":"command","command":"./bin/status.sh"}}`}, nil, "bin/more.sh", true},
		{"apiKeyHelper", map[string]string{".claude/settings.json": `{"apiKeyHelper":"./keys/get.sh"}`}, nil, "keys/get.sh", true},
		{"otelHeadersHelper", map[string]string{".claude/settings.json": `{"otelHeadersHelper":"./otel/h.sh"}`}, nil, "otel/h.sh", true},
		{"awsAuthRefresh", map[string]string{".claude/settings.json": `{"awsAuthRefresh":"./aws/refresh.sh"}`}, nil, "aws/refresh.sh", true},
		{"awsCredentialExport", map[string]string{".claude/settings.json": `{"awsCredentialExport":"./aws/export.sh"}`}, nil, "aws/export.sh", true},
		{"settings env BASH_ENV", map[string]string{".claude/settings.json": `{"env":{"BASH_ENV":"./sh/env.sh"}}`}, nil, "sh/env.sh", true},
		{"settings mcpServers", map[string]string{".claude/settings.json": `{"mcpServers":{"s":{"command":"node","args":["./m/s.js"]}}}`}, nil, "m/s.js", true},
		{"settings.local.json untracked", nil, map[string]string{".claude/settings.local.json": hookSettings("bash scripts/format.sh")}, "scripts/other.sh", true},
		{"marketplace directory", map[string]string{".claude/settings.json": `{"extraKnownMarketplaces":{"m":{"source":{"source":"directory","path":"./plugins/mp"}}}}`}, nil, "plugins/mp/.claude-plugin/marketplace.json", true},
		{"marketplace directory, a sibling is free", map[string]string{".claude/settings.json": `{"extraKnownMarketplaces":{"m":{"source":{"source":"directory","path":"./plugins/mp"}}}}`}, nil, "plugins/other/x.json", false},
		{"marketplace by url is not a path", map[string]string{".claude/settings.json": `{"extraKnownMarketplaces":{"m":{"source":{"source":"github","repo":"o/r"}}}}`}, nil, "o/r/x", false},
		// .envrc and .envrc.local
		{"envrc source_env", map[string]string{".envrc": "source_env config/env.sh\n"}, nil, "config/env.sh", true},
		{"envrc source_env_if_exists then dotenv", map[string]string{".envrc": "source_env_if_exists .envrc.local\ndotenv\n"}, nil, ".envrc.local", true},
		{"envrc dotenv adds .env", map[string]string{".envrc": "dotenv\n"}, nil, ".env", true},
		{"envrc dotenv_if_exists named file", map[string]string{".envrc": "dotenv_if_exists cfg/vars.env\n"}, nil, "cfg/vars.env", true},
		{"envrc watch_file", map[string]string{".envrc": "watch_file tools/x.cfg\n"}, nil, "tools/x.cfg", true},
		{"envrc source", map[string]string{".envrc": "source ./lib/helpers.sh\n"}, nil, "lib/helpers.sh", true},
		{"envrc dot command", map[string]string{".envrc": ". ./lib/dot.sh\n"}, nil, "lib/dot.sh", true},
		{"envrc PATH_add", map[string]string{".envrc": "PATH_add bin\n"}, nil, "bin/tool", true},
		{"envrc path_add", map[string]string{".envrc": "path_add PATH tools/bin\n"}, nil, "tools/bin/t", true},
		{"envrc export PATH", map[string]string{".envrc": "export PATH=./scripts:$PATH\n"}, nil, "scripts/x", true},
		{"envrc plain command", map[string]string{".envrc": "eval \"$(./scripts/env.sh)\"\n"}, nil, "scripts/other", true},
		{"envrc use flake", map[string]string{".envrc": "use flake\n"}, nil, "flake.nix", true},
		{"envrc use flake with a folder", map[string]string{".envrc": "use flake ./nix#dev\n"}, nil, "nix/flake.nix", true},
		{"envrc use flake with a folder, its lock", map[string]string{".envrc": "use flake ./nix#dev\n"}, nil, "nix/flake.lock", true},
		{"envrc use nix", map[string]string{".envrc": "use nix\n"}, nil, "shell.nix", true},
		{"envrc use nix with a file", map[string]string{".envrc": "use nix nix/dev.nix\n"}, nil, "nix/dev.nix", true},
		{"envrc use_nix spelling", map[string]string{".envrc": "use_nix nix/dev2.nix\n"}, nil, "nix/dev2.nix", true},
		{"envrc use devenv", map[string]string{".envrc": "use devenv\n"}, nil, "devenv.local.nix", true},
		{"envrc comment is not a source", map[string]string{".envrc": "# source_env config/env.sh\n"}, nil, "config/env.sh", false},
		{"envrc unrelated", map[string]string{".envrc": "source_env config/env.sh\n"}, nil, "src/app.go", false},
		{".envrc.local is read too", nil, map[string]string{".envrc.local": "source ./local/x.sh\n"}, "local/x.sh", true},
		// mise
		{"mise _.source", map[string]string{"mise.toml": "[env]\n_.source = \"./scripts/env.sh\"\n"}, nil, "scripts/env.sh", true},
		{"mise _.source folder", map[string]string{"mise.toml": "[env]\n_.source = \"./scripts/env.sh\"\n"}, nil, "scripts/y.sh", true},
		{"mise _.file", map[string]string{"mise.toml": "[env]\n_.file = \"cfg/.vars\"\n"}, nil, "cfg/.vars", true},
		{"mise _.path array on one line", map[string]string{"mise.toml": "[env]\n_.path = [\"tools/bin\", \"x/bin\"]\n"}, nil, "x/bin/t", true},
		{"mise _.path array over lines", map[string]string{"mise.toml": "[env]\n_.path = [\n  \"tools/bin\",\n  # note\n  \"y/bin\",\n]\nOTHER = \"1\"\n"}, nil, "y/bin/t", true},
		{"mise inline _", map[string]string{"mise.toml": "[env]\n_ = { source = \"s/a.sh\", path = [\"p/bin\"] }\n"}, nil, "s/a.sh", true},
		{"mise [env._] table", map[string]string{"mise.toml": "[env._]\nsource = \"e/x.sh\"\n"}, nil, "e/x.sh", true},
		{"mise config_root template", map[string]string{"mise.toml": "[env]\n_.source = \"{{config_root}}/scripts/e.sh\"\n"}, nil, "scripts/e.sh", true},
		{"mise exec template", map[string]string{"mise.toml": "[env]\nTOKEN = \"{{exec(command='./bin/tok.sh')}}\"\n"}, nil, "bin/tok.sh", true},
		{"mise hook", map[string]string{"mise.toml": "[hooks]\nenter = \"./bin/enter.sh\"\n"}, nil, "bin/enter.sh", true},
		{"mise hook runs make", map[string]string{"mise.toml": "[hooks]\ncd = \"make setup\"\n"}, nil, "Makefile", true},
		{"mise hook array", map[string]string{"mise.toml": "[hooks]\nleave = [\n  \"echo bye\",\n  \"./bin/leave.sh\",\n]\n"}, nil, "bin/leave.sh", true},
		{"mise settings env_file", map[string]string{"mise.toml": "[settings]\nenv_file = \"cfg/e.env\"\n"}, nil, "cfg/e.env", true},
		{"mise tasks are not run by themselves", map[string]string{"mise.toml": "[tasks.build]\nrun = \"./scripts/build.sh\"\n"}, nil, "scripts/build.sh", false},
		{".mise.toml is read", map[string]string{".mise.toml": "[env]\n_.source = \"./m1/e.sh\"\n"}, nil, "m1/e.sh", true},
		{"mise.local.toml untracked", nil, map[string]string{"mise.local.toml": "[env]\n_.source = \"./m2/e.sh\"\n"}, "m2/e.sh", true},
		{"mise.dev.toml is read", nil, map[string]string{"mise.dev.toml": "[env]\n_.source = \"./m3/e.sh\"\n"}, "m3/e.sh", true},
		{".config/mise/config.toml is read", map[string]string{".config/mise/config.toml": "[env]\n_.source = \"./m4/e.sh\"\n"}, nil, "m4/e.sh", true},
		{"mise/config.toml is read", map[string]string{"mise/config.toml": "[env]\n_.source = \"./m5/e.sh\"\n"}, nil, "m5/e.sh", true},
		// Hostile or broken files decide nothing.
		{"malformed .mcp.json", map[string]string{".mcp.json": "{not json"}, nil, "tools/x.js", false},
		{"malformed .mcp.json, the rest still counts", map[string]string{".mcp.json": "{not json", ".claude/settings.json": hookSettings("bash scripts/format.sh")}, nil, "scripts/format.sh", true},
		{"wrong shapes", map[string]string{".mcp.json": `{"mcpServers":[1,2,{"command":3}]}`, ".claude/settings.json": `{"hooks":{"A":[null,5,{"hooks":[{"command":7}]}]},"statusLine":9,"env":[1]}`}, nil, "tools/x.js", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			old := mergeSystemGitConfig
			mergeSystemGitConfig = filepath.Join(t.TempDir(), "none")
			t.Cleanup(func() { mergeSystemGitConfig = old })
			x := newMergeFx(t, "main")
			sub := func(m map[string]string) map[string]string {
				if m == nil {
					return nil
				}
				o := map[string]string{}
				for k, v := range m {
					o[k] = strings.ReplaceAll(v, "<OF>", x.of)
				}
				return o
			}
			if c.host != nil {
				x.commit("host", sub(c.host))
			}
			before := x.tip()
			x.box("main", map[string]string{c.change: "box\n"})
			for rel, content := range sub(c.untrack) {
				x.write(rel, content)
			}
			a := &auditRec{}
			got := x.run(newRec(t), a)
			if !c.guarded {
				wantMerged(t, got)
				return
			}
			wantMergeReason(t, got, MergeGuarded)
			if !slices.Contains(got.Paths, c.change) {
				t.Fatalf("Paths = %q, want %q", got.Paths, c.change)
			}
			if x.tip() != before || len(a.calls) != 0 {
				t.Fatalf("main moved or audit written: %q", a.calls)
			}
		})
	}
}

// scanRefs runs every scanner over the given files of a fresh repo dir.
func scanRefs(t *testing.T, files map[string]string) *refSink {
	t.Helper()
	of := t.TempDir()
	for rel, c := range files {
		p := filepath.Join(of, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return scanConfigRefs(of)
}

// Whatever a file holds, reading it never panics and a file that makes no
// sense names nothing.
func TestConfigRefsMalformedInputs(t *testing.T) {
	inputs := []string{
		"", "\x00", "\x00\x00\x00", "{", "}", "[", "]", `"`, `'`, `"""`, "'''", "{{", "}}", "=", "[[", "]]",
		"{not json", "[1,2,3]", `"just a string"`, "null", "123", "true",
		`{"mcpServers":null}`, `{"mcpServers":"x"}`, `{"mcpServers":{"a":null}}`, `{"mcpServers":{"a":{"command":null,"args":null}}}`,
		`{"mcpServers":{"a":{"command":["x"],"args":"y","env":"z"}}}`, `{"mcpServers":{"a":{"args":[null,1,{"x":2},["y"]]}}}`,
		`{"hooks":null}`, `{"hooks":"x"}`, `{"hooks":[{"hooks":[{"command":{}}]}]}`, `{"hooks":{"E":[[[[[[]]]]]]}}`,
		`{"statusLine":null,"apiKeyHelper":5,"extraKnownMarketplaces":[1],"env":null}`,
		`{"extraKnownMarketplaces":{"m":{"source":{"path":5}}}}`, `{"extraKnownMarketplaces":{"m":{"source":"x"}}}`,
		strings.Repeat("[", 5000), strings.Repeat(`{"a":`, 5000),
		strings.Repeat("source_env ", 10000), "source_env\n", "dotenv\x00\n", "use\n", "use flake #\n", "use flake path:\n",
		"PATH_add\n", "path_add\n", "use flake ../../../../etc\n", "use nix /abs/outside.nix\n", "source_env \"\n", "source_env '\n",
		"[env\n_.source = \n", "[env]\n_.source = [\n", "[env]\n_.source = [\"a\", \n\n\n", "[env]\n_ = {\n", "[hooks]\nenter = \"\"\"\n",
		"[env]\n_.source = \"\"\"\n./a.sh\n", "[env]\n= \"x\"\n", "[env]\n_.path = [[[[[[\n", "[[env]]\n[[hooks]]\n",
		"[hooks]\n\x00\x01\x02\n", "[env]\n_.source = \"../../../outside.sh\"\n", "[env]\n_.source = \"/etc/passwd\"\n",
		strings.Repeat("\xff\xfe", 100),
	}
	files := []string{".mcp.json", ".claude/settings.json", ".claude/settings.local.json", ".envrc", ".envrc.local", "mise.toml", ".mise.toml", "mise.local.toml", "mise.dev.toml"}
	for i, in := range inputs {
		for _, f := range files {
			func() {
				defer func() {
					if p := recover(); p != nil {
						t.Errorf("input %d (%q) in %s panicked: %v", i, in, f, p)
					}
				}()
				s := scanRefs(t, map[string]string{f: in})
				for _, r := range s.paths() {
					if r == "" || r == "." || strings.HasPrefix(r, "../") || strings.HasPrefix(r, "/") {
						t.Errorf("input %d (%q) in %s produced the path %q", i, in, f, r)
					}
				}
			}()
		}
	}
}

// Reads are bounded in count and size, a FIFO or a link out of the repo is
// never followed, and a file past the size limit is read to the limit only.
func TestConfigRefsBounded(t *testing.T) {
	t.Run("size", func(t *testing.T) {
		pad := strings.Repeat("# padding\n", (mergeImportBytes/10)+10)
		s := scanRefs(t, map[string]string{".envrc": "source_env early/a.sh\n" + pad + "source_env late/b.sh\n"})
		got := s.paths()
		if !slices.Contains(got, "early/a.sh") || slices.Contains(got, "late/b.sh") {
			t.Fatalf("paths = %q, want the early one only", got)
		}
	})
	t.Run("file count", func(t *testing.T) {
		files := map[string]string{}
		for i := 0; i < 200; i++ {
			files["mise."+strings.Repeat("a", 1+i%7)+string(rune('a'+i%26))+string(rune('a'+i/26))+".toml"] = "[hooks]\nenter = \"./h/x.sh\"\n"
		}
		s := scanRefs(t, files)
		if s.reads > mergeRefFiles {
			t.Fatalf("reads = %d, want at most %d", s.reads, mergeRefFiles)
		}
	})
	t.Run("path count", func(t *testing.T) {
		var b strings.Builder
		for i := 0; i < 3*mergeRefPaths; i++ {
			b.WriteString("source_env d" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + "/f" + strings.Repeat("y", i/26) + "\n")
		}
		s := scanRefs(t, map[string]string{".envrc": b.String()})
		if n := len(s.paths()); n > 2*mergeRefPaths {
			t.Fatalf("paths = %d, want at most %d", n, 2*mergeRefPaths)
		}
	})
	t.Run("fifo", func(t *testing.T) {
		of := t.TempDir()
		if err := syscall.Mkfifo(filepath.Join(of, ".mcp.json"), 0o644); err != nil {
			t.Skip("no mkfifo")
		}
		done := make(chan *refSink)
		go func() { done <- scanConfigRefs(of) }()
		select {
		case s := <-done:
			if len(s.paths()) != 0 {
				t.Fatalf("paths = %q from a FIFO", s.paths())
			}
		case <-time.After(10 * time.Second):
			t.Fatal("reading a FIFO blocked")
		}
	})
	t.Run("link out of the repo", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(out, []byte(mcpNode), 0o644); err != nil {
			t.Fatal(err)
		}
		of := t.TempDir()
		if err := os.Symlink(out, filepath.Join(of, ".mcp.json")); err != nil {
			t.Fatal(err)
		}
		if s := scanConfigRefs(of); len(s.paths()) != 0 {
			t.Fatalf("paths = %q through a link out of the repo", s.paths())
		}
	})
	t.Run("link inside the repo", func(t *testing.T) {
		of := t.TempDir()
		if err := os.MkdirAll(filepath.Join(of, "config"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(of, "config", "mcp.json"), []byte(mcpNode), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("config/mcp.json", filepath.Join(of, ".mcp.json")); err != nil {
			t.Fatal(err)
		}
		if got := scanConfigRefs(of).paths(); !slices.Contains(got, "tools/mcp/server.js") {
			t.Fatalf("paths = %q, want the server script through an inside link", got)
		}
	})
}

// The func returns the concrete list the symlink check resolves, and what it
// matches is at, under or a parent of each entry, case-folded.
func TestConfigRefDeniesList(t *testing.T) {
	x := newMergeFx(t, "main")
	x.commit("host", map[string]string{".mcp.json": mcpNode})
	m := &merger{of: x.of}
	deny, refs := m.configRefDenies()
	for _, want := range []string{"tools/mcp/server.js", "tools/mcp", "package.json"} {
		if !slices.Contains(refs, want) {
			t.Errorf("refs = %q, missing %q", refs, want)
		}
	}
	if slices.Contains(refs, "tools") || slices.Contains(refs, ".") || slices.Contains(refs, "") {
		t.Errorf("refs = %q hold the repo top or a folder above the script's", refs)
	}
	for p, want := range map[string]bool{
		"tools/mcp/server.js": true, "Tools/MCP/x.js": true, "tools/mcp": true, "tools": true, // a parent: a link there re-points the script
		"tools/mcp.txt": false, "src/a.go": false, "PACKAGE.JSON": true,
	} {
		if got := deny(p); got != want {
			t.Errorf("deny(%q) = %v, want %v", p, got, want)
		}
	}
}

// An empty or unreadable repo scans without a word.
func TestConfigRefsNoRepo(t *testing.T) {
	deny, refs := (&merger{of: filepath.Join(t.TempDir(), "missing")}).configRefDenies()
	if len(refs) != 0 || deny("a") {
		t.Fatalf("refs = %q", refs)
	}
}
