package analytics

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// #330: the default roots include every profile's conversations folder.
func TestDefaultRootsIncludeConversationsFolders(t *testing.T) {
	got := DefaultRoots("/home/u", "/data")
	want := []string{
		"/home/u/.claude/projects",
		"/data/boxstate/*/.claude/projects",
		"/data/boxstate/.conversations/*",
		"/data/accounts/*/claude/projects",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("DefaultRoots = %q, want %q", got, want)
	}
}

func defaultSvc(t *testing.T, base string) *Service {
	t.Helper()
	svc, err := New(filepath.Join(t.TempDir(), "a.db"), DefaultRoots(filepath.Join(base, "home"), base), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetBoxState(filepath.Join(base, "boxstate"))
	t.Cleanup(func() { svc.Close() })
	return svc
}

// A transcript in a conversations folder is ingested, under its own path.
func TestIngestConversationsFolder(t *testing.T) {
	base := t.TempDir()
	p := filepath.Join(base, "boxstate", ".conversations", "0123456789ab", "-w", "S1.jsonl")
	writeFile(t, p, line1+line2)
	svc := defaultSvc(t, base)
	st, err := svc.Ingest()
	if err != nil || st.Files != 1 || st.Errors != 0 {
		t.Fatalf("st=%+v err=%v", st, err)
	}
	if !recorded(t, svc, p) {
		t.Fatalf("%s not recorded", p)
	}
}

// Adoption moves a transcript from a home into the folder: with its offsets
// carried first, the next pass reads nothing of it again.
func TestCarryOffsetsKeepsAMovedTranscriptCountedOnce(t *testing.T) {
	base := t.TempDir()
	from := filepath.Join(base, "boxstate", "ab-box-000000000000", ".claude", "projects", "-w", "S1.jsonl")
	to := filepath.Join(base, "boxstate", ".conversations", "0123456789ab", "-w", "S1.jsonl")
	writeFile(t, from, line1+line2)
	svc := defaultSvc(t, base)
	if st, err := svc.Ingest(); err != nil || st.Files != 1 {
		t.Fatalf("first pass st=%+v err=%v", st, err)
	}
	if err := svc.CarryOffsets(from, to); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
	st, err := svc.Ingest()
	if err != nil || st.Files != 0 || st.BytesRead != 0 {
		t.Fatalf("the moved transcript was read again: st=%+v err=%v", st, err)
	}
}

// sums is session_rollup's and driver_rollup's total cost.
func sums(t *testing.T, s *Store) (session, drivers float64) {
	t.Helper()
	return scalar(t, s, `SELECT SUM(cost_usd) FROM session_rollup`), scalar(t, s, `SELECT SUM(cost_usd) FROM driver_rollup`)
}

// passWithMove is adoption racing a pass (#330): the pass has loaded its state
// cache, the box layer carries the offsets and moves the transcript, and the
// same pass then reaches it at its new path. inWindow replays the narrow,
// accepted window where the pass loaded its cache just before the carry
// committed, so the cache has no entry for the new path.
func passWithMove(t *testing.T, svc *Service, from, to string, inWindow bool) {
	t.Helper()
	svc.store.beginPass()
	defer svc.store.endPass()
	if err := svc.CarryOffsets(from, to); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
	if inWindow {
		svc.store.stMu.Lock()
		delete(svc.store.states, to)
		svc.store.stMu.Unlock()
	}
	var st IngestStats
	ingestBoxRoot(svc.store, svc.boxState, filepath.Dir(filepath.Dir(to)), svc.classify, &st)
	if st.Errors != 0 {
		t.Fatalf("pass errors: %+v", st)
	}
}

func movePaths(base string) (from, to string) {
	return filepath.Join(base, "boxstate", "ab-box-000000000000", ".claude", "projects", "-w", "S1.jsonl"),
		filepath.Join(base, "boxstate", ".conversations", "0123456789ab", "-w", "S1.jsonl")
}

// A transcript moved while a pass runs is not counted again when the same pass
// reaches its new path: totals and cost drivers stay those of one read.
func TestCarryOffsetsDuringAPassCountsOnce(t *testing.T) {
	base := t.TempDir()
	from, to := movePaths(base)
	writeFile(t, from, dl1+dl2+dl3)
	svc := defaultSvc(t, base)
	if _, err := svc.Ingest(); err != nil {
		t.Fatal(err)
	}
	wantS, wantD := sums(t, svc.store)
	if wantS <= 0 || wantD <= 0 {
		t.Fatalf("fixture has no cost: %v %v", wantS, wantD)
	}
	passWithMove(t, svc, from, to, false)
	if s, d := sums(t, svc.store); s != wantS || d != wantD {
		t.Fatalf("after the move: session %v drivers %v; want %v %v (one read)", s, d, wantS, wantD)
	}
	if _, err := svc.Ingest(); err != nil {
		t.Fatal(err)
	}
	if s, d := sums(t, svc.store); s != wantS || d != wantD {
		t.Fatalf("next pass: session %v drivers %v; want %v %v", s, d, wantS, wantD)
	}
}

// A transcript counted before schema v3 (backfilled = 0) and moved while a pass
// runs gets its drivers once: the pass backfills it at its new path, and no
// later pass backfills it again.
func TestCarryOffsetsDuringAPassBackfillsOnce(t *testing.T) {
	base := t.TempDir()
	from, to := movePaths(base)
	writeFile(t, from, dl1+dl2+dl3)
	svc := defaultSvc(t, base)
	if _, err := svc.Ingest(); err != nil {
		t.Fatal(err)
	}
	wantS, wantD := sums(t, svc.store)
	if _, err := svc.store.db.Exec(`UPDATE ingest_state SET backfilled=0; DELETE FROM driver_rollup; DELETE FROM session_dim`); err != nil {
		t.Fatal(err)
	}
	passWithMove(t, svc, from, to, false)
	for pass := 0; pass < 2; pass++ {
		if s, d := sums(t, svc.store); s != wantS || d != wantD {
			t.Fatalf("pass %d after the move: session %v drivers %v; want %v %v", pass, s, d, wantS, wantD)
		}
		if _, err := svc.Ingest(); err != nil {
			t.Fatal(err)
		}
	}
}

// In the accepted window (the pass's cache was loaded before the carry
// committed) the new path is read from 0, which counts its totals again. That
// read covers every byte, so it marks the file backfilled: its drivers are
// never added a second time by a backfill on the next pass.
func TestReadFromZeroAfterACarryIsNotBackfilledAgain(t *testing.T) {
	base := t.TempDir()
	from, to := movePaths(base)
	writeFile(t, from, dl1+dl2+dl3)
	svc := defaultSvc(t, base)
	if _, err := svc.Ingest(); err != nil {
		t.Fatal(err)
	}
	_, wantD := sums(t, svc.store)
	if _, err := svc.store.db.Exec(`UPDATE ingest_state SET backfilled=0; DELETE FROM driver_rollup; DELETE FROM session_dim`); err != nil {
		t.Fatal(err)
	}
	passWithMove(t, svc, from, to, true)
	if !backfilledOf(t, svc.store, to) {
		t.Error("a read from offset 0 left the moved file not backfilled")
	}
	for pass := 0; pass < 2; pass++ {
		if _, err := svc.Ingest(); err != nil {
			t.Fatal(err)
		}
		if _, d := sums(t, svc.store); d != wantD {
			t.Fatalf("pass %d after the move: drivers %v, want %v (one parse)", pass, d, wantD)
		}
	}
}
