package broker

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/secretstore"
)

// m3aSchema is the audit schema as M3a/M3c shipped it (before the elevated
// column and approval_audit), so tests keep describing an old store.
const m3aSchema = `
CREATE TABLE IF NOT EXISTS credential_audit (
  id INTEGER PRIMARY KEY,
  ts INTEGER NOT NULL,
  session_id TEXT NOT NULL,
  profile TEXT NOT NULL DEFAULT '',
  account_id TEXT NOT NULL DEFAULT '',
  role_arn TEXT NOT NULL DEFAULT '',
  source_identity TEXT NOT NULL DEFAULT '',
  scope_hash TEXT NOT NULL DEFAULT '',
  expires INTEGER NOT NULL DEFAULT 0,
  outcome TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_audit_session ON credential_audit(session_id, ts);
`

func TestAuditRecordAndReadBack(t *testing.T) {
	a, err := OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	t0 := time.Unix(1_800_000_000, 0)
	for i, o := range []Outcome{OutcomeMinted, OutcomeSSOExpired, OutcomeMinted} {
		if err := a.Record(Row{Time: t0.Add(time.Duration(i) * time.Minute), SessionID: "claude-3", Profile: "c",
			AccountID: "226198813800", RoleARN: "arn:aws:iam::226198813800:role/agentbox-agent",
			SourceIdentity: "agentbox-claude-3", ScopeHash: "abc", Expires: t0.Add(15 * time.Minute), Outcome: o}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Record(Row{Time: t0, SessionID: "other", Outcome: OutcomeMinted}); err != nil {
		t.Fatal(err)
	}
	rows, err := a.ForSession("claude-3", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Outcome != OutcomeMinted || rows[1].Outcome != OutcomeSSOExpired || !rows[0].Time.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("rows: %+v", rows)
	}
}

// An audit.db written by M3a/M3c has no elevated column; opening it must add
// one without touching existing rows, and opening twice must not fail.
func TestOpenAddsTheElevatedColumnToAnOldStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(m3aSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO credential_audit (ts, session_id, outcome) VALUES (1, 'old-1', 'minted')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	for i := 0; i < 2; i++ {
		a, err := OpenAudit(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		a.Close()
	}
	a, err := OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	rows, err := a.ForSession("old-1", 10)
	if err != nil || len(rows) != 1 || rows[0].Elevated {
		t.Fatalf("old row: %+v %v", rows, err)
	}
	if err := a.Record(Row{Time: time.Unix(2, 0), SessionID: "new-1", Outcome: OutcomeMinted, Elevated: true}); err != nil {
		t.Fatal(err)
	}
	rows, _ = a.ForSession("new-1", 10)
	if len(rows) != 1 || !rows[0].Elevated {
		t.Fatalf("elevated did not round-trip: %+v", rows)
	}
}

// An older binary opening a migrated store keeps working: its INSERT names its
// columns and the new one has a default.
func TestOldBinaryInsertStillWorksOnAMigratedStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	a, err := OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.db.Exec(`INSERT INTO credential_audit
		(ts, session_id, profile, account_id, role_arn, source_identity, scope_hash, expires, outcome, error)
		VALUES (1,'old-2','','','','','',0,'minted','')`); err != nil {
		t.Fatal(err)
	}
	rows, err := a.ForSession("old-2", 10)
	if err != nil || len(rows) != 1 || rows[0].Elevated {
		t.Fatalf("rows: %+v %v", rows, err)
	}
}

func TestApprovalRowsAreStored(t *testing.T) {
	a, err := OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	until := time.Unix(1_800_003_600, 0)
	in := ApprovalRow{Time: time.Unix(1_800_000_000, 0), SessionID: "aws-1", Profile: "aws", AccountID: "226198813800",
		RoleARN: "arn:aws:iam::226198813800:role/agentbox-agent-write", Action: ActionGranted, Until: until,
		Minutes: 60, Reason: "apply vpc", Cred: "cookie"}
	if err := a.RecordApproval(in); err != nil {
		t.Fatal(err)
	}
	var ts, u int64
	var sid, act, cred, reason string
	var min int
	err = a.db.QueryRow(`SELECT ts, session_id, action, until, minutes, reason, cred FROM approval_audit`).Scan(&ts, &sid, &act, &u, &min, &reason, &cred)
	if err != nil || ts != 1_800_000_000 || sid != "aws-1" || act != "granted" || u != until.Unix() || min != 60 || reason != "apply vpc" || cred != "cookie" {
		t.Fatalf("row: %d %s %s %d %d %q %s %v", ts, sid, act, u, min, reason, cred, err)
	}
}

// A zero Until is stored as 0, and Reason/Error are one line and bounded.
func TestApprovalRowSanitizesAndStoresZeroUntilAsZero(t *testing.T) {
	a, err := OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	long := make([]byte, 5000)
	for i := range long {
		long[i] = 'x'
	}
	if err := a.RecordApproval(ApprovalRow{Time: time.Unix(5, 0), SessionID: "s", Action: ActionRefused,
		Reason: "line one\nline two\t end", Error: string(long)}); err != nil {
		t.Fatal(err)
	}
	var u int64
	var reason, errStr string
	if err := a.db.QueryRow(`SELECT until, reason, error FROM approval_audit`).Scan(&u, &reason, &errStr); err != nil {
		t.Fatal(err)
	}
	if u != 0 || reason != "line one line two end" || len(errStr) > maxErrLen+len("…") {
		t.Fatalf("until=%d reason=%q len(error)=%d", u, reason, len(errStr))
	}
}

// Plan correction 17: a failed approval write must be visible to the caller.
func TestRecordApprovalReturnsTheWriteError(t *testing.T) {
	a, err := OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	if err := a.RecordApproval(ApprovalRow{Time: time.Unix(1, 0), SessionID: "s", Action: ActionGranted}); err == nil {
		t.Fatal("RecordApproval on a closed store returned nil; a caller could not fail closed")
	}
}

// Prove: an approval row holds identifiers and the human's reason, never a
// credential, token or nonce (Entry is a connection name). Pinned on the type, so a new field is a decision.
func TestApprovalRowCarriesNoSecrets(t *testing.T) {
	want := []string{"Time", "SessionID", "Profile", "AccountID", "RoleARN", "Action", "Until", "Minutes", "Reason", "Cred", "Error", "Entry"}
	ty := reflect.TypeOf(ApprovalRow{})
	var got []string
	for i := 0; i < ty.NumField(); i++ {
		got = append(got, ty.Field(i).Name)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ApprovalRow fields %v, want %v", got, want)
	}
}

// approvalActions returns a session's approval rows' actions in write order.
func approvalActions(t *testing.T, a *Audit, sid string) []string {
	t.Helper()
	rows, err := a.db.Query(`SELECT action FROM approval_audit WHERE session_id = ? ORDER BY id`, sid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func grantRow(sid string, action Action, at time.Time, until time.Time) ApprovalRow {
	return ApprovalRow{Time: at, SessionID: sid, Profile: "aws", AccountID: "123456789012",
		RoleARN: "arn:aws:iam::123456789012:role/agentbox-agent-write", Action: action, Until: until, Minutes: 30,
		Reason: "apply vpc", Cred: "cookie"}
}

func TestEndOpenWindowsWritesOneEndedRowAndIsIdempotent(t *testing.T) {
	a, err := OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	now := time.Unix(1_800_000_000, 0)
	// Granted, then extended (the extended row is the live one), then a refused
	// row after it, which must not hide the open window.
	for _, r := range []ApprovalRow{
		grantRow("s1", ActionGranted, now.Add(-20*time.Minute), now.Add(10*time.Minute)),
		grantRow("s1", ActionExtended, now.Add(-5*time.Minute), now.Add(25*time.Minute)),
		grantRow("s1", ActionRefused, now.Add(-time.Minute), time.Time{}),
	} {
		if err := a.RecordApproval(r); err != nil {
			t.Fatal(err)
		}
	}
	n, err := a.EndOpenWindows(now)
	if err != nil || n != 1 {
		t.Fatalf("first restart: n=%d err=%v", n, err)
	}
	if got := approvalActions(t, a, "s1"); !slices.Equal(got, []string{"granted", "extended", "refused", "ended"}) {
		t.Fatalf("actions: %v", got)
	}
	var profile, account, role, reason, cred string
	var until, ts int64
	if err := a.db.QueryRow(`SELECT ts, profile, account_id, role_arn, until, reason, cred FROM approval_audit WHERE action = 'ended'`).
		Scan(&ts, &profile, &account, &role, &until, &reason, &cred); err != nil {
		t.Fatal(err)
	}
	if ts != now.Unix() || profile != "aws" || account != "123456789012" || role != "arn:aws:iam::123456789012:role/agentbox-agent-write" ||
		until != now.Add(25*time.Minute).Unix() || reason != "daemon restart" || cred != "none" {
		t.Fatalf("ended row: ts=%d %s %s %s until=%d %q %q", ts, profile, account, role, until, reason, cred)
	}
	// A second start finds the terminal row and writes nothing.
	if n, err := a.EndOpenWindows(now.Add(time.Minute)); err != nil || n != 0 {
		t.Fatalf("second restart: n=%d err=%v", n, err)
	}
	if got := approvalActions(t, a, "s1"); len(got) != 4 {
		t.Fatalf("second restart wrote rows: %v", got)
	}
}

func TestEndOpenWindowsLeavesFinishedWindowsAlone(t *testing.T) {
	a, err := OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	now := time.Unix(1_800_000_000, 0)
	until := now.Add(10 * time.Minute)
	for _, r := range []ApprovalRow{
		grantRow("dropped", ActionGranted, now.Add(-10*time.Minute), until),
		grantRow("dropped", ActionDropped, now.Add(-5*time.Minute), until),
		grantRow("expired", ActionGranted, now.Add(-40*time.Minute), now.Add(-10*time.Minute)),
		grantRow("expired", ActionExpired, now.Add(-10*time.Minute), now.Add(-10*time.Minute)),
		// regranted after a drop, then ended: the old grant row is not "latest".
		grantRow("again", ActionGranted, now.Add(-30*time.Minute), now.Add(-20*time.Minute)),
		grantRow("again", ActionDropped, now.Add(-25*time.Minute), now.Add(-20*time.Minute)),
		{Time: now.Add(-time.Minute), SessionID: "signin", Action: ActionSSOLoginOK},
	} {
		if err := a.RecordApproval(r); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := a.EndOpenWindows(now); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v, want no rows for finished windows", n, err)
	}
}

// A window whose time ran out while the daemon was down never had its expiry
// timer fire: it gets an `expired` row stamped at its end, not `ended`.
func TestEndOpenWindowsWritesExpiredForALapsedWindow(t *testing.T) {
	a, err := OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	now := time.Unix(1_800_000_000, 0)
	until := now.Add(-10 * time.Minute)
	if err := a.RecordApproval(grantRow("s2", ActionGranted, now.Add(-40*time.Minute), until)); err != nil {
		t.Fatal(err)
	}
	if n, err := a.EndOpenWindows(now); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	var ts int64
	var cred string
	if err := a.db.QueryRow(`SELECT ts, cred FROM approval_audit WHERE action = 'expired'`).Scan(&ts, &cred); err != nil || ts != until.Unix() || cred != "" {
		t.Fatalf("expired row: ts=%d cred=%q err=%v", ts, cred, err)
	}
	if n, _ := a.EndOpenWindows(now); n != 0 {
		t.Fatalf("second restart wrote %d rows", n)
	}
}

// A failing write is logged and skipped; the call returns the error and the
// other sessions' rows are still written.
func TestEndOpenWindowsFailsSoft(t *testing.T) {
	a, err := OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	now := time.Unix(1_800_000_000, 0)
	if err := a.RecordApproval(grantRow("s3", ActionGranted, now.Add(-time.Minute), now.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	// Make every further insert fail.
	if _, err := a.db.Exec(`CREATE TRIGGER no_more BEFORE INSERT ON approval_audit BEGIN SELECT RAISE(ABORT, 'full'); END`); err != nil {
		t.Fatal(err)
	}
	n, err := a.EndOpenWindows(now)
	if err == nil || n != 0 {
		t.Fatalf("n=%d err=%v, want the write error and no rows", n, err)
	}
	// The gap is retried once the store works again.
	if _, err := a.db.Exec(`DROP TRIGGER no_more`); err != nil {
		t.Fatal(err)
	}
	if n, err := a.EndOpenWindows(now); err != nil || n != 1 {
		t.Fatalf("retry: n=%d err=%v", n, err)
	}
}

func TestOpenAuditRestrictsFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	// An older run left the files world-readable, with WAL siblings.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.WriteFile(path+suffix, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a, err := OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if fi, err := os.Stat(path + suffix); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v, want 0600", path+suffix, fi, err)
		}
	}
}

func TestAuditProviderRowRoundTrip(t *testing.T) {
	a, err := OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.Record(Row{Time: time.Unix(10, 0), SessionID: "gh-1", Profile: "p", Provider: "github",
		Subject: "culiops", Installation: 4242, Repos: 3, Outcome: OutcomeMinted}); err != nil {
		t.Fatal(err)
	}
	rows, err := a.ForSession("gh-1", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows: %+v %v", rows, err)
	}
	r := rows[0]
	if r.Provider != "github" || r.Subject != "culiops" || r.Installation != 4242 || r.Repos != 3 {
		t.Fatalf("round trip: %+v", r)
	}
}

// A store written before N1 has no provider columns; opening it adds them and
// its rows read back as AWS.
func TestAuditOldDBMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(m3aSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`ALTER TABLE credential_audit ADD COLUMN elevated INTEGER NOT NULL DEFAULT 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO credential_audit (ts, session_id, outcome) VALUES (1, 'pre-n1', 'minted')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	first, err := OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	a, err := OpenAudit(path) // reopening must be idempotent
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	rows, err := a.ForSession("pre-n1", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows: %+v %v", rows, err)
	}
	if rows[0].Provider != "aws" || rows[0].Subject != "" || rows[0].Installation != 0 || rows[0].Repos != 0 {
		t.Fatalf("old row: %+v", rows[0])
	}
}

func TestAuditEmptyProviderStoredAsAWS(t *testing.T) {
	a, err := OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.Record(Row{Time: time.Unix(5, 0), SessionID: "aws-1", Outcome: OutcomeMinted}); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := a.db.QueryRow(`SELECT provider FROM credential_audit WHERE session_id = 'aws-1'`).Scan(&stored); err != nil || stored != "aws" {
		t.Fatalf("stored provider %q, %v", stored, err)
	}
	rows, _ := a.ForSession("aws-1", 10)
	if len(rows) != 1 || rows[0].Provider != "aws" {
		t.Fatalf("rows: %+v", rows)
	}
}

func TestAuditProviderOutcomeValues(t *testing.T) {
	want := map[Outcome]string{
		OutcomeDeniedScope: "denied_scope", OutcomeNotInstalled: "not_installed", OutcomeScopeError: "scope_error",
		OutcomeGitHubError: "github_error", OutcomeRevoked: "revoked", OutcomeRevokeFailed: "revoke_failed",
	}
	for o, s := range want {
		if string(o) != s {
			t.Errorf("outcome %q != %q", o, s)
		}
	}
}

func TestAuditStaticRowRoundTrip(t *testing.T) {
	a, err := OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.Record(Row{Time: time.Unix(20, 0), SessionID: "st-1", Profile: "p", Provider: ProviderStatic,
		Subject: "grafana-cloud", Outcome: OutcomeFetched}); err != nil {
		t.Fatal(err)
	}
	if err := a.Record(Row{Time: time.Unix(21, 0), SessionID: "st-1", Profile: "p", Provider: ProviderStatic,
		Subject: "grafana-cloud", Outcome: OutcomeUnresolved, Error: "file missing"}); err != nil {
		t.Fatal(err)
	}
	rows, err := a.ForSession("st-1", 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows: %+v %v", rows, err)
	}
	if rows[1].Provider != "static" || rows[1].Subject != "grafana-cloud" || rows[1].Outcome != "fetched" {
		t.Fatalf("fetched row: %+v", rows[1])
	}
	if rows[0].Outcome != "unresolved" || rows[0].Error != "file missing" {
		t.Fatalf("unresolved row: %+v", rows[0])
	}
}

func TestAuditPATRowCarriesEntry(t *testing.T) {
	a, err := OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.Record(Row{Time: time.Unix(30, 0), SessionID: "pat-1", Provider: ProviderGitHub,
		Subject: "culiops", Entry: "gh-pat", Outcome: OutcomeFetched}); err != nil {
		t.Fatal(err)
	}
	rows, err := a.ForSession("pat-1", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows: %+v %v", rows, err)
	}
	if rows[0].Entry != "gh-pat" || rows[0].Provider != "github" || rows[0].Subject != "culiops" {
		t.Fatalf("round trip: %+v", rows[0])
	}
}

// A store written before N3 has no entry column; opening it adds the column and
// its rows read back with an empty Entry.
func TestAuditOldDBGetsEntryColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(m3aSchema); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`ALTER TABLE credential_audit ADD COLUMN elevated INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE credential_audit ADD COLUMN provider TEXT NOT NULL DEFAULT 'aws'`,
		`ALTER TABLE credential_audit ADD COLUMN subject TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE credential_audit ADD COLUMN installation INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE credential_audit ADD COLUMN repos INTEGER NOT NULL DEFAULT 0`,
		`INSERT INTO credential_audit (ts, session_id, outcome, provider, subject) VALUES (1, 'pre-n3', 'minted', 'github', 'culiops')`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()
	first, err := OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	a, err := OpenAudit(path) // reopening must be idempotent
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	rows, err := a.ForSession("pre-n3", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows: %+v %v", rows, err)
	}
	if rows[0].Entry != "" || rows[0].Provider != "github" || rows[0].Subject != "culiops" {
		t.Fatalf("old row: %+v", rows[0])
	}
}

func TestForSessionProviderAndExcept(t *testing.T) {
	a, err := OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	const sid = "mix-1"
	for i := 0; i < 30; i++ {
		if err := a.Record(Row{Time: time.Unix(int64(100+i), 0), SessionID: sid, Provider: ProviderStatic,
			Subject: fmt.Sprintf("tok-%d", i), Outcome: OutcomeFetched}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if err := a.Record(Row{Time: time.Unix(int64(10+i), 0), SessionID: sid, Outcome: OutcomeMinted}); err != nil {
			t.Fatal(err)
		}
	}
	// Another session's static row must not leak in.
	if err := a.Record(Row{Time: time.Unix(500, 0), SessionID: "other", Provider: ProviderStatic, Outcome: OutcomeFetched}); err != nil {
		t.Fatal(err)
	}
	other, err := a.ForSessionExcept(sid, ProviderStatic, 20)
	if err != nil || len(other) != 3 {
		t.Fatalf("except static: %+v %v", other, err)
	}
	for _, r := range other {
		if r.Provider != ProviderAWS {
			t.Fatalf("except static returned %+v", r)
		}
	}
	if !other[0].Time.After(other[2].Time) {
		t.Fatalf("not newest first: %+v", other)
	}
	st, err := a.ForSessionProvider(sid, ProviderStatic, 5)
	if err != nil || len(st) != 5 {
		t.Fatalf("static: %+v %v", st, err)
	}
	for i, r := range st {
		if want := fmt.Sprintf("tok-%d", 29-i); r.Subject != want {
			t.Fatalf("static[%d] = %q, want %q", i, r.Subject, want)
		}
	}
}

func TestAuditStaticOutcomeValues(t *testing.T) {
	if OutcomeFetched != "fetched" || OutcomeUnresolved != "unresolved" || ProviderStatic != "static" {
		t.Fatalf("%q %q %q", OutcomeFetched, OutcomeUnresolved, ProviderStatic)
	}
}

// approvalSchemaBeforeEntry is approval_audit exactly as HEAD (before the entry
// column) created it, so the migration test describes a real old store.
const approvalSchemaBeforeEntry = `
CREATE TABLE IF NOT EXISTS approval_audit (
  id INTEGER PRIMARY KEY,
  ts INTEGER NOT NULL,
  session_id TEXT NOT NULL,
  profile TEXT NOT NULL DEFAULT '',
  account_id TEXT NOT NULL DEFAULT '',
  role_arn TEXT NOT NULL DEFAULT '',
  action TEXT NOT NULL,
  until INTEGER NOT NULL DEFAULT 0,
  minutes INTEGER NOT NULL DEFAULT 0,
  reason TEXT NOT NULL DEFAULT '',
  cred TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_approval_session ON approval_audit(session_id, ts);
`

func TestApprovalAuditEntryColumnMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		approvalSchemaBeforeEntry,
		`INSERT INTO approval_audit (ts, session_id, action, minutes, reason, cred) VALUES (1, 'pre-entry', 'granted', 30, 'old grant', 'cookie')`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()
	var a *Audit
	for i := 0; i < 2; i++ { // the second open proves the migration is idempotent
		if a != nil {
			a.Close()
		}
		if a, err = OpenAudit(path); err != nil {
			t.Fatal(err)
		}
	}
	defer a.Close()
	var n int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('approval_audit') WHERE name = 'entry'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("entry column: n=%d err=%v", n, err)
	}
	rows, err := a.ApprovalsForSession("pre-entry", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows: %+v %v", rows, err)
	}
	if rows[0].Entry != "" || rows[0].Action != ActionGranted || rows[0].Minutes != 30 || rows[0].Reason != "old grant" {
		t.Fatalf("old row: %+v", rows[0])
	}
}

func TestApprovalRowEntryRoundTrip(t *testing.T) {
	a, err := OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	base := ApprovalRow{Time: time.Unix(1_800_000_000, 0), SessionID: "aws-1", Action: ActionGranted, Minutes: 15, Cred: "cookie"}
	named := base
	named.Entry = "sbx-a"
	if err := a.RecordApproval(named); err != nil {
		t.Fatal(err)
	}
	legacy := base
	legacy.Time = legacy.Time.Add(time.Minute)
	if err := a.RecordApproval(legacy); err != nil {
		t.Fatal(err)
	}
	rows, err := a.ApprovalsForSession("aws-1", 10) // newest first
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows: %+v %v", rows, err)
	}
	if rows[0].Entry != "" || rows[1].Entry != "sbx-a" {
		t.Fatalf("entries: %q %q", rows[0].Entry, rows[1].Entry)
	}
	// Legacy payloads are unchanged: an empty entry is omitted from JSON.
	if b, _ := json.Marshal(legacy); strings.Contains(string(b), `"entry"`) {
		t.Fatalf("empty entry leaked into JSON: %s", b)
	}
	if b, _ := json.Marshal(named); !strings.Contains(string(b), `"entry":"sbx-a"`) {
		t.Fatalf("entry missing from JSON: %s", b)
	}
}

// A window closed after a restart keeps naming the connection it was granted on.
func TestEndOpenWindowsCarriesTheEntry(t *testing.T) {
	a, err := OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	now := time.Unix(1_800_000_000, 0)
	if err := a.RecordApproval(ApprovalRow{Time: now, SessionID: "aws-1", Action: ActionGranted, Until: now.Add(time.Hour), Entry: "sbx-a"}); err != nil {
		t.Fatal(err)
	}
	if n, err := a.EndOpenWindows(now.Add(time.Minute)); err != nil || n != 1 {
		t.Fatalf("EndOpenWindows: %d %v", n, err)
	}
	rows, err := a.ApprovalsForSession("aws-1", 10)
	if err != nil || len(rows) != 2 || rows[0].Action != ActionEnded || rows[0].Entry != "sbx-a" {
		t.Fatalf("rows: %+v %v", rows, err)
	}
}

func TestRecordStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	a, err := OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var _ secretstore.Recorder = a
	long := strings.Repeat("d", 500)
	if err := a.RecordStore(secretstore.Event{Action: "set", Actor: "operator", Detail: "grafana-prod"}); err != nil {
		t.Fatal(err)
	}
	if err := a.RecordStore(secretstore.Event{Action: "unlock", Detail: "line one\nline two " + long}); err != nil {
		t.Fatal(err)
	}
	rows, err := a.db.Query(`SELECT ts, action, actor, detail FROM store_audit ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type got struct {
		ts            int64
		action, actor string
		detail        string
	}
	var all []got
	for rows.Next() {
		var g got
		if err := rows.Scan(&g.ts, &g.action, &g.actor, &g.detail); err != nil {
			t.Fatal(err)
		}
		all = append(all, g)
	}
	if len(all) != 2 || all[0].action != "set" || all[0].actor != "operator" || all[0].detail != "grafana-prod" || all[0].ts == 0 {
		t.Fatalf("rows: %+v", all)
	}
	if all[1].actor != "" || strings.Contains(all[1].detail, "\n") || len(all[1].detail) > maxStoreDetail+len("…") ||
		!strings.HasPrefix(all[1].detail, "line one line two ddd") || !strings.HasSuffix(all[1].detail, "…") {
		t.Fatalf("detail not bounded to one line: %q (%d)", all[1].detail, len(all[1].detail))
	}
}

// An audit.db written before the store existed gets the table on open, and its
// old rows stay.
func TestOpenAddsStoreAuditToAnOldStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(m3aSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO credential_audit (ts, session_id, outcome) VALUES (1, 'old-1', 'minted')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	for i := 0; i < 2; i++ {
		a, err := OpenAudit(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if err := a.RecordStore(secretstore.Event{Action: "lock"}); err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		a.Close()
	}
	a, err := OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var n int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM store_audit`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("store_audit rows %d %v", n, err)
	}
	if rows, err := a.ForSession("old-1", 10); err != nil || len(rows) != 1 {
		t.Fatalf("old row: %+v %v", rows, err)
	}
}

// A credential-definition event is one row in cred_audit: the action, kind,
// name and actor, with the detail bounded to one line.
func TestRecordCredRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	a, err := OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.RecordCred(CredEvent{Action: "created", Kind: "token", Name: "sentry", Actor: "web", Detail: "template=sentry"}); err != nil {
		t.Fatal(err)
	}
	if err := a.RecordCred(CredEvent{Action: "updated", Kind: "token", Name: "sentry", Actor: "web",
		Detail: "line one\nline two " + strings.Repeat("d", 300)}); err != nil {
		t.Fatal(err)
	}
	rows, err := a.db.Query(`SELECT ts, action, kind, name, actor, detail FROM cred_audit ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type got struct {
		ts                                int64
		action, kind, name, actor, detail string
	}
	var all []got
	for rows.Next() {
		var g got
		if err := rows.Scan(&g.ts, &g.action, &g.kind, &g.name, &g.actor, &g.detail); err != nil {
			t.Fatal(err)
		}
		all = append(all, g)
	}
	if len(all) != 2 || all[0].action != "created" || all[0].kind != "token" || all[0].name != "sentry" ||
		all[0].actor != "web" || all[0].detail != "template=sentry" || all[0].ts == 0 {
		t.Fatalf("rows: %+v", all)
	}
	if strings.Contains(all[1].detail, "\n") || len(all[1].detail) > maxCredDetail+len("…") ||
		!strings.HasPrefix(all[1].detail, "line one line two ddd") || !strings.HasSuffix(all[1].detail, "…") {
		t.Fatalf("detail not bounded to one line: %q (%d)", all[1].detail, len(all[1].detail))
	}
}

// The action constants are the strings the audit log and the UI rely on.
func TestCredActionValues(t *testing.T) {
	want := map[string]string{
		CredCreated: "created", CredUpdated: "updated", CredDeleted: "deleted", CredWriteAck: "write_ack",
		CredRolesListed: "aws_roles_listed", CredChecked: "aws_checked", CredSSOStarted: "aws_sso_started", CredSSOResult: "aws_sso_result",
	}
	for got, w := range want {
		if got != w {
			t.Errorf("action constant %q, want %q", got, w)
		}
	}
}
