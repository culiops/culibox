package broker

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, registered as "sqlite"
)

// Outcome classifies one credential request in the audit log.
type Outcome string

const (
	OutcomeMinted        Outcome = "minted"
	OutcomeDeniedToken   Outcome = "denied_token"
	OutcomeDeniedSession Outcome = "denied_session"
	OutcomeSSOExpired    Outcome = "sso_expired"
	OutcomeSTSError      Outcome = "sts_error"
	OutcomeRateLimited   Outcome = "rate_limited"
)

// Row is one audited credential request. It never carries credential values or
// broker tokens — only identifiers and the outcome.
type Row struct {
	Time           time.Time `json:"time"`
	SessionID      string    `json:"session_id"`
	Profile        string    `json:"profile"`
	AccountID      string    `json:"account_id"`
	RoleARN        string    `json:"role_arn"`
	SourceIdentity string    `json:"source_identity"`
	ScopeHash      string    `json:"scope_hash"`
	Expires        time.Time `json:"expires"`
	Outcome        Outcome   `json:"outcome"`
	Error          string    `json:"error,omitempty"`
	Elevated       bool      `json:"elevated,omitempty"`
}

// Action classifies one row of the approval log: a human decision about write
// elevation, or a sign-in outcome.
type Action string

const (
	ActionGranted        Action = "granted"
	ActionExtended       Action = "extended"
	ActionDropped        Action = "dropped"
	ActionExpired        Action = "expired"
	ActionRefused        Action = "refused"
	ActionSSOLoginOK     Action = "sso_login_ok"
	ActionSSOLoginFailed Action = "sso_login_failed"
)

// ApprovalRow is one audited approval decision. Like Row it holds identifiers
// and the human's reason only, never a credential, token or nonce.
type ApprovalRow struct {
	Time                                   time.Time
	SessionID, Profile, AccountID, RoleARN string
	Action                                 Action
	Until                                  time.Time
	Minutes                                int
	Reason, Cred, Error                    string
}

// ApprovalRecorder is what the elevation code writes approvals through. The
// error matters: a grant whose row cannot be written must not take effect.
type ApprovalRecorder interface{ RecordApproval(ApprovalRow) error }

const auditSchema = `
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

// Audit is the SQLite-backed credential audit log (<data_dir>/audit.db).
type Audit struct{ db *sql.DB }

// OpenAudit opens (creating if needed) the audit store at path. Pragmas go in the
// DSN, not a post-open Exec, so every pooled connection gets them (same as the
// analytics store).
func OpenAudit(path string) (*Audit, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(auditSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("audit schema: %w", err)
	}
	if err := addColumnIfMissing(db, "credential_audit", "elevated", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, fmt.Errorf("audit migrate: %w", err)
	}
	return &Audit{db: db}, nil
}

// addColumnIfMissing adds a column to an existing store (CREATE TABLE IF NOT
// EXISTS leaves an older table as it was). table, column and decl are
// compile-time constants at the call site, never input.
func addColumnIfMissing(db *sql.DB, table, column, decl string) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + decl)
	return err
}

func (a *Audit) Close() error { return a.db.Close() }

// Record appends one row. Times are stored as unix seconds.
func (a *Audit) Record(r Row) error {
	var exp int64
	if !r.Expires.IsZero() {
		exp = r.Expires.Unix()
	}
	_, err := a.db.Exec(`INSERT INTO credential_audit
		(ts, session_id, profile, account_id, role_arn, source_identity, scope_hash, expires, outcome, error, elevated)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		r.Time.Unix(), r.SessionID, r.Profile, r.AccountID, r.RoleARN, r.SourceIdentity, r.ScopeHash, exp, string(r.Outcome), r.Error, b2i(r.Elevated))
	return err
}

// RecordApproval appends one approval row. Times are unix seconds (until 0 when
// zero); Reason and Error are one line and bounded. The error is returned so a
// caller can fail closed when the decision could not be recorded.
func (a *Audit) RecordApproval(r ApprovalRow) error {
	var until int64
	if !r.Until.IsZero() {
		until = r.Until.Unix()
	}
	_, err := a.db.Exec(`INSERT INTO approval_audit
		(ts, session_id, profile, account_id, role_arn, action, until, minutes, reason, cred, error)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		r.Time.Unix(), r.SessionID, r.Profile, r.AccountID, r.RoleARN, string(r.Action), until, r.Minutes,
		sanitize(r.Reason), r.Cred, sanitize(r.Error))
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ForSession returns up to limit rows for a session, newest first.
func (a *Audit) ForSession(sessionID string, limit int) ([]Row, error) {
	rows, err := a.db.Query(`SELECT ts, session_id, profile, account_id, role_arn, source_identity, scope_hash, expires, outcome, error, elevated
		FROM credential_audit WHERE session_id = ? ORDER BY ts DESC, id DESC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		var ts, exp int64
		var o string
		var elevated int
		if err := rows.Scan(&ts, &r.SessionID, &r.Profile, &r.AccountID, &r.RoleARN, &r.SourceIdentity, &r.ScopeHash, &exp, &o, &r.Error, &elevated); err != nil {
			return nil, err
		}
		r.Time, r.Outcome, r.Elevated = time.Unix(ts, 0), Outcome(o), elevated != 0
		if exp > 0 {
			r.Expires = time.Unix(exp, 0)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
