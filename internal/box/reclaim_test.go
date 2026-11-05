package box

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// seedHome creates a box home with one byte of content and backdates its marker.
func seedHome(t *testing.T, m *Manager, name string, age time.Duration) string {
	t.Helper()
	dir := m.stateDir(name)
	if err := os.MkdirAll(filepath.Join(dir, ".npm"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".npm", "blob"), []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.touchMarker(name)
	when := time.Now().Add(-age)
	if err := os.Chtimes(m.markerPath(name), when, when); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir, when, when); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReclaimRemovesAnUnusedHomePastTheTTL(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	dir := seedHome(t, m, "ab-box-aaaaaaaaaaaa", 40*24*time.Hour)
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || done[0].Box != "ab-box-aaaaaaaaaaaa" {
		t.Fatalf("expected one reclaim, got %+v", done)
	}
	if done[0].Bytes != 10 {
		t.Fatalf("reported %d bytes, want 10", done[0].Bytes)
	}
	if done[0].Age < 39*24*time.Hour {
		t.Fatalf("age %v looks wrong", done[0].Age)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("home still present: %v", err)
	}
	if _, err := os.Stat(m.markerPath("ab-box-aaaaaaaaaaaa")); !os.IsNotExist(err) {
		t.Fatalf("marker outlived its home: %v", err)
	}
}

func TestReclaimKeepsAHomeYoungerThanTheTTL(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	dir := seedHome(t, m, "ab-box-bbbbbbbbbbbb", 3*24*time.Hour)
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 0 {
		t.Fatalf("reclaimed a fresh home: %+v", done)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("fresh home removed: %v", err)
	}
}

// Age is never enough on its own. A live session's home must survive even when
// its marker is ancient — that is the case a wrong sweep destroys.
func TestReclaimKeepsAHomeWithALiveSession(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	dir := seedHome(t, m, "ab-box-cccccccccccc", 90*24*time.Hour)
	m.acquire("ab-box-cccccccccccc")
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 0 {
		t.Fatalf("reclaimed a home with a live session: %+v", done)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("live session's home removed: %v", err)
	}
}

func TestReclaimKeepsAHomeWhoseContainerStillExists(t *testing.T) {
	m, fake := newTestManagerWithFake(t)
	dir := seedHome(t, m, "ab-box-dddddddddddd", 90*24*time.Hour)
	fake.existing = []string{"ab-box-dddddddddddd"}
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 0 {
		t.Fatalf("reclaimed a home whose container still exists: %+v", done)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("home removed under a surviving container: %v", err)
	}
}

// A box can spawn and release between the ListByLabel snapshot and the
// removal, leaving `active` back at 0 while its container is still up
// awaiting its idle timer. The Inspect re-check just before removal is what
// catches that, not the earlier snapshot — this simulates it directly rather
// than racing real goroutines, which would be flaky.
func TestReclaimSkipsABoxThatCameBackDuringTheSweep(t *testing.T) {
	m, fake := newTestManagerWithFake(t)
	dir := seedHome(t, m, "ab-box-333333333333", 90*24*time.Hour)
	fake.running["ab-box-333333333333"] = true // Inspect must say so; existing stays empty
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 0 {
		t.Fatalf("reclaimed a box that came back during the sweep: %+v", done)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("home of a box that came back was removed: %v", err)
	}
}

// The age that admits a candidate is read before opMu. A spawn landing in that
// window touches the marker, and if the container it started is then removed
// from outside (docker rm -f), the Inspect re-check says "gone" and the sweep
// would delete a home used seconds ago on a month-old reading. The age has to be
// re-read under the lock, like `active` and Inspect already are.
func TestReclaimRereadsTheAgeUnderTheLock(t *testing.T) {
	m, fake := newTestManagerWithFake(t)
	name := "ab-box-444444444444"
	dir := seedHome(t, m, name, 90*24*time.Hour)
	// Inspect runs inside reclaimOne, under opMu, after the snapshot: a spawn
	// landing exactly here is what touching the marker stands for. It stays
	// "not running", so nothing else stops the removal.
	fake.inspectHook = func(n string) {
		if n == name {
			m.touchMarker(n)
		}
	}
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 0 {
		t.Fatalf("reclaimed a home used during the sweep: %+v", done)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("home of a box used during the sweep was removed: %v", err)
	}
}

// The removal must be serialized against `ensure`, which bind-mounts this exact
// directory as a container's $HOME: a RemoveAll racing that mount deletes files
// out from under a live box. Only opMu provides that, and only an ordering test
// pins it — the Inspect re-check alone leaves the suite green when the lock is
// deleted. Every wait is bounded, so a regression fails rather than hangs.
func TestReclaimHoldsOpMuAgainstAConcurrentSpawn(t *testing.T) {
	m, fake := newTestManagerWithFake(t)
	ws := t.TempDir()
	name := mustBoxName(t, m, ws)
	seedHome(t, m, name, 90*24*time.Hour)

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	// Only the sweep's own Inspect blocks. If the lock is missing, the spawn
	// reaches Inspect too and must not be held up by this.
	fake.inspectHook = func(string) {
		first := false
		once.Do(func() { first = true })
		if first {
			close(entered)
			<-release
		}
	}

	swept := make(chan error, 1)
	go func() {
		_, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL)
		swept <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the sweep never reached its re-check")
	}

	spawned := make(chan error, 1)
	go func() {
		_, _, _, err := m.Wrap(context.Background(), ws, "bash-1", BoxKey{}, []string{"claude"}, nil, nil)
		spawned <- err
	}()
	select {
	case err := <-spawned:
		close(release)
		t.Fatalf("a spawn completed while the sweep was mid-removal (err=%v): "+
			"reclaimOne is not serialized against ensure, so RemoveAll can race a live bind mount", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-spawned:
		if err != nil {
			t.Fatalf("spawn failed after the sweep released the lock: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the spawn never completed after the sweep released the lock")
	}
	select {
	case err := <-swept:
		if err != nil {
			t.Fatalf("sweep failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the sweep never finished")
	}
}

// The sweep must never generalise to "everything under boxstate".
func TestReclaimIgnoresEntriesThatAreNotBoxHomes(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	root := filepath.Join(m.opts.StateRoot, stateDirName)
	for _, name := range []string{"notabox", "ab-box-short", "ab-box-zzzzzzzzzzzz", lastUsedDirName} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-365 * 24 * time.Hour)
		if err := os.Chtimes(filepath.Join(root, name), when, when); err != nil {
			t.Fatal(err)
		}
	}
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 0 {
		t.Fatalf("swept a non-box entry: %+v", done)
	}
	for _, name := range []string{"notabox", "ab-box-short", "ab-box-zzzzzzzzzzzz", lastUsedDirName} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("%s was removed: %v", name, err)
		}
	}
}

// Homes created before the marker existed still have to age out.
func TestReclaimFallsBackToDirectoryMtimeWithNoMarker(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	name := "ab-box-eeeeeeeeeeee"
	dir := seedHome(t, m, name, 90*24*time.Hour)
	if err := os.Remove(m.markerPath(name)); err != nil {
		t.Fatal(err)
	}
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 {
		t.Fatalf("a markerless old home must still be reclaimed: %+v", done)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("home still present: %v", err)
	}
}

// A root-owned leftover from before the mountpoint fix cannot be removed by this
// user. It must be reported, and it must not stop the rest of the sweep.
func TestOneUnremovableHomeDoesNotStopTheSweep(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission failure cannot be simulated")
	}
	m, _ := newTestManagerWithFake(t)
	stuck := seedHome(t, m, "ab-box-ffffffffffff", 90*24*time.Hour)
	ok := seedHome(t, m, "ab-box-111111111111", 90*24*time.Hour)
	locked := filepath.Join(stuck, ".npm")
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL)
	if err == nil {
		t.Fatal("an unremovable home must be reported")
	}
	if !strings.Contains(err.Error(), "ab-box-ffffffffffff") {
		t.Fatalf("the error must name the directory: %v", err)
	}
	if len(done) != 1 || done[0].Box != "ab-box-111111111111" {
		t.Fatalf("the removable home was skipped: %+v", done)
	}
	if _, err := os.Stat(ok); !os.IsNotExist(err) {
		t.Fatalf("removable home survived: %v", err)
	}
}

func TestReclaimDropsMarkersWhoseHomeIsGone(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	m.touchMarker("ab-box-222222222222") // never had a home
	if _, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.markerPath("ab-box-222222222222")); !os.IsNotExist(err) {
		t.Fatalf("orphan marker kept: %v", err)
	}
}

func TestReclaimOnAnUnusedInstanceIsNotAnError(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL)
	if err != nil {
		t.Fatalf("a daemon that has never boxed anything must not error: %v", err)
	}
	if len(done) != 0 {
		t.Fatalf("reclaimed something from an empty state root: %+v", done)
	}
}

// The out dir lives beside the home, not inside it, so removing the home alone
// would leave every reclaimed box's screenshots behind forever.
func TestReclaimRemovesTheOutputDirToo(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	const name = "ab-box-eeeeeeeeeeee"
	seedHome(t, m, name, 40*24*time.Hour)
	leaf, err := m.prepareOutputDir(name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leaf, "shot.png"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL)
	if err != nil || len(done) != 1 {
		t.Fatalf("want one reclaim, got %+v, %v", done, err)
	}
	if _, err := os.Stat(filepath.Dir(leaf)); !os.IsNotExist(err) {
		t.Fatalf("out dir outlived its box: %v", err)
	}
}

func TestReclaimKeepsTheOutputDirOfALiveBox(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	const name = "ab-box-ffffffffffff"
	seedHome(t, m, name, 90*24*time.Hour)
	leaf, err := m.prepareOutputDir(name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	m.acquire(name)
	if done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL); err != nil || len(done) != 0 {
		t.Fatalf("reclaimed a live box: %+v, %v", done, err)
	}
	if _, err := os.Stat(leaf); err != nil {
		t.Fatalf("a live box's out dir was removed: %v", err)
	}
}

// The broker keeps one socket directory per box under <stateRoot>/broker/b. A
// running box mounts it, and a directory that was removed and recreated would
// not be visible through that bind mount, so the home sweep must never touch it,
// even for the box whose home it removes.
func TestReclaimNeverRemovesTheBrokerSocketDirs(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	const name = "ab-box-aaaaaaaaaaaa"
	home := seedHome(t, m, name, 40*24*time.Hour)
	sockDir := filepath.Join(m.opts.StateRoot, "broker", "b", "aaaaaaaaaaaa")
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sockDir, "s"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 {
		t.Fatalf("reclaimed %+v, want the one stale home", done)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("the stale home is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sockDir, "s")); err != nil {
		t.Fatalf("the sweep removed the box's broker socket directory: %v", err)
	}
}

// wantCachePaths is the allowlist, spelled out so a path added to the code does
// not silently widen what these tests protect.
var wantCachePaths = []string{".cache/go-build", "go/pkg/mod", ".npm", ".claude/security/agent-sdk-venv"}

// seedCaches fills every allowlisted cache path of a home with one 10-byte file
// (so a full prune frees 40 bytes) and adds .claude/keep, which no prune may
// touch. Creating the paths bumps the home's mtime, which is the age of a home
// with no marker, so it is put back.
func seedCaches(t *testing.T, dir string) {
	t.Helper()
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range wantCachePaths {
		writeFile(t, filepath.Join(dir, p, "blob"), "0123456789")
	}
	writeFile(t, filepath.Join(dir, ".claude", "keep"), "0123456789")
	if err := os.Chtimes(dir, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
}

func mustExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); err != nil {
		t.Fatalf("%s should still be there: %v", p, err)
	}
}

func mustBeGone(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Fatalf("%s should be gone: %v", p, err)
	}
}

func TestPruneDropsCachesOfADeadHomeButKeepsClaude(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	const name = "ab-box-aaaaaaaaaaaa"
	dir := seedHome(t, m, name, 2*time.Hour)
	seedCaches(t, dir)
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultCacheTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || done[0].Box != name || !done[0].Caches || done[0].Bytes != 40 {
		t.Fatalf("want one cache prune of 40 bytes, got %+v", done)
	}
	if done[0].Age < 2*time.Hour-time.Minute || done[0].Age > 3*time.Hour {
		t.Fatalf("age %v looks wrong", done[0].Age)
	}
	for _, p := range wantCachePaths {
		mustBeGone(t, filepath.Join(dir, p))
	}
	mustExist(t, filepath.Join(dir, ".claude", "keep"))
	mustExist(t, dir)
	mustExist(t, m.markerPath(name))
	// The prune is not a use: the marker keeps saying when the box was last used,
	// so .claude still ages out 30 days after that, not after the prune.
	if age, ok := m.homeAge(name); !ok || age < 2*time.Hour-time.Minute {
		t.Fatalf("the prune reset the home's age to %v", age)
	}
}

func TestPruneKeepsCachesOfAHomeUsedWithinTheHour(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	dir := seedHome(t, m, "ab-box-bbbbbbbbbbbb", 30*time.Minute)
	seedCaches(t, dir)
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultCacheTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 0 {
		t.Fatalf("pruned a home used 30 minutes ago: %+v", done)
	}
	for _, p := range wantCachePaths {
		mustExist(t, filepath.Join(dir, p, "blob"))
	}
}

// The same three grounds that protect a whole home protect its caches: a
// session counted against it, a container that still exists, a container that
// Inspect says is running or cannot say about.
func TestPruneKeepsCachesOfALiveOrRunningBox(t *testing.T) {
	const name = "ab-box-cccccccccccc"
	for _, tc := range []struct {
		label string
		arm   func(m *Manager, f *fakeClient)
	}{
		{"live session", func(m *Manager, _ *fakeClient) { m.acquire(name) }},
		{"container listed", func(_ *Manager, f *fakeClient) { f.existing = []string{name} }},
		{"container running", func(_ *Manager, f *fakeClient) { f.running[name] = true }},
		{"inspect fails", func(_ *Manager, f *fakeClient) { f.inspectErr = errors.New("daemon unreachable") }},
	} {
		t.Run(tc.label, func(t *testing.T) {
			m, f := newTestManagerWithFake(t)
			dir := seedHome(t, m, name, 5*time.Hour)
			seedCaches(t, dir)
			tc.arm(m, f)
			done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultCacheTTL)
			if err != nil {
				t.Fatal(err)
			}
			if len(done) != 0 {
				t.Fatalf("pruned the caches of a box in use: %+v", done)
			}
			for _, p := range wantCachePaths {
				mustExist(t, filepath.Join(dir, p, "blob"))
			}
		})
	}
}

// Same hole as TestReclaimRereadsTheAgeUnderTheLock: the age that admits a
// candidate is read before opMu, and a spawn landing after it must keep its
// warm caches even though its container is already gone again.
func TestPruneRereadsTheAgeUnderTheLock(t *testing.T) {
	m, fake := newTestManagerWithFake(t)
	const name = "ab-box-444444444444"
	dir := seedHome(t, m, name, 5*time.Hour)
	seedCaches(t, dir)
	fake.inspectHook = func(n string) {
		if n == name {
			m.touchMarker(n)
		}
	}
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultCacheTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 0 {
		t.Fatalf("pruned the caches of a home used during the sweep: %+v", done)
	}
	for _, p := range wantCachePaths {
		mustExist(t, filepath.Join(dir, p, "blob"))
	}
}

// A session that starts after the snapshot but before the sweep gets the lock
// must keep its caches: the count is read again under opMu, not trusted from the
// snapshot. The test holds opMu itself so the sweep is parked between the two.
func TestPruneRechecksTheSessionCountUnderTheLock(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	const name = "ab-box-555555555555"
	dir := seedHome(t, m, name, 5*time.Hour)
	seedCaches(t, dir)

	m.opMu.Lock()
	swept := make(chan []Reclaimed, 1)
	go func() {
		done, _ := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultCacheTTL)
		swept <- done
	}()
	time.Sleep(100 * time.Millisecond) // the sweep is now waiting for opMu
	m.acquire(name)
	m.opMu.Unlock()
	select {
	case done := <-swept:
		if len(done) != 0 {
			t.Fatalf("pruned the caches of a box a session started in: %+v", done)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the sweep never finished")
	}
	for _, p := range wantCachePaths {
		mustExist(t, filepath.Join(dir, p, "blob"))
	}
}

// Like TestReclaimHoldsOpMuAgainstAConcurrentSpawn: `ensure` bind-mounts the home
// as a container's $HOME under opMu, and a prune running beside it would delete
// caches out from under a live box.
func TestPruneHoldsOpMuAgainstAConcurrentSpawn(t *testing.T) {
	m, fake := newTestManagerWithFake(t)
	ws := t.TempDir()
	name := mustBoxName(t, m, ws)
	seedCaches(t, seedHome(t, m, name, 5*time.Hour))

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	fake.inspectHook = func(string) {
		first := false
		once.Do(func() { first = true })
		if first {
			close(entered)
			<-release
		}
	}
	swept := make(chan error, 1)
	go func() {
		_, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultCacheTTL)
		swept <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the sweep never reached its re-check")
	}
	spawned := make(chan error, 1)
	go func() {
		_, _, _, err := m.Wrap(context.Background(), ws, "bash-1", BoxKey{}, []string{"claude"}, nil, nil)
		spawned <- err
	}()
	select {
	case err := <-spawned:
		close(release)
		t.Fatalf("a spawn completed while the sweep was mid-prune (err=%v): pruneOne is not serialized against ensure", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	for _, c := range []chan error{spawned, swept} {
		select {
		case err := <-c:
			if err != nil {
				t.Fatalf("failed after the lock was released: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("never finished after the lock was released")
		}
	}
}

// os.Root alone follows a symlink that stays inside the root (measured: removing
// ".x/sub" with ".x -> .claude" deleted .claude/sub), so a link anywhere in an
// allowlisted path must be refused by the pruner itself, never followed.
func TestPruneDoesNotFollowSymlinks(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	outside := t.TempDir()

	// .cache -> .claude: .cache/go-build would resolve to .claude/go-build.
	a := seedHome(t, m, "ab-box-aaaaaaaaaaaa", 5*time.Hour)
	seedCaches(t, a)
	if err := os.RemoveAll(filepath.Join(a, ".cache")); err != nil {
		t.Fatal(err)
	}
	symlink(t, ".claude", filepath.Join(a, ".cache"))
	writeFile(t, filepath.Join(a, ".claude", "go-build", "keep"), "x")

	// go -> a directory outside the home that has pkg/mod/keep.
	b := seedHome(t, m, "ab-box-bbbbbbbbbbbb", 5*time.Hour)
	seedCaches(t, b)
	if err := os.RemoveAll(filepath.Join(b, "go")); err != nil {
		t.Fatal(err)
	}
	outB := filepath.Join(outside, "b")
	writeFile(t, filepath.Join(outB, "pkg", "mod", "keep"), "x")
	symlink(t, outB, filepath.Join(b, "go"))

	// .npm itself is a link to a directory outside the home.
	c := seedHome(t, m, "ab-box-cccccccccccc", 5*time.Hour)
	seedCaches(t, c)
	if err := os.RemoveAll(filepath.Join(c, ".npm")); err != nil {
		t.Fatal(err)
	}
	outC := filepath.Join(outside, "c")
	writeFile(t, filepath.Join(outC, "keep"), "x")
	symlink(t, outC, filepath.Join(c, ".npm"))

	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultCacheTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 3 {
		t.Fatalf("want the three homes pruned of their real caches, got %+v", done)
	}
	mustExist(t, filepath.Join(a, ".claude", "go-build", "keep"))
	mustExist(t, filepath.Join(outB, "pkg", "mod", "keep"))
	mustExist(t, filepath.Join(outC, "keep"))
	// The real directories of the same homes are still pruned.
	for _, dir := range []string{a, b, c} {
		mustBeGone(t, filepath.Join(dir, ".claude", "security", "agent-sdk-venv"))
		mustExist(t, filepath.Join(dir, ".claude", "keep"))
	}
	mustBeGone(t, filepath.Join(a, ".npm"))
	mustBeGone(t, filepath.Join(b, ".cache", "go-build"))
	mustBeGone(t, filepath.Join(c, "go", "pkg", "mod"))
}

// Before -modcacherw (and in any box that overrides GOFLAGS) the Go module cache
// is read-only: 0555 directories and 0444 files, which a plain RemoveAll cannot
// delete.
func TestPruneRemovesAReadOnlyModuleCache(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only tree is removable anyway")
	}
	m, _ := newTestManagerWithFake(t)
	dir := seedHome(t, m, "ab-box-dddddddddddd", 5*time.Hour)
	mod := filepath.Join(dir, "go", "pkg", "mod")
	leaf := filepath.Join(mod, "example.com", "x@v1", "sub")
	writeFile(t, filepath.Join(leaf, "f.go"), "package x")
	// If the test fails the tree is still read-only, and t.TempDir's cleanup
	// would fail on top of it.
	t.Cleanup(func() {
		_ = filepath.WalkDir(mod, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0o755)
			}
			return nil
		})
	})
	if err := os.Chmod(filepath.Join(leaf, "f.go"), 0o444); err != nil {
		t.Fatal(err)
	}
	// Children first, so every chmod still has a writable path to its target.
	for p := leaf; len(p) >= len(mod); p = filepath.Dir(p) {
		if err := os.Chmod(p, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultCacheTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || !done[0].Caches || done[0].Bytes < 9 {
		t.Fatalf("want one cache prune, got %+v", done)
	}
	mustBeGone(t, mod)
}

// A home from before the marker existed has no record of its last use but its
// own mtime, and unlinking .npm from it bumps exactly that. A prune must not
// make such a home look used.
func TestPruneKeepsTheAgeOfAHomeWithNoMarker(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	const name = "ab-box-eeeeeeeeeeee"
	dir := seedHome(t, m, name, 2*time.Hour)
	if err := os.Remove(m.markerPath(name)); err != nil {
		t.Fatal(err)
	}
	seedCaches(t, dir)
	before, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultCacheTTL)
	if err != nil || len(done) != 1 {
		t.Fatalf("want one prune, got %+v, %v", done, err)
	}
	mustBeGone(t, filepath.Join(dir, ".npm"))
	after, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d := after.ModTime().Sub(before.ModTime()); d < -time.Second || d > time.Second {
		t.Fatalf("the prune moved the home's mtime by %v: its age is no longer when it was last used", d)
	}
	if again, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultCacheTTL); err != nil || len(again) != 0 {
		t.Fatalf("a second pass found something to do: %+v, %v", again, err)
	}
}

func TestPruneReportsNothingForAnAlreadyPrunedHome(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	dir := seedHome(t, m, "ab-box-ffffffffffff", 2*time.Hour)
	seedCaches(t, dir)
	if first, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultCacheTTL); err != nil || len(first) != 1 {
		t.Fatalf("first pass: %+v, %v", first, err)
	}
	second, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultCacheTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("the second pass reported %+v", second)
	}
	mustExist(t, filepath.Join(dir, ".claude", "keep"))
}

// One path that will not go must not keep the others, and what did go is still
// reported next to the error.
func TestPruneOneStuckPathDoesNotStopTheOthers(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission failure cannot be simulated")
	}
	m, _ := newTestManagerWithFake(t)
	dir := seedHome(t, m, "ab-box-111111111111", 2*time.Hour)
	seedCaches(t, dir)
	// .npm is a direct child of the home, and a home this user cannot write to
	// does not let it unlink .npm. The pruner chmods the directories under a
	// cache path, never the home itself.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultCacheTTL)
	if err == nil || !strings.Contains(err.Error(), "ab-box-111111111111") {
		t.Fatalf("a path that cannot be removed must be reported by box name: %v", err)
	}
	if len(done) != 1 || !done[0].Caches {
		t.Fatalf("what did go must still be reported, got %+v", done)
	}
	mustBeGone(t, filepath.Join(dir, ".claude", "security", "agent-sdk-venv"))
	mustBeGone(t, filepath.Join(dir, "go", "pkg", "mod"))
	mustBeGone(t, filepath.Join(dir, ".cache", "go-build"))
	mustExist(t, filepath.Join(dir, ".claude", "keep"))
}

// Past the TTL the whole home goes, as before; pruning is only for the stretch
// between the cache grace and the TTL.
func TestReclaimPastTheTTLStillRemovesTheWholeHome(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	const name = "ab-box-999999999999"
	dir := seedHome(t, m, name, 40*24*time.Hour)
	seedCaches(t, dir)
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultCacheTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || done[0].Box != name || done[0].Caches {
		t.Fatalf("want one whole-home reclaim, got %+v", done)
	}
	mustBeGone(t, dir)
	mustBeGone(t, m.markerPath(name))
}

// cacheTTL >= ttl is "no cache pruning", which is what every caller that wants
// the old behaviour passes.
func TestPruneIsOffWhenTheCacheTTLIsNotShorterThanTheTTL(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	dir := seedHome(t, m, "ab-box-888888888888", 5*time.Hour)
	seedCaches(t, dir)
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL)
	if err != nil || len(done) != 0 {
		t.Fatalf("pruned with the prune disabled: %+v, %v", done, err)
	}
	for _, p := range wantCachePaths {
		mustExist(t, filepath.Join(dir, p, "blob"))
	}
	// A longer cacheTTL must not postpone the removal of a home past ttl either.
	old := seedHome(t, m, "ab-box-777777777777", 40*24*time.Hour)
	seedCaches(t, old)
	done, err = m.ReclaimState(context.Background(), DefaultStateTTL, 2*DefaultStateTTL)
	if err != nil || len(done) != 1 || done[0].Caches {
		t.Fatalf("want the 40-day home removed whole, got %+v, %v", done, err)
	}
	mustBeGone(t, old)
	mustExist(t, dir)
}
