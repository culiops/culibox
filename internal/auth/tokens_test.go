package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/datacompat"
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

// A token field this build does not know (a future scope or expiry) must not be
// dropped: ignoring it would widen the token to full access (#229 part C).
func TestOpenTokenStoreUnknownFieldRefused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokens.json")
	body := `[{"id":"a","name":"n","created_at":1,"hash":"h","scope":"read"}]`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := OpenTokenStore(p)
	if !errors.Is(err, datacompat.ErrNewer) {
		t.Fatalf("err = %v, want ErrNewer", err)
	}
	if !strings.Contains(err.Error(), p) {
		t.Errorf("error %q does not name %s", err, p)
	}
	if got := readFile(t, p); got != body {
		t.Errorf("tokens.json changed: %q", got)
	}
}

func TestOpenTokenStoreObjectRefused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokens.json")
	const body = `{"version":2,"tokens":[]}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenTokenStore(p); !errors.Is(err, datacompat.ErrNewer) {
		t.Fatalf("err = %v, want ErrNewer", err)
	}
	if got := readFile(t, p); got != body {
		t.Errorf("tokens.json changed: %q", got)
	}
}

func TestOpenTokenStoreTrailingDataRefused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokens.json")
	const body = `[{"id":"a","name":"n","created_at":1,"hash":"h"}]x`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenTokenStore(p); !errors.Is(err, datacompat.ErrNewer) {
		t.Fatalf("err = %v, want ErrNewer", err)
	}
	if got := readFile(t, p); got != body {
		t.Errorf("tokens.json changed: %q", got)
	}
}

func TestOpenTokenStoreNullRecordRefused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokens.json")
	const body = `[null]`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenTokenStore(p); !errors.Is(err, datacompat.ErrNewer) {
		t.Fatalf("err = %v, want ErrNewer", err)
	}
	if got := readFile(t, p); got != body {
		t.Errorf("tokens.json changed: %q", got)
	}
}
