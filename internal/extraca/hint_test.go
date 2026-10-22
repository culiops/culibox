package extraca

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIntercepted(t *testing.T) {
	yes := []string{
		"curl: (60) SSL certificate problem: unable to get local issuer certificate",
		"x509: certificate signed by unknown authority",
		"self-signed certificate in certificate chain",
		"self signed certificate in certificate chain",
		"unable to get local issuer certificate",
		"fatal: SSL certificate problem: something",
		"Get \"https://registry-1.docker.io/v2/\": tls: failed: x509: certificate signed by unknown authority\nexit status 1",
	}
	for _, s := range yes {
		if !Intercepted(s) {
			t.Errorf("Intercepted(%q) = false", s)
		}
	}
	no := []string{"exit status 1", "curl: (22) The requested URL returned error: 404", "", "(60 seconds elapsed)"}
	for _, s := range no {
		if Intercepted(s) {
			t.Errorf("Intercepted(%q) = true", s)
		}
	}
}

func TestFailingHost(t *testing.T) {
	cases := []struct{ name, out, want string }{
		{"curl",
			"+ curl -fsSL \"https://www.hashicorp.com/.well-known/pgp-key.txt\"\ncurl: (60) SSL certificate problem: unable to get local issuer certificate\nMore details here: https://curl.se/docs/sslcerts.html\n",
			"www.hashicorp.com:443"},
		{"go get", "error: Get \"https://registry-1.docker.io/v2/\": x509: certificate signed by unknown authority\n",
			"registry-1.docker.io:443"},
		{"go get no quotes", "Get https://registry-1.docker.io/v2/: x509: certificate signed by unknown authority\n",
			"registry-1.docker.io:443"},
		{"explicit port", "curl -fsSL https://example.com:8443/x.sh\ncurl: (60) SSL certificate problem\n",
			"example.com:8443"},
		{"last URL before the first error line",
			"Step 3: fetch https://first.example.com/a\nfetch https://second.example.com/b\ncurl: (60) SSL certificate problem\nthen https://later.example.com/c\nx509: certificate signed by unknown authority\n",
			"second.example.com:443"},
		{"no url", "curl: (60) SSL certificate problem\n", ""},
		{"only url after the error", "curl: (60) SSL certificate problem\nsee https://curl.se/docs/sslcerts.html\n", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FailingHost(tc.out); got != tc.want {
				t.Errorf("FailingHost = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRootSubject(t *testing.T) {
	// A real two-element chain: the server presents only the leaf, the client
	// trusts only the root, so VerifiedChains[0] is [leaf, root]. A
	// leaf-returning implementation must fail this test.
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test Root CA", Organization: []string{"Root Org"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		BasicConstraintsValid: true,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "leaf.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, root, &leafKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}}}
	srv.StartTLS()
	defer srv.Close()
	hostport := srv.Listener.Addr().String()

	pool := x509.NewCertPool()
	pool.AddCert(root)
	subject, cn, err := rootSubjectWith(context.Background(), hostport, &tls.Config{RootCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	if subject != root.Subject.String() || cn != "Test Root CA" {
		t.Errorf("got %q / %q, want the ROOT %q / %q", subject, cn, root.Subject.String(), "Test Root CA")
	}
	if cn == "leaf.test" || subject == leafTmpl.Subject.String() {
		t.Error("returned the leaf, not the root")
	}

	// An untrusted chain is an error, not a guess.
	if _, _, err := rootSubjectWith(context.Background(), hostport, &tls.Config{RootCAs: x509.NewCertPool()}); err == nil {
		t.Error("want an error when the chain does not verify")
	}
	// Unreachable host.
	srv.Close()
	if _, _, err := RootSubject(context.Background(), hostport); err == nil {
		t.Error("want a dial error")
	}
}
