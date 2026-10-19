package bundle

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

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
	ReasonPluginCredentials = "a plugin marketplace URL carries credentials; agent-box never mounts credentials into a box"
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
	switch entry, state := scanPluginCredentials(pdir); state {
	case credFound:
		return nil, []Drop{{Kind: KindPlugins, Name: entry, Reason: ReasonPluginCredentials}}
	case credUnreadable:
		// Fail closed: a file that cannot be checked is a file that cannot be
		// vouched for.
		return nil, []Drop{{Kind: KindPlugins, Name: "*", Reason: ReasonPluginsIncomplete}}
	}
	return mounts, nil
}

// Why the set is scanned at all: a marketplace is a git clone, and a clone made
// with a token (https://<token>@github.com/org/private-marketplace) keeps that
// token in .git/config; known_marketplaces.json and installed_plugins.json can
// name the same URL. The set is mounted into a box that has network egress, so
// mounting it would publish a credential into the very place the bundle rule
// "allowlisted, secret-free" says none may go. One such URL refuses the whole
// set, like every other refusal here.
//
// The scan runs on every Ensure, so it stays cheap: it reads only the two json
// files and .git/config files, descends at most maxCredDepth directories below
// cache/ and marketplaces/, never follows a symlink, and stops at the first hit.
const (
	maxCredDepth = 4
	maxCredFile  = 1 << 20
)

type credState int

const (
	credClean credState = iota
	credFound
	credUnreadable
)

// scanPluginCredentials returns the entry the credential was found in
// ("installed_plugins.json", "known_marketplaces.json", "cache" or
// "marketplaces"), never the URL or a path below the entry.
func scanPluginCredentials(pdir string) (string, credState) {
	for _, name := range []string{"installed_plugins.json", "known_marketplaces.json"} {
		if st := fileHasCredentials(filepath.Join(pdir, name)); st != credClean {
			return name, st
		}
	}
	for _, name := range []string{"marketplaces", "cache"} {
		if st := dirHasCredentials(filepath.Join(pdir, name), 0); st != credClean {
			return name, st
		}
	}
	return "", credClean
}

// dirHasCredentials checks dir/.git/config, then recurses. .git itself must be a
// real directory and config a regular file; anything else is not read.
func dirHasCredentials(dir string, depth int) credState {
	git := filepath.Join(dir, ".git")
	if fi, err := os.Lstat(git); err == nil && fi.IsDir() {
		cfg := filepath.Join(git, "config")
		if fi, err := os.Lstat(cfg); err == nil && fi.Mode().IsRegular() {
			if st := fileHasCredentials(cfg); st != credClean {
				return st
			}
		}
	}
	if depth >= maxCredDepth {
		return credClean
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return credUnreadable
	}
	for _, e := range ents {
		if !e.IsDir() || e.Name() == ".git" { // IsDir is false for a symlink
			continue
		}
		if st := dirHasCredentials(filepath.Join(dir, e.Name()), depth+1); st != credClean {
			return st
		}
	}
	return credClean
}

func fileHasCredentials(p string) credState {
	f, err := os.Open(p)
	if err != nil {
		return credUnreadable
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxCredFile+1))
	if err != nil || len(b) > maxCredFile {
		return credUnreadable
	}
	if hasURLCredentials(b) {
		return credFound
	}
	return credClean
}

// urlAuthority matches scheme://authority@ where the authority runs to the first
// / ? # quote or space, so an @ later in a path or query is not userinfo.
var urlAuthority = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.\-]*)://([^/?#\s"'\\<>]*)@`)

// hasURLCredentials is the rule. A URL carries a credential when its authority
// has userinfo and either the scheme is http or https (any user or user:pass,
// since that is how a token is spelled) or, for any other scheme, the userinfo
// has a :password part. So https://tok@host and ssh://git:pw@host are refused;
// ssh://git@host, scp-style git@host:o/r.git (no scheme) and an empty
// https://@host are not.
func hasURLCredentials(b []byte) bool {
	for _, m := range urlAuthority.FindAllSubmatch(b, -1) {
		scheme, userinfo := strings.ToLower(string(m[1])), string(m[2])
		if userinfo == "" {
			continue
		}
		if scheme == "http" || scheme == "https" || strings.Contains(userinfo, ":") {
			return true
		}
	}
	return false
}
