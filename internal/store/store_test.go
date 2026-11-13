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

// A write that fails must leave the store as it was: the failed value is not
// served by Get and not written by the next successful write, and a failed
// delete leaves the profile where it was. Uses a real read-only directory, so
// the failure comes from the flush itself, after the in-memory change.
func readOnlyStore(t *testing.T) (s *Store, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir = t.TempDir()
	s, err := Open(filepath.Join(dir, "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(config.AgentConfig{Name: "a", Bin: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	return s, dir
}

func TestFailedSaveOfAnExistingProfileKeepsTheOldOne(t *testing.T) {
	s, dir := readOnlyStore(t)
	if err := s.Save(config.AgentConfig{Name: "a", Bin: "new"}); err == nil {
		t.Fatal("the save into a read-only directory succeeded")
	}
	if p, _ := s.Get("a"); p.Bin != "old" {
		t.Fatalf("Get after a failed save = %q, want the old value", p.Bin)
	}
	_ = os.Chmod(dir, 0o700)
	if err := s.Save(config.AgentConfig{Name: "b", Bin: "x"}); err != nil {
		t.Fatal(err)
	}
	re, err := Open(filepath.Join(dir, "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := re.Get("a"); p.Bin != "old" {
		t.Fatalf("the failed save was persisted by the next write: %q", p.Bin)
	}
}

func TestFailedSaveOfANewProfileLeavesNoTrace(t *testing.T) {
	s, _ := readOnlyStore(t)
	if err := s.Save(config.AgentConfig{Name: "new", Bin: "x"}); err == nil {
		t.Fatal("the save into a read-only directory succeeded")
	}
	if _, ok := s.Get("new"); ok {
		t.Fatal("a profile whose save failed is served by Get")
	}
	if len(s.List()) != 1 {
		t.Fatalf("List = %d profiles, want 1", len(s.List()))
	}
}

func TestFailedDeleteKeepsTheProfile(t *testing.T) {
	s, dir := readOnlyStore(t)
	if err := s.Delete("a"); err == nil {
		t.Fatal("the delete in a read-only directory succeeded")
	}
	if p, ok := s.Get("a"); !ok || p.Bin != "old" {
		t.Fatalf("the profile after a failed delete: %+v %v", p, ok)
	}
	re, err := Open(filepath.Join(dir, "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := re.Get("a"); !ok {
		t.Fatal("the file lost the profile")
	}
}
