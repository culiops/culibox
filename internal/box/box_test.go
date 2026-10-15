package box

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	for _, forbidden := range []string{"docker.sock", "/home/claude/.ssh", "/home/claude/.claude,"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("run args must never reference %q: %q", forbidden, joined)
		}
	}
	if !containsPair(got, "--mount", "type=bind,src=/home/claude/proj,dst=/home/claude/proj") {
		t.Fatalf("workspace must bind at its identical host path: %q", joined)
	}
	if n := countFlag(got, "--mount"); n != 2 {
		t.Fatalf("expected exactly 2 mounts (workspace + state), got %d: %q", n, joined)
	}
	if countFlag(got, "-v") != 0 {
		t.Fatalf("-v silently creates a missing source inside a VM; use --mount: %q", joined)
	}
}

func TestExtraMountsAreReadOnlyBinds(t *testing.T) {
	m := newTestManager(t)
	got := m.runArgs("ab-box-abc", "/home/claude/proj", []Mount{{Host: "/data/bundle", Box: "/opt/agent-bundle"}})
	if !containsPair(got, "--mount", "type=bind,src=/data/bundle,dst=/opt/agent-bundle,readonly") {
		t.Fatalf("extra mounts must be readonly binds: %q", strings.Join(got, " "))
	}
}

// docker parses --mount as CSV: a comma in a path starts a new option, so a
// workspace named "x,dst=/etc" would rewrite the mount. Refuse, never escape.
func TestWorkspaceWithMountMetacharactersIsRefused(t *testing.T) {
	m, fake := newTestManagerWithFake(t)
	for _, name := range []string{"a,b", `q"uote`, "nl\nx", "nel\u0085x", "ls\u2028x", "ps\u2029x", "c1\u009bx", "bad\xffutf8"} {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		_, _, _, err := m.Wrap(context.Background(), dir, "s-1", []string{"claude"}, nil, nil)
		if !errors.Is(err, ErrUnboxableWorkspace) {
			t.Fatalf("%q: want ErrUnboxableWorkspace, got %v", name, err)
		}
	}
	if fake.runs() != 0 {
		t.Fatal("no docker call may happen for a refused path")
	}
}

func TestUnsafeExtraMountSourceFailsTheSpawn(t *testing.T) {
	m, fake := newTestManagerWithFake(t)
	_, _, _, err := m.Wrap(context.Background(), t.TempDir(), "s-1", []string{"claude"}, nil,
		[]Mount{{Host: "/x,y", Box: "/opt/agent-bundle"}})
	if err == nil || fake.runs() != 0 {
		t.Fatalf("an unsafe mount source must fail before docker run: err=%v runs=%d", err, fake.runs())
	}
}

// MountUnsafe also refuses what a YAML/CSV reader may fold into a different
// path: C1 controls (U+0085 NEL), U+2028/U+2029 line separators, invalid UTF-8.
func TestMountUnsafeRefusesLineBreaksControlsAndBadUTF8(t *testing.T) {
	for _, p := range []string{"/a,b", `/a"b`, "/a\nb", "/a\x7fb", "/a\u0080b", "/a\u0085b", "/a\u009bb",
		"/a\u2028b", "/a\u2029b", "/a\xffb"} {
		if MountUnsafe(p) == "" {
			t.Errorf("%q must be refused", p)
		}
	}
}

func TestMountUnsafeAcceptsOrdinaryPaths(t *testing.T) {
	for _, p := range []string{"/home/claude/proj", "/Users/me/My Project", "/srv/a-b_c.d", "/Users/me/projet \u00e9t\u00e9", "/home/x/\u4e2d\u6587"} {
		if r := MountUnsafe(p); r != "" {
			t.Errorf("%q should be safe, got %q", p, r)
		}
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
	if strings.Contains(joined, "ab-state-") {
		t.Fatalf("state must not be a named volume (image ownership breaks other uids): %q", joined)
	}
	var stateMount string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--mount" && strings.Contains(args[i+1], ",dst="+boxHome) {
			stateMount = args[i+1]
		}
	}
	if stateMount == "" {
		t.Fatalf("no state mount onto %s in %q", boxHome, joined)
	}
	host := ""
	for _, kv := range strings.Split(stateMount, ",") {
		if v, ok := strings.CutPrefix(kv, "src="); ok {
			host = v
		}
	}
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

// Measured 2026-10-15 on real docker: an exited container keeps its name, so a
// second `docker run --name` answers Conflict, and every spawn into that
// workspace fails until the daemon restarts. A colima VM restart exits every box.
func TestStoppedBoxIsRemovedAndRecreated(t *testing.T) {
	m, fake := newTestManagerWithFake(t)
	dir := t.TempDir()
	name, err := m.boxName(dir)
	if err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.exited[name] = true
	fake.mu.Unlock()
	if _, _, _, err := m.Wrap(context.Background(), dir, "s-1", []string{"claude"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !fake.stoppedAny(name) {
		t.Fatal("a stopped box must be removed before it is recreated")
	}
	if fake.runs() != 1 {
		t.Fatalf("want 1 docker run after removing the stopped box, got %d", fake.runs())
	}
}

func TestInspectErrorFailsTheSpawnInsteadOfReadingAsMissing(t *testing.T) {
	m, fake := newTestManagerWithFake(t)
	fake.mu.Lock()
	fake.inspectErr = errors.New("Cannot connect to the Docker daemon")
	fake.mu.Unlock()
	if _, _, _, err := m.Wrap(context.Background(), t.TempDir(), "s-1", []string{"claude"}, nil, nil); err == nil {
		t.Fatal("an unreachable daemon must fail the spawn")
	}
	if fake.runs() != 0 {
		t.Fatal("no docker run when inspect could not answer")
	}
}

func TestExecArgvCarriesTheGlobalArgs(t *testing.T) {
	f := newFake()
	m, err := New(Options{DockerBin: "/usr/bin/docker", DockerArgs: []string{"--host", "unix:///v/s"},
		Image: "i", Instance: "T", StateRoot: t.TempDir(), Client: f})
	if err != nil {
		t.Fatal(err)
	}
	got := m.execArgv("ab-box-1", "/w", "s-1", []string{"claude"}, nil)
	if len(got) < 4 || got[0] != "/usr/bin/docker" || got[1] != "--host" || got[2] != "unix:///v/s" || got[3] != "exec" {
		t.Fatalf("exec argv must start docker --host … exec: %q", got)
	}
}

func TestUnderMountIsComponentWise(t *testing.T) {
	ms := []string{"/Users/a"}
	for p, want := range map[string]bool{
		"/Users/a": true, "/Users/a/proj": true, "/Users/ab": false, "/private/tmp/x": false,
	} {
		if got := UnderMount(p, ms); got != want {
			t.Fatalf("UnderMount(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestWorkspaceOutsideSharedDirsIsRefused(t *testing.T) {
	f := newFake()
	shared := t.TempDir()
	m, err := New(Options{DockerBin: "/d", Image: "i", Instance: "T", StateRoot: t.TempDir(), Client: f,
		Shared: []string{shared}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = m.Wrap(context.Background(), t.TempDir(), "s-1", []string{"claude"}, nil, nil)
	if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "shared directories") {
		t.Fatalf("want a shared-dirs refusal, got %v", err)
	}
	if reason, bad := m.Unboxable(t.TempDir()); !bad || !strings.Contains(reason, "shared directories") {
		t.Fatalf("Unboxable must apply the same rule, got %q %v", reason, bad)
	}
	inside := filepath.Join(shared, "proj")
	_ = os.Mkdir(inside, 0o700)
	if _, _, _, err := m.Wrap(context.Background(), inside, "s-1", []string{"claude"}, nil, nil); err != nil {
		t.Fatalf("a workspace under a shared dir must box: %v", err)
	}
}

func TestVerifyFailureRemovesTheNewBoxAndRefuses(t *testing.T) {
	f := newFake()
	m, err := New(Options{DockerBin: "/d", Image: "i", Instance: "T", StateRoot: t.TempDir(), Client: f,
		Verify: func(context.Context, string) error { return fmt.Errorf("%w: reached loopback", ErrBoxUnavailable) }})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = m.Wrap(context.Background(), t.TempDir(), "s-1", []string{"claude"}, nil, nil)
	if !errors.Is(err, ErrBoxUnavailable) {
		t.Fatalf("want ErrBoxUnavailable, got %v", err)
	}
	if f.stops() != 1 {
		t.Fatalf("the unverified box must be removed, stops=%d", f.stops())
	}
}

func TestVerifyRunsOnlyForNewContainers(t *testing.T) {
	f := newFake()
	calls := 0
	m, _ := New(Options{DockerBin: "/d", Image: "i", Instance: "T", StateRoot: t.TempDir(), Client: f,
		Verify: func(context.Context, string) error { calls++; return nil }})
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		if _, _, _, err := m.Wrap(context.Background(), dir, "s", []string{"claude"}, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("Verify must run once per created container, ran %d", calls)
	}
}

func TestPrepareIsAPassThrough(t *testing.T) {
	f := newFake()
	want := fmt.Errorf("%w: run agent-box box setup", ErrBoxUnavailable)
	m, _ := New(Options{DockerBin: "/d", Image: "i", Instance: "T", StateRoot: t.TempDir(), Client: f,
		Prepare: func(context.Context) error { return want }})
	if err := m.Prepare(context.Background()); !errors.Is(err, ErrBoxUnavailable) {
		t.Fatalf("Prepare must return the hook's error, got %v", err)
	}
	m2, _ := New(Options{DockerBin: "/d", Image: "i", Instance: "T", StateRoot: t.TempDir(), Client: f})
	if err := m2.Prepare(context.Background()); err != nil {
		t.Fatalf("a nil hook is a no-op, got %v", err)
	}
}

// A box that failed Verify and could not be removed is still running. The next
// spawn must not reuse it just because Inspect says Running: it is verified
// again, fails again, and is removed; the spawn after that creates a fresh box
// and verifies that.
func TestUnremovedUnverifiedBoxIsNeverReused(t *testing.T) {
	f := newFake()
	verifies := 0
	verifyErr := fmt.Errorf("%w: reached loopback", ErrBoxUnavailable)
	m, _ := New(Options{DockerBin: "/d", Image: "i", Instance: "T", StateRoot: t.TempDir(), Client: f,
		Verify: func(context.Context, string) error {
			verifies++
			if verifies <= 2 {
				return verifyErr
			}
			return nil
		}})
	dir := t.TempDir()
	f.stopFails = 1
	_, _, _, err := m.Wrap(context.Background(), dir, "s-1", []string{"claude"}, nil, nil)
	if !errors.Is(err, ErrBoxUnavailable) || !strings.Contains(err.Error(), "daemon unreachable") {
		t.Fatalf("want ErrBoxUnavailable naming the removal failure, got %v", err)
	}
	if f.runs() != 1 || f.stops() != 1 {
		t.Fatalf("runs=%d stops=%d", f.runs(), f.stops())
	}
	if _, _, _, err := m.Wrap(context.Background(), dir, "s-2", []string{"claude"}, nil, nil); !errors.Is(err, ErrBoxUnavailable) {
		t.Fatalf("the still-running box must be re-verified and refused: %v", err)
	}
	if f.runs() != 1 || f.stops() != 2 || verifies != 2 {
		t.Fatalf("no reuse: runs=%d stops=%d verifies=%d", f.runs(), f.stops(), verifies)
	}
	_, _, name, err := m.Wrap(context.Background(), dir, "s-3", []string{"claude"}, nil, nil)
	if err != nil {
		t.Fatalf("a fresh, verified box must serve the retry: %v", err)
	}
	if f.runs() != 2 || verifies != 3 {
		t.Fatalf("a fresh box must be created and verified: runs=%d verifies=%d", f.runs(), verifies)
	}
	// Normal accounting afterwards: exactly one live session, and the box is
	// kept (not idle-stopped) until it is released.
	m.mu.Lock()
	active := m.active[name]
	m.mu.Unlock()
	if active != 1 {
		t.Fatalf("active sessions = %d, want 1 (the failed Wraps must not leak a count)", active)
	}
	m.ReleaseBox(name)
	m.mu.Lock()
	active = m.active[name]
	m.mu.Unlock()
	if active != 0 {
		t.Fatalf("active after release = %d, want 0", active)
	}
	// Verified once, so a further Wrap reuses it without verifying again.
	if _, _, _, err := m.Wrap(context.Background(), dir, "s-4", []string{"claude"}, nil, nil); err != nil || verifies != 3 {
		t.Fatalf("a verified box is reused unchecked: err=%v verifies=%d", err, verifies)
	}
}

// If the box cannot be removed either, the spawn is refused every time, so the
// box is still never reused; once the daemon recovers the spawn succeeds.
func TestUnremovableUnverifiedBoxRefusesTheSpawn(t *testing.T) {
	f := newFake()
	verifies := 0
	failing := true
	m, _ := New(Options{DockerBin: "/d", Image: "i", Instance: "T", StateRoot: t.TempDir(), Client: f,
		Verify: func(context.Context, string) error {
			verifies++
			if failing {
				return fmt.Errorf("%w: reached loopback", ErrBoxUnavailable)
			}
			return nil
		}})
	dir := t.TempDir()
	f.stopFails = 2
	if _, _, _, err := m.Wrap(context.Background(), dir, "s-1", []string{"claude"}, nil, nil); err == nil {
		t.Fatal("first Wrap must fail Verify")
	}
	_, _, _, err := m.Wrap(context.Background(), dir, "s-2", []string{"claude"}, nil, nil)
	if !errors.Is(err, ErrBoxUnavailable) || !strings.Contains(err.Error(), "docker rm -f") {
		t.Fatalf("want ErrBoxUnavailable naming the removal failure, got %v", err)
	}
	if verifies != 2 || f.runs() != 1 {
		t.Fatalf("no reuse and no new box while the old one cannot be removed: verifies=%d runs=%d", verifies, f.runs())
	}
	m.mu.Lock()
	for n, c := range m.active {
		if c != 0 {
			t.Fatalf("leaked session count on %s: %d", n, c)
		}
	}
	m.mu.Unlock()
	// The daemon recovers and the box now passes its check: it may serve.
	failing = false
	if _, _, _, err := m.Wrap(context.Background(), dir, "s-3", []string{"claude"}, nil, nil); err != nil {
		t.Fatalf("recovery after the daemon returns: %v", err)
	}
}

// A running box this process never verified (it survived a daemon restart) is
// verified before it is reused.
func TestRunningBoxFromBeforeARestartIsVerifiedBeforeReuse(t *testing.T) {
	f := newFake()
	verifies := 0
	var verr error
	m, _ := New(Options{DockerBin: "/d", Image: "i", Instance: "T", StateRoot: t.TempDir(), Client: f,
		Verify: func(context.Context, string) error { verifies++; return verr }})
	dir := t.TempDir()
	name, err := m.boxName(dir)
	if err != nil {
		t.Fatal(err)
	}
	f.running[name] = true
	m.AdoptBox(name)
	if _, _, _, err := m.Wrap(context.Background(), dir, "s-1", []string{"claude"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if verifies != 1 || f.runs() != 0 {
		t.Fatalf("an unverified running box is verified once and reused: verifies=%d runs=%d", verifies, f.runs())
	}
	if _, _, _, err := m.Wrap(context.Background(), dir, "s-2", []string{"claude"}, nil, nil); err != nil || verifies != 1 {
		t.Fatalf("once verified it is not checked again: err=%v verifies=%d", err, verifies)
	}

	// A different box that fails its check is removed and the spawn refused.
	dir2 := t.TempDir()
	name2, _ := m.boxName(dir2)
	f.running[name2] = true
	verr = fmt.Errorf("%w: reached loopback", ErrBoxUnavailable)
	stopsBefore := f.stops()
	if _, _, _, err := m.Wrap(context.Background(), dir2, "s-3", []string{"claude"}, nil, nil); !errors.Is(err, ErrBoxUnavailable) {
		t.Fatalf("want ErrBoxUnavailable, got %v", err)
	}
	if f.stops() != stopsBefore+1 || f.running[name2] || f.runs() != 0 {
		t.Fatalf("the failing box must be removed, not reused: stops=%d running=%v runs=%d", f.stops(), f.running[name2], f.runs())
	}
}

// Removal drops the verified mark, so a re-created container is checked again.
func TestIdleStopForgetsVerification(t *testing.T) {
	f := newFake()
	verifies := 0
	m, _ := New(Options{DockerBin: "/d", Image: "i", Instance: "T", StateRoot: t.TempDir(), Client: f,
		Verify: func(context.Context, string) error { verifies++; return nil }})
	dir := t.TempDir()
	_, _, name, err := m.Wrap(context.Background(), dir, "s-1", []string{"claude"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(name)
	m.stopIfIdle(name)
	if _, _, _, err := m.Wrap(context.Background(), dir, "s-2", []string{"claude"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if verifies != 2 || f.runs() != 2 {
		t.Fatalf("a re-created box must be verified again: verifies=%d runs=%d", verifies, f.runs())
	}
}

// Reconcile can be called by Prepare while another spawn's ensure is verifying a
// box it just created, before that session is counted. It must wait for ensure
// instead of removing the box out from under it.
func TestReconcileWaitsForAnEnsureInFlight(t *testing.T) {
	f := newFake()
	entered := make(chan struct{})
	release := make(chan struct{})
	m, _ := New(Options{DockerBin: "/d", Image: "i", Instance: "T", StateRoot: t.TempDir(), Client: f,
		Verify: func(context.Context, string) error { close(entered); <-release; return nil }})
	dir := t.TempDir()
	name, _ := m.boxName(dir)
	f.existing = []string{name}

	wrapDone := make(chan error, 1)
	go func() {
		_, _, _, err := m.Wrap(context.Background(), dir, "s-1", []string{"claude"}, nil, nil)
		wrapDone <- err
	}()
	<-entered
	recDone := make(chan error, 1)
	go func() { recDone <- m.Reconcile(context.Background()) }()
	select {
	case <-recDone:
		t.Fatal("Reconcile ran while ensure held opMu")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-wrapDone; err != nil {
		t.Fatal(err)
	}
	if err := <-recDone; err != nil {
		t.Fatal(err)
	}
	if f.stops() != 0 {
		t.Fatalf("the box ensure just created must survive Reconcile, stops=%d", f.stops())
	}
}

func TestReconcileTriesEveryOrphanPastAFailedRemoval(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	f.existing = []string{"ab-box-a", "ab-box-b", "ab-box-c"}
	f.stopFails = 1
	err := m.Reconcile(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ab-box-a") {
		t.Fatalf("the failed removal must be reported, got %v", err)
	}
	if f.stops() != 3 {
		t.Fatalf("every orphan must be tried, stops=%d of 3", f.stops())
	}
}

// A workspace that overlaps the data root would hand the box the VM's root-run
// provision script (<root>/vm/agent-box/colima.yaml), lima's SSH key and the
// profiles, so every overlap shape is refused, symlinked paths included.
func TestWorkspaceOverlappingDataRootIsRefused(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "ab")
	inside := filepath.Join(root, "vm", "agent-box")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(inside, link); err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{DockerBin: "/d", Image: "i", Instance: "T", StateRoot: root, Client: newFake()})
	if err != nil {
		t.Fatal(err)
	}
	for name, ws := range map[string]string{
		"equal": root, "ancestor": parent, "descendant": inside, "symlink into it": link,
	} {
		_, _, _, err := m.Wrap(context.Background(), ws, "s-1", []string{"claude"}, nil, nil)
		if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "data directory") ||
			!strings.Contains(err.Error(), ws) {
			t.Errorf("%s: want a data-root refusal naming %q, got %v", name, ws, err)
		}
		if _, bad := m.Unboxable(ws); !bad {
			t.Errorf("%s: Unboxable must apply the same rule", name)
		}
	}
	sibling := filepath.Join(parent, "proj")
	if err := os.Mkdir(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.Wrap(context.Background(), sibling, "s-1", []string{"claude"}, nil, nil); err != nil {
		t.Fatalf("a sibling of the data root must box: %v", err)
	}
	// A data root reached through a symlink is the same directory.
	rootLink := filepath.Join(t.TempDir(), "rootlink")
	if err := os.Symlink(root, rootLink); err != nil {
		t.Fatal(err)
	}
	if reason := ProtectedOverlap(inside, []ProtectedDir{DataDirProtected(rootLink)}); reason == "" {
		t.Fatal("a symlinked data root must still be matched")
	}
	// The ab prefix must not match abc: component-wise, not string prefix.
	abc := filepath.Join(parent, "abc")
	if err := os.Mkdir(abc, 0o700); err != nil {
		t.Fatal(err)
	}
	if reason := ProtectedOverlap(abc, []ProtectedDir{DataDirProtected(root)}); reason != "" {
		t.Fatalf("%s is not inside %s: %q", abc, root, reason)
	}
}
