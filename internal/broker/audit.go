package broker

import (
	"database/sql"
	"fmt"
	"log"
	"os"
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

	// GitHub-provider outcomes.
	OutcomeDeniedScope  Outcome = "denied_scope"
	OutcomeNotInstalled Outcome = "not_installed"
	OutcomeScopeError   Outcome = "scope_error"
	OutcomeGitHubError  Outcome = "github_error"
	OutcomeRevoked      Outcome = "revoked"
	OutcomeRevokeFailed Outcome = "revoke_failed"
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
	// Provider names the credential kind; empty is stored (and read back) as "aws".
	Provider string `json:"provider,omitempty"`
	// Subject is the GitHub owner the token was minted for.
	Subject      string `json:"subject,omitempty"`
	Installation int64  `json:"installation,omitempty"`
	Repos        int    `json:"repos,omitempty"`
}

// Action classifies one row of the approval log: a human decision about write
// elevation, or a sign-in outcome.
type Action string

const (
	ActionGranted        Action = "granted"
	ActionExtended       Action = "extended"
	ActionDropped        Action = "dropped"
	ActionExpired        Action = "expired"
	ActionEnded          Action = "ended" // a write window cut short because the daemon restarted
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
	for _, c := range [][2]string{
		{"provider", "TEXT NOT NULL DEFAULT 'aws'"},
		{"subject", "TEXT NOT NULL DEFAULT ''"},
		{"installation", "INTEGER NOT NULL DEFAULT 0"},
		{"repos", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := addColumnIfMissing(db, "credential_audit", c[0], c[1]); err != nil {
			db.Close()
			return nil, fmt.Errorf("audit migrate: %w", err)
		}
	}
	// SQLite creates the file 0644; it records who was granted write access and
	// why, so only the owner may read it. The -wal/-shm siblings exist while the
	// store is open and are tightened the same way.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chmod(path+suffix, 0o600); err != nil && !os.IsNotExist(err) {
			log.Printf("broker: audit store permissions (%s): %v", path+suffix, err)
		}
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
	provider := r.Provider
	if provider == "" {
		provider = "aws"
	}
	_, err := a.db.Exec(`INSERT INTO credential_audit
		(ts, session_id, profile, account_id, role_arn, source_identity, scope_hash, expires, outcome, error, elevated,
		 provider, subject, installation, repos)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.Time.Unix(), r.SessionID, r.Profile, r.AccountID, r.RoleARN, r.SourceIdentity, r.ScopeHash, exp, string(r.Outcome), r.Error, b2i(r.Elevated),
		provider, r.Subject, r.Installation, r.Repos)
	return err
}

// RecordApproval appends one approval row. Times are unix seconds (until 0 when
// zero); Reason and Error are one line and bounded. Reason has its own, larger
// bound (maxReasonBytes) so a valid 200-character reason in any script is kept
// whole. The error is returned so a
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
		sanitizeN(r.Reason, maxReasonBytes), r.Cred, sanitize(r.Error))
	return err
}

// windowActionsSQL lists the rows that decide whether a write window is in
// force: the two that open or lengthen one and the three that end one. Refused
// and sign-in rows are not part of a window's life and are ignored.
const windowActionsSQL = `('granted','extended','dropped','expired','ended')`

// EndOpenWindows closes the audit trail of write windows a restart cut short.
// The elevation overlay lives in memory only (by design), so after a restart no
// window is in force, but a window whose latest window row is still granted or
// extended has no terminal row. For each such session it writes one:
//
//   - until > now: `ended`, reason "daemon restart", cred "none": the window was
//     still open when the daemon went down.
//   - until <= now: `expired`, stamped at until: the window ran out while the
//     daemon was down, so its expiry timer never wrote the row.
//
// Idempotent: the written row becomes the session's latest window row, so the
// next start finds nothing to do. One pass over the granted/extended rows, each
// checked with an indexed lookup on idx_approval_session. A failed write is
// logged and skipped (the gap is retried at the next start); the first error is
// returned for the caller to log. It returns the number of rows written.
func (a *Audit) EndOpenWindows(now time.Time) (int, error) {
	rows, err := a.db.Query(`SELECT session_id, profile, account_id, role_arn, until, reason
		FROM approval_audit a
		WHERE action IN ('granted','extended')
		  AND id = (SELECT id FROM approval_audit
		            WHERE session_id = a.session_id AND action IN ` + windowActionsSQL + `
		            ORDER BY ts DESC, id DESC LIMIT 1)`)
	if err != nil {
		return 0, err
	}
	var open []ApprovalRow
	for rows.Next() {
		var r ApprovalRow
		var until int64
		if err := rows.Scan(&r.SessionID, &r.Profile, &r.AccountID, &r.RoleARN, &until, &r.Reason); err != nil {
			rows.Close()
			return 0, err
		}
		r.Until = time.Unix(until, 0)
		open = append(open, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	written := 0
	var firstErr error
	for _, r := range open {
		if r.Until.After(now) {
			r.Time, r.Action, r.Cred, r.Reason = now, ActionEnded, credNone, "daemon restart"
		} else {
			r.Time, r.Action, r.Cred = r.Until, ActionExpired, ""
		}
		if err := a.RecordApproval(r); err != nil {
			log.Printf("broker: audit: closing the window of %s after restart: %v", r.SessionID, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		written++
	}
	return written, firstErr
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ForSession returns up to limit rows for a session, newest first.
func (a *Audit) ForSession(sessionID string, limit int) ([]Row, error) {
	rows, err := a.db.Query(`SELECT ts, session_id, profile, account_id, role_arn, source_identity, scope_hash, expires, outcome, error, elevated,
		provider, subject, installation, repos
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
		if err := rows.Scan(&ts, &r.SessionID, &r.Profile, &r.AccountID, &r.RoleARN, &r.SourceIdentity, &r.ScopeHash, &exp, &o, &r.Error, &elevated,
			&r.Provider, &r.Subject, &r.Installation, &r.Repos); err != nil {
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
