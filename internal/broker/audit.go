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
}

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
	return &Audit{db: db}, nil
}

func (a *Audit) Close() error { return a.db.Close() }

// Record appends one row. Times are stored as unix seconds.
func (a *Audit) Record(r Row) error {
	var exp int64
	if !r.Expires.IsZero() {
		exp = r.Expires.Unix()
	}
	_, err := a.db.Exec(`INSERT INTO credential_audit
		(ts, session_id, profile, account_id, role_arn, source_identity, scope_hash, expires, outcome, error)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		r.Time.Unix(), r.SessionID, r.Profile, r.AccountID, r.RoleARN, r.SourceIdentity, r.ScopeHash, exp, string(r.Outcome), r.Error)
	return err
}

// ForSession returns up to limit rows for a session, newest first.
func (a *Audit) ForSession(sessionID string, limit int) ([]Row, error) {
	rows, err := a.db.Query(`SELECT ts, session_id, profile, account_id, role_arn, source_identity, scope_hash, expires, outcome, error
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
		if err := rows.Scan(&ts, &r.SessionID, &r.Profile, &r.AccountID, &r.RoleARN, &r.SourceIdentity, &r.ScopeHash, &exp, &o, &r.Error); err != nil {
			return nil, err
		}
		r.Time, r.Outcome = time.Unix(ts, 0), Outcome(o)
		if exp > 0 {
			r.Expires = time.Unix(exp, 0)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
