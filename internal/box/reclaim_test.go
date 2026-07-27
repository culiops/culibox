package box

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	dir := seedHome(t, m, "ac-box-aaaaaaaaaaaa", 40*24*time.Hour)
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || done[0].Box != "ac-box-aaaaaaaaaaaa" {
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
	if _, err := os.Stat(m.markerPath("ac-box-aaaaaaaaaaaa")); !os.IsNotExist(err) {
		t.Fatalf("marker outlived its home: %v", err)
	}
}

func TestReclaimKeepsAHomeYoungerThanTheTTL(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	dir := seedHome(t, m, "ac-box-bbbbbbbbbbbb", 3*24*time.Hour)
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
	dir := seedHome(t, m, "ac-box-cccccccccccc", 90*24*time.Hour)
	m.acquire("ac-box-cccccccccccc")
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
	dir := seedHome(t, m, "ac-box-dddddddddddd", 90*24*time.Hour)
	fake.existing = []string{"ac-box-dddddddddddd"}
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
	dir := seedHome(t, m, "ac-box-333333333333", 90*24*time.Hour)
	fake.running["ac-box-333333333333"] = true // Inspect must say so; existing stays empty
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

// The sweep must never generalise to "everything under boxstate".
func TestReclaimIgnoresEntriesThatAreNotBoxHomes(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	root := filepath.Join(m.opts.StateRoot, stateDirName)
	for _, name := range []string{"notabox", "ac-box-short", "ac-box-zzzzzzzzzzzz", lastUsedDirName} {
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
	for _, name := range []string{"notabox", "ac-box-short", "ac-box-zzzzzzzzzzzz", lastUsedDirName} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("%s was removed: %v", name, err)
		}
	}
}

// Homes created before the marker existed still have to age out.
func TestReclaimFallsBackToDirectoryMtimeWithNoMarker(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	name := "ac-box-eeeeeeeeeeee"
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
	stuck := seedHome(t, m, "ac-box-ffffffffffff", 90*24*time.Hour)
	ok := seedHome(t, m, "ac-box-111111111111", 90*24*time.Hour)
	locked := filepath.Join(stuck, ".npm")
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	done, err := m.ReclaimState(context.Background(), DefaultStateTTL)
	if err == nil {
		t.Fatal("an unremovable home must be reported")
	}
	if !strings.Contains(err.Error(), "ac-box-ffffffffffff") {
		t.Fatalf("the error must name the directory: %v", err)
	}
	if len(done) != 1 || done[0].Box != "ac-box-111111111111" {
		t.Fatalf("the removable home was skipped: %+v", done)
	}
	if _, err := os.Stat(ok); !os.IsNotExist(err) {
		t.Fatalf("removable home survived: %v", err)
	}
}

func TestReclaimDropsMarkersWhoseHomeIsGone(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	m.touchMarker("ac-box-222222222222") // never had a home
	if _, err := m.ReclaimState(context.Background(), DefaultStateTTL); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.markerPath("ac-box-222222222222")); !os.IsNotExist(err) {
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
