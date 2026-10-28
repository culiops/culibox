package web

import (
	"encoding/csv"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/culiops/agent-box/internal/auditlog"
)

// AuditReader is the read side of audit.db (auditlog.Reader). Nil on the
// Server = the audit page is off: no routes, "audit": false in /api/meta.
type AuditReader interface {
	Query(auditlog.Query) (auditlog.Page, error)
	Summary(from, to int64) (auditlog.Summary, error)
	Each(auditlog.Query, int, func(auditlog.Event) error) error
}

// auditCSVMax is the row cap of one CSV export. A variable only so tests can
// lower it; production never writes it.
var auditCSVMax = auditlog.MaxCSV

const (
	auditDefaultRange = 7 * 24 * time.Hour
	auditCSVHeader    = "time_utc,session,kind,what,outcome,count,actor,reason"
)

// auditGate answers the checks every audit route shares and reports whether the
// request may proceed: GET only, and the signed-in browser only (a bearer
// token, a PAT or no credential gets 403).
func auditGate(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return false
	}
	if credKindOf(r) != credCookie {
		writeErr(w, http.StatusForbidden, "audit is read from the web UI")
		return false
	}
	return true
}

// auditTimes parses ?from= and ?to= (unix seconds). to defaults to now and
// from to seven days before to. The to bound is always explicit: auditlog reads
// to == 0 as "no upper bound", which no HTTP caller may ask for.
func auditTimes(r *http.Request) (from, to int64, err error) {
	q := r.URL.Query()
	to = time.Now().Unix()
	if v := q.Get("to"); v != "" {
		if to, err = strconv.ParseInt(v, 10, 64); err != nil || to <= 0 {
			return 0, 0, auditlog.ErrBadQuery
		}
	}
	from = max(to-int64(auditDefaultRange/time.Second), 0)
	if v := q.Get("from"); v != "" {
		if from, err = strconv.ParseInt(v, 10, 64); err != nil || from < 0 {
			return 0, 0, auditlog.ErrBadQuery
		}
	}
	if to < from {
		return 0, 0, auditlog.ErrBadQuery
	}
	return from, to, nil
}

// auditQuery builds the auditlog query from the request. Values are validated
// by auditlog (filter, session, cursor, limit); only the numbers are parsed here.
func auditQuery(r *http.Request) (auditlog.Query, error) {
	from, to, err := auditTimes(r)
	if err != nil {
		return auditlog.Query{}, err
	}
	p := r.URL.Query()
	q := auditlog.Query{Filter: p.Get("kind"), Session: p.Get("session"), From: from, To: to, Before: p.Get("before")}
	if v := p.Get("limit"); v != "" {
		if q.Limit, err = strconv.Atoi(v); err != nil {
			return auditlog.Query{}, auditlog.ErrBadQuery
		}
	}
	return q, nil
}

// auditFail maps a reader error to a response. Detail never leaves the server.
func auditFail(w http.ResponseWriter, what string, err error) {
	if errors.Is(err, auditlog.ErrBadQuery) {
		writeErr(w, http.StatusBadRequest, "bad audit query")
		return
	}
	log.Printf("audit %s: %v", what, err)
	writeErr(w, http.StatusInternalServerError, "query failed")
}

// handleAudit serves GET /api/audit?kind=&session=&from=&to=&before=&limit=:
// one newest-first page of the unified audit stream.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if !auditGate(w, r) {
		return
	}
	q, err := auditQuery(r)
	if err != nil {
		auditFail(w, "list", err)
		return
	}
	page, err := s.Audit.Query(q)
	if err != nil {
		auditFail(w, "list", err)
		return
	}
	if page.Events == nil {
		page.Events = []auditlog.Event{}
	}
	writeJSON(w, http.StatusOK, page)
}

// handleAuditSummary serves GET /api/audit/summary?from=&to=: the range's
// counts plus the retention policy the page footer states.
func (s *Server) handleAuditSummary(w http.ResponseWriter, r *http.Request) {
	if !auditGate(w, r) {
		return
	}
	from, to, err := auditTimes(r)
	if err != nil {
		auditFail(w, "summary", err)
		return
	}
	sum, err := s.Audit.Summary(from, to)
	if err != nil {
		auditFail(w, "summary", err)
		return
	}
	if sum.Sessions == nil {
		sum.Sessions = []string{}
	}
	writeJSON(w, http.StatusOK, struct {
		auditlog.Summary
		RetentionDays int `json:"retention_days"`
	}{sum, s.AuditRetentionDays})
}

// csvCell neutralises a spreadsheet formula: a cell that starts with =, +, -,
// @, tab or CR is prefixed with a single quote so Excel and Sheets read text.
func csvCell(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

// handleAuditCSV serves GET /api/audit.csv?<same filters>: the range as a
// download, newest first, at most auditCSVMax rows. The header is written only
// once the first row exists (or the walk ended empty), so a failing first read
// is still a clean 500.
func (s *Server) handleAuditCSV(w http.ResponseWriter, r *http.Request) {
	if !auditGate(w, r) {
		return
	}
	q, err := auditQuery(r)
	if err != nil {
		auditFail(w, "csv", err)
		return
	}
	q.Limit = 0 // a page size means nothing to an export
	max := min(auditCSVMax, auditlog.MaxCSV)

	var cw *csv.Writer
	start := func() {
		h := w.Header()
		h.Set("Content-Type", "text/csv; charset=utf-8")
		h.Set("Content-Disposition", `attachment; filename="culibox-audit-`+
			time.Unix(q.From, 0).UTC().Format("20060102")+"-"+time.Unix(q.To, 0).UTC().Format("20060102")+`.csv"`)
		h.Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		cw = csv.NewWriter(w)
		_ = cw.Write(strings.Split(auditCSVHeader, ","))
	}
	n, last := 0, ""
	err = s.Audit.Each(q, max, func(e auditlog.Event) error {
		if cw == nil {
			start()
		}
		n++
		last = e.Cursor
		count := ""
		if e.Count > 0 {
			count = strconv.Itoa(e.Count)
		}
		return cw.Write([]string{
			time.Unix(e.TS, 0).UTC().Format(time.RFC3339),
			csvCell(e.Session), csvCell(e.Kind), csvCell(e.What), csvCell(e.Outcome),
			count, csvCell(e.Actor), csvCell(e.Reason),
		})
	})
	if err != nil {
		if cw == nil {
			auditFail(w, "csv", err)
			return
		}
		// Rows already went out: the status is spent. Say so in the file.
		log.Printf("audit csv: %v", err)
		cw.Flush()
		_, _ = w.Write([]byte("# export failed\n"))
		return
	}
	if cw == nil {
		start()
	}
	cw.Flush()
	if n >= max && s.auditHasMore(q, last) {
		_, _ = w.Write([]byte("# truncated at " + strconv.Itoa(max) + " rows\n"))
	}
}

// auditHasMore reports whether a row follows the cursor of the last exported
// one. A failed probe reads as "no": the file is still correct, just unmarked.
func (s *Server) auditHasMore(q auditlog.Query, last string) bool {
	q.Before, q.Limit = last, 1
	p, err := s.Audit.Query(q)
	return err == nil && len(p.Events) > 0
}
