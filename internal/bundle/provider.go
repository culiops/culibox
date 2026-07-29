package bundle

import (
	"os"

	"github.com/culiops/agent-box/internal/box"
)

// Provider adapts a Manager to what the session layer needs: for a given agent
// binary, the read-only mounts and argv flags that deliver its config.
//
// It rebuilds lazily on every call, so editing the config directory is picked up
// on the next spawn without any command to remember; an unchanged source costs
// one cheap walk.
type Provider struct{ m *Manager }

func NewProvider(m *Manager) *Provider { return &Provider{m: m} }

// For returns nothing at all for an agent with no layout — a shell profile must
// never be handed agent flags.
func (p *Provider) For(bin string) ([]box.Mount, []string, string, error) {
	layout, ok := LayoutFor(bin)
	if !ok {
		return nil, nil, "", nil
	}
	built, err := p.m.Ensure()
	if err != nil {
		return nil, nil, "", err
	}
	exists := func(path string) bool { _, err := os.Stat(path); return err == nil }
	var mounts []box.Mount
	for _, mt := range layout.MountsFor(built.Dir, boxHome, exists) {
		mounts = append(mounts, box.Mount{Host: mt.Host, Box: mt.Box})
	}
	return mounts, layout.Flags(built.Dir), built.Hash, nil
}

// boxHome mirrors the box package's home path. Duplicated rather than imported
// as a constant so the dependency stays one-way.
const boxHome = "/home/agent"
