// Package account models agent accounts: named provider logins that a session
// runs under. Each non-default account owns a config dir agent-box derives from
// its validated name; "default" is the implicit, pre-existing ~/.claude login.
package account

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/culiops/culibox/internal/config"
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

// AuthEnv lists the variables that carry a provider credential or point at one
// by exact name. IsAuthEnv adds the provider-routing families by shape. A
// non-default account's session never inherits any of them.
var AuthEnv = []string{
	"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
	"CLAUDE_CONFIG_DIR", "CODEX_HOME", "OPENAI_API_KEY",
	// Named in claude 2.1.306's auth-export table but outside the shapes below:
	// login switches, a proxy and a gcloud token, and the identity hints that
	// would label an account's own session as the default login's org.
	"USE_LOCAL_OAUTH", "USE_STAGING_OAUTH", "AGENT_PROXY_AUTH_TOKEN", "CLOUDSDK_AUTH_ACCESS_TOKEN",
	"CLAUDE_CODE_FEDERATION_CACHE_DIR", "CLAUDE_CODE_ACCOUNT_UUID", "CLAUDE_CODE_ORGANIZATION_UUID",
	"CLAUDE_CODE_USER_EMAIL", "CLAUDE_CODE_ACCOUNT_TAGGED_ID", "CLAUDE_CODE_SUBSCRIPTION_TYPE",
	"CLAUDE_CODE_RATE_LIMIT_TIER", "CLAUDE_SECURESTORAGE_CONFIG_DIR", "CLAUDE_BRIDGE_BASE_URL",
	"CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST", "CLAUDE_BG_SOCKET_TOKENS_PATH",
}

// claudeCredSegments mark a CLAUDE_* variable as a credential, a pointer to
// one, or an auth/header override when any "_"-separated part equals one.
var claudeCredSegments = map[string]bool{
	"TOKEN": true, "KEY": true, "PASSPHRASE": true, "CERT": true, "SECRET": true,
	"CREDS": true, "CREDENTIALS": true, "OAUTH": true, "AUTH": true, "HEADER": true, "HEADERS": true,
}

// anthropicMetaSuffixes describe a model; they cannot route anything.
var anthropicMetaSuffixes = []string{"_MODEL", "_MODEL_NAME", "_MODEL_DESCRIPTION", "_SUPPORTED_CAPABILITIES"}

// IsAuthEnv reports whether name could hand a session another login: carry a
// credential, route inference to another provider (Bedrock, Vertex, Foundry and
// whatever Claude Code adds next) or point it at another endpoint. It fails
// closed on purpose: Claude Code keeps adding such variables, so the families
// go by shape. A false positive means a non-default account does not inherit
// that variable, and a profile that sets it cannot run under such an account.
//   - ANTHROPIC_*: all of it except model selection and metadata.
//   - AWS_BEARER_TOKEN_*: Bedrock API keys.
//   - CLAUDE_CODE_USE_*: the provider switches. CLAUDE_CODE_*BASE_URL.
//   - any CLAUDE_* with a claudeCredSegments part.
func IsAuthEnv(name string) bool {
	for _, n := range AuthEnv {
		if n == name {
			return true
		}
	}
	if rest, ok := strings.CutPrefix(name, "ANTHROPIC_"); ok {
		if rest == "" {
			return false
		}
		for _, suf := range anthropicMetaSuffixes {
			if strings.HasSuffix(name, suf) {
				return false
			}
		}
		return true
	}
	if rest, ok := strings.CutPrefix(name, "AWS_BEARER_TOKEN_"); ok {
		return rest != ""
	}
	rest, ok := strings.CutPrefix(name, "CLAUDE_")
	if !ok || rest == "" {
		return false
	}
	if code, ok := strings.CutPrefix(rest, "CODE_"); ok && (strings.HasPrefix(code, "USE_") || strings.HasSuffix(code, "BASE_URL")) {
		return true
	}
	for _, seg := range strings.Split(rest, "_") {
		if claudeCredSegments[seg] {
			return true
		}
	}
	return false
}

var (
	nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

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
	if a.TokenSecret != "" && !config.ValidEnvName(a.TokenSecret) {
		return fmt.Errorf("account %q: invalid token secret name %q", a.Name, a.TokenSecret)
	}
	return nil
}
