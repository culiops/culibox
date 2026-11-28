package main

import (
	"github.com/culiops/culibox/internal/analytics"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/gitrepo"
)

// analyticsResolveEnv is the func the analytics service calls on each ingest
// pass to learn where sessions belong. It re-reads profiles and cloneDirs on
// every call, so a profile saved or a clone dir recorded after boot is seen on
// the next pass. convKey may be nil (no containment layer): in-place profiles
// then cannot be matched, and the service never binds a method of a nil manager.
func analyticsResolveEnv(profiles func() []config.AgentConfig, cloneDirs func() ([]string, error),
	convKey func(workspace, account, profile string) string, foldCase bool) func() analytics.ResolveEnv {
	return func() analytics.ResolveEnv {
		env := analytics.ResolveEnv{
			ConvKey:  convKey,
			RepoRoot: gitrepo.WorkspaceRoot,
			FoldCase: foldCase,
		}
		for _, p := range profiles() {
			env.Profiles = append(env.Profiles, analytics.ProfileRef{
				Name: p.Name, DefaultCwd: p.DefaultCwd, CloneOf: p.CloneOf, Account: p.Account,
			})
		}
		// An unreadable or malformed record is an empty one here: the clone
		// guard reports it where it matters, and this pass only files history.
		if dirs, err := cloneDirs(); err == nil {
			env.CloneDirs = dirs
		}
		return env
	}
}
