// Package config loads and validates the agent-console configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
)

// Base env policies for a spawned agent.
const (
	BaseClean   = "clean"   // curated minimal env, no inherited secrets (default)
	BaseLogin   = "login"   // run via $SHELL -l so the profile/toolchain is sourced
	BaseInherit = "inherit" // daemon env minus Claude session markers (escape hatch)
)

// SecretSource is one place secrets are resolved from, tried in order.
type SecretSource struct {
	Type string `json:"type"`           // "env" | "file"
	Path string `json:"path,omitempty"` // for type "file"
}

// AgentConfig describes one launchable agent CLI and its environment policy.
type AgentConfig struct {
	Name    string            `json:"name"`
	Bin     string            `json:"bin"`
	Args    []string          `json:"args,omitempty"`
	Base    string            `json:"base,omitempty"`    // clean(default)|login|inherit
	Env     map[string]string `json:"env,omitempty"`     // static per-agent overrides
	Secrets []string          `json:"secrets,omitempty"` // names resolved via SecretSources
}

// BasePolicy returns the effective base, defaulting to clean.
func (a AgentConfig) BasePolicy() string {
	if a.Base == "" {
		return BaseClean
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
	SecretSources    []SecretSource `json:"secret_sources,omitempty"`
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
	for _, a := range c.Agents {
		switch a.BasePolicy() {
		case BaseClean, BaseLogin, BaseInherit:
		default:
			return fmt.Errorf("config: agent %q has invalid base %q (want clean|login|inherit)", a.Name, a.Base)
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
