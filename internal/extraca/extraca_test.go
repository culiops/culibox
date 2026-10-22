package extraca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type certOpts struct {
	cn        string
	org       string
	isCA      bool
	notBefore time.Time
	notAfter  time.Time
}

// newCert makes a self-signed certificate and returns its DER bytes.
func newCert(t *testing.T, o certOpts) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	if o.notBefore.IsZero() {
		o.notBefore = time.Now().Add(-time.Hour)
	}
	if o.notAfter.IsZero() {
		o.notAfter = time.Now().Add(24 * time.Hour)
	}
	subj := pkix.Name{CommonName: o.cn}
	if o.org != "" {
		subj.Organization = []string{o.org}
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               subj,
		NotBefore:             o.notBefore,
		NotAfter:              o.notAfter,
		BasicConstraintsValid: true,
		IsCA:                  o.isCA,
	}
	if o.isCA {
		tmpl.KeyUsage = x509.KeyUsageCertSign
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func newCA(t *testing.T, cn string) []byte {
	t.Helper()
	return newCert(t, certOpts{cn: cn, isCA: true})
}

func block(der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadAcceptsOneAndManyFiles(t *testing.T) {
	dir := t.TempDir()
	a, b, c := newCA(t, "A"), newCA(t, "B"), newCA(t, "C")
	one := writeFile(t, dir, "one.pem", block(a))
	many := writeFile(t, dir, "many.pem", block(b)+block(c))

	s, err := Load([]string{one})
	if err != nil || len(s.CAs) != 1 || s.Empty() {
		t.Fatalf("one file: %v %+v", err, s)
	}
	if s.CAs[0].Subject != "A" {
		t.Errorf("Subject = %q, want A", s.CAs[0].Subject)
	}
	s, err = Load([]string{one, many})
	if err != nil || len(s.CAs) != 3 {
		t.Fatalf("two files: %v %+v", err, s)
	}
}

func TestLoadNilIsEmpty(t *testing.T) {
	s, err := Load(nil)
	if err != nil || !s.Empty() || len(s.CAs) != 0 {
		t.Fatalf("Load(nil) = %+v, %v", s, err)
	}
}

func TestLoadDedupesAndSorts(t *testing.T) {
	dir := t.TempDir()
	var ders [][]byte
	for i := 0; i < 5; i++ {
		ders = append(ders, newCA(t, fmt.Sprintf("ca-%d", i)))
	}
	f1 := writeFile(t, dir, "f1.pem", block(ders[4])+block(ders[1])+block(ders[1]))
	f2 := writeFile(t, dir, "f2.pem", block(ders[0])+block(ders[4])+block(ders[3])+block(ders[2]))
	s, err := Load([]string{f1, f2})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.CAs) != 5 {
		t.Fatalf("got %d CAs, want 5 after de-dup", len(s.CAs))
	}
	for i := 1; i < len(s.CAs); i++ {
		if s.CAs[i-1].SHA256 >= s.CAs[i].SHA256 {
			t.Errorf("not strictly sorted by SHA256 at %d", i)
		}
	}
	for _, ca := range s.CAs {
		if len(ca.SHA256) != 64 || ca.SHA256 != strings.ToLower(ca.SHA256) || strings.Contains(ca.SHA256, ":") {
			t.Errorf("SHA256 %q is not lowercase hex without colons", ca.SHA256)
		}
	}
}

func TestLoadAcceptsFindCertificateOutput(t *testing.T) {
	dir := t.TempDir()
	a, b := newCA(t, "Corp Root"), newCA(t, "Corp Other")
	crlf := func(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }
	content := "keychain: \"/Library/Keychains/System.keychain\"\r\nversion: 512\r\n" +
		crlf(block(a)) +
		"keychain: \"/Users/me/Library/Keychains/login.keychain-db\"\r\nclass: 0x80001000\r\n" +
		crlf(block(b))
	p := writeFile(t, dir, "find.pem", content)
	s, err := Load([]string{p})
	if err != nil || len(s.CAs) != 2 {
		t.Fatalf("Load = %+v, %v", s, err)
	}
}

func TestLoadRefuses(t *testing.T) {
	dir := t.TempDir()

	keyB64 := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("super-secret-key-material-", 6)))
	keyFile := writeFile(t, dir, "key.pem",
		block(newCA(t, "ok"))+"-----BEGIN PRIVATE KEY-----\n"+keyB64+"\n-----END PRIVATE KEY-----\n")

	var many strings.Builder
	for i := 0; i < 33; i++ {
		many.WriteString(block(newCA(t, fmt.Sprintf("many-%d", i))))
	}

	// A system bundle with a non-CA cert / unparseable cert among 33 distinct
	// blocks must still get the size refusal, not a parse error about one cert.
	var bundleNonCA, bundleGarbage strings.Builder
	for i := 0; i < 32; i++ {
		ca := block(newCA(t, fmt.Sprintf("bundle-%d", i)))
		bundleNonCA.WriteString(ca)
		bundleGarbage.WriteString(ca)
	}
	bundleNonCA.WriteString(block(newCert(t, certOpts{cn: "leaf"})))
	bundleGarbage.WriteString(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("garbage, not DER")})))

	oddLabel := "SECRET-LABEL-" + strings.Repeat("x", 60)
	oddFile := writeFile(t, dir, "odd.pem", "-----BEGIN "+oddLabel+"-----\nAAAA\n-----END "+oddLabel+"-----\n")

	subdir := filepath.Join(dir, "adir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		path     string
		contains []string
		omits    []string
	}{
		{name: "relative", path: "certs/root.pem"},
		{name: "missing", path: filepath.Join(dir, "nope.pem")},
		{name: "directory", path: subdir},
		{name: "too big", path: writeFile(t, dir, "big.pem", strings.Repeat("a", 1<<20+1))},
		{name: "no blocks", path: writeFile(t, dir, "empty.pem", "just some words\n")},
		{name: "non-CA cert", path: writeFile(t, dir, "leaf.pem", block(newCert(t, certOpts{cn: "leaf"})))},
		{name: "private key", path: keyFile, contains: []string{"PRIVATE KEY"}, omits: []string{keyB64, "-----BEGIN PRIVATE"}},
		{name: "33 blocks with a non-CA", path: writeFile(t, dir, "b1.pem", bundleNonCA.String()), contains: []string{"32", "export only"}},
		{name: "33 blocks with garbage DER", path: writeFile(t, dir, "b2.pem", bundleGarbage.String()), contains: []string{"32", "export only"}},
		{name: "long odd label is not echoed", path: oddFile, contains: []string{"unrecognized PEM block"}, omits: []string{"SECRET-LABEL", "xxxx"}},
		{name: "33 CAs", path: writeFile(t, dir, "many.pem", many.String()), contains: []string{"32", "export only"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load([]string{tc.path})
			if err == nil {
				t.Fatal("want an error")
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.path) {
				t.Errorf("error %q does not name the path %q", msg, tc.path)
			}
			for _, w := range tc.contains {
				if !strings.Contains(msg, w) {
					t.Errorf("error %q lacks %q", msg, w)
				}
			}
			for _, w := range tc.omits {
				if strings.Contains(msg, w) {
					t.Errorf("error leaks %q", w)
				}
			}
		})
	}
}

func TestLoadAcceptsMoreThan32BlocksThatDedupeTo32(t *testing.T) {
	dir := t.TempDir()
	var sb strings.Builder
	var first string
	for i := 0; i < 32; i++ {
		b := block(newCA(t, fmt.Sprintf("d-%d", i)))
		if i == 0 {
			first = b
		}
		sb.WriteString(b)
	}
	sb.WriteString(first) // 33 blocks, 32 distinct
	p := writeFile(t, dir, "dup.pem", sb.String())
	s, err := Load([]string{p})
	if err != nil || len(s.CAs) != 32 {
		t.Fatalf("Load = %d CAs, %v", len(s.CAs), err)
	}
}

func TestLoadAcceptsSymlinkToFile(t *testing.T) {
	dir := t.TempDir()
	real := writeFile(t, dir, "real.pem", block(newCA(t, "S")))
	link := filepath.Join(dir, "link.pem")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if s, err := Load([]string{link}); err != nil || len(s.CAs) != 1 {
		t.Fatalf("symlink: %+v %v", s, err)
	}
}

func TestLoadAcceptsExactlyMaxSize(t *testing.T) {
	dir := t.TempDir()
	body := block(newCA(t, "pad"))
	p := writeFile(t, dir, "max.pem", body+"#"+strings.Repeat("x", 1<<20-len(body)-1))
	if fi, _ := os.Stat(p); fi.Size() != 1<<20 {
		t.Fatalf("setup: size %d", fi.Size())
	}
	if _, err := Load([]string{p}); err != nil {
		t.Fatalf("1 MiB file must load: %v", err)
	}
}

func TestLoadAcceptsExpiredCA(t *testing.T) {
	dir := t.TempDir()
	der := newCert(t, certOpts{
		cn: "Old", isCA: true,
		notBefore: time.Now().Add(-48 * time.Hour), notAfter: time.Now().Add(-24 * time.Hour),
	})
	p := writeFile(t, dir, "old.pem", block(der))
	s, err := Load([]string{p})
	if err != nil || len(s.CAs) != 1 {
		t.Fatalf("expired CA must still load: %+v %v", s, err)
	}
	if valid, _ := s.CAs[0].Describe(time.Now()); valid {
		t.Error("expired CA reported valid")
	}
}

func TestSubjectFallsBackToFullName(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "o.pem", block(newCert(t, certOpts{org: "Acme", isCA: true})))
	s, err := Load([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.CAs[0].Subject; got != "O=Acme" {
		t.Errorf("Subject = %q, want O=Acme", got)
	}
}

func TestHashEmptyAndStable(t *testing.T) {
	if h := (Set{}).Hash(); h != "" {
		t.Errorf("empty Hash = %q", h)
	}
	dir := t.TempDir()
	a := writeFile(t, dir, "a.pem", block(newCA(t, "A")))
	b := writeFile(t, dir, "b.pem", block(newCA(t, "B")))
	s1, err := Load([]string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := Load([]string{b, a})
	if err != nil {
		t.Fatal(err)
	}
	if s1.Hash() == "" || len(s1.Hash()) != 12 || s1.Hash() != s2.Hash() {
		t.Errorf("hashes %q vs %q", s1.Hash(), s2.Hash())
	}
	s3, _ := Load([]string{a})
	if s3.Hash() == s1.Hash() {
		t.Error("different sets share a hash")
	}
}

func TestPEMRoundTrips(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "in.pem", "junk\n"+block(newCA(t, "A"))+"more junk\r\n"+block(newCA(t, "B")))
	s, err := Load([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	out := writeFile(t, dir, "out.pem", string(s.PEM()))
	s2, err := Load([]string{out})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s, s2) {
		t.Errorf("round trip differs:\n%+v\n%+v", s, s2)
	}
	if n := strings.Count(string(s.PEM()), "-----BEGIN CERTIFICATE-----"); n != 2 {
		t.Errorf("PEM has %d blocks", n)
	}
	if len((Set{}).PEM()) != 0 {
		t.Error("empty Set PEM must be empty")
	}
}

func TestDescribe(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	ca := CA{
		Subject: "Corp Root", SHA256: "ab12",
		NotBefore: now.Add(-time.Hour), NotAfter: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	valid, line := ca.Describe(now)
	if !valid || line != "Corp Root sha256:ab12 expires 2030-01-02" {
		t.Errorf("Describe = %v %q", valid, line)
	}
	ca.NotAfter = now.Add(-time.Second)
	if valid, _ := ca.Describe(now); valid {
		t.Error("expired reported valid")
	}
	ca.NotBefore, ca.NotAfter = now.Add(time.Second), now.Add(time.Hour)
	if valid, _ := ca.Describe(now); valid {
		t.Error("not-yet-valid reported valid")
	}
}

func TestConstants(t *testing.T) {
	if ContainerBundle != "/etc/ssl/certs/ca-certificates.crt" || Label != "io.culiops.agent-box.ca" {
		t.Error("constants changed")
	}
	want := []string{"SSL_CERT_FILE", "CURL_CA_BUNDLE", "REQUESTS_CA_BUNDLE", "AWS_CA_BUNDLE", "GIT_SSL_CAINFO", "NODE_EXTRA_CA_CERTS"}
	if !reflect.DeepEqual(DeniedEnvNames, append(append([]string(nil), want...), "SSL_CERT_DIR", "GIT_SSL_CAPATH")) {
		t.Errorf("DeniedEnvNames = %v", DeniedEnvNames)
	}
	if !reflect.DeepEqual(EnvNames, want) {
		t.Errorf("EnvNames = %v", EnvNames)
	}
}
