package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	boximage "github.com/culiops/culibox/docker/agent-box"
	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/boxvm"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/vmshares"
	"github.com/culiops/culibox/internal/web"
)

type fakeVMControl struct {
	mu        sync.Mutex
	applied   []vmshares.Share
	whole     bool
	notSetUp  bool
	listed    []vmshares.Share
	names     []string
	cwds      []string
	applying  bool
	failStart error
	running   bool
	hold      chan struct{} // Apply blocks on it when set
	lockHeld  bool          // another process holds the apply lock
	steps     chan string
}

func (f *fakeVMControl) Applied() ([]vmshares.Share, bool, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]vmshares.Share{}, f.applied...), f.whole, !f.notSetUp, nil
}
func (f *fakeVMControl) SetListed(l []vmshares.Share) {
	f.mu.Lock()
	f.listed = l
	f.mu.Unlock()
}
func (f *fakeVMControl) RunningBoxed() []string { return f.names }
func (f *fakeVMControl) RunningCwds() []string  { return f.cwds }
func (f *fakeVMControl) Applying() bool         { return f.applying }
func (f *fakeVMControl) VMRunning() bool        { return f.running }
func (f *fakeVMControl) ApplyLockHeld() bool    { return f.lockHeld }
func (f *fakeVMControl) Apply(ctx context.Context, step func(name, state, sub string)) error {
	step("stop", "running", "")
	if f.hold != nil {
		<-f.hold
	}
	step("stop", "done", "")
	step("start", "running", "")
	if f.failStart != nil {
		step("start", "failed", "")
		return f.failStart
	}
	step("start", "done", "")
	step("check", "running", "")
	step("check", "done", "")
	return nil
}

type svcFixture struct {
	dir, home string
	env       vmshares.Env
	ctl       *fakeVMControl
	profiles  []vmshares.Profile
}

func newSvcFixture(t *testing.T) *svcFixture {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	data := filepath.Join(root, "data")
	for _, d := range []string{home, data} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	home, data = vmshares.Resolve(home), vmshares.Resolve(data)
	return &svcFixture{dir: data, home: home, env: vmshares.Env{Home: home, DataRoot: filepath.Join(data, "root"), Fold: true}, ctl: &fakeVMControl{}}
}

func (f *svcFixture) svc(t *testing.T) *vmShareSvc {
	t.Helper()
	s, err := newVMShareSvc(f.dir, f.env, f.ctl, func() []vmshares.Profile { return f.profiles }, func() []string { return nil }, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *svcFixture) sub(t *testing.T, n string) string {
	t.Helper()
	p := filepath.Join(f.home, n)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewSvcDerivesOnceAndSaves(t *testing.T) {
	f := newSvcFixture(t)
	a := f.sub(t, "a")
	f.profiles = []vmshares.Profile{{Name: "p", Workspace: a}}
	s := f.svc(t)
	if got := s.Listed(); len(got) != 1 || got[0].Path != a || got[0].Mode != vmshares.RW {
		t.Fatalf("derived %+v", got)
	}
	if len(f.ctl.listed) != 1 {
		t.Fatalf("the VM was not given the list: %+v", f.ctl.listed)
	}
	f.profiles = []vmshares.Profile{{Name: "q", Workspace: f.sub(t, "b")}}
	s2 := f.svc(t)
	if got := s2.Listed(); len(got) != 1 || got[0].Path != a {
		t.Fatalf("second construction re-derived: %+v", got)
	}
}

func TestViewStates(t *testing.T) {
	f := newSvcFixture(t)
	a, b := f.sub(t, "a"), f.sub(t, "b")
	f.profiles = []vmshares.Profile{{Name: "p", Workspace: a}}
	s := f.svc(t)
	f.ctl.applied = []vmshares.Share{{Path: a, Mode: vmshares.RW}}
	v, err := s.View()
	if err != nil || v.State != "applied" || len(v.Rows) != 1 || v.Rows[0].Pending != "" {
		t.Fatalf("applied: %+v %v", v, err)
	}
	// pending: b added, a removed
	if _, err := s.Remove(a); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Add(b, "ro", false); err != nil {
		t.Fatal(err)
	}
	v, _ = s.View()
	if v.State != "pending" || v.Added != 1 || v.Removed != 1 || len(v.Rows) != 2 {
		t.Fatalf("pending: %+v", v)
	}
	pend := map[string]string{}
	for _, r := range v.Rows {
		pend[r.Path] = r.Pending
	}
	if pend[a] != "removed" || pend[b] != "added" {
		t.Fatalf("markers %v", pend)
	}
	v, _ = s.SetMode(b, "rw")
	if v.Added != 1 {
		t.Fatalf("%+v", v)
	}
	// whole home: unmarked proposed list
	f.ctl.whole = true
	f.ctl.applied = []vmshares.Share{{Path: f.home, Mode: vmshares.RW}}
	v, _ = s.View()
	if v.State != "whole_home" || v.Added+v.Removed+v.Switched != 0 || len(v.Rows) != 1 || v.Rows[0].Pending != "" {
		t.Fatalf("whole_home: %+v", v)
	}
	f.ctl.notSetUp = true
	v, _ = s.View()
	if v.State != "not_set_up" || v.Rows == nil || v.RunningBoxed == nil {
		t.Fatalf("not_set_up: %+v", v)
	}
}

func TestViewSwitchedAndRunningCounts(t *testing.T) {
	f := newSvcFixture(t)
	a := f.sub(t, "a")
	f.profiles = []vmshares.Profile{{Name: "p", Workspace: a}}
	s := f.svc(t)
	f.ctl.applied = []vmshares.Share{{Path: strings.ToUpper(a), Mode: vmshares.RO}}
	f.ctl.cwds = []string{filepath.Join(a, "x"), "/elsewhere"}
	f.ctl.names = []string{"s1"}
	v, _ := s.View()
	if v.Switched != 1 || v.Rows[0].Pending != "switched" || v.Rows[0].Running != 1 || len(v.RunningBoxed) != 1 {
		t.Fatalf("%+v", v)
	}
}

func TestAddDryRunCoveredNoWrite(t *testing.T) {
	f := newSvcFixture(t)
	a := f.sub(t, "a")
	f.profiles = []vmshares.Profile{{Name: "p", Workspace: a}}
	s := f.svc(t)
	file := filepath.Join(f.dir, vmshares.FileName)
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	_, res, err := s.Add(filepath.Join(a, "sub"), "ro", false)
	if err != nil || res.CoveredBy != a {
		t.Fatalf("covered: %+v %v", res, err)
	}
	_, res, err = s.Add(f.sub(t, "c"), "rw", true)
	if err != nil || len(s.Listed()) != 1 {
		t.Fatalf("dry run changed the list: %v %v", s.Listed(), err)
	}
	if res.Absorbed == nil {
		t.Fatal("Absorbed must be non-nil")
	}
	fi, _ := os.Stat(file)
	if !fi.ModTime().Equal(old) {
		t.Fatalf("the file was written: %v", fi.ModTime())
	}
}

func TestWritesRefusedWhileListCorrupt(t *testing.T) {
	f := newSvcFixture(t)
	file := filepath.Join(f.dir, vmshares.FileName)
	if err := os.WriteFile(file, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := f.svc(t)
	if _, _, err := s.Add(f.sub(t, "a"), "rw", false); !errors.Is(err, web.ErrListCorrupt) {
		t.Fatalf("add: %v", err)
	}
	if _, err := s.Remove("/x"); !errors.Is(err, web.ErrListCorrupt) {
		t.Fatalf("remove: %v", err)
	}
	if _, err := s.Apply(); !errors.Is(err, web.ErrListCorrupt) {
		t.Fatalf("apply: %v", err)
	}
	if b, _ := os.ReadFile(file); string(b) != "{nope" {
		t.Fatalf("file overwritten: %q", b)
	}
	if v, err := s.View(); err != nil || v.ListError == "" {
		t.Fatalf("view: %+v %v", v, err)
	}
}

func waitJob(t *testing.T, s *vmShareSvc) web.VMApply {
	t.Helper()
	for i := 0; i < 400; i++ {
		if j, ok := s.ApplyStatus(); ok && j.State != "running" {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return web.VMApply{}
}

func TestApplyRefusesSecondRun(t *testing.T) {
	f := newSvcFixture(t)
	f.ctl.hold = make(chan struct{})
	f.ctl.names = []string{"a", "b"}
	s := f.svc(t)
	j, err := s.Apply()
	if err != nil || j.State != "running" || j.Steps[0].Sub != "a and b ended" {
		t.Fatalf("%+v %v", j, err)
	}
	if _, err := s.Apply(); !errors.Is(err, web.ErrApplyRunning) {
		t.Fatalf("second: %v", err)
	}
	if _, _, err := s.Add(f.sub(t, "z"), "rw", false); !errors.Is(err, web.ErrApplyRunning) {
		t.Fatalf("write during job: %v", err)
	}
	close(f.ctl.hold)
	if j := waitJob(t, s); j.State != "done" {
		t.Fatalf("%+v", j)
	}
	f.ctl.notSetUp = true
	if _, err := s.Apply(); !errors.Is(err, web.ErrVMNotSetUp) {
		t.Fatalf("not set up: %v", err)
	}
}

func TestApplyJobStepsAndFailure(t *testing.T) {
	f := newSvcFixture(t)
	f.ctl.failStart = boxvm.ErrStartTimeout
	s := f.svc(t)
	if _, err := s.Apply(); err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, s)
	var got []string
	for _, st := range j.Steps {
		got = append(got, st.State)
	}
	if strings.Join(got, ",") != "done,failed,not_run" || j.Error != "The VM did not start within 2 minutes." || j.Detail == "" || j.State != "failed" {
		t.Fatalf("%+v", j)
	}
	if j.Steps[0].Sub != "No box sessions were running" {
		t.Fatalf("sub %q", j.Steps[0].Sub)
	}
	f.ctl.failStart = errors.New("boom")
	if _, err := s.Apply(); err != nil {
		t.Fatal(err)
	}
	if j := waitJob(t, s); j.ID != 2 || j.Error != "The VM did not start." || j.Detail != "boom" {
		t.Fatalf("%+v", j)
	}
}

func TestPrepareRefusedWhileApplying(t *testing.T) {
	l, fc, _ := darwinLayer(t, false)
	l.ctl = &fakeVMControl{applying: true}
	err := l.prepare(context.Background())
	if !errors.Is(err, box.ErrVMRestarting) || !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("%v", err)
	}
	if fc.called("start") {
		t.Fatal("started the VM during an apply")
	}
}

func TestPrepareVerifiesMountsAfterStart(t *testing.T) {
	l, fc, _ := darwinLayer(t, false)
	fc.mounts = "" // /proc/self/mounts lists nothing: every mount is missing
	err := l.prepare(context.Background())
	if !errors.Is(err, box.ErrBoxUnavailable) || !strings.Contains(err.Error(), "folders do not match its folder list") || !strings.Contains(err.Error(), "agent-box doctor") {
		t.Fatalf("%v", err)
	}
	if l.foldersVerified() {
		t.Fatal("a failed check must be remembered")
	}
	// The VM is running now: the next spawn re-checks because the last one failed.
	if err := l.prepare(context.Background()); err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatalf("second: %v", err)
	}
}

// S3: a check that failed in another process (a CLI apply) or before a
// restart lives in no memory here, so a new layer starts unverified: the first
// spawn on a VM that was already running checks its folders too.
func TestPrepareVerifiesARunningVMOnFirstUse(t *testing.T) {
	l, fc, d := darwinLayer(t, false)
	d.set(t, "image", "arm64|"+boximage.Hash()+"\n")
	fc.running = true // started by someone else: this layer never sees started=true
	fc.mounts = ""    // every mount is missing
	err := l.prepare(context.Background())
	if !errors.Is(err, box.ErrBoxUnavailable) || !strings.Contains(err.Error(), "folders do not match its folder list") {
		t.Fatalf("first prepare on a running VM with wrong folders: %v", err)
	}
}

func mountReads(fc *fakeColima) int {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	n := 0
	for _, c := range fc.calls {
		if strings.Contains(c, "/proc/self/mounts") {
			n++
		}
	}
	return n
}

// A pass holds until the VM's colima.yaml changes (an apply, from any process).
func TestPrepareVerifiesOnceUntilTheFolderListIsApplied(t *testing.T) {
	l, fc, d := darwinLayer(t, false)
	d.set(t, "image", "arm64|"+boximage.Hash()+"\n")
	fc.running = true
	for i := 0; i < 2; i++ {
		if err := l.prepare(context.Background()); err != nil {
			t.Fatalf("prepare %d: %v", i, err)
		}
	}
	if n := mountReads(fc); n != 1 {
		t.Fatalf("folder checks = %d, want 1 (verified once)", n)
	}
	writeColimaYAML(t, l, l.vm.Config().Mounts()) // another process applied the list
	if err := l.prepare(context.Background()); err != nil {
		t.Fatalf("prepare after apply: %v", err)
	}
	if n := mountReads(fc); n != 2 {
		t.Fatalf("folder checks = %d, want 2 (an apply clears the verdict)", n)
	}
}

// A set-up VM (stamp present) with no colima.yaml would start on colima's
// defaults, which include $HOME: that is a failed check, never a pass.
func TestPrepareMissingYAMLAfterStartIsRefused(t *testing.T) {
	l, _, _ := darwinLayer(t, false)
	if err := os.Remove(l.vm.Config().YAMLPath()); err != nil {
		t.Fatal(err)
	}
	err := l.prepare(context.Background())
	if !errors.Is(err, box.ErrBoxUnavailable) || !strings.Contains(err.Error(), "folders do not match its folder list") {
		t.Fatalf("%v", err)
	}
}

func TestWholeHomeLine(t *testing.T) {
	want := "the box VM shares your whole home folder; apply the folder list in Settings › VM folders (or `agent-box box share apply`) so it sees only 3 folders"
	if got := vmshares.WholeHomeLine(3); got != want {
		t.Fatalf("%q", got)
	}
}

func TestVMProfilesMapsBoxedOnly(t *testing.T) {
	cfg := config.Config{DefaultRuntime: config.RuntimeBox}
	all := []config.AgentConfig{
		{Name: "b", DefaultCwd: "/w"},
		{Name: "h", Runtime: config.RuntimeHost, DefaultCwd: "/h"},
		{Name: "c", DefaultCwd: "/copies/c", CloneOf: "/orig", ContextPaths: []string{"/ctx"}},
	}
	got := vmProfiles(cfg, all)
	if len(got) != 2 || got[0].Clone || !got[1].Clone || got[1].CloneOf != "/orig" || got[1].Workspace != "/copies/c" {
		t.Fatalf("%+v", got)
	}
	if d := vmMemoryDirs("/mem", got); len(d) != 1 || !strings.HasPrefix(d[0], "/mem/projects/") {
		t.Fatalf("%v", d)
	}
	if d := vmMemoryDirs("", got); len(d) != 0 {
		t.Fatalf("%v", d)
	}
}

// A failed start is held while the VM is down, forgotten once it runs with the saved list (a terminal apply worked); a failed
// containment check is held either way.
func TestFailedJobForgottenOnceVMRunsWithTheList(t *testing.T) {
	f := newSvcFixture(t)
	f.ctl.failStart = boxvm.ErrStartTimeout
	s := f.svc(t)
	if _, err := s.Apply(); err != nil {
		t.Fatal(err)
	}
	waitJob(t, s)
	if _, ok := s.ApplyStatus(); !ok {
		t.Fatal("a failed start with the VM down must be reported")
	}
	f.ctl.running = true
	if _, ok := s.ApplyStatus(); ok {
		t.Fatal("VM running with the saved list: the failed job is stale")
	}
	if v, err := s.View(); err != nil || v.Apply != nil {
		t.Fatalf("view still carries it: %+v %v", v.Apply, err)
	}
	// Running but the list differs: still reported.
	f.ctl.running = true
	if _, _, err := s.Add(f.sub(t, "z"), "rw", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ApplyStatus(); !ok {
		t.Fatal("list differs from the VM: the failed job stays")
	}
}

func TestRunningJobCarriesServerElapsed(t *testing.T) {
	f := newSvcFixture(t)
	f.ctl.hold = make(chan struct{})
	s := f.svc(t)
	if _, err := s.Apply(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	j, _ := s.ApplyStatus()
	if j.State != "running" || j.ElapsedMs < 20 || j.ElapsedMs > 5000 {
		t.Fatalf("%+v", j)
	}
	close(f.ctl.hold)
	waitJob(t, s)
}

// S4: the CLI and the daemon each run a service over one vm-shares.json. A
// removal from the CLI is not undone by the daemon's next write, and the
// daemon's view shows it.
func TestSvcReloadsAListChangedByAnotherProcess(t *testing.T) {
	f := newSvcFixture(t)
	x, y := f.sub(t, "x"), f.sub(t, "y")
	serve := f.svc(t)
	if _, _, err := serve.Add(x, "rw", false); err != nil {
		t.Fatal(err)
	}
	cli := f.svc(t)
	if _, err := cli.Remove(x); err != nil {
		t.Fatal(err)
	}
	v, err := serve.View()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range v.Rows {
		if r.Path == x && r.Pending != "removed" {
			t.Fatalf("the daemon still lists %s after the CLI removed it: %+v", x, v.Rows)
		}
	}
	if _, _, err := serve.Add(y, "rw", false); err != nil {
		t.Fatal(err)
	}
	list, _, err := vmshares.Load(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Path != y {
		t.Fatalf("saved list %+v, want only %s (the CLI removal kept)", list, y)
	}
	if got := f.ctl.listed; len(got) != 1 || got[0].Path != y {
		t.Fatalf("the VM was handed %+v", got)
	}
}

// S4: an apply another process is running is refused here as "already applying".
func TestSvcApplyRefusedWhileAnotherProcessApplies(t *testing.T) {
	f := newSvcFixture(t)
	s := f.svc(t)
	f.ctl.lockHeld = true
	_, err := s.Apply()
	if !errors.Is(err, web.ErrApplyRunning) || err.Error() != "another agent-box is applying the box VM's folder list; try again when it finishes" {
		t.Fatalf("Apply = %v", err)
	}
	if j, ok := s.lastJob(); ok {
		t.Fatalf("a job was started: %+v", j)
	}
}
