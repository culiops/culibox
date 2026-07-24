package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culiops-agent-console/internal/config"
)

func TestEnvSource(t *testing.T) {
	t.Setenv("AC_TEST_SECRET", "s3cr3t")
	r, err := New([]config.SecretSource{{Type: "env"}})
	if err != nil {
		t.Fatal(err)
	}
	v, ok, err := r.Resolve("AC_TEST_SECRET")
	if err != nil || !ok || v != "s3cr3t" {
		t.Fatalf("env resolve: v=%q ok=%v err=%v", v, ok, err)
	}
	if _, ok, _ := r.Resolve("AC_MISSING_XYZ"); ok {
		t.Fatal("missing should be ok=false")
	}
}

func TestFileSource_OwnerOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.env")
	body := "# comment\n\nexport TOK=abc123\nPLAIN=zzz\nQUOTED=\"q u o t e d\"\nEQ=a=b=c\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := New([]config.SecretSource{{Type: "file", Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok, err := r.Resolve("TOK"); err != nil || !ok || v != "abc123" { // export prefix stripped
		t.Fatalf("TOK: v=%q ok=%v err=%v", v, ok, err)
	}
	if v, ok, _ := r.Resolve("PLAIN"); !ok || v != "zzz" {
		t.Fatalf("PLAIN: v=%q ok=%v", v, ok)
	}
	if v, ok, _ := r.Resolve("QUOTED"); !ok || v != "q u o t e d" { // surrounding quotes stripped
		t.Fatalf("QUOTED: v=%q ok=%v", v, ok)
	}
	if v, ok, _ := r.Resolve("EQ"); !ok || v != "a=b=c" { // split on FIRST '=' only
		t.Fatalf("EQ: v=%q ok=%v", v, ok)
	}
}

func TestFileSource_RejectsPermissiveFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.env")
	if err := os.WriteFile(path, []byte("TOK=abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _ := New([]config.SecretSource{{Type: "file", Path: path}})
	_, _, err := r.Resolve("TOK")
	if err == nil {
		t.Fatal("group/other-readable secrets file must be rejected")
	}
	if strings.Contains(err.Error(), "abc") {
		t.Fatal("error must not contain the secret value")
	}
}

func TestComposite_Order(t *testing.T) {
	t.Setenv("BOTH", "from_env")
	dir := t.TempDir()
	path := filepath.Join(dir, "s.env")
	os.WriteFile(path, []byte("BOTH=from_file\nONLY_FILE=f"), 0o600)
	r, _ := New([]config.SecretSource{{Type: "env"}, {Type: "file", Path: path}})
	if v, _, _ := r.Resolve("BOTH"); v != "from_env" {
		t.Fatalf("env should win first: %q", v)
	}
	if v, ok, _ := r.Resolve("ONLY_FILE"); !ok || v != "f" {
		t.Fatalf("file fallback: v=%q ok=%v", v, ok)
	}
}

func TestNew_UnknownSourceType(t *testing.T) {
	if _, err := New([]config.SecretSource{{Type: "vault"}}); err == nil {
		t.Fatal("unknown source type should error")
	}
}
