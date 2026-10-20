package secretstore

import "testing"

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
