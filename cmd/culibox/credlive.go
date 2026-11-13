package main

import (
	"sync"
	"sync/atomic"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/ghapp"
	"github.com/culiops/culibox/internal/launcher"
	"github.com/culiops/culibox/internal/storerefs"
	"github.com/culiops/culibox/internal/web"
)

// tokenSet is the broker's view of which static_tokens entries are defined. It
// is swapped whole, so a lookup sees the old list or the new one, never a mix.
// A nil *tokenSet is an empty one.
type tokenSet struct {
	p atomic.Pointer[[]config.StaticToken]
}

func (t *tokenSet) Set(entries []config.StaticToken) {
	if t == nil {
		return
	}
	cp := append([]config.StaticToken(nil), entries...)
	t.p.Store(&cp)
}

func (t *tokenSet) Get() []config.StaticToken {
	if t == nil {
		return nil
	}
	if p := t.p.Load(); p != nil {
		return *p
	}
	return nil
}

// credLive pushes the merged credential lists (config.json seeds plus the
// web-defined credentials file) to every holder of them. Boot and every web
// credential save go through Apply, so no holder can keep a stale copy.
//
// The fields are wired once at startup, before the first concurrent Apply; a
// nil holder is skipped. Holders that have a setter swap their list under their
// own lock. A spawn racing an Apply sees the old or the new list in each
// holder; running sessions keep the definitions they were spawned with.
//
// Holders deliberately NOT here: the egress service's private-CIDR overlap
// warning (startup only; web tokens accept public egress hosts only) and the
// config bundle's MCP commands (web-created tokens carry no `mcp`).
type credLive struct {
	lnch        *launcher.AgentLauncher
	pm          *web.ProfileManager
	gh          *ghapp.Minter
	tokens      *tokenSet
	passwordEnv string
	inUse       atomic.Bool

	// scrubMu guards scrub: every variable name ever handed to the launcher's
	// daemon-secret list. It only grows (see Apply).
	scrubMu sync.Mutex
	scrub   map[string]bool
}

// Apply installs cfg's credential lists in every holder. Callers serialise
// Apply and call it inside the same critical section as the write that produced
// cfg, so two saves cannot push their lists out of order.
//
// The daemon-env scrub list is security-critical: a token entry's env var lives
// in the daemon's environment, and must never reach a session through an
// inherited base. The list is therefore never shrunk for the life of the
// process: it is the union of the password variable and every token variable
// any Apply (boot included) has seen, so a definition that later disappears
// from the merged config still has its variable scrubbed.
func (c *credLive) Apply(cfg config.Config) {
	if c.lnch != nil {
		c.lnch.SetDaemonSecretEnv(c.scrubNames(cfg)...)
		c.lnch.SetStaticTokens(cfg.StaticTokens)
		c.lnch.SetAWSConnections(cfg.AWSConnections)
	}
	if c.pm != nil {
		c.pm.SetGitHubApps(cfg.GitHubApps)
		c.pm.SetStaticTokens(cfg.StaticTokens)
		c.pm.SetAWSConnections(cfg.AWSConnections)
	}
	if c.gh != nil {
		c.gh.SetApps(cfg.GitHubApps)
	}
	c.tokens.Set(cfg.StaticTokens)
	c.inUse.Store(storerefs.InUse(cfg))
}

// scrubNames adds this cfg's names to the running union and returns all of it.
func (c *credLive) scrubNames(cfg config.Config) []string {
	c.scrubMu.Lock()
	defer c.scrubMu.Unlock()
	if c.scrub == nil {
		c.scrub = map[string]bool{}
	}
	c.scrub[c.passwordEnv] = true
	for _, v := range tokenVars(cfg.StaticTokens) {
		c.scrub[v] = true
	}
	names := make([]string, 0, len(c.scrub))
	for n := range c.scrub {
		names = append(names, n)
	}
	return names
}

// InUse reports whether any definition reads the encrypted store (behind
// web.Server.StoreInUse).
func (c *credLive) InUse() bool { return c.inUse.Load() }
