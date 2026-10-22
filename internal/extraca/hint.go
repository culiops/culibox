package extraca

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// interceptPatterns are the failure signatures of a TLS-inspecting proxy
// (spec 6.7), matched case-insensitively.
var interceptPatterns = regexp.MustCompile(`(?i)` + strings.Join([]string{
	`curl:? \(60\)`,
	`SSL certificate problem`,
	`x509: certificate signed by unknown authority`,
	`self[- ]signed certificate in certificate chain`,
	`unable to get local issuer certificate`,
}, "|"))

var httpsURL = regexp.MustCompile(`https://[^\s"'<>` + "`" + `]+`)

// Intercepted reports whether build/pull output looks like HTTPS being
// intercepted by a proxy whose root CA the box does not trust.
func Intercepted(output string) bool {
	return interceptPatterns.MatchString(output)
}

// FailingHost returns host:port (port defaults to 443) of the last https URL
// that appears before the first interception error, or "" if there is none.
// There is deliberately no fallback host: interception can be selective by
// domain, so guessing one would show an untouched chain.
func FailingHost(output string) string {
	loc := interceptPatterns.FindStringIndex(output)
	if loc == nil {
		return ""
	}
	urls := httpsURL.FindAllString(output[:loc[0]], -1)
	if len(urls) == 0 {
		return ""
	}
	raw := strings.TrimRight(urls[len(urls)-1], ".,;:)")
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// RootSubject dials hostport from the host (5 s budget) and returns the full
// subject and the CommonName of the root of the verified chain the host sees.
// On macOS the host trusts the corporate root, so that root is the CA to export.
func RootSubject(ctx context.Context, hostport string) (subject, cn string, err error) {
	return rootSubjectWith(ctx, hostport, nil)
}

func rootSubjectWith(ctx context.Context, hostport string, cfg *tls.Config) (subject, cn string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: cfg}
	conn, err := d.DialContext(ctx, "tcp", hostport)
	if err != nil {
		return "", "", err
	}
	defer conn.Close()
	tc, ok := conn.(*tls.Conn)
	if !ok {
		return "", "", errors.New("not a TLS connection")
	}
	chains := tc.ConnectionState().VerifiedChains
	if len(chains) == 0 || len(chains[0]) == 0 {
		return "", "", errors.New("no verified chain")
	}
	root := chains[0][len(chains[0])-1]
	return root.Subject.String(), root.Subject.CommonName, nil
}
