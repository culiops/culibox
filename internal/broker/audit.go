package broker

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/culiops/culibox/internal/secretstore"
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

	// An sso_direct mint the read-only gate did not pass (not read-only, or
	// unverified): the credential was fetched on the host and never left it.
	OutcomeDeniedNotReadOnly Outcome = "denied_not_read_only"

	// A valid token presented from a box other than its session's (or from a
	// box the broker could not identify, or over the shared socket that boxes
	// from before the per-box sockets still mount). Provider says which path.
	OutcomeDeniedBox Outcome = "denied_box"

	// Base-role write probe: a mint made only to simulate the role's own write
	// access. The credential never leaves the host.
	OutcomeProbeMinted Outcome = "probe_minted"
	OutcomeProbeFailed Outcome = "probe_failed"

	// GitHub-provider outcomes.
	OutcomeDeniedScope  Outcome = "denied_scope"
	OutcomeNotInstalled Outcome = "not_installed"
	OutcomeScopeError   Outcome = "scope_error"
	OutcomeGitHubError  Outcome = "github_error"
	OutcomeRevoked      Outcome = "revoked"
	OutcomeRevokeFailed Outcome = "revoke_failed"

	// Static-token outcomes: a value handed out, or an entry whose file or env
	// reference could not be read.
	OutcomeFetched    Outcome = "fetched"
	OutcomeUnresolved Outcome = "unresolved"

	// The encrypted store is locked: a token or key read from it was refused.
	OutcomeStoreLocked Outcome = "store_locked"

	// The store key could not be read from the macOS Keychain: not a lock, so
	// nothing to unlock; the reply carries the fix.
	OutcomeStoreUnavailable Outcome = "store_unavailable"
)

// Credential providers, as stored in Row.Provider.
const (
	ProviderAWS    = "aws"
	ProviderGitHub = "github"
	ProviderStatic = "static"
)

// NormalizeProvider is the provider a reader shows for a stored or in-flight
// value: empty is AWS (what Record stores for it), any other value is kept.
func NormalizeProvider(p string) string {
	if p == "" {
		return ProviderAWS
	}
	return p
}

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
	// Entry names the tokens entry a fetch used (a static token, or the PAT entry
	// behind a GitHub owner); empty for rows that used none.
	Entry string `json:"entry,omitempty"`
	// Mode is the AWS binding's mode: "" for AssumeRole (and every non-AWS
	// row), config.AWSModeSSODirect for the permission set's own credentials.
	Mode string `json:"mode,omitempty"`
	// Tier is the kind of session the row belongs to: TierBox or TierHost; ""
	// when unknown (a row written before the column existed, or a token that
	// named no session).
	Tier string `json:"tier,omitempty"`
}

// Row.Tier values.
const (
	TierBox  = "box"
	TierHost = "host"
)

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
	// Entry names the AWS connection a grant was for; empty for a session with
	// the inline binding, and for every row written before connections existed.
	Entry string `json:"entry,omitempty"`
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
CREATE INDEX IF NOT EXISTS idx_credential_ts ON credential_audit(ts);
CREATE INDEX IF NOT EXISTS idx_approval_ts ON approval_audit(ts);
CREATE TABLE IF NOT EXISTS store_audit (
  id INTEGER PRIMARY KEY,
  ts INTEGER NOT NULL,
  action TEXT NOT NULL,
  actor TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_store_ts ON store_audit(ts);
CREATE TABLE IF NOT EXISTS cred_audit (
  id INTEGER PRIMARY KEY,
  ts INTEGER NOT NULL,
  action TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  actor TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_cred_ts ON cred_audit(ts);
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
		{"entry", "TEXT NOT NULL DEFAULT ''"},
		{"mode", "TEXT NOT NULL DEFAULT ''"},
		{"tier", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := addColumnIfMissing(db, "credential_audit", c[0], c[1]); err != nil {
			db.Close()
			return nil, fmt.Errorf("audit migrate: %w", err)
		}
	}
	if err := addColumnIfMissing(db, "approval_audit", "entry", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("audit migrate: %w", err)
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
		provider = ProviderAWS
	}
	_, err := a.db.Exec(`INSERT INTO credential_audit
		(ts, session_id, profile, account_id, role_arn, source_identity, scope_hash, expires, outcome, error, elevated,
		 provider, subject, installation, repos, entry, mode, tier)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.Time.Unix(), r.SessionID, r.Profile, r.AccountID, r.RoleARN, r.SourceIdentity, r.ScopeHash, exp, string(r.Outcome), r.Error, b2i(r.Elevated),
		provider, r.Subject, r.Installation, r.Repos, r.Entry, r.Mode, r.Tier)
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
		(ts, session_id, profile, account_id, role_arn, action, until, minutes, reason, cred, error, entry)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.Time.Unix(), r.SessionID, r.Profile, r.AccountID, r.RoleARN, string(r.Action), until, r.Minutes,
		sanitizeN(r.Reason, maxReasonBytes), r.Cred, sanitize(r.Error), r.Entry)
	return err
}

// maxStoreDetail bounds a store event's detail (an id or a TTL) in bytes.
const maxStoreDetail = 200

// RecordStore appends one encrypted-store event (set, delete, unlock, lock).
// It satisfies secretstore.Recorder; the error is returned so the store can
// fail closed when the event could not be recorded. An Event carries an id or a
// TTL, never a value; the fields are still one line and bounded.
func (a *Audit) RecordStore(e secretstore.Event) error {
	_, err := a.db.Exec(`INSERT INTO store_audit (ts, action, actor, detail) VALUES (?,?,?,?)`,
		time.Now().Unix(), sanitize(e.Action), sanitize(e.Actor), sanitizeN(e.Detail, maxStoreDetail))
	return err
}

// Actions of a CredEvent: a credential definition created, updated or deleted,
// a write acknowledgement, and the AWS setup calls.
const (
	CredCreated     = "created"
	CredUpdated     = "updated"
	CredDeleted     = "deleted"
	CredWriteAck    = "write_ack"
	CredRolesListed = "aws_roles_listed"
	CredChecked     = "aws_checked"
	CredSSOStarted  = "aws_sso_started"
	CredSSOResult   = "aws_sso_result"
)

// maxCredDetail bounds a credential event's detail in bytes.
const maxCredDetail = 200

// CredEvent is one row of the credential-definition audit trail. Detail is a
// short description (a template name, a setup outcome); the caller must never
// put a secret value in it. Every field is stored one line and bounded.
type CredEvent struct {
	Action, Kind, Name, Actor, Detail string
}

// RecordCred appends one credential-definition event to cred_audit. The error
// is returned so a caller can fail closed when the event could not be recorded.
func (a *Audit) RecordCred(e CredEvent) error {
	_, err := a.db.Exec(`INSERT INTO cred_audit (ts, action, kind, name, actor, detail) VALUES (?,?,?,?,?,?)`,
		time.Now().Unix(), sanitize(e.Action), sanitize(e.Kind), sanitize(e.Name), sanitize(e.Actor), sanitizeN(e.Detail, maxCredDetail))
	return err
}

// ApprovalsForSession returns up to limit approval rows for a session, newest
// first.
func (a *Audit) ApprovalsForSession(sessionID string, limit int) ([]ApprovalRow, error) {
	rows, err := a.db.Query(`SELECT ts, session_id, profile, account_id, role_arn, action, until, minutes, reason, cred, error, entry
		FROM approval_audit WHERE session_id = ? ORDER BY ts DESC, id DESC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ApprovalRow
	for rows.Next() {
		var r ApprovalRow
		var ts, until int64
		var action string
		if err := rows.Scan(&ts, &r.SessionID, &r.Profile, &r.AccountID, &r.RoleARN, &action, &until, &r.Minutes,
			&r.Reason, &r.Cred, &r.Error, &r.Entry); err != nil {
			return nil, err
		}
		r.Time, r.Action = time.Unix(ts, 0), Action(action)
		if until != 0 {
			r.Until = time.Unix(until, 0)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ActivityApprovals returns up to limit approval rows of a session for the
// activity timeline, newest first: ts >= sinceTS, and ts < beforeTS when
// beforeTS > 0 (unix seconds), only the actions that are part of a write
// window's life (sign-in rows are left out).
func (a *Audit) ActivityApprovals(sessionID string, sinceTS, beforeTS int64, limit int) ([]ApprovalRow, error) {
	rows, err := a.db.Query(`SELECT ts, session_id, profile, account_id, role_arn, action, until, minutes, reason, cred, error, entry
		FROM approval_audit WHERE session_id = ? AND ts >= ? AND (? = 0 OR ts < ?)
		AND action IN ('granted','extended','dropped','expired','ended','refused')
		ORDER BY ts DESC, id DESC LIMIT ?`, sessionID, sinceTS, beforeTS, beforeTS, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ApprovalRow
	for rows.Next() {
		var r ApprovalRow
		var ts, until int64
		var action string
		if err := rows.Scan(&ts, &r.SessionID, &r.Profile, &r.AccountID, &r.RoleARN, &action, &until, &r.Minutes,
			&r.Reason, &r.Cred, &r.Error, &r.Entry); err != nil {
			return nil, err
		}
		r.Time, r.Action = time.Unix(ts, 0), Action(action)
		if until != 0 {
			r.Until = time.Unix(until, 0)
		}
		out = append(out, r)
	}
	return out, rows.Err()
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
	rows, err := a.db.Query(`SELECT session_id, profile, account_id, role_arn, until, reason, entry
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
		if err := rows.Scan(&r.SessionID, &r.Profile, &r.AccountID, &r.RoleARN, &until, &r.Reason, &r.Entry); err != nil {
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

// auditCols is the credential_audit column list every row query selects, in
// the order queryRows reads it.
const auditCols = `ts, session_id, profile, account_id, role_arn, source_identity, scope_hash, expires, outcome, error, elevated,
	provider, subject, installation, repos, entry, mode, tier`

// ForSession returns up to limit rows for a session, newest first.
func (a *Audit) ForSession(sessionID string, limit int) ([]Row, error) {
	return a.queryRows(`SELECT `+auditCols+`
		FROM credential_audit WHERE session_id = ? ORDER BY ts DESC, id DESC LIMIT ?`, sessionID, limit)
}

// ForSessionProvider returns up to limit rows of one provider for a session,
// newest first.
func (a *Audit) ForSessionProvider(sessionID, provider string, limit int) ([]Row, error) {
	return a.queryRows(`SELECT `+auditCols+`
		FROM credential_audit WHERE session_id = ? AND provider = ? ORDER BY ts DESC, id DESC LIMIT ?`, sessionID, provider, limit)
}

// ForSessionExcept returns up to limit rows for a session whose provider is not
// the given one, newest first, so a chatty provider cannot crowd out the rest.
func (a *Audit) ForSessionExcept(sessionID, provider string, limit int) ([]Row, error) {
	return a.queryRows(`SELECT `+auditCols+`
		FROM credential_audit WHERE session_id = ? AND provider != ? ORDER BY ts DESC, id DESC LIMIT ?`, sessionID, provider, limit)
}

// activityHiddenOutcomesSQL lists the outcomes the activity timeline never shows:
// base-role probes and GitHub revokes are the daemon's own housekeeping.
const activityHiddenOutcomesSQL = `('probe_minted','probe_failed','revoked','revoke_failed')`

// ActivityRows returns up to limit credential rows of a session for the
// activity timeline, newest first: ts >= sinceTS, and ts < beforeTS when
// beforeTS > 0 (both unix seconds), without the daemon's own probe and revoke
// rows. Filtering happens in SQL so hidden rows never take a place in a page.
func (a *Audit) ActivityRows(sessionID string, sinceTS, beforeTS int64, limit int) ([]Row, error) {
	return a.queryRows(`SELECT `+auditCols+`
		FROM credential_audit WHERE session_id = ? AND ts >= ? AND (? = 0 OR ts < ?)
		AND outcome NOT IN `+activityHiddenOutcomesSQL+` ORDER BY ts DESC, id DESC LIMIT ?`,
		sessionID, sinceTS, beforeTS, beforeTS, limit)
}

// CountMinted counts the credentials a session was handed (minted, or a static
// value fetched) since sinceTS (unix seconds), on the (session_id, ts) index.
func (a *Audit) CountMinted(sessionID string, sinceTS int64) (int, error) {
	var n int
	err := a.db.QueryRow(`SELECT COUNT(*) FROM credential_audit WHERE session_id = ? AND ts >= ?
		AND outcome IN ('minted','fetched')`, sessionID, sinceTS).Scan(&n)
	return n, err
}

func (a *Audit) queryRows(query string, args ...any) ([]Row, error) {
	rows, err := a.db.Query(query, args...)
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
			&r.Provider, &r.Subject, &r.Installation, &r.Repos, &r.Entry, &r.Mode, &r.Tier); err != nil {
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

// SessionIDs returns every distinct session id that has a credential or
// approval row, sorted. The manager uses it to learn which ids were ever issued,
// so a new session never takes over an old one's history. Both queries are
// index scans (idx_audit_session, idx_approval_session).
func (a *Audit) SessionIDs() ([]string, error) {
	rows, err := a.db.Query(`SELECT session_id FROM credential_audit GROUP BY session_id
		UNION SELECT session_id FROM approval_audit GROUP BY session_id ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// tierRecorder stamps Row.Tier, in one place, from the session's view when the
// call site left it empty: a Host session is "host", a boxed one "box", an
// unknown session or a token that named none stays "". The wrapped Recorder is
// the caller's own (Broker.appr is taken from it, not from the wrapper).
type tierRecorder struct {
	Recorder
	lookup Lookup
}

func (t tierRecorder) Record(r Row) error {
	if r.Tier == "" && r.SessionID != "" && t.lookup != nil {
		if sv, ok := t.lookup(r.SessionID); ok {
			switch {
			case sv.Host:
				r.Tier = TierHost
			case sv.Boxed:
				r.Tier = TierBox
			}
		}
	}
	return t.Recorder.Record(r)
}
