package bundle

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/culiops/agent-box/internal/box"
)

// Provider adapts a Manager to what the session layer needs: the read-only
// mounts a box carries and, for a given agent binary, the argv flags that point
// it at its config.
//
// It rebuilds lazily on every call, so editing the config directory is picked up
// on the next spawn without any command to remember; an unchanged source costs
// one cheap walk.
type Provider struct{ m *Manager }

func NewProvider(m *Manager) *Provider { return &Provider{m: m} }

// Grant is everything one spawn takes from the bundle: what the box mounts, what
// the agent is passed, which bundle that is, and what was left out and why.
type Grant struct {
	Mounts []box.Mount
	Flags  []string
	Hash   string
	// Drops is the lock's drops followed by the plugin drops. The plugin set is
	// not part of the bundle dir: it is a read-only, git-free snapshot copied
	// beside it, one dir per part, so a plugin change recopies only that part;
	// the bundle is rebuilt only when whether the set is mounted changes. Its
	// drops are decided on every Ensure with the snapshot, so they cannot live
	// in the lock.
	Drops []Drop
	// Imports is how many of Mounts are CLAUDE.md imports.
	Imports int
}

// For returns the box's mounts and the agent's flags. The two are deliberately
// decided differently (brain #131):
//
//   - Mounts belong to the BOX, so they are the same for every bin: the union
//     of every layout's mounts. A box is keyed by workspace, account and
//     binding, not by agent, and its mounts are fixed when it starts, so a box
//     a shell started must already carry what a claude session in the same
//     box will need. It is the read-only, allowlisted, secret-free bundle every
//     boxed claude already gets, so a shell box exposes nothing new.
//   - Flags belong to the AGENT: only bin's own layout, so a shell profile is
//     never handed agent flags.
//
// For an agent the bundle root is required (its flags point into it) and the
// config overlays are Optional, so a box that lacks one stays usable while it
// runs. For a bin without a layout every mount is Optional: a shell reads none
// of them, so it must never be refused a box that lacks them.
//
// The plugin mounts are always Optional: a live box created before plugins were
// mounted is reused and its session runs without them, rather than being refused.
//
// withImports adds one mount per file CLAUDE.md imports, file by file so nothing
// else in the imports dir is exposed. They are Optional (a reused live box
// lacks them and runs without) and Yield (a workspace or another mount that
// already covers the path wins, so an import never shadows real content). With
// withImports false the mounts and the import drops are both left out.
func (p *Provider) For(bin string, withImports bool) (Grant, error) {
	built, err := p.m.Ensure()
	if err != nil {
		return Grant{}, err
	}
	layout, agent := LayoutFor(bin)
	exists := func(path string) bool { _, err := os.Stat(path); return err == nil }
	var mounts []box.Mount
	seen := map[string]bool{}
	for _, l := range layouts {
		for _, mt := range l.MountsFor(built.Dir, boxHome, exists) {
			if seen[mt.Box] { // layouts share the bundle root
				continue
			}
			seen[mt.Box] = true
			mounts = append(mounts, box.Mount{Host: mt.Host, Box: mt.Box, Optional: !agent || mt.Box != BoxBundleDir})
		}
	}
	for _, mt := range built.pluginMounts {
		if !seen[mt.Box] {
			seen[mt.Box] = true
			mounts = append(mounts, mt)
		}
	}
	imports := 0
	if withImports {
		for _, li := range built.Lock.Imports {
			if !validImportBox(li.Box) || seen[li.Box] {
				continue // the lock is host-owned, but a mount is not worth trusting it blindly
			}
			seen[li.Box] = true
			imports++
			mounts = append(mounts, box.Mount{
				Host:     filepath.Join(importsDir(built.Dir), strings.TrimPrefix(li.Box, "/")),
				Box:      li.Box,
				Optional: true,
				Yield:    true,
			})
		}
	}
	var flags []string
	if agent {
		flags = layout.Flags(built.Dir)
	}
	var drops []Drop
	for _, d := range built.Lock.Dropped {
		if d.Kind == KindImport && !withImports {
			continue
		}
		drops = append(drops, d)
	}
	drops = append(drops, built.pluginDrops...)
	return Grant{Mounts: mounts, Flags: flags, Hash: built.Hash, Drops: drops, Imports: imports}, nil
}

// validImportBox is an absolute, clean in-box path that is not the root.
func validImportBox(p string) bool {
	return p != "/" && filepath.IsAbs(p) && filepath.Clean(p) == p
}

// boxHome mirrors the box package's home path. Duplicated rather than imported
// as a constant so the dependency stays one-way.
const boxHome = "/home/agent"
