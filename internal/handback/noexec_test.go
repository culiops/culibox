package handback

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The package inspects files a box wrote; it must never start a process (not
// the file, not a helper such as file(1) or tar). The spec forbids it, so no
// non-test file may import os/exec.
func TestPackageNeverImportsOSExec(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no sources found: %v", err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, imp := range f.Imports {
			if imp.Path.Value == `"os/exec"` {
				t.Errorf("%s imports os/exec: internal/handback must never run a process", name)
			}
		}
	}
}
