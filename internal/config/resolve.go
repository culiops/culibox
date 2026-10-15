package config

import (
	"errors"
	"fmt"
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

// Root is agent-box's single home: everything it writes (state, sockets on
// Linux, profiles, its config file) lives under it, so one directory is the
// whole install. $AGENT_BOX_HOME (abHome) moves it and must be absolute;
// otherwise it is ~/.local/share/agent-box.
func Root(home, abHome string) (string, error) {
	if abHome != "" {
		if !filepath.IsAbs(abHome) {
			return "", fmt.Errorf("AGENT_BOX_HOME %q is not an absolute path", abHome)
		}
		return filepath.Clean(abHome), nil
	}
	return filepath.Join(home, ".local", "share", "agent-box"), nil
}

// Resolve loads the config. With an explicit -config (set), that file must
// exist. Otherwise the first existing file wins: <root>/config.json, then —
// only when AGENT_BOX_HOME is unset, so a relocated instance never picks up
// another one's file — $XDG_CONFIG_HOME/agent-box/config.json (absolute XDG
// only, per the spec), else ~/.config/agent-box/config.json; with none,
// Defaults(). The current directory is deliberately NOT searched: running
// agent-box inside a cloned repo must not load that repo's config
// (allowed_hosts, password_env, agent bins). A config without data_dir gets the
// root as its data dir.
func Resolve(explicit string, set bool, home, xdg, abHome string) (Config, Source, error) {
	root, err := Root(home, abHome)
	if err != nil {
		return Config{}, Source{Path: "$AGENT_BOX_HOME"}, err
	}
	c, src, err := load(explicit, set, root, home, xdg, abHome != "")
	if err == nil && c.DataDir == "" {
		c.DataDir = root
	}
	return c, src, err
}

func load(explicit string, set bool, root, home, xdg string, relocated bool) (Config, Source, error) {
	if set {
		c, err := Load(explicit)
		return c, Source{Path: explicit}, err
	}
	candidates := []string{filepath.Join(root, "config.json")}
	if !relocated {
		base := filepath.Join(home, ".config")
		if xdg != "" && filepath.IsAbs(xdg) {
			base = xdg
		}
		candidates = append(candidates, filepath.Join(base, "agent-box", "config.json"))
	}
	for _, p := range candidates {
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
