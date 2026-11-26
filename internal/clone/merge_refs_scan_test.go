package clone

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// mcpCmd is an .mcp.json whose one server runs command with args.
func mcpCmd(command string, args ...string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]any{"mcpServers": map[string]any{"s": map[string]any{"command": command, "args": args}}})
	return b.String()
}

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
		{"mcp npx -y: package.json", map[string]string{".mcp.json": `{"mcpServers":{"s":{"command":"npx","args":["-y","some-server"]}}}`}, nil, "package.json", true},
		{"mcp npx versioned spec: package.json still refused (#370 R1c)", map[string]string{".mcp.json": `{"mcpServers":{"s":{"command":"npx","args":["-y","some-server@1"]}}}`}, nil, "package.json", true},
		{"mcp local npx runs package.json", map[string]string{".mcp.json": `{"mcpServers":{"s":{"command":"npx","args":["some-server"]}}}`}, nil, "package.json", true},
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
		{"hook uv sync, setup.py", map[string]string{".claude/settings.json": hookSettings("uv sync"), "pyproject.toml": "[project]\n"}, nil, "setup.py", true},
		{"hook poetry install, setup.cfg", map[string]string{".claude/settings.json": hookSettings("poetry install")}, nil, "setup.cfg", true},
		{"hook pip install, setup.cfg", map[string]string{".claude/settings.json": hookSettings("pip install -e .")}, nil, "setup.cfg", true},
		{"hook pip3 install, pyproject.toml", map[string]string{".claude/settings.json": hookSettings("pip3 install .")}, nil, "pyproject.toml", true},
		{"hook pip install, setup.py", map[string]string{".claude/settings.json": hookSettings("pip install -e .")}, nil, "setup.py", true},
		{"hook task", map[string]string{".claude/settings.json": hookSettings("task fmt")}, nil, "Taskfile.yml", true},
		{"hook cargo", map[string]string{".claude/settings.json": hookSettings("cargo fmt")}, nil, "build.rs", true},
		{"hook go", map[string]string{".claude/settings.json": hookSettings("go vet ./...")}, nil, "go.mod", true},
		{"hook project dir variable", map[string]string{".claude/settings.json": hookSettings(`\"$CLAUDE_PROJECT_DIR\"/tools/h/run.py`)}, nil, "tools/h/run.py", true},
		{"hook project dir variable, braces", map[string]string{".claude/settings.json": hookSettings("${CLAUDE_PROJECT_DIR}/tools/h/run.py")}, nil, "tools/h/other.py", true},
		{"statusLine", map[string]string{".claude/settings.json": `{"statusLine":{"type":"command","command":"./bin/status.sh"}}`}, nil, "bin/status.sh", true},
		{"statusLine neighbour", map[string]string{".claude/settings.json": `{"statusLine":{"type":"command","command":"./bin/status.sh"}}`}, nil, "bin/more.sh", true},
		{"apiKeyHelper", map[string]string{".claude/settings.json": `{"apiKeyHelper":"./keys/get.sh"}`}, nil, "keys/get.sh", true},
		{"otelHeadersHelper", map[string]string{".claude/settings.json": `{"otelHeadersHelper":"./otel/h.sh"}`}, nil, "otel/h.sh", true},
		{"fileSuggestion", map[string]string{".claude/settings.json": `{"fileSuggestion":{"type":"command","command":"./fs/suggest.sh"}}`}, nil, "fs/suggest.sh", true},
		{"gcpAuthRefresh", map[string]string{".claude/settings.json": `{"gcpAuthRefresh":"./gcp/r.sh"}`}, nil, "gcp/r.sh", true},
		{"proxyAuthHelper", map[string]string{".claude/settings.json": `{"proxyAuthHelper":"./px/h.sh"}`}, nil, "px/h.sh", true},
		{"statusLine as a string", map[string]string{".claude/settings.json": `{"statusLine":"./sl/s.sh"}`}, nil, "sl/s.sh", true},
		{"marketplace headersHelper", map[string]string{".claude/settings.json": `{"extraKnownMarketplaces":{"m":{"source":{"source":"url","url":"https://x","headersHelper":"./mk/h.sh"}}}}`}, nil, "mk/h.sh", true},
		{"an unknown key's command is read", map[string]string{".claude/settings.json": `{"futureThing":{"command":"./fx/run.sh"}}`}, nil, "fx/run.sh", true},
		{".mcp.json http server headersHelper", map[string]string{".mcp.json": `{"mcpServers":{"h":{"type":"http","url":"https://x","headersHelper":"./mh/h.sh"}}}`}, nil, "mh/h.sh", true},
		{".mcp.json ws server headersHelper", map[string]string{".mcp.json": `{"mcpServers":{"h":{"type":"ws","url":"https://x","headersHelper":"./mw/h.sh"}}}`}, nil, "mw/h.sh", true},
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
		// Command forms: plain words, modules, folders (I1).
		{"python -m module", map[string]string{".mcp.json": mcpCmd("python", "-m", "my_server")}, nil, "my_server/__main__.py", true},
		{"python -m module, README still merges", map[string]string{".mcp.json": mcpCmd("python", "-m", "my_server")}, nil, "README.md", false},
		{"python3 -m dotted module", map[string]string{".mcp.json": mcpCmd("python3", "-m", "pkg.sub.main")}, nil, "pkg/helper.py", true},
		{"python -m module file", map[string]string{".mcp.json": mcpCmd("python", "-m", "mod1")}, nil, "mod1.py", true},
		{"uv run -m", map[string]string{".mcp.json": mcpCmd("uv", "run", "-m", "svc.app")}, nil, "svc/x.py", true},
		{"node --import bare module", map[string]string{".mcp.json": mcpCmd("node", "--import", "register", "app.js")}, nil, "register.js", true},
		{"node -r bare module", map[string]string{".mcp.json": mcpCmd("node", "-r", "preload", "app.js")}, nil, "preload/index.js", true},
		{"node --require bare module", map[string]string{".mcp.json": mcpCmd("node", "--require", "boot", "app.js")}, nil, "boot.cjs", true},
		{"node --require=module", map[string]string{".mcp.json": mcpCmd("node", "--require=boot2", "app.js")}, nil, "boot2.js", true},
		{"NODE_OPTIONS require module", map[string]string{".mcp.json": `{"mcpServers":{"s":{"command":"srv","env":{"NODE_OPTIONS":"--require boot3"}}}}`}, nil, "boot3.js", true},
		{"-m outside python is a message", map[string]string{".claude/settings.json": hookSettings("git commit -m notamodule")}, nil, "notamodule/x.py", false},
		{"plain word, folder", map[string]string{".mcp.json": mcpCmd("node", "server"), "server/index.js": "h\n"}, nil, "server/index.js", true},
		{"plain word, folder neighbour", map[string]string{".mcp.json": mcpCmd("node", "server"), "server/index.js": "h\n"}, nil, "server/lib/x.js", true},
		{"plain word, file with extension", map[string]string{".mcp.json": mcpCmd("node", "srv2"), "srv2.js": "h\n"}, nil, "srv2.js", true},
		{"plain word, python file", map[string]string{".mcp.json": mcpCmd("python", "srv3"), "srv3.py": "h\n"}, nil, "srv3.py", true},
		{"plain word, file without extension", map[string]string{".mcp.json": mcpCmd("bash", "runner"), "runner": "h\n"}, nil, "runner", true},
		{"plain word, nothing there", map[string]string{".mcp.json": mcpCmd("node", "nothere")}, nil, "nothere/x.js", true},
		{"plain word, other entries stay free", map[string]string{".mcp.json": mcpCmd("node", "server"), "server/index.js": "h\n"}, nil, "README.md", false},
		{"plain word in a hook", map[string]string{".claude/settings.json": hookSettings("node hookdir"), "hookdir/main.js": "h\n"}, nil, "hookdir/main.js", true},
		{"an argument of a non-interpreter is not looked up", map[string]string{".envrc": "tool envdir\n", "envdir/x.sh": "h\n"}, nil, "envdir/x.sh", false},
		{"plain word in an envrc", map[string]string{".envrc": "node envdir2\n", "envdir2/x.js": "h\n"}, nil, "envdir2/x.js", true},
		// A tool's own config at the repo top (R2-I1).
		{"tool config: eslint.config.js", map[string]string{".claude/settings.json": hookSettings("npx eslint --fix"), "eslint.config.js": "h\n"}, nil, "eslint.config.js", true},
		{"tool config: .eslintrc.cjs", map[string]string{".claude/settings.json": hookSettings("npx eslint --fix"), ".eslintrc.cjs": "h\n"}, nil, ".eslintrc.cjs", true},
		{"tool config: .prettierrc.js", map[string]string{".claude/settings.json": hookSettings("prettier --write ."), ".prettierrc.js": "h\n"}, nil, ".prettierrc.js", true},
		{"tool config: prettier.config.cjs", map[string]string{".claude/settings.json": hookSettings("prettier --write ."), "prettier.config.cjs": "h\n"}, nil, "prettier.config.cjs", true},
		{"tool config: jest.config.ts", map[string]string{".claude/settings.json": hookSettings("jest --bail"), "jest.config.ts": "h\n"}, nil, "jest.config.ts", true},
		{"tool config folds case", map[string]string{".claude/settings.json": hookSettings("jest --bail"), "Jest.Config.ts": "h\n"}, nil, "jest.config.ts", true},
		{"tool config in a cd folder", map[string]string{".claude/settings.json": hookSettings("cd web && jest"), "web/jest.config.js": "h\n"}, nil, "web/jest.config.js", true},
		{"tool config, README still merges", map[string]string{".claude/settings.json": hookSettings("npx eslint --fix"), "eslint.config.js": "h\n"}, nil, "README.md", false},
		{"tool config, tsc has none", map[string]string{".claude/settings.json": hookSettings("tsc --noEmit"), "tsconfig.json": "{}\n"}, nil, "tsconfig.json", false},
		{"tool config, a name that only contains the word", map[string]string{".claude/settings.json": hookSettings("jest --bail"), "myjest.config.ts": "h\n"}, nil, "myjest.config.ts", false},
		// A file the range ADDS is matched by the same rules (A2-I2).
		{"added eslint.config.js ahead of the host's .mjs", map[string]string{".claude/settings.json": hookSettings("npx eslint --fix"), "eslint.config.mjs": "h\n"}, nil, "eslint.config.js", true},
		{"added prettier config when the host has none", map[string]string{".claude/settings.json": hookSettings("prettier --write .")}, nil, "prettier.config.js", true},
		{"added word.js next to the host's word/ folder", map[string]string{".mcp.json": mcpCmd("node", "srvq"), "srvq/index.js": "h\n"}, nil, "srvq.js", true},
		{"added mypy.ini", map[string]string{".claude/settings.json": hookSettings("mypy ."), "pyproject.toml": "[tool.mypy]\n"}, nil, "mypy.ini", true},
		{"added name folds case", map[string]string{".claude/settings.json": hookSettings("npx eslint")}, nil, "ESLint.Config.JS", true},
		{"added unrelated file merges", map[string]string{".claude/settings.json": hookSettings("npx eslint")}, nil, "docs/new.md", false},
		{"script word does not get the prefix rule", map[string]string{".mcp.json": mcpCmd("node", "srvp")}, nil, "srvp.config.js", false},
		{"tool config, git keeps .gitkeep free", map[string]string{".claude/settings.json": hookSettings("git diff --stat"), ".gitkeep": "x\n"}, nil, ".gitkeep", false},
		{".gitignore is protected whatever the hook", nil, nil, ".gitignore", true}, // R7: a rule dropped now decides what a later merge may add
		{".gitignore is protected at any depth", nil, nil, "sub/.gitignore", true},
		{"tool config, git keeps .github free", map[string]string{".claude/settings.json": hookSettings("git status"), ".github/workflows/ci.yml": "x\n"}, nil, ".github/workflows/ci.yml", false},
		{"tool config, bash keeps .bashrc free", map[string]string{".claude/settings.json": hookSettings("bash -c true"), ".bashrc": "x\n"}, nil, ".bashrc", false},
		{"tool config, a one-character word", map[string]string{".claude/settings.json": hookSettings("x run"), "x.txt": "h\n"}, nil, "x.txt", false},
		{"--directory folder", map[string]string{".mcp.json": mcpCmd("uv", "run", "--directory", "mcp", "srv")}, nil, "mcp/x.py", true},
		{"--directory=folder", map[string]string{".mcp.json": mcpCmd("uv", "run", "--directory=mcp2", "srv")}, nil, "mcp2/x.py", true},
		{"--cwd folder", map[string]string{".mcp.json": mcpCmd("srv", "--cwd", "w1")}, nil, "w1/a.js", true},
		{"--prefix folder", map[string]string{".mcp.json": mcpCmd("npm", "--prefix", "w2", "start")}, nil, "w2/a.js", true},
		{"--dir folder", map[string]string{".mcp.json": mcpCmd("srv", "--dir", "w3")}, nil, "w3/a.js", true},
		{"--project folder", map[string]string{".mcp.json": mcpCmd("uv", "run", "--project", "w4", "srv")}, nil, "w4/a.py", true},
		{"-C folder", map[string]string{".mcp.json": mcpCmd("make", "-C", "w5", "serve")}, nil, "w5/a.mk", true},
		{"folder flag, a sibling is free", map[string]string{".mcp.json": mcpCmd("make", "-C", "w5", "serve")}, nil, "w6/a.mk", false},
		{"folder flag, the folder's relative script", map[string]string{".mcp.json": mcpCmd("srv", "--cwd", "w7", "./run")}, nil, "w7/run", true},
		{"the repo top as a folder denies nothing", map[string]string{".mcp.json": mcpCmd("srv", "--directory", ".")}, nil, "README.md", false},
		{"cd then script", map[string]string{".mcp.json": mcpCmd("bash", "-c", "cd tools && ./run")}, nil, "tools/run", true},
		{"cd then script, the folder", map[string]string{".mcp.json": mcpCmd("bash", "-c", "cd tools && ./run")}, nil, "tools/other.sh", true},
		{"cd then script, at the repo top too", map[string]string{".mcp.json": mcpCmd("bash", "-c", "cd tools && ./run")}, nil, "run", true},
		{"cd then script, README still merges", map[string]string{".mcp.json": mcpCmd("bash", "-c", "cd tools && ./run")}, nil, "README.md", false},
		{"cd twice", map[string]string{".mcp.json": mcpCmd("bash", "-c", "cd a1 && cd b1 && ./run")}, nil, "a1/b1/run", true},
		{"cd in a hook", map[string]string{".claude/settings.json": hookSettings("cd tools2 && ./fmt")}, nil, "tools2/fmt", true},
		{"cd to the top", map[string]string{".mcp.json": mcpCmd("bash", "-c", "cd . && ./run")}, nil, "README.md", false},
		{"cd out of the repo", map[string]string{".mcp.json": mcpCmd("bash", "-c", "cd ../x && ./run")}, nil, "x/other", false},
		{"cd to an absolute folder inside", map[string]string{".mcp.json": mcpCmd("bash", "-c", "cd <OF>/abs1 && ./run")}, nil, "abs1/run", true},
		{"cd then a plain word under the folder", map[string]string{".mcp.json": mcpCmd("bash", "-c", "cd tools3 && node srv"), "tools3/srv/index.js": "h\n"}, nil, "tools3/srv/lib.js", true},
		// Hook managers and editor tasks (I3).
		{"husky hook script", map[string]string{".husky/pre-commit": "#!/usr/bin/env sh\nbash hk1/lint.sh\n"}, nil, "hk1/lint.sh", true},
		{"husky hook script folder", map[string]string{".husky/pre-commit": "bash hk1/lint.sh\n"}, nil, "hk1/other.sh", true},
		{"husky hook runs package.json", map[string]string{".husky/pre-commit": "npx lint-staged\n"}, nil, "package.json", true},
		{"husky comment is not read", map[string]string{".husky/pre-commit": "# bash hk2/commented.sh\n"}, nil, "hk2/commented.sh", false},
		{"husky helper dir is skipped", map[string]string{".husky/_/husky.sh": "bash hk3/helper.sh\n"}, nil, "hk3/helper.sh", false},
		{"husky untracked hook", nil, map[string]string{".husky/commit-msg": "./hk4/msg.sh \"$1\"\n"}, "hk4/msg.sh", true},
		{"husky second file", map[string]string{".husky/pre-commit": "true\n", ".husky/pre-push": "bash hk5/push.sh\n"}, nil, "hk5/push.sh", true},
		{"pre-commit entry", map[string]string{".pre-commit-config.yaml": "repos:\n- repo: local\n  hooks:\n  - id: x\n    name: x\n    entry: pc1/check.sh --fix\n    language: script\n"}, nil, "pc1/check.sh", true},
		{"pre-commit entry folder", map[string]string{".pre-commit-config.yaml": "repos:\n- repo: local\n  hooks:\n  - id: x\n    entry: pc1/check.sh\n"}, nil, "pc1/other.py", true},
		{"pre-commit entry quoted", map[string]string{".pre-commit-config.yaml": "repos:\n- repo: local\n  hooks:\n  - id: x\n    entry: \"./pc2/x.sh --fix\"\n"}, nil, "pc2/x.sh", true},
		{"pre-commit entry with a dash", map[string]string{".pre-commit-config.yaml": "repos:\n- repo: local\n  hooks:\n  - entry: pc3/y.sh\n    id: y\n"}, nil, "pc3/y.sh", true},
		{"pre-commit entry folded block", map[string]string{".pre-commit-config.yaml": "repos:\n- repo: local\n  hooks:\n  - id: x\n    entry: >\n      bash\n      pc4/z.sh\n    language: system\n"}, nil, "pc4/z.sh", true},
		{"pre-commit entry literal block", map[string]string{".pre-commit-config.yaml": "repos:\n- repo: local\n  hooks:\n  - id: x\n    entry: |\n      set -e\n      ./pc5/a.sh\n      ./pc5b/b.sh\n    language: system\n"}, nil, "pc5b/b.sh", true},
		{"pre-commit block ends at the next key", map[string]string{".pre-commit-config.yaml": "repos:\n- repo: local\n  hooks:\n  - id: x\n    entry: |\n      true\n    files: pc6/other.py\n"}, nil, "pc6/other.py", false},
		{"pre-commit python -m", map[string]string{".pre-commit-config.yaml": "repos:\n- repo: local\n  hooks:\n  - id: x\n    entry: python -m pc7chk\n"}, nil, "pc7chk/__main__.py", true},
		{"pre-commit other keys are not commands", map[string]string{".pre-commit-config.yaml": "repos:\n- repo: local\n  hooks:\n  - id: x\n    name: pc8/name.sh\n    entry: true\n"}, nil, "pc8/name.sh", false},
		{"pre-commit comment is not read", map[string]string{".pre-commit-config.yaml": "# entry: pc9/c.sh\nrepos: []\n"}, nil, "pc9/c.sh", false},
		{"lefthook run", map[string]string{"lefthook.yml": "pre-commit:\n  commands:\n    lint:\n      run: lh1/lint.sh {staged_files}\n"}, nil, "lh1/lint.sh", true},
		{"lefthook run, dot yaml", map[string]string{".lefthook.yaml": "pre-push:\n  commands:\n    t:\n      run: ./lh2/t.sh\n"}, nil, "lh2/t.sh", true},
		{"lefthook-local run", nil, map[string]string{"lefthook-local.yml": "pre-commit:\n  commands:\n    l:\n      run: ./lh3/l.sh\n"}, "lh3/l.sh", true},
		{"lefthook run block", map[string]string{"lefthook.yml": "pre-commit:\n  commands:\n    l:\n      run: |\n        echo hi\n        ./lh4/l.sh\n      tags: x\n"}, nil, "lh4/l.sh", true},
		{"lefthook run list item", map[string]string{"lefthook.yml": "pre-commit:\n  jobs:\n    - run: ./lh5/job.sh\n"}, nil, "lh5/job.sh", true},
		{"lefthook runs yarn", map[string]string{"lefthook.yml": "pre-commit:\n  commands:\n    l:\n      run: yarn lint\n"}, nil, "package.json", true},
		{"lefthook other keys", map[string]string{"lefthook.yml": "pre-commit:\n  commands:\n    l:\n      glob: lh6/*.sh\n      run: true\n"}, nil, "lh6/a.sh", false},
		{"overcommit scalar command", map[string]string{".overcommit.yml": "PreCommit:\n  Custom:\n    enabled: true\n    command: ./oc1/check.sh\n"}, nil, "oc1/check.sh", true},
		{"overcommit list command", map[string]string{".overcommit.yml": "PreCommit:\n  Custom:\n    command: ['bundle', 'exec', 'oc2/rubo.sh']\n"}, nil, "oc2/rubo.sh", true},
		{"overcommit list command over lines", map[string]string{".overcommit.yml": "PreCommit:\n  Custom:\n    command:\n      - bundle\n      - ./oc3/rubo.sh\n    flags: x\n"}, nil, "oc3/rubo.sh", true},
		{"overcommit runs bundle", map[string]string{".overcommit.yml": "PreCommit:\n  Custom:\n    command: ['bundle', 'exec', 'rubocop']\n"}, nil, "Gemfile", true},
		{"vscode task command", map[string]string{".vscode/tasks.json": `{"version":"2.0.0","tasks":[{"label":"b","type":"shell","command":"./vs1/build.sh"}]}`}, nil, "vs1/build.sh", true},
		{"vscode task args", map[string]string{".vscode/tasks.json": `{"tasks":[{"label":"b","command":"python","args":["${workspaceFolder}/vs2/gen.py","--x"]}]}`}, nil, "vs2/gen.py", true},
		{"vscode task args objects", map[string]string{".vscode/tasks.json": `{"tasks":[{"label":"b","command":"bash","args":[{"value":"vs3/a b.sh","quoting":"strong"}]}]}`}, nil, "vs3/a", true},
		{"vscode task os sub-objects", map[string]string{".vscode/tasks.json": `{"tasks":[{"label":"b","command":"true","linux":{"command":"./vs4/l.sh"},"osx":{"command":"vs5/m.sh"},"windows":{"command":"vs6/w.bat","args":["vs7/x.bat"]}}]}`}, nil, "vs7/x.bat", true},
		{"vscode windows backslash path merges", map[string]string{".vscode/tasks.json": `{"tasks":[{"label":"b","command":"true","windows":{"command":".\\tools\\w.bat"}}]}`}, nil, "tools/w.bat", false},
		{"vscode linux variant still refused", map[string]string{".vscode/tasks.json": `{"tasks":[{"label":"b","command":"true","linux":{"command":"./tools/l.sh"}}]}`}, nil, "tools/l.sh", true},
		{"vscode task os sub-object osx", map[string]string{".vscode/tasks.json": `{"tasks":[{"label":"b","command":"true","osx":{"command":"vs5/m.sh"}}]}`}, nil, "vs5/m.sh", true},
		{"vscode jsonc comments and trailing commas", map[string]string{".vscode/tasks.json": "{\n // build\n \"version\": \"2.0.0\", /* x */\n \"tasks\": [\n  {\"label\": \"http://x//y\", \"command\": \"./vs8/b.sh\",},\n ],\n}\n"}, nil, "vs8/b.sh", true},
		{"vscode top-level command", map[string]string{".vscode/tasks.json": `{"version":"0.1.0","command":"./vs9/t.sh","tasks":[]}`}, nil, "vs9/t.sh", true},
		{"vscode malformed", map[string]string{".vscode/tasks.json": `{"tasks":[{"command":"./vs10/t.sh"`}, nil, "vs10/t.sh", false},
		{"vscode wrong shapes", map[string]string{".vscode/tasks.json": `{"tasks":[1,null,{"command":5,"args":"x","linux":3},[]]}`}, nil, "vs11/t.sh", false},
		{"vscode untracked", nil, map[string]string{".vscode/tasks.json": `{"tasks":[{"command":"./vs12/t.sh"}]}`}, "vs12/t.sh", true},
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
	s, err := scanRefsErr(t, files)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// scanRefsErr is scanRefs that hands back the scan's error.
func scanRefsErr(t *testing.T, files map[string]string) (*refSink, error) {
	t.Helper()
	noManagedSettings(t)
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
	return scanConfigRefs(of, nil, "")
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
		"entry:", "entry: >", "entry: |\n", "- entry:\n  - \n  -", "run:\n\n\n", "command: [", "command: ['a',", "entry: \x00", "run: " + strings.Repeat("a ", 10000),
		"entry: |\n" + strings.Repeat("  x\n", 1000), "command:\n" + strings.Repeat("- ./a.sh\n", 1000),
		"cd", "cd ..", "cd -", "python -m", "python -m ..", "node --require", "uv run --directory", "-C", "--cwd=", "--directory ../../x",
		"cd /abs/outside && ./x", "cd " + strings.Repeat("d/", 500) + " && ./x", strings.Repeat("cd a && ", 100) + "./x",
		".\\tools\\x.bat C:\\x\\y.ps1", "bash -c", "npx", "pnpm dlx", "env", "sudo",
		"/* unterminated", "// only a comment", `{"tasks":[{"command":"a",}],}`, "{\"a\":\"/*\"}", "\"unterminated",
		`{"tasks":[{"command":{"value":5},"args":[{"value":{}},[],null]}]}`,
	}
	files := []string{".mcp.json", ".claude/settings.json", ".claude/settings.local.json", ".envrc", ".envrc.local", "mise.toml", ".mise.toml", "mise.local.toml", "mise.dev.toml",
		".husky/pre-commit", ".githooks/pre-commit", ".pre-commit-config.yaml", "lefthook.yml", ".lefthook-local.yaml", ".overcommit.yml", ".vscode/tasks.json"}
	for i, in := range inputs {
		for _, f := range files {
			func() {
				defer func() {
					if p := recover(); p != nil {
						t.Errorf("input %d (%q) in %s panicked: %v", i, in, f, p)
					}
				}()
				s, err := scanRefsErr(t, map[string]string{f: in})
				if err != nil && !errors.Is(err, errScanCap) {
					t.Errorf("input %d (%q) in %s: unexpected error %v", i, in, f, err)
				}
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
		_, err := scanRefsErr(t, map[string]string{".envrc": "source_env early/a.sh\n" + pad + "source_env late/b.sh\n"})
		if !errors.Is(err, errTooBig) {
			t.Fatalf("err = %v, want errTooBig", err)
		}
	})
	t.Run("package.json size", func(t *testing.T) {
		pad := strings.Repeat(" ", mergeImportBytes+10)
		_, err := scanRefsErr(t, map[string]string{".envrc": "npm test\n", "package.json": `{"scripts":{"test":"node t.js"}}` + pad})
		if !errors.Is(err, errTooBig) {
			t.Fatalf("err = %v, want errTooBig", err)
		}
	})
	t.Run("file count", func(t *testing.T) {
		files := map[string]string{}
		for i := 0; i < 200; i++ {
			files["mise."+strings.Repeat("a", 1+i%7)+string(rune('a'+i%26))+string(rune('a'+i/26))+".toml"] = "[hooks]\nenter = \"./h/x.sh\"\n"
		}
		if _, err := scanRefsErr(t, files); !errors.Is(err, errScanCap) {
			t.Fatalf("err = %v, want errScanCap", err)
		}
	})
	t.Run("many runner words on one line", func(t *testing.T) {
		start := time.Now()
		_, err := scanRefsErr(t, map[string]string{".claude/settings.json": hookSettings(strings.Repeat("make ", 8000)), "Makefile": "x:\n\ttrue\n"})
		if d := time.Since(start); d > 2*time.Second*slowFactor {
			t.Fatalf("took %v", d)
		}
		if err != nil && !errors.Is(err, errScanCap) {
			t.Fatalf("err = %v, want nil or errScanCap", err)
		}
		for _, line := range []string{strings.Repeat("make -f a.mk ", 8000), strings.Repeat("npm run x ", 8000), strings.Repeat(`bash -c "make x" `, 8000),
			`bash -c "` + strings.Repeat("make;", 100000) + `"`, `bash -c "` + strings.Repeat("make x ", 100000) + `"`,
			`bash -c "` + strings.Repeat("just&", 100000) + `"`} {
			start = time.Now()
			if ci := parseCommand("", line); !ci.over {
				t.Errorf("%.20q...: no cap hit", line)
			}
			if d := time.Since(start); d > 2*time.Second*slowFactor {
				t.Fatalf("%.20q...: parse took %v", line, d)
			}
		}
	})
	t.Run("hook folder file count", func(t *testing.T) {
		files := map[string]string{}
		for i := 0; i < 3*mergeRefDirFiles; i++ {
			files[".husky/h"+strings.Repeat("a", i%9)+string(rune('a'+i%26))+string(rune('a'+i/26))] = "./h/x.sh\n"
		}
		if _, err := scanRefsErr(t, files); !errors.Is(err, errScanCap) {
			t.Fatalf("err = %v, want errScanCap", err)
		}
	})
	t.Run("hook script size", func(t *testing.T) {
		pad := strings.Repeat("# padding\n", (mergeImportBytes/10)+10)
		_, err := scanRefsErr(t, map[string]string{".husky/pre-commit": "./early/a.sh\n" + pad + "./late/b.sh\n"})
		if !errors.Is(err, errTooBig) {
			t.Fatalf("err = %v, want errTooBig", err)
		}
	})
	t.Run("folder listings", func(t *testing.T) {
		var b strings.Builder
		for i := 0; i < 3*mergeRefListings; i++ {
			b.WriteString("cd d" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + " && word\n")
		}
		if _, err := scanRefsErr(t, map[string]string{".envrc": b.String()}); !errors.Is(err, errScanCap) {
			t.Fatalf("err = %v, want errScanCap", err)
		}
	})
	t.Run("a folder past the listing limit fails closed", func(t *testing.T) {
		files := map[string]string{".envrc": "node zzword\n"}
		for i := 0; i < mergeRefDirScan+10; i++ {
			files["f"+strings.Repeat("0", 5)+string(rune('a'+i%26))+string(rune('a'+(i/26)%26))+string(rune('a'+(i/676)%26))] = ""
		}
		if _, err := scanRefsErr(t, files); !errors.Is(err, errScanCap) {
			t.Fatalf("err = %v, want errScanCap", err)
		}
	})
	t.Run("past the look-up cap fails closed", func(t *testing.T) {
		old := mergeRefPlain
		mergeRefPlain = 1
		t.Cleanup(func() { mergeRefPlain = old })
		if _, err := scanRefsErr(t, map[string]string{".envrc": "node zzfirst\nnode zzsecond\n"}); !errors.Is(err, errScanCap) {
			t.Fatalf("err = %v, want errScanCap", err)
		}
	})
	t.Run("script count", func(t *testing.T) {
		var b strings.Builder
		b.WriteString(`{"scripts":{`)
		for i := 0; i < 2*mergeRefScripts; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`"s` + strconv.Itoa(i) + `":"npm run s` + strconv.Itoa(i+1) + `"`)
		}
		b.WriteString(`}}`)
		files := map[string]string{"package.json": b.String(), ".envrc": "npm run s0\n"}
		if _, err := scanRefsErr(t, files); !errors.Is(err, errScanCap) {
			t.Fatalf("err = %v, want errScanCap", err)
		}
	})
	t.Run("recipe line count", func(t *testing.T) {
		files := map[string]string{".envrc": "make\n", "Makefile": strings.Repeat("x:\n", mergeRefLines) + "\t./late/b.sh\n"}
		if _, err := scanRefsErr(t, files); !errors.Is(err, errScanCap) {
			t.Fatalf("err = %v, want errScanCap", err)
		}
	})
	t.Run("envrc line count", func(t *testing.T) {
		files := map[string]string{".envrc": strings.Repeat("true\n", mergeRefLines) + "source_env late/b.sh\n"}
		if _, err := scanRefsErr(t, files); !errors.Is(err, errScanCap) {
			t.Fatalf("err = %v, want errScanCap", err)
		}
	})
	t.Run("mise line count", func(t *testing.T) {
		files := map[string]string{"mise.toml": strings.Repeat("# c\n", mergeRefLines) + "[hooks]\nenter = \"./late/b.sh\"\n"}
		if _, err := scanRefsErr(t, files); !errors.Is(err, errScanCap) {
			t.Fatalf("err = %v, want errScanCap", err)
		}
	})
	t.Run("hook script line count", func(t *testing.T) {
		files := map[string]string{".husky/pre-commit": strings.Repeat("true\n", mergeRefLines) + "./late/b.sh\n"}
		if _, err := scanRefsErr(t, files); !errors.Is(err, errScanCap) {
			t.Fatalf("err = %v, want errScanCap", err)
		}
	})
	t.Run("yaml hook config line count", func(t *testing.T) {
		pad := strings.Repeat("# c\n", mergeRefLines)
		for name, body := range map[string]string{
			".pre-commit-config.yaml": pad + "  - entry: ./late/b.sh\n",
			"lefthook.yml":            pad + "pre-commit:\n  commands:\n    a:\n      run: ./late/b.sh\n",
		} {
			if _, err := scanRefsErr(t, map[string]string{name: body}); !errors.Is(err, errScanCap) {
				t.Errorf("%s: err = %v, want errScanCap", name, err)
			}
		}
		// a blank or comment tail past the cap is not a refusal
		if _, err := scanRefsErr(t, map[string]string{".pre-commit-config.yaml": strings.Repeat("# c\n", mergeRefLines+5)}); err != nil {
			t.Errorf("comment tail: err = %v, want nil", err)
		}
	})
	t.Run("too many cd folders fails closed", func(t *testing.T) {
		if _, err := scanRefsErr(t, map[string]string{".envrc": strings.Repeat("cd a && ", 20) + "./x\n"}); !errors.Is(err, errScanCap) {
			t.Fatalf("err = %v, want errScanCap", err)
		}
	})
	t.Run("an unreadable cd folder fails closed", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads any folder")
		}
		noManagedSettings(t)
		of := t.TempDir()
		if err := os.WriteFile(filepath.Join(of, ".envrc"), []byte("cd locked && node w\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(of, "locked"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(of, "locked"), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(filepath.Join(of, "locked"), 0o755) })
		_, err := scanConfigRefs(of, nil, "")
		if err == nil || errors.Is(err, errScanCap) {
			t.Fatalf("err = %v, want a read error", err)
		}
	})
	t.Run("a top-level mise listing past the cap fails closed", func(t *testing.T) {
		files := map[string]string{}
		for i := 0; i < mergeRefDirScan+10; i++ {
			files["f"+strings.Repeat("0", 5)+string(rune('a'+i%26))+string(rune('a'+(i/26)%26))+string(rune('a'+(i/676)%26))] = ""
		}
		if _, err := scanRefsErr(t, files); !errors.Is(err, errScanCap) {
			t.Fatalf("err = %v, want errScanCap", err)
		}
	})
	t.Run("path count", func(t *testing.T) {
		var b strings.Builder
		for i := 0; i < 3*mergeRefPaths; i++ {
			b.WriteString("source_env d" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + "/f" + strings.Repeat("y", i/26) + "\n")
		}
		if _, err := scanRefsErr(t, map[string]string{".envrc": b.String()}); !errors.Is(err, errScanCap) {
			t.Fatalf("err = %v, want errScanCap", err)
		}
	})
	t.Run("fifo", func(t *testing.T) {
		of := t.TempDir()
		if err := syscall.Mkfifo(filepath.Join(of, ".mcp.json"), 0o644); err != nil {
			t.Skip("no mkfifo")
		}
		done := make(chan *refSink)
		go func() { s, _ := scanConfigRefs(of, nil, ""); done <- s }()
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
		// A link out of the repo is read at its target (#370 R4a): dotfiles links are normal.
		s, err := scanConfigRefs(of, nil, "")
		if err != nil || errors.Is(err, errScanCap) {
			t.Fatalf("err = %v, want nil", err)
		}
		if !slices.Contains(s.paths(), "tools/mcp/server.js") {
			t.Fatalf("paths = %q, want the server script named by the target", s.paths())
		}
	})
	t.Run("link out of the repo, a loop", func(t *testing.T) {
		of := t.TempDir()
		if err := os.Symlink(".mcp.json", filepath.Join(of, ".mcp.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := scanConfigRefs(of, nil, ""); err == nil || errors.Is(err, errScanCap) {
			t.Fatalf("err = %v, want a read error", err)
		}
	})
	t.Run("link out of the repo, a missing target", func(t *testing.T) {
		of := t.TempDir()
		if err := os.Symlink(filepath.Join(t.TempDir(), "none.json"), filepath.Join(of, ".mcp.json")); err != nil {
			t.Fatal(err)
		}
		if s, err := scanConfigRefs(of, nil, ""); err != nil || len(s.paths()) != 0 {
			t.Fatalf("err = %v, paths = %q, want nothing", err, s.paths())
		}
	})
	t.Run("link out of the repo, over 1 MiB", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "big.json")
		if err := os.WriteFile(out, []byte(mcpNode+strings.Repeat(" ", mergeImportBytes+10)), 0o644); err != nil {
			t.Fatal(err)
		}
		of := t.TempDir()
		if err := os.Symlink(out, filepath.Join(of, ".mcp.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := scanConfigRefs(of, nil, ""); !errors.Is(err, errTooBig) {
			t.Fatalf("err = %v, want errTooBig", err)
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
		if s, _ := scanConfigRefs(of, nil, ""); !slices.Contains(s.paths(), "tools/mcp/server.js") {
			t.Fatalf("paths = %q, want the server script through an inside link", s.paths())
		}
	})
}

// The func returns the concrete list the symlink check resolves, and what it
// matches is at, under or a parent of each entry, case-folded.
func TestConfigRefDeniesList(t *testing.T) {
	x := newMergeFx(t, "main")
	x.commit("host", map[string]string{".mcp.json": mcpNode})
	m := &merger{of: x.of}
	rd, err := m.configRefDenies("", nil)
	deny, refs := rd.denied, rd.paths
	if err != nil {
		t.Fatal(err)
	}
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
	rd, err := (&merger{of: filepath.Join(t.TempDir(), "missing")}).configRefDenies("", nil)
	if err != nil {
		t.Fatal(err)
	}
	deny, refs := rd.denied, rd.paths
	if len(refs) != 0 || deny("a") {
		t.Fatalf("refs = %q", refs)
	}
}

// A scan that hits a cap ends the merge failed, not a merge that saw fewer paths.
func TestMergeHomeConfigRefCapFailsClosed(t *testing.T) {
	noManagedSettings(t)
	noSystemConfig(t)
	x := newMergeFx(t, "main")
	var b strings.Builder
	for i := 0; i < 3*mergeRefPaths; i++ {
		b.WriteString("source_env d" + strconv.Itoa(i) + "/f\n")
	}
	x.commit("host", map[string]string{".envrc": b.String()})
	before := x.tip()
	x.box("main", map[string]string{"README.md": "box\n"})
	a := &auditRec{}
	got := x.run(newRec(t), a)
	wantMergeReason(t, got, MergeFailed)
	if !strings.Contains(got.Detail, "too many paths to check") {
		t.Fatalf("Detail = %q", got.Detail)
	}
	if len(a.calls) != 0 || x.tip() != before {
		t.Fatalf("audited or merged: %q", a.calls)
	}
}

// Plain words are looked up only where a tool runs them as files: the command
// word, an interpreter's script and a fetcher's tool. Subcommands, flag values
// and other arguments name nothing (#370 B-M1); a fetcher always refuses its
// manifest (#370 R1c). The shapes are the ones real configs use.
func TestMergeHomePlainWordShapes(t *testing.T) {
	const pw = `{"mcpServers":{"pw":{"command":"npx","args":["-y","@playwright/mcp@latest","--headless","--browser","chrome"]}}}`
	cases := []struct {
		name      string
		host      map[string]string // committed on the host after the common files
		home      map[string]string // under the user's home
		gitConfig string            // appended to .git/config
		change    string
		guarded   bool
	}{
		{"user npx playwright@latest: package.json refused (#370 R1c)", nil, map[string]string{".claude.json": pw}, "", "package.json", true},
		{"user npx playwright: a flag value merges", nil, map[string]string{".claude.json": pw}, "", "chrome.cfg", false},
		{"mcp uvx @latest: pyproject.toml refused (#370 R1c)", map[string]string{".mcp.json": mcpCmd("uvx", "awslabs.x@latest")}, nil, "", "pyproject.toml", true},
		{"mcp docker run: .dockerignore merges", map[string]string{".mcp.json": mcpCmd("docker", "run", "-i", "--rm", "img")}, nil, "", ".dockerignore", false},
		{"mcp docker run: run.sh merges", map[string]string{".mcp.json": mcpCmd("docker", "run", "-i", "--rm", "img")}, nil, "", "run.sh", false},
		{"git credential helper gh: auth/ merges", nil, nil, "[credential]\n\thelper = !/usr/bin/gh auth git-credential\n", "auth/x", false},
		{"husky npm test: test/ merges", map[string]string{".husky/pre-commit": "npm test\n"}, nil, "", "test/a_test.js", false},
		{"husky npm test: package.json stays refused", map[string]string{".husky/pre-commit": "npm test\n"}, nil, "", "package.json", true},
		{"hook go test: test/ merges", map[string]string{".claude/settings.json": hookSettings("go test ./...")}, nil, "", "test/a_test.js", false},
		{"hook go test: go.mod stays refused", map[string]string{".claude/settings.json": hookSettings("go test ./...")}, nil, "", "go.mod", true},
		// Still refused.
		{"mcp node server: server.js", map[string]string{".mcp.json": mcpCmd("node", "server"), "server.js": "h\n"}, nil, "", "server.js", true},
		{"hook npx eslint: eslint.config.js", map[string]string{".claude/settings.json": hookSettings("npx eslint --fix"), "eslint.config.js": "h\n"}, nil, "", "eslint.config.js", true},
		{"mcp bash -c node: srvz.js", map[string]string{".mcp.json": mcpCmd("bash", "-c", "node srvz"), "srvz.js": "h\n"}, nil, "", "srvz.js", true},
		{"hook uv run pytest: pytest.ini", map[string]string{".claude/settings.json": hookSettings("uv run pytest -q"), "pytest.ini": "h\n"}, nil, "", "pytest.ini", true},
		{"hook npx -y yarn: package.json", map[string]string{".claude/settings.json": hookSettings("npx -y yarn install")}, nil, "", "package.json", true},
		{"mcp uvx poetry: pyproject.toml", map[string]string{".mcp.json": mcpCmd("uvx", "poetry", "run", "pytest")}, nil, "", "pyproject.toml", true},
		// A fetcher always refuses its manifest, whatever the spec (#370 R1c).
		{"hook npx -y prettier: package.json", map[string]string{".claude/settings.json": hookSettings("npx -y prettier --write .")}, nil, "", "package.json", true},
		{"husky npx --yes lint-staged: package.json", map[string]string{".husky/pre-commit": "npx --yes lint-staged\n"}, nil, "", "package.json", true},
		{"hook npx @biomejs/biome: package.json", map[string]string{".claude/settings.json": hookSettings("npx @biomejs/biome check .")}, nil, "", "package.json", true},
		{"hook npx -p eslint: package.json", map[string]string{".claude/settings.json": hookSettings("npx -p eslint eslint .")}, nil, "", "package.json", true},
		{"hook uvx mypy: pyproject.toml", map[string]string{".claude/settings.json": hookSettings("uvx mypy .")}, nil, "", "pyproject.toml", true},
		{"hook pipx run mypy: pyproject.toml", map[string]string{".claude/settings.json": hookSettings("pipx run mypy .")}, nil, "", "pyproject.toml", true},
		{"hook pnpm dlx tool: package.json", map[string]string{".claude/settings.json": hookSettings("pnpm dlx some-tool")}, nil, "", "package.json", true},
		{"hook npx -p versioned: package.json", map[string]string{".claude/settings.json": hookSettings("npx -p cowsay@1 cowsay hi")}, nil, "", "package.json", true},
		{"hook npx prettier@3: package.json", map[string]string{".claude/settings.json": hookSettings("npx -y prettier@3 --write .")}, nil, "", "package.json", true},
		{"hook pnpm dlx versioned: package.json", map[string]string{".claude/settings.json": hookSettings("pnpm dlx create-x@1")}, nil, "", "package.json", true},
		{"hook cross-env node server: server.js", map[string]string{".claude/settings.json": hookSettings("cross-env NODE_ENV=test node server"), "server.js": "h\n"}, nil, "", "server.js", true},
		{"hook timeout node server: server.js", map[string]string{".claude/settings.json": hookSettings("timeout 60 node server"), "server.js": "h\n"}, nil, "", "server.js", true},
		{"hook $PY script: srvp.py", map[string]string{".claude/settings.json": hookSettings("$PY srvp"), "srvp.py": "h\n"}, nil, "", "srvp.py", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noSystemConfig(t)
			home := t.TempDir()
			x := newMergeFx(t, "main")
			noManagedSettings(t)
			x.commit("common", map[string]string{"package.json": "{}\n", "pyproject.toml": "h\n", ".dockerignore": "h\n",
				"run.sh": "h\n", "chrome.cfg": "h\n", "auth/x": "h\n", "test/a_test.js": "h\n", "go.mod": "module x\n", "go.sum": "h\n"})
			if c.host != nil {
				x.commit("host", c.host)
			}
			for rel, content := range c.home {
				p := filepath.Join(home, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if c.gitConfig != "" {
				f, err := os.OpenFile(filepath.Join(x.of, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.WriteString(c.gitConfig); err != nil {
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			before := x.tip()
			x.box("main", map[string]string{c.change: "box\n"})
			got := x.runHome(home)
			if !c.guarded {
				wantMerged(t, got)
				return
			}
			wantMergeReason(t, got, MergeGuarded)
			if !slices.Contains(got.Paths, c.change) {
				t.Fatalf("Paths = %q, want %q", got.Paths, c.change)
			}
			if x.tip() != before {
				t.Fatal("main moved")
			}
		})
	}
}

// The matcher answers for a path that is not there yet with the rules plainWord
// lists the host's entries by.
func TestPlainMatcher(t *testing.T) {
	var pm plainMatcher
	pm.add(".", "eslint", true)
	pm.add(".", "server", false)
	pm.add("web", "jest", true)
	pm.addGlob(".", "*.mk")
	pm.addGlob("mk", "rules-?.MK")
	for p, want := range map[string]bool{
		"eslint.config.js": true, ".eslintrc.cjs": true, "server.js": true, "server/index.js": true,
		"server.config.js": false, "web/jest.config.ts": true, "Web/JEST.CONFIG.TS": true,
		"jest.config.ts": false, "src/eslint.config.js": false, "myeslint.js": false, "web": false,
		"b.mk": true, "B.MK": true, "x.mk/y": true, "b.mkx": false, "src/b.mk": false,
		"mk/rules-a.mk": true, "MK/Rules-B.Mk": true, "mk/rules-ab.mk": false, "mk/other.mk": false, "rules-a.inc": false,
	} {
		if got := pm.denied(p); got != want {
			t.Errorf("denied(%q) = %v, want %v", p, got, want)
		}
	}
}

// A runner call reads the package script or recipe it runs, for the files they
// run (#370).
func TestMergeHomeRunnerScriptsRead(t *testing.T) {
	lint := `{"scripts":{"lint":"npm run lint:js && npm run lint:css","lint:js":"eslint . && node tools/check.js","lint:css":"true"}}`
	cases := []struct {
		name    string
		host    map[string]string
		change  string
		guarded bool
	}{
		{"npm run reads the script", map[string]string{".claude/settings.json": hookSettings("npm run lint"), "package.json": `{"scripts":{"lint":"node scripts/lint.mjs"}}`}, "scripts/lint.mjs", true},
		{"pre script", map[string]string{".claude/settings.json": hookSettings("npm run lint"), "package.json": `{"scripts":{"prelint":"bash pre/p.sh","lint":"true"}}`}, "pre/p.sh", true},
		{"script calls another script", map[string]string{".claude/settings.json": hookSettings("npm run lint"), "package.json": lint}, "tools/check.js", true},
		{"script names with a colon", map[string]string{".claude/settings.json": hookSettings("npm run lint:js"), "package.json": lint}, "tools/check.js", true},
		{"a script's tool config, added by the box", map[string]string{".claude/settings.json": hookSettings("npm run lint:js"), "package.json": lint}, "eslint.config.js", true},
		{"a script that is not run merges", map[string]string{".claude/settings.json": hookSettings("npm run lint"), "package.json": `{"scripts":{"lint":"true","build":"node bld/build.js"}}`}, "bld/build.js", false},
		{"undetermined name reads every script", map[string]string{".claude/settings.json": hookSettings(`npm run \"$TASK\"`), "package.json": `{"scripts":{"lint":"true","build":"node bld/build.js"}}`}, "bld/build.js", true},
		{"husky npm test reads test", map[string]string{".husky/pre-commit": "npm test\n", "package.json": `{"scripts":{"test":"vitest run"}}`}, "vitest.config.ts", true},
		{"script loop ends", map[string]string{".claude/settings.json": hookSettings("npm run a"), "package.json": `{"scripts":{"a":"npm run b","b":"npm run a && node lp/x.js"}}`}, "lp/x.js", true},
		{"malformed package.json", map[string]string{".claude/settings.json": hookSettings("npm run lint"), "package.json": `{"scripts":`}, "src/x.js", false},
		{"malformed package.json, the manifest stays guarded", map[string]string{".claude/settings.json": hookSettings("npm run lint"), "package.json": `{"scripts":`}, "package.json", true},
		{"make recipe", map[string]string{".claude/settings.json": hookSettings("make fmt"), "Makefile": "fmt:\n\t@./tools/fmt.sh\n"}, "tools/fmt.sh", true},
		{"make include", map[string]string{".claude/settings.json": hookSettings("make fmt"), "Makefile": "include mk/rules.mk\n"}, "mk/rules.mk", true},
		{"make shell call", map[string]string{".claude/settings.json": hookSettings("make fmt"), "Makefile": "V := $(shell ./tools/v.sh)\n"}, "tools/v.sh", true},
		{"lower-case makefile", map[string]string{".claude/settings.json": hookSettings("make x"), "makefile": "x:\n\t./mk2/x.sh\n"}, "mk2/x.sh", true},
		{"just recipe", map[string]string{".claude/settings.json": hookSettings("just fmt"), "justfile": "fmt:\n  ./j/fmt.sh\n"}, "j/fmt.sh", true},
		{"task cmds", map[string]string{".claude/settings.json": hookSettings("task lint"), "Taskfile.yml": "version: '3'\ntasks:\n  lint:\n    cmds:\n      - ./t/x.sh\n"}, "t/x.sh", true},
		{"Taskfile.dist.yml", map[string]string{".claude/settings.json": hookSettings("task lint"), "Taskfile.dist.yml": "version: '3'\ntasks:\n  lint:\n    cmds:\n      - ./t/x.sh\n"}, "t/x.sh", true},
		{"make include is read", map[string]string{".claude/settings.json": hookSettings("make fmt"), "Makefile": "include mk/rules.mk\n", "mk/rules.mk": "fmt:\n\t./inc1/x.sh\n"}, "inc1/x.sh", true},
		{"make -f names a file", map[string]string{".claude/settings.json": hookSettings("make -f other.mk fmt"), "other.mk": "fmt:\n\t./inc2/x.sh\n"}, "inc2/x.sh", true},
		{"make -f file itself is guarded", map[string]string{".claude/settings.json": hookSettings("make -f other.mk fmt"), "other.mk": "fmt:\n"}, "other.mk", true},
		{"just import is read", map[string]string{".claude/settings.json": hookSettings("just fmt"), "justfile": "import 'j/more.just'\n", "j/more.just": "fmt:\n  ./inc3/x.sh\n"}, "inc3/x.sh", true},
		{"just mod is read", map[string]string{".claude/settings.json": hookSettings("just fmt"), "justfile": "mod sub\n", "sub.just": "fmt:\n  ./inc4/x.sh\n"}, "inc4/x.sh", true},
		{"just --justfile", map[string]string{".claude/settings.json": hookSettings("just --justfile ops.just fmt"), "ops.just": "fmt:\n  ./inc5/x.sh\n"}, "inc5/x.sh", true},
		{"task -t", map[string]string{".claude/settings.json": hookSettings("task -t ops.yml lint"), "ops.yml": "version: '3'\ntasks:\n  lint:\n    cmds:\n      - ./inc6/x.sh\n"}, "inc6/x.sh", true},
		{"taskfile includes, short form", map[string]string{".claude/settings.json": hookSettings("task lint"), "Taskfile.yml": "version: '3'\nincludes:\n  sub: ./sub/Taskfile.yml\n", "sub/Taskfile.yml": "version: '3'\ntasks:\n  lint:\n    cmds:\n      - ./inc7/x.sh\n"}, "inc7/x.sh", true},
		{"taskfile includes, taskfile key and a folder", map[string]string{".claude/settings.json": hookSettings("task lint"), "Taskfile.yml": "version: '3'\nincludes:\n  sub:\n    taskfile: ./sub\n    dir: ./sub\n", "sub/Taskfile.yml": "tasks:\n  lint:\n    cmds:\n      - ./inc8/x.sh\n"}, "inc8/x.sh", true},
		{"include loop ends", map[string]string{".claude/settings.json": hookSettings("make a"), "Makefile": "include b.mk\n", "b.mk": "include Makefile\n./inc9/x.sh\n"}, "inc9/x.sh", true},
		{"an include nobody reaches merges", map[string]string{".claude/settings.json": hookSettings("true"), "Makefile": "include mk/rules.mk\n", "mk/rules.mk": "fmt:\n\t./inc10/x.sh\n"}, "inc10/x.sh", false},
		{"Taskfile.dist.yml is a manifest", map[string]string{".claude/settings.json": hookSettings("task lint"), "Taskfile.dist.yml": "version: '3'\n"}, "Taskfile.dist.yml", true},
		{"Taskfile.dist.yaml is a manifest", map[string]string{".claude/settings.json": hookSettings("task lint"), "Taskfile.yml": "version: '3'\n"}, "Taskfile.dist.yaml", true},
		{"package.json with a BOM", map[string]string{".claude/settings.json": hookSettings("npm run lint"), "package.json": "\xef\xbb\xbf" + `{"scripts":{"lint":"node bomx/l.js"}}`}, "bomx/l.js", true},
		{"just import?", map[string]string{".claude/settings.json": hookSettings("just fmt"), "justfile": "import? 'opt/x.just'\n", "opt/x.just": "fmt:\n  ./inc11/x.sh\n"}, "inc11/x.sh", true},
		{"just mod?", map[string]string{".claude/settings.json": hookSettings("just fmt"), "justfile": "mod? foo\n", "foo.just": "fmt:\n  ./inc12/x.sh\n"}, "inc12/x.sh", true},
		{"just mod folder .justfile", map[string]string{".claude/settings.json": hookSettings("just fmt"), "justfile": "mod foo\n", "foo/.justfile": "fmt:\n  ./inc13/x.sh\n"}, "inc13/x.sh", true},
		{"just mod folder Justfile, any case", map[string]string{".claude/settings.json": hookSettings("just fmt"), "justfile": "mod foo\n", "foo/Justfile": "fmt:\n  ./inc14/x.sh\n"}, "inc14/x.sh", true},
		{"just mod NAME PATH reads PATH", map[string]string{".claude/settings.json": hookSettings("just fmt"), "justfile": "mod foo 'bar/m.just'\n", "bar/m.just": "fmt:\n  ./inc15/x.sh\n"}, "inc15/x.sh", true},
		{"just mod NAME PATH does not read NAME", map[string]string{".claude/settings.json": hookSettings("just fmt"), "justfile": "mod foo 'bar/m.just'\n", "foo.just": "fmt:\n  ./inc16/x.sh\n"}, "inc16/x.sh", false},
		{"npm publish runs prepublishOnly", map[string]string{".claude/settings.json": hookSettings("npm publish"), "package.json": `{"scripts":{"prepublishOnly":"node pub/x.js"}}`}, "pub/x.js", true},
		{"npm pack runs prepack", map[string]string{".claude/settings.json": hookSettings("npm pack"), "package.json": `{"scripts":{"prepack":"node pk/x.js"}}`}, "pk/x.js", true},
		{"npm version runs preversion", map[string]string{".claude/settings.json": hookSettings("npm version patch"), "package.json": `{"scripts":{"preversion":"node ver/x.js"}}`}, "ver/x.js", true},
		{"npm rebuild runs install", map[string]string{".claude/settings.json": hookSettings("npm rebuild"), "package.json": `{"scripts":{"install":"node rb/x.js"}}`}, "rb/x.js", true},
		{"npm it runs test", map[string]string{".claude/settings.json": hookSettings("npm it"), "package.json": `{"scripts":{"test":"node it/x.js"}}`}, "it/x.js", true},
		{"node --run runs the script", map[string]string{".claude/settings.json": hookSettings("node --run lint"), "package.json": `{"scripts":{"lint":"node nr/x.js"}}`}, "nr/x.js", true},
		{"node --run runs no pre script", map[string]string{".claude/settings.json": hookSettings("node --run lint"), "package.json": `{"scripts":{"prelint":"node nr2/x.js","lint":"true"}}`}, "nr2/x.js", false},
		{"include glob reads every match", map[string]string{".claude/settings.json": hookSettings("make fmt"), "Makefile": "include mk/*.mk\n", "mk/a.mk": "fmt:\n\t./tools/a.sh\n", "mk/notes.txt": "x\n"}, "tools/a.sh", true},
		{"include glob, upper case file name", map[string]string{".claude/settings.json": hookSettings("make fmt"), "Makefile": "include mk/*.mk\n", "mk/A.MK": "fmt:\n\t./tools/b.sh\n"}, "tools/b.sh", true},
		{"include glob with ?", map[string]string{".claude/settings.json": hookSettings("make fmt"), "Makefile": "include mk/?.mk\n", "mk/a.mk": "fmt:\n\t./tools/c.sh\n"}, "tools/c.sh", true},
		{"make -fFILE", map[string]string{".claude/settings.json": hookSettings("make -fother.mk fmt"), "other.mk": "fmt:\n\t./of1/x.sh\n"}, "of1/x.sh", true},
		{"make -sf FILE", map[string]string{".claude/settings.json": hookSettings("make -sf other.mk fmt"), "other.mk": "fmt:\n\t./of2/x.sh\n"}, "of2/x.sh", true},
		{"make -sfFILE", map[string]string{".claude/settings.json": hookSettings("make -sfother.mk fmt"), "other.mk": "fmt:\n\t./of3/x.sh\n"}, "of3/x.sh", true},
		{"make --file=FILE", map[string]string{".claude/settings.json": hookSettings("make --file=other.mk fmt"), "other.mk": "fmt:\n\t./of4/x.sh\n"}, "of4/x.sh", true},
		{"make --makefile FILE", map[string]string{".claude/settings.json": hookSettings("make --makefile other.mk fmt"), "other.mk": "fmt:\n\t./of5/x.sh\n"}, "of5/x.sh", true},
		{"just -fFILE", map[string]string{".claude/settings.json": hookSettings("just -fx.just fmt"), "x.just": "fmt:\n  ./of6/x.sh\n"}, "of6/x.sh", true},
		{"just --justfile=FILE", map[string]string{".claude/settings.json": hookSettings("just --justfile=x.just fmt"), "x.just": "fmt:\n  ./of7/x.sh\n"}, "of7/x.sh", true},
		{"task -tFILE", map[string]string{".claude/settings.json": hookSettings("task -tx.yml lint"), "x.yml": "tasks:\n  lint:\n    cmds:\n      - ./of8/x.sh\n"}, "of8/x.sh", true},
		{"task --taskfile=FILE", map[string]string{".claude/settings.json": hookSettings("task --taskfile=x.yml lint"), "x.yml": "tasks:\n  lint:\n    cmds:\n      - ./of9/x.sh\n"}, "of9/x.sh", true},
		{"make -C is not a file option", map[string]string{".claude/settings.json": hookSettings("make -Cfoo fmt"), "oo": "x\n"}, "unrelated/x.sh", false},
		{"include glob refuses a match the box adds", map[string]string{".claude/settings.json": hookSettings("make fmt"), "Makefile": "include *.mk\n", "a.mk": "fmt:\n\ttrue\n"}, "b.mk", true},
		{"include glob refuses an added match, any case", map[string]string{".claude/settings.json": hookSettings("make fmt"), "Makefile": "include *.mk\n"}, "NEW.MK", true},
		{"include glob with ? refuses an added match", map[string]string{".claude/settings.json": hookSettings("make fmt"), "Makefile": "-include rules-?.mk\n"}, "rules-z.mk", true},
		{"included file's glob refuses an added match at the top", map[string]string{".claude/settings.json": hookSettings("make fmt"), "Makefile": "include sub/a.mk\n", "sub/a.mk": "include *.inc\n"}, "z.inc", true},
		{"just import glob refuses an added match", map[string]string{".claude/settings.json": hookSettings("just fmt"), "justfile": "import 'j*.just'\n"}, "jx.just", true},
		{"include glob: a path it does not match merges", map[string]string{".claude/settings.json": hookSettings("make fmt"), "Makefile": "include *.mk\n"}, "notes.txt", false},
		{"make -f\"FILE\"", map[string]string{".claude/settings.json": hookSettings(`make -f\"q1.mk\" fmt`), "q1.mk": "fmt:\n\t./oq1/x.sh\n"}, "oq1/x.sh", true},
		{"make '-f' FILE", map[string]string{".claude/settings.json": hookSettings(`make '-f' q2.mk fmt`), "q2.mk": "fmt:\n\t./oq2/x.sh\n"}, "oq2/x.sh", true},
		{"make \"--file=FILE\"", map[string]string{".claude/settings.json": hookSettings(`make \"--file=q3.mk\" fmt`), "q3.mk": "fmt:\n\t./oq3/x.sh\n"}, "oq3/x.sh", true},
		{"make -\"f\"FILE", map[string]string{".claude/settings.json": hookSettings(`make -\"f\"q4.mk fmt`), "q4.mk": "fmt:\n\t./oq4/x.sh\n"}, "oq4/x.sh", true},
		{"make -f \"a file\"", map[string]string{".claude/settings.json": hookSettings(`make -f \"my q5.mk\" fmt`), "my q5.mk": "fmt:\n\t./oq5/x.sh\n"}, "oq5/x.sh", true},
		{"make -f\\FILE", map[string]string{".claude/settings.json": hookSettings(`make -f\\q6.mk fmt`), "q6.mk": "fmt:\n\t./oq6/x.sh\n"}, "oq6/x.sh", true},
		{"bash -c make -f", map[string]string{".claude/settings.json": hookSettings(`bash -c \"make -f q7.mk fmt\"`), "q7.mk": "fmt:\n\t./oq7/x.sh\n"}, "oq7/x.sh", true},
		{"make --mak=FILE", map[string]string{".claude/settings.json": hookSettings("make --mak=a1.mk fmt"), "a1.mk": "fmt:\n\t./ab1/x.sh\n"}, "ab1/x.sh", true},
		{"make --fi FILE", map[string]string{".claude/settings.json": hookSettings("make --fi a2.mk fmt"), "a2.mk": "fmt:\n\t./ab2/x.sh\n"}, "ab2/x.sh", true},
		{"just --just=FILE", map[string]string{".claude/settings.json": hookSettings("just --just=a3.just fmt"), "a3.just": "fmt:\n  ./ab3/x.sh\n"}, "ab3/x.sh", true},
		{"task --task=FILE", map[string]string{".claude/settings.json": hookSettings("task --task=a4.yml lint"), "a4.yml": "tasks:\n  lint:\n    cmds:\n      - ./ab4/x.sh\n"}, "ab4/x.sh", true},
		{"bash -c make && echo reads the recipe", map[string]string{".claude/settings.json": hookSettings(`bash -c \"make fmt && echo ok\"`), "Makefile": "fmt:\n\t./bc/x.sh\n"}, "bc/x.sh", true},
		{"bash -c make && echo, an unrelated file merges", map[string]string{".claude/settings.json": hookSettings(`bash -c \"make fmt && echo ok\"`), "Makefile": "fmt:\n\t./bc/x.sh\n"}, "unrelated/y.sh", false},
		{"taskfile include with nested vars holding a template", map[string]string{".claude/settings.json": hookSettings("task lint"), "Taskfile.yml": "version: '3'\nincludes:\n  sub:\n    taskfile: ./sub\n    vars:\n      X: '{{.Y}}'\n", "sub/Taskfile.yml": "tasks:\n  lint:\n    cmds:\n      - ./tv/x.sh\n"}, "tv/x.sh", true},
		{"bash -c: a second make in the word reads its -f file", map[string]string{".claude/settings.json": hookSettings(`bash -c \"make fmt; make -f x1.mk lint\"`), "x1.mk": "lint:\n\t./xm1/x.sh\n"}, "xm1/x.sh", true},
		{"bash -c: a second make after && reads its -f file", map[string]string{".claude/settings.json": hookSettings(`bash -c \"make fmt && make -f x2.mk lint\"`), "x2.mk": "lint:\n\t./xm2/x.sh\n"}, "xm2/x.sh", true},
		{"make 2>&1 -f FILE", map[string]string{".claude/settings.json": hookSettings("make 2>&1 -f r1.mk lint"), "r1.mk": "lint:\n\t./rd1/x.sh\n"}, "rd1/x.sh", true},
		{"a recipe file nobody calls merges", map[string]string{".claude/settings.json": hookSettings("true"), "Makefile": "fmt:\n\t./tools/fmt.sh\n"}, "tools/fmt.sh", false},
		// A make rule's prerequisites are targets, not commands (#370 M1).
		{"make .PHONY prerequisites: test/ merges", map[string]string{".claude/settings.json": hookSettings("make lint"), "test/a_test.go": "h\n", "build.sh": "h\n", "Makefile": ".PHONY: test lint\nlint:\n\tgolangci-lint run\n"}, "test/a_test.go", false},
		{"make rule prerequisites: build.sh merges", map[string]string{".claude/settings.json": hookSettings("make lint"), "test/a_test.go": "h\n", "build.sh": "h\n", "Makefile": "all: build test\nlint: build\n\tgolangci-lint run\n"}, "build.sh", false},
		{"make prerequisites continued: build.sh merges", map[string]string{".claude/settings.json": hookSettings("make lint"), "test/a_test.go": "h\n", "build.sh": "h\n", "Makefile": "lint: fmt \\\n  build test\n\tgolangci-lint run\n"}, "build.sh", false},
		{"make recipe line: refused", map[string]string{".claude/settings.json": hookSettings("make lint"), "test/a_test.go": "h\n", "build.sh": "h\n", "Makefile": ".PHONY: lint\nlint:\n\t./tools/x.sh\n"}, "tools/x.sh", true},
		{"make recipe after ; on a rule line: refused", map[string]string{".claude/settings.json": hookSettings("make lint"), "test/a_test.go": "h\n", "build.sh": "h\n", "Makefile": "lint: deps ; ./tools/y.sh\n"}, "tools/y.sh", true},
		{"make recipe line with a colon: refused", map[string]string{".claude/settings.json": hookSettings("make lint"), "test/a_test.go": "h\n", "build.sh": "h\n", "Makefile": "lint:\n\t./tools/z.sh --opt a: b\n"}, "tools/z.sh", true},
		{"make recipe continued with a colon: refused", map[string]string{".claude/settings.json": hookSettings("make lint"), "test/a_test.go": "h\n", "build.sh": "h\n", "Makefile": "lint:\n\tgolangci-lint run \\\n   ./tools/w.sh: x\n"}, "tools/w.sh", true},
		{"make prerequisite with a function call: refused", map[string]string{".claude/settings.json": hookSettings("make lint"), "test/a_test.go": "h\n", "build.sh": "h\n", "Makefile": "lint: $(shell ./tools/v.sh)\n\ttrue\n"}, "tools/v.sh", true},
		{"make target-specific shell variable: refused", map[string]string{".claude/settings.json": hookSettings("make lint"), "test/a_test.go": "h\n", "build.sh": "h\n", "Makefile": "lint: V := $(shell ./tools/u.sh)\nlint:\n\ttrue\n"}, "tools/u.sh", true},
		{"make define block: a line in it is read whole", map[string]string{".claude/settings.json": hookSettings("make lint"), "Makefile": "define RUN\n./tools/d.sh: arg\nendef\nlint:\n\t$(RUN)\n"}, "tools/d.sh", true},
		{"make after a define block: prerequisites skipped", map[string]string{".claude/settings.json": hookSettings("make lint"), "build.sh": "h\n", "Makefile": "define RUN\ntrue\nendef\nlint: build\n\t$(RUN)\n"}, "build.sh", false},
		{"make path prerequisite read by $<: refused", map[string]string{".claude/settings.json": hookSettings("make lint"), "foo": "h\n", "test/a_test.go": "h\n", "Makefile": "lint: tools/x.sh\n\tbash $<\n"}, "tools/x.sh", true},
		{"make path prerequisite: always refused", map[string]string{".claude/settings.json": hookSettings("make lint"), "foo": "h\n", "test/a_test.go": "h\n", "Makefile": "lint: tools/p.sh\n\ttrue\n"}, "tools/p.sh", true},
		{"make leading-dot prerequisite: always refused", map[string]string{".claude/settings.json": hookSettings("make lint"), "foo": "h\n", "test/a_test.go": "h\n", "Makefile": "lint: .tools/q\n\ttrue\n"}, ".tools/q", true},
		{"make plain prerequisite run by ./$<: refused", map[string]string{".claude/settings.json": hookSettings("make lint"), "foo": "h\n", "test/a_test.go": "h\n", "Makefile": "x: foo\n\t./$<\n"}, "foo", true},
		{"make plain prerequisite, ${^} in the file: refused", map[string]string{".claude/settings.json": hookSettings("make lint"), "foo": "h\n", "test/a_test.go": "h\n", "Makefile": "lint: foo\n\techo ${^}\n"}, "foo", true},
		{"make plain prerequisite, $(<D) in the file: refused", map[string]string{".claude/settings.json": hookSettings("make lint"), "foo": "h\n", "test/a_test.go": "h\n", "Makefile": "lint: foo\n\tcd $(<D)\n"}, "foo", true},
		{"make plain prerequisite, $? in an include read later: refused", map[string]string{".claude/settings.json": hookSettings("make lint"), "foo": "h\n", "test/a_test.go": "h\n", "mk/a.mk": "x:\n\techo $?\n", "Makefile": "lint: foo\n\ttrue\ninclude mk/a.mk\n"}, "foo", true},
		{"make plain prerequisite, computed variable name: refused", map[string]string{".claude/settings.json": hookSettings("make lint"), "foo": "h\n", "test/a_test.go": "h\n", "Makefile": "$(R)PREFIX = >\nlint: foo\n\ttrue\n"}, "foo", true},
		{"make plain prerequisite, no automatic variable: foo merges", map[string]string{".claude/settings.json": hookSettings("make lint"), "foo": "h\n", "test/a_test.go": "h\n", "Makefile": "lint: foo\n\ttrue\n"}, "foo", false},
		{"make .RECIPEPREFIX: every line read", map[string]string{".claude/settings.json": hookSettings("make lint"), "test/a_test.go": "h\n", "build.sh": "h\n", "Makefile": ".RECIPEPREFIX = >\nlint:\n>build.sh x: y\n"}, "build.sh", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			old := mergeSystemGitConfig
			mergeSystemGitConfig = filepath.Join(t.TempDir(), "none")
			t.Cleanup(func() { mergeSystemGitConfig = old })
			x := newMergeFx(t, "main")
			x.commit("host", c.host)
			before := x.tip()
			x.box("main", map[string]string{c.change: "box\n"})
			a := &auditRec{}
			got := x.run(newRec(t), a)
			if !c.guarded {
				wantMerged(t, got)
				return
			}
			wantMergeReason(t, got, MergeGuarded)
			if !slices.Contains(got.Paths, c.change) || x.tip() != before || len(a.calls) != 0 {
				t.Fatalf("Paths = %q, main moved or audit written: %q", got.Paths, a.calls)
			}
		})
	}
}

// A call that runs the scripts of workspace packages cannot be followed: the
// merge fails with a fixed reason (#370).
func TestMergeHomeWorkspaceCallFails(t *testing.T) {
	for _, cmd := range []string{"npm run lint --workspaces", "npm run lint -w pkg", "npm run lint --workspace=pkg",
		"pnpm --filter web lint", "pnpm -r lint", "pnpm run -F web lint", "yarn workspace web lint", "yarn workspaces foreach run lint",
		"bun --filter '*' lint",
		// A workspace flag with exec or dlx: the program runs in each package's folder.
		"pnpm --filter web exec eslint", "pnpm -r exec tsc", "npm exec --workspaces -- eslint", "yarn workspaces foreach exec tsc",
		"npm run exec --workspaces", // exec is the script name here
		"yarn --cwd . workspace web lint", "pnpm recursive run lint", "pnpm m run lint", "pnpm multi run lint",
		// A workspace setting from the environment.
		"npm_config_workspace=pkg npm run lint", "NPM_CONFIG_WORKSPACES=true npm run lint", "npm_config_filter=web pnpm lint",
		"env npm_config_workspaces=true npm run lint", "export npm_config_workspaces=true; npm run lint",
		"npm_config_filter_prod=web pnpm lint", "npm_config_workspace_x=1 npm run lint", "pnpm --filter-prod web lint",
		// Any npm or pnpm setting from the environment (#370 R14b).
		"npm_config_registry=http://r.example npm ci", "pnpm_config_x=1 pnpm lint", "PNPM_CONFIG_FOO=1 pnpm lint"} {
		t.Run(cmd, func(t *testing.T) {
			old := mergeSystemGitConfig
			mergeSystemGitConfig = filepath.Join(t.TempDir(), "none")
			t.Cleanup(func() { mergeSystemGitConfig = old })
			x := newMergeFx(t, "main")
			x.commit("host", map[string]string{".claude/settings.json": hookSettings(cmd), "package.json": `{"scripts":{"lint":"true","exec":"true"}}`})
			before := x.tip()
			x.box("main", map[string]string{"src/x.js": "box\n"})
			a := &auditRec{}
			got := x.run(newRec(t), a)
			wantMergeReason(t, got, MergeFailed)
			if !strings.Contains(got.Detail, "cannot follow workspace package scripts") || x.tip() != before || len(a.calls) != 0 {
				t.Fatalf("Detail = %q, main moved or audit written", got.Detail)
			}
		})
	}
}

// A workspace setting in the host's .npmrc makes every npm and pnpm call a
// workspace call: the merge fails with the workspace reason (#370).
func TestMergeHomeNpmrcWorkspaceFails(t *testing.T) {
	cases := []struct {
		name, npmrc, cmd string
		fails            bool
	}{
		{"workspaces = true", "registry=https://r.example\nworkspaces = true\n", "npm run lint", true},
		{"workspace=pkg", "workspace=pkg\n", "npm test", true},
		{"workspace[] array form", "workspace[] = a\nworkspace[] = b\n", "npm run lint", true},
		{"filter for pnpm", "  filter = web\n", "pnpm lint", true},
		{"any case key", "Workspaces=true\n", "npm run lint", true},
		{"pnpm exec", "filter=web\n", "pnpm exec eslint", true},
		{"a folder's .npmrc", "", "cd sub && npm run lint", true},
		{"comment lines do not count", "; workspaces = true\n# filter = web\n", "npm run lint", false},
		{"filter-prod", "filter-prod=web\n", "pnpm lint", true},
		{"workspace prefix", "workspaces-update=false\n", "npm run lint", true},
		{"other keys do not count", "engine-strict=true\nshared-workspace-lockfile=false\n", "npm run lint", false},
		{"yarn does not read it", "workspaces=true\n", "yarn lint", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			old := mergeSystemGitConfig
			mergeSystemGitConfig = filepath.Join(t.TempDir(), "none")
			t.Cleanup(func() { mergeSystemGitConfig = old })
			x := newMergeFx(t, "main")
			host := map[string]string{".claude/settings.json": hookSettings(c.cmd), "package.json": `{"scripts":{"lint":"true"}}`}
			if c.npmrc != "" {
				host[".npmrc"] = c.npmrc
			} else {
				host["sub/.npmrc"] = "workspaces=true\n"
				host["sub/package.json"] = `{"scripts":{"lint":"true"}}`
			}
			x.commit("host", host)
			before := x.tip()
			x.box("main", map[string]string{"src/x.js": "box\n"})
			a := &auditRec{}
			got := x.run(newRec(t), a)
			if !c.fails {
				wantMerged(t, got)
				return
			}
			wantMergeReason(t, got, MergeFailed)
			if !strings.Contains(got.Detail, "cannot follow workspace package scripts") || x.tip() != before || len(a.calls) != 0 {
				t.Fatalf("Detail = %q, main moved or audit written", got.Detail)
			}
		})
	}
}

// A host pnpm-workspace.yaml is a protected name at any depth; by itself it
// does not make a pnpm call a workspace call (#370 R12).
func TestMergeHomePnpmWorkspaceYaml(t *testing.T) {
	for _, c := range []struct {
		change  string
		guarded bool
	}{{"src/x.js", false}, {"pnpm-workspace.yaml", true}, {"packages/a/pnpm-workspace.yml", true}} {
		t.Run(c.change, func(t *testing.T) {
			old := mergeSystemGitConfig
			mergeSystemGitConfig = filepath.Join(t.TempDir(), "none")
			t.Cleanup(func() { mergeSystemGitConfig = old })
			x := newMergeFx(t, "main")
			x.commit("host", map[string]string{".claude/settings.json": hookSettings("pnpm lint"), "package.json": `{"scripts":{"lint":"true"}}`,
				"pnpm-workspace.yaml": "packages:\n  - 'packages/*'\n"})
			x.box("main", map[string]string{c.change: "box\n"})
			got := x.run(newRec(t), &auditRec{})
			if !c.guarded {
				wantMerged(t, got)
				return
			}
			wantMergeReason(t, got, MergeGuarded)
		})
	}
}

// An absolute include is read as the repo path when it is inside the repo, and
// at its real path when it is outside (#370).
func TestMergeHomeRecipeAbsoluteInclude(t *testing.T) {
	cases := []struct {
		name   string
		host   map[string]string // /ABS/ is the repo top, /OUT/ a folder outside it
		out    map[string]string // files in /OUT/
		change string
		want   string // "" = merged
	}{
		{"absolute include inside the repo", map[string]string{"Makefile": "include /ABS/mkabs/a.mk\n", "mkabs/a.mk": "fmt:\n\t./zai/x.sh\n"}, nil, "zai/x.sh", MergeGuarded},
		{"absolute include outside the repo", map[string]string{"Makefile": "include /OUT/x.mk\n"}, map[string]string{"x.mk": "fmt:\n\t./za/x.sh\n"}, "za/x.sh", MergeGuarded},
		{"outside include that includes another", map[string]string{"Makefile": "include /OUT/x.mk\n"}, map[string]string{"x.mk": "include /OUT/y.mk\n", "y.mk": "fmt:\n\t./zb/x.sh\n"}, "zb/x.sh", MergeGuarded},
		{"make -f outside the repo", map[string]string{".claude/settings.json": hookSettings("make -f /OUT/x.mk fmt")}, map[string]string{"x.mk": "fmt:\n\t./zc/x.sh\n"}, "zc/x.sh", MergeGuarded},
		{"just import outside the repo", map[string]string{".claude/settings.json": hookSettings("just fmt"), "justfile": "import '/OUT/x.just'\n"}, map[string]string{"x.just": "fmt:\n  ./zd/x.sh\n"}, "zd/x.sh", MergeGuarded},
		{"just mod outside the repo", map[string]string{".claude/settings.json": hookSettings("just fmt"), "justfile": "mod foo '/OUT/m.just'\n"}, map[string]string{"m.just": "fmt:\n  ./ze/x.sh\n"}, "ze/x.sh", MergeGuarded},
		{"missing outside include merges", map[string]string{"Makefile": "-include /OUT/none.mk\n"}, nil, "src/x.js", ""},
		{"outside include that is a folder is skipped", map[string]string{"Makefile": "-include /OUT/d\n"}, map[string]string{"d/x": "x\n"}, "src/x.js", ""},
		{"outside include that is a FIFO", map[string]string{"Makefile": "include /OUT/fifo\n"}, map[string]string{"fifo": "FIFO"}, "src/x.js", MergeFailed},
		{"outside include over the size cap", map[string]string{"Makefile": "include /OUT/big.mk\n"}, map[string]string{"big.mk": strings.Repeat("# pad\n", mergeImportBytes/6+10)}, "src/x.js", MergeFailed},
		{"outside include that is a device", map[string]string{"Makefile": "include /dev/null\n"}, nil, "src/x.js", MergeFailed},
		{"outside include with a glob", map[string]string{"Makefile": "include /OUT/*.mk\n"}, map[string]string{"x.mk": "x\n"}, "src/x.js", MergeFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			old := mergeSystemGitConfig
			mergeSystemGitConfig = filepath.Join(t.TempDir(), "none")
			t.Cleanup(func() { mergeSystemGitConfig = old })
			out := t.TempDir()
			for rel, body := range c.out {
				p := filepath.Join(out, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if body == "FIFO" {
					if err := syscall.Mkfifo(p, 0o644); err != nil {
						t.Fatal(err)
					}
					continue
				}
				if err := os.WriteFile(p, []byte(strings.ReplaceAll(body, "/OUT/", out+"/")), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			x := newMergeFx(t, "main")
			host := map[string]string{}
			if _, ok := c.host[".claude/settings.json"]; !ok {
				host[".claude/settings.json"] = hookSettings("make fmt")
			}
			for k, v := range c.host {
				v = strings.ReplaceAll(v, "/ABS/", x.of+"/")
				host[k] = strings.ReplaceAll(v, "/OUT/", out+"/")
			}
			x.commit("host", host)
			before := x.tip()
			x.box("main", map[string]string{c.change: "box\n"})
			got := x.run(newRec(t), &auditRec{})
			if c.want == "" {
				wantMerged(t, got)
				return
			}
			wantMergeReason(t, got, c.want)
			if x.tip() != before {
				t.Fatal("main moved")
			}
		})
	}
}

// A recipe file or include the scan cannot name fails the merge (#370).
func TestMergeHomeRecipeUnknownFileFails(t *testing.T) {
	cases := []struct {
		name string
		host map[string]string
	}{
		{"include $(wildcard)", map[string]string{".claude/settings.json": hookSettings("make fmt"), "Makefile": "include $(wildcard mk/*.mk)\n"}},
		{"-include $(DEPS)", map[string]string{".claude/settings.json": hookSettings("make fmt"), "Makefile": "-include $(DEPS)\n"}},
		{"just import with a template", map[string]string{".claude/settings.json": hookSettings("just fmt"), "justfile": "import '{{x}}.just'\n"}},
		{"make -f $F", map[string]string{".claude/settings.json": hookSettings("make -f $F fmt")}},
		{"make -f with no value", map[string]string{".claude/settings.json": hookSettings("make -f")}},
		{"task -t with a variable", map[string]string{".claude/settings.json": hookSettings("task --taskfile=$T lint")}},
		{"glob in the folder", map[string]string{".claude/settings.json": hookSettings("make fmt"), "Makefile": "include */x.mk\n"}},
		{"just mod with a variable path", map[string]string{".claude/settings.json": hookSettings("just fmt"), "justfile": "mod foo '$m/mod.just'\n", "$m/mod.just": "fmt:\n  ./zj/x.sh\n"}},
		{"just mod with a glob path", map[string]string{".claude/settings.json": hookSettings("just fmt"), "justfile": "mod foo 'm*/mod.just'\n"}},
		{"just mod with a template path", map[string]string{".claude/settings.json": hookSettings("just fmt"), "justfile": "mod foo '{{d}}/m.just'\n"}},
		{"just mod with a parenthesis", map[string]string{".claude/settings.json": hookSettings("just fmt"), "justfile": "mod foo 'a(b).just'\n"}},
		{"just mod NAME with a variable", map[string]string{".claude/settings.json": hookSettings("just fmt"), "justfile": "mod $m\n"}},
		{"make -C quoted substitution", map[string]string{".claude/settings.json": hookSettings(`make -C \"$(pwd)\" -f ci.mk lint`), "ci.mk": "lint:\n\t./zt/x.sh\n"}},
		{"make quoted delimiter", map[string]string{".claude/settings.json": hookSettings(`make \"a;b\" -f ci.mk`), "ci.mk": "lint:\n\t./zt/x.sh\n"}},
		{"make trailing backslash", map[string]string{".claude/settings.json": hookSettings(`make fmt \\`)}},
		{"make -f value with a glob", map[string]string{".claude/settings.json": hookSettings("make -f 'c*.mk' fmt"), "ci.mk": "fmt:\n\t./zt/x.sh\n"}},
		{"taskfile include as a flow map", map[string]string{".claude/settings.json": hookSettings("task lint"), "Taskfile.yml": "version: '3'\nincludes:\n  sub: {taskfile: ./sub/Taskfile.yml}\n", "sub/Taskfile.yml": "tasks:\n  lint:\n    cmds:\n      - ./zf/x.sh\n"}},
		{"taskfile includes as a flow map", map[string]string{".claude/settings.json": hookSettings("task lint"), "Taskfile.yml": "version: '3'\nincludes: {sub: ./sub}\n", "sub/Taskfile.yml": "tasks:\n  lint:\n    cmds:\n      - ./zf/x.sh\n"}},
		{"make -f\\ FILE (a name with a leading space)", map[string]string{".claude/settings.json": hookSettings(`make -f\\ x.mk fmt`)}},
		{"unbalanced quote on an option", map[string]string{".claude/settings.json": hookSettings(`make '-f x.mk fmt`)}},
		{"unbalanced quote after the options", map[string]string{".claude/settings.json": hookSettings(`make fmt \"-f`)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			old := mergeSystemGitConfig
			mergeSystemGitConfig = filepath.Join(t.TempDir(), "none")
			t.Cleanup(func() { mergeSystemGitConfig = old })
			x := newMergeFx(t, "main")
			x.commit("host", c.host)
			before := x.tip()
			x.box("main", map[string]string{"src/x.js": "box\n"})
			a := &auditRec{}
			got := x.run(newRec(t), a)
			wantMergeReason(t, got, MergeFailed)
			if x.tip() != before || len(a.calls) != 0 {
				t.Fatalf("main moved or audit written")
			}
		})
	}
}

// An env value is read by its key (#370 A2-M6, R3): the keys that name a file or
// a command are parsed for it, any other key only for path-shaped words.
func TestConfigRefsEnvKeys(t *testing.T) {
	cases := []struct {
		name  string
		env   string
		files map[string]string
		want  string // "" none
		not   string
	}{
		{"a command is no command", `{"FOO":"npm run x"}`, map[string]string{"package.json": `{"scripts":{"x":"node tools/x.js"}}`}, "", "package.json"},
		{"a plain word is no look-up", `{"FOO":"server"}`, map[string]string{"server.js": "h\n"}, "", "server.js"},
		{"a path-shaped word", `{"FOO":"./cfg/x.js"}`, nil, "cfg/x.js", ""},
		{"a word with a script extension", `{"FOO":"x.sh"}`, nil, "x.sh", ""},
		{"PATH", `{"PATH":"./bin:$PATH"}`, nil, "bin", ""},
		{"LD_PRELOAD", `{"LD_PRELOAD":"./lib/a.so"}`, nil, "lib/a.so", ""},
		{"PYTHONPATH", `{"PYTHONPATH":"./py:/usr/lib"}`, nil, "py", ""},
		{"NODE_OPTIONS", `{"NODE_OPTIONS":"--require boot4"}`, nil, "boot4.js", ""},
		{"PERL5OPT", `{"PERL5OPT":"-Mfoo ./pl/x.pl"}`, nil, "pl/x.pl", ""},
		{"RUBYOPT", `{"RUBYOPT":"-r ./rb/x.rb"}`, nil, "rb/x.rb", ""},
		{"BASH_ENV a bare name", `{"BASH_ENV":"envfile"}`, nil, "envfile", ""},
		{"NODE_PATH", `{"NODE_PATH":"./np/lib"}`, nil, "np/lib", ""},
		{"GEM_HOME", `{"GEM_HOME":"./gems"}`, nil, "gems", ""},
		{"LD_LIBRARY_PATH", `{"LD_LIBRARY_PATH":"./ll:/usr/lib"}`, nil, "ll", ""},
		{"EDITOR is a command", `{"EDITOR":"npm run ed"}`, map[string]string{"package.json": `{"scripts":{"ed":"node tools/e.js"}}`}, "tools/e.js", ""},
		{"PAGER is a command", `{"PAGER":"./bin/pg --x"}`, nil, "bin/pg", ""},
		{"an unknown key stays path-shaped", `{"MY_KEY":"npm run ed"}`, map[string]string{"package.json": `{"scripts":{"ed":"node tools/e.js"}}`}, "", "tools/e.js"},
		{"GIT_SSH_COMMAND", `{"GIT_SSH_COMMAND":"ssh -F cfg/ssh"}`, nil, "cfg/ssh", ""},
		{"GIT_EDITOR runs a runner", `{"GIT_EDITOR":"npm run edit"}`, map[string]string{"package.json": `{"scripts":{"edit":"node tools/ed.js"}}`}, "tools/ed.js", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{".mcp.json": `{"mcpServers":{"s":{"command":"srv","env":` + c.env + `}}}`}
			for k, v := range c.files {
				files[k] = v
			}
			s := scanRefs(t, files)
			got := s.paths()
			if c.want != "" && !slices.Contains(got, c.want) {
				t.Errorf("paths = %q, want %q", got, c.want)
			}
			if c.not != "" && slices.Contains(got, c.not) {
				t.Errorf("paths = %q, must not have %q", got, c.not)
			}
		})
	}
}

// A spelling that came from an env value is never shown in the refused list,
// even when the link check cannot follow it.
func TestMergeHomeEnvRefNotEchoed(t *testing.T) {
	run := func(t *testing.T, mcp string, loop string) *MergeResult {
		t.Helper()
		x := newMergeFx(t, "main")
		x.commit("host", map[string]string{".mcp.json": mcp})
		x.linkUntracked(loop, "a")
		x.box("main", map[string]string{"b.txt": "b\n"})
		return x.run(newRec(t), &auditRec{})
	}
	t.Run("env value", func(t *testing.T) {
		got := run(t, `{"mcpServers":{"s":{"command":"srv","env":{"X":"./envloop/a"}}}}`, "envloop/a")
		wantMergeReason(t, got, MergeGuarded)
		if len(got.Paths) != 0 {
			t.Fatalf("Paths = %q, want none", got.Paths)
		}
		if !strings.Contains(got.Detail, "environment value") {
			t.Fatalf("Detail = %q", got.Detail)
		}
	})
	t.Run("args path control", func(t *testing.T) {
		got := run(t, `{"mcpServers":{"s":{"command":"srv","args":["./argloop/a"]}}}`, "argloop/a")
		wantMergeReason(t, got, MergeGuarded)
		if !slices.Equal(got.Paths, []string{"argloop/a"}) {
			t.Fatalf("Paths = %q, want [argloop/a]", got.Paths)
		}
	})
}

// A path an env value named never shows in the Detail text either, even when
// the link check fails on it with an error that names the path (#370 M3).
func TestMergeHomeEnvRefNotInDetail(t *testing.T) {
	long := strings.Repeat("s", 300) // an Lstat error: the name is too long
	x := newMergeFx(t, "main")
	x.commit("host", map[string]string{".mcp.json": `{"mcpServers":{"s":{"command":"srv","env":{"X":"./envdir/` + long + `"}}}}`,
		"envdir/keep": "h\n"})
	x.box("main", map[string]string{"b.txt": "b\n"})
	got := x.run(newRec(t), &auditRec{})
	wantMergeReason(t, got, MergeGuarded)
	if len(got.Paths) != 0 {
		t.Fatalf("Paths = %q, want none", got.Paths)
	}
	if strings.Contains(got.Detail, "envdir") || strings.Contains(got.Detail, long[:40]) {
		t.Fatalf("Detail = %q, names the env value", got.Detail)
	}
	if !strings.Contains(got.Detail, "environment value") {
		t.Fatalf("Detail = %q", got.Detail)
	}
}
