package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/culiops/culiops-agent-console/internal/config"
)

func TestStore_CRUD_Persists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 0 {
		t.Fatal("new store should be empty")
	}

	if err := s.Save(config.AgentConfig{Name: "claude", Bin: "claude", Base: "inherit"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(config.AgentConfig{Name: "shell", Bin: "bash"}); err != nil {
		t.Fatal(err)
	}
	// update
	if err := s.Save(config.AgentConfig{Name: "claude", Bin: "claude", Base: "clean"}); err != nil {
		t.Fatal(err)
	}

	// reopen — data persisted, sorted
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	list := s2.List()
	if len(list) != 2 || list[0].Name != "claude" || list[1].Name != "shell" {
		t.Fatalf("reopened list wrong: %+v", list)
	}
	if p, _ := s2.Get("claude"); p.Base != "clean" {
		t.Fatalf("update not persisted: %+v", p)
	}

	if err := s2.Delete("claude"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Get("claude"); ok {
		t.Fatal("claude should be deleted")
	}
	if err := s2.Delete("ghost"); err != ErrNotFound {
		t.Fatalf("delete unknown want ErrNotFound, got %v", err)
	}

	// file is owner-only
	info, _ := os.Stat(path)
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("store file too permissive: %o", info.Mode().Perm())
	}
}

func TestStore_Open_CorruptErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("corrupt store should return an error so the caller can quarantine it")
	}
}
