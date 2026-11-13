package upgradebackup

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

const day = 24 * time.Hour

// mkBackups creates backups/<dirName> for each age (in days before t0) and
// returns the names in the order given.
func mkBackups(t *testing.T, dir string, ages ...int) []string {
	t.Helper()
	var names []string
	for _, a := range ages {
		n := dirName("v1", "v2", t0.Add(-time.Duration(a)*day))
		p := filepath.Join(dir, backupsDir, n)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, p, "profiles.json", "{}")
		names = append(names, n)
	}
	return names
}

func listBackups(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(dir, backupsDir))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func sorted(s []string) []string {
	c := append([]string(nil), s...)
	sort.Strings(c)
	return c
}

func runPrune(t *testing.T, dir string) []string {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	removed, err := prune(r, t0)
	if err != nil {
		t.Fatal(err)
	}
	return removed
}

func ageRange(from, to int) []int {
	var a []int
	for i := from; i <= to; i++ {
		a = append(a, i)
	}
	return a
}

func TestRetentionKeeps30Days(t *testing.T) {
	r := t.TempDir()
	names := mkBackups(t, r, ageRange(1, 6)...)
	if removed := runPrune(t, r); len(removed) != 0 {
		t.Fatalf("removed %v, want none", removed)
	}
	if got := listBackups(t, r); !reflect.DeepEqual(got, sorted(names)) {
		t.Fatalf("got %v want %v", got, sorted(names))
	}
}

func TestRetentionKeeps3Newest(t *testing.T) {
	r := t.TempDir()
	names := mkBackups(t, r, ageRange(40, 44)...)
	removed := runPrune(t, r)
	if want := sorted(names[3:]); !reflect.DeepEqual(sorted(removed), want) {
		t.Fatalf("removed %v want %v", sorted(removed), want)
	}
	if got, want := listBackups(t, r), sorted(names[:3]); !reflect.DeepEqual(got, want) {
		t.Fatalf("kept %v want %v", got, want)
	}
}

func TestRetentionBoundary30Days(t *testing.T) {
	r := t.TempDir()
	// 3 newest at 1..3 days; exactly 30 days is kept, 30 days + 1 s is not.
	names := mkBackups(t, r, 1, 2, 3, 30)
	old := dirName("v1", "v2", t0.Add(-30*day-time.Second))
	if err := os.Mkdir(filepath.Join(r, backupsDir, old), 0o700); err != nil {
		t.Fatal(err)
	}
	removed := runPrune(t, r)
	if !reflect.DeepEqual(removed, []string{old}) {
		t.Fatalf("removed %v want [%s]", removed, old)
	}
	if got := listBackups(t, r); !reflect.DeepEqual(got, sorted(names)) {
		t.Fatalf("got %v want %v", got, sorted(names))
	}
}

func TestRetentionCap(t *testing.T) {
	r := t.TempDir()
	names := mkBackups(t, r, ageRange(0, 24)...)
	removed := runPrune(t, r)
	if want := sorted(names[20:]); !reflect.DeepEqual(sorted(removed), want) {
		t.Fatalf("removed %v want %v", sorted(removed), want)
	}
	if got, want := listBackups(t, r), sorted(names[:20]); !reflect.DeepEqual(got, want) {
		t.Fatalf("kept %v want %v", got, want)
	}
}

func TestRetentionManyInOneDay(t *testing.T) {
	r := t.TempDir()
	var names []string
	for i := 0; i < 30; i++ { // 30 deploys, a minute apart, plus an old one
		n := dirName("v1", "v2", t0.Add(-time.Duration(i)*time.Minute))
		if err := os.MkdirAll(filepath.Join(r, backupsDir, n), 0o700); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	runPrune(t, r)
	if got, want := listBackups(t, r), sorted(names[:20]); !reflect.DeepEqual(got, want) {
		t.Fatalf("kept %v want %v", got, want)
	}
}

func TestRetentionCounterNamesParse(t *testing.T) {
	r := t.TempDir()
	// Task 2's collision names carry the counter before the timestamp.
	var names []string
	for i := 0; i < 5; i++ {
		n := dirName("v1", "v2_"+string(rune('2'+i)), t0.Add(-time.Duration(40+i)*day))
		if err := os.MkdirAll(filepath.Join(r, backupsDir, n), 0o700); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	removed := runPrune(t, r)
	if want := sorted(names[3:]); !reflect.DeepEqual(sorted(removed), want) {
		t.Fatalf("removed %v want %v", sorted(removed), want)
	}
}

func TestRetentionIgnoresForeignNames(t *testing.T) {
	r := t.TempDir()
	mkBackups(t, r, ageRange(40, 44)...)
	b := filepath.Join(r, backupsDir)
	outside := t.TempDir()
	writeFile(t, outside, "keep", "x")
	foreign := []string{"notes", ".tmp-x", "v1-to-v2-20260101T000000", "v1-to-v2-2026O101T000000Z", "20260101T000000Z", ".v1-to-v2-20200101T000000Z"}
	for _, n := range foreign {
		if err := os.Mkdir(filepath.Join(b, n), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, b, "plain-file-20200101T000000Z", "x") // file with a parseable name
	if err := os.Symlink(outside, filepath.Join(b, "link-20200101T000000Z")); err != nil {
		t.Fatal(err)
	}
	removed := runPrune(t, r)
	if len(removed) != 2 {
		t.Fatalf("removed %v, want the 2 oldest backups", removed)
	}
	for _, n := range append(foreign, "plain-file-20200101T000000Z", "link-20200101T000000Z") {
		if _, err := os.Lstat(filepath.Join(b, n)); err != nil {
			t.Errorf("%s was touched: %v", n, err)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Errorf("symlink target was touched: %v", err)
	}
}

func TestRetentionForeignDoNotCount(t *testing.T) {
	r := t.TempDir()
	// 20 fresh backups plus unparsable dirs: nothing is removed.
	mkBackups(t, r, ageRange(0, 19)...)
	for _, n := range []string{"a", "b", ".tmp-c"} {
		if err := os.Mkdir(filepath.Join(r, backupsDir, n), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if removed := runPrune(t, r); len(removed) != 0 {
		t.Fatalf("removed %v", removed)
	}
}

func TestRetentionNoBackupsDir(t *testing.T) {
	r := t.TempDir()
	if removed := runPrune(t, r); len(removed) != 0 {
		t.Fatalf("removed %v", removed)
	}
}

func TestRunPrunesAfterStamp(t *testing.T) {
	r := t.TempDir()
	writeFile(t, r, "profiles.json", "{}")
	writeFile(t, r, StampName, "v0 abc\n")
	old := mkBackups(t, r, ageRange(40, 44)...)
	res, err := Run(opts(r, "v1"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Taken {
		t.Fatal("no backup taken")
	}
	// the new backup (age 0) plus the 3 newest old ones... the new one is index 0
	// so only 2 old ones stay in the top 3.
	if want := sorted(old[2:]); !reflect.DeepEqual(sorted(res.Pruned), want) {
		t.Fatalf("Pruned %v want %v", sorted(res.Pruned), want)
	}
	if got := readStamp(mustRoot(t, r)); got != "v1" {
		t.Fatalf("stamp %q", got)
	}
}

func TestRunPruneErrorDoesNotFail(t *testing.T) {
	r := t.TempDir()
	writeFile(t, r, "profiles.json", "{}")
	o := opts(r, "v1")
	var logged int
	o.Logf = func(string, ...any) { logged++ }
	// A backup dir that cannot be removed: read-only parent entry content.
	n := dirName("v1", "v2", t0.Add(-90*day))
	locked := filepath.Join(r, backupsDir, n)
	if err := os.MkdirAll(filepath.Join(locked, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		mkBackups(t, r, 50+i)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0o700)
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	res, err := Run(o)
	if err != nil {
		t.Fatalf("Run failed on prune error: %v", err)
	}
	if !res.Taken || logged == 0 {
		t.Fatalf("taken=%v logged=%d", res.Taken, logged)
	}
	if got := readStamp(mustRoot(t, r)); got != "v1" {
		t.Fatalf("stamp %q", got)
	}
}

func mustRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}
