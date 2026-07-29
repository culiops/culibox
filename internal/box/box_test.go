package box

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
	got := m.execArgv("ab-box-abc", "/home/claude/proj", "bash-1", []string{"claude", "--model", "opus"}, env)
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
	got := m.execArgv("b", "/w", "bash-1", []string{"claude"}, env)
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
	got := m.runArgs("ab-box-abc", "/home/claude/proj", nil)
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
	joined := strings.Join(m.runArgs("ab-box-abc", "/tmp/p", nil), " ")
	for _, want := range []string{"--cap-drop=ALL", "no-new-privileges", "--read-only", "--pids-limit", "--init"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing hardening %q in %q", want, joined)
		}
	}
}

func TestSysboxRuntimeOnlyWhenDetected(t *testing.T) {
	plain := newTestManager(t)
	if strings.Contains(strings.Join(plain.runArgs("b", "/tmp/p", nil), " "), "sysbox") {
		t.Fatal("must not request sysbox when it is not installed")
	}
	if plain.Runtime() != runtimeRunc {
		t.Fatalf("Runtime() = %q, want %q", plain.Runtime(), runtimeRunc)
	}
	withSysbox := newTestManagerWithSysbox(t)
	if !strings.Contains(strings.Join(withSysbox.runArgs("b", "/tmp/p", nil), " "), "--runtime="+runtimeSysbox) {
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
		if _, _, _, err := m.Wrap(context.Background(), dir, "bash-1", []string{"claude"}, nil, nil); err != nil {
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
	if _, _, _, err := m.Wrap(context.Background(), dir, "bash-1", []string{"claude"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(mustBoxName(t, m, dir))
	waitFor(t, 2*time.Second, func() bool { return fake.stoppedAny(namePrefix) })
}

func TestReleaseOfOneOfTwoSessionsKeepsTheBox(t *testing.T) {
	m, fake := newTestManagerWithFakeTTL(t, 20*time.Millisecond)
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		if _, _, _, err := m.Wrap(context.Background(), dir, "bash-1", []string{"claude"}, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	m.ReleaseBox(mustBoxName(t, m, dir))
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
	if _, _, _, err := m.Wrap(context.Background(), dir, "bash-1", []string{"claude"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(mustBoxName(t, m, dir))
	time.Sleep(30 * time.Millisecond)
	if _, _, _, err := m.Wrap(context.Background(), dir, "bash-1", []string{"claude"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if fake.stops() != 0 {
		t.Fatalf("pending stop was not cancelled by the new session (stops=%d)", fake.stops())
	}
}

func TestReconcileReapsBoxesWithNoSessions(t *testing.T) {
	m, fake := newTestManagerWithFake(t)
	fake.existing = []string{"ab-box-dead1", "ab-box-dead2"}
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
	if _, _, _, err := m.Wrap(context.Background(), dir, "bash-1", []string{"claude"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	name, _ := m.boxName(dir)
	fake.existing = []string{name, "ab-box-orphan"}
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
	m.AdoptBox(name)
	fake.existing = []string{name, "ab-box-orphan"}
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

// A named volume is seeded from the image, so it inherits the image's ownership
// and any host uid that differs cannot write to its own home — no auth
// persistence, no resume. A host-side directory created by the daemon is owned
// by the running user by construction, so the box works for every uid.
func TestStateIsAHostDirectoryNotANamedVolume(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	if _, err := m.ensureStateDir("ab-box-abc", nil); err != nil {
		t.Fatal(err)
	}
	args := m.runArgs("ab-box-abc", "/w", nil)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "ac-state-") {
		t.Fatalf("state must not be a named volume (image ownership breaks other uids): %q", joined)
	}
	var stateMount string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-v" && strings.HasSuffix(args[i+1], ":"+boxHome) {
			stateMount = args[i+1]
		}
	}
	if stateMount == "" {
		t.Fatalf("no state mount onto %s in %q", boxHome, joined)
	}
	host := strings.TrimSuffix(stateMount, ":"+boxHome)
	if !strings.HasPrefix(host, "/") {
		t.Fatalf("state source %q must be an absolute host path", host)
	}
	fi, err := os.Stat(host)
	if err != nil || !fi.IsDir() {
		t.Fatalf("state dir %q must exist before the box starts: %v", host, err)
	}
	// The invariant that actually makes this uid-agnostic: the daemon creates
	// the directory and passes its OWN uid to --user, so owner and runtime user
	// match by construction on any machine. A named volume could not hold this,
	// because its owner is fixed when the image is built.
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no syscall.Stat_t on this platform")
	}
	wantUser := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	if !containsPair(args, "--user", wantUser) {
		t.Fatalf("--user must be the daemon's own uid %q: %q", wantUser, joined)
	}
	if int(st.Uid) != os.Getuid() {
		t.Fatalf("state dir owned by uid %d but the box runs as %d", st.Uid, os.Getuid())
	}
}

// Containment is decided by what gets mounted, and the workspace is the one
// writable mount. Boxing $HOME therefore hands the agent the entire home —
// including ~/.ssh and any secrets file — which voids every other control in
// this package. Refusing is the only correct answer; narrowing silently would
// hide it.
func TestBoxingTheHomeDirectoryIsRefused(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	_, _, _, err = m.Wrap(context.Background(), home, "s-1", []string{"claude"}, nil, nil)
	if err == nil {
		t.Fatal("boxing the home directory must be refused; it mounts the whole home into the box")
	}
	if !strings.Contains(err.Error(), "host") {
		t.Fatalf("the error must name the way forward (trusted-host): %v", err)
	}
}

func TestBoxingAnAncestorOfHomeIsRefused(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	if _, _, _, err := m.Wrap(context.Background(), "/", "s-1", []string{"claude"}, nil, nil); err == nil {
		t.Fatal("boxing / must be refused")
	}
}

// A fresh box home has no onboarding state, so an interactive agent meets a
// first-run wizard instead of a prompt. The markers are configuration, not
// credentials, so seeding them is safe.
func TestNewBoxStateIsSeededPastFirstRunOnboarding(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	dir, err := m.ensureStateDir("ab-box-seed", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".claude.json"))
	if err != nil {
		t.Fatalf("a new box needs seeded agent state: %v", err)
	}
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d["hasCompletedOnboarding"] != true {
		t.Fatalf("onboarding marker missing: %v", d)
	}
}

// Seeding must never clobber state a running agent has already written.
func TestSeedingDoesNotOverwriteExistingState(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	dir, _ := m.ensureStateDir("ab-box-keep", nil)
	p := filepath.Join(dir, ".claude.json")
	if err := os.WriteFile(p, []byte(`{"mine":"kept"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ensureStateDir("ab-box-keep", nil); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), "kept") {
		t.Fatalf("existing agent state was overwritten: %s", raw)
	}
}

// Docker creates a missing bind target as root, and those stubs land inside the
// box home where the daemon user can no longer unlink them. Pre-creating the
// targets is what keeps the home reclaimable.
func TestMountpointsInsideTheBoxHomeArePreCreated(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "CLAUDE.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mounts := []Mount{
		{Host: filepath.Join(src, "skills"), Box: boxHome + "/.claude/skills"},
		{Host: filepath.Join(src, "CLAUDE.md"), Box: boxHome + "/.claude/CLAUDE.md"},
	}
	dir, err := m.ensureStateDir("ab-box-aaaaaaaaaaaa", mounts)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, ".claude/skills"))
	if err != nil || !fi.IsDir() {
		t.Fatalf("a directory-typed mount needs a directory target: %v", err)
	}
	fi, err = os.Stat(filepath.Join(dir, ".claude/CLAUDE.md"))
	if err != nil || fi.IsDir() {
		t.Fatalf("a file-typed mount needs a regular-file target: %v", err)
	}
	if fi.Size() != 0 {
		t.Fatalf("the mountpoint is a stub, not a copy: %d bytes", fi.Size())
	}
}

// The bundle root mounts onto the box's read-only rootfs, not into the home.
func TestMountpointsOutsideTheBoxHomeCreateNothing(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	src := t.TempDir()
	dir, err := m.ensureStateDir("ab-box-bbbbbbbbbbbb",
		[]Mount{{Host: src, Box: "/opt/agent-bundle"}})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != ".claude.json" {
			t.Fatalf("state dir gained %q from a mount outside the box home", e.Name())
		}
	}
}

func TestExistingMountpointContentIsNeverClobbered(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "CLAUDE.md"), []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	mounts := []Mount{{Host: filepath.Join(src, "CLAUDE.md"), Box: boxHome + "/.claude/CLAUDE.md"}}
	dir, err := m.ensureStateDir("ab-box-cccccccccccc", mounts)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, ".claude/CLAUDE.md")
	if err := os.WriteFile(p, []byte("written by the agent"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ensureStateDir("ab-box-cccccccccccc", mounts); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if string(raw) != "written by the agent" {
		t.Fatalf("mountpoint content was clobbered: %q", raw)
	}
}

// A host path that does not exist yields no mount from the bundle layer, but the
// state dir must survive one arriving anyway rather than failing the spawn.
func TestMountpointForAMissingHostPathIsAFileStub(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	dir, err := m.ensureStateDir("ab-box-dddddddddddd",
		[]Mount{{Host: "/nonexistent/agents", Box: boxHome + "/.claude/agents"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude/agents")); err != nil {
		t.Fatalf("a mountpoint must exist even when the host side does not: %v", err)
	}
}

// The box owns every byte of its own home, so it can plant a symlink where the
// NEXT spawn expects to create a mountpoint — a boxed shell needs no bundle
// mounts, so it can cold-start the workspace's box, do exactly this, and idle
// out before a boxed claude re-enters ensureStateDir. Resolving that path with
// os.MkdirAll would have the daemon create directories at a host path the box
// chose. Every write below the box home therefore goes through an os.Root
// anchored at it, the same containment os.Root gives $HOME in internal/web:
// nothing is created outside, and the spawn fails loudly instead.
func TestMountpointIsNotCreatedThroughASymlinkOutOfTheBoxHome(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	name := "ab-box-555555555555"
	dir := m.stateDir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir() // stands in for any daemon-writable host path
	if err := os.Symlink(outside, filepath.Join(dir, ".claude")); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := m.ensureStateDir(name, []Mount{
		{Host: filepath.Join(src, "skills"), Box: boxHome + "/.claude/skills"},
	})
	if err == nil {
		t.Error("ensureStateDir walked a symlink out of the box home instead of refusing")
	}
	if _, err := os.Stat(filepath.Join(outside, "skills")); err == nil {
		t.Fatal("a mountpoint was created outside the state root")
	}
}

// Same defect on the seed path: a DANGLING symlink at .claude.json makes a
// following Stat report "missing", and the write then lands on the symlink's
// target. Nothing may be written outside the home; an existing name — symlink
// included — is state the box already has, so it is left alone.
func TestSeedIsNotWrittenThroughASymlinkOutOfTheBoxHome(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	name := "ab-box-666666666666"
	dir := m.stateDir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	target := filepath.Join(outside, "stolen.json")
	if err := os.Symlink(target, filepath.Join(dir, ".claude.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ensureStateDir(name, nil); err != nil {
		t.Fatalf("an existing name is not an error, it is state to leave alone: %v", err)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("the onboarding seed was written outside the state root")
	}
}

// The marker records genuine use. It must be touched on a warm start too — a box
// that stays up for weeks would otherwise look untouched to the reclaim sweep.
func TestSpawnTouchesTheLastUsedMarker(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	dir := t.TempDir()
	if _, _, _, err := m.Wrap(context.Background(), dir, "bash-1", []string{"claude"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	marker := m.markerPath(mustBoxName(t, m, dir))
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(marker, old, old); err != nil {
		t.Fatalf("the first spawn must create the marker: %v", err)
	}
	// Second spawn: the box is already running, so this is the warm path.
	if _, _, _, err := m.Wrap(context.Background(), dir, "bash-2", []string{"claude"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(fi.ModTime()) > time.Minute {
		t.Fatalf("a warm start left the marker stale: %v", fi.ModTime())
	}
}

// The box home is mounted as the agent's $HOME. Bookkeeping the host's reclaim
// depends on does not belong inside the blast radius of the contained thing.
func TestTheMarkerLivesOutsideTheBoxHome(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	dir := t.TempDir()
	if _, _, _, err := m.Wrap(context.Background(), dir, "bash-1", []string{"claude"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	name := mustBoxName(t, m, dir)
	home := m.stateDir(name)
	marker := m.markerPath(name)
	if strings.HasPrefix(marker, home+string(filepath.Separator)) {
		t.Fatalf("marker %q is inside the box home %q", marker, home)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker not written: %v", err)
	}
}
