package extraca

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"
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
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	leaf := srv.Certificate()
	pool := x509.NewCertPool()
	pool.AddCert(leaf)

	hostport := srv.Listener.Addr().String()
	subject, cn, err := rootSubjectWith(context.Background(), hostport, &tls.Config{RootCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	if subject != leaf.Subject.String() || cn != leaf.Subject.CommonName {
		t.Errorf("got %q / %q, want %q / %q", subject, cn, leaf.Subject.String(), leaf.Subject.CommonName)
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
