package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
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

// handEdit writes a valid profiles list over path the way a person with an
// editor would: profile "a" plus an env var, in a different layout from ours.
func handEdit(t *testing.T, path string) []byte {
	t.Helper()
	hand := []byte(`[{"name":"a","bin":"claude","env":{"EDITED":"by hand"}}]`)
	if err := os.WriteFile(path, hand, 0o600); err != nil {
		t.Fatal(err)
	}
	return hand
}

func TestSaveRefusesAfterAHandEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(config.AgentConfig{Name: "a", Bin: "claude"}); err != nil {
		t.Fatal(err)
	}
	hand := handEdit(t, path)

	err = s.Save(config.AgentConfig{Name: "b", Bin: "bash"})
	if !errors.Is(err, ErrChangedOnDisk) {
		t.Fatalf("Save over a hand-edited file = %v, want ErrChangedOnDisk", err)
	}
	got, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !bytes.Equal(got, hand) {
		t.Fatalf("the hand edit was overwritten: %s", got)
	}
	if _, ok := s.Get("b"); ok {
		t.Fatal("a refused Save must not change the in-memory map")
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Save(config.AgentConfig{Name: "b", Bin: "bash"}); err != nil {
		t.Fatalf("Save after reloading the file = %v, want nil", err)
	}
}

func TestDeleteRefusesAfterAHandEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(config.AgentConfig{Name: "a", Bin: "claude"}); err != nil {
		t.Fatal(err)
	}
	hand := handEdit(t, path)

	err = s.Delete("a")
	if !errors.Is(err, ErrChangedOnDisk) {
		t.Fatalf("Delete over a hand-edited file = %v, want ErrChangedOnDisk", err)
	}
	got, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !bytes.Equal(got, hand) {
		t.Fatalf("the hand edit was overwritten: %s", got)
	}
	if _, ok := s.Get("a"); !ok {
		t.Fatal("a refused Delete must leave the profile in memory")
	}
}

func TestSaveRefusesWhenTheFileAppearedAfterOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	s, err := Open(path) // no file yet
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(config.AgentConfig{Name: "a", Bin: "claude"}); !errors.Is(err, ErrChangedOnDisk) {
		t.Fatalf("Save when the file appeared = %v, want ErrChangedOnDisk", err)
	}
}

func TestSaveRefusesWhenTheFileWasRemoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(config.AgentConfig{Name: "a", Bin: "claude"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(config.AgentConfig{Name: "b", Bin: "bash"}); !errors.Is(err, ErrChangedOnDisk) {
		t.Fatalf("Save when the file was removed = %v, want ErrChangedOnDisk", err)
	}
}

func TestSaveAllowsAnIdenticalRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(config.AgentConfig{Name: "a", Bin: "claude"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	// the mtime moved, the content did not
	if err := s.Save(config.AgentConfig{Name: "b", Bin: "bash"}); err != nil {
		t.Fatalf("Save after an identical rewrite = %v, want nil", err)
	}
}

func TestOwnWritesAreNotHandEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		name string
		do   func() error
	}{
		{"Save a", func() error { return s.Save(config.AgentConfig{Name: "a", Bin: "claude"}) }},
		{"Save a again", func() error { return s.Save(config.AgentConfig{Name: "a", Bin: "claude", Base: "clean"}) }},
		{"Delete a", func() error { return s.Delete("a") }},
		{"Save b", func() error { return s.Save(config.AgentConfig{Name: "b", Bin: "bash"}) }},
	}
	for _, st := range steps {
		if err := st.do(); err != nil {
			t.Fatalf("%s = %v, want nil", st.name, err)
		}
	}
}
