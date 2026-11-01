package box

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func okVerify(context.Context, string) error { return nil }

// D3 (M3c) plus M3b: an unfenced box (unbound, EgressAll off: darwin) gets no egress flags; its only change since
// M3c is the claims label (M3b spec correction 8a), which is a label, not a
// network, mount or env change.
func TestUnfencedRunArgsOnlyGainTheClaimsLabel(t *testing.T) {
	m := newTestManager(t)
	m.SetEgress(Egress{Network: "agentbox-egress-TEST", Verify: okVerify, AddHosts: []string{"host.lima.internal:192.168.5.2"}})
	got := m.runArgs("ab-box-abc", "/home/claude/proj", nil, false, false, "", DefaultMemory)
	if slices.Contains(got, "--network") || slices.Contains(got, "--add-host") {
		t.Fatalf("unbound box got egress flags: %q", got)
	}
	want := []string{
		"run", "-d", "--init", "--pull=never",
		"--name", "ab-box-abc",
		"--label", LabelWorkspace + "=/home/claude/proj",
		"--label", LabelInstance + "=TEST",
		"--label", LabelClaims + "=1",
		"--cap-drop=ALL",
		"--security-opt", "no-new-privileges",
		"--read-only",
		"--tmpfs", "/tmp:rw,nosuid,nodev,exec,size=512m",
		"--pids-limit", "512",
		"--memory", DefaultMemory, "--memory-swap", DefaultMemory,
		"--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
		"--mount", "type=bind,src=/home/claude/proj,dst=/home/claude/proj",
		"--mount", "type=bind,src=" + m.stateDir("ab-box-abc") + ",dst=" + boxHome,
		"--mount", "type=bind,src=" + OutputDir(m.opts.StateRoot, "ab-box-abc") + ",dst=" + PlaywrightOutBox,
		"culiops/agent-box:base", "sleep", "infinity",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("unbound runArgs changed:\n got  %q\n want %q", got, want)
	}
}

func TestBoundRunArgsJoinEgressNetwork(t *testing.T) {
	m := newTestManager(t)
	m.SetEgress(Egress{Network: "agentbox-egress-TEST", Verify: okVerify, AddHosts: []string{"host.lima.internal:192.168.5.2"}})
	got := m.runArgs("ab-box-abc", "/w", nil, true, false, "", DefaultMemory)
	if !containsPair(got, "--network", "agentbox-egress-TEST") || !containsPair(got, "--add-host", "host.lima.internal:192.168.5.2") {
		t.Fatalf("runArgs = %q", got)
	}
	// image and command stay last
	if got[len(got)-3] != "culiops/agent-box:base" {
		t.Fatalf("image not in place: %q", got)
	}
}

func TestBoundWrapRefusedWithoutEgress(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	_, _, _, err := m.Wrap(context.Background(), t.TempDir(), "s1", BoxKey{Binding: "abc123abc123"}, []string{"sh"}, nil, nil)
	if !errors.Is(err, ErrBoxUnavailable) || !strings.Contains(err.Error(), "egress control is not running") {
		t.Fatalf("err = %v", err)
	}
	if f.runs() != 0 {
		t.Fatal("docker run happened without egress control")
	}
	// Network set but no verifier is just as unusable.
	m.SetEgress(Egress{Network: "n"})
	if _, _, _, err := m.Wrap(context.Background(), t.TempDir(), "s2", BoxKey{Binding: "abc123abc123"}, []string{"sh"}, nil, nil); !errors.Is(err, ErrBoxUnavailable) {
		t.Fatalf("nil Verify must refuse: %v", err)
	}
	if f.runs() != 0 {
		t.Fatal("docker run happened without an egress verifier")
	}
}

func newVerifyCountingManager(t *testing.T, optsVerify func(context.Context, string) error) (*Manager, *fakeClient) {
	t.Helper()
	f := newFake()
	m, err := New(Options{DockerBin: "/d", Image: "culiops/agent-box:base", Instance: "TEST", IdleTTL: time.Hour,
		StateRoot: t.TempDir(), Client: f, Verify: optsVerify})
	if err != nil {
		t.Fatal(err)
	}
	return m, f
}

func TestBoundBoxUsesEgressVerifyNotOptionsVerify(t *testing.T) {
	var optsN, egressN int
	m, _ := newVerifyCountingManager(t, func(context.Context, string) error { optsN++; return nil })
	m.SetEgress(Egress{Network: "n", Verify: func(context.Context, string) error { egressN++; return nil }})
	if _, _, _, err := m.Wrap(context.Background(), t.TempDir(), "s1", BoxKey{Binding: "abc123abc123"}, []string{"sh"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if optsN != 0 || egressN != 1 {
		t.Fatalf("bound: Options.Verify=%d Egress.Verify=%d, want 0 and 1", optsN, egressN)
	}
	optsN, egressN = 0, 0
	if _, _, _, err := m.Wrap(context.Background(), t.TempDir(), "s2", BoxKey{}, []string{"sh"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if optsN != 1 || egressN != 0 {
		t.Fatalf("unbound: Options.Verify=%d Egress.Verify=%d, want 1 and 0", optsN, egressN)
	}
}

func TestBoundVerifyFailureRemovesFreshBox(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	m.SetEgress(Egress{Network: "n", Verify: func(context.Context, string) error { return errors.New("direct egress open") }})
	_, _, _, err := m.Wrap(context.Background(), t.TempDir(), "s1", BoxKey{Binding: "abc123abc123"}, []string{"sh"}, nil, nil)
	if !errors.Is(err, ErrBoxUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if f.runs() != 1 || f.stops() != 1 {
		t.Fatalf("runs=%d stops=%d, want the fresh box started once and removed once", f.runs(), f.stops())
	}
}

func TestPreEgressBoxWithLiveSessionsIsRefusedNotRemoved(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	m.SetEgress(Egress{Network: "n", Verify: func(context.Context, string) error { return errors.New("direct egress open") }})
	dir := t.TempDir()
	name, _ := m.boxName(dir, BoxKey{Binding: "abc123abc123"})
	f.running[name] = true
	m.AdoptBox(name) // a live session from before the restart
	_, _, _, err := m.Wrap(context.Background(), dir, "s2", BoxKey{Binding: "abc123abc123"}, []string{"sh"}, nil, nil)
	if !errors.Is(err, ErrBoxUnavailable) || !strings.Contains(err.Error(), "still runs 1 session") {
		t.Fatalf("err = %v", err)
	}
	if f.stops() != 0 {
		t.Fatal("a box with a live session was removed")
	}
	if m.isVerified(name) {
		t.Fatal("a failed box must not be marked verified")
	}
	// The counter is untouched: the refused spawn did not leak a reference.
	m.mu.Lock()
	n := m.active[name]
	m.mu.Unlock()
	if n != 1 {
		t.Fatalf("active = %d, want 1", n)
	}
}

func TestPreEgressIdleBoxIsRemovedAndRecreatedOnNetwork(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	calls := 0
	m.SetEgress(Egress{Network: "n", Verify: func(context.Context, string) error {
		calls++
		if calls == 1 {
			return errors.New("direct egress open")
		}
		return nil
	}})
	dir := t.TempDir()
	name, _ := m.boxName(dir, BoxKey{Binding: "abc123abc123"})
	f.running[name] = true // left over from M3a, no live session
	f.mounts[name] = []string{dir, boxHome, PlaywrightOutBox}
	if _, _, _, err := m.Wrap(context.Background(), dir, "s1", BoxKey{Binding: "abc123abc123"}, []string{"sh"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if f.stops() != 1 || f.runs() != 1 || calls != 2 {
		t.Fatalf("stops=%d runs=%d verifies=%d, want 1/1/2", f.stops(), f.runs(), calls)
	}
	if !containsPair(f.runArgs[0], "--network", "n") {
		t.Fatalf("recreated box is not on the egress network: %q", f.runArgs[0])
	}
	if !m.isVerified(name) {
		t.Fatal("the recreated box should be verified")
	}
}

func TestPreEgressIdleBoxThatCannotBeRemovedIsRefused(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	m.SetEgress(Egress{Network: "n", Verify: func(context.Context, string) error { return errors.New("direct egress open") }})
	dir := t.TempDir()
	name, _ := m.boxName(dir, BoxKey{Binding: "abc123abc123"})
	f.running[name] = true
	f.mounts[name] = []string{dir, boxHome, PlaywrightOutBox}
	f.stopFails = 1
	_, _, _, err := m.Wrap(context.Background(), dir, "s1", BoxKey{Binding: "abc123abc123"}, []string{"sh"}, nil, nil)
	if !errors.Is(err, ErrBoxUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if f.runs() != 0 {
		t.Fatal("must not docker run over a box that could not be removed")
	}
}

// newFencedManager is a Linux-shaped manager: EgressAll on, so every box is
// fenced, with the given egress check. Options.Verify is left nil.
func newFencedManager(t *testing.T, verify func(context.Context, string) error) (*Manager, *fakeClient) {
	t.Helper()
	f := newFake()
	m, err := New(Options{DockerBin: "/d", Image: "culiops/agent-box:base", Instance: "TEST", IdleTTL: time.Hour,
		StateRoot: t.TempDir(), Client: f, EgressAll: true})
	if err != nil {
		t.Fatal(err)
	}
	if verify != nil {
		m.SetEgress(Egress{Network: "agentbox-egress-TEST", Verify: verify})
	}
	return m, f
}

func TestUnboundBoxJoinsEgressNetworkWhenEgressAll(t *testing.T) {
	m, f := newFencedManager(t, okVerify)
	if _, _, _, err := m.Wrap(context.Background(), t.TempDir(), "s1", BoxKey{}, []string{"sh"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if f.runs() != 1 || !containsPair(f.runArgs[0], "--network", "agentbox-egress-TEST") {
		t.Fatalf("unbound box not on the egress network: %q", f.runArgs)
	}
}

func TestUnboundBoxUsesEgressVerifyWhenEgressAll(t *testing.T) {
	var optsN, egressN int
	f := newFake()
	m, err := New(Options{DockerBin: "/d", Image: "culiops/agent-box:base", Instance: "TEST", IdleTTL: time.Hour,
		StateRoot: t.TempDir(), Client: f, EgressAll: true,
		Verify: func(context.Context, string) error { optsN++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	m.SetEgress(Egress{Network: "n", Verify: func(context.Context, string) error { egressN++; return nil }})
	if _, _, _, err := m.Wrap(context.Background(), t.TempDir(), "s1", BoxKey{}, []string{"sh"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if optsN != 0 || egressN != 1 {
		t.Fatalf("Options.Verify=%d Egress.Verify=%d, want 0 and 1", optsN, egressN)
	}
}

func TestUnboundWrapRefusedWithoutEgressWhenEgressAll(t *testing.T) {
	m, f := newFencedManager(t, nil)
	_, _, _, err := m.Wrap(context.Background(), t.TempDir(), "s1", BoxKey{}, []string{"sh"}, nil, nil)
	if !errors.Is(err, ErrBoxUnavailable) || !strings.Contains(err.Error(), "egress control is not running") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "AWS") {
		t.Fatalf("an unbound box's refusal must not talk about AWS: %v", err)
	}
	if f.runs() != 0 {
		t.Fatal("docker run happened without egress control")
	}
}

func TestPreEgressUnboundBoxWithLiveSessionsIsRefused(t *testing.T) {
	m, f := newFencedManager(t, func(context.Context, string) error { return errors.New("direct egress open") })
	dir := t.TempDir()
	name, _ := m.boxName(dir, BoxKey{})
	f.running[name] = true
	m.AdoptBox(name)
	_, _, _, err := m.Wrap(context.Background(), dir, "s2", BoxKey{}, []string{"sh"}, nil, nil)
	if !errors.Is(err, ErrBoxUnavailable) || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "still runs 1 session") {
		t.Fatalf("err = %v", err)
	}
	if f.stops() != 0 {
		t.Fatal("a box with a live session was removed")
	}
	m.mu.Lock()
	n := m.active[name]
	m.mu.Unlock()
	if n != 1 {
		t.Fatalf("active = %d, want 1", n)
	}
}

func TestPreEgressUnboundIdleBoxIsRecreatedOnNetwork(t *testing.T) {
	calls := 0
	m, f := newFencedManager(t, func(context.Context, string) error {
		calls++
		if calls == 1 {
			return errors.New("direct egress open")
		}
		return nil
	})
	dir := t.TempDir()
	name, _ := m.boxName(dir, BoxKey{})
	f.running[name] = true // an unfenced box left by an older build, no live session
	f.mounts[name] = []string{dir, boxHome, PlaywrightOutBox}
	if _, _, _, err := m.Wrap(context.Background(), dir, "s1", BoxKey{}, []string{"sh"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if f.stops() != 1 || f.runs() != 1 || calls != 2 {
		t.Fatalf("stops=%d runs=%d verifies=%d, want 1/1/2", f.stops(), f.runs(), calls)
	}
	if !containsPair(f.runArgs[0], "--network", "agentbox-egress-TEST") {
		t.Fatalf("recreated box is not on the egress network: %q", f.runArgs[0])
	}
	if !m.isVerified(name) {
		t.Fatal("the recreated box should be verified")
	}
}

// newDarwinShapedManager is what boxLayer.options builds on macOS since #69:
// EgressAll on (every box fenced), the VM hooks set, GuardInPlace off.
func newDarwinShapedManager(t *testing.T, verify func(context.Context, string) error) (*Manager, *fakeClient) {
	t.Helper()
	f := newFake()
	m, err := New(Options{DockerBin: "/d", Image: "culiops/agent-box:base", Instance: "TEST", IdleTTL: time.Hour,
		StateRoot: t.TempDir(), Client: f, EgressAll: true, GuardInPlace: false,
		Prepare:   func(context.Context) error { return nil },
		Reachable: func(context.Context) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	m.SetEgress(Egress{Network: "agentbox-egress-TEST", Verify: verify})
	return m, f
}

// An unbound box left on the default bridge by a build from before #69 fails
// the fenced verify. With sessions in it, it is refused (not killed); idle, it
// is recreated on the egress network.
func TestDarwinUnboundOldBoxRefusedWhileLive(t *testing.T) {
	m, f := newDarwinShapedManager(t, func(context.Context, string) error { return errors.New("direct egress open") })
	dir := t.TempDir()
	name, _ := m.boxName(dir, BoxKey{})
	f.running[name] = true
	m.AdoptBox(name)
	_, _, _, err := m.Wrap(context.Background(), dir, "s2", BoxKey{}, []string{"sh"}, nil, nil)
	if !errors.Is(err, ErrBoxUnavailable) || !strings.Contains(err.Error(), "started before egress control") {
		t.Fatalf("err = %v", err)
	}
	if f.stops() != 0 || f.runs() != 0 {
		t.Fatalf("a box with a live session was touched: stops=%d runs=%d", f.stops(), f.runs())
	}
}

func TestDarwinUnboundOldIdleBoxRecreatedOnEgressNetwork(t *testing.T) {
	calls := 0
	m, f := newDarwinShapedManager(t, func(context.Context, string) error {
		calls++
		if calls == 1 {
			return errors.New("direct egress open")
		}
		return nil
	})
	dir := t.TempDir()
	name, _ := m.boxName(dir, BoxKey{})
	f.running[name] = true
	f.mounts[name] = []string{dir, boxHome, PlaywrightOutBox}
	if _, _, _, err := m.Wrap(context.Background(), dir, "s1", BoxKey{}, []string{"sh"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if f.stops() != 1 || f.runs() != 1 {
		t.Fatalf("stops=%d runs=%d, want 1/1", f.stops(), f.runs())
	}
	if !containsPair(f.runArgs[0], "--network", "agentbox-egress-TEST") {
		t.Fatalf("recreated box is not on the egress network: %q", f.runArgs[0])
	}
}

func TestEgressAllOffKeepsUnboundOnBridge(t *testing.T) {
	var optsN, egressN int
	m, f := newVerifyCountingManager(t, func(context.Context, string) error { optsN++; return nil })
	m.SetEgress(Egress{Network: "n", Verify: func(context.Context, string) error { egressN++; return nil }})
	if _, _, _, err := m.Wrap(context.Background(), t.TempDir(), "s1", BoxKey{}, []string{"sh"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(f.runArgs[0], "--network") {
		t.Fatalf("unbound box left the default network with EgressAll off: %q", f.runArgs[0])
	}
	if optsN != 1 || egressN != 0 {
		t.Fatalf("Options.Verify=%d Egress.Verify=%d, want 1 and 0", optsN, egressN)
	}
}

func TestRunInputFeedsStdin(t *testing.T) {
	c := CLIClient{Bin: "/bin/sh"}
	out, err := c.RunInput(context.Background(), []string{"-c", "cat"}, "hi")
	if err != nil || out != "hi" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

// The stdin payload carries the probe credential, so a failing command must not
// echo it back into the returned error.
func TestRunInputErrorDoesNotEchoStdin(t *testing.T) {
	c := CLIClient{Bin: "/bin/sh"}
	_, err := c.RunInput(context.Background(), []string{"-c", "echo oops >&2; exit 3"}, "SECRET-CRED")
	if err == nil || strings.Contains(err.Error(), "SECRET-CRED") {
		t.Fatalf("err = %v", err)
	}
}
