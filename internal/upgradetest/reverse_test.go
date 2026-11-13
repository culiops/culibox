//go:build upgrade

package upgradetest

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAddFutureColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`CREATE TABLE a (x TEXT)`, `CREATE TABLE b (y INTEGER)`, `INSERT INTO a VALUES ('1')`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	tables, err := addFutureColumn(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tables, []string{"a", "b"}) {
		t.Fatalf("altered %v", tables)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tab := range tables {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name = 'upg_future'`, tab).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s has upg_future %d times (%v)", tab, n, err)
		}
	}
}

type recorder struct{ logs, errs []string }

func (r *recorder) Logf(f string, a ...any)   { r.logs = append(r.logs, fmt.Sprintf(f, a...)) }
func (r *recorder) Errorf(f string, a ...any) { r.errs = append(r.errs, fmt.Sprintf(f, a...)) }

func TestReportBreakKnownVsUnknown(t *testing.T) {
	known := map[string]Break{"S2/R1-start/serve": {Reason: "the legacy path is a symlink", Owner: "229C"}}
	r := &recorder{}
	reportBreak(r, known, Break{Scenario: "S2", Step: "R1-start", File: "serve", Reason: "refused"})
	if len(r.errs) != 0 || len(r.logs) != 1 || !strings.HasPrefix(r.logs[0], "KNOWN-BREAK ") {
		t.Fatalf("known: logs %v errs %v", r.logs, r.errs)
	}
	r = &recorder{}
	reportBreak(r, known, Break{Scenario: "S1", Step: "R1", File: "profiles.json", Reason: "differs"})
	if len(r.errs) != 1 || !strings.HasPrefix(r.errs[0], "REVERSE-BREAK ") || !strings.Contains(r.errs[0], "profiles.json") {
		t.Fatalf("unknown: logs %v errs %v", r.logs, r.errs)
	}
}

func TestReportUnbrokenKnownEntries(t *testing.T) {
	known := map[string]Break{"S2/R1-start/serve": {}, "S1/R2/audit.db": {}}
	r := &recorder{}
	reportUnbroken(r, known, "S2", map[string]bool{})
	if len(r.errs) != 0 || len(r.logs) != 1 || !strings.Contains(r.logs[0], "FIXED? remove knownReverseBreaks[S2/R1-start/serve]") {
		t.Fatalf("logs %v errs %v", r.logs, r.errs)
	}
	r = &recorder{}
	reportUnbroken(r, known, "S2", map[string]bool{"S2/R1-start/serve": true})
	if len(r.logs) != 0 {
		t.Fatalf("a key that broke was reported fixed: %v", r.logs)
	}
}
