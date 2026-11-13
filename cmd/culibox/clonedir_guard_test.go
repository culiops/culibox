package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Nothing may delete a clone dir: it can hold work not yet home (spec §5.2
// "never stranded"). These are every RemoveAll in product code and why none
// reaches a clone. A new one fails here until someone shows it cannot, or makes
// it refuse while clone.ReadReturnState reports branches not home.
var removeAllSites = map[string]int{
	"internal/bundle/plugins.go":    2, // bundle staging dirs (a temp tree and a replaced final tree)
	"internal/bundle/bundle.go":     4, // bundle staging dirs, and the imports temp dir and its replaced final dir
	"internal/bundle/importsfs.go":  1, // the imports temp dir, under the bundle root
	"internal/bundle/gc.go":         1, // a condemned bundle or imports dir: a direct child of the bundle root, exact generated name
	"internal/web/fsops.go":         1, // the file tree's user-confirmed delete (a person's action, not cleanup)
	"internal/box/boxout.go":        1, // box output dirs under the state root
	"internal/box/reclaim.go":       3, // a box's home and out dirs under the state root (one call in a loop over those two paths), and one of four fixed cache paths inside a home through an os.Root, never through a symlink (two calls: the retry after the chmod walk)
	"internal/clone/create.go":      1, // the temp dir a new clone is built in, before it is renamed into place
	"internal/doctor/doctor.go":     1, // doctor's throwaway probe dir
	"internal/secretstore/store.go": 1, // the secret store's own dir
}

func TestNothingRemovesACloneDir(t *testing.T) {
	root := filepath.Join("..", "..")
	got := map[string]int{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// .git, .worktrees and .superpowers hold no product code; vendor is not ours.
			if n := d.Name(); p != root && (strings.HasPrefix(n, ".") || n == "vendor") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		// A selector, not only a call: RemoveAll handed on as a func value
		// deletes just the same.
		ast.Inspect(f, func(n ast.Node) bool {
			if s, ok := n.(*ast.SelectorExpr); ok && s.Sel.Name == "RemoveAll" {
				got[rel]++
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var diffs []string
	for k, n := range got {
		if removeAllSites[k] != n {
			diffs = append(diffs, fmt.Sprintf("  %q: found %d, pinned %d", k, n, removeAllSites[k]))
		}
	}
	for k, n := range removeAllSites {
		if _, ok := got[k]; !ok {
			diffs = append(diffs, fmt.Sprintf("  %q: found 0, pinned %d (stale entry)", k, n))
		}
	}
	if len(diffs) > 0 {
		sort.Strings(diffs)
		t.Fatalf("RemoveAll sites changed. Nothing may delete a clone dir (it can hold work not yet home); "+
			"show the new one cannot reach a clone, or make it refuse while clone.ReadReturnState reports "+
			"branches not home, then pin it with its reason:\n%s", strings.Join(diffs, "\n"))
	}
}
