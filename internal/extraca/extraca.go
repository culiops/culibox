// Package extraca loads, validates and canonicalizes the operator's extra
// CA certificates (config "extra_ca_certs"), so a box can trust a corporate
// TLS-inspection root. It is a leaf: it only reads files and never writes.
package extraca

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// ContainerBundle is where the box's merged trust bundle is mounted.
	ContainerBundle = "/etc/ssl/certs/ca-certificates.crt"
	// Label marks a box (container/image) built with a given CA set.
	Label = "io.culiops.agent-box.ca"

	maxFileBytes = 1 << 20
	maxCAs       = 32
)

// EnvNames are the variables a box sets to point common tools at ContainerBundle.
var EnvNames = []string{"SSL_CERT_FILE", "CURL_CA_BUNDLE", "REQUESTS_CA_BUNDLE", "AWS_CA_BUNDLE", "GIT_SSL_CAINFO", "NODE_EXTRA_CA_CERTS"}

// CA is one parsed certificate authority.
type CA struct {
	Subject   string // CommonName, or the full subject when the CN is empty
	SHA256    string // lowercase hex of the DER, no colons
	NotBefore time.Time
	NotAfter  time.Time
	DER       []byte
}

// Set is the de-duplicated CAs of all configured files, sorted by SHA256.
type Set struct {
	CAs []CA
}

// Load reads every path and returns the combined Set. Expiry is not an error
// (see Describe). Errors name the offending path and never echo file bytes.
func Load(paths []string) (Set, error) {
	seen := map[string]CA{}
	for _, p := range paths {
		cas, err := loadFile(p)
		if err != nil {
			return Set{}, err
		}
		for _, ca := range cas {
			seen[ca.SHA256] = ca
		}
		if len(seen) > maxCAs {
			return Set{}, fmt.Errorf("%s: the configured files hold more than %d certificates; export only the TLS-inspection root CA, not a whole system bundle", p, maxCAs)
		}
	}
	var s Set
	for _, ca := range seen {
		s.CAs = append(s.CAs, ca)
	}
	sort.Slice(s.CAs, func(i, j int) bool { return s.CAs[i].SHA256 < s.CAs[j].SHA256 })
	return s, nil
}

func loadFile(path string) ([]CA, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%s: must be an absolute path", path)
	}
	fi, err := os.Lstat(path)
	if err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		fi, err = os.Stat(path) // a symlink is fine if it resolves to a regular file
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, unwrapPathErr(err))
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, unwrapPathErr(err))
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, unwrapPathErr(err))
	}
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("%s: larger than 1 MiB; export only the TLS-inspection root CA", path)
	}

	var cas []CA
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("%s: contains a %q block; only CERTIFICATE blocks are allowed (is this a key file?)", path, b.Type)
		}
		cert, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%s: a CERTIFICATE block does not parse as an X.509 certificate", path)
		}
		subject := cert.Subject.CommonName
		if subject == "" {
			subject = cert.Subject.String()
		}
		if !cert.BasicConstraintsValid || !cert.IsCA {
			return nil, fmt.Errorf("%s: certificate %q is not a CA (no CA basic constraint); export the root CA, not a server certificate", path, subject)
		}
		sum := sha256.Sum256(cert.Raw)
		cas = append(cas, CA{
			Subject:   subject,
			SHA256:    hex.EncodeToString(sum[:]),
			NotBefore: cert.NotBefore,
			NotAfter:  cert.NotAfter,
			DER:       cert.Raw,
		})
	}
	if len(cas) == 0 {
		return nil, fmt.Errorf("%s: contains no CERTIFICATE block", path)
	}
	return cas, nil
}

// unwrapPathErr drops the *PathError wrapper so the path is not named twice.
func unwrapPathErr(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// Empty reports whether no CA is configured.
func (s Set) Empty() bool { return len(s.CAs) == 0 }

// PEM returns the canonical CERTIFICATE blocks in Set order, re-encoded from
// DER so the output never carries the source files' comments or line endings.
func (s Set) PEM() []byte {
	var out []byte
	for _, ca := range s.CAs {
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.DER})...)
	}
	return out
}

// Hash is "" for an empty Set, otherwise a stable 12-hex-char digest of the
// fingerprints (used as the image/container CA label).
func (s Set) Hash() string {
	if s.Empty() {
		return ""
	}
	fps := make([]string, len(s.CAs))
	for i, ca := range s.CAs {
		fps[i] = ca.SHA256
	}
	sum := sha256.Sum256([]byte(strings.Join(fps, "\n")))
	return hex.EncodeToString(sum[:])[:12]
}

// Describe returns whether now lies within the CA's validity window and a
// one-line summary for doctor/setup output.
func (c CA) Describe(now time.Time) (valid bool, line string) {
	valid = !now.Before(c.NotBefore) && !now.After(c.NotAfter)
	return valid, fmt.Sprintf("%s sha256:%s expires %s", c.Subject, c.SHA256, c.NotAfter.UTC().Format("2006-01-02"))
}
