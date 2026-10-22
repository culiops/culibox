package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	boximage "github.com/culiops/agent-box/docker/agent-box"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/extraca"
)

// writeTestCA writes a self-signed CA valid from notBefore to notAfter and
// returns its path.
func writeTestCA(t *testing.T, cn string, notBefore, notAfter time.Time) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		BasicConstraintsValid: true,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func twoCASet() extraca.Set {
	return extraca.Set{CAs: []extraca.CA{
		{Subject: "Corp Root", SHA256: "aa11", NotBefore: setupNow.Add(-time.Hour), NotAfter: setupNow.Add(24 * time.Hour)},
		{Subject: "Old Root", SHA256: "bb22", NotBefore: setupNow.Add(-48 * time.Hour), NotAfter: setupNow.Add(-24 * time.Hour)},
	}}
}

func TestPrintExtraCA(t *testing.T) {
	var w bytes.Buffer
	printExtraCA(&w, twoCASet(), setupNow)
	lines := strings.Split(strings.TrimSpace(w.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want one line per CA:\n%s", w.String())
	}
	if lines[0] != "PASS extra-ca: Corp Root sha256:aa11 expires 2026-10-19" {
		t.Errorf("valid CA line: %q", lines[0])
	}
	if lines[1] != "WARN extra-ca: Old Root sha256:bb22 expires 2026-10-17 (expired or not yet valid)" {
		t.Errorf("expired CA line: %q", lines[1])
	}
	w.Reset()
	printExtraCA(&w, extraca.Set{}, setupNow)
	if w.Len() != 0 {
		t.Errorf("empty set must print nothing: %q", w.String())
	}
}

func fakeRoot(subject, cn string, err error) func(context.Context, string) (string, string, error) {
	return func(context.Context, string) (string, string, error) { return subject, cn, err }
}

const curl60 = "#8 12.3 curl: (60) SSL certificate problem: self-signed certificate in certificate chain\n" +
	"#8 12.3 More details here: https://curl.se/docs/sslcerts.html\n"
const curl60Host = "#8 12.0 Downloading https://releases.hashicorp.com/terraform/1.9/terraform.zip\n" + curl60

func TestHintSilentOnOtherErrors(t *testing.T) {
	var w bytes.Buffer
	called := false
	root := func(context.Context, string) (string, string, error) { called = true; return "", "", nil }
	printInterceptionHint(context.Background(), &w, "docker build: exit status 1\nE: Unable to locate package foo", false, root)
	if w.Len() != 0 || called {
		t.Fatalf("hint on an unrelated error: %q (root called: %v)", w.String(), called)
	}
}

func TestHintWithRootNamesExportCommand(t *testing.T) {
	var w bytes.Buffer
	printInterceptionHint(context.Background(), &w, curl60Host, false,
		fakeRoot("CN=Corp Root,O=Corp", "Corp Root", nil))
	out := w.String()
	for _, want := range []string{
		"HINT: HTTPS inside the box VM/build is being intercepted by a TLS-inspecting proxy.",
		`"extra_ca_certs"`,
		"releases.hashicorp.com:443",
		"CN=Corp Root,O=Corp",
		`macOS export: security find-certificate -c "Corp Root" -p > ~/agent-box-ca.pem`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	rerun := "-rebuild"
	if runtime.GOOS == "darwin" {
		rerun = "-recreate"
	}
	if !strings.Contains(out, "`agent-box box setup "+rerun+"`") {
		t.Errorf("want the %s command for %s:\n%s", rerun, runtime.GOOS, out)
	}
	if strings.Contains(out, "did not cover") {
		t.Errorf("no CA is configured, so nothing can be 'not covered':\n%s", out)
	}
}

func TestHintWithRootButNoCNOmitsExportLine(t *testing.T) {
	var w bytes.Buffer
	printInterceptionHint(context.Background(), &w, curl60Host, false, fakeRoot("O=Corp", "", nil))
	out := w.String()
	if !strings.Contains(out, "O=Corp") || strings.Contains(out, "macOS export") {
		t.Fatalf("chain line without export line expected:\n%s", out)
	}
}

func TestHintWithoutHostOmitsChainLines(t *testing.T) {
	for name, tc := range map[string]struct {
		out  string
		root func(context.Context, string) (string, string, error)
	}{
		"no host in output": {curl60, fakeRoot("CN=X", "X", nil)},
		"dial fails":        {curl60Host, fakeRoot("", "", errors.New("dial tcp: i/o timeout"))},
	} {
		var w bytes.Buffer
		printInterceptionHint(context.Background(), &w, tc.out, false, tc.root)
		out := w.String()
		if !strings.Contains(out, "HINT:") || !strings.Contains(out, "extra_ca_certs") {
			t.Errorf("%s: the hint itself must still print:\n%s", name, out)
		}
		if strings.Contains(out, "chain") || strings.Contains(out, "macOS export") {
			t.Errorf("%s: no chain or export line without a root:\n%s", name, out)
		}
	}
}

func TestHintWhenConfiguredSaysNotCovered(t *testing.T) {
	var w bytes.Buffer
	printInterceptionHint(context.Background(), &w, curl60Host, true, fakeRoot("CN=Other Root", "Other Root", nil))
	out := w.String()
	for _, want := range []string{"configured CAs did not cover this chain", "CN=Other Root", `-c "Other Root"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestBuildFailureHintUsesTail(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "docker")
	script := "#!/bin/sh\ncase \"$*\" in\n*version*) echo arm64; exit 0;;\n\"build \"*) cat >/dev/null; echo 'fetching https://example.test/x'; echo 'curl: (60) SSL certificate problem' >&2; exit 1;;\n*) echo 'Error: No such image' >&2; exit 1;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var asked string
	old := hintRoot
	hintRoot = func(_ context.Context, hp string) (string, string, error) { asked = hp; return "CN=R", "R", nil }
	defer func() { hintRoot = old }()
	var w bytes.Buffer
	d := boximage.Docker{Bin: bin, ExtraCA: []byte("pem")}
	if rc := boxSetupImage(context.Background(), d, baseRef, false, setupNow, &w); rc != 1 {
		t.Fatalf("rc %d: %s", rc, w.String())
	}
	if asked != "example.test:443" {
		t.Errorf("dialled %q", asked)
	}
	if !strings.Contains(w.String(), "FAIL image:") || !strings.Contains(w.String(), "configured CAs did not cover this chain") {
		t.Errorf("output:\n%s", w.String())
	}
}

func TestPullFailureHint(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "docker")
	script := "#!/bin/sh\ncase \"$*\" in\n*version*) echo arm64; exit 0;;\npull*) echo 'Get \"https://ghcr.io/v2/\": tls: failed to verify certificate: x509: certificate signed by unknown authority' >&2; exit 1;;\n*) echo 'Error: No such image' >&2; exit 1;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := hintRoot
	hintRoot = fakeRoot("CN=R", "R", nil)
	defer func() { hintRoot = old }()
	var w bytes.Buffer
	if rc := boxSetupImage(context.Background(), boximage.Docker{Bin: bin}, remoteRef, false, setupNow, &w); rc != 1 {
		t.Fatalf("rc %d: %s", rc, w.String())
	}
	if !strings.Contains(w.String(), "HINT:") || !strings.Contains(w.String(), "ghcr.io:443") {
		t.Errorf("output:\n%s", w.String())
	}
}

func TestOptionsPassExtraCA(t *testing.T) {
	p := writeTestCA(t, "Corp Root", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	cfg := config.Config{DockerBin: "/bin/sh", ExtraCACerts: []string{p}}
	l, err := newBoxLayer(cfg, t.TempDir(), t.TempDir(), "linux", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	o := l.options(cfg)
	if o.ExtraCA.Empty() || o.ExtraCAErr != nil {
		t.Fatalf("options: %+v / %v", o.ExtraCA, o.ExtraCAErr)
	}
	if string(l.img.ExtraCA) != string(o.ExtraCA.PEM()) || len(l.img.ExtraCA) == 0 {
		t.Errorf("the image builder must get the same bundle")
	}
}

func TestOptionsExtraCALoadErrorFailsClosed(t *testing.T) {
	cfg := config.Config{DockerBin: "/bin/sh", ExtraCACerts: []string{filepath.Join(t.TempDir(), "missing.pem")}}
	l, err := newBoxLayer(cfg, t.TempDir(), t.TempDir(), "linux", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	o := l.options(cfg)
	if o.ExtraCAErr == nil || !o.ExtraCA.Empty() {
		t.Fatalf("a bad CA file must set ExtraCAErr and leave the set empty: %+v / %v", o.ExtraCA, o.ExtraCAErr)
	}
	if l.img.ExtraCA != nil {
		t.Errorf("image builder must not get a bundle")
	}
}
