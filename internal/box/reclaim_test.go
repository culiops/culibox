package box

import (
	"context"
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
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL)
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
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL)
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
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL)
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
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL)
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
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL)
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
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL)
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
		_, err := m.ReclaimState(context.Background(), DefaultStateTTL)
		swept <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the sweep never reached its re-check")
	}

	spawned := make(chan error, 1)
	go func() {
		_, _, _, err := m.Wrap(context.Background(), ws, "bash-1", "", []string{"claude"}, nil, nil)
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
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL)
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
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL)
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

	done, err := m.ReclaimState(context.Background(), DefaultStateTTL)
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
	if _, err := m.ReclaimState(context.Background(), DefaultStateTTL); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.markerPath("ab-box-222222222222")); !os.IsNotExist(err) {
		t.Fatalf("orphan marker kept: %v", err)
	}
}

func TestReclaimOnAnUnusedInstanceIsNotAnError(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL)
	if err != nil {
		t.Fatalf("a daemon that has never boxed anything must not error: %v", err)
	}
	if len(done) != 0 {
		t.Fatalf("reclaimed something from an empty state root: %+v", done)
	}
}
