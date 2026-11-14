package auth

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/datacompat"
)

func TestResolveCred(t *testing.T) {
	t.Setenv("AB_PASS", "s3cr3t")
	p, err := ResolveCred(config.Config{PasswordEnv: "AB_PASS"})
	if err != nil || p != "s3cr3t" {
		t.Fatalf("resolve: %v %q", err, p)
	}
	if _, err := ResolveCred(config.Config{PasswordEnv: "MISSING_VAR_XYZ"}); err == nil {
		t.Fatal("missing env should error")
	}
	if _, err := ResolveCred(config.Config{}); err == nil {
		t.Fatal("empty password_env should error")
	}
}

// A secret persisted before a daemon restart must load identically after it: the
// session cookie and WS route both sign with this secret, so a fresh per-process
// secret would 401 every live cookie. A persisted secret (same path across
// "processes") fixes it; a different path yields a different secret.
func TestCookieAuthPersistedSecretSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookie.secret")

	ca1, err := NewCookieAuth(path) // first process
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil {
		t.Fatalf("secret not persisted: %v", err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Fatalf("secret file perms = %o, want 600", fi.Mode().Perm())
	}

	ca2, err := NewCookieAuth(path) // simulated restart: same path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ca1.Secret(), ca2.Secret()) {
		t.Fatal("secret changed across restart despite same path")
	}

	other, err := NewCookieAuth(filepath.Join(t.TempDir(), "cookie.secret"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ca1.Secret(), other.Secret()) {
		t.Fatal("a different path must yield a different secret")
	}
}

// An empty secret file (a torn first write) holds nothing to lose, so it is
// minted into a working 32-byte secret rather than bricking startup.
func TestCookieAuthEmptySecretMinted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookie.secret")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ca, err := NewCookieAuth(path)
	if err != nil {
		t.Fatalf("an empty secret file should be minted, got error: %v", err)
	}
	if b, err := os.ReadFile(path); err != nil || len(b) != 32 {
		t.Fatalf("secret not minted to 32 bytes: len=%d err=%v", len(b), err)
	}
	if len(ca.Secret()) != 32 {
		t.Fatalf("in-memory secret len=%d, want 32", len(ca.Secret()))
	}
}

// A secret of any other length may be a newer CuliBox's format: refuse it and
// leave it on disk.
func TestCookieAuthWrongLengthRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookie.secret")
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewCookieAuth(path)
	if !errors.Is(err, datacompat.ErrNewer) {
		t.Fatalf("want ErrNewer, got %v", err)
	}
	for _, want := range []string{path, "5 bytes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if b, _ := os.ReadFile(path); string(b) != "short" {
		t.Fatalf("a refused secret must stay untouched, file now %q", b)
	}
}
