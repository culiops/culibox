package clone

import (
	"slices"
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
