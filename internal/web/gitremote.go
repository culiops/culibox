package web

import (
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/culiops/culibox/internal/gitview"
)

// BlameRemote is where the blame card's links point: Kind is github, gitlab or
// bitbucket, Base the repository's https page on that host (no userinfo, port,
// query or fragment). The client adds a fixed suffix and a validated commit id
// or request number to it, and checks Base again before it does.
type BlameRemote struct {
	Kind string `json:"kind"`
	Base string `json:"base"`
}

// maxGitConfigBytes bounds the repository config read for its origin URL.
const maxGitConfigBytes = 64 << 10

// remoteHosts are the only hosts a link is built for, with their kind.
var remoteHosts = map[string]string{"github.com": "github", "gitlab.com": "gitlab", "bitbucket.org": "bitbucket"}

var (
	remoteSegRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	// scpRE is git's scp-like form, [user@]host:path, path not absolute.
	scpRE = regexp.MustCompile(`^(?:[A-Za-z0-9._-]+@)?([A-Za-z0-9.-]+):([^/].*)$`)
)

// readOrigin is the link base of t's origin remote, read as text from the
// repository's config file (never by running git: anti-goal A1). The file is
// opened through the fs policy like any read, without following a link, and
// must be a regular file of at most maxGitConfigBytes. nil when any of that
// fails, or the URL is not one remoteLink accepts.
func (p fsPolicy) readOrigin(t gitview.Target) *BlameRemote {
	common := t.Common
	if common == "" {
		common = filepath.Join(t.Top, ".git")
	}
	path := filepath.Join(common, "config")
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	req, err := p.resolve(path)
	if err != nil || req.Abs != path {
		return nil
	}
	f, err := openFsTarget(req, syscall.O_NOFOLLOW)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxGitConfigBytes {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(f, maxGitConfigBytes+1))
	if err != nil || len(b) > maxGitConfigBytes {
		return nil
	}
	return remoteLink(originURL(b))
}

// originURL is the first url of [remote "origin"] in a git config file's text,
// or "". It reads the plain forms git writes: sections, "key = value" with an
// optional quoted value, and # or ; comments. Includes and url.*.insteadOf are
// not followed: a link built from less is at worst missing, never elsewhere.
func originURL(cfg []byte) string {
	in := false
	for _, line := range strings.Split(string(cfg), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				in = false
				continue
			}
			name, sub, _ := strings.Cut(line[1:end], " ")
			in = strings.EqualFold(name, "remote") && strings.TrimSpace(sub) == `"origin"`
			continue
		}
		if !in {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "url") {
			continue
		}
		val = strings.TrimSpace(val)
		if strings.HasPrefix(val, `"`) {
			if end := strings.IndexByte(val[1:], '"'); end >= 0 {
				return val[1 : end+1]
			}
			return ""
		}
		if i := strings.IndexAny(val, "#;"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		return val
	}
	return ""
}

// remoteLink is the link base of a remote URL on github.com, gitlab.com or
// bitbucket.org, in any of git's forms (https, http, ssh, git, scp-like), or nil.
// The host must be exactly one of those, with no port; the path must be
// owner/repo (GitLab: group/…/project) of plain segments, an optional ".git" at
// the end; nothing of the URL but host and path reaches the base, so userinfo
// (a token in an https remote) never does.
func remoteLink(raw string) *BlameRemote {
	if raw == "" || len(raw) > 512 {
		return nil
	}
	for _, r := range raw {
		if r <= ' ' || r > '~' {
			return nil
		}
	}
	var host, path string
	if m := scpRE.FindStringSubmatch(raw); m != nil && !strings.Contains(raw, "://") {
		host, path = m[1], m[2]
	} else {
		u, err := url.Parse(raw)
		if err != nil || u.Opaque != "" || u.Port() != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
			strings.Contains(raw, "#") || u.RawPath != "" {
			return nil
		}
		switch u.Scheme {
		case "https", "http", "ssh", "git":
		default:
			return nil
		}
		host = u.Hostname()
		if !strings.HasPrefix(u.Path, "/") {
			return nil
		}
		path = u.Path[1:]
	}
	host = strings.ToLower(host)
	kind, ok := remoteHosts[host]
	if !ok {
		return nil
	}
	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")
	segs := strings.Split(path, "/")
	for _, s := range segs {
		if !remoteSegRE.MatchString(s) || s == "." || s == ".." {
			return nil
		}
	}
	if n := len(segs); kind == "gitlab" && (n < 2 || n > 20) || kind != "gitlab" && n != 2 {
		return nil
	}
	return &BlameRemote{Kind: kind, Base: "https://" + host + "/" + strings.Join(segs, "/")}
}
