package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/session"
	"github.com/culiops/culibox/internal/vmshares"
	"github.com/culiops/culibox/internal/web"
)

// applyLayer is a set-up darwin layer whose VM runs with good firewall rules.
func applyLayer(t *testing.T) (*boxLayer, *fakeColima, *fakeDocker) {
	t.Helper()
	l, fc, d := darwinLayer(t, false)
	vc := l.vm.Config()
	fc.running = true
	fc.rules = goodFilterRules(vc.EgressPort, vc.BrokerPort)
	return l, fc, d
}

func noSessionInfo() []session.Info { return nil }

func shortenApplyTimeouts(t *testing.T, job, check time.Duration) {
	t.Helper()
	oj, oc := applyJobTimeout, applyCheckTimeout
	applyJobTimeout, applyCheckTimeout = job, check
	t.Cleanup(func() { applyJobTimeout, applyCheckTimeout = oj, oc })
}

func realSvc(t *testing.T, l *boxLayer) *vmShareSvc {
	t.Helper()
	f := bootVMFolders(l, config.Config{}, func() []config.AgentConfig { return nil }, func() []string { return nil }, "", noSessionInfo)
	svc, ok := f.(*vmShareSvc)
	if !ok {
		t.Fatalf("bootVMFolders returned %T", f)
	}
	return svc
}

func TestLayerApplyStepsFlagAndCheckOrder(t *testing.T) {
	l, fc, d := applyLayer(t)
	ctl := newLayerVMControl(l, noSessionInfo)
	var events []string
	var duringSSH bool
	fc.onSSH = func() { duringSSH = ctl.Applying() }
	err := ctl.Apply(context.Background(), func(name, state, sub string) { events = append(events, name+":"+state) })
	if err != nil {
		t.Fatal(err)
	}
	want := "stop:running stop:done start:running start:done check:running check:done"
	if got := strings.Join(events, " "); got != want {
		t.Fatalf("steps\n got %s\nwant %s", got, want)
	}
	if !duringSSH {
		t.Fatal("Applying() was false while the job ran")
	}
	if ctl.Applying() {
		t.Fatal("Applying() still true after the job")
	}
	iptables, mounts, start := -1, -1, -1
	for i, c := range fc.calls {
		switch {
		case strings.HasPrefix(c, "start "):
			start = i
		case strings.Contains(c, "iptables"):
			iptables = i
		case strings.Contains(c, "/proc/self/mounts"):
			mounts = i
		}
	}
	if !(start >= 0 && start < iptables && iptables < mounts) {
		t.Fatalf("order: start %d, rules %d, mounts %d; calls %q", start, iptables, mounts, fc.calls)
	}
	if !strings.Contains(d.log(), "ps") {
		t.Fatalf("no reconcile after the checks:\n%s", d.log())
	}
	if !l.foldersVerified() {
		t.Fatal("a passing check left the failure flag set")
	}
}

func TestLayerApplyCheckFailureCopy(t *testing.T) {
	l, fc, _ := applyLayer(t)
	fc.mounts = ""
	svc := realSvc(t, l)
	if _, err := svc.Apply(); err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, svc)
	if j.State != "failed" || j.Error != "A containment check failed." || j.Steps[2].State != "failed" || j.Steps[1].State != "done" {
		t.Fatalf("%+v", j)
	}
	if !strings.Contains(j.Detail, "folders do not match") {
		t.Fatalf("detail %q", j.Detail)
	}
	if l.ctl.Applying() || l.foldersVerified() {
		t.Fatal("flag must clear and the failed check be remembered")
	}
}

func TestLayerApplyStopFailureCopy(t *testing.T) {
	shortenApplyTimeouts(t, 300*time.Millisecond, time.Second)
	l, fc, _ := applyLayer(t)
	fc.hang = true // `colima status` never answers
	svc := realSvc(t, l)
	if _, err := svc.Apply(); err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, svc)
	if j.State != "failed" || j.Error != "The VM did not stop." || j.Steps[0].State != "failed" || j.Steps[1].State != "not_run" {
		t.Fatalf("%+v", j)
	}
	if l.ctl.Applying() {
		t.Fatal("a wedged colima left Applying() true")
	}
}

func TestLayerApplyHangingCheckEndsFailed(t *testing.T) {
	for _, kind := range []string{"iptables", "mounts"} {
		t.Run(kind, func(t *testing.T) {
			shortenApplyTimeouts(t, time.Minute, 300*time.Millisecond)
			l, fc, _ := applyLayer(t)
			fc.hangSSH = kind
			svc := realSvc(t, l)
			start := time.Now()
			if _, err := svc.Apply(); err != nil {
				t.Fatal(err)
			}
			j := waitJob(t, svc)
			if j.State != "failed" || j.Error != "A containment check failed." || time.Since(start) > 10*time.Second {
				t.Fatalf("%+v after %s", j, time.Since(start))
			}
			if l.ctl.Applying() {
				t.Fatal("Applying() still true")
			}
		})
	}
}

func TestApplyingIsTrueBeforeApplyReturns(t *testing.T) {
	shortenApplyTimeouts(t, time.Minute, 500*time.Millisecond)
	l, fc, _ := applyLayer(t)
	fc.hangSSH = "iptables" // keeps the job alive for the assertion
	svc := realSvc(t, l)
	if _, err := svc.Apply(); err != nil {
		t.Fatal(err)
	}
	if !l.ctl.Applying() {
		t.Fatal("a spawn could slip in: Applying() was false right after Apply() returned")
	}
	if err := l.prepare(context.Background()); !errors.Is(err, box.ErrVMRestarting) {
		t.Fatalf("prepare during the job: %v", err)
	}
	waitJob(t, svc)
}

func TestBootVMFoldersLogsWholeHome(t *testing.T) {
	l, _, _ := applyLayer(t)
	vc := l.vm.Config()
	l.vm.SetShares([]vmshares.Share{{Path: vc.Home, Mode: vmshares.RW}})
	y, err := l.vm.Config().YAML()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vc.YAMLPath(), y, 0o600); err != nil {
		t.Fatal(err)
	}
	l.vm.SetShares(nil)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	var _ web.VMFolders = realSvc(t, l)
	if want := vmshares.WholeHomeLine(0); !strings.Contains(buf.String(), want) {
		t.Fatalf("log %q lacks %q", buf.String(), want)
	}
}

func TestBootVMFoldersQuietWhenNotWholeHome(t *testing.T) {
	l, _, _ := applyLayer(t)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	realSvc(t, l)
	if strings.Contains(buf.String(), "whole home folder") {
		t.Fatalf("log %q", buf.String())
	}
}

func TestCorruptListDoesNotHideVMState(t *testing.T) {
	f := newSvcFixture(t)
	if err := os.WriteFile(f.dir+"/"+vmshares.FileName, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := f.svc(t)
	f.ctl.whole = true
	f.ctl.applied = []vmshares.Share{{Path: f.home, Mode: vmshares.RW}}
	if v, _ := s.View(); v.State != "whole_home" || v.ListError == "" {
		t.Fatalf("%+v", v)
	}
	f.ctl.notSetUp = true
	if v, _ := s.View(); v.State != "not_set_up" {
		t.Fatalf("%+v", v)
	}
}

func statusCalls(fc *fakeColima) int {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	n := 0
	for _, c := range fc.calls {
		if strings.HasPrefix(c, "status ") {
			n++
		}
	}
	return n
}

// C3: VMRunning runs on /api/meta while a failed apply is held, so its colima
// status answer is cached for 5 s and bounded at 3 s (under the 4 s probe).
func TestVMRunningIsCached(t *testing.T) {
	l, fc, _ := darwinLayer(t, false)
	fc.running = true
	c := newLayerVMControl(l, func() []session.Info { return nil })
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	if !c.VMRunning() || !c.VMRunning() {
		t.Fatal("VMRunning = false for a running VM")
	}
	if n := statusCalls(fc); n != 1 {
		t.Fatalf("colima status ran %d times within 5 s, want 1", n)
	}
	fc.mu.Lock()
	fc.running = false
	fc.mu.Unlock()
	now = now.Add(4 * time.Second)
	if !c.VMRunning() {
		t.Fatal("a cached answer changed before 5 s")
	}
	now = now.Add(2 * time.Second)
	if c.VMRunning() {
		t.Fatal("the cache outlived 5 s")
	}
	if n := statusCalls(fc); n != 2 {
		t.Fatalf("colima status ran %d times, want 2", n)
	}
	if vmRunningTimeout != 3*time.Second {
		t.Fatalf("vmRunningTimeout = %s, want 3s", vmRunningTimeout)
	}
}
