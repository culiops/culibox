package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/culiops/culibox/internal/clone"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/session"
	"github.com/culiops/culibox/internal/vmshares"
	"github.com/culiops/culibox/internal/web"
)

// vmControl is what the folder service needs from the box VM and the sessions.
// The real one wraps the box layer (layerVMControl); the UI demo has a fake.
type vmControl interface {
	// Applied is what the VM's written colima.yaml mounts (the data-root mounts
	// left out), whether that is the whole home folder, and whether the VM is
	// set up at all.
	Applied() (shares []vmshares.Share, wholeHome, setUp bool, err error)
	// SetListed hands the VM the folder list its next apply writes.
	SetListed([]vmshares.Share)
	// Apply stops the VM, rewrites its folders and starts it, then runs the
	// post-start checks. It reports each step as it goes.
	Apply(ctx context.Context, step func(name, state, sub string)) error
	// RunningBoxed names the live boxed sessions; RunningCwds are their working
	// folders (what a folder row counts).
	RunningBoxed() []string
	RunningCwds() []string
	// Applying is true while an Apply is in flight: spawns wait it out.
	Applying() bool
	// VMRunning is true while the VM is up (a failed apply is forgotten once the VM runs with the saved list).
	VMRunning() bool
	// ApplyLockHeld is true while an apply holds the cross-process apply lock
	// (another agent-box: the daemon, or `box share apply` from a terminal).
	ApplyLockHeld() bool
}

// vmShareSvc owns the folder list (vm-shares.json) and the apply job. The
// daemon and a `box share` CLI each run one over the same file, so every view,
// write and apply first re-reads the file if it changed since this service
// last read or wrote it. One mutex covers the list, the file and the job.
type vmShareSvc struct {
	dataDir string
	env     vmshares.Env
	ctl     vmControl
	logf    func(string, ...any)

	mu      sync.Mutex
	list    []vmshares.Share
	corrupt error
	file    fileStamp // vm-shares.json as last read or written; zero = none
	job     *web.VMApply
	jobAt   map[string]int64 // step name -> start, unix ms
	nextID  int

	profiles func() []vmshares.Profile
	recorded func() []string
}

var _ web.VMFolders = (*vmShareSvc)(nil)

// newVMShareSvc loads vm-shares.json, or derives and saves it on the first run
// (the narrowest list that keeps today's profiles working). A damaged file is
// never overwritten: the service reports it and refuses every write and apply.
// memoryDirs may be nil.
func newVMShareSvc(dataDir string, env vmshares.Env, ctl vmControl, profiles func() []vmshares.Profile, recorded func() []string, memoryDirs func() []string, logf func(string, ...any)) (*vmShareSvc, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	s := &vmShareSvc{dataDir: dataDir, env: env, ctl: ctl, logf: logf, profiles: profiles, recorded: recorded}
	list, exists, err := vmshares.Load(dataDir)
	switch {
	case errors.Is(err, vmshares.ErrCorrupt):
		s.corrupt = err
		s.file = statShares(dataDir) // re-read only once it changes
		logf("box VM folders: %v; the list is left alone and cannot be changed until it is fixed", err)
		return s, nil
	case err != nil:
		return nil, err
	case exists:
		out, skipped := vmshares.Normalize(list, env)
		for _, k := range skipped {
			logf("box VM folders: skipping %s: %s", k.Path, k.Reason)
		}
		s.list = out
	default:
		in := vmshares.DeriveInput{Profiles: profiles(), Env: env}
		if recorded != nil {
			in.Recorded = recorded()
		}
		if memoryDirs != nil {
			in.MemoryDirs = memoryDirs()
		}
		out, skipped := vmshares.Derive(in)
		for _, k := range skipped {
			logf("box VM folders: skipping %s: %s", k.Path, k.Reason)
		}
		for _, d := range vmshares.CopyParents(in.Profiles) {
			if err := os.MkdirAll(d, 0o700); err != nil {
				logf("box VM folders: cannot create %s: %v", d, err)
			}
		}
		if err := vmshares.Save(dataDir, out); err != nil {
			return nil, fmt.Errorf("saving the first folder list: %w", err)
		}
		s.list = out
	}
	s.file = statShares(dataDir)
	ctl.SetListed(append([]vmshares.Share{}, s.list...))
	return s, nil
}

// fileStamp identifies one version of vm-shares.json.
type fileStamp struct {
	mtime time.Time
	size  int64
	ino   uint64
	ok    bool
}

func statShares(dataDir string) fileStamp {
	fi, err := os.Lstat(filepath.Join(dataDir, vmshares.FileName))
	if err != nil {
		return fileStamp{}
	}
	st := fileStamp{mtime: fi.ModTime(), size: fi.Size(), ok: true}
	if sys, ok := fi.Sys().(*syscall.Stat_t); ok {
		st.ino = uint64(sys.Ino)
	}
	return st
}

// reload re-reads vm-shares.json when another process changed it (its mtime,
// size or inode differ from what this service last read or wrote). The
// caller holds s.mu. A missing file keeps the list in memory; a damaged one
// makes the service refuse writes, as at boot.
func (s *vmShareSvc) reload() {
	now := statShares(s.dataDir)
	if now == s.file || !now.ok {
		return
	}
	list, _, err := vmshares.Load(s.dataDir)
	switch {
	case errors.Is(err, vmshares.ErrCorrupt):
		s.corrupt = err
		s.logf("box VM folders: %v; the list is left alone and cannot be changed until it is fixed", err)
	case err != nil:
		s.logf("box VM folders: re-reading the folder list: %v", err)
		return
	default:
		out, skipped := vmshares.Normalize(list, s.env)
		for _, k := range skipped {
			s.logf("box VM folders: skipping %s: %s", k.Path, k.Reason)
		}
		s.list, s.corrupt = out, nil
		s.ctl.SetListed(append([]vmshares.Share{}, out...))
	}
	s.file = now
}

// Listed is a copy of the saved list.
func (s *vmShareSvc) Listed() []vmshares.Share {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]vmshares.Share{}, s.list...)
}

func (s *vmShareSvc) running() bool { return s.job != nil && s.job.State == "running" }

// write checks that the list may change, runs change on a copy, and saves and
// hands over the result. nothing-to-write results (a covered add) skip the save.
func (s *vmShareSvc) write(change func(list []vmshares.Share) ([]vmshares.Share, bool, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reload()
	if s.corrupt != nil {
		return web.ErrListCorrupt
	}
	if s.running() {
		return web.ErrApplyRunning
	}
	next, changed, err := change(append([]vmshares.Share{}, s.list...))
	if err != nil || !changed {
		return err
	}
	if err := vmshares.Save(s.dataDir, next); err != nil {
		return err
	}
	s.file = statShares(s.dataDir)
	s.list = next
	s.ctl.SetListed(append([]vmshares.Share{}, next...))
	return nil
}

func parseMode(m string) (vmshares.Mode, error) {
	switch vmshares.Mode(m) {
	case vmshares.RW, vmshares.RO:
		return vmshares.Mode(m), nil
	}
	return "", web.ErrVMBadMode
}

func (s *vmShareSvc) Add(typed, mode string, dryRun bool) (web.VMFoldersView, web.VMAddResult, error) {
	var res web.VMAddResult
	m, err := parseMode(mode)
	if err != nil {
		return web.VMFoldersView{}, res, err
	}
	p, err := vmshares.Expand(typed, s.env.Home)
	if err != nil {
		return web.VMFoldersView{}, res, err
	}
	err = s.write(func(list []vmshares.Share) ([]vmshares.Share, bool, error) {
		out, err := vmshares.Add(list, vmshares.Share{Path: p, Mode: m}, s.env)
		if err != nil {
			return nil, false, err
		}
		res = web.VMAddResult{CoveredBy: out.CoveredBy, Absorbed: out.Absorbed}
		if res.Absorbed == nil {
			res.Absorbed = []string{}
		}
		return out.List, !dryRun && out.CoveredBy == "", nil
	})
	if err != nil {
		return web.VMFoldersView{}, res, err
	}
	v, err := s.View()
	return v, res, err
}

// AddWorkspace makes sure the list gives a boxed workspace read & write. It
// returns the listed folder that already does ("" when it was added), and then
// writes nothing: a re-run of `culibox init` must not rewrite the list, and
// vmshares.Add treats a folder that is already listed as a mode change, not as
// "covered".
func (s *vmShareSvc) AddWorkspace(typed string) (string, error) {
	p, err := vmshares.Expand(typed, s.env.Home)
	if err != nil {
		return "", err
	}
	want := s.key(p)
	for _, e := range s.Listed() {
		if e.Mode == vmshares.RW && s.key(e.Path) == want {
			return e.Path, nil
		}
	}
	_, res, err := s.Add(p, string(vmshares.RW), false)
	return res.CoveredBy, err
}

func (s *vmShareSvc) SetMode(path, mode string) (web.VMFoldersView, error) {
	m, err := parseMode(mode)
	if err != nil {
		return web.VMFoldersView{}, err
	}
	err = s.write(func(list []vmshares.Share) ([]vmshares.Share, bool, error) {
		out, err := vmshares.SetMode(list, path, m, s.env)
		return out, err == nil, err
	})
	if err != nil {
		return web.VMFoldersView{}, err
	}
	return s.View()
}

func (s *vmShareSvc) Remove(path string) (web.VMFoldersView, error) {
	err := s.write(func(list []vmshares.Share) ([]vmshares.Share, bool, error) {
		out, err := vmshares.Remove(list, path, s.env.Fold)
		return out, err == nil, err
	})
	if err != nil {
		return web.VMFoldersView{}, err
	}
	return s.View()
}

func (s *vmShareSvc) key(p string) string {
	p = vmshares.Resolve(p)
	if s.env.Fold {
		return strings.ToLower(p)
	}
	return p
}

// View is the page: the saved list against what the VM has applied.
func (s *vmShareSvc) View() (web.VMFoldersView, error) {
	s.mu.Lock()
	s.reload()
	list := append([]vmshares.Share{}, s.list...)
	corrupt := s.corrupt
	job := s.jobCopy()
	s.mu.Unlock()
	if job != nil && job.State == "failed" && s.failureSuperseded(job, list) {
		job = nil
	}

	applied, whole, setUp, err := s.ctl.Applied()
	if err != nil {
		return web.VMFoldersView{}, err
	}
	var profiles []vmshares.Profile
	if s.profiles != nil {
		profiles = s.profiles()
	}
	var recorded []string
	if s.recorded != nil {
		recorded = s.recorded()
	}
	v := web.VMFoldersView{Rows: []web.VMFolderRow{}, DataRoot: s.env.DataRoot, RunningBoxed: s.ctl.RunningBoxed(), Apply: job}
	if v.RunningBoxed == nil {
		v.RunningBoxed = []string{}
	}
	if corrupt != nil {
		v.ListError = corrupt.Error()
	}
	cwds := s.ctl.RunningCwds()
	row := func(r vmshares.Row, pending string) web.VMFolderRow {
		n := 0
		for _, c := range cwds {
			if vmshares.Within(c, r.Path, s.env.Fold) {
				n++
			}
		}
		used := r.UsedBy
		if used == nil {
			used = []string{}
		}
		return web.VMFolderRow{Path: r.Path, Display: r.Display, Mode: string(r.Mode), Origin: r.Origin, UsedBy: used, Running: n, Pending: pending}
	}
	describe := func(l []vmshares.Share) []vmshares.Row {
		return vmshares.Describe(l, profiles, recorded, s.env.Home, s.env.Fold)
	}
	switch {
	case corrupt != nil || !setUp || whole:
		// Rows are unmarked: the saved list cannot be trusted (corrupt), or there
		// is nothing applied to compare it with. The VM's own state still shows.
		src := list
		if corrupt != nil {
			src = applied
		}
		for _, r := range describe(src) {
			v.Rows = append(v.Rows, row(r, ""))
		}
		switch {
		case !setUp:
			v.State = "not_set_up"
		case whole:
			v.State = "whole_home"
		default:
			v.State = "applied"
		}
	default:
		have := map[string]vmshares.Mode{}
		for _, a := range applied {
			have[s.key(a.Path)] = a.Mode
		}
		want := map[string]bool{}
		for _, r := range describe(list) {
			k := s.key(r.Path)
			want[k] = true
			pending := ""
			switch m, ok := have[k]; {
			case !ok:
				pending = "added"
				v.Added++
			case m != r.Mode:
				pending = "switched"
				v.Switched++
			}
			v.Rows = append(v.Rows, row(r, pending))
		}
		var gone []vmshares.Share
		for _, a := range applied {
			if !want[s.key(a.Path)] {
				gone = append(gone, a)
			}
		}
		for _, r := range describe(gone) {
			v.Rows = append(v.Rows, row(r, "removed"))
			v.Removed++
		}
		keys := make(map[string]string, len(v.Rows))
		for _, r := range v.Rows {
			keys[r.Path] = s.key(r.Path)
		}
		sort.SliceStable(v.Rows, func(i, j int) bool { return keys[v.Rows[i].Path] < keys[v.Rows[j].Path] })
		v.State = "applied"
		if v.Added+v.Removed+v.Switched > 0 {
			v.State = "pending"
		}
	}
	return v, nil
}

// vmProfiles maps the merged profiles to what shares care about: boxed ones only.
// memRoot is the config bundle source ("" with the bundle off).
func vmProfiles(cfg config.Config, all []config.AgentConfig) []vmshares.Profile {
	out := []vmshares.Profile{}
	for _, a := range all {
		if cfg.RuntimeFor(a) != config.RuntimeBox {
			continue
		}
		out = append(out, vmshares.Profile{Name: a.Name, Workspace: a.DefaultCwd, CloneOf: a.CloneOf, Clone: a.CloneOf != "", ContextPaths: a.ContextPaths})
	}
	return out
}

// vmMemoryDirs are the project-memory folders of the clone profiles' originals.
func vmMemoryDirs(memRoot string, profiles []vmshares.Profile) []string {
	out := []string{}
	if !filepath.IsAbs(memRoot) {
		return out
	}
	for _, p := range profiles {
		if p.Clone {
			out = append(out, clone.MemoryDir(memRoot, p.CloneOf))
		}
	}
	return out
}

// bootVMFolders builds the folder service on macOS and logs the whole-home
// warning; nil (never a typed nil) without a VM. A failure to build is logged and
// leaves the VM with the list bootShares read.
func bootVMFolders(layer *boxLayer, cfg config.Config, all func() []config.AgentConfig, recorded func() []string, memRoot string, sessions func() []session.Info) web.VMFolders {
	if layer == nil || layer.vm == nil {
		return nil
	}
	vc := layer.vm.Config()
	ctl := newLayerVMControl(layer, sessions)
	profiles := func() []vmshares.Profile { return vmProfiles(cfg, all()) }
	svc, err := newVMShareSvc(layer.dataDir, vmshares.Env{Home: vc.Home, DataRoot: vc.Root, Fold: vc.Fold, Protected: layer.protected}, ctl, profiles, recorded,
		func() []string { return vmMemoryDirs(memRoot, profiles()) }, log.Printf)
	if err != nil {
		log.Printf("box VM folders: %v; the folder list cannot be managed from here", err)
		// Every share refusal points to Settings › VM folders: keep the page,
		// saying why, and refuse every change with the same error.
		return brokenVMFolders{err: err, dataRoot: vc.Root}
	}
	if _, whole, setUp, err := ctl.Applied(); err == nil && setUp && whole {
		log.Print(vmshares.WholeHomeLine(len(svc.Listed())))
	}
	ctl.jobRunning = svc.jobRunning
	return svc
}

// brokenVMFolders is the VM folders page when the folder service could not be
// built at boot: it shows the boot error and refuses every change and apply
// with it.
type brokenVMFolders struct {
	err      error
	dataRoot string
}

var _ web.VMFolders = brokenVMFolders{}

func (b brokenVMFolders) View() (web.VMFoldersView, error) {
	return web.VMFoldersView{State: "not_set_up", ListError: b.err.Error(), Rows: []web.VMFolderRow{},
		RunningBoxed: []string{}, DataRoot: b.dataRoot}, nil
}

func (b brokenVMFolders) Add(string, string, bool) (web.VMFoldersView, web.VMAddResult, error) {
	return web.VMFoldersView{}, web.VMAddResult{}, b.err
}

func (b brokenVMFolders) SetMode(string, string) (web.VMFoldersView, error) {
	return web.VMFoldersView{}, b.err
}

func (b brokenVMFolders) Remove(string) (web.VMFoldersView, error) { return web.VMFoldersView{}, b.err }
func (b brokenVMFolders) Apply() (web.VMApply, error)              { return web.VMApply{}, b.err }
func (b brokenVMFolders) ApplyStatus() (web.VMApply, bool)         { return web.VMApply{}, false }
