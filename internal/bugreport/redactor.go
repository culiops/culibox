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
	// A maximal run of dotted numbers. Boundaries are the run itself, not \b,
	// which treats "_" and letters as word characters ("ip_10.0.0.5" and
	// "x10.0.0.5y" must still match).
	dottedRunRe = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)+`)
	// A run of characters an IPv6 literal can be made of (dots for ::ffff:1.2.3.4).
	ipv6RunRe = regexp.MustCompile(`[0-9A-Fa-f:.]*:[0-9A-Fa-f:.]*:[0-9A-Fa-f:.]*`)
)

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// maxIPv6Len is the longest textual IPv6 literal (mapped IPv4 form).
const maxIPv6Len = 45

// redactIPv4 replaces IPv4 literals other than loopback/unspecified. It works
// on whole dotted-number runs. A run of four parts is one candidate. A longer
// run ("10.0.0.5.443" is tcpdump's ip.port, "1.2.3.4.5" may be a version) fails
// closed: every four-part window inside it that parses as a redactable address
// is replaced, and overlapping windows merge into one "<ip>". A sentence-ending
// dot is not part of a run.
func redactIPv4(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range dottedRunRe.FindAllStringIndex(s, -1) {
		// Part boundaries within the run, as offsets into s.
		starts := []int{m[0]}
		for i := m[0]; i < m[1]; i++ {
			if s[i] == '.' {
				starts = append(starts, i+1)
			}
		}
		if len(starts) < 4 {
			continue
		}
		partEnd := func(k int) int { // end offset of part k (exclusive)
			if k+1 < len(starts) {
				return starts[k+1] - 1
			}
			return m[1]
		}
		spanStart, spanEnd := -1, -1
		flush := func() {
			if spanStart < 0 {
				return
			}
			b.WriteString(s[last:spanStart])
			b.WriteString("<ip>")
			last = spanEnd
			spanStart, spanEnd = -1, -1
		}
		for k := 0; k+4 <= len(starts); k++ {
			lo, hi := starts[k], partEnd(k+3)
			ip, ok := parseIPv4(s[lo:hi])
			if !ok || ip.IsLoopback() || ip.IsUnspecified() {
				continue
			}
			if spanStart >= 0 && lo <= spanEnd {
				spanEnd = hi // overlaps or touches the open span
				continue
			}
			flush()
			spanStart, spanEnd = lo, hi
		}
		flush()
	}
	b.WriteString(s[last:])
	return b.String()
}

// parseIPv4 accepts zero-padded octets ("010.000.000.005"), which netip
// rejects but some tools print.
func parseIPv4(t string) (netip.Addr, bool) {
	if ip, err := netip.ParseAddr(t); err == nil {
		return ip, true
	}
	parts := strings.Split(t, ".")
	for i, p := range parts {
		p = strings.TrimLeft(p, "0")
		if p == "" {
			p = "0"
		}
		parts[i] = p
	}
	ip, err := netip.ParseAddr(strings.Join(parts, "."))
	return ip, err == nil
}

// redactIPv6 replaces IPv6 literals other than loopback/unspecified. Inside
// each candidate run it takes, from the left, the longest span that parses, so
// a swallowed neighbour (a trailing ":" in "2001:db8::1: refused", a prefix in
// "key:2001:db8::1") cannot make a real address survive unparsed.
func redactIPv6(s string) string {
	var b strings.Builder
	last := 0
	for _, run := range ipv6RunRe.FindAllStringIndex(s, -1) {
		for i := run[0]; i < run[1]; {
			end := min(run[1], i+maxIPv6Len)
			hit := -1
			var ip netip.Addr
			for j := end; j > i+1; j-- {
				if a, err := netip.ParseAddr(s[i:j]); err == nil {
					hit, ip = j, a
					break
				}
			}
			if hit < 0 {
				i++
				continue
			}
			if u := ip.Unmap(); !u.IsLoopback() && !u.IsUnspecified() {
				b.WriteString(s[last:i])
				b.WriteString("<ip>")
				last = hit
			}
			i = hit
		}
	}
	b.WriteString(s[last:])
	return b.String()
}

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
	s = redactIPv6(s)
	s = redactIPv4(s)
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
