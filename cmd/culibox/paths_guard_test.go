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

// pathEscapes is every place the product code OBTAINS a location outside the
// data root: the home, temp, config or cache dir from the OS or the environment,
// or a literal path into /tmp, ~/Library or a ~/.local, ~/.config or ~/.cache
// component. Each entry is "<file> <kind>" → count, with the reason it may.
// Anything agent-box writes belongs under the data root (config.Root /
// data_dir), so a new escape fails this test until someone decides, here, that
// it is a read of another program's files, a guard, or a kernel limit.
//
// Its limit: it sees where those locations come from, not what is built from
// them. filepath.Join(home, "x") on the home that main.go already resolved is
// invisible here; review catches that.
var pathEscapes = map[string]int{
	// The refusal list for VM shares: ~/Library and ~/.config/gcloud are named so they can
	// never be shared (a deny, never a read or a write).
	"internal/vmshares/check.go literal .config": 3,
	"internal/vmshares/check.go literal Library": 1,
	// The workspace refusal list: /Library is named so it can never be a workspace
	// or a root (a deny, never a read or a write).
	"internal/wsroots/forbid.go literal Library": 1,
	// Locating the data root itself, and the ~/.config fallback for the config file.
	"internal/config/resolve.go literal .local":  1, // DefaultRoots; ResolvedDataDir goes through it
	"internal/config/resolve.go literal .config": 1,
	// The bundle reads the user's Claude config (a read, never a write): "~" in
	// bundle_source, the default ~/.claude source, and refusing a source whose
	// symlinks resolve to the whole home, and the home CLAUDE.md imports resolve
	// against (read-only).
	"internal/bundle/bundle.go os.UserHomeDir": 4,
	// ~/.config/gh in the import deny list (a deny, never a read or a write).
	"internal/bundle/bundle.go literal .config": 1,
	// Lists the profiles of the host's ~/.aws/config for the credentials screen (a read of
	// the user's own AWS config, never a write; AWS_CONFIG_FILE wins when set).
	"internal/awshost/profiles.go os.UserHomeDir": 1,
	// A spawned agent with no HOME gets the user's: the agent's env, not our storage.
	"internal/launcher/launcher.go os.UserHomeDir": 1,
	// Refuses the home dir as a boxed profile's default_cwd (UnboxableDefaultCwd).
	"internal/web/profiles.go os.UserHomeDir": 1,
	// Refuses $HOME as a boxed workspace (it would mount the whole home).
	"internal/box/box.go os.UserHomeDir": 1,
	// Expands a "~" / "~user" core.hooksPath the way host git does, to decide
	// whether the hooks dir lies in an in-place workspace (a read, never a write);
	// and tells a git refusal whose repo is the home directory to say Host or a
	// move, not a copy (resolvedHome, #371: a path comparison only).
	"internal/box/inplace.go os.UserHomeDir": 2,
	"internal/box/inplace.go HomeDir":        1,
	// The merge guard reads the user's global git config ($XDG_CONFIG_HOME/git/config,
	// ~/.config/git/config) for core.hooksPath, so a hooks dir inside the repo is
	// denied (a read, never a write; #370). The second .config is a deny-list
	// name: a box commit under the repo's own top-level .config/ is not merged.
	"internal/clone/merge.go env XDG_CONFIG_HOME": 1,
	"internal/clone/merge.go literal .config":     2,
	// darwin caps a socket path at 103 bytes, so the socket dir cannot live in the
	// data root there; it is still derived from it (a hash) and holds only sockets.
	"internal/abduco/socketpath.go literal /tmp": 1,
	// doctor's throwaway probe dir, removed before it returns.
	"internal/doctor/doctor.go literal /tmp": 1,
	// The home dir and XDG_CONFIG_HOME are resolved once, here, and handed to
	// everything else; the -config help text names the lookup order.
	"cmd/culibox/main.go os.UserHomeDir":      1,
	"cmd/culibox/main.go env XDG_CONFIG_HOME": 1,
	// `box setup` reads the config files of every instance that could share the
	// Docker store (to keep the images they pin); the ~/.config ones follow this.
	"cmd/culibox/boxprune.go env XDG_CONFIG_HOME": 1,
	"cmd/culibox/cli.go literal .local":           2, // -config help and the usage text
	// `culibox service` writes its one unit file where the user's supervisor
	// reads it (~/.config/systemd/user or ~/Library/LaunchAgents), marked as ours.
	"internal/service/platform.go literal .config": 1,
	"internal/service/platform.go literal Library": 1,
	// `culibox service install` refuses a binary in the temp dir (go run): a check, never a write.
	"cmd/culibox/servicecmd.go os.TempDir": 1,
	"cmd/culibox/cli.go literal .config":   1,
	// context_paths denylist: refuses /tmp itself as a context path (it would
	// shadow the box's tmpfs), and the in-box /tmp/agentbox-gh and
	// /tmp/ac-session-* (prefix match and its message); names compared with,
	// never locations used.
	"internal/config/contextpaths.go literal /tmp": 4,
	// /tmp INSIDE the box: its tmpfs mount and the pid file kept on it.
	"internal/box/argv.go literal /tmp": 2,
	// /tmp INSIDE the box: where the Playwright MCP writes (PlaywrightOutBox);
	// the host side of that bind lives under the data root (OutputDir).
	"internal/box/boxout.go literal /tmp": 1,
	// ".cache/go-build" is a path INSIDE a box home (pruned relative to an os.Root on
	// <data root>/boxstate/<box>), not the user's ~/.cache.
	"internal/box/reclaim.go literal .cache": 1,
	// gh's config dir INSIDE a GitHub-bound box (GHConfigDir): dies with the box.
	"internal/broker/boxfiles.go literal /tmp": 1,
	// The bundle container's own tmpfs and HOME (clone fetch), not a host path.
	"internal/clone/fetch.go literal /tmp": 1,
	// The git status/diff container's own tmpfs and HOME (gitview), not a host path.
	"internal/gitview/args.go literal /tmp": 1,
	// Claude Code's per-user temp folder (claude-<uid> under $CLAUDE_CODE_TMPDIR,
	// /tmp or the OS temp dir): a READ of another program's files. The fs API
	// serves it read-only (#50); agent-box never writes there.
	"internal/web/fsscratch.go literal /tmp": 1,
	"cmd/culibox/main.go os.TempDir":         1,
	// msgBoxOnly names /tmp/playwright-mcp in the refusal text: prose shown to
	// the user, never a location used.
	"internal/web/fsmsg.go literal /tmp": 1,
	// The file-search index skips or includes dirs under the home it browses.
	"internal/web/fssearch.go literal .local":  1,
	"internal/web/fssearch.go literal .config": 3,
	"internal/web/fssearch.go literal .cache":  1,
}

func TestOutOfRootLocationsAreAllowlisted(t *testing.T) {
	root := filepath.Join("..", "..")
	got := map[string]int{}
	for _, dir := range []string{"cmd", "docker", "internal"} {
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
				for _, k := range escapeKinds(n) {
					got[rel+" "+k]++
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
		t.Fatalf("locations outside the data root changed. Anything agent-box writes must derive from "+
			"the data root (config.Root / data_dir); a read of another program's files, a guard or a "+
			"kernel limit goes in pathEscapes with its reason:\n%s", strings.Join(diffs, "\n"))
	}
}

// escapeKinds names the ways node n reaches a location outside the data root:
// the home, temp, config or cache dir obtained from the OS or the environment,
// a user's HomeDir, a temp file in the default temp dir, or a string literal
// pointing into /tmp, ~/Library or a ~/.local, ~/.config or ~/.cache component.
// Dot dirs count only as a whole path component, so SQL like
// "excluded.cache_read" is not mistaken for ~/.cache.
func escapeKinds(n ast.Node) []string {
	switch n := n.(type) {
	case *ast.SelectorExpr: // a call or a func value (serveHome(os.UserHomeDir))
		if isPkg(n.X, "os") {
			switch n.Sel.Name {
			case "UserHomeDir", "TempDir", "UserConfigDir", "UserCacheDir":
				return []string{"os." + n.Sel.Name}
			}
		}
		if n.Sel.Name == "HomeDir" { // user.Current().HomeDir and friends
			return []string{"HomeDir"}
		}
	case *ast.CallExpr:
		sel, ok := n.Fun.(*ast.SelectorExpr)
		if !ok || !isPkg(sel.X, "os") || len(n.Args) == 0 {
			return nil
		}
		arg, _ := literal(n.Args[0])
		switch sel.Sel.Name {
		case "MkdirTemp", "CreateTemp":
			if arg == "" && isStringLit(n.Args[0]) {
				return []string{"os." + sel.Sel.Name + `("")`}
			}
		case "Getenv", "LookupEnv":
			if arg == "HOME" || arg == "TMPDIR" || strings.HasPrefix(arg, "XDG_") {
				return []string{"env " + arg}
			}
		}
	case *ast.BasicLit:
		v, ok := literal(n)
		if !ok {
			return nil
		}
		var out []string
		if strings.Contains(v, "/tmp") {
			out = append(out, "literal /tmp")
		}
		parts := strings.Split(v, "/")
		for _, c := range []string{".local", ".config", ".cache", "Library"} {
			for _, p := range parts {
				if p == c {
					out = append(out, "literal "+c)
					break
				}
			}
		}
		return out
	}
	return nil
}

func isPkg(x ast.Expr, name string) bool {
	id, ok := x.(*ast.Ident)
	return ok && id.Name == name
}

func isStringLit(x ast.Expr) bool {
	l, ok := x.(*ast.BasicLit)
	return ok && l.Kind == token.STRING
}

func literal(x ast.Node) (string, bool) {
	l, ok := x.(*ast.BasicLit)
	if !ok || l.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(l.Value)
	return v, err == nil
}
