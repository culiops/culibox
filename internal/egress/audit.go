package egress

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, registered as "sqlite"
)

// Outcome classifies one tunnel attempt in the audit log.
type Outcome string

const (
	OutcomeAllowed     Outcome = "allowed"
	OutcomeDenied      Outcome = "denied"
	OutcomeBadAuth     Outcome = "bad_auth"
	OutcomeBadMethod   Outcome = "bad_method"
	OutcomePrivateAddr Outcome = "private_addr"
	// OutcomeAllowedPrivate: an allowed tunnel whose dialled address came from
	// a token entry's private exception (exact host + cidr), not from the
	// public set. Visible in the audit so a private reach is never silent.
	OutcomeAllowedPrivate Outcome = "allowed_private"
	// OutcomeDialError: the host is allowed but the upstream dial failed.
	// Recording that as "allowed" would be a lie.
	OutcomeDialError Outcome = "dial_error"
)

// maxHost is the longest DNS name; the audit store never keeps more of the
// (attacker-controlled) CONNECT target than that.
const maxHost = 253

// Row is one audited tunnel or refusal. It never carries a token, a password
// or any byte of tunnel content: the CONNECT target is all the proxy sees.
type Row struct {
	Time       time.Time     `json:"time"`
	SessionID  string        `json:"session_id"` // "" when unauthenticated
	Box        string        `json:"box"`
	Host       string        `json:"host"` // normalised, or the sanitised raw target (≤ 253 printable bytes)
	Port       int           `json:"port"`
	Outcome    Outcome       `json:"outcome"`
	BytesUp    int64         `json:"bytes_up"`
	BytesDown  int64         `json:"bytes_down"`
	Duration   time.Duration `json:"duration"`
	Suppressed int           `json:"suppressed,omitempty"` // throttled refusals folded into this row
}

// Recorder is what the proxy writes audit rows to.
type Recorder interface{ Record(Row) error }

const auditSchema = `
CREATE TABLE IF NOT EXISTS egress_audit (
  id INTEGER PRIMARY KEY,
  ts INTEGER NOT NULL,
  session_id TEXT NOT NULL DEFAULT '',
  box TEXT NOT NULL DEFAULT '',
  host TEXT NOT NULL DEFAULT '',
  port INTEGER NOT NULL DEFAULT 0,
  outcome TEXT NOT NULL,
  bytes_up INTEGER NOT NULL DEFAULT 0,
  bytes_down INTEGER NOT NULL DEFAULT 0,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  suppressed INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_egress_session ON egress_audit(session_id, ts);
CREATE INDEX IF NOT EXISTS idx_egress_ts ON egress_audit(ts);
-- Covers the Audit page's Summary (counts and session list), so that range read
-- is index-only. Created on existing databases at the next daemon start.
CREATE INDEX IF NOT EXISTS idx_egress_cover ON egress_audit(ts, session_id, outcome, suppressed);
`

// Audit is the egress audit log, a table in the same file as the broker's
// (<data_dir>/audit.db) on its own *sql.DB handle. A separate handle keeps this
// package from importing broker; SQLite with busy_timeout serialises writers.
type Audit struct{ db *sql.DB }

// OpenAudit opens (creating if needed) the egress audit table at path. Pragmas
// go in the DSN, not a post-open Exec, so every pooled connection gets them
// (same as broker.OpenAudit).
func OpenAudit(path string) (*Audit, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(auditSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("egress audit schema: %w", err)
	}
	return &Audit{db: db}, nil
}

func (a *Audit) Close() error { return a.db.Close() }

// Record appends one row. Times are stored as unix seconds, durations as
// milliseconds. Host is sanitised here as well as by the proxy, so a caller
// that forgets cannot put control bytes or an unbounded string in the store.
func (a *Audit) Record(r Row) error {
	_, err := a.db.Exec(`INSERT INTO egress_audit
		(ts, session_id, box, host, port, outcome, bytes_up, bytes_down, duration_ms, suppressed)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		r.Time.Unix(), r.SessionID, r.Box, cleanHost(r.Host), r.Port, string(r.Outcome),
		r.BytesUp, r.BytesDown, r.Duration.Milliseconds(), r.Suppressed)
	return err
}

// cleanHost keeps printable ASCII only (anything else becomes '?') and at most
// maxHost bytes.
func cleanHost(h string) string {
	var b strings.Builder
	for i := 0; i < len(h) && b.Len() < maxHost; i++ {
		c := h[i]
		if c < 0x20 || c > 0x7e {
			c = '?'
		}
		b.WriteByte(c)
	}
	return b.String()
}

// pruneGap is the pause between Prune batches, so a large backlog does not hold
// the shared audit file's write lock back to back.
const pruneGap = 50 * time.Millisecond

// Prune deletes egress rows older than before, at most batch rows per statement
// (an index scan over idx_egress_ts), pausing between batches so the proxy's
// own Record calls are not starved. It returns the number deleted. Only
// egress_audit is touched; the broker and store tables in the same file are not.
func (a *Audit) Prune(before time.Time, batch int) (int64, error) {
	if batch < 1 {
		batch = 1000
	}
	var total int64
	for {
		res, err := a.db.Exec(`DELETE FROM egress_audit WHERE id IN
			(SELECT id FROM egress_audit WHERE ts < ? ORDER BY ts LIMIT ?)`, before.Unix(), batch)
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n < int64(batch) {
			return total, nil
		}
		time.Sleep(pruneGap)
	}
}

// ForSession returns up to limit rows for a session, newest first.
func (a *Audit) ForSession(sessionID string, limit int) ([]Row, error) {
	rows, err := a.db.Query(`SELECT ts, session_id, box, host, port, outcome, bytes_up, bytes_down, duration_ms, suppressed
		FROM egress_audit WHERE session_id = ? ORDER BY ts DESC, id DESC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		var ts, ms int64
		var o string
		if err := rows.Scan(&ts, &r.SessionID, &r.Box, &r.Host, &r.Port, &o, &r.BytesUp, &r.BytesDown, &ms, &r.Suppressed); err != nil {
			return nil, err
		}
		r.Time, r.Outcome, r.Duration = time.Unix(ts, 0), Outcome(o), time.Duration(ms)*time.Millisecond
		out = append(out, r)
	}
	return out, rows.Err()
}

// SessionIDs returns every distinct session id that has an egress row, sorted
// (an index scan over idx_egress_session). See broker.Audit.SessionIDs.
func (a *Audit) SessionIDs() ([]string, error) {
	rows, err := a.db.Query(`SELECT session_id FROM egress_audit GROUP BY session_id ORDER BY 1`)
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
