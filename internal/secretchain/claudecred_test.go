package secretchain

import (
	"errors"
	"testing"

	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/config"
)

// oracle is a PresenceFunc over a fixed table; a name not in it is Absent. It
// records every name it was asked.
type oracle struct {
	ans   map[string]Presence
	errs  map[string]error
	asked []string
}

func (o *oracle) fn(name string) (Presence, error) {
	o.asked = append(o.asked, name)
	return o.ans[name], o.errs[name]
}

func TestClaudeBoxCredentialSecretPresent(t *testing.T) {
	sec := &oracle{ans: map[string]Presence{"CLAUDE_CODE_OAUTH_TOKEN": Present}}
	acct := &oracle{}
	a := config.AgentConfig{Secrets: []string{"CLAUDE_CODE_OAUTH_TOKEN"}, Account: "claude-box"}
	if p, err := ClaudeBoxCredential(a, sec.fn, acct.fn); p != Present || err != nil {
		t.Fatalf("= %v, %v; want Present", p, err)
	}
	if len(acct.asked) != 0 {
		t.Fatalf("account oracle was asked %v", acct.asked)
	}
}

func TestClaudeBoxCredentialIgnoresOtherSecrets(t *testing.T) {
	sec := &oracle{ans: map[string]Presence{"GITHUB_TOKEN": Present}}
	acct := &oracle{}
	a := config.AgentConfig{Secrets: []string{"GITHUB_TOKEN"}}
	if p, _ := ClaudeBoxCredential(a, sec.fn, acct.fn); p != Absent {
		t.Fatalf("= %v; want Absent", p)
	}
	if len(sec.asked) != 0 {
		t.Fatalf("secret oracle was asked %v for a non-claude secret", sec.asked)
	}
}

func TestClaudeBoxCredentialLockedBeatsAbsent(t *testing.T) {
	sec := &oracle{ans: map[string]Presence{"ANTHROPIC_API_KEY": StoredLocked, "CLAUDE_CODE_OAUTH_TOKEN": Absent}}
	acct := &oracle{ans: map[string]Presence{"claude-box": Present}}
	a := config.AgentConfig{Secrets: []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}, Account: "claude-box"}
	if p, _ := ClaudeBoxCredential(a, sec.fn, acct.fn); p != StoredLocked {
		t.Fatalf("= %v; want StoredLocked", p)
	}
	if len(acct.asked) != 0 {
		t.Fatalf("account oracle was asked %v", acct.asked)
	}
}

func TestClaudeBoxCredentialUnavailableBeatsLocked(t *testing.T) {
	e, e2 := errors.New("keychain"), errors.New("second")
	a := config.AgentConfig{Secrets: []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}}
	none := (&oracle{}).fn
	sec := &oracle{
		ans:  map[string]Presence{"ANTHROPIC_API_KEY": StoredLocked, "CLAUDE_CODE_OAUTH_TOKEN": StoredUnavailable},
		errs: map[string]error{"CLAUDE_CODE_OAUTH_TOKEN": e},
	}
	if p, err := ClaudeBoxCredential(a, sec.fn, none); p != StoredUnavailable || err != e {
		t.Fatalf("= %v, %v; want StoredUnavailable, %v", p, err, e)
	}
	// The first Keychain error is the one kept.
	sec = &oracle{
		ans:  map[string]Presence{"ANTHROPIC_API_KEY": StoredUnavailable, "CLAUDE_CODE_OAUTH_TOKEN": StoredUnavailable},
		errs: map[string]error{"ANTHROPIC_API_KEY": e, "CLAUDE_CODE_OAUTH_TOKEN": e2},
	}
	if p, err := ClaudeBoxCredential(a, sec.fn, none); p != StoredUnavailable || err != e {
		t.Fatalf("= %v, %v; want StoredUnavailable, %v", p, err, e)
	}
	// A present secret still wins over an unavailable one.
	sec = &oracle{ans: map[string]Presence{"ANTHROPIC_API_KEY": StoredUnavailable, "CLAUDE_CODE_OAUTH_TOKEN": Present}}
	if p, _ := ClaudeBoxCredential(a, sec.fn, none); p != Present {
		t.Fatalf("= %v; want Present", p)
	}
}

func TestClaudeBoxCredentialAccount(t *testing.T) {
	none := (&oracle{}).fn
	for _, tc := range []struct {
		name string
		ans  Presence
		err  error
		want Presence
	}{
		{"present", Present, nil, Present},
		{"locked", StoredLocked, nil, StoredLocked},
		{"unavailable", StoredUnavailable, errors.New("keychain"), StoredUnavailable},
		{"absent", Absent, nil, Absent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acct := &oracle{ans: map[string]Presence{"claude-box": tc.ans}, errs: map[string]error{"claude-box": tc.err}}
			p, err := ClaudeBoxCredential(config.AgentConfig{Account: "claude-box"}, none, acct.fn)
			if p != tc.want || err != tc.err {
				t.Fatalf("= %v, %v; want %v, %v", p, err, tc.want, tc.err)
			}
			if len(acct.asked) != 1 || acct.asked[0] != "claude-box" {
				t.Fatalf("account oracle asked %v; want [claude-box]", acct.asked)
			}
		})
	}
	for _, name := range []string{"", account.Default} {
		acct := &oracle{ans: map[string]Presence{"": Present, account.Default: Present}}
		if p, _ := ClaudeBoxCredential(config.AgentConfig{Account: name}, none, acct.fn); p != Absent || len(acct.asked) != 0 {
			t.Fatalf("account %q: = %v, asked %v; want Absent, never asked", name, p, acct.asked)
		}
	}
}

func TestProfileBoxCredentialFromChains(t *testing.T) {
	chain, err := New([]config.SecretSource{envSrc}, nil)
	if err != nil {
		t.Fatal(err)
	}
	known := account.Account{Name: "work", Provider: "claude", TokenSecret: "WORK_CLAUDE_TOKEN"}
	resolve := func(name string) (account.Account, error) {
		if name == known.Name {
			return known, nil
		}
		return account.Account{}, errors.New("no such account")
	}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "x")
	t.Setenv("WORK_CLAUDE_TOKEN", "y")
	fn := ProfileBoxCredential(chain, nil, resolve)

	if p := fn(config.AgentConfig{Secrets: []string{"CLAUDE_CODE_OAUTH_TOKEN"}}); p != Present {
		t.Fatalf("secret profile = %v; want Present", p)
	}
	if p := fn(config.AgentConfig{Account: "work"}); p != Present {
		t.Fatalf("account profile = %v; want Present", p)
	}
	if p := fn(config.AgentConfig{Account: "nope"}); p != Absent {
		t.Fatalf("unknown account = %v; want Absent", p)
	}
	if p := fn(config.AgentConfig{Secrets: []string{"ANTHROPIC_API_KEY"}}); p != Absent {
		t.Fatalf("unset secret = %v; want Absent", p)
	}
}
