// Package account models agent accounts: named provider logins that a session
// runs under. Each non-default account owns a config dir agent-box derives from
// its validated name; "default" is the implicit, pre-existing ~/.claude login.
package account

import (
	"errors"
	"fmt"
	"regexp"
)

const (
	Default        = "default"
	ProviderClaude = "claude"
	ProviderCodex  = "codex"
	// TokenEnv is the variable a claude account's TokenSecret is injected as.
	TokenEnv = "CLAUDE_CODE_OAUTH_TOKEN"
	// RateLimitsName is the file the embedded statusLine writes in a config dir.
	RateLimitsName = "agent-box-rate-limits.json"
	// MCPFileName holds the inherited MCP servers of an InheritPersonal account.
	MCPFileName = "agentbox-mcp.json"
)

// AuthEnv lists every variable that carries a provider credential or points at
// one. A non-default account's session never inherits any of them.
var AuthEnv = []string{
	"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
	"CLAUDE_CONFIG_DIR", "CODEX_HOME", "OPENAI_API_KEY",
}

func IsAuthEnv(name string) bool {
	for _, n := range AuthEnv {
		if n == name {
			return true
		}
	}
	return false
}

var (
	nameRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	secretNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

	ErrUnknown = errors.New("unknown account")
	ErrExists  = errors.New("account already exists")
)

// Account is what accounts.json stores: metadata only, never a secret value.
type Account struct {
	Name            string `json:"name"`
	Provider        string `json:"provider"`
	TokenSecret     string `json:"token_secret,omitempty"` // a secret NAME in secret_sources
	InheritPersonal bool   `json:"inherit_personal,omitempty"`
}

// Resolved is an account plus where it lives on this host.
type Resolved struct {
	Account
	ConfigDir string `json:"-"`
	IsDefault bool   `json:"-"`
}

// DefaultResolved is the implicit default account rooted at dir (~/.claude).
func DefaultResolved(dir string) Resolved {
	return Resolved{Account: Account{Name: Default, Provider: ProviderClaude}, ConfigDir: dir, IsDefault: true}
}

func ValidName(n string) error {
	if n == Default {
		return errors.New(`account name "default" is reserved`)
	}
	if !nameRe.MatchString(n) {
		return fmt.Errorf("invalid account name %q (want lowercase letters, digits, '-', max 32)", n)
	}
	return nil
}

func (a Account) Validate() error {
	if err := ValidName(a.Name); err != nil {
		return err
	}
	switch a.Provider {
	case ProviderClaude, ProviderCodex:
	default:
		return fmt.Errorf("account %q: invalid provider %q (want claude|codex)", a.Name, a.Provider)
	}
	if a.TokenSecret != "" && !secretNameRe.MatchString(a.TokenSecret) {
		return fmt.Errorf("account %q: invalid token secret name %q", a.Name, a.TokenSecret)
	}
	return nil
}
