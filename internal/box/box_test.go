package box

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBoxNameIsStableAndCanonical(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	m := newTestManager(t)
	viaReal, err := m.boxName(dir)
	if err != nil {
		t.Fatal(err)
	}
	viaLink, err := m.boxName(link)
	if err != nil {
		t.Fatal(err)
	}
	if viaReal != viaLink {
		t.Fatalf("symlink and real path produced different boxes: %q vs %q", viaReal, viaLink)
	}
	if !strings.HasPrefix(viaReal, namePrefix) {
		t.Fatalf("box name %q lacks the %q prefix", viaReal, namePrefix)
	}
}

func TestBoxNameSeparatesInstances(t *testing.T) {
	dir := t.TempDir()
	a, err := newTestManagerWithInstance(t, "DEV").boxName(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newTestManagerWithInstance(t, "PROD").boxName(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("DEV and PROD must not share a box for the same workspace")
	}
}

// --- argv composition ------------------------------------------------------

func TestExecArgvForwardsEnvByNameOnly(t *testing.T) {
	m := newTestManager(t)
	env := []string{"ANTHROPIC_API_KEY=sk-super-secret", "TERM=xterm-256color"}
	got := m.execArgv("ac-box-abc", "/home/claude/proj", []string{"claude", "--model", "opus"}, env)
	joined := strings.Join(got, " ")
	if strings.Contains(joined, "sk-super-secret") {
		t.Fatalf("secret VALUE leaked into argv: %q", joined)
	}
	if !containsPair(got, "-e", "ANTHROPIC_API_KEY") {
		t.Fatalf("expected valueless -e ANTHROPIC_API_KEY, got %q", joined)
	}
	if !containsPair(got, "-w", "/home/claude/proj") {
		t.Fatalf("expected -w with the workspace, got %q", joined)
	}
	if got[0] != "/usr/bin/docker" {
		t.Fatalf("argv[0] = %q, want the absolute docker path", got[0])
	}
}

// Host-specific variables must not cross the boundary: a host PATH or HOME
// inside the box breaks in-container binary resolution and leaks host layout.
func TestExecArgvDropsHostSpecificVariables(t *testing.T) {
	m := newTestManager(t)
	env := []string{
		"PATH=/home/claude/.local/bin:/usr/bin",
		"HOME=/home/claude",
		"SHELL=/usr/bin/zsh",
		"USER=claude",
		"LOGNAME=claude",
		"PWD=/home/claude",
		"ABDUCO_SOCKET_DIR=/run/x",
		"TERM=xterm-256color",
	}
	got := m.execArgv("b", "/w", []string{"claude"}, env)
	for _, forbidden := range []string{"PATH", "HOME", "SHELL", "USER", "LOGNAME", "PWD", "ABDUCO_SOCKET_DIR"} {
		if containsPair(got, "-e", forbidden) {
			t.Fatalf("host-specific %q must not be forwarded into the box: %v", forbidden, got)
		}
	}
	if !containsPair(got, "-e", "TERM") {
		t.Fatal("TERM must be forwarded so TUIs render")
	}
}

func TestRunArgsMountsExactlyTheWorkspaceAndState(t *testing.T) {
	m := newTestManager(t)
	got := m.runArgs("ac-box-abc", "/home/claude/proj")
	joined := strings.Join(got, " ")
	for _, forbidden := range []string{"docker.sock", "/home/claude/.ssh", "/home/claude/.claude:"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("run args must never reference %q: %q", forbidden, joined)
		}
	}
	if !containsPair(got, "-v", "/home/claude/proj:/home/claude/proj:rw") {
		t.Fatalf("workspace must mount at its identical host path: %q", joined)
	}
	if n := countFlag(got, "-v"); n != 2 {
		t.Fatalf("expected exactly 2 mounts (workspace + state), got %d: %q", n, joined)
	}
}

func TestRunArgsAreHardened(t *testing.T) {
	m := newTestManager(t)
	joined := strings.Join(m.runArgs("ac-box-abc", "/tmp/p"), " ")
	for _, want := range []string{"--cap-drop=ALL", "no-new-privileges", "--read-only", "--pids-limit", "--init"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing hardening %q in %q", want, joined)
		}
	}
}

func TestSysboxRuntimeOnlyWhenDetected(t *testing.T) {
	plain := newTestManager(t)
	if strings.Contains(strings.Join(plain.runArgs("b", "/tmp/p"), " "), "sysbox") {
		t.Fatal("must not request sysbox when it is not installed")
	}
	if plain.Runtime() != runtimeRunc {
		t.Fatalf("Runtime() = %q, want %q", plain.Runtime(), runtimeRunc)
	}
	withSysbox := newTestManagerWithSysbox(t)
	if !strings.Contains(strings.Join(withSysbox.runArgs("b", "/tmp/p"), " "), "--runtime="+runtimeSysbox) {
		t.Fatal("expected --runtime=sysbox-runc when detected")
	}
	if withSysbox.Runtime() != runtimeSysbox {
		t.Fatalf("Runtime() = %q, want %q", withSysbox.Runtime(), runtimeSysbox)
	}
}

// --- lifecycle -------------------------------------------------------------

func TestSecondSessionReusesTheSameBox(t *testing.T) {
	m, fake := newTestManagerWithFake(t)
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		if _, _, err := m.Wrap(context.Background(), dir, []string{"claude"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if fake.runs() != 1 {
		t.Fatalf("expected 1 docker run for 2 sessions on one workspace, got %d", fake.runs())
	}
}

func TestBoxStopsAfterIdleTTLWhenLastSessionReleases(t *testing.T) {
	m, fake := newTestManagerWithFakeTTL(t, 20*time.Millisecond)
	dir := t.TempDir()
	if _, _, err := m.Wrap(context.Background(), dir, []string{"claude"}, nil); err != nil {
		t.Fatal(err)
	}
	m.Release(dir)
	waitFor(t, 2*time.Second, func() bool { return fake.stoppedAny(namePrefix) })
}

func TestReleaseOfOneOfTwoSessionsKeepsTheBox(t *testing.T) {
	m, fake := newTestManagerWithFakeTTL(t, 20*time.Millisecond)
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		if _, _, err := m.Wrap(context.Background(), dir, []string{"claude"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	m.Release(dir)
	time.Sleep(200 * time.Millisecond)
	if fake.stops() != 0 {
		t.Fatalf("box stopped while a session was still live (stops=%d)", fake.stops())
	}
}

// A session started during the idle window must cancel the pending stop —
// otherwise the box dies underneath a live session.
func TestSpawnDuringIdleWindowCancelsTheStop(t *testing.T) {
	m, fake := newTestManagerWithFakeTTL(t, 150*time.Millisecond)
	dir := t.TempDir()
	if _, _, err := m.Wrap(context.Background(), dir, []string{"claude"}, nil); err != nil {
		t.Fatal(err)
	}
	m.Release(dir)
	time.Sleep(30 * time.Millisecond)
	if _, _, err := m.Wrap(context.Background(), dir, []string{"claude"}, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if fake.stops() != 0 {
		t.Fatalf("pending stop was not cancelled by the new session (stops=%d)", fake.stops())
	}
}

func TestReconcileReapsBoxesWithNoSessions(t *testing.T) {
	m, fake := newTestManagerWithFake(t)
	fake.existing = []string{"ac-box-dead1", "ac-box-dead2"}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.stops() != 2 {
		t.Fatalf("reconcile must reap orphaned boxes; stopped %d of 2", fake.stops())
	}
}

func TestReconcileKeepsBoxesThatHaveLiveSessions(t *testing.T) {
	m, fake := newTestManagerWithFake(t)
	dir := t.TempDir()
	if _, _, err := m.Wrap(context.Background(), dir, []string{"claude"}, nil); err != nil {
		t.Fatal(err)
	}
	name, _ := m.boxName(dir)
	fake.existing = []string{name, "ac-box-orphan"}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.stoppedAny(name) {
		t.Fatal("reconcile stopped a box that still has a live session")
	}
	if fake.stops() != 1 {
		t.Fatalf("expected only the orphan reaped, stops=%d", fake.stops())
	}
}

// After a daemon restart the session layer reattaches live sessions before the
// box layer reconciles. Without Adopt, reconcile would reap the box out from
// under a session abduco just brought back — the worst regression this slice
// could ship.
func TestAdoptedWorkspaceSurvivesReconcile(t *testing.T) {
	m, fake := newTestManagerWithFake(t)
	dir := t.TempDir()
	name, err := m.boxName(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Adopt(dir); err != nil {
		t.Fatal(err)
	}
	fake.existing = []string{name, "ac-box-orphan"}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.stoppedAny(name) {
		t.Fatal("reconcile reaped a box belonging to a reattached session")
	}
	if fake.stops() != 1 {
		t.Fatalf("expected exactly the orphan reaped, stops=%d", fake.stops())
	}
}
