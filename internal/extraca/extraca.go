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
	"regexp"
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
//
// It works in two phases so the size limit is judged on what the operator
// pointed at, not on what happens to parse: a whole system bundle (which can
// hold an unparseable or v1 root) must get the "export only" refusal, not a
// parse error about one of its certificates.
func Load(paths []string) (Set, error) {
	type fileBlocks struct {
		path   string
		blocks []*pem.Block
	}
	var files []fileBlocks
	distinct := map[[sha256.Size]byte]struct{}{}
	for _, p := range paths {
		blocks, err := readBlocks(p)
		if err != nil {
			return Set{}, err
		}
		for _, b := range blocks {
			distinct[sha256.Sum256(b.Bytes)] = struct{}{}
		}
		if len(distinct) > maxCAs {
			return Set{}, fmt.Errorf("%s: the configured files hold more than %d certificates; export only the TLS-inspection root CA, not a whole system bundle", p, maxCAs)
		}
		files = append(files, fileBlocks{p, blocks})
	}

	seen := map[string]CA{}
	for _, f := range files {
		for _, b := range f.blocks {
			ca, err := parseCA(f.path, b)
			if err != nil {
				return Set{}, err
			}
			seen[ca.SHA256] = ca
		}
	}
	var s Set
	for _, ca := range seen {
		s.CAs = append(s.CAs, ca)
	}
	sort.Slice(s.CAs, func(i, j int) bool { return s.CAs[i].SHA256 < s.CAs[j].SHA256 })
	return s, nil
}

// pemLabel matches a plausible PEM type label; anything else is not echoed.
var pemLabel = regexp.MustCompile(`^[A-Z ]{1,40}$`)

// readBlocks reads one file and returns its PEM blocks, all of type CERTIFICATE.
func readBlocks(path string) ([]*pem.Block, error) {
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

	var blocks []*pem.Block
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			// The label comes from the file; echo it only when it looks like one.
			if pemLabel.MatchString(b.Type) {
				return nil, fmt.Errorf("%s: contains a %q block; only CERTIFICATE blocks are allowed (is this a key file?)", path, b.Type)
			}
			return nil, fmt.Errorf("%s: contains an unrecognized PEM block; only CERTIFICATE blocks are allowed (is this a key file?)", path)
		}
		blocks = append(blocks, b)
	}
	if len(blocks) == 0 {
		return nil, fmt.Errorf("%s: contains no CERTIFICATE block", path)
	}
	return blocks, nil
}

func parseCA(path string, b *pem.Block) (CA, error) {
	cert, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return CA{}, fmt.Errorf("%s: a CERTIFICATE block does not parse as an X.509 certificate", path)
	}
	subject := cert.Subject.CommonName
	if subject == "" {
		subject = cert.Subject.String()
	}
	if !cert.BasicConstraintsValid || !cert.IsCA {
		return CA{}, fmt.Errorf("%s: certificate %q is not a CA (no CA basic constraint); export the root CA, not a server certificate", path, subject)
	}
	sum := sha256.Sum256(cert.Raw)
	return CA{
		Subject:   subject,
		SHA256:    hex.EncodeToString(sum[:]),
		NotBefore: cert.NotBefore,
		NotAfter:  cert.NotAfter,
		DER:       cert.Raw,
	}, nil
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
