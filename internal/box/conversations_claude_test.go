package box

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The key is per (instance, workspace, account, profile): what decides it is
// pinned, and nothing else (a scope change) can enter it, since it takes no
// other input.
func TestConversationsKeyInputs(t *testing.T) {
	base := ConversationsKey("PROD", "/w", "", "p")
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(base) {
		t.Fatalf("key %q is not 12 lowercase hex", base)
	}
	if got := ConversationsKey("PROD", "/w", "default", "p"); got != base {
		t.Errorf(`account "default" = %q, want the same key as "" (%q)`, got, base)
	}
	for name, k := range map[string]string{
		"instance":  ConversationsKey("DEV", "/w", "", "p"),
		"workspace": ConversationsKey("PROD", "/w2", "", "p"),
		"account":   ConversationsKey("PROD", "/w", "work", "p"),
		"profile":   ConversationsKey("PROD", "/w", "", "q"),
	} {
		if k == base {
			t.Errorf("a different %s gives the same key", name)
		}
	}
	// The separator keeps the fields apart: moving a byte across one changes the key.
	if ConversationsKey("PROD", "/w", "a", "bc") == ConversationsKey("PROD", "/w", "ab", "c") {
		t.Error("fields run together")
	}
	// Pinned: a change to the derivation would hide every existing folder.
	if got, want := ConversationsKey("PROD", "/home/u/repo", "", "claude"), "132b31134ce4"; got != want {
		t.Errorf("pinned key = %q, want %q", got, want)
	}
}

func TestEnsureConversationsDirCreatesPrivateFolder(t *testing.T) {
	root := t.TempDir()
	key := ConversationsKey("TEST", "/w", "", "p")
	dir, err := ensureConversationsDir(root, key)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "boxstate", ".conversations", key); dir != want || ConversationsDir(root, key) != want {
		t.Fatalf("dir = %q (ConversationsDir %q), want %q", dir, ConversationsDir(root, key), want)
	}
	for _, p := range []string{filepath.Dir(dir), dir} {
		fi, err := os.Lstat(p)
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v, want a 0700 directory", p, fi, err)
		}
	}
	// Idempotent, and what a session wrote stays.
	if err := os.WriteFile(filepath.Join(dir, "x"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if again, err := ensureConversationsDir(root, key); err != nil || again != dir {
		t.Fatalf("second call: %q %v", again, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "x")); err != nil {
		t.Fatalf("content lost: %v", err)
	}
	// Not a box name: reclaim's listing never takes it for a home.
	if isBoxName(conversationsDirName) {
		t.Fatal(".conversations reads as a box name")
	}
}

func TestEnsureConversationsDirRefusesBadKeysAndLinks(t *testing.T) {
	for _, k := range []string{"", "..", "../../etc", "0bbd0df0bc8", "0BBD0DF0BC8A", "0bbd0df0bc8a/x"} {
		if _, err := ensureConversationsDir(t.TempDir(), k); err == nil {
			t.Errorf("key %q accepted", k)
		}
	}
	key := ConversationsKey("TEST", "/w", "", "p")

	// A symlinked .conversations: refused, and nothing is made where it points.
	root := t.TempDir()
	out := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "boxstate"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(root, "boxstate", ".conversations")); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureConversationsDir(root, key); err == nil {
		t.Fatal("symlinked .conversations accepted")
	}
	if ents, _ := os.ReadDir(out); len(ents) != 0 {
		t.Fatalf("created through the link: %v", ents)
	}

	// A symlinked key folder, even one pointing inside boxstate: refused.
	root = t.TempDir()
	conv := filepath.Join(root, "boxstate", ".conversations")
	if err := os.MkdirAll(conv, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../ab-box-000000000000", filepath.Join(conv, key)); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureConversationsDir(root, key); err == nil {
		t.Fatal("symlinked key folder accepted")
	}

	// A file where the folder belongs: refused.
	root = t.TempDir()
	conv = filepath.Join(root, "boxstate", ".conversations")
	if err := os.MkdirAll(conv, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conv, key), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureConversationsDir(root, key); err == nil {
		t.Fatal("a file accepted as the folder")
	}
}
