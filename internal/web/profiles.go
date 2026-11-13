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

	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/awsscope"
	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credfile"
	"github.com/culiops/culibox/internal/launcher"
	"github.com/culiops/culibox/internal/session"
	"github.com/culiops/culibox/internal/store"
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

// Where a profile came from (ProfileView.Source). Provider-neutral: it says
// who made the profile, never what it runs.
const (
	// ProfileSourceBuiltin: CuliBox added it itself (the zero-config default
	// agents and the detected-shell seed), so the user has made nothing yet.
	ProfileSourceBuiltin = "builtin"
	// ProfileSourceConfig: an agents entry of the user's config.json.
	ProfileSourceConfig = "config"
	// ProfileSourceRuntime: saved from the UI (the profile store); it wins over
	// a same-named config entry.
	ProfileSourceRuntime = "runtime"
)

// ProfileView is a profile for GET /api/profiles. Editable = came from the
// runtime store (config seeds are read-only). Source says where the profile
// came from; it is derived on every read and never written to a file, and
// Editable is exactly Source == ProfileSourceRuntime. Runtime is the EFFECTIVE
// tier the spawn form should open on — already resolved against the instance
// default, so the client never has to re-derive containment policy.
type ProfileView struct {
	Name       string          `json:"name"`
	Account    string          `json:"account,omitempty"`
	Editable   bool            `json:"editable"`
	Source     string          `json:"source"`
	Runtime    string          `json:"runtime"`
	DefaultCwd string          `json:"default_cwd,omitempty"`
	Options    []ProfileOption `json:"options,omitempty"`
	// RuntimeAsked is the tier a session of this profile asks for: its own
	// runtime, else the instance's default_runtime as configured, else "" (it
	// asks for nothing). Runtime above ends in box when nobody named a tier, so
	// it cannot tell the two apart, and with no box layer they differ: a session
	// that asks for box is refused, one that asks for nothing runs on this machine
	// (session.runtimeFor, launcher.RequestedRuntime).
	RuntimeAsked string `json:"runtime_asked,omitempty"`
	// AcceptsPrompt: the spawn form may offer a first message (launcher.AcceptsPrompt).
	AcceptsPrompt bool `json:"accepts_prompt,omitempty"`
	// Claude: replies (and their default) apply to this profile at all.
	Claude bool `json:"claude,omitempty"`
	// Replyable is the profile's reply default — only ever true for claude.
	Replyable bool `json:"replyable,omitempty"`
	// BypassArgs: the profile's own args already bypass permission checks, so
	// the spawn form must warn when replies are on even with the option off.
	BypassArgs bool `json:"bypass_args,omitempty"`
	// Bin is the profile's agent binary. The rest say what a session of it can
	// reach, for the spawn dialog's "touches" line: names only, never a role
	// ARN, a token reference or a value. Tokens are the static_tokens entry
	// names, GitHubOwners the owners (not their App or PAT references).
	Bin            string   `json:"bin,omitempty"`
	AWSConnections []string `json:"aws_connections,omitempty"`
	// AWSInline is the legacy inline AWS binding, named like a connection.
	AWSInline    *ProfileAWSInline `json:"aws_inline,omitempty"`
	GitHubOwners []string          `json:"github_owners,omitempty"`
	// GitHubWrite: the binding can push (a PAT owner, or an App granted
	// contents write). GitHubPAT: some owner takes a static token.
	GitHubWrite bool     `json:"github_write,omitempty"`
	GitHubPAT   bool     `json:"github_pat,omitempty"`
	Tokens      []string `json:"tokens,omitempty"`
	// Network is the posture a session of this profile reports ("allowlist",
	// "open" or "": session.EgressMode applied to the profile's bindings).
	Network string `json:"network,omitempty"`
	// ContextPaths are the host paths its box can read; CloneOf the repo a
	// clone-mode profile clones.
	ContextPaths []string `json:"context_paths,omitempty"`
	CloneOf      string   `json:"clone_of,omitempty"`
	// ImportsOff: a boxed claude of this profile does not get the files the
	// bundled CLAUDE.md imports.
	ImportsOff bool `json:"imports_off,omitempty"`
	// AutoReturn: a clone profile whose work comes home on its own (the switch
	// is on). NotHome is what the clone's last return left behind, if anything.
	AutoReturn bool                  `json:"auto_return,omitempty"`
	NotHome    *session.NotHomeState `json:"not_home,omitempty"`
	// What the read view lists beyond the touch line (names, repos, paths and
	// hosts only, like the fields above). GitHubRepos is each owner's repo list;
	// GitHubPermissions the EFFECTIVE permission set (the defaults when the
	// profile sets none), present only with a GitHub binding. EgressAllow are
	// the profile's extra domains and CloneShare the clone's read-only shared
	// paths, relative to the repo it clones.
	GitHubRepos       []ProfileGitHubRepos `json:"github_repos,omitempty"`
	GitHubPermissions map[string]string    `json:"github_permissions,omitempty"`
	EgressAllow       []string             `json:"egress_allow,omitempty"`
	CloneShare        []string             `json:"clone_share,omitempty"`
}

// ProfileGitHubRepos is one owner of a profile's GitHub binding and the repos it
// reaches there. Never the App or token entry that backs the owner.
type ProfileGitHubRepos struct {
	Owner string   `json:"owner"`
	Repos []string `json:"repos"`
}

// ProfileAWSInline names a profile's legacy inline AWS binding for the spawn
// dialog: its label ("aws" when it has none, as the Access view names it), the
// prod flag and the elevate window. No source profile, ARN, region or bucket.
type ProfileAWSInline struct {
	Name              string `json:"name"`
	Prod              bool   `json:"prod,omitempty"`
	ElevateMaxMinutes int    `json:"elevate_max_minutes,omitempty"`
	// Mode is the binding's mode ("" = assume_role, "sso_direct").
	Mode string `json:"mode,omitempty"`
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
	// shares is the box VM's live folder list (macOS), nil without a VM.
	shares box.SharesFunc
	// accounts validates a profile's agent account on save; nil = only the
	// default account exists. Spawn-time checkAccount stays the authority.
	accounts launcher.AccountResolver
	// cloneCheck is the disk-side check of a clone profile's repo (clone.CheckOf,
	// wired at boot); nil = pure validation only. Spawn re-checks.
	cloneCheck func(config.Clone) error
	// notHome reports what a clone's last return left behind (cloneHooks.NotHome,
	// wired at boot); nil = nothing to show.
	notHome func(config.Clone) *session.NotHomeState
	// contextCheck is the disk-side check of one context_paths entry
	// (box.CheckContextPath plus the box-owned refusal, wired at boot); nil =
	// pure validation only. Spawn re-checks.
	contextCheck func(path string) error
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
	// egressAll: every boxed spawn goes through the egress proxy, bound or not
	// (both OSes; Manager.SetEgressAll). It decides whether an unbound boxed profile
	// previews as "open" or as no egress control.
	egressAll bool
	// builtin names the seeds CuliBox added itself (see SetBuiltin); nil = none.
	builtin map[string]bool
}

// SetBuiltin names the seeds that are built in: profiles CuliBox put there
// itself, not ones the user wrote, so Views reports them as ProfileSourceBuiltin
// instead of ProfileSourceConfig. Call at boot; nil = no profile is built in.
// A profile saved from the UI is runtime whatever its name.
func (m *ProfileManager) SetBuiltin(names []string) {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	m.mu.Lock()
	m.builtin = set
	m.mu.Unlock()
}

// SetEgressAll records whether egress is mandatory for every boxed spawn, so a
// profile's network preview matches what its session will report. Call at boot,
// with Manager.SetEgressAll.
func (m *ProfileManager) SetEgressAll(all bool) {
	m.mu.Lock()
	m.egressAll = all
	m.mu.Unlock()
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
	Mode         string `json:"mode,omitempty"` // "" = assume_role; "sso_direct"
}

// AWSConnectionMetaList lists the instance's aws_connections for the profile
// editor's picker, in config order. Never nil, so it encodes as [].
func (m *ProfileManager) AWSConnectionMetaList() []AWSConnectionMeta {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AWSConnectionMeta, 0, len(m.awsConns))
	for _, c := range m.awsConns {
		out = append(out, AWSConnectionMeta{Name: c.Name, Label: c.Label, AccountID: awsscope.AccountOf(c.RoleARN),
			Region: c.Region, Description: c.Description, Prod: c.Prod, WriteCapable: c.Elevate != nil, Mode: c.Mode})
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

// SetNotHome installs the source of a clone profile's not-home state for the
// profile views. Call at boot; nil shows none.
func (m *ProfileManager) SetNotHome(f func(config.Clone) *session.NotHomeState) {
	m.mu.Lock()
	m.notHome = f
	m.mu.Unlock()
}

// SetContextCheck installs the save-time check of one context_paths entry. Call
// at boot; nil leaves only the pure validation.
func (m *ProfileManager) SetContextCheck(f func(path string) error) {
	m.mu.Lock()
	m.contextCheck = f
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
	m.setSeeds(seeds, options)
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

// setSeeds loads the read-only config seeds and the option definitions.
func (m *ProfileManager) setSeeds(seeds []config.AgentConfig, options []config.OptionDef) {
	for _, s := range seeds {
		if _, dup := m.seeds[s.Name]; !dup {
			m.seedOrder = append(m.seedOrder, s.Name)
		}
		m.seeds[s.Name] = s
	}
	for _, o := range options {
		m.optDefs[o.Key] = o
	}
}

// staticStore is a profileStore over a fixed list (MergeProfiles).
type staticStore []config.AgentConfig

func (s staticStore) List() []config.AgentConfig { return s }
func (s staticStore) Get(name string) (config.AgentConfig, bool) {
	for _, p := range s {
		if p.Name == name {
			return p, true
		}
	}
	return config.AgentConfig{}, false
}
func (staticStore) Save(config.AgentConfig) error { return errors.New("read-only profile list") }
func (staticStore) Delete(string) error           { return errors.New("read-only profile list") }

// MergeProfiles is the effective profile set for config seeds and stored
// profiles without a running manager (the CLI): the same merge and option
// scoping as ProfileManager.All.
func MergeProfiles(seeds, stored []config.AgentConfig, options []config.OptionDef) []config.AgentConfig {
	m := &ProfileManager{seeds: map[string]config.AgentConfig{}, optDefs: map[string]config.OptionDef{}, store: staticStore(stored)}
	m.setSeeds(seeds, options)
	return m.merged()
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
		rt := config.Config{DefaultRuntime: m.defaultRuntime}.RuntimeFor(a)
		asked := a.Runtime
		if asked == "" {
			asked = m.defaultRuntime
		}
		bound := a.AWS != nil || len(a.AWSConnections) > 0 || a.GitHub != nil || len(a.Tokens) > 0
		source := ProfileSourceConfig
		switch {
		case editable[a.Name]:
			source = ProfileSourceRuntime
		case m.builtin[a.Name]:
			source = ProfileSourceBuiltin
		}
		pv := ProfileView{
			Name:          a.Name,
			Account:       a.Account,
			Editable:      source == ProfileSourceRuntime,
			Source:        source,
			Runtime:       rt,
			RuntimeAsked:  asked,
			DefaultCwd:    a.DefaultCwd,
			AcceptsPrompt: launcher.AcceptsPrompt(a.Bin),
			Claude:        launcher.IsClaudeBin(a.Bin),
			Replyable:     a.Replyable && launcher.IsClaudeBin(a.Bin),
			BypassArgs:    argsBypassPermissions(a.Args),
			Bin:           a.Bin,
			Tokens:        slices.Clone(a.Tokens),
			// A boxed spawn has egress when it is bound or egress is mandatory
			// (the needEgress rule of Manager.Spawn); the host tier never has it.
			Network:        session.EgressMode(rt == config.RuntimeBox && (bound || m.egressAll), bound && !a.EgressOpen),
			AWSConnections: slices.Clone(a.AWSConnections),
			ContextPaths:   slices.Clone(a.ContextPaths),
			CloneOf:        a.CloneOf,
			ImportsOff:     a.ImportsOff,
			EgressAllow:    slices.Clone(a.EgressAllow),
			CloneShare:     slices.Clone(a.CloneShare),
		}
		if c, err := a.Clone(); err == nil && c != nil {
			pv.AutoReturn = !c.NoAutoReturn
			if m.notHome != nil {
				pv.NotHome = m.notHome(*c)
			}
		}
		if a.AWS != nil {
			in := &ProfileAWSInline{Name: a.AWS.Label, Prod: a.AWS.Prod, Mode: a.AWS.Mode}
			if in.Name == "" {
				in.Name = "aws"
			}
			if a.AWS.Elevate != nil {
				in.ElevateMaxMinutes = a.AWS.Elevate.Max()
			}
			pv.AWSInline = in
		}
		if a.GitHub != nil {
			for _, o := range a.GitHub.Owners {
				pv.GitHubOwners = append(pv.GitHubOwners, o.Owner)
				pv.GitHubRepos = append(pv.GitHubRepos, ProfileGitHubRepos{Owner: o.Owner, Repos: append([]string{}, o.Repos...)})
			}
			pv.GitHubPermissions = a.GitHub.EffectivePermissions()
			pv.GitHubWrite = a.GitHub.PushRoute()
			pv.GitHubPAT = a.GitHub.HasPAT()
		}
		// An unset enum is offered with a blank value (the agent's own default),
		// so a profile made in the editor can still pick Model/Effort at spawn.
		for _, def := range m.options {
			if !def.AppliesTo(a.Bin) || !def.OfferedBy(a) {
				continue
			}
			v, ok := a.Options[def.Key]
			if !ok {
				v = ""
			}
			pv.Options = append(pv.Options, ProfileOption{
				Key: def.Key, Label: def.Label, Type: def.Type, Choices: def.Choices, Value: v,
			})
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
	return m.getLocked(name)
}

// getLocked is Get for a caller that already holds m.mu.
func (m *ProfileManager) getLocked(name string) (config.AgentConfig, bool) {
	for _, p := range m.merged() {
		if p.Name == name {
			return p, true
		}
	}
	return config.AgentConfig{}, false
}

// All is the merged profile set: config seeds and runtime profiles, a runtime
// profile overriding a same-named seed.
func (m *ProfileManager) All() []config.AgentConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.merged()
}

// RefsTo returns, sorted, the names of the profiles (seed or runtime) that bind
// the credential entry kind/name. Kinds are the credfile ones: an AWS
// connection by aws_connections; a GitHub App by a github owner's app; a static
// token by tokens or a github owner's token (a PAT).
func (m *ProfileManager) RefsTo(kind, name string) []string {
	var out []string
	for _, p := range m.All() {
		if profileBinds(p, kind, name) {
			out = append(out, p.Name)
		}
	}
	sort.Strings(out)
	return out
}

func profileBinds(p config.AgentConfig, kind, name string) bool {
	switch kind {
	case credfile.KindAWS:
		return slices.Contains(p.AWSConnections, name)
	case credfile.KindToken:
		if slices.Contains(p.Tokens, name) {
			return true
		}
	}
	if p.GitHub == nil {
		return false
	}
	for _, o := range p.GitHub.Owners {
		if (kind == credfile.KindGitHubApp && o.App == name) || (kind == credfile.KindToken && o.Token == name) {
			return true
		}
	}
	return false
}

// Create saves a new runtime profile and returns its revision. It refuses a name
// a config seed or a stored profile already holds (ErrProfileExists): a create
// must never replace a profile it did not see.
func (m *ProfileManager) Create(p config.AgentConfig) (string, error) {
	return m.save(p, func() error {
		if _, taken := m.getLocked(p.Name); taken {
			return ErrProfileExists
		}
		return nil
	})
}

// Update saves a runtime profile only if it is still the one the caller opened:
// rev, taken from Get's value by profileRev, must equal the revision of the
// current merged profile of that name. A profile that changed since gives
// ErrProfileChanged and one that was deleted gives ErrProfileGone; either way
// nothing is saved and nothing of the caller's edit is merged in. A config seed
// has a revision too, so saving one creates its override. The profile is
// validated first, so an invalid one is reported as such, not as stale. It
// returns the revision of the profile it stored.
func (m *ProfileManager) Update(p config.AgentConfig, rev string) (string, error) {
	return m.save(p, func() error {
		cur, ok := m.getLocked(p.Name)
		if !ok {
			return ErrProfileGone
		}
		if rev == "" || profileRev(cur) != rev {
			return ErrProfileChanged
		}
		return nil
	})
}

// Save validates and persists a runtime profile, then refreshes the launcher.
// It checks no revision and no existing name: the API goes through Create and
// Update, so Save is for tests and for callers that really mean "overwrite".
func (m *ProfileManager) Save(p config.AgentConfig) error {
	_, err := m.save(p, nil)
	return err
}

// save is the body of Save, Create and Update. guard, when not nil, runs under
// m.mu right before the store write, after every validation, so what it checks
// cannot change before the write; an error from it is returned as it is. A
// failed store write comes back as a *storeFailure. On success it returns the
// revision of the stored profile as Get shows it.
func (m *ProfileManager) save(p config.AgentConfig, guard func() error) (string, error) {
	if err := config.ValidateProfile(p, m.optDefs); err != nil {
		return "", err
	}
	// ValidateProfile lets a whitespace-only box.image through; it is no image,
	// so drop it rather than store a blank the config layer would ignore.
	p.Box = config.BoxConfig{}
	if p.DefaultCwd != "" {
		rt := config.Config{DefaultRuntime: m.defaultRuntime}.RuntimeFor(p)
		if rt == config.RuntimeBox {
			m.mu.Lock()
			shares := m.shares
			m.mu.Unlock()
			if reason, bad := UnboxableDefaultCwd(p.DefaultCwd, m.home, m.protected, shares); bad {
				return "", config.WithField("default_cwd", fmt.Errorf("default working dir cannot be contained: %s", reason))
			}
		}
	}
	if err := m.checkClone(p); err != nil {
		return "", err
	}
	if err := m.checkContextPaths(p); err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := config.CheckGitHubApps(p, m.ghApps); err != nil {
		return "", fmt.Errorf("profile %q: %w", p.Name, err)
	}
	if err := config.CheckTokens(p, m.tokens, m.optDefs); err != nil {
		return "", fmt.Errorf("profile %q: %w", p.Name, err)
	}
	if err := config.CheckAWSConnections(p, m.awsConns); err != nil {
		return "", fmt.Errorf("profile %q: %w", p.Name, err)
	}
	if err := m.checkAccount(p); err != nil {
		return "", err
	}
	if guard != nil {
		if err := guard(); err != nil {
			return "", err
		}
	}
	if err := m.store.Save(p); err != nil {
		return "", profileStoreErr(err)
	}
	m.apply(m.merged())
	// The revision of what was just stored, taken under the lock that wrote it:
	// a client that keeps it can save again without a read in between.
	saved, _ := m.getLocked(p.Name)
	return profileRev(saved), nil
}

// checkClone is the save-time form of the clone checks: a clone profile must
// run boxed, and its repo must pass the installed disk check. Spawn re-checks.
func (m *ProfileManager) checkClone(p config.AgentConfig) error {
	c, err := p.Clone()
	if err != nil || c == nil {
		return err
	}
	if rt := (config.Config{DefaultRuntime: m.defaultRuntime}).RuntimeFor(p); rt != config.RuntimeBox {
		return config.WithField("clone_of", errors.New(`clone mode needs runtime "box" (this instance defaults to host)`))
	}
	m.mu.Lock()
	check := m.cloneCheck
	m.mu.Unlock()
	if check == nil {
		return nil
	}
	return config.WithField("clone_of", check(*c))
}

// checkContextPaths is the save-time form of the context_paths checks: the
// profile must run boxed (ValidateProfile sees only an explicit runtime, so the
// instance default is resolved here), and every path must pass the installed
// disk check. Spawn re-checks.
func (m *ProfileManager) checkContextPaths(p config.AgentConfig) error {
	if len(p.ContextPaths) == 0 {
		return nil
	}
	if rt := (config.Config{DefaultRuntime: m.defaultRuntime}).RuntimeFor(p); rt != config.RuntimeBox {
		return config.WithField("context_paths", config.ErrContextNeedsBox)
	}
	m.mu.Lock()
	check := m.contextCheck
	m.mu.Unlock()
	if check == nil {
		return nil
	}
	for i, path := range p.ContextPaths {
		if err := check(path); err != nil {
			return config.WithField(fmt.Sprintf("context_paths[%d]", i), err)
		}
	}
	return nil
}

// maxSecretNames is how many file names a context warning lists.
const maxSecretNames = 5

// ContextWarnings are the saved profile's context_paths the user should know
// about: secret-looking names, secret-looking files inside a directory, and a
// boxed profile whose workspace a context path overlaps. Warnings, not
// refusals: what the box may read is the user's call.
func (m *ProfileManager) ContextWarnings(p config.AgentConfig) []string {
	if len(p.ContextPaths) == 0 {
		return nil
	}
	out := config.ContextNameWarnings(p.ContextPaths)
	for _, path := range p.ContextPaths {
		if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
			continue
		}
		names := box.SecretFiles(path)
		if len(names) == 0 {
			continue
		}
		list := strings.Join(names[:min(len(names), maxSecretNames)], ", ")
		if len(names) > maxSecretNames {
			list += ", …"
		}
		out = append(out, fmt.Sprintf("context_paths %q contains files that look like secrets (%s); the box will be able to read and send them", path, list))
	}
	rt := config.Config{DefaultRuntime: m.defaultRuntime}
	for _, o := range m.All() {
		// An in-place boxed profile writes its default_cwd; a clone profile
		// writes its clone, not default_cwd's source.
		if o.Name == p.Name || o.CloneOf != "" || strings.TrimSpace(o.DefaultCwd) == "" || rt.RuntimeFor(o) != config.RuntimeBox {
			continue
		}
		cwd := filepath.Clean(strings.TrimSpace(o.DefaultCwd))
		for _, path := range p.ContextPaths {
			if box.Overlaps(path, cwd) {
				out = append(out, fmt.Sprintf("context_paths %q overlaps the workspace of boxed profile %q; that box can write what this box reads", path, o.Name))
			}
		}
	}
	return out
}

// checkAccount is the early, friendly form of launcher.checkAccount: it rejects
// a profile whose account cannot work with it. Spawn time re-checks. Caller
// holds m.mu.
func (m *ProfileManager) checkAccount(p config.AgentConfig) error {
	// The account owns the config dir whichever account the profile names.
	for k := range p.Env {
		if k == "CLAUDE_CONFIG_DIR" || k == "CODEX_HOME" {
			return config.WithField("env", fmt.Errorf("profile %q: %s is set by the agent account, not the profile", p.Name, k))
		}
	}
	if p.Account == "" || p.Account == account.Default {
		return nil
	}
	if m.accounts == nil {
		return config.WithField("account", fmt.Errorf("profile %q: agent accounts are not configured", p.Name))
	}
	acct, err := m.accounts.Resolve(p.Account)
	if err != nil {
		return config.WithField("account", fmt.Errorf("profile %q: %w", p.Name, err))
	}
	switch acct.Provider {
	case account.ProviderClaude:
		if !launcher.IsClaudeBin(p.Bin) {
			return config.WithField("account", fmt.Errorf("profile %q: bin %s cannot use claude account %q", p.Name, p.Bin, p.Account))
		}
	case account.ProviderCodex:
		if p.Bin == "" || filepath.Base(p.Bin) != "codex" {
			return config.WithField("account", fmt.Errorf("profile %q: bin %s cannot use codex account %q", p.Name, p.Bin, p.Account))
		}
	}
	for i, n := range p.Secrets {
		if account.IsAuthEnv(n) {
			return config.WithField(fmt.Sprintf("secrets[%d]", i), fmt.Errorf("profile %q: secret %s conflicts with account %q; set the token on the account", p.Name, n, p.Account))
		}
	}
	for k := range p.Env {
		if account.IsAuthEnv(k) {
			return config.WithField("env", fmt.Errorf("profile %q: env %s conflicts with account %q", p.Name, k, p.Account))
		}
	}
	return nil
}

// Delete removes a runtime profile (config seeds are not deletable → ErrNotFound)
// with no revision check, like Save: for tests and callers that really mean
// "whatever is there". The API goes through Remove.
func (m *ProfileManager) Delete(name string) error { return m.remove(name, nil) }

// Remove deletes a runtime profile only if it is still the one the caller
// opened: rev, from Get's value by profileRev, must equal the revision of the
// current merged profile. A profile edited since gives ErrProfileChanged and
// nothing is removed; that matters most for a stored override of a config seed,
// where the delete reverts the profile to its seed. A name with no stored
// profile is ErrNotFound, as for Delete; an empty rev for one that is stored
// is refused (errDeleteRevRequired).
func (m *ProfileManager) Remove(name, rev string) error { return m.remove(name, &rev) }

func (m *ProfileManager) remove(name string, rev *string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.store.Get(name); !ok {
		return store.ErrNotFound
	}
	if rev != nil {
		if *rev == "" {
			return errDeleteRevRequired
		}
		if cur, ok := m.getLocked(name); !ok || profileRev(cur) != *rev {
			return ErrProfileChanged
		}
	}
	if err := m.store.Delete(name); err != nil {
		return profileStoreErr(err)
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
	return launcher.ArgsBypassPermissions(args) // one spelling list, shared with the box's bypass pre-accept
}

// SetShares installs the box VM's live folder list, which the default_cwd save
// check reads. Call at boot; nil (no VM) leaves every directory visible.
func (m *ProfileManager) SetShares(f box.SharesFunc) {
	m.mu.Lock()
	m.shares = f
	m.mu.Unlock()
}
