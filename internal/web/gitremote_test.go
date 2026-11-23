package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/gitview"
)

func TestOriginURL(t *testing.T) {
	for name, c := range map[string]struct{ cfg, want string }{
		"plain":                            {"[core]\n\tbare = false\n[remote \"origin\"]\n\turl = https://github.com/o/r.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n", "https://github.com/o/r.git"},
		"case-insensitive section and key": {"[Remote \"origin\"]\n\tURL=git@github.com:o/r.git\n", "git@github.com:o/r.git"},
		"subsection is exact":              {"[remote \"Origin\"]\n\turl = https://github.com/o/r\n", ""},
		"quoted, with a comment":           {"[remote \"origin\"]\n\turl = \"https://gitlab.com/g/p.git\" # mine\n", "https://gitlab.com/g/p.git"},
		"semicolon comment":                {"[remote \"origin\"]\n\turl = https://github.com/o/r ; x\n", "https://github.com/o/r"},
		"first of two":                     {"[remote \"origin\"]\n\turl = https://github.com/a/b\n\turl = https://github.com/c/d\n", "https://github.com/a/b"},
		"other remote":                     {"[remote \"upstream\"]\n\turl = https://github.com/o/r\n", ""},
		"after another section":            {"[remote \"upstream\"]\n\turl = https://github.com/u/r\n[remote \"origin\"]\n\turl = https://github.com/o/r\n", "https://github.com/o/r"},
		"commented out":                    {"[remote \"origin\"]\n#\turl = https://github.com/o/r\n", ""},
		"key outside a section":            {"url = https://github.com/o/r\n", ""},
		"empty":                            {"", ""},
	} {
		if got := originURL([]byte(c.cfg)); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

func TestRemoteLink(t *testing.T) {
	gh := &BlameRemote{Kind: "github", Base: "https://github.com/o/r"}
	for in, want := range map[string]*BlameRemote{
		"https://github.com/o/r.git":                            gh,
		"https://github.com/o/r":                                gh,
		"https://github.com/o/r/":                               gh,
		"http://github.com/o/r.git":                             gh,
		"git@github.com:o/r.git":                                gh,
		"ssh://git@github.com/o/r.git":                          gh,
		"git://github.com/o/r.git":                              gh,
		"https://user:tok@github.com/o/r.git":                   gh,
		"https://GitHub.com/o/r":                                gh,
		"https://github.com/my.org/re-po_1":                     {Kind: "github", Base: "https://github.com/my.org/re-po_1"},
		"https://gitlab.com/g/s/p.git":                          {Kind: "gitlab", Base: "https://gitlab.com/g/s/p"},
		"git@gitlab.com:g/p.git":                                {Kind: "gitlab", Base: "https://gitlab.com/g/p"},
		"git@bitbucket.org:o/r.git":                             {Kind: "bitbucket", Base: "https://bitbucket.org/o/r"},
		"https://github.com/o":                                  nil,
		"https://github.com/o/r/x":                              nil,
		"https://bitbucket.org/o/r/x":                           nil,
		"https://gitlab.com/g":                                  nil,
		"https://evil.com/o/r":                                  nil,
		"https://github.com.evil.com/o/r":                       nil,
		"https://evil.com/github.com/o/r":                       nil,
		"https://github.com:8443/o/r":                           nil,
		"ssh://git@github.com:22/o/r.git":                       nil,
		"https://github.com/o/r?x=1":                            nil,
		"https://github.com/o/r#x":                              nil,
		"https://github.com/../r":                               nil,
		"https://github.com/o/..":                               nil,
		"https://github.com/./r":                                nil,
		"https://github.com/o/<x>":                              nil,
		"https://github.com/o/r%2f..":                           nil,
		"https://github.com//o/r":                               nil,
		"javascript:alert(1)//github.com/o/r":                   nil,
		"file:///github.com/o/r":                                nil,
		"/srv/git/o/r.git":                                      nil,
		"github.com:o/r":                                        gh,
		"git@github.com:/o/r.git":                               nil,
		"":                                                      nil,
		"https://github.com/o/r.git\nx":                         nil,
		"https://github.com/" + strings.Repeat("a", 300) + "/r": nil,
	} {
		got := remoteLink(in)
		if (got == nil) != (want == nil) || got != nil && *got != *want {
			t.Errorf("remoteLink(%q) = %+v, want %+v", in, got, want)
		}
	}
}

func TestReadOrigin(t *testing.T) {
	s, h, _ := hostGitServer(t)
	p, err := s.fsPolicy()
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	tg := gitview.Target{Top: repo, Common: filepath.Join(repo, ".git")}
	cfg := filepath.Join(repo, ".git", "config")
	if r := p.readOrigin(tg); r != nil {
		t.Fatalf("no config: %+v", r)
	}
	mustWrite(t, cfg, "[remote \"origin\"]\n\turl = https://github.com/example/demo.git\n")
	if r := p.readOrigin(tg); r == nil || r.Base != "https://github.com/example/demo" {
		t.Fatalf("config: %+v", r)
	}
	// A linked worktree reads the common dir's config.
	if r := p.readOrigin(gitview.Target{Top: filepath.Join(h, "wt"), Common: filepath.Join(repo, ".git")}); r == nil {
		t.Fatal("worktree: no remote")
	}
	// Too big: not read.
	mustWrite(t, cfg, "[remote \"origin\"]\n\turl = https://github.com/example/demo.git\n"+strings.Repeat("#\n", 40<<10))
	if r := p.readOrigin(tg); r != nil {
		t.Fatalf("huge config: %+v", r)
	}
	// A link is not followed.
	other := filepath.Join(h, "other-config")
	mustWrite(t, other, "[remote \"origin\"]\n\turl = https://github.com/example/demo.git\n")
	if err := os.Remove(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, cfg); err != nil {
		t.Fatal(err)
	}
	if r := p.readOrigin(tg); r != nil {
		t.Fatalf("linked config: %+v", r)
	}
}
