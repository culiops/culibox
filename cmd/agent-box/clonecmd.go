package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/culiops/culibox/internal/boxvm"
	"github.com/culiops/culibox/internal/bundle"
	"github.com/culiops/culibox/internal/clone"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/extraca"
	"github.com/culiops/culibox/internal/launcher"
	"github.com/culiops/culibox/internal/store"
)

// cloneFetch and cloneBackends are swappable so the command is testable without
// docker or git.
var (
	cloneFetch    = clone.Fetch
	cloneBackends = realCloneBackends
)

// realCloneBackends finds host git and the docker CLI the way the daemon does.
func realCloneBackends(cfg config.Config, goos, dataDir, home string) (clone.Git, clone.Docker, error) {
	g, err := clone.NewExecGit()
	if err != nil {
		return nil, nil, err
	}
	d, err := cloneDocker(context.Background(), cfg, goos, dataDir, home, boxvm.ExecRunner{})
	if err != nil {
		return nil, nil, err
	}
	return g, d, nil
}

// cloneDocker is the docker the bundle container runs on: the box layer's, as
// in the daemon, so on macOS the box VM's (where the clone is shared). It never
// starts the VM: a CLI starting colima beside a serving daemon could race its
// own start, so a stopped VM is refused with the way to start it.
func cloneDocker(ctx context.Context, cfg config.Config, goos, dataDir, home string, runner boxvm.Runner) (clone.Docker, error) {
	l, err := newBoxLayer(cfg, dataDir, home, goos, runner, nil)
	if err != nil {
		return nil, err
	}
	if l.vmStopped(ctx) {
		return nil, errors.New("the box VM is not running; start it with `agent-box box setup` (or any boxed session), then run clone fetch again")
	}
	return clone.ExecDocker{Bin: l.dockerBin, Args: l.dockerArgs}, nil
}

// runClone serves `agent-box clone fetch`: it brings a clone-mode profile's
// branch home as box/<branch> in clone_of. It reads the profile store without
// writing it, so it is safe to run while agent-box serves (a daemon restart aborts it; rerun).
func runClone(a cliArgs, cfg config.Config, home, goos string, cfgFiles []string, w io.Writer) int {
	fail := func(format string, args ...any) int {
		fmt.Fprintf(w, "agent-box clone: "+format+"\n", args...)
		return 1
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
	p.Box = config.BoxConfig{}  // retired per-profile box.image: ignored on load, as in the daemon
	p.Env = withoutCAEnv(p.Env) // a stored CA env name is dropped at spawn; only a save refuses it
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
	g, d, err := cloneBackends(cfg, goos, dataDir, home)
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
		Protected: protectedDirs(dataDir, cfgFiles, profilesPath, bundle.DefaultSource(), bundleSourceOf(cfg)),

		DataDir:         dataDir,
		AllowOldHistory: a.AllowOldHistory,
	})
	if err != nil {
		return fail("%v", err)
	}
	ref := strings.TrimPrefix(res.Ref, "refs/heads/")
	// The branch is home now: its not-home entry would only mislead the profile
	// row. Best effort; a state that cannot be rewritten is the next return's to fix.
	forgetErr := clone.ForgetNotHome(dataDir, c.Dir, a.Branch)
	fmt.Fprintf(w, "fetched %s (%d commit(s) not on %s):\n", ref, len(res.Commits), a.Base)
	for _, line := range res.Commits {
		fmt.Fprintf(w, "  %s\n", line)
	}
	for _, warn := range res.Warnings {
		fmt.Fprintf(w, "WARN: %s\n", warn)
	}
	if forgetErr != nil {
		fmt.Fprintf(w, "WARN: could not clear %s from the not-home list: %s\n", a.Branch, clone.Sanitize(forgetErr.Error()))
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

// withoutCAEnv is env minus the CA variable names agent-box owns. It returns a
// copy when it drops anything, so a map shared with the store is never mutated.
func withoutCAEnv(env map[string]string) map[string]string {
	var out map[string]string
	for _, n := range extraca.DeniedEnvNames {
		if _, ok := env[n]; !ok {
			continue
		}
		if out == nil {
			out = make(map[string]string, len(env))
			for k, v := range env {
				out[k] = v
			}
		}
		delete(out, n)
	}
	if out == nil {
		return env
	}
	return out
}
