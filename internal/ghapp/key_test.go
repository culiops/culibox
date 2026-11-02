package ghapp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/config"
)

func genKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func writeKey(t *testing.T, dir, name string, mode os.FileMode, pkcs8 bool, k *rsa.PrivateKey) string {
	t.Helper()
	var blk *pem.Block
	if pkcs8 {
		der, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			t.Fatal(err)
		}
		blk = &pem.Block{Type: "PRIVATE KEY", Bytes: der}
	} else {
		blk = &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, pem.EncodeToMemory(blk), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadKeyHappyPath(t *testing.T) {
	k := genKey(t)
	for _, pkcs8 := range []bool{false, true} {
		dir := t.TempDir()
		p := writeKey(t, dir, "k.pem", 0o600, pkcs8, k)
		got, err := LoadKey(config.KeyRef{Type: "file", Path: p})
		if err != nil {
			t.Fatalf("pkcs8=%v: %v", pkcs8, err)
		}
		if !got.Equal(k) {
			t.Fatalf("pkcs8=%v: key mismatch", pkcs8)
		}
	}
}

func TestLoadKeyRefusals(t *testing.T) {
	k := genKey(t)
	dir := t.TempDir()

	loose := writeKey(t, dir, "loose.pem", 0o644, false, k)
	_, err := LoadKey(config.KeyRef{Type: "file", Path: loose})
	if err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("mode 0644: want mode error, got %v", err)
	}

	good := writeKey(t, dir, "good.pem", 0o600, false, k)
	link := filepath.Join(dir, "link.pem")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKey(config.KeyRef{Type: "file", Path: link}); err == nil {
		t.Fatal("symlink: want error")
	}

	junk := filepath.Join(dir, "junk.pem")
	if err := os.WriteFile(junk, []byte("-----BEGIN NOTHING-----\nnot pem\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = LoadKey(config.KeyRef{Type: "file", Path: junk})
	if err == nil {
		t.Fatal("not a PEM: want error")
	}
	if strings.Contains(err.Error(), "BEGIN") {
		t.Fatalf("error leaks key bytes: %v", err)
	}

	if _, err := LoadKey(config.KeyRef{Type: "file", Path: filepath.Join(dir, "missing.pem")}); err == nil {
		t.Fatal("missing: want error")
	}
	if _, err := LoadKey(config.KeyRef{Type: "env", Path: good}); err == nil {
		t.Fatal("non-file type: want error")
	}
	if _, err := LoadKey(config.KeyRef{Type: "file", Path: "relative.pem"}); err == nil {
		t.Fatal("relative path: want error")
	}
}
