// Package auditlog reads the five audit tables that live in <data_dir>/audit.db
// (credential_audit, approval_audit, egress_audit, cred_audit, store_audit) as
// one time-ordered event stream: bounded, keyset-paginated, read-only.
//
// Every query uses `?` parameters only; table and column names are constants in
// this file, never request text. Reads are always bounded by an indexed ts range
// and a hard row cap.
package auditlog

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/egress"
	_ "modernc.org/sqlite" // pure-Go driver, registered as "sqlite"
)

// Event kinds (the Event.Kind field).
const (
	KindCredential = "credential"
	KindElevation  = "elevation"
	KindSignIn     = "signin"
	KindEgress     = "egress"
	KindSettings   = "settings"
	KindStore      = "store"
)

// Caps and defaults.
const (
	MaxLimit     = 500        // rows in one page
	MaxCSV       = 10000      // rows in one CSV export
	MaxSessions  = 200        // distinct sessions in a Summary
	MaxWindow    = 31 * 86400 // widest [From, To] a read accepts, in seconds (31 days)
	defaultLimit = 100
)

// ErrBadQuery is returned for any invalid Query, cursor or range.
var ErrBadQuery = errors.New("bad audit query")

// Event is one audit row in display form.
type Event struct {
	TS       int64  `json:"ts"`     // unix seconds
	Cursor   string `json:"cursor"` // opaque keyset position of this row
	Kind     string `json:"kind"`
	Session  string `json:"session,omitempty"`
	What     string `json:"what"`             // one line, masked
	Parts    []Part `json:"parts,omitempty"`  // What split so the page can set identifiers apart; see Part
	Outcome  string `json:"outcome"`          // raw word: minted, granted, denied, created, ...
	Actor    string `json:"actor,omitempty"`  // "you" for a cookie approval, "API token" for bearer, else the actor column
	Reason   string `json:"reason,omitempty"` // elevation reason, masked
	Count    int    `json:"count,omitempty"`  // egress: 1 + suppressed when > 1
	Elevated bool   `json:"elevated,omitempty"`
	Provider string `json:"provider,omitempty"` // credential rows only: aws | github | static, derived at read, not stored
}

// Part is one run of an Event's description. ID marks an identifier the page
// sets in mono (a profile, entry, account, role, host:port, token, app or value
// name). Joined, the Texts of Parts are What, except a store unlock whose ttl
// the parts spell in plain words ("for 7 days") while What keeps "ttl=168h0m0s".
// Parts is empty when it could not be kept faithful to What (see describe).
type Part struct {
	Text string `json:"text"`
	ID   bool   `json:"id,omitempty"`
}

// Query selects one page. Filter is the HTTP ?kind= value: "" (all),
// "credentials" (credential + signin), "elevations", "egress" or "settings"
// (settings + store). From and To are inclusive unix seconds. To == 0 means no
// upper bound (an open-ended read); callers that expose this over HTTP should
// always pass an explicit To. Before is the Cursor of the last row of the
// previous page. A bounded range may span at most MaxWindow (31 days), else
// ErrBadQuery; only the unbounded To == 0 read is exempt, and no HTTP caller may
// make it.
type Query struct {
	Filter, Session string
	From, To        int64
	Before          string
	Limit           int
}

// Page is one page of events, newest first. Total counts every row matching
// the filters (ignoring Before); Next is set when more rows follow.
type Page struct {
	Events []Event `json:"events"`
	Total  int     `json:"total"`
	Next   string  `json:"next,omitempty"`
}

// Summary counts the range for the Audit page header.
type Summary struct {
	Issued        int      `json:"issued"`
	Refused       int      `json:"refused"`
	Elevations    int      `json:"elevations"`
	EgressAllowed int      `json:"egress_allowed"`
	EgressDenied  int      `json:"egress_denied"`
	Sessions      []string `json:"sessions"` // distinct session ids in range, newest activity first
}

var (
	cursorRE  = regexp.MustCompile(`^\d{1,12}\.[1-5]\.\d{1,19}$`)
	sessionRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,80}$`)
)

// Reader reads audit.db. It holds its own handle; the schema is ensured at Open.
type Reader struct{ db *sql.DB }

// Open ensures both writers' schemas (and the ts indexes) exist in the file at
// path, then opens its own handle with the same DSN form they use.
func Open(path string) (*Reader, error) {
	b, err := broker.OpenAudit(path)
	if err != nil {
		return nil, err
	}
	if err := b.Close(); err != nil {
		return nil, err
	}
	e, err := egress.OpenAudit(path)
	if err != nil {
		return nil, err
	}
	if err := e.Close(); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	return &Reader{db: db}, nil
}

func (r *Reader) Close() error { return r.db.Close() }

// Per-table rank in the cursor and the outer ORDER BY.
const (
	srcCredential = 5
	srcApproval   = 4
	srcEgress     = 3
	srcCred       = 2
	srcStore      = 1
)

// signinActions are the approval_audit actions that are sign-ins, not elevations.
const signinActions = `('sso_login_ok','sso_login_failed')`

// table describes how one audit table is read into the shared row shape
// (ts, src, id, session, a..f, n1..n3). The slot meaning per table is fixed
// in describe.go. All fields are compile-time constants.
type table struct {
	src    int
	name   string
	cols   string // the select list after "ts, <src>, id"
	sess   bool   // has a session_id column
	filter string // extra constant WHERE clause for the "credentials" / "elevations" splits
}

var (
	tCredential = table{srcCredential, "credential_audit",
		"session_id, provider, entry, subject, account_id, role_arn, outcome, elevated, repos, mode = 'sso_direct'", true, ""}
	tApproval = table{srcApproval, "approval_audit",
		"session_id, action, entry, account_id, profile, reason, cred, minutes, 0, 0", true, ""}
	tEgress = table{srcEgress, "egress_audit",
		"session_id, host, outcome, '', '', '', '', port, bytes_down, suppressed", true, ""}
	tCred = table{srcCred, "cred_audit",
		"'', action, kind, name, actor, detail, '', 0, 0, 0", false, ""}
	tStore = table{srcStore, "store_audit",
		"'', action, actor, detail, '', '', '', 0, 0, 0", false, ""}
)

// tablesFor lists the tables (with their per-table WHERE) a filter includes.
func tablesFor(filter string) ([]table, error) {
	signin, elev := tApproval, tApproval
	signin.filter = "action IN " + signinActions
	elev.filter = "action NOT IN " + signinActions
	switch filter {
	case "":
		return []table{tCredential, tApproval, tEgress, tCred, tStore}, nil
	case "credentials":
		return []table{tCredential, signin}, nil
	case "elevations":
		return []table{elev}, nil
	case "egress":
		return []table{tEgress}, nil
	case "settings":
		return []table{tCred, tStore}, nil
	}
	return nil, ErrBadQuery
}

type cursor struct {
	ts  int64
	src int
	id  int64
}

func parseCursor(s string) (cursor, error) {
	if !cursorRE.MatchString(s) {
		return cursor{}, ErrBadQuery
	}
	p := strings.Split(s, ".")
	ts, err1 := strconv.ParseInt(p[0], 10, 64)
	src, err2 := strconv.Atoi(p[1])
	id, err3 := strconv.ParseInt(p[2], 10, 64) // 19 digits can overflow int64
	if err1 != nil || err2 != nil || err3 != nil {
		return cursor{}, ErrBadQuery
	}
	return cursor{ts, src, id}, nil
}

func formatCursor(ts int64, src int, id int64) string {
	return strconv.FormatInt(ts, 10) + "." + strconv.Itoa(src) + "." + strconv.FormatInt(id, 10)
}

// rng validates a [from, to] range; to 0 means unbounded. A bounded range wider
// than MaxWindow is refused, so no read costs more than 31 days of rows, whatever
// the retention. Query, Each and Summary all pass through here: it is the one
// place the cap is enforced.
func rng(from, to int64) (int64, int64, error) {
	if from < 0 || to < 0 || (to != 0 && to < from) || (to != 0 && to-from > MaxWindow) {
		return 0, 0, ErrBadQuery
	}
	if to == 0 {
		to = 1<<63 - 1
	}
	return from, to, nil
}

// where builds the WHERE clause and args for one table: the ts range, the
// table's own filter, the session filter, and (when cur != nil) the keyset
// predicate "strictly after the cursor in ts DESC, src DESC, id DESC order".
// src is constant per table, so the predicate is picked here, not in SQL.
//
// The cursor is folded into the ts range bound, not added as a separate
// filter: a deep cursor then starts the index range at the cursor instead of
// scanning from the top of the range. Only the same-src case keeps a residual
// id test, and only for rows at ts = T.
func where(t table, q Query, from, to int64, cur *cursor) (string, []any, bool) {
	if q.Session != "" && !t.sess {
		return "", nil, false // settings and store rows have no session
	}
	hi := to
	var resid string
	var resArgs []any
	if cur != nil {
		switch {
		case t.src > cur.src:
			hi = min(to, cur.ts-1)
		case t.src < cur.src:
			hi = min(to, cur.ts)
		default:
			hi = min(to, cur.ts)
			resid, resArgs = "(ts < ? OR id < ?)", []any{cur.ts, cur.id}
		}
	}
	conds := []string{"ts BETWEEN ? AND ?"}
	args := []any{from, hi}
	if resid != "" {
		conds = append(conds, resid)
		args = append(args, resArgs...)
	}
	if t.filter != "" {
		conds = append(conds, t.filter)
	}
	if q.Session != "" {
		conds = append(conds, "session_id = ?")
		args = append(args, q.Session)
	}
	return strings.Join(conds, " AND "), args, true
}

func normalize(q Query) (Query, error) {
	if q.Limit < 0 {
		return q, ErrBadQuery
	}
	if q.Limit == 0 {
		q.Limit = defaultLimit
	}
	if q.Limit > MaxLimit {
		q.Limit = MaxLimit
	}
	if q.Session != "" && !sessionRE.MatchString(q.Session) {
		return q, ErrBadQuery
	}
	return q, nil
}

// Query returns one page of events matching q, newest first.
func (r *Reader) Query(q Query) (Page, error) { return r.page(q, true) }

func (r *Reader) page(q Query, withTotal bool) (Page, error) {
	q, err := normalize(q)
	if err != nil {
		return Page{}, err
	}
	from, to, err := rng(q.From, q.To)
	if err != nil {
		return Page{}, err
	}
	tabs, err := tablesFor(q.Filter)
	if err != nil {
		return Page{}, err
	}
	var cur *cursor
	if q.Before != "" {
		c, err := parseCursor(q.Before)
		if err != nil {
			return Page{}, err
		}
		cur = &c
	}

	total := 0
	if withTotal {
		for _, t := range tabs {
			cw, ca, ok := where(t, q, from, to, nil)
			if !ok {
				continue
			}
			var n int
			if err := r.db.QueryRow(`SELECT COUNT(*) FROM `+t.name+` WHERE `+cw, ca...).Scan(&n); err != nil {
				return Page{}, err
			}
			total += n
		}
	}
	page := Page{Events: []Event{}, Total: total}
	query, args := buildPage(tabs, q, from, to, cur)
	if query == "" {
		return page, nil
	}
	rows, err := r.db.Query(query, args...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var rr rawRow
		if err := rows.Scan(&rr.ts, &rr.src, &rr.id, &rr.session,
			&rr.a, &rr.b, &rr.c, &rr.d, &rr.e, &rr.f, &rr.n1, &rr.n2, &rr.n3); err != nil {
			return Page{}, err
		}
		if len(page.Events) == q.Limit { // the n+1 row: more follow
			page.Next = page.Events[len(page.Events)-1].Cursor
			break
		}
		page.Events = append(page.Events, describe(rr))
	}
	return page, rows.Err()
}

// buildPage returns the UNION query for one page (q already normalised and
// validated) and its arguments; "" when no table can match.
func buildPage(tabs []table, q Query, from, to int64, cur *cursor) (string, []any) {
	var parts []string
	var args []any
	for _, t := range tabs {
		w, a, ok := where(t, q, from, to, cur)
		if !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf(
			`SELECT * FROM (SELECT ts, %d AS src, id, %s FROM %s WHERE %s ORDER BY ts DESC, id DESC LIMIT ?)`,
			t.src, t.cols, t.name, w))
		args = append(args, a...)
		args = append(args, q.Limit+1)
	}
	if len(parts) == 0 {
		return "", nil
	}
	return strings.Join(parts, " UNION ALL ") + ` ORDER BY ts DESC, src DESC, id DESC LIMIT ?`, append(args, q.Limit+1)
}

// Each walks the matching events newest first, calling fn for each, until max
// rows (capped at MaxCSV) or fn returns an error. q.Before starts the walk. The
// range is held to the same MaxWindow as Query.
func (r *Reader) Each(q Query, max int, fn func(Event) error) error {
	if max <= 0 || max > MaxCSV {
		max = MaxCSV
	}
	q.Limit = MaxLimit
	for seen := 0; seen < max; {
		if rest := max - seen; rest < q.Limit {
			q.Limit = rest
		}
		p, err := r.page(q, false)
		if err != nil {
			return err
		}
		for _, e := range p.Events {
			if err := fn(e); err != nil {
				return err
			}
		}
		seen += len(p.Events)
		if p.Next == "" {
			return nil
		}
		q.Before = p.Next
	}
	return nil
}

// egressCountsSQL counts the egress rows of the range in ONE pass: allowed rows
// one each, refused rows 1 + suppressed (the proxy folds throttled refusals into
// one row); a policy change (#95) is neither. Every column it reads is in
// idx_egress_cover, so no table lookup.
const egressCountsSQL = `SELECT
	SUM(CASE WHEN outcome IN ('allowed','allowed_private') THEN 1 ELSE 0 END),
	SUM(CASE WHEN outcome IN ('allowed','allowed_private','policy_changed') THEN 0 ELSE 1 + suppressed END)
	FROM egress_audit WHERE ts BETWEEN ?1 AND ?2`

// sessionsSQL lists the range's sessions, most recently active first. Its egress
// branch reads session_id and ts from idx_egress_cover as well.
const sessionsSQL = `SELECT session_id FROM (
		SELECT session_id, MAX(ts) AS m FROM credential_audit WHERE ts BETWEEN ?1 AND ?2 GROUP BY session_id
		UNION ALL SELECT session_id, MAX(ts) FROM approval_audit WHERE ts BETWEEN ?1 AND ?2 GROUP BY session_id
		UNION ALL SELECT session_id, MAX(ts) FROM egress_audit WHERE ts BETWEEN ?1 AND ?2 GROUP BY session_id)
	WHERE session_id <> '' GROUP BY session_id ORDER BY MAX(m) DESC, session_id LIMIT ?3`

// Summary counts the range [from, to] (to 0 = no upper bound; a bounded range
// wider than MaxWindow is ErrBadQuery). Cost is bounded by the window, not by
// retention: the egress counts are one ts-range pass over the covering index.
func (r *Reader) Summary(from, to int64) (Summary, error) {
	from, to, err := rng(from, to)
	if err != nil {
		return Summary{}, err
	}
	s := Summary{Sessions: []string{}}
	one := func(dst *int, query string) error {
		var n sql.NullInt64
		if err := r.db.QueryRow(query, from, to).Scan(&n); err != nil {
			return err
		}
		*dst = int(n.Int64)
		return nil
	}
	for _, c := range []struct {
		dst *int
		q   string
	}{
		{&s.Issued, `SELECT COUNT(*) FROM credential_audit WHERE ts BETWEEN ? AND ? AND outcome IN ('minted','fetched')`},
		{&s.Refused, `SELECT (SELECT COUNT(*) FROM credential_audit WHERE ts BETWEEN ?1 AND ?2 AND outcome IN ('denied_token','denied_session','denied_scope','denied_not_read_only','denied_box'))
			+ (SELECT COUNT(*) FROM approval_audit WHERE ts BETWEEN ?1 AND ?2 AND action = 'refused')`},
		{&s.Elevations, `SELECT COUNT(*) FROM approval_audit WHERE ts BETWEEN ? AND ? AND action = 'granted'`},
	} {
		if err := one(c.dst, c.q); err != nil {
			return Summary{}, err
		}
	}
	var allowed, denied sql.NullInt64
	if err := r.db.QueryRow(egressCountsSQL, from, to).Scan(&allowed, &denied); err != nil {
		return Summary{}, err
	}
	s.EgressAllowed, s.EgressDenied = int(allowed.Int64), int(denied.Int64)
	rows, err := r.db.Query(sessionsSQL, from, to, MaxSessions)
	if err != nil {
		return Summary{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return Summary{}, err
		}
		s.Sessions = append(s.Sessions, id)
	}
	return s, rows.Err()
}
