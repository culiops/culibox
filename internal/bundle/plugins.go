package bundle

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/culiops/agent-box/internal/box"
)

// Drop kinds and the fixed reasons the plugin and bundle paths record. The
// strings are user-facing and pinned by tests, so they live here as constants
// the session layer reuses instead of retyping.
const (
	KindPlugins = "plugins"
	KindBundle  = "bundle"

	ReasonPluginsIncomplete = "the host plugin dir is incomplete; plugins load only with installed_plugins.json, known_marketplaces.json, cache/ and marketplaces/ all present"
	ReasonPluginSymlink     = "is a symlink on the host; agent-box mounts only real files and directories"
	ReasonBundleFailed      = "the config bundle could not be built; this box runs without your CLAUDE.md, skills, MCP servers, settings and plugins"
)

// pluginEntry is one part of the host plugins/ dir a box needs.
type pluginEntry struct {
	name string
	dir  bool
}

// pluginEntries are exactly the parts of plugins/ that resolve which plugins
// load, in mount order. data/, store/ and the catalog caches are deliberately
// absent: they are host state a box has no business reading.
var pluginEntries = []pluginEntry{
	{"installed_plugins.json", false},
	{"known_marketplaces.json", false},
	{"cache", true},
	{"marketplaces", true},
}

// pluginMounts decides, from the host config dir src, whether the plugin set is
// mounted into a box. It is the ONE place that decides: the bundle's source hash
// and settings, and the provider's mounts and drops, all take the verdict from
// here, so they cannot disagree.
//
// All or nothing. Measured 2026-10-18: a partial set "failed to load", and only
// all four entries enable plugins. And nothing in the set may be a symlink:
// Docker resolves a symlinked bind source on the HOST, as root, so a planted
// cache -> ~/.ssh would publish its target into the box. That is why this uses
// Lstat and refuses rather than following — the opposite of the skills walk,
// which copies and so can follow safely.
//
// No installed_plugins.json (or no plugins/ at all) means the user simply has
// no plugins: no mounts and no drop. Present-but-unusable is a drop, so the
// loss is a named decision rather than silence.
//
// Each entry is mounted ONCE, at the box home. claude rebases the absolute
// installPath recorded in installed_plugins.json onto its own config dir
// (measured 2026-10-19), so a second mount at the host path is unneeded and
// would only leak the host layout.
func pluginMounts(src string) ([]box.Mount, []Drop) {
	pdir := filepath.Join(src, "plugins")
	fi, err := os.Lstat(pdir)
	if err != nil {
		return nil, nil // absent: no plugins
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, []Drop{{Kind: KindPlugins, Name: "plugins", Reason: ReasonPluginSymlink}}
	}
	if !fi.IsDir() {
		return nil, nil
	}
	if _, err := os.Lstat(filepath.Join(pdir, pluginEntries[0].name)); errors.Is(err, fs.ErrNotExist) {
		return nil, nil // no installed_plugins.json: the user has no plugins
	}

	var mounts []box.Mount
	var drops []Drop
	incomplete := false
	for _, e := range pluginEntries {
		host := filepath.Join(pdir, e.name)
		fi, err := os.Lstat(host)
		switch {
		case err != nil:
			incomplete = true
		case fi.Mode()&os.ModeSymlink != 0:
			drops = append(drops, Drop{Kind: KindPlugins, Name: e.name, Reason: ReasonPluginSymlink})
		case fi.IsDir() != e.dir:
			// A bind of the wrong type is a docker mount error, and the set is
			// unusable either way.
			incomplete = true
		default:
			mounts = append(mounts, box.Mount{Host: host, Box: path.Join(boxHome, ".claude", "plugins", e.name), Optional: true})
		}
	}
	if len(drops) > 0 {
		return nil, drops // a symlink names itself; the rest is moot
	}
	if incomplete {
		return nil, []Drop{{Kind: KindPlugins, Name: "*", Reason: ReasonPluginsIncomplete}}
	}
	return mounts, nil
}
