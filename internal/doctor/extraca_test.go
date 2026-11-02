package doctor

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
)

func writeCA(t *testing.T, cn string, notBefore, notAfter time.Time) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: notBefore, NotAfter: notAfter,
		BasicConstraintsValid: true, IsCA: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), cn+".pem")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDoctorExtraCALines(t *testing.T) {
	now := time.Now()
	good := writeCA(t, "Corp Root", now.Add(-time.Hour), now.Add(48*time.Hour))
	old := writeCA(t, "Old Root", now.Add(-72*time.Hour), now.Add(-24*time.Hour))

	if rs := checkExtraCA(config.Config{}, now); len(rs) != 0 {
		t.Fatalf("unset must add no rows: %+v", rs)
	}

	rs := checkExtraCA(config.Config{ExtraCACerts: []string{good, old}}, now)
	if len(rs) != 2 {
		t.Fatalf("want a row per CA: %+v", rs)
	}
	byName := map[string]Result{}
	for _, r := range rs {
		if r.Check != "extra-ca" {
			t.Errorf("check name %q", r.Check)
		}
		byName[strings.Fields(r.Detail)[0]+" "+strings.Fields(r.Detail)[1]] = r
	}
	if r := byName["Corp Root"]; r.Status != Pass || !strings.Contains(r.Detail, "sha256:") || !strings.Contains(r.Detail, "expires") {
		t.Errorf("valid CA: %+v", r)
	}
	if r := byName["Old Root"]; r.Status != Warn || !strings.Contains(r.Detail, "expired or not yet valid") {
		t.Errorf("expired CA: %+v", r)
	}
}

func TestDoctorExtraCAUnreadableFails(t *testing.T) {
	rs := checkExtraCA(config.Config{ExtraCACerts: []string{filepath.Join(t.TempDir(), "missing.pem")}}, time.Now())
	if len(rs) != 1 || rs[0].Status != Fail {
		t.Fatalf("a file that cannot load must FAIL: %+v", rs)
	}
}

func TestDoctorChecksIncludeExtraCAAfterConfig(t *testing.T) {
	in := input(t)
	in.Config.ExtraCACerts = []string{writeCA(t, "Corp Root", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))}
	rs := Checks(in)
	for i, r := range rs {
		if r.Check == "config" {
			if i+1 >= len(rs) || rs[i+1].Check != "extra-ca" {
				t.Fatalf("extra-ca must follow the config PASS: %+v", rs[i+1:])
			}
			return
		}
	}
	t.Fatal("no config row")
}
