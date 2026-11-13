package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/boxedws"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/session"
	"github.com/culiops/culibox/internal/store"
	"github.com/culiops/culibox/internal/vmshares"
	"github.com/culiops/culibox/internal/web"
)

// shareListTimeout bounds the CLI's one docker listing of running boxes.
const shareListTimeout = 10 * time.Second

// cliBoxSessions is the running boxes as the folder service sees sessions: one
// docker listing taken now (the CLI has no session manager). A VM that is not
// running, or a listing that fails, gives none; the apply guard (noLiveBoxes)
// is what fails closed.
func cliBoxSessions(ctx context.Context, l *boxLayer) func() []session.Info {
	var out []session.Info
	if l.vm.SetUp() {
		if st, err := l.vm.Status(ctx); err == nil && st.Running {
			lctx, cancel := context.WithTimeout(ctx, shareListTimeout)
			bs, err := l.cli.ListBoxes(lctx, box.LabelInstance)
			cancel()
			if err == nil {
				for _, b := range bs {
					if b.State == "running" {
						out = append(out, session.Info{Name: b.Name, Runtime: "box", Cwd: b.Workspace})
					}
				}
			}
		}
	}
	return func() []session.Info { return out }
}

// newCLIShareSvc builds the folder service the way serve does, from the same
// inputs: the merged profile list (config seeds plus profiles.json), the boxed
// workspace record and the config bundle's memory root. On a first run with no
// vm-shares.json it derives and saves the list.
func newCLIShareSvc(ctx context.Context, cfg config.Config, l *boxLayer) (*vmShareSvc, error) {
	agents, _ := config.WithDetectedShell(cfg.Agents)
	st, err := store.Open(cfg.ResolvedProfilesPath(l.dataDir))
	if err != nil {
		return nil, fmt.Errorf("profiles: %w", err)
	}
	merged := web.MergeProfiles(agents, st.List(), cfg.Options)
	reg, _, err := boxedws.Open(l.dataDir, l.boxes.UnboxableIgnoringShares)
	if err != nil {
		return nil, fmt.Errorf("boxed workspace record: %w", err)
	}
	vc := l.vm.Config()
	profiles := func() []vmshares.Profile { return vmProfiles(cfg, merged) }
	ctl := newLayerVMControl(l, cliBoxSessions(ctx, l))
	svc, err := newVMShareSvc(l.dataDir, vmshares.Env{Home: vc.Home, DataRoot: vc.Root, Fold: vc.Fold, Protected: l.protected}, ctl, profiles, reg.List,
		func() []string { return vmMemoryDirs(bundleSourceOf(cfg), profiles()) }, nil)
	if err != nil {
		return nil, err
	}
	ctl.jobRunning = svc.jobRunning
	return svc, nil
}

// boxShareVM is `box share list|add|rm|apply`. Only apply touches the VM.
func boxShareVM(ctx context.Context, a cliArgs, cfg config.Config, l *boxLayer, w io.Writer) int {
	svc, err := newCLIShareSvc(ctx, cfg, l)
	if err != nil {
		fmt.Fprintf(w, "FAIL box VM folders: %v\n", err)
		return 1
	}
	switch a.ShareAction {
	case "add":
		mode := string(vmshares.RW)
		if a.ReadOnly {
			mode = string(vmshares.RO)
		}
		v, res, err := svc.Add(a.SharePath, mode, false)
		switch {
		case err != nil:
			fmt.Fprintf(w, "refusing: %v\n", err)
			return 1
		case res.CoveredBy != "":
			fmt.Fprintf(w, "already covered by %s; nothing to add\n", res.CoveredBy)
			return 0
		}
		return printShares(w, svc, v, true)
	case "rm":
		path, err := vmshares.Expand(a.SharePath, svc.env.Home)
		var v web.VMFoldersView
		if err == nil {
			v, err = svc.Remove(path)
			if errors.Is(err, vmshares.ErrNotListed) {
				// The list holds symlink-resolved paths.
				v, err = svc.Remove(vmshares.Resolve(path))
			}
		}
		if err != nil {
			fmt.Fprintf(w, "refusing: %v\n", err)
			return 1
		}
		return printShares(w, svc, v, true)
	case "apply":
		return applyShares(ctx, a.Force, svc, l, w)
	}
	v, err := svc.View()
	if err != nil {
		fmt.Fprintf(w, "FAIL box VM folders: %v\n", err)
		return 1
	}
	return printShares(w, svc, v, false)
}

// printShares prints the list, one `rw|ro  <display>  <origin>` line per folder
// and the CuliBox data line, then either the not-applied note (after a change)
// or the state line (list).
func printShares(w io.Writer, svc *vmShareSvc, v web.VMFoldersView, changed bool) int {
	if v.ListError != "" {
		fmt.Fprintf(w, "FAIL box VM folders: %s\n", v.ListError)
		return 1
	}
	n := 0
	for _, r := range v.Rows {
		if r.Pending == "removed" {
			continue
		}
		n++
		fmt.Fprintf(w, "%s  %s  %s\n", r.Mode, r.Display, r.Origin)
	}
	fmt.Fprintf(w, "rw  CuliBox data (%s/{boxstate}; ro: bundle, broker, ca)\n", vmshares.Display(svc.env.DataRoot, svc.env.Home))
	switch {
	case changed:
		fmt.Fprintln(w, "not applied yet: run `culibox box share apply` (restarts the box VM)")
	case v.State == "pending":
		fmt.Fprintln(w, "changes not applied yet: run culibox box share apply")
	case v.State == "whole_home":
		fmt.Fprintln(w, vmshares.WholeHomeLine(n))
	case v.State == "not_set_up":
		fmt.Fprintln(w, "the box VM is not set up yet: `culibox box setup` starts it with this list")
	default:
		fmt.Fprintln(w, "applied")
	}
	return 0
}

// applyShares restarts the VM with the saved list and prints each step as it
// ends. Without force it refuses while any box runs (noLiveBoxes: an idle box
// counts, and a VM or box listing that cannot be read is refused) and when the
// VM reads as not running (that reading cannot rule boxes out).
func applyShares(ctx context.Context, force bool, svc *vmShareSvc, l *boxLayer, w io.Writer) int {
	if !force {
		running, ok := noLiveBoxes(ctx, l, w)
		if !ok {
			return 1
		}
		// As with -recreate: "not running" is also what a non-zero colima status
		// exit reads as while the VM runs, so it does not rule boxes out.
		if !running {
			fmt.Fprintln(w, "refusing: the box VM reads as not running, so running boxes cannot be ruled out. Use -force.")
			return 1
		}
	}
	if _, err := svc.Apply(); err != nil {
		fmt.Fprintf(w, "refusing: %v\n", err)
		return 1
	}
	printed := 0
	for {
		st, _ := svc.lastJob() // unfiltered: ApplyStatus forgets a failure the VM has left behind
		for printed < len(st.Steps) {
			s := st.Steps[printed]
			if s.State == "done" {
				fmt.Fprintf(w, "%s… done (%d s)\n", s.Name, (s.Ms+500)/1000)
			} else if s.State == "failed" && st.State != "running" {
				fmt.Fprintf(w, "%s… failed: %s\n", s.Name, st.Error)
				if st.Raw != "" {
					fmt.Fprintf(w, "detail: %s\n", strings.TrimSpace(st.Raw))
				}
			} else {
				break
			}
			printed++
		}
		if st.State != "running" {
			if st.State == "failed" {
				return 1
			}
			return 0
		}
		select {
		case <-ctx.Done():
			fmt.Fprintf(w, "FAIL box VM folders: interrupted (%v). The box VM may be stopped; run `culibox box share apply -force` to finish.\n", ctx.Err())
			return 1
		case <-time.After(100 * time.Millisecond):
		}
	}
}
