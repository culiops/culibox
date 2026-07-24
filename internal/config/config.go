// Package config loads and validates the agent-console configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
)

// AgentConfig describes one launchable agent CLI.
type AgentConfig struct {
	Name string   `json:"name"`
	Bin  string   `json:"bin"`
	Args []string `json:"args,omitempty"`
}

// Config is the top-level app configuration.
type Config struct {
	BindAddr         string        `json:"bind_addr"`
	BasicAuthUser    string        `json:"basic_auth_user"`
	BasicAuthPassEnv string        `json:"basic_auth_pass_env"`
	DefaultCwd       string        `json:"default_cwd"`
	AllowPublic      bool          `json:"allow_public"`
	Agents           []AgentConfig `json:"agents"`
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
