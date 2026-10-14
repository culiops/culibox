package config

import (
	"errors"
	"os"
	"path/filepath"
)

// Source records where the effective config came from, for the startup log and
// agent-box doctor.
type Source struct {
	Path    string // the file read; "" with Default
	Default bool   // no file found: built-in defaults
}

func (s Source) String() string {
	if s.Default {
		return "built-in defaults (no config file found)"
	}
	return s.Path
}

// Resolve loads the config. With an explicit -config (set), that file must
// exist. Otherwise the first existing file wins:
// $XDG_CONFIG_HOME/agent-box/config.json (absolute XDG only, per the spec), else
// ~/.config/agent-box/config.json; with none, Defaults(). The current directory
// is deliberately NOT searched: running agent-box inside a cloned repo must not
// load that repo's config (allowed_hosts, password_env, agent bins).
func Resolve(explicit string, set bool, home, xdg string) (Config, Source, error) {
	if set {
		c, err := Load(explicit)
		return c, Source{Path: explicit}, err
	}
	base := filepath.Join(home, ".config")
	if xdg != "" && filepath.IsAbs(xdg) {
		base = xdg
	}
	for _, p := range []string{filepath.Join(base, "agent-box", "config.json")} {
		_, err := os.Stat(p)
		if err == nil {
			c, lerr := Load(p)
			return c, Source{Path: p}, lerr
		}
		if !errors.Is(err, os.ErrNotExist) {
			return Config{}, Source{Path: p}, err
		}
	}
	return Defaults(), Source{Default: true}, nil
}

// Defaults is the zero-config laptop setup: loopback only, token login (no
// password_env), host tier, one claude agent. The quick shell is added at
// startup by WithDetectedShell.
func Defaults() Config {
	return Config{
		BindAddr:       "127.0.0.1:8899",
		DefaultRuntime: RuntimeHost,
		Agents:         []AgentConfig{{Name: "claude", Bin: "claude"}},
	}
}
