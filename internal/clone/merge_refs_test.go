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
