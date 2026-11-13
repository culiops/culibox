package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	boximage "github.com/culiops/culibox/docker/agent-box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/extraca"
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
	printInterceptionHint(context.Background(), &w, "docker build: exit status 1\nE: Unable to locate package foo", false, "linux", false, root)
	if w.Len() != 0 || called {
		t.Fatalf("hint on an unrelated error: %q (root called: %v)", w.String(), called)
	}
}

func TestHintWithRootNamesExportCommand(t *testing.T) {
	var w bytes.Buffer
	printInterceptionHint(context.Background(), &w, curl60Host, false, "darwin", false,
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
	if !strings.Contains(out, "`agent-box box setup -recreate`") {
		t.Errorf("darwin wants -recreate:\n%s", out)
	}
	if strings.Contains(out, "did not cover") {
		t.Errorf("no CA is configured, so nothing can be 'not covered':\n%s", out)
	}
}

func TestHintLinuxBuildNamesRebuildAndNoMacExport(t *testing.T) {
	var w bytes.Buffer
	printInterceptionHint(context.Background(), &w, curl60Host, false, "linux", false, fakeRoot("CN=R", "R", nil))
	out := w.String()
	if !strings.Contains(out, "`agent-box box setup -rebuild`") || strings.Contains(out, "-recreate") {
		t.Errorf("linux build wants -rebuild only:\n%s", out)
	}
	if !strings.Contains(out, "CN=R") || strings.Contains(out, "security find-certificate") {
		t.Errorf("linux keeps the chain line but not the macOS export line:\n%s", out)
	}
}

func TestHintWithRootButNoCNOmitsExportLine(t *testing.T) {
	var w bytes.Buffer
	printInterceptionHint(context.Background(), &w, curl60Host, false, "darwin", false, fakeRoot("O=Corp", "", nil))
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
		printInterceptionHint(context.Background(), &w, tc.out, false, "linux", false, tc.root)
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
	printInterceptionHint(context.Background(), &w, curl60Host, true, "darwin", false, fakeRoot("CN=Other Root", "Other Root", nil))
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
	if rc := boxSetupImage(context.Background(), d, baseRef, false, setupNow, "linux", &w); rc != 1 {
		t.Fatalf("rc %d: %s", rc, w.String())
	}
	if asked != "example.test:443" {
		t.Errorf("dialled %q", asked)
	}
	if !strings.Contains(w.String(), "FAIL image:") || !strings.Contains(w.String(), "configured CAs did not cover this chain") {
		t.Errorf("output:\n%s", w.String())
	}
}

func TestHintDarwinWordingNamesRecreate(t *testing.T) {
	for _, pull := range []bool{false, true} {
		var w bytes.Buffer
		printInterceptionHint(context.Background(), &w, curl60Host, false, "darwin", pull, fakeRoot("CN=R", "R", nil))
		out := w.String()
		if !strings.Contains(out, "`agent-box box setup -recreate`") || strings.Contains(out, "-rebuild") {
			t.Errorf("darwin (pull=%v) wants -recreate only:\n%s", pull, out)
		}
		if !strings.Contains(out, `"extra_ca_certs"`) || !strings.Contains(out, "security find-certificate") {
			t.Errorf("darwin (pull=%v) keeps the extra_ca_certs hint:\n%s", pull, out)
		}
	}
}

func TestHintLinuxPullPointsAtHostTrust(t *testing.T) {
	for _, configured := range []bool{false, true} {
		var w bytes.Buffer
		printInterceptionHint(context.Background(), &w, curl60Host, configured, "linux", true, fakeRoot("CN=Corp Root,O=Corp", "Corp Root", nil))
		out := w.String()
		for _, want := range []string{"host's docker daemon", "/usr/local/share/ca-certificates", "update-ca-certificates",
			"restart docker", "releases.hashicorp.com:443", "CN=Corp Root,O=Corp"} {
			if !strings.Contains(out, want) {
				t.Errorf("configured=%v: missing %q:\n%s", configured, want, out)
			}
		}
		for _, bad := range []string{"extra_ca_certs", "-rebuild", "-recreate", "security find-certificate", "did not cover"} {
			if strings.Contains(out, bad) {
				t.Errorf("configured=%v: linux pull hint must not mention %q:\n%s", configured, bad, out)
			}
		}
	}
}

func pullFailDocker(t *testing.T) boximage.Docker {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\ncase \"$*\" in\n*version*) echo arm64; exit 0;;\npull*) echo 'Get \"https://ghcr.io/v2/\": tls: failed to verify certificate: x509: certificate signed by unknown authority' >&2; exit 1;;\n*) echo 'Error: No such image' >&2; exit 1;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return boximage.Docker{Bin: bin, ExtraCA: []byte("pem")}
}

func TestPullFailureHintPerPlatform(t *testing.T) {
	old := hintRoot
	hintRoot = fakeRoot("CN=R", "R", nil)
	defer func() { hintRoot = old }()
	for _, tc := range []struct {
		goos      string
		want, not []string
	}{
		{"linux", []string{"HINT:", "ghcr.io:443", "host's docker daemon", "update-ca-certificates"},
			[]string{"extra_ca_certs", "-rebuild", "-recreate", "did not cover"}},
		{"darwin", []string{"HINT:", "ghcr.io:443", `"extra_ca_certs"`, "`agent-box box setup -recreate`", "configured CAs did not cover this chain"},
			[]string{"-rebuild", "update-ca-certificates"}},
	} {
		var w bytes.Buffer
		if rc := boxSetupImage(context.Background(), pullFailDocker(t), remoteRef, false, setupNow, tc.goos, &w); rc != 1 {
			t.Fatalf("%s: rc %d: %s", tc.goos, rc, w.String())
		}
		for _, s := range tc.want {
			if !strings.Contains(w.String(), s) {
				t.Errorf("%s: missing %q:\n%s", tc.goos, s, w.String())
			}
		}
		// The FAIL line carries docker's own output; look only at the hint.
		hint := w.String()[strings.Index(w.String(), "HINT:"):]
		for _, s := range tc.not {
			if strings.Contains(hint, s) {
				t.Errorf("%s: hint must not contain %q:\n%s", tc.goos, s, hint)
			}
		}
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

// sampleConfigFile pulls the JSON object out of the hint output: the lines that
// follow "config.json:" up to the closing brace, indentation stripped.
func sampleConfigFile(t *testing.T, out string) []byte {
	t.Helper()
	_, rest, ok := strings.Cut(out, "\n      {\n")
	if !ok {
		t.Fatalf("no JSON sample in:\n%s", out)
	}
	body, _, ok := strings.Cut(rest, "\n      }\n")
	if !ok {
		t.Fatalf("unterminated JSON sample in:\n%s", out)
	}
	var b strings.Builder
	b.WriteString("{\n")
	for _, l := range strings.Split(body, "\n") {
		b.WriteString(strings.TrimPrefix(l, "      "))
		b.WriteString("\n")
	}
	b.WriteString("}")
	return []byte(b.String())
}

// With no config file, the hint names the file to create and shows a minimal
// valid one: a config file replaces the built-in defaults, so extra_ca_certs
// alone is rejected (no agents, no bind_addr). The sample comes from
// config.Defaults(), so the test checks it validates rather than its text.
func TestHintWithoutConfigFileShowsPathAndValidSample(t *testing.T) {
	old := hintConfigPath
	hintConfigPath = "/home/u/.local/share/agent-box/config.json"
	defer func() { hintConfigPath = old }()
	var w bytes.Buffer
	printInterceptionHint(context.Background(), &w, curl60Host, false, "darwin", false, fakeRoot("CN=R", "R", nil))
	out := w.String()
	for _, want := range []string{"No config file exists", hintConfigPath, "replaces the built-in defaults"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	raw := sampleConfigFile(t, out)
	// Key order is part of the contract: what to change comes last.
	last := -1
	for _, k := range []string{`"bind_addr"`, `"default_runtime"`, `"agents"`, `"extra_ca_certs"`} {
		i := bytes.Index(raw, []byte(k))
		if i < 0 || i < last {
			t.Fatalf("key %s missing or out of order in:\n%s", k, raw)
		}
		last = i
	}
	// The sample's CA path is a placeholder; point it at a real CA, as the user
	// does, and the whole file must load and validate.
	ca := writeTestCA(t, "Corp Root", setupNow.Add(-time.Hour), setupNow.Add(24*time.Hour))
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("sample is not JSON: %v\n%s", err, raw)
	}
	m["extra_ca_certs"] = []string{ca}
	fixed, _ := json.Marshal(m)
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, fixed, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("sample does not validate: %v\n%s", err, raw)
	}
	d := config.Defaults()
	if cfg.BindAddr != d.BindAddr || !reflect.DeepEqual(cfg.Agents, d.Agents) {
		t.Errorf("sample drifted from Defaults(): %+v vs %+v", cfg, d)
	}
}

// A config file exists: the hint must not tell the user to create one, and the
// host-trust (Linux pull) hint never mentions extra_ca_certs at all.
func TestHintWithConfigFileOrHostTrustOmitsSample(t *testing.T) {
	old := hintConfigPath
	hintConfigPath = "/x/config.json"
	defer func() { hintConfigPath = old }()
	for name, tc := range map[string]struct {
		configured bool
		goos       string
		pull       bool
	}{
		"configured":      {true, "darwin", false},
		"linux host pull": {false, "linux", true},
	} {
		var w bytes.Buffer
		printInterceptionHint(context.Background(), &w, curl60Host, tc.configured, tc.goos, tc.pull, fakeRoot("CN=R", "R", nil))
		if strings.Contains(w.String(), "bind_addr") || strings.Contains(w.String(), "No config file exists") {
			t.Errorf("%s: no sample expected:\n%s", name, w.String())
		}
	}
}
