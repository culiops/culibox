package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"regexp"
	"slices"
	"strings"

	"github.com/culiops/agent-box/internal/hostmatch"
)

// TokenEgress is one destination a static token's boxes may reach. Host is an
// exact name; there are no wildcards, because shared-tenant domains
// (*.atlassian.net) are exactly the wildcard that widens exfiltration.
type TokenEgress struct {
	Host    string `json:"host"`
	Port    int    `json:"port,omitempty"` // 0 = 443
	Private bool   `json:"private,omitempty"`
	CIDR    string `json:"cidr,omitempty"`
}

// EffectivePort is the TLS port the host is reached on: 443 when unset.
func (h TokenEgress) EffectivePort() int {
	if h.Port == 0 {
		return 443
	}
	return h.Port
}

// TokenMCP is the box-side MCP server that uses a token.
type TokenMCP struct {
	Server  string   `json:"server"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Extra   []string `json:"extra,omitempty"` // other entries this server also reads: at most 4, distinct, not self
}

// StaticToken is a host-owned long-lived token entry. The host says what the
// token is and where it may go; a profile only chooses it by name. Value says
// where the bytes live and is never copied into an entry's other fields.
type StaticToken struct {
	Name        string            `json:"name"`
	Value       KeyRef            `json:"value"`
	Env         []string          `json:"env"`
	ConfigEnv   map[string]string `json:"config_env,omitempty"` // NOT secret: shown in the UI and the broker reply
	Egress      []TokenEgress     `json:"egress"`
	MCP         *TokenMCP         `json:"mcp,omitempty"`
	Provider    string            `json:"provider,omitempty"`
	Label       string            `json:"label,omitempty"`
	Prod        bool              `json:"prod,omitempty"`
	Description string            `json:"description,omitempty"`
}

const (
	maxTokenEnv       = 4
	maxTokenConfigEnv = 16
	maxTokenValueLen  = 1024
	maxTokenEgress    = 16
	maxTokenMCPArgs   = 32
	maxTokenMCPExtra  = 4
	maxTokenLabel     = 32
	maxTokenDesc      = 200
)

var (
	tokenNameRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	tokenServerRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	tokenProviderRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

	// tokenPrivateRanges are the only ranges a private cidr may sit in. CGNAT
	// 100.64.0.0/10 is deliberately absent: it holds the tailnet.
	tokenPrivateRanges = []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("fc00::/7"),
	}
)

// ValidTokenName reports whether s is a well-formed static-token entry name.
func ValidTokenName(s string) bool { return tokenNameRe.MatchString(s) }

// tokenDeniedExact are names that change how a program runs or where it
// connects. Names that only carry a token (GH_TOKEN, GITHUB_TOKEN, ...) are
// allowed on purpose: delivering them is the point of an entry.
var tokenDeniedExact = func() map[string]bool {
	m := map[string]bool{}
	for _, n := range strings.Fields(`HOME PATH SHELL USER LOGNAME PWD TMPDIR TERM IFS ENV BASH_ENV PS4 PROMPT_COMMAND
		NODE_OPTIONS NODE_PATH NODE_EXTRA_CA_CERTS NODE_TLS_REJECT_UNAUTHORIZED NODE_USE_ENV_PROXY
		PYTHONPATH PYTHONHOME PYTHONSTARTUP PERL5LIB PERL5OPT RUBYOPT RUBYLIB SSL_CERT_FILE SSL_CERT_DIR
		CURL_CA_BUNDLE REQUESTS_CA_BUNDLE GIT_SSL_CAINFO GH_REPO GH_HOST GH_CONFIG_DIR GH_PROMPT_DISABLED
		GIT_ASKPASS SSH_ASKPASS GIT_SSH GIT_SSH_COMMAND GIT_EXEC_PATH GIT_TERMINAL_PROMPT
		GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM TF_CLI_ARGS
		CLAUDE_CONFIG_DIR CODEX_HOME
		AWS_CONFIG_FILE AWS_PROFILE AWS_DEFAULT_PROFILE AWS_REGION AWS_DEFAULT_REGION
		` +
		// Copy of broker.StaticCredEnv (config cannot import broker; a test in
		// broker keeps the two in step).
		`AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN AWS_SECURITY_TOKEN
		AWS_CONTAINER_CREDENTIALS_FULL_URI AWS_CONTAINER_CREDENTIALS_RELATIVE_URI
		AWS_CONTAINER_AUTHORIZATION_TOKEN AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE
		AWS_WEB_IDENTITY_TOKEN_FILE AWS_ROLE_ARN AWS_ROLE_SESSION_NAME AWS_SHARED_CREDENTIALS_FILE`) {
		m[n] = true
	}
	return m
}()

var tokenDeniedPrefixes = []string{"AGENTBOX_", "AB_", "LD_", "DYLD_", "GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_", "BASH_FUNC_", "TF_CLI_ARGS_"}

// TokenEnvDenied reports whether an entry may not set this variable in a box.
func TokenEnvDenied(name string) bool {
	if tokenDeniedExact[name] {
		return true
	}
	switch strings.ToLower(name) {
	case "http_proxy", "https_proxy", "all_proxy", "no_proxy":
		return true
	}
	if strings.HasPrefix(strings.ToLower(name), "npm_config_") {
		return true
	}
	for _, p := range tokenDeniedPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// Validate checks every entry rule. It never reads the value.
func (e StaticToken) Validate() error {
	f := func(field, format string, a ...any) error {
		return fmt.Errorf("static_tokens[%q].%s: %s", e.Name, field, fmt.Sprintf(format, a...))
	}
	if !tokenNameRe.MatchString(e.Name) {
		return f("name", "must be lowercase letters, digits and - (at most 63, starting with a letter or digit)")
	}
	if err := e.Value.Validate(); err != nil {
		return f("value", "%v", err)
	}
	if n := len(e.Env); n < 1 || n > maxTokenEnv {
		return f("env", "needs 1 to %d variable names, got %d", maxTokenEnv, n)
	}
	for i, n := range e.Env {
		if !ValidEnvName(n) {
			return f("env", "%q is not a valid variable name", n)
		}
		if TokenEnvDenied(n) {
			return f("env", "%q changes how programs run or connect and cannot carry a token", n)
		}
		if slices.Contains(e.Env[:i], n) {
			return f("env", "%q is listed twice", n)
		}
	}
	if len(e.ConfigEnv) > maxTokenConfigEnv {
		return f("config_env", "at most %d pairs, got %d", maxTokenConfigEnv, len(e.ConfigEnv))
	}
	for _, k := range slices.Sorted(maps.Keys(e.ConfigEnv)) {
		v := e.ConfigEnv[k]
		switch {
		case !ValidEnvName(k):
			return f("config_env", "key %q is not a valid variable name", k)
		case TokenEnvDenied(k):
			return f("config_env", "key %q changes how programs run or connect", k)
		case slices.Contains(e.Env, k):
			return f("config_env", "key %q is also in env", k)
		case len(v) > maxTokenValueLen:
			return f("config_env", "value of %q is over %d bytes", k, maxTokenValueLen)
		case hasControl(v):
			return f("config_env", "value of %q contains a control character", k)
		}
	}
	if len(e.Egress) > maxTokenEgress {
		return f("egress", "at most %d hosts, got %d", maxTokenEgress, len(e.Egress))
	}
	for i, h := range e.Egress {
		if err := h.validate(); err != nil {
			return f("egress", "entry %d (%q): %v", i, h.Host, err)
		}
	}
	if e.MCP != nil {
		if err := e.MCP.validate(e.Name); err != nil {
			return f("mcp", "%v", err)
		}
	}
	if e.Provider != "" && !tokenProviderRe.MatchString(e.Provider) {
		return f("provider", "%q must be lowercase letters, digits and - (at most 32)", e.Provider)
	}
	if len(e.Label) > maxTokenLabel || hasControl(e.Label) {
		return f("label", "at most %d characters, no control characters", maxTokenLabel)
	}
	if len(e.Description) > maxTokenDesc || hasControl(e.Description) {
		return f("description", "at most %d characters, no control characters", maxTokenDesc)
	}
	return nil
}

func (h TokenEgress) validate() error {
	n, ok := hostmatch.Normalize(h.Host)
	if !ok || n != h.Host || strings.Contains(h.Host, "*") {
		return errors.New("host must be an exact lowercase DNS name (no wildcard, no IP address)")
	}
	if h.Port < 0 || h.Port > 65535 {
		return fmt.Errorf("port %d out of range (want 1-65535, or 0 for 443)", h.Port)
	}
	if h.Private != (h.CIDR != "") {
		return errors.New("private and cidr go together: set both or neither")
	}
	if h.CIDR == "" {
		return nil
	}
	p, err := netip.ParsePrefix(h.CIDR)
	if err != nil {
		return fmt.Errorf("cidr %q: %v", h.CIDR, err)
	}
	if p != p.Masked() {
		return fmt.Errorf("cidr %q is not canonical (write %s)", h.CIDR, p.Masked())
	}
	if p.Addr().Is4In6() {
		return fmt.Errorf("cidr %q: write an IPv4 range as IPv4", h.CIDR)
	}
	if !slices.ContainsFunc(tokenPrivateRanges, func(r netip.Prefix) bool {
		return r.Addr().Is4() == p.Addr().Is4() && r.Contains(p.Addr()) && p.Bits() >= r.Bits()
	}) {
		return fmt.Errorf("cidr %q must lie inside 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16 or fc00::/7 (100.64.0.0/10 is refused: it holds the tailnet)", h.CIDR)
	}
	if p.Addr().Is6() && p.Bits() < 32 {
		return fmt.Errorf("cidr %q is too wide (IPv6 needs /32 or narrower)", h.CIDR)
	}
	return nil
}

func (m TokenMCP) validate(self string) error {
	if !tokenServerRe.MatchString(m.Server) {
		return fmt.Errorf("server %q must be lowercase letters, digits, _ and - (at most 63)", m.Server)
	}
	if m.Command == "" || hasControl(m.Command) {
		return errors.New("command is required and must not contain control characters")
	}
	if len(m.Args) > maxTokenMCPArgs {
		return fmt.Errorf("at most %d args, got %d", maxTokenMCPArgs, len(m.Args))
	}
	for _, a := range m.Args {
		if strings.Contains(a, "${") {
			return fmt.Errorf("arg %q contains ${: interpolation is not allowed", a)
		}
		if hasControl(a) {
			return errors.New("args must not contain control characters")
		}
	}
	if len(m.Extra) > maxTokenMCPExtra {
		return fmt.Errorf("extra names at most %d entries, got %d", maxTokenMCPExtra, len(m.Extra))
	}
	for i, x := range m.Extra {
		switch {
		case !tokenNameRe.MatchString(x):
			return fmt.Errorf("extra %q is not an entry name", x)
		case x == self:
			return fmt.Errorf("extra %q names the entry itself", x)
		case slices.Contains(m.Extra[:i], x):
			return fmt.Errorf("extra %q is listed twice", x)
		}
	}
	return nil
}

// Names is every variable the entry sets in a box: env plus config_env keys, sorted.
func (e StaticToken) Names() []string {
	out := slices.Clone(e.Env)
	for k := range e.ConfigEnv {
		out = append(out, k)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Clone is a deep copy.
func (e StaticToken) Clone() StaticToken {
	c := e
	c.Env = slices.Clone(e.Env)
	c.ConfigEnv = maps.Clone(e.ConfigEnv)
	c.Egress = slices.Clone(e.Egress)
	if e.MCP != nil {
		m := *e.MCP
		m.Args = slices.Clone(e.MCP.Args)
		m.Extra = slices.Clone(e.MCP.Extra)
		c.MCP = &m
	}
	return c
}

// ValidateStaticTokens checks each entry and the set: unique names, unique mcp
// servers, every extra naming an existing entry, and no variable shared between
// an entry and its extras (they would land in one process and one would shadow
// the other). Entries that share variables without extra are fine: they are
// alternative environments, and a profile picks one.
func ValidateStaticTokens(entries []StaticToken) error {
	names := map[string]bool{}
	servers := map[string]string{}
	for _, e := range entries {
		if err := e.Validate(); err != nil {
			return err
		}
		if names[e.Name] {
			return fmt.Errorf("static_tokens[%q]: duplicate name", e.Name)
		}
		names[e.Name] = true
		if e.MCP != nil {
			if other, dup := servers[e.MCP.Server]; dup {
				return fmt.Errorf("static_tokens[%q].mcp.server: %q is a duplicate (also used by %q)", e.Name, e.MCP.Server, other)
			}
			servers[e.MCP.Server] = e.Name
		}
	}
	for _, e := range entries {
		if e.MCP == nil {
			continue
		}
		for _, x := range e.MCP.Extra {
			if !names[x] {
				return fmt.Errorf("static_tokens[%q].mcp.extra: %q is not a defined entry", e.Name, x)
			}
			other, _ := TokenByName(entries, x)
			mine := e.Names()
			for _, n := range other.Names() {
				if _, found := slices.BinarySearch(mine, n); found {
					return fmt.Errorf("static_tokens[%q].mcp.extra: %q and %q both set %s", e.Name, e.Name, x, n)
				}
			}
		}
	}
	return nil
}

// TokenByName finds an entry. The result is a deep copy.
func TokenByName(entries []StaticToken, name string) (StaticToken, bool) {
	for _, e := range entries {
		if e.Name == name {
			return e.Clone(), true
		}
	}
	return StaticToken{}, false
}

// maxBoundTokens caps how many entries one profile may bind.
const maxBoundTokens = 16

// validateTokens checks a profile's tokens list on its own: boxed only, a
// bounded count, no duplicates and well-formed names. Whether the entries exist
// is CheckTokens' question. Like validateAWS, the runtime must be named "box":
// an empty runtime means "the instance default", which may be host.
func validateTokens(a AgentConfig) error {
	if len(a.Tokens) == 0 {
		return nil
	}
	if a.Runtime != RuntimeBox {
		return errors.New("tokens require runtime: box (set it explicitly); the host tier cannot contain credentials")
	}
	if len(a.Tokens) > maxBoundTokens {
		return fmt.Errorf("tokens: at most %d entries, got %d", maxBoundTokens, len(a.Tokens))
	}
	for i, n := range a.Tokens {
		if !tokenNameRe.MatchString(n) {
			return fmt.Errorf("tokens[%d] %q is not an entry name", i, n)
		}
		if slices.Contains(a.Tokens[:i], n) {
			return fmt.Errorf("tokens[%d] %q is listed twice", i, n)
		}
	}
	return nil
}

// CheckTokens refuses a profile that names entries the instance does not
// define, binds an entry that is only usable as a GitHub PAT, or would set one
// variable twice (an entry's variables against the profile's env, its secrets
// and the env of any option that applies to its agent).
func CheckTokens(a AgentConfig, entries []StaticToken, optDefs map[string]OptionDef) error {
	var bound []StaticToken
	for i, n := range a.Tokens {
		e, ok := TokenByName(entries, n)
		if !ok {
			return fmt.Errorf("tokens[%d] %q is not defined in static_tokens", i, n)
		}
		if len(e.Egress) == 0 {
			return fmt.Errorf("tokens[%d] %q has no egress hosts; it can only be a GitHub owner's token", i, n)
		}
		bound = append(bound, e)
	}
	if a.GitHub != nil {
		for i, o := range a.GitHub.Owners {
			if o.Token == "" {
				continue
			}
			if _, ok := TokenByName(entries, o.Token); !ok {
				return fmt.Errorf("github.owners[%d].token %q is not defined in static_tokens", i, o.Token)
			}
		}
	}
	for _, e := range bound {
		for _, n := range e.Names() {
			if _, set := a.Env[n]; set {
				return fmt.Errorf("token %q sets %s, which the profile also sets as an env entry", e.Name, n)
			}
			if slices.Contains(a.Secrets, n) {
				return fmt.Errorf("token %q sets %s, which the profile also sets as a secret", e.Name, n)
			}
			for _, k := range slices.Sorted(maps.Keys(optDefs)) {
				if d := optDefs[k]; d.Env == n && d.AppliesTo(a.Bin) {
					return fmt.Errorf("token %q sets %s, which the profile also sets as the env of option %q", e.Name, n, k)
				}
			}
		}
	}
	return nil
}

// EntryMCPArgs is the argument list of `agentbox-env` for an entry's MCP
// server: the entry, the other entries the server also reads, then the real
// command. Every extra must be bound to the same profile, or the server would
// start without a token it needs.
func EntryMCPArgs(e StaticToken, bound map[string]bool) ([]string, error) {
	if e.MCP == nil {
		return nil, fmt.Errorf("entry %q has no mcp server", e.Name)
	}
	args := []string{e.Name}
	for _, x := range e.MCP.Extra {
		if !bound[x] {
			return nil, fmt.Errorf("%s needs token %s, not bound to this profile", e.MCP.Server, x)
		}
		args = append(args, x)
	}
	args = append(args, "--", e.MCP.Command)
	return append(args, e.MCP.Args...), nil
}

// DefHash is 12 hex over everything about an entry except where its value
// lives. Name, label and the rest are covered because tokens.json, read by the
// in-box list command, sits in the binding directory this hash keys.
func (e StaticToken) DefHash() string {
	c := e.Clone()
	c.Value = KeyRef{}
	j, _ := json.Marshal(c)
	sum := sha256.Sum256(j)
	return hex.EncodeToString(sum[:6])
}

// TokensHash is 12 hex over the entries a session binds as tokens and as GitHub
// PATs. Order does not matter; the same entry in the two roles hashes apart.
func TokensHash(tokens, pats []StaticToken) string {
	lines := make([]string, 0, len(tokens)+len(pats))
	for _, e := range tokens {
		lines = append(lines, "t:"+e.Name+":"+e.DefHash())
	}
	for _, e := range pats {
		lines = append(lines, "p:"+e.Name+":"+e.DefHash())
	}
	slices.Sort(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:6])
}
