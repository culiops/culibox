package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
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
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("home dir %q is not an absolute path; set $HOME", home)
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
	if err := checkRoot(root); err != nil {
		return Config{}, Source{Path: root}, err
	}
	for _, p := range candidates(root, home, xdg, relocated) {
		fi, err := os.Stat(p)
		if err == nil {
			if err := checkOwned(p, fi); err != nil {
				return Config{}, Source{Path: p}, err
			}
			c, lerr := Load(p)
			return c, Source{Path: p}, lerr
		}
		if !errors.Is(err, os.ErrNotExist) {
			return Config{}, Source{Path: p}, err
		}
	}
	return Defaults(), Source{Default: true}, nil
}

// candidates are the config files load tries, in order.
func candidates(root, home, xdg string, relocated bool) []string {
	out := []string{filepath.Join(root, "config.json")}
	if !relocated {
		base := filepath.Join(home, ".config")
		if xdg != "" && filepath.IsAbs(xdg) {
			base = xdg
		}
		out = append(out, filepath.Join(base, "agent-box", "config.json"))
	}
	return out
}

// ConfigDirs is every directory Resolve (with the same arguments) reads a
// config file from, whether or not the file exists yet: the explicit file's
// directory, else each candidate's. A boxed workspace must not overlap any of
// them, or a box could write the config that names the binaries agent-box
// runs on its next start.
func ConfigDirs(explicit string, set bool, home, xdg, abHome string) ([]string, error) {
	if set {
		p, err := filepath.Abs(explicit)
		if err != nil {
			return nil, err
		}
		return []string{filepath.Dir(p)}, nil
	}
	root, err := Root(home, abHome)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range candidates(root, home, xdg, abHome != "") {
		out = append(out, filepath.Dir(p))
	}
	return out, nil
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

// checkRoot refuses a root that someone else could have planted a config.json
// in: the config names the binaries agent-box runs. A root that does not exist
// yet is fine (serve creates it 0700).
func checkRoot(root string) error {
	fi, err := os.Stat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("data root %s is not a directory", root)
	}
	return checkOwned("data root "+root, fi)
}

// checkOwned refuses what is not ours or is writable by group or others
// (group too: on macOS group staff is every local user).
func checkOwned(what string, fi os.FileInfo) error {
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not owned by you; refusing to read a config from it", what)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by others (mode %o); chmod go-w it", what, fi.Mode().Perm())
	}
	return nil
}
