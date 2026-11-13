package upgradebackup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func restoreRoundTrip(t *testing.T, r string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(r, "store", "values"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, r, "profiles.json", `{"orig":1}`)
	writeFile(t, r, filepath.Join("store", "values", "a.age"), "orig-secret")
	writeFile(t, r, StampName, "v0 old\n")

	res, err := Run(opts(r, "v1"))
	if err != nil || !res.Taken {
		t.Fatalf("backup: %+v %v", res, err)
	}
	writeFile(t, r, "profiles.json", `{"changed":2}`)
	writeFile(t, r, filepath.Join("store", "values", "a.age"), "changed")
	writeFile(t, r, "newfile", "keep me")

	out, err := exec.Command("sh", "-c", RestoreCommand(r, res.Dir)).CombinedOutput()
	if err != nil {
		t.Fatalf("restore command: %v\n%s\ncmd: %s", err, out, RestoreCommand(r, res.Dir))
	}
	for name, want := range map[string]string{
		"profiles.json": `{"orig":1}`,
		filepath.Join("store", "values", "a.age"): "orig-secret",
		"newfile": "keep me",
		StampName: "v0 old\n",
	} {
		b, err := os.ReadFile(filepath.Join(r, name))
		if err != nil || string(b) != want {
			t.Errorf("%s = %q, %v; want %q", name, b, err, want)
		}
	}
}

func TestRestoreCommandRoundTrip(t *testing.T) {
	restoreRoundTrip(t, t.TempDir())
}

func TestRestoreCommandRoundTripSpaceAndQuote(t *testing.T) {
	r := filepath.Join(t.TempDir(), "my data's root")
	if err := os.MkdirAll(r, 0o700); err != nil {
		t.Fatal(err)
	}
	restoreRoundTrip(t, r)
}

func TestRestoreCommandText(t *testing.T) {
	// No audit.db in the backup: the live audit.db and its sidecars are left alone.
	b := filepath.Join(t.TempDir(), "b")
	if err := os.MkdirAll(b, 0o700); err != nil {
		t.Fatal(err)
	}
	got := RestoreCommand("/d/r", b)
	want := `rm -f '/d/r'/last-version && cp -a '` + b + `'/. '/d/r'/`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if strings.Contains(got, "$") || strings.Contains(got, "audit.db") {
		t.Fatalf("unexpected text in %s", got)
	}

	// audit.db in the backup: its WAL files go, so a newer WAL is never applied to it.
	writeFile(t, b, "audit.db", "snapshot")
	got = RestoreCommand("/d/r", b)
	want = `rm -f '/d/r'/last-version && cp -a '` + b + `'/. '/d/r'/ && rm -f '/d/r'/audit.db-wal '/d/r'/audit.db-shm`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}

	// A symlink or directory named audit.db is not a snapshot: no WAL removal.
	b2 := filepath.Join(t.TempDir(), "b2")
	if err := os.MkdirAll(b2, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(b2, "audit.db")); err != nil {
		t.Fatal(err)
	}
	if got := RestoreCommand("/d/r", b2); strings.Contains(got, "audit.db") {
		t.Fatalf("symlinked audit.db counted as a snapshot: %s", got)
	}
}

// Without audit.db in the backup, a restore leaves the live audit.db and its
// -wal and -shm files exactly as they are.
func TestRestoreKeepsLiveAuditWhenBackupHasNone(t *testing.T) {
	r := t.TempDir()
	writeFile(t, r, "profiles.json", `{"orig":1}`)
	res, err := Run(opts(r, "v1"))
	if err != nil || !res.Taken {
		t.Fatalf("backup: %+v %v", res, err)
	}
	writeFile(t, r, "audit.db", "live")
	writeFile(t, r, "audit.db-wal", "wal rows")
	writeFile(t, r, "audit.db-shm", "shm")
	if out, err := exec.Command("sh", "-c", RestoreCommand(r, res.Dir)).CombinedOutput(); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	for name, want := range map[string]string{"audit.db": "live", "audit.db-wal": "wal rows", "audit.db-shm": "shm"} {
		if b, err := os.ReadFile(filepath.Join(r, name)); err != nil || string(b) != want {
			t.Errorf("%s = %q, %v; want %q", name, b, err, want)
		}
	}
}

// With audit.db in the backup, the restore replaces audit.db and drops the
// newer -wal and -shm files.
func TestRestoreDropsWALWhenBackupHasAudit(t *testing.T) {
	r, b := t.TempDir(), filepath.Join(t.TempDir(), "b")
	if err := os.MkdirAll(b, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, b, "audit.db", "snapshot")
	writeFile(t, r, "audit.db", "live")
	writeFile(t, r, "audit.db-wal", "newer wal")
	writeFile(t, r, "audit.db-shm", "shm")
	if out, err := exec.Command("sh", "-c", RestoreCommand(r, b)).CombinedOutput(); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(filepath.Join(r, "audit.db")); string(got) != "snapshot" {
		t.Errorf("audit.db = %q, want the snapshot", got)
	}
	for _, n := range []string{"audit.db-wal", "audit.db-shm"} {
		if _, err := os.Lstat(filepath.Join(r, n)); err == nil {
			t.Errorf("%s survived the restore", n)
		}
	}
}
