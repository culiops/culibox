// Package hostmatch normalises hostnames and matches them against the egress
// allowlist patterns. It is pure and strict: anything ambiguous is refused,
// because the same code guards config validation and the proxy's CONNECT target.
package hostmatch

import (
	"errors"
	"net/netip"
	"strings"
)

const (
	maxName  = 253
	maxLabel = 63
)

// Normalize returns the canonical form of a DNS name: ASCII-lowercase, at most
// one trailing dot removed. It refuses IP literals (any spelling), anything
// that is not a plain ASCII DNS name, single-label names, and names whose last
// label does not start with a letter (a numeric or hex-looking tail is how
// "127.1" and "0x7f.0x1" read as addresses to a libc resolver).
func Normalize(host string) (string, bool) {
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > maxName {
		return "", false
	}
	if strings.ContainsAny(host, ":[]") {
		return "", false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return "", false
	}
	// ASCII-only folding: strings.ToLower would map U+212A (KELVIN SIGN) to 'k'.
	b := make([]byte, len(host))
	for i := 0; i < len(host); i++ {
		c := host[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	h := string(b)

	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return "", false
	}
	for _, l := range labels {
		if !validLabel(l) {
			return "", false
		}
	}
	if last := labels[len(labels)-1]; last[0] < 'a' || last[0] > 'z' {
		return "", false
	}
	return h, true
}

func validLabel(l string) bool {
	if l == "" || len(l) > maxLabel || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}
	for i := 0; i < len(l); i++ {
		c := l[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// ValidPattern accepts an exact host that is already in normal form, or "*."
// followed by such a host with at least two labels (so "*.com" is refused).
func ValidPattern(p string) error {
	if rest, ok := strings.CutPrefix(p, "*."); ok {
		if n, good := Normalize(rest); !good || n != rest || strings.HasSuffix(rest, ".") {
			return errors.New("wildcard must be *. followed by a normalised host of at least two labels")
		}
		return nil
	}
	if n, good := Normalize(p); !good || n != p || strings.HasSuffix(p, ".") {
		return errors.New("must be a normalised lowercase hostname (no trailing dot, no IP literal) or *.host")
	}
	return nil
}

// Match reports whether host is allowed by any pattern. host must already be
// normalised; anything else never matches. Invalid patterns never match.
// "*.example.com" matches "a.example.com" and "a.b.example.com", not "example.com".
func Match(patterns []string, host string) bool {
	if n, ok := Normalize(host); !ok || n != host || strings.HasSuffix(host, ".") {
		return false
	}
	for _, p := range patterns {
		if ValidPattern(p) != nil {
			continue
		}
		if strings.HasPrefix(p, "*.") {
			if strings.HasSuffix(host, p[1:]) {
				return true
			}
		} else if host == p {
			return true
		}
	}
	return false
}
