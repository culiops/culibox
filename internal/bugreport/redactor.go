package bugreport

import (
	"net"
	"net/netip"
	"regexp"
	"sort"
	"strings"

	"github.com/culiops/agent-box/internal/doctor"
)

// redactor holds everything that identifies this machine or its owner, built
// once per bundle so no regexp is compiled per line.
type redactor struct {
	paths *strings.Replacer // configured data dir / profiles path
	hosts *regexp.Regexp    // dotted names (FQDN, bind host, allowed hosts): substring match
	words *regexp.Regexp    // dotless names (short hostname): whole tokens only
}

// Placeholders stand in while the passes run so one pass cannot match the
// text another inserted (a host named "host" must not hit "<host>").
const (
	hostMark = "\x00"
	pathMark = "\x01"
)

var (
	ipv4Re = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)
	// Loose candidate for an IPv6 literal; netip decides if it really is one,
	// so timestamps like 12:30:45 survive.
	ipv6Re = regexp.MustCompile(`(?i)[0-9a-f]{0,4}(?::[0-9a-f]{0,4}){2,7}`)
)

func newRedactor(in Input) *redactor {
	r := &redactor{}
	c := in.Config

	var paths []string
	for _, p := range []string{c.DataDir, c.ProfilesPath} {
		if p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) > 0 {
		sort.SliceStable(paths, func(i, j int) bool { return len(paths[i]) > len(paths[j]) })
		var pairs []string
		for _, p := range paths {
			pairs = append(pairs, p, pathMark)
		}
		r.paths = strings.NewReplacer(pairs...)
	}

	var dotted, words []string
	add := func(h string) {
		switch {
		case h == "" || strings.EqualFold(h, "localhost"):
		case strings.Contains(h, "."):
			dotted = append(dotted, h)
		default:
			words = append(words, h)
		}
	}
	if h, _, err := net.SplitHostPort(c.BindAddr); err == nil {
		if ip, err := netip.ParseAddr(h); err != nil || !(ip.IsLoopback() || ip.IsUnspecified()) {
			add(h)
		}
	}
	for _, a := range c.AllowedHosts {
		add(a)
		if h, _, err := net.SplitHostPort(a); err == nil {
			add(h)
		}
	}
	add(in.Host)
	// The short form of a dotted hostname: "mac" must not turn "macOS" into
	// "<host>OS", so it only ever matches as a whole token.
	if short, _, _ := strings.Cut(in.Host, "."); strings.Contains(in.Host, ".") && len(short) >= 3 {
		add(short)
	}
	r.hosts = alternation(dotted)
	r.words = alternation(words)
	return r
}

func (r *redactor) apply(s string, in Input) string {
	s = strings.NewReplacer(hostMark, "", pathMark, "").Replace(s)
	if r.paths != nil {
		s = r.paths.Replace(s) // before tilde, which would shorten them
	}
	// Host is withheld from doctor.Redact: its plain substring replace would
	// turn "macOS" into "<host>OS" for a host named "mac".
	s = doctor.Redact(s, in.Home, "")
	if r.hosts != nil {
		s = r.hosts.ReplaceAllString(s, hostMark)
	}
	if r.words != nil {
		s = replaceWords(s, r.words, hostMark)
	}
	s = ipv4Re.ReplaceAllStringFunc(s, func(m string) string {
		if ip, err := netip.ParseAddr(m); err == nil && (ip.IsLoopback() || ip.IsUnspecified()) {
			return m
		}
		return "<ip>"
	})
	s = ipv6Re.ReplaceAllStringFunc(s, func(m string) string {
		if ip, err := netip.ParseAddr(m); err == nil && !ip.IsLoopback() && !ip.IsUnspecified() {
			return "<ip>"
		}
		return m
	})
	return strings.NewReplacer(hostMark, "<host>", pathMark, "<path>").Replace(s)
}

// alternation is a case-insensitive "a|b|c", longest first; nil when empty.
func alternation(names []string) *regexp.Regexp {
	if len(names) == 0 {
		return nil
	}
	sort.SliceStable(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = regexp.QuoteMeta(n)
	}
	return regexp.MustCompile(`(?i)` + strings.Join(quoted, "|"))
}

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// replaceWords replaces re's matches that are whole tokens: at the start or
// end of s, or next to a non-[A-Za-z0-9] byte. RE2 has no lookaround and a
// consuming boundary group would swallow the separator between "mac-mac", so
// the boundary bytes are checked by hand.
func replaceWords(s string, re *regexp.Regexp, repl string) string {
	var b strings.Builder
	last := 0
	for _, m := range re.FindAllStringIndex(s, -1) {
		if m[0] > 0 && isAlnum(s[m[0]-1]) || m[1] < len(s) && isAlnum(s[m[1]]) {
			continue
		}
		b.WriteString(s[last:m[0]])
		b.WriteString(repl)
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}
