package upgradebackup

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// makeAudit creates R/audit.db in WAL mode with n rows and returns the open
// handle (kept open by the caller so the -wal file survives).
func makeAudit(t *testing.T, r string, n int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(r, "audit.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA wal_autocheckpoint=0",
		"CREATE TABLE audit (id INTEGER PRIMARY KEY, msg TEXT)",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < n; i++ {
		if _, err := db.Exec("INSERT INTO audit (msg) VALUES (?)", "row"); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestAuditSnapshotIncludesWALRows(t *testing.T) {
	r := t.TempDir()
	makeAudit(t, r, 3)
	if _, err := os.Stat(filepath.Join(r, "audit.db-wal")); err != nil {
		t.Fatalf("test setup: no -wal file: %v", err)
	}
	if err := os.Chmod(filepath.Join(r, "audit.db"), 0o640); err != nil {
		t.Fatal(err)
	}
	res, err := Run(opts(r, "v1"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Taken || res.AuditSkipped != 0 {
		t.Fatalf("result: %+v", res)
	}
	snap := filepath.Join(res.Dir, "audit.db")
	db, err := sql.Open("sqlite", "file:"+snap+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM audit").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("snapshot has %d rows, want 3", n)
	}
	for _, x := range []string{"audit.db-wal", "audit.db-shm"} {
		if _, err := os.Lstat(filepath.Join(res.Dir, x)); err == nil {
			t.Fatalf("%s in the backup", x)
		}
	}
	fi, _ := os.Stat(snap)
	if fi.Mode().Perm() != 0o640 {
		t.Fatalf("snapshot mode %v, want the source mode 0640", fi.Mode().Perm())
	}
}

func TestAuditOverBoundSkipped(t *testing.T) {
	r := t.TempDir()
	db := makeAudit(t, r, 3)
	db.Close()
	var lr logRecorder
	o := opts(r, "v1")
	o.AuditMaxBytes = 1
	o.Logf = lr.logf
	res, err := Run(o)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Taken || res.AuditSkipped <= 0 {
		t.Fatalf("result: %+v", res)
	}
	if _, err := os.Lstat(filepath.Join(res.Dir, "audit.db")); err == nil {
		t.Fatal("over-bound audit.db was backed up")
	}
	if !strings.Contains(lr.joined(), "audit.db") || !strings.Contains(lr.joined(), strconv.FormatInt(res.AuditSkipped, 10)) {
		t.Fatalf("Logf did not name audit.db and its size %d:\n%s", res.AuditSkipped, lr.joined())
	}
}

func TestAuditCorruptFails(t *testing.T) {
	r := t.TempDir()
	junk := make([]byte, 4096)
	for i := range junk {
		junk[i] = 0xFF
	}
	if err := os.WriteFile(filepath.Join(r, "audit.db"), junk, 0o600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, r, StampName, "v0 old\n")
	res, err := Run(opts(r, "v1"))
	if err == nil {
		t.Fatalf("no error: %+v", res)
	}
	if !strings.Contains(err.Error(), "audit.db") {
		t.Fatalf("error does not name audit.db: %v", err)
	}
	if res.Taken {
		t.Fatalf("Taken with a failed backup: %+v", res)
	}
	b, _ := os.ReadFile(filepath.Join(r, StampName))
	if string(b) != "v0 old\n" {
		t.Fatalf("stamp changed to %q", b)
	}
	ents, _ := os.ReadDir(filepath.Join(r, "backups"))
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("finished backup dir %q after a failure", e.Name())
		}
	}
}

func TestAuditSymlinkSkipped(t *testing.T) {
	out := t.TempDir()
	db := makeAudit(t, out, 3)
	db.Close()
	r := t.TempDir()
	writeFile(t, r, "tokens.json", "{}")
	if err := os.Symlink(filepath.Join(out, "audit.db"), filepath.Join(r, "audit.db")); err != nil {
		t.Fatal(err)
	}
	var lr logRecorder
	o := opts(r, "v1")
	o.Logf = lr.logf
	res, err := Run(o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(res.Dir, "audit.db")); err == nil {
		t.Fatal("symlinked audit.db was followed")
	}
	if !strings.Contains(lr.joined(), "audit.db") {
		t.Fatalf("skip not logged:\n%s", lr.joined())
	}
}

func TestNoAuditNoSnapshot(t *testing.T) {
	r := t.TempDir()
	writeFile(t, r, "tokens.json", "{}")
	res, err := Run(opts(r, "v1"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Taken || res.AuditSkipped != 0 {
		t.Fatalf("result: %+v", res)
	}
	if _, err := os.Lstat(filepath.Join(res.Dir, "audit.db")); err == nil {
		t.Fatal("audit.db in the backup")
	}
}

// A FIFO at an allowlisted path is skipped; Run must not block on it.
func TestFIFOSkippedNoHang(t *testing.T) {
	r := t.TempDir()
	writeFile(t, r, "tokens.json", "{}")
	if err := syscall.Mkfifo(filepath.Join(r, "profiles.json"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	var lr logRecorder
	o := opts(r, "v1")
	o.Logf = lr.logf
	type out struct {
		res Result
		err error
	}
	ch := make(chan out, 1)
	go func() {
		res, err := Run(o)
		ch <- out{res, err}
	}()
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if _, err := os.Lstat(filepath.Join(got.res.Dir, "profiles.json")); err == nil {
			t.Fatal("FIFO was copied")
		}
		if !strings.Contains(lr.joined(), "profiles.json") {
			t.Fatalf("skip not logged:\n%s", lr.joined())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run blocked on a FIFO")
	}
}
