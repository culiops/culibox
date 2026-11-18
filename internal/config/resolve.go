package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// RootName is the data root's directory name under ~/.local/share; LegacyRootName
// is what it was called before the rebrand (#255). Both are default locations.
const (
	RootName       = "culibox"
	LegacyRootName = "agent-box"
)

// RootKind says how a data root was picked.
type RootKind int

const (
	RootEnv    RootKind = iota // CULIBOX_HOME / AGENT_BOX_HOME: used as is, never moved
	RootNew                    // ~/.local/share/culibox (also: nothing exists yet)
	RootLegacy                 // ~/.local/share/agent-box, from before the rename
	RootLinked                 // culibox, with the compat link left at the legacy path
)

// RootInfo is a resolved data root and why it is the one.
type RootInfo struct {
	Path string
	Kind RootKind
	// EmptyNew: Kind is RootLegacy and an empty new directory sits next to it.
	// An empty directory holds no config, so it cannot take anything over; the
	// migration removes it first.
	EmptyNew bool
}

// Source records where the effective config came from, for the startup log and
// culibox doctor.
type Source struct {
	Path    string   // the file read; "" with Default
	Default bool     // no file found: built-in defaults
	Root    RootInfo // the data root Resolve picked
	// XDGCulibox: the file read is the culibox ~/.config one. XDGTwin: something
	// (a file, or the link a move leaves) sits at the agent-box path beside it
	// (#266). See UnvettedWarning.
	XDGCulibox, XDGTwin bool
}

// StartFacts are what serve's start did before the config was resolved, for
// UnvettedWarning. The zero value is "nothing happened" (the CLI, doctor).
type StartFacts struct {
	// RootMoved: the data root move happened in this very start, so the root now
	// resolves as the new one although the config was read before any move.
	RootMoved bool
	// ConfigRelinked: this start only made a missing link at the agent-box path
	// for a culibox file that is already there. The link says nothing about where
	// the file came from, so the file still counts as new.
	ConfigRelinked bool
}

// UnvettedWarning is the WARN for a config read from the culibox ~/.config file
// before the one-time data root move (#266). "" when there is nothing to warn
// about.
func (s Source) UnvettedWarning(f StartFacts) string {
	fresh := s.XDGCulibox && (!s.XDGTwin || f.ConfigRelinked)
	if !fresh || s.Default || (s.Root.Kind != RootLegacy && !f.RootMoved) {
		return ""
	}
	return fmt.Sprintf("%s was read before the data root moved from %s. Before the rename this folder was not protected from box workspaces and shares, so check that the file is yours (it names the programs CuliBox runs), or delete it",
		s.Path, filepath.Join(filepath.Dir(s.Root.Path), LegacyRootName))
}

func (s Source) String() string {
	if s.Default {
		return "built-in defaults (no config file found)"
	}
	return s.Path
}

// DefaultRoots are the two default data roots: the new one and the one from
// before the rename.
func DefaultRoots(home string) (newRoot, legacyRoot string) {
	base := filepath.Join(home, ".local", "share")
	return filepath.Join(base, RootName), filepath.Join(base, LegacyRootName)
}

// HomeEnv reads the root env var: CULIBOX_HOME, or the deprecated
// AGENT_BOX_HOME. notice is a line for stderr (and a doctor WARN), empty when
// nothing is deprecated. Both set to different paths is an error: nothing is
// picked silently. A relative value is an error naming the variable it came
// from (ResolveRoot checks again, for callers that bring their own value).
func HomeEnv(getenv func(string) string) (value, notice string, err error) {
	n, o := getenv("CULIBOX_HOME"), getenv("AGENT_BOX_HOME")
	pick := func(name, v, notice string) (string, string, error) {
		if v != "" && !filepath.IsAbs(v) {
			return "", "", fmt.Errorf("%s %q is not an absolute path", name, v)
		}
		return v, notice, nil
	}
	switch {
	case o == "":
		return pick("CULIBOX_HOME", n, "")
	case n == "":
		return pick("AGENT_BOX_HOME", o, "AGENT_BOX_HOME is deprecated; rename it to CULIBOX_HOME")
	case filepath.Clean(n) == filepath.Clean(o):
		return pick("CULIBOX_HOME", n, "AGENT_BOX_HOME is deprecated and set to the same path as CULIBOX_HOME; unset it")
	}
	return "", "", fmt.Errorf("CULIBOX_HOME (%s) and AGENT_BOX_HOME (%s) disagree; unset one", n, o)
}

// Root is the data root's path; see ResolveRoot.
func Root(home, envHome string) (string, error) {
	ri, err := ResolveRoot(home, envHome)
	return ri.Path, err
}

// ResolveRoot picks CuliBox's single home: everything it writes (state, sockets
// on Linux, profiles, its config file) lives under it, so one directory is the
// whole install. It never moves or creates anything.
//
// envHome (the value HomeEnv returned) must be absolute, is used as is, and is
// never moved. Otherwise, with new = ~/.local/share/culibox and legacy =
// ~/.local/share/agent-box, each checked with Lstat:
//   - neither exists, or only new: new;
//   - only legacy: legacy (the migration candidate). A legacy symlink to some
//     other directory is used as spelled, not resolved (it is not moved either);
//   - legacy a symlink to new: new, RootLinked (the link a migration leaves);
//   - both the same directory otherwise: the spelling that is the real
//     directory, so hash-derived names stay stable;
//   - both exist and new is a real, empty directory: legacy with EmptyNew. When
//     legacy is empty too the answer is the same: legacy holds the original
//     hash-derived names and the choice must not depend on luck;
//   - legacy or new a broken symlink, or not stat-able: an error naming it;
//   - both exist with different content: an error naming both and the fix. A
//     silent pick would hide data, or let a planted directory take over, since
//     config.json names the binaries that run.
func ResolveRoot(home, envHome string) (RootInfo, error) {
	if envHome != "" {
		if !filepath.IsAbs(envHome) {
			return RootInfo{}, fmt.Errorf("CULIBOX_HOME (or the deprecated AGENT_BOX_HOME) %q is not an absolute path", envHome)
		}
		return RootInfo{Path: filepath.Clean(envHome), Kind: RootEnv}, nil
	}
	if !filepath.IsAbs(home) {
		return RootInfo{}, fmt.Errorf("home dir %q is not an absolute path; set $HOME", home)
	}
	newRoot, legacy := DefaultRoots(home)
	nfi, nerr := os.Lstat(newRoot)
	lfi, lerr := os.Lstat(legacy)
	if nerr != nil && !errors.Is(nerr, os.ErrNotExist) {
		return RootInfo{}, fmt.Errorf("data root %s cannot be checked: %w", newRoot, nerr)
	}
	if lerr != nil && !errors.Is(lerr, os.ErrNotExist) {
		return RootInfo{}, fmt.Errorf("data root %s cannot be checked: %w", legacy, lerr)
	}
	newExists, legExists := nerr == nil, lerr == nil
	if !legExists {
		return RootInfo{Path: newRoot, Kind: RootNew}, nil
	}
	lst, serr := os.Stat(legacy) // follows a link
	if serr != nil {
		return RootInfo{}, fmt.Errorf("data root %s is a broken link or cannot be read (%v); fix or remove it, or set CULIBOX_HOME", legacy, serr)
	}
	if !newExists {
		return RootInfo{Path: legacy, Kind: RootLegacy}, nil
	}
	nst, serr := os.Stat(newRoot)
	if serr != nil && nfi.Mode()&os.ModeSymlink != 0 {
		return RootInfo{}, fmt.Errorf("data root %s is a broken link or cannot be read (%v); fix or remove it, or set CULIBOX_HOME", newRoot, serr)
	}
	if serr == nil && os.SameFile(nst, lst) {
		legLink, newLink := lfi.Mode()&os.ModeSymlink != 0, nfi.Mode()&os.ModeSymlink != 0
		if legLink && !newLink {
			return RootInfo{Path: newRoot, Kind: RootLinked}, nil
		}
		// New a link to legacy, or (two links to a third directory, or a bind
		// mount) no real spelling to prefer: legacy, the name the hashes use today.
		return RootInfo{Path: legacy, Kind: RootLegacy}, nil
	}
	if nfi.IsDir() { // a real directory, not a link
		ents, err := os.ReadDir(newRoot)
		if err != nil {
			return RootInfo{}, fmt.Errorf("data root %s cannot be read: %w", newRoot, err)
		}
		if len(ents) == 0 {
			return RootInfo{Path: legacy, Kind: RootLegacy, EmptyNew: true}, nil
		}
	}
	return RootInfo{}, fmt.Errorf("both %s and %s exist with different content; set CULIBOX_HOME to the one to use, or move one aside", newRoot, legacy)
}

// Resolve loads the config. With an explicit -config (set), that file must
// exist. Otherwise the first existing file wins: <root>/config.json, then —
// only when no root env var is set (envHome == ""), so a relocated instance
// never picks up another one's file — $XDG_CONFIG_HOME/culibox/config.json
// (absolute XDG only, per the spec), else ~/.config/culibox/config.json, then
// the same under agent-box. Both XDG files existing as different files is an
// error. With none, Defaults(). The current directory is deliberately NOT
// searched: running culibox inside a cloned repo must not load that repo's
// config (allowed_hosts, password_env, agent bins). A config without data_dir
// gets the root as its data dir.
func Resolve(explicit string, set bool, home, xdg, envHome string) (Config, Source, error) {
	ri, err := ResolveRoot(home, envHome)
	if err != nil {
		label := "data root"
		if envHome != "" {
			label = "$CULIBOX_HOME"
		}
		return Config{}, Source{Path: label}, err
	}
	c, src, err := load(explicit, set, ri.Path, home, xdg, envHome != "")
	src.Root = ri
	if err == nil && c.DataDir == "" {
		c.DataDir = ri.Path
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
	cands := candidates(root, home, xdg, relocated)
	for i, p := range cands {
		fi, err := os.Stat(p)
		if err == nil {
			if i == 1 { // the culibox XDG file: refuse if the legacy one is a different file
				if lfi, lerr := os.Stat(cands[2]); lerr == nil && !os.SameFile(fi, lfi) {
					return Config{}, Source{Path: p}, fmt.Errorf("both %s and %s exist; keep one", p, cands[2])
				}
			}
			if err := checkOwned(p, fi); err != nil {
				return Config{}, Source{Path: p}, err
			}
			c, lerr := Load(p)
			src := Source{Path: p}
			if i == 1 { // a link a move leaves at the agent-box path counts as something there
				_, aerr := os.Lstat(cands[2])
				src.XDGCulibox, src.XDGTwin = true, !errors.Is(aerr, os.ErrNotExist)
			}
			return c, src, lerr
		}
		if !errors.Is(err, os.ErrNotExist) {
			return Config{}, Source{Path: p}, err
		}
	}
	return Defaults(), Source{Default: true}, nil
}

// XDGBase is the directory the ~/.config fallback lives in: an absolute
// $XDG_CONFIG_HOME, else ~/.config.
func XDGBase(home, xdg string) string {
	if xdg != "" && filepath.IsAbs(xdg) {
		return xdg
	}
	return filepath.Join(home, ".config")
}

// candidates are the config files load tries, in order: the root's, then (not
// when relocated) the culibox and legacy XDG ones.
func candidates(root, home, xdg string, relocated bool) []string {
	out := []string{filepath.Join(root, "config.json")}
	if !relocated {
		base := XDGBase(home, xdg)
		out = append(out, filepath.Join(base, RootName, "config.json"), filepath.Join(base, LegacyRootName, "config.json"))
	}
	return out
}

// ConfigFiles is every config file Resolve (with the same arguments) reads on
// the next start, absolute, whether or not it exists yet: the explicit file,
// else each candidate. Unrelocated it adds the default root that is NOT in use,
// so both default roots are protected whichever one is. A boxed workspace must
// not reach any of them (nor the files they link to), or a box could write the
// config that names the binaries culibox runs.
func ConfigFiles(explicit string, set bool, home, xdg, envHome string) ([]string, error) {
	if set {
		p, err := filepath.Abs(explicit)
		if err != nil {
			return nil, err
		}
		return []string{p}, nil
	}
	ri, err := ResolveRoot(home, envHome)
	if err != nil {
		return nil, err
	}
	out := candidates(ri.Path, home, xdg, envHome != "")
	if envHome == "" {
		newRoot, legacy := DefaultRoots(home)
		other := newRoot
		if ri.Path == newRoot {
			other = legacy
		}
		out = append(out, filepath.Join(other, "config.json"))
	}
	return out, nil
}

// Defaults is the zero-config laptop setup: loopback only, token login (no
// password_env), host tier, one claude agent with the built-in launch options.
// The quick shell is added at startup by WithDetectedShell.
func Defaults() Config {
	return Config{
		BindAddr:       "127.0.0.1:8899",
		DefaultRuntime: RuntimeHost,
		Agents:         []AgentConfig{{Name: "claude", Bin: "claude"}},
		Options:        withBuiltinOptions(nil),
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

// CheckOwned is checkOwned for other packages (the ~/.config move checks the
// directory it moves a config into).
func CheckOwned(what string, fi os.FileInfo) error { return checkOwned(what, fi) }

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
