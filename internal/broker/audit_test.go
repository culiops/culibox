package broker

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
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
// credential, token or nonce. Pinned on the type, so a new field is a decision.
func TestApprovalRowCarriesNoSecrets(t *testing.T) {
	want := []string{"Time", "SessionID", "Profile", "AccountID", "RoleARN", "Action", "Until", "Minutes", "Reason", "Cred", "Error"}
	ty := reflect.TypeOf(ApprovalRow{})
	var got []string
	for i := 0; i < ty.NumField(); i++ {
		got = append(got, ty.Field(i).Name)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ApprovalRow fields %v, want %v", got, want)
	}
}
