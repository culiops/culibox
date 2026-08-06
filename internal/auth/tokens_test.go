package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTokenStore_CreateHashesAndRevealsOnce(t *testing.T) {
	s, err := OpenTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	plain, info, err := s.Create("macbook")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, "abx_") {
		t.Fatalf("plaintext %q missing abx_ prefix", plain)
	}
	if info.Name != "macbook" || info.ID == "" {
		t.Fatalf("bad info: %+v", info)
	}
	for _, li := range s.List() {
		if strings.Contains(li.Name, plain) || li.ID == plain {
			t.Fatal("plaintext leaked into listing")
		}
	}
}

func TestTokenStore_VerifyAndRevoke(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	s, _ := OpenTokenStore(path)
	plain, info, _ := s.Create("cli")

	got, ok := s.Verify(plain)
	if !ok || got.ID != info.ID {
		t.Fatalf("verify live token: ok=%v id=%v", ok, got.ID)
	}
	if _, ok := s.Verify("abx_nonsense"); ok {
		t.Fatal("unknown token verified")
	}
	if err := s.Revoke(info.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Verify(plain); ok {
		t.Fatal("revoked token still verifies")
	}
	if err := s.Revoke("nope"); err != ErrTokenNotFound {
		t.Fatalf("revoke unknown: got %v", err)
	}
}

func TestTokenStore_PersistsHashOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	s, _ := OpenTokenStore(path)
	plain, _, _ := s.Create("x")

	reopened, err := OpenTokenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Verify(plain); !ok {
		t.Fatal("token did not survive reopen")
	}
	raw := readFile(t, path)
	if strings.Contains(raw, plain) {
		t.Fatal("plaintext token written to disk")
	}
}
