package config

import (
	"errors"
	"fmt"
	"testing"
)

func TestWithFieldKeepsSentenceAndSentinel(t *testing.T) {
	err := WithField("context_paths", ErrContextNeedsBox)
	if !errors.Is(err, ErrContextNeedsBox) {
		t.Fatal("WithField must keep the wrapped error reachable through errors.Is")
	}
	if err.Error() != ErrContextNeedsBox.Error() {
		t.Fatalf("the sentence changed: %q, want %q", err.Error(), ErrContextNeedsBox.Error())
	}
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Path != "context_paths" {
		t.Fatalf("errors.As gave %+v, want Path context_paths", fe)
	}
}

func TestWithFieldInnermostWins(t *testing.T) {
	inner := errors.New("tokens[2] \"x\" is not defined")
	err := WithField("tokens", WithField("tokens[2]", inner))
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Path != "tokens[2]" {
		t.Fatalf("errors.As gave %+v, want the innermost path tokens[2]", fe)
	}
	// The same holds when the inner path sits behind a %w wrapper, as the
	// "profile %q: %w" wrappers put it.
	err = WithField("tokens", fmt.Errorf("profile %q: %w", "p", WithField("tokens[2]", inner)))
	if !errors.As(err, &fe) || fe.Path != "tokens[2]" {
		t.Fatalf("behind a wrapper: got %+v, want tokens[2]", fe)
	}
}

func TestWithFieldNilStaysNil(t *testing.T) {
	if err := WithField("name", nil); err != nil {
		t.Fatalf("WithField(nil) = %v, want nil", err)
	}
}

// TestProfileRefusalsNameTheirField pins which field each save-time refusal is
// about. The sentence of every row is pinned by the tests of the rule itself;
// this table pins only the path, and that the path survives the "profile %q: %w"
// wrapper ValidateProfile puts around it.
func TestProfileRefusalsNameTheirField(t *testing.T) {
	optDefs := map[string]OptionDef{
		"flag":  {Key: "flag", Type: OptBool, Flag: "--flag"},
		"model": {Key: "model", Type: OptEnum, Choices: []string{"a", "b"}, Flag: "--model", Env: "MODEL"},
	}
	box := func(a AgentConfig) AgentConfig { a.Runtime = RuntimeBox; return a }
	ok := func(a AgentConfig) AgentConfig {
		if a.Name == "" {
			a.Name = "p"
		}
		if a.Bin == "" {
			a.Bin = "claude"
		}
		return a
	}
	ghOwner := func(o GitHubOwner) AgentConfig {
		return box(ok(AgentConfig{GitHub: &GitHubBinding{Owners: []GitHubOwner{o}}}))
	}
	rows := []struct {
		name string
		a    AgentConfig
		want string
	}{
		{"no name", AgentConfig{Bin: "claude"}, "name"},
		{"no bin", AgentConfig{Name: "p"}, "bin"},
		{"CA env", ok(AgentConfig{Env: map[string]string{"SSL_CERT_FILE": "/x"}}), "env"},
		{"base", ok(AgentConfig{Base: "nope"}), "base"},
		{"runtime", ok(AgentConfig{Runtime: "nope"}), "runtime"},
		{"unknown option", ok(AgentConfig{Options: map[string]any{"nope": true}}), "options.nope"},
		{"option of the wrong type", ok(AgentConfig{Options: map[string]any{"flag": "yes"}}), "options.flag"},
		{"option outside its choices", ok(AgentConfig{Options: map[string]any{"model": "c"}}), "options.model"},
		{"option env that is a secret", ok(AgentConfig{Options: map[string]any{"model": "a"}, Secrets: []string{"MODEL"}}), "options.model"},

		{"inline aws on the host", ok(AgentConfig{Runtime: RuntimeHost, AWS: &AWSBinding{}}), "aws"},
		{"inline aws that is not valid", box(ok(AgentConfig{AWS: &AWSBinding{}})), "aws"},

		{"aws_connections on the host", ok(AgentConfig{Runtime: RuntimeHost, AWSConnections: []string{"a"}}), "aws_connections"},
		{"aws_connections name", box(ok(AgentConfig{AWSConnections: []string{"a", "Not A Name"}})), "aws_connections[1]"},
		{"aws_connections twice", box(ok(AgentConfig{AWSConnections: []string{"a", "a"}})), "aws_connections[1]"},
		{"aws and aws_connections", box(ok(AgentConfig{AWSConnections: []string{"a"}, AWS: &AWSBinding{
			SourceProfile: "sandbox-admin", RoleARN: "arn:aws:iam::123456789012:role/agentbox-agent", Region: "ap-southeast-1", S3Read: []string{"culishop-tfstate"}}})), "aws_connections"},

		{"github on the host", ok(AgentConfig{Runtime: RuntimeHost, GitHub: &GitHubBinding{}}), "github"},
		{"github without owners", box(ok(AgentConfig{GitHub: &GitHubBinding{}})), "github.owners"},
		{"github owner login", ghOwner(GitHubOwner{Owner: "-no", App: "x", Repos: []string{"r"}}), "github.owners[0].owner"},
		{"github owner without app or token", ghOwner(GitHubOwner{Owner: "acme", Repos: []string{"r"}}), "github.owners[0].app"},
		{"github owner token name", ghOwner(GitHubOwner{Owner: "acme", Token: "Not A Name", Repos: []string{"r"}}), "github.owners[0].token"},
		{"github owner without repos", ghOwner(GitHubOwner{Owner: "acme", App: "x"}), "github.owners[0].repos"},
		{"github repo name", ghOwner(GitHubOwner{Owner: "acme", App: "x", Repos: []string{"ok", "a b"}}), "github.owners[0].repos"},
		{"github empty permissions", box(ok(AgentConfig{GitHub: &GitHubBinding{
			Owners: []GitHubOwner{{Owner: "acme", App: "x", Repos: []string{"r"}}}, Permissions: map[string]string{}}})), "github.permissions"},
		{"github permission that cannot be written", box(ok(AgentConfig{GitHub: &GitHubBinding{
			Owners: []GitHubOwner{{Owner: "acme", App: "x", Repos: []string{"r"}}}, Permissions: map[string]string{"actions": "write"}}})), "github.permissions"},

		{"tokens on the host", ok(AgentConfig{Runtime: RuntimeHost, Tokens: []string{"a"}}), "tokens"},
		{"tokens name", box(ok(AgentConfig{Tokens: []string{"a", "Not A Name"}})), "tokens[1]"},

		{"egress_allow on the host", ok(AgentConfig{Runtime: RuntimeHost, EgressAllow: []string{"example.com"}}), "egress_allow"},
		{"egress_open on the host", ok(AgentConfig{Runtime: RuntimeHost, EgressOpen: true}), "egress_open"},
		{"egress_allow pattern", box(ok(AgentConfig{EgressAllow: []string{"example.com", "Bad Host"}})), "egress_allow[1]"},

		{"clone_of not absolute", box(ok(AgentConfig{CloneOf: "repo", DefaultCwd: "/srv/clone"})), "clone_of"},
		{"clone_of without default_cwd", box(ok(AgentConfig{CloneOf: "/srv/repo"})), "default_cwd"},
		{"clone default_cwd not absolute", box(ok(AgentConfig{CloneOf: "/srv/repo", DefaultCwd: "clone"})), "default_cwd"},
		{"clone dir inside the repo", box(ok(AgentConfig{CloneOf: "/srv/repo", DefaultCwd: "/srv/repo/clone"})), "clone_of"},
		{"clone_share without clone_of", box(ok(AgentConfig{CloneShare: []string{"docs"}})), "clone_share"},
		{"clone_share twice", box(ok(AgentConfig{CloneOf: "/srv/repo", DefaultCwd: "/srv/clone", CloneShare: []string{"docs", "docs"}})), "clone_share[1]"},
		{"clone_share nested", box(ok(AgentConfig{CloneOf: "/srv/repo", DefaultCwd: "/srv/clone", CloneShare: []string{"docs", "src", "docs/a"}})), "clone_share[2]"},

		{"context_paths on the host", ok(AgentConfig{Runtime: RuntimeHost, ContextPaths: []string{"/srv/a"}}), "context_paths"},
		{"context_paths not absolute", box(ok(AgentConfig{ContextPaths: []string{"/srv/a", "b"}})), "context_paths[1]"},
		{"context_paths twice", box(ok(AgentConfig{ContextPaths: []string{"/srv/a", "/srv/a"}})), "context_paths[1]"},
		{"context_paths nested", box(ok(AgentConfig{ContextPaths: []string{"/srv/a", "/srv/b", "/srv/a/c"}})), "context_paths[2]"},
		{"context_paths over default_cwd", box(ok(AgentConfig{ContextPaths: []string{"/srv/a", "/srv/w/x"}, DefaultCwd: "/srv/w"})), "context_paths[1]"},
		{"context_paths over clone_of", box(ok(AgentConfig{ContextPaths: []string{"/srv/repo/x"}, CloneOf: "/srv/repo", DefaultCwd: "/srv/clone"})), "context_paths[0]"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			err := ValidateProfile(row.a, optDefs)
			if err == nil {
				t.Fatal("ValidateProfile accepted the profile")
			}
			var fe *FieldError
			if !errors.As(err, &fe) {
				t.Fatalf("no field on %q, want %q", err, row.want)
			}
			if fe.Path != row.want {
				t.Errorf("field = %q, want %q (%s)", fe.Path, row.want, err)
			}
		})
	}
}

// TestCredentialRefChecksNameTheirField covers the checks against the instance's
// credential lists, which ValidateProfile does not run. The typed error the boot
// path tolerates must stay reachable behind the field.
func TestCredentialRefChecksNameTheirField(t *testing.T) {
	tok := func(name string, env ...string) StaticToken {
		return StaticToken{Name: name, Env: env, Egress: []TokenEgress{{Host: "api.example.com"}}}
	}
	conns := []AWSConnection{{Name: "a"}}
	apps := []GitHubApp{{Name: "app"}}
	tokens := []StaticToken{tok("t1", "T1"), tok("t2", "T2"), {Name: "pat"}}
	owner := func(o ...GitHubOwner) *GitHubBinding { return &GitHubBinding{Owners: o} }
	rows := []struct {
		name string
		a    AgentConfig
		want string
	}{
		{"undefined connection", AgentConfig{AWSConnections: []string{"a", "b"}}, "aws_connections[1]"},
		{"undefined App", AgentConfig{GitHub: owner(GitHubOwner{App: "app"}, GitHubOwner{App: "gone"})}, "github.owners[1].app"},
		{"undefined token", AgentConfig{Tokens: []string{"t1", "gone"}}, "tokens[1]"},
		{"token that only a PAT can be", AgentConfig{Tokens: []string{"pat"}}, "tokens[0]"},
		{"undefined PAT token", AgentConfig{GitHub: owner(GitHubOwner{App: "app"}, GitHubOwner{Token: "gone"})}, "github.owners[1].token"},
		{"token variable also in env", AgentConfig{Tokens: []string{"t1", "t2"}, Env: map[string]string{"T2": "x"}}, "tokens[1]"},
		{"token variable also a secret", AgentConfig{Tokens: []string{"t1"}, Secrets: []string{"T1"}}, "tokens[0]"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			err := errors.Join(
				CheckAWSConnections(row.a, conns),
				CheckGitHubApps(row.a, apps),
			)
			if err == nil {
				err = CheckTokens(row.a, tokens, nil)
			}
			if err == nil {
				t.Fatal("no check refused the profile")
			}
			var fe *FieldError
			if !errors.As(err, &fe) || fe.Path != row.want {
				t.Fatalf("field = %+v, want %q (%s)", fe, row.want, err)
			}
		})
	}

	// The boot path tolerates an undefined reference by its type; the field
	// wrapper must not hide it.
	err := CheckCredentialRefs(AgentConfig{Tokens: []string{"gone"}}, Config{}, nil)
	var ur *UndefinedRefError
	if !errors.As(err, &ur) || ur.Kind != RefToken || ur.Name != "gone" {
		t.Fatalf("UndefinedRefError not reachable behind the field: %v", err)
	}
}
