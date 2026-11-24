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

	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credref"
	"github.com/culiops/culibox/internal/datacompat"
	"github.com/culiops/culibox/internal/secretstore"
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
	// Clone is the per-spawn choice between the profile's clone setup and the
	// user's own folder. nil = the profile decides (today's behaviour, and every
	// old client). false = spawn as if the profile had no clone_of, clone_share or
	// clone_no_auto_return: in place, in Cwd. true = the profile must have a clone
	// setup, and the spawn is a box one. Applied by the session layer's Spawn; Build
	// never reads it.
	Clone *bool
	// HostDropsAck is the caller's confirmation that a host run drops what the
	// profile's box setup carries (config.HostDrops: context paths, box-only
	// settings) and has its bindings as the default credentials, not a limit
	// (config.HostGuardrail). Applied by the session layer's Spawn, which refuses a
	// host spawn of such a profile without it; Build never reads it. It does not
	// lift a box_only lock, and it is never persisted: a restart of a host
	// session sets it itself.
	HostDropsAck bool
	// ExtraArgs are flags the session layer adds (the boxed config bundle's).
	// Build places them after the option flags and BEFORE the "--" that ends
	// options — appended afterwards, they would become operands.
	ExtraArgs []string
	// ParentID is the session this one is a companion shell of. Set only by
	// the web layer's shell endpoint, never from a spawn request body. Build
	// ignores it; it is never put in an agent's env.
	ParentID string
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
// argv AND runs an interactive prompt where pasted text is a message, never
// commands: the review bar's Paste is gated on it too (session Info.AgentPrompt,
// #352). Enable it for another CLI only when both hold. Claude only: codex
// could not be measured (not installed), and a refusal is the safe answer for
// an unmeasured CLI.
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
	secrets    SecretResolver
	shell      string
	optionDefs map[string]config.OptionDef
	// defaultRuntime is the instance-level tier for agents that name none. It
	// lives here rather than being stamped onto each agent so that profiles
	// added later through SetAgents pick it up too.
	defaultRuntime string
	// accounts resolves agent accounts; nil means only the default exists.
	accounts AccountResolver
	// acctTokenStore reads the encrypted store directly, for an account's own
	// pasted token (account.OwnsStoreToken) when secret_sources has no store
	// source. nil: no fallback.
	acctTokenStore SecretResolver
	// daemonSecretEnv names daemon-only variables (without the AB_ prefix) that
	// never reach a session through an inherited base.
	daemonSecretEnv map[string]bool
	// tokens are the instance's static-token entries, read once at boot.
	tokens []config.StaticToken
	// awsConns are the instance's named AWS connections, read once at boot.
	awsConns []config.AWSConnection
	// boxGate, when set, can refuse a boxed spawn (nil = never).
	boxGate func() error
	// profilesGate, when set, refuses every spawn (see ErrProfilesNewer).
	profilesGate func() error
}

// SecretResolver resolves a declared secret name to its value. secrets.New's
// result and secretchain.Chain both satisfy it.
type SecretResolver interface {
	Resolve(name string) (string, bool, error)
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
	// ErrCodexAccount: codex accounts validate and are stored, so old configs
	// still load, but never spawn: Codex support is dropped (#89).
	ErrCodexAccount = errors.New("codex accounts are not supported")
	// ErrAccountConflict wraps every refusal to mix an account with a profile
	// that would carry another login's credentials. Safe to show the caller.
	ErrAccountConflict = errors.New("account conflict")
	// ErrStoreLocked refuses a spawn whose secrets or GitHub App key live in
	// the locked encrypted store. The text is fixed and secret-free so it can
	// be shown to the caller; the secret name is appended only to wrapped
	// errors, which go to the server log, never the response.
	ErrStoreLocked = errors.New(`store is locked — unlock it in the UI or with "culibox store unlock"`)
	// ErrStoreUnavailable refuses a spawn whose secrets live in a store whose
	// key the macOS Keychain did not hand over. Not a lock: there is no
	// passphrase to type. Wrapped errors also carry the secretstore
	// UnavailableError, whose message holds the fix.
	ErrStoreUnavailable = errors.New("the macOS Keychain did not give CuliBox the store key")
)

// storeLocked returns the ErrStoreLocked (naming the secret, for the log) to
// use when a resolve error means "the store is locked", else nil. A Keychain
// failure is ErrStoreUnavailable wrapping the UnavailableError (also naming the
// secret, for the log): never a lock, so the caller never offers an unlock.
func storeLocked(name string, err error) error {
	if errors.Is(err, credref.ErrLocked) {
		return fmt.Errorf("%w (%s)", ErrStoreLocked, name)
	}
	var ue *secretstore.UnavailableError
	if errors.As(err, &ue) {
		return fmt.Errorf("%w: %w (%s)", ErrStoreUnavailable, ue, name)
	}
	return nil
}

// SetAccounts installs the account resolver. Call at boot.
func (l *AgentLauncher) SetAccounts(r AccountResolver) {
	l.mu.Lock()
	l.accounts = r
	l.mu.Unlock()
}

// SetAccountTokenStore installs the store-only resolver behind an account's own
// pasted token (see acctTokenStore). Call at boot.
func (l *AgentLauncher) SetAccountTokenStore(r SecretResolver) {
	l.mu.Lock()
	l.acctTokenStore = r
	l.mu.Unlock()
}

// resolveAccountToken resolves an account's token_secret: the secret_sources
// chain first (config order is honoured), then, only for the account's own
// TokenID, the encrypted store directly. A chain error is returned as is, so a
// locked store source is never masked by the fallback.
func (l *AgentLauncher) resolveAccountToken(a account.Account) (string, bool, error) {
	v, ok, err := l.secrets.Resolve(a.TokenSecret)
	if err != nil || ok {
		return v, ok, err
	}
	l.mu.RLock()
	st := l.acctTokenStore
	l.mu.RUnlock()
	if st == nil || !a.OwnsStoreToken() {
		return "", false, nil
	}
	return st.Resolve(a.TokenSecret)
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
// is stripped regardless; this adds names that do not carry the prefix. The
// list is replaced, not merged, and is re-driven on every credential save: a
// spawn sees the old list or the new one.
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
func New(agents []config.AgentConfig, resolver SecretResolver, optionDefs []config.OptionDef) *AgentLauncher {
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

// RequestedRuntime reports the tier a spawn NAMED, as opposed to RuntimeFor's
// effective one: the per-spawn override, else the profile's own tier, else the
// instance default, else "" (nothing asked). RuntimeFor ends in box when nothing
// is named; this does not, so a caller can tell "asked for box" from "got box
// because nobody said otherwise".
func (l *AgentLauncher) RequestedRuntime(spec SpawnSpec) (string, error) {
	cfg, ok := l.agent(spec.Agent)
	if !ok {
		return "", fmt.Errorf("launcher: unknown agent %q", spec.Agent)
	}
	if spec.Runtime != "" {
		return spec.Runtime, nil
	}
	if cfg.Runtime != "" {
		return cfg.Runtime, nil
	}
	l.mu.RLock()
	def := l.defaultRuntime
	l.mu.RUnlock()
	return def, nil
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

// GitHubFor returns a deep copy of the profile's GitHub binding, or nil for a
// profile without one (or an unknown profile), for the same reason as AWSFor.
func (l *AgentLauncher) GitHubFor(agent string) *config.GitHubBinding {
	cfg, ok := l.agent(agent)
	if !ok || cfg.GitHub == nil {
		return nil
	}
	return cfg.GitHub.Clone()
}

// EgressFor returns the profile's network policy: a copy of egress_allow (nil
// when unset) and egress_open. An unknown profile has none.
func (l *AgentLauncher) EgressFor(agent string) ([]string, bool) {
	cfg, ok := l.agent(agent)
	if !ok {
		return nil, false
	}
	return slices.Clone(cfg.EgressAllow), cfg.EgressOpen
}

// LiveEgressFor is EgressFor for a session already running (#95): ok is false
// when the profile no longer exists or now names another tier than the box,
// and the session then fails closed. A profile that names no tier on an
// instance whose default is host still answers (its sessions were boxed per
// spawn): egress_allow and egress_open need an explicit runtime: box, so its
// policy is empty either way, and it must not read as removed.
func (l *AgentLauncher) LiveEgressFor(agent string) (allow []string, open, ok bool) {
	cfg, found := l.agent(agent)
	if !found || (cfg.Runtime != "" && l.tierOf(cfg) != config.RuntimeBox) {
		return nil, false, false
	}
	return slices.Clone(cfg.EgressAllow), cfg.EgressOpen, true
}

// SetStaticTokens installs the instance's static-token entries (deep-copied).
// Safe to call again (a credential save pushes the new list); a running
// session keeps the copy it was spawned with.
func (l *AgentLauncher) SetStaticTokens(entries []config.StaticToken) {
	cp := make([]config.StaticToken, len(entries))
	for i, e := range entries {
		cp[i] = e.Clone()
	}
	l.mu.Lock()
	l.tokens = cp
	l.mu.Unlock()
}

// ErrProfilesNewer refuses every spawn while profiles.json was written by a
// newer CuliBox (#291). That file may hold an override of a config seed (saving a
// seed in the UI writes one), so with it unreadable no profile, seeds included,
// can be trusted to be what the user saved. The wrapped text names the file, the
// version, this build's version and docs/UPGRADING.md.
var ErrProfilesNewer = errors.New("no session can start while profiles.json is from a newer CuliBox")

// ProfilesNewerGate is the gate for SetProfilesGate: non-nil only when parseErr
// is the "written by a newer CuliBox" state. Any other parse error, or none,
// gives nil, so a plain unparsable file behaves as it always has.
func ProfilesNewerGate(parseErr error) func() error {
	if !errors.Is(parseErr, datacompat.ErrNewer) {
		return nil
	}
	err := fmt.Errorf("%w: %v", ErrProfilesNewer, parseErr)
	return func() error { return err }
}

// SetProfilesGate installs the check SpawnRefusal and Build run first; a non-nil
// error refuses the spawn. nil removes it. Call once at boot.
func (l *AgentLauncher) SetProfilesGate(gate func() error) {
	l.mu.Lock()
	l.profilesGate = gate
	l.mu.Unlock()
}

// SpawnRefusal is why no spawn can start now, or nil. The session manager asks
// it first, before it prepares a clone or a box; Build asks again.
func (l *AgentLauncher) SpawnRefusal() error {
	l.mu.RLock()
	gate := l.profilesGate
	l.mu.RUnlock()
	if gate == nil {
		return nil
	}
	return gate()
}

// SetBoxGate installs a check that runs for every boxed spawn, before anything
// is resolved; a non-nil error refuses the spawn. Host-tier spawns never call
// it. Call once at boot. nil removes the gate.
func (l *AgentLauncher) SetBoxGate(gate func() error) {
	l.mu.Lock()
	l.boxGate = gate
	l.mu.Unlock()
}

// SeccompGate refuses boxed spawns while Docker's seccomp is off and an
// encrypted store exists. storeExists is read on every call, so `store init`
// while the daemon runs takes effect, and probe is not called at all while
// there is no store. With a store, probe runs on EVERY gated spawn and nothing
// is cached: docker's security options can change under a running daemon, and
// an error (we cannot tell) is refused too, so a failure clears itself on the
// next spawn.
func SeccompGate(probe func() (bool, error), storeExists func() bool) func() error {
	return func() error {
		if !storeExists() {
			return nil
		}
		on, err := probe()
		if err != nil {
			log.Printf("seccomp: could not read docker's security options (%v); boxed spawn refused", err)
			return box.ErrSeccompOff
		}
		if !on {
			return box.ErrSeccompOff
		}
		return nil
	}
}

// SetAWSConnections installs the instance's aws_connections (deep-copied).
// Safe to call again (a credential save pushes the new list); a running
// session keeps the copy it was spawned with.
func (l *AgentLauncher) SetAWSConnections(list []config.AWSConnection) {
	cp := make([]config.AWSConnection, len(list))
	for i, c := range list {
		cp[i] = c.Clone()
	}
	l.mu.Lock()
	l.awsConns = cp
	l.mu.Unlock()
}

// AWSConnectionsFor returns deep copies of the connections a profile binds,
// sorted by name so Bindings.Hash does not depend on the order the profile
// lists them. It is nil for a profile with none (or an unknown profile). The
// profile-binding rule is re-checked here (spawn is the second of its two
// checkpoints), and a name no connection defines is an error, never a partial
// binding: the spawn is refused rather than run with less than the profile asks.
func (l *AgentLauncher) AWSConnectionsFor(agent string) ([]config.AWSConnection, error) {
	cfg, ok := l.agent(agent)
	if !ok || len(cfg.AWSConnections) == 0 {
		return nil, nil
	}
	if err := config.ValidateAWSConnBinding(cfg); err != nil {
		return nil, err
	}
	l.mu.RLock()
	list := l.awsConns
	l.mu.RUnlock()
	out := make([]config.AWSConnection, 0, len(cfg.AWSConnections))
	for _, n := range cfg.AWSConnections {
		c, ok := config.AWSConnByName(list, n)
		if !ok {
			return nil, fmt.Errorf("profile %q names AWS connection %q, which is not defined in aws_connections", agent, n)
		}
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b config.AWSConnection) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// TokensFor returns deep copies of the entries a profile binds: tokens (its
// `tokens` list) and pats (the entries its GitHub owners name), each sorted by
// entry name, pats deduped by name, so Bindings.Hash does not depend on the
// order the profile lists them. Both are nil for a profile with none (or an
// unknown profile). A name no entry defines is an error, never a partial
// binding: the spawn is refused rather than run with less than the profile asks.
func (l *AgentLauncher) TokensFor(agent string) (tokens, pats []config.StaticToken, err error) {
	cfg, ok := l.agent(agent)
	if !ok {
		return nil, nil, nil
	}
	l.mu.RLock()
	entries := l.tokens
	l.mu.RUnlock()
	find := func(n string) (config.StaticToken, error) {
		e, ok := config.TokenByName(entries, n)
		if !ok {
			return config.StaticToken{}, fmt.Errorf("profile %q names token entry %q, which is not defined in static_tokens", agent, n)
		}
		return e, nil
	}
	for _, n := range cfg.Tokens {
		e, err := find(n)
		if err != nil {
			return nil, nil, err
		}
		tokens = append(tokens, e)
	}
	if cfg.GitHub != nil {
		for _, o := range cfg.GitHub.Owners {
			if o.Token == "" || slices.ContainsFunc(pats, func(e config.StaticToken) bool { return e.Name == o.Token }) {
				continue
			}
			e, err := find(o.Token)
			if err != nil {
				return nil, nil, err
			}
			pats = append(pats, e)
		}
	}
	byName := func(a, b config.StaticToken) int { return strings.Compare(a.Name, b.Name) }
	slices.SortFunc(tokens, byName)
	slices.SortFunc(pats, byName)
	return tokens, pats, nil
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

// HostBlockFor returns config.HostBlock for the profile: "" when the host tier
// is allowed, else the reason clause (only the box_only lock). An unknown
// profile is an error, so a spawn naming one is refused rather than treated as
// unrestricted.
func (l *AgentLauncher) HostBlockFor(agent string) (string, error) {
	cfg, ok := l.agent(agent)
	if !ok {
		return "", fmt.Errorf("launcher: unknown agent %q", agent)
	}
	return config.HostBlock(cfg), nil
}

// HostDropsFor returns config.HostDrops for the profile: what a host run would
// drop (context paths, box-only env and options), nil when nothing. An unknown
// profile is an error, as in HostBlockFor.
func (l *AgentLauncher) HostDropsFor(agent string) ([]string, error) {
	cfg, ok := l.agent(agent)
	if !ok {
		return nil, fmt.Errorf("launcher: unknown agent %q", agent)
	}
	return config.HostDrops(cfg, l.optionDefs), nil
}

// HostGuardrailFor returns config.HostGuardrail for the profile: the bindings a
// host run keeps as its default credentials, nil when unbound. An unknown
// profile is an error, as in HostDropsFor.
func (l *AgentLauncher) HostGuardrailFor(agent string) ([]string, error) {
	cfg, ok := l.agent(agent)
	if !ok {
		return nil, fmt.Errorf("launcher: unknown agent %q", agent)
	}
	return config.HostGuardrail(cfg), nil
}

// ContextPathsFor returns a copy of the profile's context_paths, or nil, nil
// for a profile without any (or an unknown one). A stored list that no longer
// validates is an error: the spawn is refused rather than mount a list that
// agent-box would not have accepted on save.
func (l *AgentLauncher) ContextPathsFor(agent string) ([]string, error) {
	cfg, ok := l.agent(agent)
	if !ok {
		return nil, nil
	}
	return cfg.ContextPathList()
}

// ImportsOffFor reports whether the profile turns off the files the bundled
// CLAUDE.md imports; false for an unknown profile.
func (l *AgentLauncher) ImportsOffFor(agent string) bool {
	cfg, ok := l.agent(agent)
	return ok && cfg.ImportsOff
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
	agents := make([]config.AgentConfig, 0, len(l.agents))
	for _, cfg := range l.agents {
		agents = append(agents, cfg)
	}
	l.mu.RUnlock()
	return ClonesOf(agents)
}

// BoxedDefaultCwds returns the non-empty default_cwd of every profile whose
// effective tier (its own runtime, else the instance default, else box) is
// config.RuntimeBox, in profile-name order. It seeds the boxed-workspace record
// on first boot, when profiles that already spawn boxed have workspaces the
// record has not yet seen; a later spawn records its own.
func (l *AgentLauncher) BoxedDefaultCwds() []string {
	l.mu.RLock()
	names := make([]string, 0, len(l.agents))
	for name := range l.agents {
		names = append(names, name)
	}
	l.mu.RUnlock()
	slices.Sort(names)
	var out []string
	for _, name := range names {
		cfg, ok := l.agent(name)
		if !ok || cfg.DefaultCwd == "" || l.tierOf(cfg) != config.RuntimeBox {
			continue
		}
		out = append(out, cfg.DefaultCwd)
	}
	return out
}

// ClonesOf is Clones over an explicit profile list, for callers (the
// `clone fetch` command) that read the profiles without a launcher.
func ClonesOf(agents []config.AgentConfig) []config.Clone {
	var out []config.Clone
	for _, cfg := range agents {
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
	if err := l.SpawnRefusal(); err != nil {
		return nil, nil, err
	}
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

	if boxed {
		l.mu.RLock()
		gate := l.boxGate
		l.mu.RUnlock()
		if gate != nil {
			if err := gate(); err != nil {
				return nil, nil, err
			}
		}
	}

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
	if !boxed {
		opts = config.StripBoxOnly(l.optionDefs, opts)
	}
	opts = l.claudeGateOptions(cfg, opts)
	l.applyOptionEnv(opts, env)

	if !acct.IsDefault { // never let the daemon's own login reach another account
		for k := range env {
			if account.IsAuthEnv(k) {
				delete(env, k)
			}
		}
	}

	for _, name := range cfg.Secrets { // secrets by reference (authoritative)
		if spec.Login && account.IsAuthEnv(name) {
			continue // login mode: the profile's own login never reaches the account
		}
		v, ok, err := l.secrets.Resolve(name)
		if err != nil {
			if locked := storeLocked(name, err); locked != nil {
				return nil, nil, locked
			}
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
			v, ok, err := l.resolveAccountToken(acct.Account)
			if err != nil {
				if locked := storeLocked(acct.TokenSecret, err); locked != nil {
					return nil, nil, locked
				}
				return nil, nil, fmt.Errorf("launcher: account %q: resolving token secret %s: %w", acct.Name, acct.TokenSecret, err)
			}
			if !ok {
				if acct.OwnsStoreToken() { // a pasted token needs no secret_sources entry
					return nil, nil, fmt.Errorf("launcher: account %q: no token is saved for it (paste one in Settings → Agent accounts)", acct.Name)
				}
				return nil, nil, fmt.Errorf("launcher: account %q: token secret %s is not resolvable (check secret_sources)", acct.Name, acct.TokenSecret)
			}
			env[account.TokenEnv] = v
		}
	}
	if _, ok := env["TERM"]; !ok { // explicit agent.env TERM wins; else default
		env["TERM"] = "xterm-256color"
	}
	if !boxed { // every env layer is written: a host process never sees a box-only name
		stripBoxOnlyEnv(env)
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
	return config.SupersedeLegacyBypass(l.optionDefs, merged)
}

// Reasons an override is dropped. The rejected value is never part of a
// reason: it is caller input and may be junk.
const (
	dropNotExposed   = "this profile does not expose this option"
	dropInvalidValue = "invalid value for this option"
)

// checkOverride is the one rule for a per-spawn option override: the profile
// offers the key (config.OptionOffered), and the value is valid for its
// type. An untrusted enum value never reaches the command. A blank enum is no
// override at all (the profile's default, else the agent's own): not ok, and
// no reason, so it is not reported as dropped. reason is "" when ok.
func (l *AgentLauncher) checkOverride(cfg config.AgentConfig, k string, v any) (ok bool, reason string) {
	def, known := l.optionDefs[k]
	if !known || !config.OptionOffered(def, cfg, l.optionDefs) {
		return false, dropNotExposed
	}
	if s, isStr := v.(string); isStr && s == "" && def.Type == config.OptEnum {
		return false, ""
	}
	if !validOptionValue(def, v) {
		return false, dropInvalidValue
	}
	return true, ""
}

// acceptOverride reports whether checkOverride keeps the override.
func (l *AgentLauncher) acceptOverride(cfg config.AgentConfig, k string, v any) bool {
	ok, _ := l.checkOverride(cfg, k, v)
	return ok
}

// acceptedOverrides returns the spec's overrides that acceptOverride keeps, nil
// when there are none. Runtime plays no part: a box-only key is kept here.
func (l *AgentLauncher) acceptedOverrides(cfg config.AgentConfig, spec SpawnSpec) map[string]any {
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

// AcceptedOptions returns the overrides Build would apply for spec — what a
// restart must re-apply. nil when there are none. A host spawn never carries a
// box-only option (Build strips it), so it is left out.
func (l *AgentLauncher) AcceptedOptions(spec SpawnSpec) map[string]any {
	cfg, ok := l.agent(spec.Agent)
	if !ok {
		return nil
	}
	out := l.acceptedOverrides(cfg, spec)
	if out != nil && spec.Runtime == config.RuntimeHost {
		out = config.StripBoxOnly(l.optionDefs, out)
		if len(out) == 0 {
			out = nil
		}
	}
	return out
}

// OptionDrop is one per-spawn override the profile refused: the key and why. The
// key is one the caller sent, or one the profile itself defaults (critical-rm
// on without Bypass). It never carries the value.
type OptionDrop struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

// Caps on what DroppedOptions reports: keys are caller input.
const (
	maxDroppedOptions  = 16
	maxDroppedKeyBytes = 64
)

// DroppedOptions returns the overrides in spec that Build would ignore (the
// complement of AcceptedOptions up to the cap), plus a profile-default key the
// Claude rules gate (critical-rm on without Bypass, even when the caller sent
// nothing). Sorted by key, capped at maxDroppedOptions entries with each key cut
// to maxDroppedKeyBytes. nil when all are accepted and for an unknown agent (the
// spawn fails elsewhere).
func (l *AgentLauncher) DroppedOptions(spec SpawnSpec) []OptionDrop {
	cfg, ok := l.agent(spec.Agent)
	if !ok {
		return nil
	}
	var out []OptionDrop
	for _, k := range sortedKeys(spec.Options) {
		if ok, reason := l.checkOverride(cfg, k, spec.Options[k]); !ok && reason != "" {
			out = append(out, OptionDrop{Key: truncateBytes(k, maxDroppedKeyBytes), Reason: reason})
		}
	}
	for _, d := range l.claudeDrops(cfg, l.mergeOptions(cfg, spec)) {
		if !slices.ContainsFunc(out, func(o OptionDrop) bool { return o.Key == d.Key }) {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	if len(out) > maxDroppedOptions {
		out = out[:maxDroppedOptions]
	}
	return out
}

// truncateBytes cuts s to at most n bytes without splitting a UTF-8 rune.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
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
	return append(cmd, config.OptionFlags(l.optionDefs, opts)...)
}

func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// truthy coerces a JSON-decoded value to bool; the rule lives in config.Truthy.
func truthy(v any) bool { return config.Truthy(v) }

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
		if boxShells[filepath.Base(cmd[0])] {
			return append([]string{"sh", "-c", boxShellFallback}, cmd...), nil
		}
		return cmd, nil
	}
	if cfg.BasePolicy() == config.BaseLogin {
		flags := []string{"-l", "-c"}
		if strings.Contains(filepath.Base(l.shell), "zsh") {
			flags = []string{"-l", "-i", "-c"}
		}
		var unset strings.Builder // a login shell's rc may set a box-only name: a host process never sees it
		for _, n := range config.BoxOnlyEnv {
			unset.WriteString("env -u " + n + " ")
		}
		return append(append([]string{l.shell}, flags...), "exec "+unset.String()+shellJoin(cmd)), nil
	}
	bin, err := lookPathIn(cmd[0], pathv)
	if err != nil {
		return nil, fmt.Errorf("launcher: agent %q binary %q not found on its PATH", cfg.Name, cmd[0])
	}
	return append([]string{bin}, cmd[1:]...), nil
}

// boxShells are the interactive shells a boxed profile may name by a host path
// the image lacks: the quick shell is the host's $SHELL, /opt/homebrew/bin/zsh
// on a Mac. An agent binary is never in this set: a missing agent must fail.
var boxShells = map[string]bool{
	"bash": true, "zsh": true, "fish": true, "sh": true, "dash": true,
	"ksh": true, "mksh": true, "tcsh": true, "csh": true,
}

// boxShellFallback runs inside the box (by dash, as `sh -c script shell args...`):
// the shell's path, else its basename on the box PATH, else /bin/bash with a
// notice and without the args, which were meant for the other shell. A -c
// among them (alone or bundled, as in -lc) means the profile runs a command through that shell, which an
// interactive bash would silently skip, so that fails instead. `command -v` is
// not enough for a path (dash reports any existing file), hence -f -x.
const boxShellFallback = `for b in "$0" "${0##*/}"; do
	case $b in
	*/*) [ -f "$b" ] && [ -x "$b" ] && exec "$b" "$@" ;;
	*) command -v "$b" >/dev/null 2>&1 && exec "$b" "$@" ;;
	esac
done
for a in "$@"; do
	case $a in
	--*) ;;
	-*c*)
		echo "culibox: $0 is not in the box image, so its -c command cannot run" >&2
		exit 127 ;;
	esac
done
echo "culibox: $0 is not in the box image; starting /bin/bash instead" >&2
exec /bin/bash`

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

// SessionLookPath finds bin on the PATH of a session's env (NAME=value; the
// last PATH wins, as exec takes it), never the daemon's, and returns its
// absolute path, or "" when it is not there. Relative PATH entries are skipped:
// the answer names one file whatever the cwd. A bin with a slash is not looked
// up ("").
func SessionLookPath(env []string, bin string) string {
	if strings.Contains(bin, "/") {
		return ""
	}
	pathv, found := "", false
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			pathv, found = v, true
		}
	}
	if !found {
		return ""
	}
	for _, dir := range filepath.SplitList(pathv) {
		if !filepath.IsAbs(dir) {
			continue
		}
		cand := filepath.Join(dir, bin)
		if info, err := os.Stat(cand); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return cand
		}
	}
	return ""
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
