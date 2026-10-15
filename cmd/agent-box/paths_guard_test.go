package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// pathEscapes is every place the product code reaches outside the data root on
// its own: the user's home, the temp dir, an XDG-style dot dir or ~/Library. Each entry is
// "<file> <kind>" → count, with the reason it may. Anything agent-box WRITES
// belongs under the data root (config.Root / data_dir), so a new escape fails
// this test until someone decides, here, that it is a read of another program's
// files, a guard, or a kernel limit.
var pathEscapes = map[string]int{
	// Locating the data root itself, and the ~/.config fallback for the config file.
	"internal/config/config.go literal .local":   1, // ResolvedDataDir, for a Config not built by Resolve
	"internal/config/resolve.go literal .local":  1,
	"internal/config/resolve.go literal .config": 1,
	// The bundle reads the user's Claude config (a read, never a write) and
	// refuses $HOME itself as a bundle source.
	"internal/bundle/bundle.go os.UserHomeDir": 3,
	// A spawned agent with no HOME gets the user's: the agent's env, not our storage.
	"internal/launcher/launcher.go os.UserHomeDir": 1,
	// Expands "~" in a profile's default_cwd: a workspace the user picked.
	"internal/web/profiles.go os.UserHomeDir": 1,
	// Refuses $HOME as a boxed workspace (it would mount the whole home).
	"internal/box/box.go os.UserHomeDir": 1,
	// darwin caps a socket path at 103 bytes, so the socket dir cannot live in the
	// data root there; it is still derived from it (a hash) and holds only sockets.
	"internal/abduco/socketpath.go literal /tmp": 1,
	// doctor's throwaway probe dir, removed before it returns.
	"internal/doctor/doctor.go literal /tmp": 1,
	// The home dir is resolved once, here, and handed to everything else; the
	// -config help text names the lookup order.
	"cmd/agent-box/main.go os.UserHomeDir": 1,
	"cmd/agent-box/cli.go literal .local":  1,
	"cmd/agent-box/cli.go literal .config": 1,
	// /tmp INSIDE the box: its tmpfs mount and the pid file kept on it.
	"internal/box/argv.go literal /tmp": 2,
	// The file-search index skips or includes dirs under the home it browses.
	"internal/web/fssearch.go literal .local":  1,
	"internal/web/fssearch.go literal .config": 3,
	"internal/web/fssearch.go literal .cache":  1,
}

func TestNothingWritesOutsideTheDataRoot(t *testing.T) {
	root := filepath.Join("..", "..")
	got := map[string]int{}
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.SelectorExpr: // a call or a func value (serveHome(os.UserHomeDir))
					if x, ok := n.X.(*ast.Ident); ok && x.Name == "os" &&
						(n.Sel.Name == "UserHomeDir" || n.Sel.Name == "TempDir") {
						got[rel+" os."+n.Sel.Name]++
					}
				case *ast.BasicLit:
					if n.Kind != token.STRING {
						return true
					}
					v, err := strconv.Unquote(n.Value)
					if err != nil {
						return true
					}
					for _, needle := range escapeNeedles(v) {
						got[rel+" literal "+needle]++
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var diffs []string
	for k, n := range got {
		if pathEscapes[k] != n {
			diffs = append(diffs, fmt.Sprintf("  %q: found %d, allowed %d", k, n, pathEscapes[k]))
		}
	}
	for k, n := range pathEscapes {
		if _, ok := got[k]; !ok {
			diffs = append(diffs, fmt.Sprintf("  %q: found 0, allowed %d (stale entry)", k, n))
		}
	}
	if len(diffs) > 0 {
		sort.Strings(diffs)
		t.Fatalf("paths outside the data root changed. Anything agent-box writes must derive from "+
			"the data root (config.Root / data_dir); a read of another program's files, a guard or a "+
			"kernel limit goes in pathEscapes with its reason:\n%s", strings.Join(diffs, "\n"))
	}
}

// escapeNeedles names the out-of-root locations a string literal points into.
// Dot dirs count only as a whole path component, so SQL like
// "excluded.cache_read" is not mistaken for ~/.cache.
func escapeNeedles(v string) []string {
	var out []string
	for _, n := range []string{"/tmp", "Library/"} {
		if strings.Contains(v, n) {
			out = append(out, n)
		}
	}
	parts := strings.Split(v, "/")
	for _, n := range []string{".local", ".config", ".cache"} {
		for _, p := range parts {
			if p == n {
				out = append(out, n)
				break
			}
		}
	}
	return out
}
