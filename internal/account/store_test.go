package account

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	data := t.TempDir()
	def := filepath.Join(t.TempDir(), ".claude")
	if err := os.MkdirAll(def, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(data, "accounts.json"), data, def)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, data
}

func TestStoreDefaultIsImplicit(t *testing.T) {
	s, _ := newStore(t)
	l := s.List()
	if len(l) != 1 || !l[0].IsDefault || l[0].Name != Default {
		t.Fatalf("List = %+v, want only default", l)
	}
	for _, n := range []string{"", Default} {
		r, err := s.Resolve(n)
		if err != nil || !r.IsDefault {
			t.Fatalf("Resolve(%q) = %+v, %v", n, r, err)
		}
	}
	if _, err := s.Resolve("nope"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("Resolve(nope) err = %v, want ErrUnknown", err)
	}
}

func TestStoreCreatePersistsAndDerivesDir(t *testing.T) {
	s, data := newStore(t)
	r, err := s.Create(Account{Name: "work", Provider: ProviderClaude})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(data, "accounts", "work", "claude")
	if r.ConfigDir != want {
		t.Fatalf("ConfigDir = %q, want %q", r.ConfigDir, want)
	}
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("config dir not created 0700: %v %v", fi, err)
	}
	if _, err := s.Create(Account{Name: "work", Provider: ProviderClaude}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate create err = %v", err)
	}
	// Reopen: persisted, default still first.
	s2, err := Open(filepath.Join(data, "accounts.json"), data, s.defaultDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	l := s2.List()
	if len(l) != 2 || l[0].Name != Default || l[1].Name != "work" {
		t.Fatalf("reopened List = %+v", l)
	}
	b, _ := os.ReadFile(filepath.Join(data, "accounts.json"))
	if strings.Contains(string(b), `"name": "default"`) {
		t.Fatalf("accounts.json must not store default: %s", b)
	}
	if fi, err := os.Stat(filepath.Join(data, "accounts.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("accounts.json mode: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(data, "accounts.json.tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tmp file left behind: %v", err)
	}
}

func TestStoreUpdatePersists(t *testing.T) {
	s, data := newStore(t)
	if _, err := s.Create(Account{Name: "work", Provider: ProviderClaude}); err != nil {
		t.Fatal(err)
	}
	r, err := s.Update("work", "WORK_TOK", true)
	if err != nil || r.TokenSecret != "WORK_TOK" || !r.InheritPersonal {
		t.Fatalf("Update = %+v, %v", r, err)
	}
	if _, err := s.Update("work", "BAD NAME", false); err == nil {
		t.Fatal("bad secret name accepted on update")
	}
	if _, err := s.Update("nope", "", false); !errors.Is(err, ErrUnknown) {
		t.Fatalf("Update(nope) err = %v", err)
	}
	s2, err := Open(filepath.Join(data, "accounts.json"), data, s.defaultDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.Resolve("work")
	if err != nil || got.TokenSecret != "WORK_TOK" || !got.InheritPersonal {
		t.Fatalf("reopened = %+v, %v", got, err)
	}
}

func TestStoreRejectsBadInput(t *testing.T) {
	s, _ := newStore(t)
	for _, a := range []Account{{Name: "../x", Provider: "claude"}, {Name: "default", Provider: "claude"}, {Name: "x", Provider: "y"}} {
		if _, err := s.Create(a); err == nil {
			t.Errorf("Create(%+v) accepted", a)
		}
	}
	if _, err := s.Update(Default, "", true); err == nil {
		t.Error("default must not be editable")
	}
	if _, err := s.Delete(Default); err == nil {
		t.Error("default must not be deletable")
	}
}

func TestStoreDeleteLeavesDir(t *testing.T) {
	s, data := newStore(t)
	if _, err := s.Create(Account{Name: "work", Provider: ProviderClaude}); err != nil {
		t.Fatal(err)
	}
	root, err := s.Delete("work")
	if err != nil {
		t.Fatal(err)
	}
	if root != filepath.Join(data, "accounts", "work") {
		t.Fatalf("root = %q", root)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal("delete must leave the dir on disk")
	}
	if _, err := s.Resolve("work"); !errors.Is(err, ErrUnknown) {
		t.Fatal("deleted account still resolves")
	}
	// Re-create reuses the dir (idempotent, never clobbers).
	if _, err := s.Create(Account{Name: "work", Provider: ProviderClaude}); err != nil {
		t.Fatalf("re-create: %v", err)
	}
}

// A symlink planted at accounts/ must not redirect provisioning outside dataDir.
func TestStoreProvisionRefusesEscapingSymlink(t *testing.T) {
	s, data := newStore(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(data, "accounts")); err != nil {
		t.Fatal(err)
	}
	// accounts/ is a symlink to a dir outside dataDir: root-relative resolution
	// refuses to follow it, so nothing may land in outside.
	_, err := s.Create(Account{Name: "work", Provider: ProviderClaude})
	if err == nil {
		t.Fatal("Create followed an escaping symlink")
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("escaped write landed in %s: %v", outside, ents)
	}
	if _, rerr := s.Resolve("work"); !errors.Is(rerr, ErrUnknown) {
		t.Fatal("failed create must not register the account")
	}
}
