package clone

import (
	"slices"
	"strings"
	"testing"
)

func TestCommandPaths(t *testing.T) {
	cases := []struct {
		in      string
		paths   []string
		runners []string
	}{
		{"./scripts/clean.sh", []string{"./scripts/clean.sh"}, nil},
		{"bash scripts/format.sh --fix", []string{"scripts/format.sh"}, nil},
		{"node ./tools/mcp/server.js", []string{"./tools/mcp/server.js"}, []string{"node"}},
		{"python3 run.py", []string{"run.py"}, nil},
		{"!f() { ./hooks/a.sh \"$@\"; }; f", []string{"./hooks/a.sh"}, nil},
		{"cat | tools/tc.sh>out.txt", []string{"tools/tc.sh"}, nil},
		{"$CLAUDE_PROJECT_DIR/.claude/hooks/x", []string{"./.claude/hooks/x"}, nil},
		{"${CLAUDE_PROJECT_DIR}/hooks/x.sh", []string{"./hooks/x.sh"}, nil},
		{"\"$CLAUDE_PROJECT_DIR\"/bin/tool", []string{"./bin/tool"}, nil},
		{"tool --config=./conf/a.json --verbose", []string{"./conf/a.json"}, nil},
		{"CONFIG=./c.toml tool", []string{"./c.toml"}, nil},
		{"npm run lint && pnpm test", nil, []string{"npm", "pnpm"}},
		{"/usr/bin/make all", []string{"/usr/bin/make"}, []string{"make"}},
		{"curl https://example.com/a/b.sh", nil, nil},
		{"~/bin/x.sh $HOME/y.sh $FOO", nil, nil},
		{"-x --flag . .. plain words", nil, nil},
		{"/abs/tool.sh", []string{"/abs/tool.sh"}, nil},
		{".envrc.local", []string{".envrc.local"}, nil},
		{"!./bin/st.sh --x", []string{"./bin/st.sh"}, nil},
		{"!f(){./a/b.sh;}; f", []string{"./a/b.sh"}, nil},
		{"export PATH=./scripts:$PATH", []string{"./scripts"}, nil},
		{"A=/usr/bin:./bin:$X cmd", []string{"/usr/bin", "./bin"}, nil},
		{"$PWD/tools/x.sh", []string{"./tools/x.sh"}, nil},
		{"\"$PWD\"/tools/x.sh", []string{"./tools/x.sh"}, nil},
		{"${PWD}/tools/x.sh", []string{"./tools/x.sh"}, nil},
		{"$(pwd)/tools/x.sh", []string{"./tools/x.sh"}, nil},
		{"{{config_root}}/scripts/e.sh", []string{"./scripts/e.sh"}, nil},
		{"{{ config_root }}/scripts/e.sh", []string{"./scripts/e.sh"}, nil},
		{"$MISE_PROJECT_ROOT/scripts/e.sh", []string{"./scripts/e.sh"}, nil},
		{"", nil, nil},
	}
	for _, c := range cases {
		paths, runners := commandPaths(c.in)
		if !slices.Equal(paths, c.paths) || !slices.Equal(runners, c.runners) {
			t.Errorf("commandPaths(%q) = %q, %q; want %q, %q", c.in, paths, runners, c.paths, c.runners)
		}
	}
}

func TestRepoDenyPaths(t *testing.T) {
	of := t.TempDir()
	got := repoDenyPaths(of, []string{
		"./scripts/clean.sh", // file and folder
		"top.js",             // top-level file: itself only
		"a/b/c.sh",           // folder is a/b only, not a
		of + "/abs/x.sh",     // absolute inside
		"/elsewhere/y.sh",    // absolute outside: skipped
		"../up/z.sh",         // escapes: skipped
		".", "..", "",        // never the top
		"tools/",           // a directory: itself
		"scripts/clean.sh", // duplicate
	})
	want := []string{"scripts/clean.sh", "scripts", "top.js", "a/b/c.sh", "a/b", "abs/x.sh", "abs", "tools"}
	if !slices.Equal(got, want) {
		t.Fatalf("repoDenyPaths = %q, want %q", got, want)
	}
}

func TestParseCommand(t *testing.T) {
	of := t.TempDir()
	cases := []struct {
		in      string
		paths   []string
		dirs    []string
		plain   []string // "word@base,base", a trailing "*" when the tool-config rule applies
		runners []string
	}{
		{"python -m a.b.c", []string{"a", "a.py"}, nil, []string{"python@.*"}, nil},
		{"python3 -m pkg", []string{"pkg", "pkg.py"}, nil, []string{"python3@.*"}, nil},
		{"uv run -m svc.app", []string{"svc", "svc.py"}, nil, []string{"uv@.*"}, []string{"uv"}},
		{"python -mfoo.bar", []string{"foo", "foo.py"}, nil, []string{"python@.*"}, nil},
		{"uv run -mfoo", []string{"foo", "foo.py"}, nil, []string{"uv@.*"}, []string{"uv"}},
		{"git commit -mmsg", nil, nil, []string{"git@.*"}, nil},
		{"python --mfoo", nil, nil, []string{"python@.*"}, nil},
		{"git commit -m msg", nil, nil, []string{"git@.*"}, nil},
		{"node --import reg app.js", []string{"reg", "reg.js", "reg.mjs", "reg.cjs", "reg.ts", "reg.mts", "app.js"}, nil, []string{"node@.*", "reg@."}, []string{"node"}},
		{"node -r ./pre/x.js", []string{"./pre/x.js"}, nil, []string{"node@.*"}, []string{"node"}},
		{"uv run --directory mcp srv", []string{}, []string{"mcp"}, []string{"uv@.*", "srv@.,mcp*"}, []string{"uv"}},
		{"srv --cwd=w1 ./run", []string{"./run", "w1/run"}, []string{"w1"}, []string{"srv@.*"}, nil},
		{"cd tools && ./run", []string{"./run", "tools/run"}, []string{"tools"}, nil, nil},
		{"cd a && cd b && x.sh", []string{"x.sh", "a/x.sh", "b/x.sh", "a/b/x.sh"}, []string{"a", "b", "a/b"}, nil, nil},
		{"cd -P tools; ./run", []string{"./run", "tools/run"}, []string{"tools"}, nil, nil},
		{"cd .. && ./run", []string{"./run"}, nil, nil, nil},
		{"cd . && ./run", []string{"./run"}, nil, nil, nil},
		{"cd $HOME && ./run", []string{"./run"}, nil, nil, nil},
		{"make -C sub all", nil, []string{"sub"}, []string{"make@.*"}, []string{"make"}},
		{"echo a.b FOO=bar -x", nil, nil, []string{"echo@.*"}, nil},
		// Plain words are looked up only as the command, an interpreter's script
		// or a fetcher's tool (#370 B-M1).
		{"node server", nil, nil, []string{"node@.*", "server@."}, []string{"node"}},
		{"npx -y @playwright/mcp@latest --headless --browser chrome", []string{"@playwright/mcp@latest"}, nil, []string{"npx@.*"}, nil},
		{"uvx awslabs.x@latest", nil, nil, []string{"uvx@.*"}, nil},
		{"npx eslint --fix", nil, nil, []string{"npx@.*", "eslint@.*"}, []string{"npx"}},
		{"npx eslint@9 --fix", nil, nil, []string{"npx@.*", "eslint@.*"}, nil},
		{"npx --yes some-server", nil, nil, []string{"npx@.*", "some-server@.*"}, nil},
		{"pnpm dlx create-x", nil, nil, []string{"pnpm@.*", "create-x@.*"}, nil},
		{"pnpm exec tsc", nil, nil, []string{"pnpm@.*", "tsc@.*"}, []string{"pnpm"}},
		{"npm exec -y cowsay@1", nil, nil, []string{"npm@.*", "cowsay@.*"}, nil},
		{"docker run -i --rm img", nil, nil, []string{"docker@.*"}, nil},
		{"!/usr/bin/gh auth git-credential", []string{"/usr/bin/gh"}, nil, nil, nil},
		{"npm test", nil, nil, []string{"npm@.*"}, []string{"npm"}},
		{"go test ./...", []string{"./..."}, nil, []string{"go@.*"}, []string{"go"}},
		{"bash -c \"node srvz\"", nil, nil, []string{"bash@.*", "node@.*", "srvz@."}, []string{"node"}},
		{"env FOO=1 node srvw", nil, nil, []string{"env@.*", "node@.*", "srvw@."}, []string{"node"}},
		{"uv run pytest -q", nil, nil, []string{"uv@.*", "pytest@.*"}, []string{"uv"}},
		{"python -X dev script", nil, nil, []string{"python@.*", "dev@.", "script@."}, nil},
		{"npx -y pkg@1 && npm test", nil, nil, []string{"npx@.*", "pkg@.*", "npm@.*"}, []string{"npm"}},
		// A fetched tool that is a runner keeps its manifest, and what follows it
		// reads as after that command (task 2 review I1).
		{"npx -y yarn install", nil, nil, []string{"npx@.*", "yarn@.*"}, []string{"yarn"}},
		{"pnpm dlx yarn@1 install", nil, nil, []string{"pnpm@.*", "yarn@.*"}, []string{"yarn"}},
		{"pipx run poetry install", nil, nil, []string{"pipx@.*", "poetry@.*"}, []string{"poetry"}},
		{"uvx poetry run pytest", nil, nil, []string{"uvx@.*", "poetry@.*", "pytest@.*"}, []string{"poetry"}},
		// An interpreter, fetcher or wrapper later in the command is a new
		// command word, except among a script's own arguments (I2).
		{"cross-env NODE_ENV=test node server", nil, nil, []string{"cross-env@.*", "node@.*", "server@."}, []string{"node"}},
		{"timeout 60 node server", nil, nil, []string{"timeout@.*", "node@.*", "server@."}, []string{"node"}},
		{"dotenv -- node server", nil, nil, []string{"dotenv@.*", "node@.*", "server@."}, []string{"node"}},
		{"node server.js node x", []string{"server.js"}, nil, []string{"node@.*"}, []string{"node"}},
		// A variable command word may be an interpreter: its next word is a script.
		{"$PY srv", nil, nil, []string{"srv@."}, nil},
		{"${NODE} srv2", nil, nil, []string{"srv2@."}, nil},
		// An option with a value before a fetch subcommand.
		{"pnpm --filter web exec tsc", nil, nil, []string{"pnpm@.*", "tsc@.*"}, []string{"pnpm"}},
	}
	for _, c := range cases {
		ci := parseCommand(of, c.in)
		var plain []string
		for _, p := range ci.plain {
			w := p.word + "@" + strings.Join(p.bases, ",")
			if p.tool {
				w += "*"
			}
			plain = append(plain, w)
		}
		same := func(a, b []string) bool { return slices.Equal(a, b) || len(a) == 0 && len(b) == 0 }
		if !same(ci.paths, c.paths) || !same(ci.dirs, c.dirs) || !same(plain, c.plain) || !same(ci.runners, c.runners) {
			t.Errorf("parseCommand(%q) = paths %q dirs %q plain %q runners %q; want %q %q %q %q",
				c.in, ci.paths, ci.dirs, plain, ci.runners, c.paths, c.dirs, c.plain, c.runners)
		}
	}
}

func TestScriptNames(t *testing.T) {
	set := func(n string) []string { return []string{"pre" + n, n, "post" + n} }
	install := []string{"preinstall", "install", "postinstall", "prepublish", "preprepare", "prepare", "postprepare"}
	restart := append(append(set("restart"), set("stop")...), set("start")...)
	cases := []struct {
		runner string
		args   []string
		names  []string
		all    bool
	}{
		{"npm", []string{"run", "lint"}, set("lint"), false},
		{"npm", []string{"run-script", "lint:js"}, set("lint:js"), false},
		{"npm", []string{"test"}, set("test"), false},
		{"npm", []string{"t"}, set("test"), false},
		{"npm", []string{"restart"}, restart, false},
		{"npm", []string{"ci"}, install, false},
		{"yarn", nil, install, false},
		{"yarn", []string{"lint"}, set("lint"), false},
		{"bun", []string{"dev"}, set("dev"), false},
		{"pnpm", []string{"run", "build"}, set("build"), false},
		{"npm", []string{"run"}, nil, true},
		{"npm", []string{"run", "$TASK"}, nil, true},
		{"npm", []string{"--prefix", "w", "start"}, nil, true},
		{"npm", []string{"run", "-w", "pkg", "lint"}, nil, true},
		{"npm", []string{"run", "lint", "--workspaces"}, set("lint"), false},
		{"npm", []string{"pack"}, []string{"prepack", "prepare", "postpack", "prepublishOnly", "prepublish", "publish", "postpublish"}, false},
		{"npm", []string{"publish"}, []string{"prepack", "prepare", "postpack", "prepublishOnly", "prepublish", "publish", "postpublish"}, false},
		{"npm", []string{"it"}, append(slices.Clone(install), set("test")...), false},
		{"npm", []string{"install-ci-test"}, append(slices.Clone(install), set("test")...), false},
		{"npm", []string{"rebuild"}, []string{"preinstall", "install", "postinstall", "prepare"}, false},
		{"npm", []string{"version", "patch"}, set("version"), false},
		{"yarn", []string{"dlx", "x"}, nil, false},
		{"pnpm", []string{"exec", "x"}, nil, false},
		{"npm", []string{"exec", "x"}, nil, false},
	}
	for _, c := range cases {
		names, all := scriptNames(c.runner, c.args)
		if !slices.Equal(names, c.names) || all != c.all {
			t.Errorf("scriptNames(%q, %q) = %q, %v; want %q, %v", c.runner, c.args, names, all, c.names, c.all)
		}
	}
}

func TestParseCommandScripts(t *testing.T) {
	ci := parseCommand("", "cd w && npm run lint; make -C sub fmt")
	if len(ci.scripts) != 2 {
		t.Fatalf("scripts = %+v", ci.scripts)
	}
	if r := ci.scripts[0]; r.runner != "npm" || !slices.Equal(r.dirs, []string{".", "w"}) || !slices.Equal(r.names, []string{"prelint", "lint", "postlint"}) {
		t.Errorf("npm ref = %+v", r)
	}
	if r := ci.scripts[1]; r.runner != "make" || !r.all || !slices.Equal(r.dirs, []string{".", "w", "sub", "w/sub"}) {
		t.Errorf("make ref = %+v", r)
	}
}

func TestWorkspaceCall(t *testing.T) {
	cases := []struct {
		runner string
		args   string
		want   bool
	}{
		{"npm", "run lint --workspaces", true}, {"npm", "run lint --workspace=pkg", true}, {"npm", "run lint -w pkg", true},
		{"pnpm", "--filter web lint", true}, {"pnpm", "-F web lint", true}, {"pnpm", "-r lint", true}, {"pnpm", "run lint --recursive", true},
		{"yarn", "workspace web lint", true}, {"yarn", "workspaces foreach run lint", true},
		{"pnpm", "--filter web exec eslint", true}, {"pnpm", "exec --filter web eslint", true}, {"npm", "run exec --workspaces", true},
		{"npm", "exec --workspaces -- eslint", true}, {"yarn", "dlx -w x", false},
		{"yarn", "--cwd x workspace web lint", true}, {"yarn", "--silent workspaces foreach run lint", true},
		{"pnpm", "recursive run lint", true}, {"pnpm", "m run lint", true}, {"pnpm", "multi run lint", true}, {"pnpm", "--silent recursive run lint", true},
		{"pnpm", "run m", false}, {"pnpm", "run recursive", false}, {"yarn", "run workspace", false}, {"yarn", "lint workspace", false},
		{"pnpm", "--filter-prod web lint", true}, {"pnpm", "--filter-prod=web lint", true}, {"npm", "run lint --workspace-x", true},
		{"pnpm", "--workspace-root lint", true},
		{"npm", "run lint", false}, {"yarn", "lint", false}, {"npm", "run test -- -w", false}, {"yarn", "-w", false},
		{"npm", "run lint --work", false},
	}
	for _, c := range cases {
		if got := workspaceCall(c.runner, strings.Fields(c.args)); got != c.want {
			t.Errorf("workspaceCall(%q, %q) = %v, want %v", c.runner, c.args, got, c.want)
		}
	}
}

func TestParseCommandNodeRun(t *testing.T) {
	ci := parseCommand("", "node --run lint; node server.js; node --run $X")
	if len(ci.scripts) != 2 || ci.scripts[0].runner != "node" || !slices.Equal(ci.scripts[0].names, []string{"lint"}) || ci.scripts[0].all || !ci.scripts[1].all {
		t.Fatalf("scripts = %+v", ci.scripts)
	}
}

func TestRecipeFilesIn(t *testing.T) {
	cases := []struct {
		runner, args string
		files        []string
		bad          bool
	}{
		{"make", "-f a.mk x", []string{"a.mk"}, false},
		{"make", "-fa.mk", []string{"a.mk"}, false},
		{"make", "-sf a.mk", []string{"a.mk"}, false},
		{"make", "-sfa.mk", []string{"a.mk"}, false},
		{"make", "--file=a.mk", []string{"a.mk"}, false},
		{"make", "--makefile a.mk", []string{"a.mk"}, false},
		{"make", "-Cfoo x", nil, false},
		{"make", "-j4 -f a.mk", []string{"a.mk"}, false},
		{"just", "-fx.just", []string{"x.just"}, false},
		{"just", "--justfile=x", []string{"x"}, false},
		{"task", "-tx.yml", []string{"x.yml"}, false},
		{"task", "--taskfile x.yml", []string{"x.yml"}, false},
		{"task", "-f lint", nil, false},
		{"make", "-f", nil, true},
		{"make", "-f $F", nil, true},
		{"make", "--file={{x}}", nil, true},
		{"make", "fmt", nil, false},
		{"make", "fmt -- -f", nil, false},
		// GNU getopt abbreviations of a file option; an ambiguous one counts too.
		{"make", "--mak=a.mk", []string{"a.mk"}, false},
		{"make", "--fi a.mk", []string{"a.mk"}, false},
		{"make", "--f a.mk", []string{"a.mk"}, false},
		{"make", "--make a.mk", []string{"a.mk"}, false},
		{"just", "--just=x", []string{"x"}, false},
		{"task", "--task x.yml", []string{"x.yml"}, false},
		{"make", "--files a.mk", nil, false},
		{"make", "--debug a.mk", nil, false},
		{"just", "--file x", nil, false},
		// A value the shell would expand cannot be named (#370 R13).
		{"make", "-f a*.mk", nil, true}, {"make", "-f a?.mk", nil, true}, {"make", "-f [ab].mk", nil, true},
		{"make", "-f {a,b}.mk", nil, true}, {"make", "-f a,b.mk", nil, true}, {"make", "-f ~/x.mk", nil, true},
		{"make", "--file=x}.mk", nil, true},
	}
	for _, c := range cases {
		files, bad := recipeFilesIn(c.runner, strings.Fields(c.args))
		if !slices.Equal(files, c.files) || bad != c.bad {
			t.Errorf("recipeFilesIn(%q, %q) = %q, %v; want %q, %v", c.runner, c.args, files, bad, c.files, c.bad)
		}
	}
	// A value with white space at an end cannot be named faithfully.
	if _, bad := recipeFilesIn("make", []string{"-f", " x.mk"}); !bad {
		t.Error("a value with a leading space is not bad")
	}
}

// shellWords unquotes the way sh does; a word whose quote does not close is
// not ok.
func TestShellWords(t *testing.T) {
	cases := []struct {
		in   string
		want []string
		ok   []bool
	}{
		{`make -f"x.mk" fmt`, []string{"make", "-fx.mk", "fmt"}, []bool{true, true, true}},
		{`make '-f' x.mk`, []string{"make", "-f", "x.mk"}, []bool{true, true, true}},
		{`"--file=x.mk"`, []string{"--file=x.mk"}, []bool{true}},
		{`-f\ x.mk`, []string{"-f x.mk"}, []bool{true}},
		{`-"f"x.mk --fi""le=y`, []string{"-fx.mk", "--file=y"}, []bool{true, true}},
		{`"a \"b\" \$c \x" 'd\e'`, []string{`a "b" $c \x`, `d\e`}, []bool{true, true}},
		{"a\\\nb", []string{"ab"}, []bool{true}},
		{`make '-f x.mk`, []string{"make", "-f x.mk"}, []bool{true, false}},
		{`x "y`, []string{"x", "y"}, []bool{true, false}},
		{`  `, nil, nil},
	}
	for _, c := range cases {
		ws := shellWords(c.in)
		var got []string
		var oks []bool
		for _, w := range ws {
			got = append(got, w.text)
			oks = append(oks, w.ok)
			if w.start < 0 || w.end > len(c.in) || w.start >= w.end {
				t.Errorf("shellWords(%q): bad span %d..%d", c.in, w.start, w.end)
			}
		}
		if !slices.Equal(got, c.want) || !slices.Equal(oks, c.ok) {
			t.Errorf("shellWords(%q) = %q %v, want %q %v", c.in, got, oks, c.want, c.ok)
		}
	}
}

// A recipe runner's options are read from the unquoted words of the command.
func TestParseCommandRecipeQuoting(t *testing.T) {
	cases := []struct {
		cmd   string
		files []string
		bad   bool
	}{
		{`make -f"q.mk" fmt`, []string{"q.mk"}, false},
		{`make '-f' q.mk fmt`, []string{"q.mk"}, false},
		{`make "--file=q.mk" fmt`, []string{"q.mk"}, false},
		{`make -"f"q.mk fmt`, []string{"q.mk"}, false},
		{`make --fi""le=q.mk fmt`, []string{"q.mk"}, false},
		{`make -f "my file.mk" fmt`, []string{"my file.mk"}, false},
		{`make -f\q.mk fmt`, []string{"q.mk"}, false},
		{`bash -c "make -f q.mk fmt"`, []string{"q.mk"}, false},
		{`bash -c "bash -c 'make -f q.mk fmt'"`, []string{"q.mk"}, false},
		{`cd w && make -f q.mk fmt`, []string{"q.mk"}, false},
		{`make fmt; make -f q.mk fmt`, []string{"q.mk"}, false},
		{`make -f\ q.mk fmt`, nil, true},
		{`make '-f q.mk fmt`, nil, true},
		{`make fmt "-f`, nil, true},
		{`make -f "q.mk`, nil, true},
		{`make fmt "x`, nil, true}, // R13: an unclosed word after the runner
		{`make "a;b" -f q.mk`, nil, true},
		{`make -C "$(pwd)" -f q.mk lint`, nil, true},
		{`make $(x) -f q.mk`, nil, true},
		{"make `x` -f q.mk", nil, true},
		{`make a\;b -f q.mk`, nil, true},
		{`make fmt \`, nil, true},
		{`bash -c "make -f q.mk lint && echo ok"`, []string{"q.mk"}, false},
		{`sh -c 'make -f q.mk fmt || true'`, []string{"q.mk"}, false},
		{`(cd w && make -f q.mk fmt)`, []string{"q.mk"}, false},
		{`make -f q.mk fmt | tee log`, []string{"q.mk"}, false},
		{`make -f q.mk fmt; echo "a;b"`, []string{"q.mk"}, false},
		{`bash -c "sh -c 'nice make'"`, nil, false},
		{`bash -c "sh -c 'nice make -f q.mk'"`, []string{"q.mk"}, false},
	}
	for _, c := range cases {
		ci := parseCommand("", c.cmd)
		r := ci.scripts[len(ci.scripts)-1]
		if r.runner != "make" || !slices.Equal(r.files, c.files) || r.badFile != c.bad {
			t.Errorf("parseCommand(%q) make ref files %q bad %v, want %q %v", c.cmd, r.files, r.badFile, c.files, c.bad)
		}
	}
}
