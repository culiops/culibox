package web

import (
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/culiops/agent-box/internal/account"
	"github.com/culiops/agent-box/internal/awsscope"
	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/launcher"
	"github.com/culiops/agent-box/internal/store"
)

// ProfileOption is one option a profile exposes, for the spawn-form UI. It
// carries no secret names/values or flag/env mapping.
type ProfileOption struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Type    string   `json:"type"`
	Choices []string `json:"choices,omitempty"`
	Value   any      `json:"value"`
}

// ProfileView is a profile for GET /api/profiles. Editable = came from the
// runtime store (config seeds are read-only). Runtime is the EFFECTIVE tier the
// spawn form should open on — already resolved against the instance default, so
// the client never has to re-derive containment policy.
type ProfileView struct {
	Name       string          `json:"name"`
	Account    string          `json:"account,omitempty"`
	Editable   bool            `json:"editable"`
	Runtime    string          `json:"runtime"`
	DefaultCwd string          `json:"default_cwd,omitempty"`
	Options    []ProfileOption `json:"options,omitempty"`
	// AcceptsPrompt: the spawn form may offer a first message (launcher.AcceptsPrompt).
	AcceptsPrompt bool `json:"accepts_prompt,omitempty"`
	// Claude: replies (and their default) apply to this profile at all.
	Claude bool `json:"claude,omitempty"`
	// Replyable is the profile's reply default — only ever true for claude.
	Replyable bool `json:"replyable,omitempty"`
	// BypassArgs: the profile's own args already bypass permission checks, so
	// the spawn form must warn when replies are on even with the option off.
	BypassArgs bool `json:"bypass_args,omitempty"`
}

// profileStore is the subset of *store.Store the manager needs (injectable).
type profileStore interface {
	List() []config.AgentConfig
	Get(name string) (config.AgentConfig, bool)
	Save(config.AgentConfig) error
	Delete(name string) error
}

// ProfileManager merges read-only config seeds with the editable runtime store,
// validates saves, and pushes the merged set to the launcher on every change.
type ProfileManager struct {
	mu        sync.Mutex
	seeds     map[string]config.AgentConfig
	seedOrder []string
	options   []config.OptionDef
	optDefs   map[string]config.OptionDef
	store     profileStore
	apply     func([]config.AgentConfig) // e.g. launcher.SetAgents
	// defaultRuntime is the instance-level tier for profiles that name none. It
	// is a constructor argument rather than a setter so that no construction
	// site can leave the containment default implicit.
	defaultRuntime string
	home           string // for save-time boxability checks on default_cwd
	// protected are the directories a boxed default_cwd must not overlap (data
	// root, config and profiles directories; box.ProtectedOverlap). nil only in
	// tests.
	protected []box.ProtectedDir
	// accounts validates a profile's agent account on save; nil = only the
	// default account exists. Spawn-time checkAccount stays the authority.
	accounts launcher.AccountResolver
	// cloneCheck is the disk-side check of a clone profile's repo (clone.CheckOf,
	// wired at boot); nil = pure validation only. Spawn re-checks.
	cloneCheck func(config.Clone) error
	// ghApps are the instance's github_apps connections (config.GitHubApps),
	// wired at boot, so a profile cannot be saved naming an App that does not
	// exist. nil = none defined.
	ghApps []config.GitHubApp
	// tokens are the instance's static_tokens entries (config.StaticTokens),
	// wired at boot, so a profile cannot be saved naming an entry that does
	// not exist. nil = none defined.
	tokens []config.StaticToken
	// awsConns are the instance's aws_connections (config.AWSConnections),
	// wired at boot, so a profile cannot be saved naming a connection that does
	// not exist. nil = none defined.
	awsConns []config.AWSConnection
}

// SetAWSConnections installs the instance's named AWS connections, which Save
// checks a profile's aws_connections against. Safe to call again on a credential save.
func (m *ProfileManager) SetAWSConnections(list []config.AWSConnection) {
	cp := make([]config.AWSConnection, len(list))
	for i, c := range list {
		cp[i] = c.Clone()
	}
	m.mu.Lock()
	m.awsConns = cp
	m.mu.Unlock()
}

// SetStaticTokens installs the instance's static token entries, which Save
// checks a profile's tokens and GitHub PAT owners against. Safe to call again on a credential save.
func (m *ProfileManager) SetStaticTokens(entries []config.StaticToken) {
	cp := make([]config.StaticToken, len(entries))
	for i, e := range entries {
		cp[i] = e.Clone()
	}
	m.mu.Lock()
	m.tokens = cp
	m.mu.Unlock()
}

// SetGitHubApps installs the instance's GitHub App connections, which Save
// checks a profile's github binding against. Safe to call again on a credential save.
func (m *ProfileManager) SetGitHubApps(apps []config.GitHubApp) {
	m.mu.Lock()
	m.ghApps = slices.Clone(apps)
	m.mu.Unlock()
}

// GitHubAppNames are the names of the instance's github_apps connections, for
// the profile editor's App picker. Names only: the client id and the key path
// stay on the host. Never nil, so it encodes as [].
func (m *ProfileManager) GitHubAppNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, len(m.ghApps))
	for i, a := range m.ghApps {
		names[i] = a.Name
	}
	return names
}

// TokenHostMeta is one destination of a token entry, as the picker shows it.
type TokenHostMeta struct {
	Host    string `json:"host"`
	Port    int    `json:"port"` // the TLS port: 443 when the entry declares none
	Private bool   `json:"private,omitempty"`
}

// TokenMeta is what the profile editor may know of a static_tokens entry: its
// name, how it is shown, where it may go and which env names it fills. Never
// the value, the reference it is read from, or a config_env value.
type TokenMeta struct {
	Name          string          `json:"name"`
	Provider      string          `json:"provider,omitempty"`
	Label         string          `json:"label,omitempty"`
	Prod          bool            `json:"prod,omitempty"`
	Description   string          `json:"description,omitempty"`
	Env           []string        `json:"env"`
	ConfigEnvKeys []string        `json:"config_env_keys"`
	Hosts         []TokenHostMeta `json:"hosts"`
	MCPServer     string          `json:"mcp_server,omitempty"`
}

// StaticTokenMeta lists the instance's static_tokens entries for the profile
// editor's Tokens picker, in config order. Never nil, so it encodes as [].
func (m *ProfileManager) StaticTokenMeta() []TokenMeta {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]TokenMeta, 0, len(m.tokens))
	for _, e := range m.tokens {
		tm := TokenMeta{Name: e.Name, Provider: e.Provider, Label: e.Label, Prod: e.Prod, Description: e.Description,
			Env: slices.Clone(e.Env), ConfigEnvKeys: slices.Sorted(maps.Keys(e.ConfigEnv)), Hosts: []TokenHostMeta{}}
		if tm.Env == nil {
			tm.Env = []string{}
		}
		if tm.ConfigEnvKeys == nil {
			tm.ConfigEnvKeys = []string{}
		}
		for _, h := range e.Egress {
			tm.Hosts = append(tm.Hosts, TokenHostMeta{Host: h.Host, Port: h.EffectivePort(), Private: h.Private})
		}
		if e.MCP != nil {
			tm.MCPServer = e.MCP.Server
		}
		out = append(out, tm)
	}
	return out
}

// AWSConnectionMeta is what the profile editor may know of an aws_connections
// entry: its name, how it is shown, the account and region it reaches and
// whether it can be widened to write. Never the source profile or a role ARN.
type AWSConnectionMeta struct {
	Name         string `json:"name"`
	Label        string `json:"label"`
	AccountID    string `json:"account_id"`
	Region       string `json:"region"`
	Description  string `json:"description"`
	Prod         bool   `json:"prod"`
	WriteCapable bool   `json:"write_capable"`
}

// AWSConnectionMetaList lists the instance's aws_connections for the profile
// editor's picker, in config order. Never nil, so it encodes as [].
func (m *ProfileManager) AWSConnectionMetaList() []AWSConnectionMeta {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AWSConnectionMeta, 0, len(m.awsConns))
	for _, c := range m.awsConns {
		out = append(out, AWSConnectionMeta{Name: c.Name, Label: c.Label, AccountID: awsscope.AccountOf(c.RoleARN),
			Region: c.Region, Description: c.Description, Prod: c.Prod, WriteCapable: c.Elevate != nil})
	}
	return out
}

// SetAccounts installs the account resolver used by Save. Call at boot.
func (m *ProfileManager) SetAccounts(r launcher.AccountResolver) {
	m.mu.Lock()
	m.accounts = r
	m.mu.Unlock()
}

// SetCloneCheck installs the save-time check of a clone profile's repo. Call
// at boot; nil leaves only the pure validation.
func (m *ProfileManager) SetCloneCheck(f func(config.Clone) error) {
	m.mu.Lock()
	m.cloneCheck = f
	m.mu.Unlock()
}

// NewProfileManager wires seeds + store + launcher-apply and does the initial
// refresh. defaultRuntime is the instance tier (config.DefaultRuntime); "" means
// box, matching config.AgentConfig.RuntimePolicy. protected are the directories
// a boxed default_cwd must not overlap (the same set the box manager refuses).
func NewProfileManager(seeds []config.AgentConfig, options []config.OptionDef, defaultRuntime string, protected []box.ProtectedDir, st profileStore, apply func([]config.AgentConfig)) *ProfileManager {
	m := &ProfileManager{
		seeds:          map[string]config.AgentConfig{},
		options:        options,
		optDefs:        map[string]config.OptionDef{},
		store:          st,
		apply:          apply,
		defaultRuntime: defaultRuntime,
		protected:      protected,
	}
	for _, s := range seeds {
		if _, dup := m.seeds[s.Name]; !dup {
			m.seedOrder = append(m.seedOrder, s.Name)
		}
		m.seeds[s.Name] = s
	}
	for _, o := range options {
		m.optDefs[o.Key] = o
	}
	m.home, _ = os.UserHomeDir()
	m.logLegacyBoxImages()
	m.apply(m.merged())
	return m
}

// logLegacyBoxImages names every profile that still carries the retired
// per-profile box.image (merged ignores it). Once per profile per call: a
// stored profile, and a seed it does not shadow.
func (m *ProfileManager) logLegacyBoxImages() {
	logIgnored := func(a config.AgentConfig) {
		if img := a.Box.Image; strings.TrimSpace(img) != "" {
			log.Printf("profile %q: ignoring box.image %q; boxes run the instance-wide box_image", a.Name, img)
		}
	}
	stored := map[string]bool{}
	for _, p := range m.store.List() {
		stored[p.Name] = true
		logIgnored(p)
	}
	for _, name := range m.seedOrder {
		if !stored[name] {
			logIgnored(m.seeds[name])
		}
	}
}

// scopeOptions returns a copy of a with any KNOWN option not applicable to its
// bin removed, so a legacy stored profile can never emit an inapplicable flag/env
// at spawn. Unknown keys are left as-is (the launcher already ignores them).
// It also drops the retired per-profile Box, so no caller ever sees one.
func (m *ProfileManager) scopeOptions(a config.AgentConfig) config.AgentConfig {
	a.Box = config.BoxConfig{}
	if len(a.Options) == 0 {
		return a
	}
	scoped := make(map[string]any, len(a.Options))
	for k, v := range a.Options {
		if def, ok := m.optDefs[k]; ok && !def.AppliesTo(a.Bin) {
			continue // drop inapplicable option (self-heal)
		}
		scoped[k] = v
	}
	a.Options = scoped
	return a
}

// merged is the effective set: a stored profile overrides a same-named seed;
// order = config seeds first, then store-only profiles (sorted). Every
// returned profile's Options is scoped to its bin (see scopeOptions) so
// callers (launcher apply, Views, Get) never see an inapplicable option even
// for a legacy stored profile created before bin-scoping was enforced.
func (m *ProfileManager) merged() []config.AgentConfig {
	stored := map[string]config.AgentConfig{}
	for _, p := range m.store.List() {
		stored[p.Name] = p
	}
	var out []config.AgentConfig
	seen := map[string]bool{}
	for _, name := range m.seedOrder {
		seen[name] = true
		if p, ok := stored[name]; ok {
			out = append(out, m.scopeOptions(p))
		} else {
			out = append(out, m.scopeOptions(m.seeds[name]))
		}
	}
	var extra []config.AgentConfig
	for name, p := range stored {
		if !seen[name] {
			extra = append(extra, m.scopeOptions(p))
		}
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i].Name < extra[j].Name })
	return append(out, extra...)
}

// Views returns the merged profiles + their options for the UI.
func (m *ProfileManager) Views() []ProfileView {
	m.mu.Lock()
	defer m.mu.Unlock()
	editable := map[string]bool{}
	for _, p := range m.store.List() {
		editable[p.Name] = true
	}
	merged := m.merged()
	out := make([]ProfileView, 0, len(merged))
	for _, a := range merged {
		pv := ProfileView{
			Name:          a.Name,
			Account:       a.Account,
			Editable:      editable[a.Name],
			Runtime:       config.Config{DefaultRuntime: m.defaultRuntime}.RuntimeFor(a),
			DefaultCwd:    a.DefaultCwd,
			AcceptsPrompt: launcher.AcceptsPrompt(a.Bin),
			Claude:        launcher.IsClaudeBin(a.Bin),
			Replyable:     a.Replyable && launcher.IsClaudeBin(a.Bin),
			BypassArgs:    argsBypassPermissions(a.Args),
		}
		for _, def := range m.options {
			if v, ok := a.Options[def.Key]; ok && def.AppliesTo(a.Bin) {
				pv.Options = append(pv.Options, ProfileOption{
					Key: def.Key, Label: def.Label, Type: def.Type, Choices: def.Choices, Value: v,
				})
			}
		}
		out = append(out, pv)
	}
	return out
}

// OptionDefs returns the configured option definitions (for the profile editor).
func (m *ProfileManager) OptionDefs() []config.OptionDef { return m.options }

// Get returns the full merged profile config by name (for the profile editor).
func (m *ProfileManager) Get(name string) (config.AgentConfig, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.merged() {
		if p.Name == name {
			return p, true
		}
	}
	return config.AgentConfig{}, false
}

// Save validates and persists a runtime profile, then refreshes the launcher.
func (m *ProfileManager) Save(p config.AgentConfig) error {
	if err := config.ValidateProfile(p, m.optDefs); err != nil {
		return err
	}
	// ValidateProfile lets a whitespace-only box.image through; it is no image,
	// so drop it rather than store a blank the config layer would ignore.
	p.Box = config.BoxConfig{}
	if p.DefaultCwd != "" {
		rt := config.Config{DefaultRuntime: m.defaultRuntime}.RuntimeFor(p)
		if rt == config.RuntimeBox {
			if reason, bad := UnboxableDefaultCwd(p.DefaultCwd, m.home, m.protected); bad {
				return fmt.Errorf("default working dir cannot be contained: %s", reason)
			}
		}
	}
	if err := m.checkClone(p); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := config.CheckGitHubApps(p, m.ghApps); err != nil {
		return fmt.Errorf("profile %q: %w", p.Name, err)
	}
	if err := config.CheckTokens(p, m.tokens, m.optDefs); err != nil {
		return fmt.Errorf("profile %q: %w", p.Name, err)
	}
	if err := config.CheckAWSConnections(p, m.awsConns); err != nil {
		return fmt.Errorf("profile %q: %w", p.Name, err)
	}
	if err := m.checkAccount(p); err != nil {
		return err
	}
	if err := m.store.Save(p); err != nil {
		return err
	}
	m.apply(m.merged())
	return nil
}

// checkClone is the save-time form of the clone checks: a clone profile must
// run boxed, and its repo must pass the installed disk check. Spawn re-checks.
func (m *ProfileManager) checkClone(p config.AgentConfig) error {
	c, err := p.Clone()
	if err != nil || c == nil {
		return err
	}
	if rt := (config.Config{DefaultRuntime: m.defaultRuntime}).RuntimeFor(p); rt != config.RuntimeBox {
		return errors.New(`clone mode needs runtime "box" (this instance defaults to host)`)
	}
	m.mu.Lock()
	check := m.cloneCheck
	m.mu.Unlock()
	if check == nil {
		return nil
	}
	return check(*c)
}

// checkAccount is the early, friendly form of launcher.checkAccount: it rejects
// a profile whose account cannot work with it. Spawn time re-checks. Caller
// holds m.mu.
func (m *ProfileManager) checkAccount(p config.AgentConfig) error {
	// The account owns the config dir whichever account the profile names.
	for k := range p.Env {
		if k == "CLAUDE_CONFIG_DIR" || k == "CODEX_HOME" {
			return fmt.Errorf("profile %q: %s is set by the agent account, not the profile", p.Name, k)
		}
	}
	if p.Account == "" || p.Account == account.Default {
		return nil
	}
	if m.accounts == nil {
		return fmt.Errorf("profile %q: agent accounts are not configured", p.Name)
	}
	acct, err := m.accounts.Resolve(p.Account)
	if err != nil {
		return fmt.Errorf("profile %q: %w", p.Name, err)
	}
	switch acct.Provider {
	case account.ProviderClaude:
		if !launcher.IsClaudeBin(p.Bin) {
			return fmt.Errorf("profile %q: bin %s cannot use claude account %q", p.Name, p.Bin, p.Account)
		}
	case account.ProviderCodex:
		if p.Bin == "" || filepath.Base(p.Bin) != "codex" {
			return fmt.Errorf("profile %q: bin %s cannot use codex account %q", p.Name, p.Bin, p.Account)
		}
	}
	for _, n := range p.Secrets {
		if account.IsAuthEnv(n) {
			return fmt.Errorf("profile %q: secret %s conflicts with account %q; set the token on the account", p.Name, n, p.Account)
		}
	}
	for k := range p.Env {
		if account.IsAuthEnv(k) {
			return fmt.Errorf("profile %q: env %s conflicts with account %q", p.Name, k, p.Account)
		}
	}
	return nil
}

// Delete removes a runtime profile (config seeds are not deletable → ErrNotFound).
func (m *ProfileManager) Delete(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.store.Get(name); !ok {
		return store.ErrNotFound
	}
	if err := m.store.Delete(name); err != nil {
		return err
	}
	m.apply(m.merged())
	return nil
}

// argsBypassPermissions reports whether a profile's static args already turn
// Claude Code's permission checks off. Best-effort match for a UI warning, not
// a security control: it only recognises the exact spellings a profile's own
// editor produces (a bare flag, `--permission-mode bypassPermissions` as two
// args, or `--permission-mode=bypassPermissions` as one). A variant it does
// not special-case here — e.g. `--dangerously-skip-permissions=true` — is not
// detected and silently reads as "not bypassing", so the warning can under-
// but never over-report.
func argsBypassPermissions(args []string) bool {
	for i, a := range args {
		switch {
		case a == "--dangerously-skip-permissions", a == "--permission-mode=bypassPermissions":
			return true
		case a == "--permission-mode" && i+1 < len(args) && args[i+1] == "bypassPermissions":
			return true
		}
	}
	return false
}
