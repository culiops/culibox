package main

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/culiops/culibox/internal/boxvm"
	"github.com/culiops/culibox/internal/session"
	"github.com/culiops/culibox/internal/vmshares"
	"github.com/culiops/culibox/internal/web"
)

// Deadlines of the apply job; vars so a test can shorten them.
var (
	applyJobTimeout   = 5 * time.Minute
	applyCheckTimeout = 60 * time.Second
)

const (
	subStart = "Usually about 16 s"
	subCheck = "The firewall and folder checks that run after every start"
)

// endedLine is the stop step's text: which sessions the stop ends.
func endedLine(names []string) string {
	switch len(names) {
	case 0:
		return "No box sessions were running"
	case 1:
		return names[0] + " ended"
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1] + " ended"
}

// jobCopy is the last job, deep-copied. The caller holds s.mu.
func (s *vmShareSvc) jobCopy() *web.VMApply {
	if s.job == nil {
		return nil
	}
	j := *s.job
	j.Steps = append([]web.VMApplyStep{}, s.job.Steps...)
	// The server's own clock, so a browser with a skewed clock still shows the right time.
	if j.State == "running" {
		j.ElapsedMs = time.Now().UnixMilli() - j.StartedMs
	}
	return &j
}

// jobRunning is true while an apply job runs.
func (s *vmShareSvc) jobRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running()
}

// lastJob is the last job as it is, for the CLI: it must see a failure however the VM looks now.
func (s *vmShareSvc) lastJob() (web.VMApply, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job == nil {
		return web.VMApply{}, false
	}
	return *s.jobCopy(), true
}

// ApplyStatus is the last job, if any, for the web (meta, GET apply).
// A failed job that the VM has since left behind (a later `agent-box box share apply` from a terminal worked) is not reported.
func (s *vmShareSvc) ApplyStatus() (web.VMApply, bool) {
	s.mu.Lock()
	j := s.jobCopy()
	list := append([]vmshares.Share{}, s.list...)
	s.mu.Unlock()
	if j == nil {
		return web.VMApply{}, false
	}
	if j.State == "failed" && s.failureSuperseded(j, list) {
		return web.VMApply{}, false
	}
	return *j, true
}

// failureSuperseded: the VM runs with exactly the saved folder list, so the failed job no longer describes it. A start failure
// leaves the written colima.yaml equal to the list too, so the VM must also be running; a failed containment check leaves the VM
// running with the list, so that failure is kept until the next apply (nothing here can tell a fixed check from a broken one).
func (s *vmShareSvc) failureSuperseded(j *web.VMApply, list []vmshares.Share) bool {
	for _, st := range j.Steps {
		if st.State == "failed" && st.Name == "check" {
			return false
		}
	}
	applied, whole, setUp, err := ctlApplied(s)
	if err != nil || !setUp || whole || s.corruptNow() || len(applied) != len(list) {
		return false
	}
	have := map[string]vmshares.Mode{}
	for _, a := range applied {
		have[s.key(a.Path)] = a.Mode
	}
	for _, l := range list {
		if m, ok := have[s.key(l.Path)]; !ok || m != l.Mode {
			return false
		}
	}
	return s.ctl.VMRunning()
}

func ctlApplied(s *vmShareSvc) ([]vmshares.Share, bool, bool, error) { return s.ctl.Applied() }

func (s *vmShareSvc) corruptNow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.corrupt != nil
}

// errApplyElsewhere: another agent-box holds the apply lock. It is the web's
// ErrApplyRunning (code apply_running) with the lock's own words.
type errApplyElsewhere struct{}

func (errApplyElsewhere) Error() string { return boxvm.ErrApplyLocked.Error() }
func (errApplyElsewhere) Is(target error) bool {
	return target == web.ErrApplyRunning || target == boxvm.ErrApplyLocked
}

// Apply starts the background job that restarts the VM with the saved list. It
// returns the job as it starts; the page polls ApplyStatus.
func (s *vmShareSvc) Apply() (web.VMApply, error) {
	s.mu.Lock()
	s.reload()
	if s.corrupt != nil {
		s.mu.Unlock()
		return web.VMApply{}, web.ErrListCorrupt
	}
	if s.running() {
		s.mu.Unlock()
		return web.VMApply{}, web.ErrApplyRunning
	}
	if s.ctl.ApplyLockHeld() {
		s.mu.Unlock()
		return web.VMApply{}, errApplyElsewhere{}
	}
	resolved, err := s.recheckList()
	if err != nil {
		s.mu.Unlock()
		return web.VMApply{}, err
	}
	if _, _, setUp, err := s.ctl.Applied(); err == nil && !setUp {
		s.mu.Unlock()
		return web.VMApply{}, web.ErrVMNotSetUp
	}
	if rs, ok := s.ctl.(resolvedLister); ok {
		rs.SetListedResolved(resolved) // the paths the re-check resolved, not resolved again
	} else {
		s.ctl.SetListed(append([]vmshares.Share{}, s.list...))
	}
	s.nextID++
	now := time.Now()
	s.job = &web.VMApply{ID: s.nextID, State: "running", StartedMs: now.UnixMilli(), Steps: []web.VMApplyStep{
		{Name: "stop", State: "running", Sub: endedLine(s.ctl.RunningBoxed())},
		{Name: "start", State: "waiting", Sub: subStart},
		{Name: "check", State: "waiting", Sub: subCheck},
	}}
	s.jobAt = map[string]int64{"stop": now.UnixMilli()}
	id := s.job.ID
	out := *s.jobCopy()
	s.mu.Unlock()
	go s.runJob(context.WithoutCancel(context.Background()), id)
	return out, nil
}

// recheckList runs the list through the same rules as at load, on the folders
// as they are now (resolved), just before it is handed to the VM. A folder
// that changed on disk since it was added (a link retargeted) and is no longer
// allowed, or no longer a separate entry, refuses the apply. The caller holds s.mu.
//
// It returns the list with each path replaced by the resolved path it was
// checked on, for resolvedLister.
func (s *vmShareSvc) recheckList() ([]vmshares.Share, error) {
	out, keys, skipped := vmshares.NormalizeResolved(s.list, s.env)
	if len(skipped) == 0 && slices.Equal(out, s.list) {
		resolved := make([]vmshares.Share, len(out))
		for i, sh := range out {
			resolved[i] = vmshares.Share{Path: keys[i], Mode: sh.Mode}
		}
		return resolved, nil
	}
	for _, k := range skipped {
		s.logf("box VM folders: not applying: %s: %s", k.Path, k.Reason)
	}
	s.logf("box VM folders: not applying: the folder list no longer passes the checks")
	return nil, web.ErrSharesChanged
}

// resolvedLister is a vmControl that can take paths already resolved and
// checked (the real one); the others get SetListed.
type resolvedLister interface{ SetListedResolved([]vmshares.Share) }

func (s *vmShareSvc) runJob(ctx context.Context, id int) {
	err := s.ctl.Apply(ctx, func(name, state, sub string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.job == nil || s.job.ID != id {
			return
		}
		for i := range s.job.Steps {
			st := &s.job.Steps[i]
			if st.Name != name {
				continue
			}
			now := time.Now().UnixMilli()
			if state == "running" {
				s.jobAt[name] = now
			}
			if (state == "done" || state == "failed") && s.jobAt[name] != 0 {
				st.Ms = now - s.jobAt[name]
			}
			st.State = state
			if sub != "" {
				st.Sub = sub
			}
		}
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job == nil || s.job.ID != id {
		return
	}
	now := time.Now().UnixMilli()
	if err == nil {
		for i := range s.job.Steps {
			if s.job.Steps[i].State != "done" {
				s.job.Steps[i].State = "done"
			}
		}
		s.job.State = "done"
		s.logf("box VM folders: applied the folder list")
		return
	}
	failed := ""
	for i := range s.job.Steps {
		st := &s.job.Steps[i]
		switch {
		case failed != "":
			st.State = "not_run"
		case st.State != "done":
			failed = st.Name
			if st.State == "running" && s.jobAt[st.Name] != 0 {
				st.Ms = now - s.jobAt[st.Name]
			}
			st.State = "failed"
		}
	}
	s.job.State = "failed"
	s.job.Error = applyErrorText(failed, err)
	s.job.Detail = web.VMApplyDetail
	s.job.Raw = err.Error() // the logf below has it too; the CLI prints it, the browser never gets it
	s.logf("box VM folders: apply failed at %s: %v", failed, err)
}

func applyErrorText(step string, err error) string {
	switch {
	case errors.Is(err, boxvm.ErrApplyLocked):
		return boxvm.ErrApplyLocked.Error()
	case errors.Is(err, boxvm.ErrMountsOverlap):
		return boxvm.ErrMountsOverlap.Error()
	case errors.Is(err, boxvm.ErrStartTimeout):
		return boxvm.ErrStartTimeout.Error()
	case errors.Is(err, boxvm.ErrNeedsRecreate):
		return boxvm.ErrNeedsRecreate.Error()
	}
	switch step {
	case "stop":
		return "The VM did not stop."
	case "start":
		return "The VM did not start."
	}
	return "A containment check failed."
}

// layerVMControl is the real vmControl: the box layer's VM and the session manager.
type layerVMControl struct {
	l        *boxLayer
	sessions func() []session.Info
	applying atomic.Bool
	// jobRunning is the folder service's job state: it is true from the moment
	// Apply() accepts the job, before its goroutine runs.
	jobRunning func() bool
	// now is the cache clock (time.Now; a test sets it).
	now func() time.Time
	// runMu guards the VMRunning cache and is held across the colima call, so
	// concurrent askers share one fork.
	runMu  sync.Mutex
	runAt  time.Time
	runVal bool
	runSet bool
}

func newLayerVMControl(l *boxLayer, sessions func() []session.Info) *layerVMControl {
	c := &layerVMControl{l: l, sessions: sessions}
	l.ctl = c
	return c
}

func (c *layerVMControl) Applied() ([]vmshares.Share, bool, bool, error) {
	vm := c.l.vm
	if !vm.SetUp() {
		return nil, false, false, nil
	}
	ms, err := vm.AppliedMounts()
	if err != nil {
		return nil, false, true, err
	}
	data := map[string]bool{}
	for _, d := range boxvm.DataMounts(vm.Config().Root) {
		data[d.Location] = true
	}
	out := []vmshares.Share{}
	for _, m := range ms {
		if data[m.Location] {
			continue
		}
		mode := vmshares.RO
		if m.Writable {
			mode = vmshares.RW
		}
		out = append(out, vmshares.Share{Path: m.Location, Mode: mode})
	}
	whole, err := vm.WholeHome()
	return out, whole, true, err
}

// vmRunningTTL and vmRunningTimeout bound VMRunning: it runs on every meta
// request while a failed job is held, and /api/meta answers the 4 s reconnect
// probe, so one colima fork serves every caller for 5 s and gives up at 3 s.
const vmRunningTTL = 5 * time.Second

var vmRunningTimeout = 3 * time.Second

// VMRunning asks colima, cached and bounded (vmRunningTTL, vmRunningTimeout).
func (c *layerVMControl) VMRunning() bool {
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	c.runMu.Lock()
	defer c.runMu.Unlock()
	if c.runSet && now().Sub(c.runAt) < vmRunningTTL {
		return c.runVal
	}
	ctx, cancel := context.WithTimeout(context.Background(), vmRunningTimeout)
	defer cancel()
	c.runVal, c.runAt, c.runSet = c.l.vm.Running(ctx), now(), true
	return c.runVal
}

func (c *layerVMControl) SetListedResolved(l []vmshares.Share) { c.l.vm.SetResolvedShares(l) }

func (c *layerVMControl) SetListed(l []vmshares.Share) { c.l.vm.SetShares(l) }

func (c *layerVMControl) ApplyLockHeld() bool { return c.l.vm.ApplyLockHeld() }

func (c *layerVMControl) live() []session.Info {
	var out []session.Info
	for _, i := range c.sessions() {
		if i.Runtime == "box" && !i.Exited {
			out = append(out, i)
		}
	}
	return out
}

func (c *layerVMControl) RunningBoxed() []string {
	out := []string{}
	for _, i := range c.live() {
		out = append(out, i.Name)
	}
	return out
}

func (c *layerVMControl) RunningCwds() []string {
	out := []string{}
	for _, i := range c.live() {
		if i.Cwd != "" {
			out = append(out, i.Cwd)
		}
	}
	return out
}

func (c *layerVMControl) Applying() bool {
	return c.applying.Load() || (c.jobRunning != nil && c.jobRunning())
}

func (c *layerVMControl) Apply(ctx context.Context, step func(name, state, sub string)) error {
	c.applying.Store(true)
	defer c.applying.Store(false)
	c.l.foldersOK.Store(nil) // the restart needs a new check
	// A wedged colima must not leave the flag set (and every spawn refused)
	// forever. The start has its own bound inside ApplyMounts.
	ctx, cancel := context.WithTimeout(ctx, applyJobTimeout)
	defer cancel()
	names := map[boxvm.ApplyStep]string{boxvm.StepStop: "stop", boxvm.StepStart: "start"}
	err := c.l.vm.ApplyMounts(ctx, boxvm.ApplyHooks{Step: func(s boxvm.ApplyStep, e boxvm.StepEvent) {
		switch {
		case !e.Done:
			step(names[s], "running", "")
		case e.Err != nil:
			step(names[s], "failed", "")
		default:
			step(names[s], "done", "")
		}
	}})
	if err != nil {
		return err
	}
	step("check", "running", "")
	if err := c.l.checkAfterApply(ctx); err != nil {
		step("check", "failed", "")
		return err
	}
	step("check", "done", "")
	return nil
}

// checkAfterApply is the post-start check: the live firewall rules doctor reads,
// the VM's real mounts against its colima.yaml, then the pass that reconciles
// boxes and reclaims state in the fresh VM.
func (l *boxLayer) checkAfterApply(ctx context.Context) error {
	vc := l.vm.Config()
	rctx, cancel := context.WithTimeout(ctx, applyCheckTimeout)
	rules, err := l.vm.FilterRules(rctx)
	cancel()
	if err == nil {
		if err = boxvm.CheckInputRules(rules, vc.EgressPort, vc.BrokerPort); err == nil {
			err = boxvm.CheckDockerUserRules(rules)
		}
	}
	if err == nil {
		mctx, cancel := context.WithTimeout(ctx, applyCheckTimeout)
		err = l.verifyMounts(mctx, true)
		cancel()
	}
	if err != nil {
		return err
	}
	// reconcileAndReclaim bounds itself (vmStartPassTimeout).
	l.reconcileAndReclaim(ctx)
	return nil
}

// verifyMounts compares the running VM's mounts with its colima.yaml and
// remembers a pass against that colima.yaml, so spawns keep checking until one
// passes and again after the next apply. A missing colima.yaml is a failure
// too: a set-up VM started without it runs on colima's defaults, which share
// the home folder. justStarted is true only where this process started the VM
// (and so may fix a folder's mode in it); every other check is read-only.
func (l *boxLayer) verifyMounts(ctx context.Context, justStarted bool) error {
	// Stat first: a colima.yaml rewritten during the check then reads as changed.
	fi, statErr := os.Stat(l.vm.Config().YAMLPath())
	verify := l.vm.VerifyMounts
	if justStarted {
		// A VM we just started may still hold a folder at its old mode (#236).
		verify = l.vm.ReconcileMounts
	}
	err := verify(ctx)
	if err != nil || statErr != nil {
		l.foldersOK.Store(nil)
		return err
	}
	l.foldersOK.Store(&yamlStamp{fi.ModTime(), fi.Size()})
	return nil
}

// foldersVerified reports whether a folder check passed against the VM's
// current colima.yaml.
func (l *boxLayer) foldersVerified() bool {
	ok := l.foldersOK.Load()
	if ok == nil {
		return false
	}
	fi, err := os.Stat(l.vm.Config().YAMLPath())
	return err == nil && fi.ModTime().Equal(ok.mtime) && fi.Size() == ok.size
}
