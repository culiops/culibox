package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func seedDoctorHome(t *testing.T, dataDir, name string, age time.Duration, conv bool) {
	t.Helper()
	dir := filepath.Join(dataDir, "boxstate", name)
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "projects", "-w"), 0o700); err != nil {
		t.Fatal(err)
	}
	if conv {
		if err := os.WriteFile(filepath.Join(dir, ".claude", "projects", "-w", "s.jsonl"), make([]byte, 3*1024*1024), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(dir, when, when); err != nil {
		t.Fatal(err)
	}
}

// #330: doctor names every box home reclaim keeps past 30 days because it holds
// conversations, with its size and how to remove one by hand.
func TestKeptHomesRowListsKeptHomes(t *testing.T) {
	dataDir := t.TempDir()
	seedDoctorHome(t, dataDir, "ab-box-aaaaaaaaaaaa", 40*24*time.Hour, true)
	seedDoctorHome(t, dataDir, "ab-box-bbbbbbbbbbbb", 40*24*time.Hour, false)
	seedDoctorHome(t, dataDir, "ab-box-cccccccccccc", 2*24*time.Hour, true)
	rows := checkKeptHomes(dataDir)
	if len(rows) != 1 {
		t.Fatalf("want one row, got %+v", rows)
	}
	r := rows[0]
	if r.Status != Pass || r.Check != "kept box homes" {
		t.Fatalf("row %+v", r)
	}
	for _, want := range []string{"1 home", "ab-box-aaaaaaaaaaaa", "unused 40 days", "3.0 MiB", filepath.Join(dataDir, "boxstate", "ab-box-aaaaaaaaaaaa"), "culibox box status"} {
		if !strings.Contains(r.Detail, want) {
			t.Errorf("detail lacks %q:\n%s", want, r.Detail)
		}
	}
	for _, not := range []string{"ab-box-bbbbbbbbbbbb", "ab-box-cccccccccccc"} {
		if strings.Contains(r.Detail, not) {
			t.Errorf("detail names %s, which reclaim does not keep:\n%s", not, r.Detail)
		}
	}
}

// Nothing kept, no row: doctor's report stays short.
func TestKeptHomesRowIsAbsentWithNothingKept(t *testing.T) {
	dataDir := t.TempDir()
	seedDoctorHome(t, dataDir, "ab-box-bbbbbbbbbbbb", 40*24*time.Hour, false)
	if rows := checkKeptHomes(dataDir); len(rows) != 0 {
		t.Fatalf("want no row, got %+v", rows)
	}
	if rows := checkKeptHomes(filepath.Join(dataDir, "missing")); len(rows) != 0 {
		t.Fatalf("want no row for a data dir with no box state, got %+v", rows)
	}
}
