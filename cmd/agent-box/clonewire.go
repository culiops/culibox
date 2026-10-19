package main

import (
	"context"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/clone"
	"github.com/culiops/agent-box/internal/config"
)

// cloneHooks is the daemon's session.CloneHooks: it hands the session manager
// the clone package's Prepare and BoxOwned over the daemon's own paths. clones
// is read on every call, so a profile saved after boot is covered.
type cloneHooks struct {
	env    clone.Env
	clones func() []config.Clone
}

func (h *cloneHooks) Prepare(ctx context.Context, c config.Clone, withMemory bool) (box.CloneMounts, error) {
	return clone.Prepare(ctx, h.env, c, withMemory)
}

func (h *cloneHooks) BoxOwned(cwd string) (string, bool) {
	return clone.BoxOwned(cwd, h.clones())
}

// newCloneHooks builds the clone-mode hooks. Clone mode is Linux-only in
// Phase A and lives in the box layer, so it returns nil, nil off Linux or
// without a layer: the caller then leaves the hooks unset and clone-mode
// profiles are refused. memRoot is the config bundle source ("" with the bundle
// off), and must be absolute when set. An error means the host git is missing.
func newCloneHooks(goos string, layer *boxLayer, home, memRoot string, protected []box.ProtectedDir, clones func() []config.Clone) (*cloneHooks, error) {
	if goos != "linux" || layer == nil {
		return nil, nil
	}
	g, err := clone.NewExecGit()
	if err != nil {
		return nil, err
	}
	return &cloneHooks{
		env:    clone.Env{Git: g, Home: home, Protected: protected, MemoryRoot: memRoot},
		clones: clones,
	}, nil
}
