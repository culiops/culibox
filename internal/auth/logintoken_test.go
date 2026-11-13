package auth

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/datacompat"
)

var tokenShape = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func TestLoginToken_CreateAndReuse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d", LoginTokenFile)
	a, err := LoadOrCreateLoginToken(path)
	if err != nil || !tokenShape.MatchString(a) {
		t.Fatalf("token %q err %v", a, err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("token file mode %o, want 600", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Dir(path))
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %o, want 700", di.Mode().Perm())
	}
	b, _ := LoadOrCreateLoginToken(path)
	if a != b {
		t.Errorf("token changed across loads")
	}
}

// Review focus #3.
func TestLoginToken_HandEditedWhitespaceKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), LoginTokenFile)
	tok, _ := LoadOrCreateLoginToken(path)
	for _, body := range []string{tok + "\r\n", "  " + tok + "  \n", tok} {
		_ = os.WriteFile(path, []byte(body), 0o600)
		got, err := LoadOrCreateLoginToken(path)
		if err != nil || got != tok {
			t.Errorf("body %q: got %q,%v want the same token", body, got, err)
		}
	}
}

func TestLoginToken_BlankMinted(t *testing.T) {
	path := filepath.Join(t.TempDir(), LoginTokenFile)
	_ = os.WriteFile(path, []byte("  \n"), 0o600)
	got, err := LoadOrCreateLoginToken(path)
	if err != nil || !tokenShape.MatchString(got) {
		t.Fatalf("blank file: got %q,%v", got, err)
	}
}

func TestLoginToken_MalformedRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), LoginTokenFile)
	_ = os.WriteFile(path, []byte("short"), 0o600)
	_, err := LoadOrCreateLoginToken(path)
	if !errors.Is(err, datacompat.ErrNewer) || !strings.Contains(err.Error(), "password_env") {
		t.Fatalf("want ErrNewer naming password_env, got %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "short" {
		t.Fatalf("a refused token must stay untouched, file now %q", b)
	}
}

func TestLoginToken_UnreadableIsFatal(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads anything")
	}
	path := filepath.Join(t.TempDir(), LoginTokenFile)
	_ = os.WriteFile(path, []byte("x"), 0o000)
	if _, err := LoadOrCreateLoginToken(path); err == nil {
		t.Fatal("an unreadable token must be an error, never silently replaced")
	}
}

func TestResolveLogin(t *testing.T) {
	dir := t.TempDir()
	l, err := ResolveLogin(config.Config{}, dir)
	if err != nil || l.Mode != ModeToken || l.TokenPath != filepath.Join(dir, LoginTokenFile) || l.Secret == "" {
		t.Fatalf("token mode: %+v %v", l, err)
	}
	t.Setenv("AB_TEST_PASS", "correct horse battery")
	l, err = ResolveLogin(config.Config{PasswordEnv: "AB_TEST_PASS"}, dir)
	if err != nil || l.Mode != ModePassword || l.Secret != "correct horse battery" {
		t.Fatalf("password mode: %+v %v", l, err)
	}
	t.Setenv("AB_TEST_PASS", "")
	if _, err := ResolveLogin(config.Config{PasswordEnv: "AB_TEST_PASS"}, dir); err == nil {
		t.Fatal("password_env set but empty must stay fatal, never fall into token mode")
	}
}

func TestWeakPasswordAndURL(t *testing.T) {
	if !WeakPassword("admin") || WeakPassword("twelve-chars") {
		t.Error("threshold is 12 characters")
	}
	if got := LoginURL("127.0.0.1:8899", "tok"); got != "http://127.0.0.1:8899/login#token=tok" {
		t.Errorf("LoginURL = %q", got)
	}
}
