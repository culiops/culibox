// Package config loads and validates the agent-console configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// Base env policies for a spawned agent.
const (
	BaseInherit = "inherit" // daemon env minus Claude session markers (default)
	BaseClean   = "clean"   // curated minimal env, no inherited secrets (opt-in lock-down)
	BaseLogin   = "login"   // run via $SHELL -l so the profile/toolchain is sourced
)

// SecretSource is one place secrets are resolved from, tried in order.
type SecretSource struct {
	Type string `json:"type"`           // "env" | "file"
	Path string `json:"path,omitempty"` // for type "file"
}

// Option types.
const (
	OptBool = "bool"
	OptEnum = "enum"
)

// OptionDef declares a launch option and how it maps to a flag and/or env var.
// bool: value true → append Flag / set Env="1". enum: value (∈ Choices) →
// append Flag <value> / set Env=<value>.
type OptionDef struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Type    string   `json:"type"`              // bool | enum
	Choices []string `json:"choices,omitempty"` // enum only
	Flag    string   `json:"flag,omitempty"`
	Env     string   `json:"env,omitempty"`
	Bins    []string `json:"bins,omitempty"` // agent bins this option applies to; empty = all
}

// AppliesTo reports whether this option is offered for an agent binary. No bins →
// applies to every agent; otherwise matches the bin exactly or by basename (so
// "/usr/local/bin/claude" matches ["claude"]).
func (o OptionDef) AppliesTo(bin string) bool {
	if len(o.Bins) == 0 {
		return true
	}
	base := filepath.Base(bin)
	for _, b := range o.Bins {
		if b == bin || b == base {
			return true
		}
	}
	return false
}

// AgentConfig describes one launchable agent CLI and its environment policy.
type AgentConfig struct {
	Name    string            `json:"name"`
	Bin     string            `json:"bin"`
	Args    []string          `json:"args,omitempty"`
	Base    string            `json:"base,omitempty"`    // inherit(default)|clean|login
	Env     map[string]string `json:"env,omitempty"`     // static per-agent overrides
	Secrets []string          `json:"secrets,omitempty"` // names resolved via SecretSources
	Options map[string]any    `json:"options,omitempty"` // option key → default value (bool/enum)
}

// BasePolicy returns the effective base, defaulting to inherit (agents get the
// daemon's full env minus Claude session markers; set base:clean per agent to
// contain one).
func (a AgentConfig) BasePolicy() string {
	if a.Base == "" {
		return BaseInherit
	}
	return a.Base
}

// Config is the top-level app configuration.
type Config struct {
	BindAddr         string         `json:"bind_addr"`
	BasicAuthUser    string         `json:"basic_auth_user"`
	BasicAuthPassEnv string         `json:"basic_auth_pass_env"`
	DefaultCwd       string         `json:"default_cwd"`
	AllowPublic      bool           `json:"allow_public"`
	ProfilesPath     string         `json:"profiles_path,omitempty"` // store for runtime profiles
	DataDir          string         `json:"data_dir,omitempty"`      // default ~/.local/share/agent-console
	AbducoBin        string         `json:"abduco_bin,omitempty"`    // default "abduco" on PATH
	AbducoSocketDir  string         `json:"abduco_socket_dir,omitempty"`
	SecretSources    []SecretSource `json:"secret_sources,omitempty"`
	Options          []OptionDef    `json:"options,omitempty"`
	Agents           []AgentConfig  `json:"agents"`
}

// Load reads and parses a JSON config file.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	return c, nil
}

// Validate enforces the security and completeness invariants.
func (c Config) Validate() error {
	if len(c.Agents) == 0 {
		return errors.New("config: at least one agent required")
	}
	seen := map[string]bool{}
	for _, a := range c.Agents {
		if a.Name == "" || a.Bin == "" {
			return errors.New("config: each agent requires a name and bin")
		}
		if seen[a.Name] {
			return fmt.Errorf("config: duplicate agent name %q", a.Name)
		}
		seen[a.Name] = true
		switch a.BasePolicy() {
		case BaseClean, BaseLogin, BaseInherit:
		default:
			return fmt.Errorf("config: agent %q has invalid base %q (want clean|login|inherit)", a.Name, a.Base)
		}
	}
	optDefs := map[string]OptionDef{}
	for _, o := range c.Options {
		if o.Key == "" {
			return errors.New("config: option needs a key")
		}
		if _, dup := optDefs[o.Key]; dup {
			return fmt.Errorf("config: duplicate option key %q", o.Key)
		}
		if o.Type != OptBool && o.Type != OptEnum {
			return fmt.Errorf("config: option %q has invalid type %q (want bool|enum)", o.Key, o.Type)
		}
		if o.Type == OptEnum && len(o.Choices) == 0 {
			return fmt.Errorf("config: enum option %q needs choices", o.Key)
		}
		if o.Type == OptBool && len(o.Choices) > 0 {
			return fmt.Errorf("config: bool option %q must not set choices", o.Key)
		}
		if o.Flag == "" && o.Env == "" {
			return fmt.Errorf("config: option %q must set a flag and/or env", o.Key)
		}
		optDefs[o.Key] = o
	}
	for _, a := range c.Agents {
		for k, v := range a.Options {
			def, ok := optDefs[k]
			if !ok {
				return fmt.Errorf("config: agent %q sets unknown option %q", a.Name, k)
			}
			if !def.AppliesTo(a.Bin) {
				return fmt.Errorf("config: agent %q sets option %q that does not apply to bin %q", a.Name, k, a.Bin)
			}
			switch def.Type {
			case OptBool:
				if _, ok := v.(bool); !ok {
					return fmt.Errorf("config: agent %q option %q default must be a bool", a.Name, k)
				}
			case OptEnum:
				if s, ok := v.(string); !ok || !contains(def.Choices, s) {
					return fmt.Errorf("config: agent %q option %q default must be one of %v", a.Name, k, def.Choices)
				}
			}
			// an option env must not collide with a declared secret (secrets stay authoritative)
			if def.Env != "" && contains(a.Secrets, def.Env) {
				return fmt.Errorf("config: agent %q option %q env %q collides with a declared secret", a.Name, k, def.Env)
			}
		}
	}
	for _, s := range c.SecretSources {
		switch s.Type {
		case "env":
		case "file":
			if s.Path == "" {
				return errors.New("config: file secret_source requires a path")
			}
		default:
			return fmt.Errorf("config: unknown secret_source type %q", s.Type)
		}
	}
	return c.checkBind()
}

// ValidateProfile validates a single profile (used for runtime-stored profiles)
// against the given option definitions.
func ValidateProfile(a AgentConfig, optDefs map[string]OptionDef) error {
	if a.Name == "" || a.Bin == "" {
		return errors.New("profile requires a name and bin")
	}
	switch a.BasePolicy() {
	case BaseClean, BaseLogin, BaseInherit:
	default:
		return fmt.Errorf("profile %q has invalid base %q (want clean|login|inherit)", a.Name, a.Base)
	}
	for k, v := range a.Options {
		def, ok := optDefs[k]
		if !ok {
			return fmt.Errorf("profile %q sets unknown option %q", a.Name, k)
		}
		if !def.AppliesTo(a.Bin) {
			return fmt.Errorf("profile %q sets option %q that does not apply to bin %q", a.Name, k, a.Bin)
		}
		switch def.Type {
		case OptBool:
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("profile %q option %q must be a bool", a.Name, k)
			}
		case OptEnum:
			if s, ok := v.(string); !ok || !contains(def.Choices, s) {
				return fmt.Errorf("profile %q option %q must be one of %v", a.Name, k, def.Choices)
			}
		}
		if def.Env != "" && contains(a.Secrets, def.Env) {
			return fmt.Errorf("profile %q option %q env %q collides with a declared secret", a.Name, k, def.Env)
		}
	}
	return nil
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// checkBind refuses any all-interfaces bind unless AllowPublic is set.
func (c Config) checkBind() error {
	if c.AllowPublic {
		return nil
	}
	host, _, err := net.SplitHostPort(c.BindAddr)
	if err != nil {
		return fmt.Errorf("config: invalid bind_addr %q: %w", c.BindAddr, err)
	}
	switch host {
	case "", "0.0.0.0", "::":
		return fmt.Errorf("config: bind_addr %q binds all interfaces; set allow_public to override", c.BindAddr)
	}
	return nil
}
