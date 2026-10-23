package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestValidateContextPaths(t *testing.T) {
	many := make([]string, maxContextPaths+1)
	for i := range many {
		many[i] = fmt.Sprintf("/srv/p%d", i)
	}
	cases := []struct {
		name string
		a    AgentConfig
		want string // "" = valid
	}{
		{"valid", AgentConfig{ContextPaths: []string{"/srv/x/CLAUDE.md", "/home/u/b/docs"}, DefaultCwd: "/home/u/a"}, ""},
		{"none", AgentConfig{}, ""},
		{"tmp subdir is fine", AgentConfig{ContextPaths: []string{"/tmp/ab-x/docs"}}, ""},
		{"relative", AgentConfig{ContextPaths: []string{"docs"}}, "must be an absolute path"},
		{"unclean", AgentConfig{ContextPaths: []string{"/home/u/b/../c"}}, "must be a clean path"},
		{"colon", AgentConfig{ContextPaths: []string{"/home/u/b:ro"}}, `":" is not allowed`},
		{"control", AgentConfig{ContextPaths: []string{"/home/u/\x01"}}, "control characters"},
		{"bad utf8", AgentConfig{ContextPaths: []string{"/home/u/\xff"}}, "valid UTF-8"},
		{"root", AgentConfig{ContextPaths: []string{"/"}}, "the filesystem root"},
		{"too many", AgentConfig{ContextPaths: many}, "at most 16"},
		{"duplicate", AgentConfig{ContextPaths: []string{"/home/u/b", "/home/u/b"}}, "duplicate"},
		{"nested", AgentConfig{ContextPaths: []string{"/home/u/b", "/home/u/b/docs"}}, "nest"},
		{"nested reversed", AgentConfig{ContextPaths: []string{"/home/u/b/docs", "/home/u/b"}}, "nest"},
		{"sibling prefix is not nesting", AgentConfig{ContextPaths: []string{"/home/u/b", "/home/u/bc"}}, ""},
		{"cwd inside context", AgentConfig{ContextPaths: []string{"/home/u"}, DefaultCwd: "/home/u/a"}, "overlaps default_cwd"},
		{"context inside cwd", AgentConfig{ContextPaths: []string{"/home/u/a/docs"}, DefaultCwd: "/home/u/a"}, "overlaps default_cwd"},
		{"cwd equal", AgentConfig{ContextPaths: []string{"/home/u/a"}, DefaultCwd: "/home/u/a"}, "overlaps default_cwd"},
		{"cwd with trailing slash", AgentConfig{ContextPaths: []string{"/home/u/b/docs"}, DefaultCwd: "/home/u/b/"}, "overlaps default_cwd"},
		{"cwd with dotdot", AgentConfig{ContextPaths: []string{"/home/u/b/docs"}, DefaultCwd: "/home/u/x/../b"}, "overlaps default_cwd"},
		{"clone_of overlap", AgentConfig{ContextPaths: []string{"/r/agent-box/docs"}, CloneOf: "/r/agent-box", DefaultCwd: "/c/agent-box"}, "overlaps clone_of"},
		{"clone_of inside context", AgentConfig{ContextPaths: []string{"/r"}, CloneOf: "/r/agent-box", DefaultCwd: "/c/agent-box"}, "overlaps clone_of"},
		{"host runtime", AgentConfig{ContextPaths: []string{"/srv/x"}, Runtime: "host"}, `needs runtime "box"`},
		{"usr", AgentConfig{ContextPaths: []string{"/usr/x"}}, "refused: it would shadow"},
		{"etc", AgentConfig{ContextPaths: []string{"/etc"}}, "refused: it would shadow"},
		{"opt", AgentConfig{ContextPaths: []string{"/opt/y"}}, "refused: it would shadow"},
		{"proc", AgentConfig{ContextPaths: []string{"/proc/1"}}, "refused: it would shadow"},
		{"box home", AgentConfig{ContextPaths: []string{"/home/agent/z"}}, "refused: it would shadow"},
		{"var", AgentConfig{ContextPaths: []string{"/var/lib/x"}}, "refused: it would shadow"},
		{"home", AgentConfig{ContextPaths: []string{"/home"}}, "refused: it would shadow"},
		{"tmp", AgentConfig{ContextPaths: []string{"/tmp"}}, "refused: it would shadow"},
		{"mnt", AgentConfig{ContextPaths: []string{"/mnt"}}, "refused: it would shadow"},
		{"media", AgentConfig{ContextPaths: []string{"/media"}}, "refused: it would shadow"},
		{"mnt subdir is fine", AgentConfig{ContextPaths: []string{"/mnt/cold/docs"}}, ""},
		{"gh config dir", AgentConfig{ContextPaths: []string{"/tmp/agentbox-gh"}}, "refused: it would shadow /tmp/agentbox-gh"},
		{"inside gh config dir", AgentConfig{ContextPaths: []string{"/tmp/agentbox-gh/hosts.yml"}}, "refused: it would shadow /tmp/agentbox-gh"},
		{"gh look-alike is fine", AgentConfig{ContextPaths: []string{"/tmp/agentbox-ghx"}}, ""},
		{"session pid file", AgentConfig{ContextPaths: []string{"/tmp/ac-session-abc.pid"}}, "agent-box's own files"},
		{"under ac-session dir", AgentConfig{ContextPaths: []string{"/tmp/ac-session-x/docs"}}, "agent-box's own files"},
		{"ac-session deeper is fine", AgentConfig{ContextPaths: []string{"/tmp/proj/ac-session-x"}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.a.Name, tc.a.Bin = "p", "claude"
			err := validateContextPaths(tc.a)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("want valid, got %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestContextHash(t *testing.T) {
	if got := ContextHash([]string{"/a", "/b"}); got != ContextHash([]string{"/b", "/a"}) {
		t.Errorf("order must not matter: %q", got)
	}
	if ContextHash([]string{"/a", "/b"}) == ContextHash([]string{"/a", "/c"}) {
		t.Error("different lists must hash differently")
	}
	if ContextHash([]string{"/ab"}) == ContextHash([]string{"/a", "/b"}) {
		t.Error("the separator must keep [/ab] and [/a /b] apart")
	}
	if got := ContextHash(nil); got != "" {
		t.Errorf("empty list: want \"\", got %q", got)
	}
	if got := ContextHash([]string{"/a"}); len(got) != 16 {
		t.Errorf("want 16 hex chars, got %q", got)
	}
	in := []string{"/b", "/a"}
	ContextHash(in)
	if in[0] != "/b" {
		t.Error("ContextHash must not reorder its input")
	}
}

func TestContextNameWarnings(t *testing.T) {
	for _, p := range []string{"/x/.env.prd", "/x/id_rsa", "/x/id_ecdsa", "/x/id_ed25519_sk", "/x/prod.tfstate", "/x/terraform.tfstate.backup",
		"/x/credentials", "/x/credentials.json", "/x/prod.tfvars", "/x/.netrc", "/x/.npmrc", "/x/.git-credentials",
		"/x/auth.json", "/x/cert.p12", "/x/cert.pfx", "/x/kubeconfig"} {
		w := ContextNameWarnings([]string{p})
		if len(w) != 1 || !strings.Contains(w[0], "looks like a secret or config file; the box will be able to read and send it") {
			t.Errorf("%s: want one warning, got %v", p, w)
		}
	}
	for _, p := range []string{"/x/docs", "/x/CLAUDE.md", "/x/main.tf", "/x/variables.tf"} {
		if w := ContextNameWarnings([]string{p}); len(w) != 0 {
			t.Errorf("%s: want no warning, got %v", p, w)
		}
	}
}

func TestContextPathsEnvValue(t *testing.T) {
	if ContextPathsEnv != "AGENTBOX_CONTEXT_PATHS" {
		t.Errorf("env name = %q", ContextPathsEnv)
	}
	if got := ContextPathsEnvValue([]string{"/a", "/b/c"}); got != "/a:/b/c" {
		t.Errorf("got %q", got)
	}
}

func TestValidateProfileCallsContextPaths(t *testing.T) {
	a := AgentConfig{Name: "p", Bin: "claude", ContextPaths: []string{"docs"}}
	if err := ValidateProfile(a, nil); err == nil || !strings.Contains(err.Error(), `profile "p": context_paths`) {
		t.Errorf("ValidateProfile: %v", err)
	}
}

func TestConfigValidateContextPaths(t *testing.T) {
	a := AgentConfig{Name: "p", Bin: "claude", ContextPaths: []string{"/srv/x"}}
	if err := (Config{BindAddr: "127.0.0.1:8899", Agents: []AgentConfig{a}}).Validate(); err != nil {
		t.Errorf("box default: %v", err)
	}
	rel := a
	rel.ContextPaths = []string{"docs"}
	if err := (Config{BindAddr: "127.0.0.1:8899", Agents: []AgentConfig{rel}}).Validate(); err == nil || !strings.Contains(err.Error(), `agent "p": context_paths`) {
		t.Errorf("relative path: %v", err)
	}
	// An agent with no runtime of its own inherits an instance default of host.
	if err := (Config{BindAddr: "127.0.0.1:8899", DefaultRuntime: "host", Agents: []AgentConfig{a}}).Validate(); err == nil || !strings.Contains(err.Error(), `needs runtime "box"`) {
		t.Errorf("instance default host: %v", err)
	}
	// An explicit box beats the instance default.
	box := a
	box.Runtime = "box"
	if err := (Config{BindAddr: "127.0.0.1:8899", DefaultRuntime: "host", Agents: []AgentConfig{box}}).Validate(); err != nil {
		t.Errorf("explicit box under host default: %v", err)
	}
}
