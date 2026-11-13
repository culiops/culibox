//go:build upgrade

package upgradetest

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Break is a binary-only rollback finding: vN-1, run on data vN wrote, could
// not start or could not read one file.
type Break struct {
	Scenario, Step, File, Reason, Owner string
}

func (b Break) key() string { return b.Scenario + "/" + b.Step + "/" + b.File }

// knownReverseBreaks are the rollback findings already recorded and handed to
// #229 part C (rollback-safe loaders), keyed "<scenario>/<step>/<file>". A known
// break is logged; a new one fails the test; a known one that no longer breaks
// is logged as FIXED? so a part C fix never turns the test red.
var knownReverseBreaks = map[string]Break{}

// reporter is the part of testing.T the break reports use.
type reporter interface {
	Logf(format string, args ...any)
	Errorf(format string, args ...any)
}

func reportBreak(r reporter, known map[string]Break, b Break) {
	if k, ok := known[b.key()]; ok {
		r.Logf("KNOWN-BREAK %s: %s (known: %s; owner %s)", b.key(), b.Reason, k.Reason, k.Owner)
		return
	}
	r.Errorf("REVERSE-BREAK %s: %s (vN-1 cannot use what vN left in %s; add it to knownReverseBreaks only with a reason and an owner)", b.key(), b.Reason, b.File)
}

func reportUnbroken(r reporter, known map[string]Break, scenario string, broke map[string]bool) {
	var keys []string
	for k := range known {
		if strings.HasPrefix(k, scenario+"/") && !broke[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		r.Logf("FIXED? remove knownReverseBreaks[%s]: it did not break in this run", k)
	}
}

// addFutureColumn adds a nullable upg_future column to every table of the
// SQLite file at path, standing in for a later release's additive migration.
func addFutureColumn(path string) ([]string, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tables, err := tableNames(db)
	if err != nil {
		return nil, err
	}
	for _, tab := range tables {
		if _, err := db.Exec(`ALTER TABLE "` + tab + `" ADD COLUMN upg_future TEXT`); err != nil {
			return nil, fmt.Errorf("%s: %v", tab, err)
		}
	}
	return tables, nil
}

// Reverse runs the binary-only rollback on the data vN left in root:
// R1 starts vN-1 again and checks it reads everything back and can still write;
// R2 adds a column to every audit.db table (an additive migration a later
// release might make) and checks vN-1 still writes and lists audit rows.
//
// c is the scenario's client: its cookie jar holds the session vN-1 issued
// before the upgrade.
func Reverse(t *testing.T, scenario string, c *Client, prevBin string, e Env, f *Fixture, root string) {
	t.Helper()
	broke := map[string]bool{}
	brk := func(step, file, reason string) {
		b := Break{Scenario: scenario, Step: step, File: file, Reason: reason}
		broke[b.key()] = true
		reportBreak(t, knownReverseBreaks, b)
	}
	defer reportUnbroken(t, knownReverseBreaks, scenario, broke)

	port := strings.TrimPrefix(c.Base, "http://127.0.0.1:")
	srv, err := TryStart(t, prevBin, port, e, f.Log)
	if err != nil {
		brk("R1-start", "serve", err.Error())
		return
	}
	if err := NewClient(port).Login(f.Token); err != nil {
		brk("R1", "login.token", err.Error())
	}
	r1(t, c, f, brk)
	srv.Stop(t)

	tables, err := addFutureColumn(filepath.Join(root, "audit.db"))
	if err != nil {
		t.Fatalf("R2: adding the column: %v", err)
	}
	srv, err = TryStart(t, prevBin, port, e, f.Log)
	if err != nil {
		brk("R2", "audit.db", "vN-1 does not start on an audit.db with an added column: "+err.Error())
		return
	}
	defer srv.Stop(t)
	before := auditRows(t, c)
	if _, err := c.Do("PUT", "/api/store/values/UPG_FOUR", map[string]string{"value": "four-229b"}, nil); err != nil {
		brk("R2", "audit.db", "a store write (which records an audit row) fails: "+err.Error())
		return
	}
	after := auditRows(t, c)
	if miss := missingRows(before, after); len(miss) > 0 || countOutcome(after, "value_set") != countOutcome(before, "value_set")+1 {
		brk("R2", "audit.db", fmt.Sprintf("vN-1 lost or did not list audit rows with the added column (missing %v)", miss))
		return
	}
	t.Logf("PASS R2: vN-1 writes and lists audit rows on an audit.db with an added column (tables: %s)", strings.Join(tables, ", "))
}

// r1 is the read-back half of the rollback, against a logged-in vN-1.
func r1(t *testing.T, c *Client, f *Fixture, brk func(step, file, reason string)) {
	t.Helper()
	check := func(file string, err error) {
		if err != nil {
			brk("R1", file, err.Error())
		}
	}
	_, err := c.Do("GET", "/api/accounts", nil, nil)
	check("cookie.secret", err)
	check("accounts.json", sameRaw(c, "/api/accounts", f.Accounts))
	check("profiles.json", sameRaw(c, "/api/profiles", f.Profiles))
	var st struct {
		Initialized bool `json:"initialized"`
		Values      int  `json:"values"`
	}
	if _, err := c.Do("GET", "/api/store", nil, &st); err != nil {
		check("store", err)
	} else if !st.Initialized || st.Values != len(storeValues) {
		check("store", fmt.Errorf("initialized=%v values=%d, want true %d", st.Initialized, st.Values, len(storeValues)))
	}
	var resp struct {
		Events []AuditRow `json:"events"`
	}
	if _, err := c.Do("GET", "/api/audit", nil, &resp); err != nil {
		check("audit.db", err)
	} else if miss := missingRows(f.Audit, resp.Events); len(miss) > 0 {
		check("audit.db", fmt.Errorf("rows missing: %+v", miss))
	}
	if _, err := c.Do("POST", "/api/analytics/ingest", nil, nil); err != nil {
		check("analytics.db", err)
	} else {
		check("analytics.db", sameRaw(c, analyticsPath(), f.Analytics))
	}
	if _, err := c.WithBearer(f.Bearer).Do("GET", "/api/sessions", nil, nil); err != nil {
		check("tokens.json", err)
	}
	before := len(f.Audit)
	if _, err := c.Do("PUT", "/api/store/values/UPG_THREE", map[string]string{"value": "three-229b"}, nil); err != nil {
		check("store", fmt.Errorf("a new value cannot be stored: %v", err))
	} else if after := auditRows(t, c); len(after) <= before {
		check("audit.db", fmt.Errorf("the value_set row for UPG_THREE is not listed (%d rows, had %d)", len(after), before))
	}
	if !t.Failed() {
		t.Logf("PASS R1: vN-1 starts on vN's data and reads it back")
	}
}

func sameRaw(c *Client, path string, want json.RawMessage) error {
	var got json.RawMessage
	if _, err := c.Do("GET", path, nil, &got); err != nil {
		return err
	}
	return sameProjected(string(want), string(got), nil)
}

func countOutcome(rows []AuditRow, outcome string) int {
	n := 0
	for _, r := range rows {
		if r.Outcome == outcome {
			n++
		}
	}
	return n
}
