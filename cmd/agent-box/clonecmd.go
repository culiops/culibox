package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/bundle"
	"github.com/culiops/agent-box/internal/clone"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/launcher"
	"github.com/culiops/agent-box/internal/store"
)

// cloneFetch and cloneBackends are swappable so the command is testable without
// docker or git.
var (
	cloneFetch    = clone.Fetch
	cloneBackends = realCloneBackends
)

// realCloneBackends finds host git and the docker CLI the way the daemon does.
func realCloneBackends(cfg config.Config) (clone.Git, clone.Docker, error) {
	g, err := clone.NewExecGit()
	if err != nil {
		return nil, nil, err
	}
	bin, err := box.LookDocker(cfg.DockerBin)
	if err != nil {
		return nil, nil, err
	}
	return g, clone.ExecDocker{Bin: bin}, nil
}

// runClone serves `agent-box clone fetch`: it brings a clone-mode profile's
// branch home as box/<branch> in clone_of. It reads the profile store without
// writing it, so it is safe to run while agent-box serves.
func runClone(a cliArgs, cfg config.Config, home string, cfgFiles []string, w io.Writer) int {
	fail := func(format string, args ...any) int {
		fmt.Fprintf(w, "agent-box clone: "+format+"\n", args...)
		return 1
	}
	if runtime.GOOS == "darwin" {
		return fail("clone mode is Linux-only for now")
	}
	dataDir, err := filepath.Abs(cfg.ResolvedDataDir(home))
	if err != nil {
		return fail("%v", err)
	}
	profilesPath := profilesPathFor(cfg, dataDir)
	st, err := store.Open(profilesPath)
	if err != nil {
		return fail("%v", err)
	}
	// The store overrides a config seed of the same name, as in the daemon.
	p, ok := st.Get(a.Profile)
	if !ok {
		for _, s := range cfg.Agents {
			if s.Name == a.Profile {
				p, ok = s, true
				break
			}
		}
	}
	if !ok {
		return fail("unknown profile %q", a.Profile)
	}
	defs := make(map[string]config.OptionDef, len(cfg.Options))
	for _, o := range cfg.Options {
		defs[o.Key] = o
	}
	if err := config.ValidateProfile(p, defs); err != nil {
		return fail("%v", err)
	}
	c, err := p.Clone()
	if err != nil {
		return fail("%v", err)
	}
	if c == nil {
		return fail("profile %q has no clone_of", a.Profile)
	}
	// clone_of must be host-owned: a box-written .git there would run its hooks
	// in the host git calls below. Same live-profiles + clone-dirs-record check
	// as the daemon, read-only.
	all := st.List()
	have := map[string]bool{}
	for _, s := range all {
		have[s.Name] = true
	}
	for _, s := range cfg.Agents {
		if !have[s.Name] {
			all = append(all, s)
		}
	}
	owner := &cloneHooks{
		clones: func() []config.Clone { return launcher.ClonesOf(all) },
		file:   filepath.Join(dataDir, cloneDirsFile),
	}
	if err := owner.checkOf(c.Of); err != nil {
		return fail("%v", err)
	}
	g, d, err := cloneBackends(cfg)
	if err != nil {
		return fail("%v", err)
	}
	// Ctrl-C / SIGTERM cancel the fetch so its deferred cleanup runs: without
	// this the process dies at once and leaves up to 512 MiB under clone-fetch.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := cloneFetch(ctx, g, d, clone.FetchRequest{
		Clone:     *c,
		Branch:    a.Branch,
		Base:      a.Base,
		Image:     cfg.BoxImageOrDefault(), // D10: the global image, not a per-profile one
		Instance:  cfg.BoxInstance(),
		TempDir:   filepath.Join(dataDir, "clone-fetch"),
		Home:      home,
		Protected: protectedDirs(dataDir, cfgFiles, profilesPath, bundle.DefaultSource()),
	})
	if err != nil {
		return fail("%v", err)
	}
	ref := strings.TrimPrefix(res.Ref, "refs/heads/")
	fmt.Fprintf(w, "fetched %s (%d commit(s) not on %s):\n", ref, len(res.Commits), a.Base)
	for _, line := range res.Commits {
		fmt.Fprintf(w, "  %s\n", line)
	}
	for _, warn := range res.Warnings {
		fmt.Fprintf(w, "WARN: %s\n", warn)
	}
	fmt.Fprintf(w, "review: git -C %s log -p %s..%s; merge per your review policy\n", shellQuote(c.Of), a.Base, ref)
	return 0
}

// shellQuote makes s safe to paste into a shell: left bare when it has only
// safe characters, else in single quotes with embedded quotes escaped.
func shellQuote(s string) string {
	safe := s != ""
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_./@%+=:,-", r)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
