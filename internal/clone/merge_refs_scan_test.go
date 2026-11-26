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
		{"mcp npx fetches, package.json merges", map[string]string{".mcp.json": `{"mcpServers":{"s":{"command":"npx","args":["-y","some-server"]}}}`}, nil, "package.json", false},
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
		{"tool config, git keeps .gitignore free", map[string]string{".claude/settings.json": hookSettings("git diff --stat"), ".gitignore": "x\n"}, nil, ".gitignore", false},
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
		if _, err := scanRefsErr(t, files); !errors.Is(err, errScanCap) {
			t.Fatalf("err = %v, want errScanCap", err)
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
		s := scanRefs(t, map[string]string{".husky/pre-commit": "./early/a.sh\n" + pad + "./late/b.sh\n"})
		got := s.paths()
		if !slices.Contains(got, "early/a.sh") || slices.Contains(got, "late/b.sh") {
			t.Fatalf("paths = %q, want the early one only", got)
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
		if s, _ := scanConfigRefs(of, nil, ""); len(s.paths()) != 0 {
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
// and other arguments name nothing, and a fetched remote package adds no
// manifest (#370 B-M1). The shapes are the ones real configs use.
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
		{"user npx playwright: package.json merges", nil, map[string]string{".claude.json": pw}, "", "package.json", false},
		{"user npx playwright: a flag value merges", nil, map[string]string{".claude.json": pw}, "", "chrome.cfg", false},
		{"mcp uvx: pyproject.toml merges", map[string]string{".mcp.json": mcpCmd("uvx", "awslabs.x@latest")}, nil, "", "pyproject.toml", false},
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
		{"hook cross-env node server: server.js", map[string]string{".claude/settings.json": hookSettings("cross-env NODE_ENV=test node server"), "server.js": "h\n"}, nil, "", "server.js", true},
		{"hook timeout node server: server.js", map[string]string{".claude/settings.json": hookSettings("timeout 60 node server"), "server.js": "h\n"}, nil, "", "server.js", true},
		{"hook $PY script: srvp.py", map[string]string{".claude/settings.json": hookSettings("$PY srvp"), "srvp.py": "h\n"}, nil, "", "srvp.py", true},
		{"hook pnpm --filter exec eslint: eslint.config.js", map[string]string{".claude/settings.json": hookSettings("pnpm --filter web exec eslint ."), "eslint.config.js": "h\n"}, nil, "", "eslint.config.js", true},
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
	for p, want := range map[string]bool{
		"eslint.config.js": true, ".eslintrc.cjs": true, "server.js": true, "server/index.js": true,
		"server.config.js": false, "web/jest.config.ts": true, "Web/JEST.CONFIG.TS": true,
		"jest.config.ts": false, "src/eslint.config.js": false, "myeslint.js": false, "web": false,
	} {
		if got := pm.denied(p); got != want {
			t.Errorf("denied(%q) = %v, want %v", p, got, want)
		}
	}
}
