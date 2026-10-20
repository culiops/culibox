package secretstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKeyDescriptionPerDataDir(t *testing.T) {
	a, b := KeyHash("/a/agent-console"), KeyHash("/a/agent-console-prod")
	if a == b {
		t.Fatal("two data dirs share a key hash")
	}
	if len(a) != 16 {
		t.Fatalf("hash length %d, want 16", len(a))
	}
	if KeyHash("/a/x/") != KeyHash("/a/x") {
		t.Fatal("trailing slash changes the hash")
	}
	if KeyHash("/a/x/../x") != KeyHash("/a/x") {
		t.Fatal("unclean path changes the hash")
	}
}

func TestKeyHashResolvesSymlinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if KeyHash(link) != KeyHash(target) {
		t.Fatal("a symlinked data dir hashes differently from its target")
	}
	// A relative route to the same dir agrees too.
	t.Chdir(root)
	if KeyHash("link") != KeyHash(target) || KeyHash("./real/") != KeyHash(target) {
		t.Fatal("a relative route hashes differently")
	}
}

func TestKeyHashMissingDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-yet")
	h := KeyHash(missing)
	if len(h) != 16 || h != KeyHash(missing+"/") {
		t.Fatalf("missing dir hash %q", h)
	}
}
