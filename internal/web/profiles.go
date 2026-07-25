package web

import (
	"sort"
	"sync"

	"github.com/culiops/culiops-agent-console/internal/config"
	"github.com/culiops/culiops-agent-console/internal/store"
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
// runtime store (config seeds are read-only).
type ProfileView struct {
	Name     string          `json:"name"`
	Editable bool            `json:"editable"`
	Options  []ProfileOption `json:"options,omitempty"`
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
}

// NewProfileManager wires seeds + store + launcher-apply and does the initial refresh.
func NewProfileManager(seeds []config.AgentConfig, options []config.OptionDef, st profileStore, apply func([]config.AgentConfig)) *ProfileManager {
	m := &ProfileManager{
		seeds:   map[string]config.AgentConfig{},
		options: options,
		optDefs: map[string]config.OptionDef{},
		store:   st,
		apply:   apply,
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
	m.apply(m.merged())
	return m
}

// scopeOptions returns a copy of a with any KNOWN option not applicable to its
// bin removed, so a legacy stored profile can never emit an inapplicable flag/env
// at spawn. Unknown keys are left as-is (the launcher already ignores them).
func (m *ProfileManager) scopeOptions(a config.AgentConfig) config.AgentConfig {
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
		pv := ProfileView{Name: a.Name, Editable: editable[a.Name]}
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.Save(p); err != nil {
		return err
	}
	m.apply(m.merged())
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
