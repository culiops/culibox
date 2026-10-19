package clone

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// gitExecs returns where f runs the host's git by name: exec.Command or
// exec.CommandContext whose program is a string literal ending in "git", and
// exec.LookPath of such a literal. Like paths_guard_test.go it sees literals
// only: a program held in a variable (ExecGit.Bin) is how gitexec.go itself
// runs the binary it found, and is the one place that may.
func gitExecs(f *ast.File) []string {
	var found []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "exec" {
			return true
		}
		arg := 0
		switch sel.Sel.Name {
		case "Command", "LookPath":
		case "CommandContext":
			arg = 1
		default:
			return true
		}
		if arg >= len(call.Args) {
			return true
		}
		lit, ok := call.Args[arg].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if name, err := strconv.Unquote(lit.Value); err == nil && path.Base(name) == "git" {
			found = append(found, sel.Sel.Name)
		}
		return true
	})
	return found
}

func TestOnlyGitexecRunsGit(t *testing.T) {
	root := filepath.Join("..", "..")
	var got []string
	for _, dir := range []string{"cmd", "docker", "internal"} {
		start := filepath.Join(root, dir)
		if _, err := os.Stat(start); err != nil {
			continue
		}
		err := filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			for _, what := range gitExecs(f) {
				got = append(got, filepath.ToSlash(rel)+" "+what)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"internal/clone/gitexec.go LookPath"}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("host git is run from %q, want only %q. Every host git exec goes through "+
			"internal/clone/gitexec.go (clean env, no -C into a box-owned clone); route the new "+
			"call through clone.Git", got, want)
	}
}

// The scan is only worth its verdict if it can see what it forbids.
func TestGitExecDetectorSees(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want int
	}{
		{`exec.Command("git", "status")`, 1},
		{`exec.CommandContext(ctx, "git", "status")`, 1},
		{`exec.CommandContext(ctx, "/usr/bin/git", "status")`, 1},
		{`exec.LookPath("git")`, 1},
		{`exec.Command("docker", "ps")`, 0},
		{`exec.CommandContext(ctx, "docker", "git")`, 0},
		{`exec.Command(bin, "status")`, 0},
	} {
		f, err := parser.ParseFile(token.NewFileSet(), "x.go", fmt.Sprintf("package x\nfunc f(){ _ = %s }\n", tc.src), 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(gitExecs(f)); got != tc.want {
			t.Errorf("%s: %d findings, want %d", tc.src, got, tc.want)
		}
	}
}
