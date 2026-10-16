package analytics

import (
	"path/filepath"
	"testing"
	"time"
)

// Roots are glob patterns re-expanded on every Ingest, so an account (or box)
// created after the service started is picked up without a restart.
func TestIngestRegloadsRootPatterns(t *testing.T) {
	base := t.TempDir()
	svc, err := New(filepath.Join(t.TempDir(), "a.db"),
		[]string{filepath.Join(base, "accounts", "*", "claude", "projects")}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if _, err := svc.Ingest(); err != nil { // nothing exists yet
		t.Fatal(err)
	}
	rows, _ := svc.ByDayModel("2026-08-01", "2026-08-31")
	if len(rows) != 0 {
		t.Fatalf("rows before any account exists: %+v", rows)
	}

	// An account created after the service started:
	writeFile(t, filepath.Join(base, "accounts", "work", "claude", "projects", "-p", "S1.jsonl"), line1+line2)
	st, err := svc.Ingest()
	if err != nil {
		t.Fatal(err)
	}
	if st.Files != 1 {
		t.Fatalf("second pass files=%d want 1", st.Files)
	}
	rows, _ = svc.ByDayModel("2026-08-01", "2026-08-31")
	if len(rows) != 1 || rows[0].Input != 100 || rows[0].Output != 20 {
		t.Fatalf("account transcript not ingested: %+v", rows)
	}
}

// A literal root is a pattern that matches itself, so configured roots keep working.
func TestIngestLiteralRootStillWorks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "-p", "S1.jsonl"), line1+line2)
	svc, err := New(filepath.Join(t.TempDir(), "a.db"), []string{root}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if st, err := svc.Ingest(); err != nil || st.Files != 1 {
		t.Fatalf("literal root: st=%+v err=%v", st, err)
	}
}
