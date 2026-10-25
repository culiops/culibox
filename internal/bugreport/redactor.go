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
	hosts *regexp.Regexp    // hostname, its short form, bind host, allowed hosts
}

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
			pairs = append(pairs, p, "<path>")
		}
		r.paths = strings.NewReplacer(pairs...)
	}

	var hosts []string
	add := func(h string) {
		if h != "" && !strings.EqualFold(h, "localhost") {
			hosts = append(hosts, h)
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
	if in.Host != "" && !strings.EqualFold(in.Host, "localhost") {
		add(in.Host)
		if short, _, _ := strings.Cut(in.Host, "."); len(short) >= 3 {
			add(short)
		}
	}
	if len(hosts) > 0 {
		sort.SliceStable(hosts, func(i, j int) bool { return len(hosts[i]) > len(hosts[j]) })
		quoted := make([]string, len(hosts))
		for i, h := range hosts {
			quoted[i] = regexp.QuoteMeta(h)
		}
		r.hosts = regexp.MustCompile(`(?i)` + strings.Join(quoted, "|"))
	}
	return r
}

func (r *redactor) apply(s string, in Input) string {
	if r.paths != nil {
		s = r.paths.Replace(s) // before tilde, which would shorten them
	}
	s = doctor.Redact(s, in.Home, in.Host)
	if r.hosts != nil {
		s = r.hosts.ReplaceAllString(s, "<host>")
	}
	s = ipv4Re.ReplaceAllStringFunc(s, func(m string) string {
		if ip, err := netip.ParseAddr(m); err == nil && (ip.IsLoopback() || ip.IsUnspecified()) {
			return m
		}
		return "<ip>"
	})
	return ipv6Re.ReplaceAllStringFunc(s, func(m string) string {
		if ip, err := netip.ParseAddr(m); err == nil && !ip.IsLoopback() && !ip.IsUnspecified() {
			return "<ip>"
		}
		return m
	})
}
