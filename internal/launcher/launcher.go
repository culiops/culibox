// Package launcher composes the command + environment for a spawned agent.
// It owns the env model: base policy (clean/login/inherit), Claude-marker
// stripping, per-agent static env, and secret injection by reference. Secret
// values are read only here for injection and are never logged.
package launcher

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/culiops/agent-box/internal/account"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/secrets"
)

// SpawnSpec is the request to launch an agent.
type SpawnSpec struct {
	Agent   string
	Name    string // optional display label; blank -> working-dir basename
	Cwd     string
	Options map[string]any // per-spawn option overrides (profile keys only)
	Runtime string         // "" = use the agent's setting; box|host otherwise
	// Prompt is the agent's first message. Claude only (AcceptsPrompt),
	// validated by ValidatePrompt, and appended LAST after a "--": a prompt
	// can never be read as a flag, nor be swallowed by a variadic one.
	Prompt string
	// Resume continues a claude conversation (`--resume <id>`). Set only by
	// the session layer's Restart; must look like a session id.
	Resume string
	// Account overrides the profile's agent account; "" = the profile's (default
	// when that is unset too). Resolved by AccountFor, never trusted as a path.
	Account string
	// Login marks a spawn that exists only to sign an account in (Settings →
	// Log in). It is valid for a non-default claude account on a claude bin at
	// the host tier, and makes Build drop the profile's own auth secrets
	// instead of refusing them, so the session sees no login but the account's.
	Login bool
	// Replyable overrides the profile's reply default; nil = the default.
	// Read by ReplyableFor, never by Build.
	Replyable *bool
	// ExtraArgs are flags the session layer adds (the boxed config bundle's).
	// Build places them after the option flags and BEFORE the "--" that ends
	// options — appended afterwards, they would become operands.
	ExtraArgs []string
}

// Builder composes argv + env for a spawn.
type Builder interface {
	Build(spec SpawnSpec) (argv []string, env []string, err error)
}

// First-message and resume refusals. The messages name only a reason, never
// the text: they are returned to the API caller and logged.
var (
	ErrBadPrompt         = errors.New("invalid first message")
	ErrPromptUnsupported = errors.New("this profile's agent does not accept a first message")
	ErrBadResume         = errors.New("launcher: invalid resume session id")
)

// maxPromptBytes caps a first message. It travels in argv (visible to
// same-uid processes, bounded by ARG_MAX) and is typed by a human.
const maxPromptBytes = 8 << 10

// oneWordRe matches a prompt that is a single bare word. Measured on Claude
// Code 2.1.284: `claude -- doctor` runs the doctor SUBCOMMAND ("--" does not
// stop subcommand dispatch), so a one-word first message could run update,
// doctor, mcp, … instead of starting a chat.
var oneWordRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

// resumeIDRe mirrors session.sessionIDRe: a uuid-ish token, nothing a shell
// or a path could misread. The first character must be alphanumeric: a
// leading "-" would make `--resume <id>` read as a bare --resume (its value
// is OPTIONAL, so commander stops at the next flag-looking arg) followed by
// an injected flag such as "-p" or "--dangerously-skip-permissions".
var resumeIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,127}$`)

// ValidatePrompt accepts a first message: non-empty once trimmed, at most
// maxPromptBytes, valid UTF-8, no control characters except newline and tab,
// no Unicode format characters (bidi overrides, zero-width joiners, …,
// consistent with the transcript display sanitising in commit 9900d74), and
// more than one bare word.
func ValidatePrompt(p string) error {
	t := strings.TrimSpace(p)
	switch {
	case t == "":
		return fmt.Errorf("%w: it is empty", ErrBadPrompt)
	case len(t) > maxPromptBytes:
		return fmt.Errorf("%w: it is longer than 8 KiB", ErrBadPrompt)
	case !utf8.ValidString(t):
		return fmt.Errorf("%w: it is not valid UTF-8", ErrBadPrompt)
	}
	for _, r := range t {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return fmt.Errorf("%w: it contains a control character", ErrBadPrompt)
		}
		if unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("%w: it contains a Unicode format character", ErrBadPrompt)
		}
	}
	if oneWordRe.MatchString(t) {
		return fmt.Errorf("%w: a single word can be read as a claude subcommand — add a few more words", ErrBadPrompt)
	}
	return nil
}

// IsClaudeBin is THE claude test (the session package delegates to it).
func IsClaudeBin(bin string) bool { return bin != "" && filepath.Base(bin) == "claude" }

// AcceptsPrompt reports whether an agent binary takes a first message in
// argv. Claude only: codex could not be measured (not installed), and a
// refusal is the safe answer for an unmeasured CLI.
func AcceptsPrompt(bin string) bool { return IsClaudeBin(bin) }

// claudeSessionEnvBlocklist: Claude Code session-identity vars that make a
// spawned `claude` behave as a nested child session. Stripped from every base.
var claudeSessionEnvBlocklist = map[string]bool{
	"CLAUDECODE":                                true,
	"CLAUDE_CODE_CHILD_SESSION":                 true,
	"CLAUDE_CODE_SESSION_ID":                    true,
	"CLAUDE_CODE_ENTRYPOINT":                    true,
	"CLAUDE_CODE_EXECPATH":                      true,
	"CLAUDE_PID":                                true,
	"CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX": true,
}

// cleanBaseVars: the curated allowlist for base=clean (plus any LC_* and TERM).
var cleanBaseVars = []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG"}

// AgentLauncher builds commands from configured agents + a secret resolver.
// The agents map is refreshable at runtime (SetAgents) so newly stored profiles
// are spawnable without a restart.
type AgentLauncher struct {
	mu         sync.RWMutex
	agents     map[string]config.AgentConfig
	secrets    secrets.Resolver
	shell      string
	optionDefs map[string]config.OptionDef
	// defaultRuntime is the instance-level tier for agents that name none. It
	// lives here rather than being stamped onto each agent so that profiles
	// added later through SetAgents pick it up too.
	defaultRuntime string
	// accounts resolves agent accounts; nil means only the default exists.
	accounts AccountResolver
	// daemonSecretEnv names daemon-only variables (without the AB_ prefix) that
	// never reach a session through an inherited base.
	daemonSecretEnv map[string]bool
}

// AccountResolver is the launcher's view of the account store.
type AccountResolver interface {
	Resolve(name string) (account.Resolved, error)
}

// accountRefresher is optionally implemented by the resolver: Refresh brings a
// named account's config dir up to date with the default's (settings, MCP
// servers) so a new session sees changes made since the daemon started. Build
// calls it once per host-tier spawn; AccountFor, which the session manager also
// calls, never does.
type accountRefresher interface {
	Refresh(name string) error
}

var (
	// ErrCodexAccount: codex accounts validate and are stored, but cannot spawn yet.
	ErrCodexAccount = errors.New("codex accounts are not supported yet")
	// ErrAccountConflict wraps every refusal to mix an account with a profile
	// that would carry another login's credentials. Safe to show the caller.
	ErrAccountConflict = errors.New("account conflict")
)

// SetAccounts installs the account resolver. Call at boot.
func (l *AgentLauncher) SetAccounts(r AccountResolver) {
	l.mu.Lock()
	l.accounts = r
	l.mu.Unlock()
}

// AccountFor resolves which account a spawn runs under: the spawn override,
// else the profile's, else default. Without a resolver only default exists.
func (l *AgentLauncher) AccountFor(spec SpawnSpec) (account.Resolved, error) {
	cfg, ok := l.agent(spec.Agent)
	if !ok {
		return account.Resolved{}, fmt.Errorf("launcher: unknown agent %q", spec.Agent)
	}
	name := spec.Account
	if name == "" {
		name = cfg.Account
	}
	l.mu.RLock()
	r := l.accounts
	l.mu.RUnlock()
	if r == nil {
		if name == "" || name == account.Default {
			return account.DefaultResolved(""), nil
		}
		return account.Resolved{}, fmt.Errorf("%w %q (accounts not configured)", account.ErrUnknown, name)
	}
	return r.Resolve(name)
}

// checkAccount refuses combinations that would mix credentials. boxed is the
// spawn's effective tier: a boxed spawn never login-wraps (Build forces the
// clean base), so base=login only matters on the host.
func checkAccount(cfg config.AgentConfig, acct account.Resolved, boxed, login bool) error {
	if login {
		if acct.IsDefault {
			return fmt.Errorf("%w: login mode needs a named account", ErrAccountConflict)
		}
		if boxed {
			return fmt.Errorf("%w: login mode runs on the host tier only", ErrAccountConflict)
		}
	}
	if acct.IsDefault {
		return nil
	}
	if acct.Provider == account.ProviderCodex {
		return ErrCodexAccount
	}
	if !IsClaudeBin(cfg.Bin) {
		return fmt.Errorf("%w: profile %q (bin %s) cannot run under claude account %q", ErrAccountConflict, cfg.Name, cfg.Bin, acct.Name)
	}
	// $SHELL -l -i re-sources the rc files AFTER the env strip, and those can
	// re-export CLAUDE_CODE_OAUTH_TOKEN: the default login would reach this account.
	if !boxed && cfg.BasePolicy() == config.BaseLogin {
		return fmt.Errorf("%w: profile %q uses base=login, whose shell rc files can re-export another login's credentials; use base clean or inherit with account %q", ErrAccountConflict, cfg.Name, acct.Name)
	}
	if login { // Build drops the profile's auth env and secrets in login mode
		return nil
	}
	for k := range cfg.Env {
		if account.IsAuthEnv(k) {
			return fmt.Errorf("%w: profile %q sets %s; it cannot run under account %q", ErrAccountConflict, cfg.Name, k, acct.Name)
		}
	}
	for _, n := range cfg.Secrets {
		if account.IsAuthEnv(n) {
			return fmt.Errorf("%w: profile %q carries secret %s; it cannot run under account %q", ErrAccountConflict, cfg.Name, n, acct.Name)
		}
	}
	return nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// SetDefaultRuntime installs the instance-level tier default. Call once at boot.
func (l *AgentLauncher) SetDefaultRuntime(rt string) {
	l.mu.Lock()
	l.defaultRuntime = rt
	l.mu.Unlock()
}

// SetDaemonSecretEnv names daemon-only variables (the login password's env var)
// that must never reach a session through an inherited base. Every AB_* variable
// is stripped regardless; this adds names that do not carry the prefix.
func (l *AgentLauncher) SetDaemonSecretEnv(names ...string) {
	m := map[string]bool{}
	for _, n := range names {
		if n != "" {
			m[n] = true
		}
	}
	l.mu.Lock()
	l.daemonSecretEnv = m
	l.mu.Unlock()
}

func (l *AgentLauncher) tierOf(cfg config.AgentConfig) string {
	l.mu.RLock()
	def := l.defaultRuntime
	l.mu.RUnlock()
	return config.Config{DefaultRuntime: def}.RuntimeFor(cfg)
}

// SetAgents replaces the launchable profile set (config seeds merged with the
// runtime store).
func (l *AgentLauncher) SetAgents(agents []config.AgentConfig) {
	m := make(map[string]config.AgentConfig, len(agents))
	for _, a := range agents {
		m[a.Name] = a
	}
	l.mu.Lock()
	l.agents = m
	l.mu.Unlock()
}

func (l *AgentLauncher) agent(name string) (config.AgentConfig, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	c, ok := l.agents[name]
	return c, ok
}

// New indexes agents + option definitions and resolves a usable login shell.
func New(agents []config.AgentConfig, resolver secrets.Resolver, optionDefs []config.OptionDef) *AgentLauncher {
	m := make(map[string]config.AgentConfig, len(agents))
	for _, a := range agents {
		m[a.Name] = a
		if a.BasePolicy() == config.BaseLogin && len(a.Secrets) > 0 {
			log.Printf("config warning: agent %q is base=login and declares secrets; the login profile "+
				"already exposes profile env and can override injected values; use base=clean for contained/authoritative secrets", a.Name)
		}
	}
	defs := make(map[string]config.OptionDef, len(optionDefs))
	for _, o := range optionDefs {
		defs[o.Key] = o
	}
	return &AgentLauncher{agents: m, secrets: resolver, shell: config.DetectShell(), optionDefs: defs}
}

// RuntimeFor reports the effective tier for a spawn: the per-spawn override when
// set, else the agent's configured policy (which defaults to box).
func (l *AgentLauncher) RuntimeFor(spec SpawnSpec) (string, error) {
	cfg, ok := l.agent(spec.Agent)
	if !ok {
		return "", fmt.Errorf("launcher: unknown agent %q", spec.Agent)
	}
	if spec.Runtime != "" {
		return spec.Runtime, nil
	}
	return l.tierOf(cfg), nil
}

// BinFor reports the binary an agent profile runs. The session manager needs it
// to choose a config-bundle layout; it deliberately does not parse argv, which
// may be login-shell wrapped.
func (l *AgentLauncher) BinFor(spec SpawnSpec) (string, error) {
	cfg, ok := l.agent(spec.Agent)
	if !ok {
		return "", fmt.Errorf("launcher: unknown agent %q", spec.Agent)
	}
	return cfg.Bin, nil
}

// AWSFor returns a copy of the profile's AWS binding, or nil for a profile
// without one (or an unknown profile). The copy is the session's own: a later
// profile edit must never change the scope of a session already spawned.
func (l *AgentLauncher) AWSFor(agent string) *config.AWSBinding {
	cfg, ok := l.agent(agent)
	if !ok || cfg.AWS == nil {
		return nil
	}
	return cfg.AWS.Clone()
}

// CloneFor returns a copy of the profile's clone spec, or nil, nil for a profile
// without clone_of (or an unknown one). A stored clone that no longer validates
// is an error, never a partial spec: the spawn is refused rather than run
// against a half-understood clone.
func (l *AgentLauncher) CloneFor(agent string) (*config.Clone, error) {
	cfg, ok := l.agent(agent)
	if !ok {
		return nil, nil
	}
	return cfg.Clone()
}

// Clones returns the clone spec of every profile with clone_of set and an
// absolute, clean default_cwd, ordered by directory. It feeds the host-tier
// box-owned-directory check, which must cover a directory a box may have
// written even when the profile's clone spec no longer validates (a
// hand-edited profiles.json): such a profile contributes {Of, Dir} without
// shares, and is refused on its own spawn through CloneFor, which keeps its
// error behaviour. A profile with no usable directory has none to guard.
func (l *AgentLauncher) Clones() []config.Clone {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []config.Clone
	for _, cfg := range l.agents {
		if cfg.CloneOf == "" {
			continue
		}
		if cl, err := cfg.Clone(); err == nil && cl != nil {
			out = append(out, *cl)
		} else if d := cfg.DefaultCwd; filepath.IsAbs(d) && filepath.Clean(d) == d {
			out = append(out, config.Clone{Of: cfg.CloneOf, Dir: d})
		}
	}
	slices.SortFunc(out, func(a, b config.Clone) int { return strings.Compare(a.Dir, b.Dir) })
	return out
}

// Build composes (argv, env) for the agent per its base policy + env + secrets.
//
// A boxed spawn overrides the base policy on purpose. base=inherit would hand
// the agent the daemon's WHOLE environment, and the box forwards env by name —
// so inheriting would pipe every daemon secret into the container and undo the
// containment. base=login is equally wrong: it wraps argv in the HOST $SHELL,
// which need not exist in the image. Boxed therefore always composes from the
// curated base and never login-wraps.
func (l *AgentLauncher) Build(spec SpawnSpec) ([]string, []string, error) {
	cfg, ok := l.agent(spec.Agent)
	if !ok {
		return nil, nil, fmt.Errorf("launcher: unknown agent %q", spec.Agent)
	}

	// Validate the first message and the resume id BEFORE anything reads a
	// secret: a refusal must not cost a secret lookup.
	if spec.Prompt != "" {
		if !AcceptsPrompt(cfg.Bin) {
			return nil, nil, ErrPromptUnsupported
		}
		if err := ValidatePrompt(spec.Prompt); err != nil {
			return nil, nil, err
		}
	}
	if spec.Resume != "" && (!IsClaudeBin(cfg.Bin) || !resumeIDRe.MatchString(spec.Resume)) {
		return nil, nil, ErrBadResume
	}

	boxed := spec.Runtime == config.RuntimeBox ||
		(spec.Runtime == "" && l.tierOf(cfg) == config.RuntimeBox)

	acct, err := l.AccountFor(spec)
	if err != nil {
		return nil, nil, err
	}
	if err := checkAccount(cfg, acct, boxed, spec.Login); err != nil {
		return nil, nil, err
	}
	if !boxed && !acct.IsDefault {
		// A boxed spawn never reads the account's host config dir (it gets the
		// bundle), so refreshing it there would only do pointless work. A failed
		// refresh leaves the previous copy in place; the spawn goes on.
		l.mu.RLock()
		r := l.accounts
		l.mu.RUnlock()
		if rf, ok := r.(accountRefresher); ok {
			if err := rf.Refresh(acct.Name); err != nil {
				log.Printf("account %q: refresh before spawn failed (continuing): %v", acct.Name, err)
			}
		}
	}

	basePolicy := cfg.BasePolicy()
	if boxed {
		basePolicy = config.BaseClean
	}
	env := l.baseEnv(basePolicy)
	l.mu.RLock()
	daemonSecrets := l.daemonSecretEnv
	l.mu.RUnlock()
	for k := range env { // strip Claude session markers and daemon secrets from any base
		if claudeSessionEnvBlocklist[k] || strings.HasPrefix(k, "AB_") || daemonSecrets[k] {
			delete(env, k)
		}
	}
	for k, v := range cfg.Env { // static per-agent overrides
		env[k] = v
	}

	// Merge option defaults ⊕ per-spawn overrides (profile keys only). Apply the
	// ENV part at the static-env layer — BEFORE secrets — so an injected secret
	// always wins over an option env of the same name.
	opts := l.mergeOptions(cfg, spec)
	l.applyOptionEnv(opts, env)

	if !acct.IsDefault { // never let the daemon's own login reach another account
		for _, k := range account.AuthEnv {
			delete(env, k)
		}
	}

	for _, name := range cfg.Secrets { // secrets by reference (authoritative)
		if spec.Login && account.IsAuthEnv(name) {
			continue // login mode: the profile's own login never reaches the account
		}
		v, ok, err := l.secrets.Resolve(name)
		if err != nil {
			return nil, nil, fmt.Errorf("launcher: resolving secret %q for agent %q: %w", name, cfg.Name, err)
		}
		if !ok {
			return nil, nil, fmt.Errorf("launcher: agent %q declares secret %q but it is not resolvable", cfg.Name, name)
		}
		env[name] = v
	}
	if !acct.IsDefault {
		if !boxed { // in a box the per-account box home's .claude is the config dir
			env["CLAUDE_CONFIG_DIR"] = acct.ConfigDir
		}
		if acct.TokenSecret != "" {
			v, ok, err := l.secrets.Resolve(acct.TokenSecret)
			if err != nil {
				return nil, nil, fmt.Errorf("launcher: account %q: resolving token secret %s: %w", acct.Name, acct.TokenSecret, err)
			}
			if !ok {
				return nil, nil, fmt.Errorf("launcher: account %q: token secret %s is not resolvable (check secret_sources)", acct.Name, acct.TokenSecret)
			}
			env[account.TokenEnv] = v
		}
	}
	if _, ok := env["TERM"]; !ok { // explicit agent.env TERM wins; else default
		env["TERM"] = "xterm-256color"
	}

	// Agent command + option FLAGS appended to the cmd slice, so for base=login
	// they pass through shellJoin's single-quoting (never string-concatenated
	// post-quote — that would be an injection vector).
	cmd := append([]string{cfg.Bin}, cfg.Args...)
	if !acct.IsDefault && !boxed && acct.InheritPersonal {
		if p := filepath.Join(acct.ConfigDir, account.MCPFileName); fileExists(p) {
			cmd = append(cmd, "--mcp-config", p) // variadic: stays before flags/prompt
		}
	}
	cmd = l.applyOptionFlags(opts, cmd)
	if spec.Resume != "" {
		cmd = append(cmd, "--resume", spec.Resume)
	}
	cmd = append(cmd, spec.ExtraArgs...)
	// "--" then the prompt, LAST: options end here, so the prompt is one
	// operand whatever it starts with (see ValidatePrompt for the one case
	// "--" cannot cover).
	if p := strings.TrimSpace(spec.Prompt); p != "" {
		cmd = append(cmd, "--", p)
	}

	argv, err := l.argv(cfg, cmd, env["PATH"], boxed)
	if err != nil {
		return nil, nil, err
	}
	return argv, envSlice(env), nil
}

// mergeOptions layers per-spawn overrides over the profile's option defaults,
// keeping only overrides acceptOverride allows.
func (l *AgentLauncher) mergeOptions(cfg config.AgentConfig, spec SpawnSpec) map[string]any {
	merged := make(map[string]any, len(cfg.Options))
	for k, v := range cfg.Options {
		merged[k] = v
	}
	for k, v := range spec.Options {
		if l.acceptOverride(cfg, k, v) {
			merged[k] = v
		}
	}
	return merged
}

// acceptOverride is the one rule for a per-spawn option override: the profile
// exposes the key, and the value is valid for its type. An untrusted enum
// value never reaches the command.
func (l *AgentLauncher) acceptOverride(cfg config.AgentConfig, k string, v any) bool {
	if _, ok := cfg.Options[k]; !ok {
		return false
	}
	def, ok := l.optionDefs[k]
	return ok && validOptionValue(def, v)
}

// AcceptedOptions returns the overrides Build would apply for spec — what a
// restart must re-apply. nil when there are none.
func (l *AgentLauncher) AcceptedOptions(spec SpawnSpec) map[string]any {
	cfg, ok := l.agent(spec.Agent)
	if !ok {
		return nil
	}
	var out map[string]any
	for k, v := range spec.Options {
		if l.acceptOverride(cfg, k, v) {
			if out == nil {
				out = map[string]any{}
			}
			out[k] = v
		}
	}
	return out
}

// ReplyableFor is the reply opt-in a new session starts with: the per-spawn
// override when given, else the profile default — and never for a non-claude
// agent, whose sessions have no reply path at all.
func (l *AgentLauncher) ReplyableFor(spec SpawnSpec) bool {
	cfg, ok := l.agent(spec.Agent)
	if !ok || !IsClaudeBin(cfg.Bin) {
		return false
	}
	if spec.Replyable != nil {
		return *spec.Replyable
	}
	return cfg.Replyable
}

func validOptionValue(def config.OptionDef, v any) bool {
	switch def.Type {
	case config.OptBool:
		_, ok := v.(bool)
		return ok
	case config.OptEnum:
		s, ok := v.(string)
		return ok && contains(def.Choices, s)
	}
	return false
}

// applyOptionEnv sets option-mapped env vars. Enum values are validated against
// choices (an untrusted override with a bogus value is ignored, not injected).
func (l *AgentLauncher) applyOptionEnv(opts map[string]any, env map[string]string) {
	for _, k := range sortedKeys(opts) {
		def, ok := l.optionDefs[k]
		if !ok || def.Env == "" {
			continue
		}
		switch def.Type {
		case config.OptBool:
			if truthy(opts[k]) {
				env[def.Env] = "1"
			}
		case config.OptEnum:
			if s, ok := opts[k].(string); ok && contains(def.Choices, s) {
				env[def.Env] = s
			}
		}
	}
}

// applyOptionFlags appends option-mapped flags to the agent command. Enum values
// are validated against choices before use.
func (l *AgentLauncher) applyOptionFlags(opts map[string]any, cmd []string) []string {
	for _, k := range sortedKeys(opts) {
		def, ok := l.optionDefs[k]
		if !ok || def.Flag == "" {
			continue
		}
		switch def.Type {
		case config.OptBool:
			if truthy(opts[k]) {
				cmd = append(cmd, def.Flag)
			}
		case config.OptEnum:
			if s, ok := opts[k].(string); ok && contains(def.Choices, s) {
				cmd = append(cmd, def.Flag, s)
			}
		}
	}
	return cmd
}

func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// truthy coerces a JSON-decoded value to bool without panicking on wrong types.
func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1"
	case float64:
		return t != 0
	default:
		return false
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func (l *AgentLauncher) baseEnv(policy string) map[string]string {
	if policy == config.BaseInherit {
		out := map[string]string{}
		for _, kv := range os.Environ() {
			if i := strings.IndexByte(kv, '='); i >= 0 {
				out[kv[:i]] = kv[i+1:]
			}
		}
		return out
	}
	return curatedBase() // clean and login both start here
}

func curatedBase() map[string]string {
	out := map[string]string{}
	for _, k := range cleanBaseVars {
		if v, ok := os.LookupEnv(k); ok {
			out[k] = v
		}
	}
	for _, kv := range os.Environ() { // locale
		if strings.HasPrefix(kv, "LC_") {
			if i := strings.IndexByte(kv, '='); i >= 0 {
				out[kv[:i]] = kv[i+1:]
			}
		}
	}
	if out["PATH"] == "" {
		out["PATH"] = "/usr/local/bin:/usr/bin:/bin"
	}
	if out["HOME"] == "" {
		if h, err := os.UserHomeDir(); err == nil {
			out["HOME"] = h
		}
	}
	return out
}

// argv composes the command. base=login wraps in a login shell (exec so the
// agent keeps the process-group leadership the daemon's negative-pid kill needs;
// zsh needs -i to source ~/.zshrc). Non-login resolves bin against the COMPOSED
// PATH (not the daemon's), so a clean agent's custom PATH actually finds it.
func (l *AgentLauncher) argv(cfg config.AgentConfig, cmd []string, pathv string, boxed bool) ([]string, error) {
	if boxed {
		// Resolution belongs to the CONTAINER's PATH. The host may not have this
		// binary at all, and a host absolute path is meaningless inside the image.
		return cmd, nil
	}
	if cfg.BasePolicy() == config.BaseLogin {
		flags := []string{"-l", "-c"}
		if strings.Contains(filepath.Base(l.shell), "zsh") {
			flags = []string{"-l", "-i", "-c"}
		}
		return append(append([]string{l.shell}, flags...), "exec "+shellJoin(cmd)), nil
	}
	bin, err := lookPathIn(cmd[0], pathv)
	if err != nil {
		return nil, fmt.Errorf("launcher: agent %q binary %q not found on its PATH", cfg.Name, cmd[0])
	}
	return append([]string{bin}, cmd[1:]...), nil
}

// lookPathIn resolves bin against the given PATH string (not the daemon's env).
// A bin containing a slash is used verbatim.
func lookPathIn(bin, pathv string) (string, error) {
	if strings.Contains(bin, "/") {
		return bin, nil
	}
	for _, dir := range filepath.SplitList(pathv) {
		if dir == "" {
			continue
		}
		cand := filepath.Join(dir, bin)
		if info, err := os.Stat(cand); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return cand, nil
		}
	}
	return "", fmt.Errorf("not found")
}

// shellJoin single-quote-escapes each token for a shell -c string. bin/args
// come from trusted config, but for a base=login spawn the trailing "--
// <prompt>" tokens are the untrusted API prompt — shellJoin IS the trust
// boundary there, the one place that text passes through a shell parser.
// See TestShellJoinRoundTripsHostileText, which verifies the round trip
// (and no injected command execution) across sh/bash/zsh.
func shellJoin(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

func envSlice(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out) // deterministic
	return out
}
