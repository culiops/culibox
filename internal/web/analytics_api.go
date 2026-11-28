package web

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/culiops/culibox/internal/analytics"
)

// Analytics endpoints. All are registered only when s.Analytics != nil (the
// feature is enabled). Reads are plain GETs; the ingest trigger is a POST and so
// inherits csrfGuard's same-origin check at the mux layer.

// AgentSessionIndexer says which live culibox session holds an agent
// conversation. *session.Manager satisfies it.
type AgentSessionIndexer interface {
	AgentSessionIndex() map[string]string
}

// isDay reports whether s looks like YYYY-MM-DD, so a query param cannot inject
// arbitrary text into the date-range filter.
func isDay(s string) bool {
	if len(s) != 10 {
		return false
	}
	for i, c := range s {
		switch i {
		case 4, 7:
			if c != '-' {
				return false
			}
		default:
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

func validDay(s, def string) string {
	if isDay(s) {
		return s
	}
	return def
}

// analyticsWindow resolves the [from,to] date range, defaulting to the last 30
// days (UTC), matching how days are bucketed at ingest.
func analyticsWindow(r *http.Request) (from, to string) {
	now := time.Now().UTC()
	q := r.URL.Query()
	to = validDay(q.Get("to"), now.Format("2006-01-02"))
	from = validDay(q.Get("from"), now.AddDate(0, 0, -29).Format("2006-01-02"))
	return from, to
}

func queryLimit(r *http.Request, def, max int) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// handleAnalyticsSummary → per-(day,model) token/prompt aggregates for the window.
func (s *Server) handleAnalyticsSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	from, to := analyticsWindow(r)
	rows, err := s.Analytics.ByDayModel(from, to, analytics.ParseWhere(r.URL.Query().Get("where")))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "byDayModel": rows})
}

// handleAnalyticsModels → per-model aggregates ($, tokens, prompts, sessions) for the window.
func (s *Server) handleAnalyticsModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	from, to := analyticsWindow(r)
	rows, err := s.Analytics.ByModel(from, to, analytics.ParseWhere(r.URL.Query().Get("where")))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "byModel": rows})
}

// handleAnalyticsProjects → per-(project,model) aggregates for the window (the
// frontend derives project totals and the per-project model mix).
func (s *Server) handleAnalyticsProjects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	from, to := analyticsWindow(r)
	rows, err := s.Analytics.ByProject(from, to, analytics.ParseWhere(r.URL.Query().Get("where")))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "byProject": rows})
}

// handleAnalyticsSessions → per-session rows (tokens, prompts, tokens/prompt).
func (s *Server) handleAnalyticsSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	from, to := analyticsWindow(r)
	rows, err := s.Analytics.Sessions(from, to, queryLimit(r, 200, 2000), analytics.ParseWhere(r.URL.Query().Get("where")))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "sessions": rows})
}

// sessionCostRow is one session-costs row: the aggregate plus the culibox
// session that still holds the conversation ("" when none does).
type sessionCostRow struct {
	analytics.SessionAggRow
	LiveID string `json:"liveId"`
}

// handleAnalyticsSessionCosts → per-session cost aggregates (complete set, no
// day-ordered cap) so the session count, histogram and top-by-cost are correct
// on wide windows. Each row carries liveId, the culibox session whose agent
// conversations include the row's session id; modelCosts is the same window
// split per (session, model).
func (s *Server) handleAnalyticsSessionCosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	from, to := analyticsWindow(r)
	where := analytics.ParseWhere(r.URL.Query().Get("where"))
	rows, err := s.Analytics.SessionCosts(from, to, where)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	mc, err := s.Analytics.SessionModelCosts(from, to, where)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	var live map[string]string
	if s.AgentIndex != nil {
		live = s.AgentIndex.AgentSessionIndex()
	}
	out := make([]sessionCostRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, sessionCostRow{SessionAggRow: row, LiveID: live[row.SessionID]})
	}
	if mc == nil {
		mc = []analytics.SessionModelCost{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "sessions": out, "modelCosts": mc})
}

// handleAnalyticsWorkspaces → one row per workspace with its child rows, for the window.
func (s *Server) handleAnalyticsWorkspaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	from, to := analyticsWindow(r)
	rows, err := s.Analytics.Workspaces(from, to, analytics.ParseWhere(r.URL.Query().Get("where")))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	if rows == nil {
		rows = []analytics.WorkspaceRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "workspaces": rows})
}

// handleAnalyticsDrivers → cost per agent, skill, plugin and MCP server; the
// DriversResp fields sit at the top level next to from/to.
func (s *Server) handleAnalyticsDrivers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	from, to := analyticsWindow(r)
	d, err := s.Analytics.Drivers(from, to, analytics.ParseWhere(r.URL.Query().Get("where")))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		From string `json:"from"`
		To   string `json:"to"`
		analytics.DriversResp
	}{from, to, d})
}

// handleAnalyticsWhere → the where select's entries with their cost over the
// window. It takes no where: it lists what a where can name.
func (s *Server) handleAnalyticsWhere(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	from, to := analyticsWindow(r)
	opts, err := s.Analytics.WhereOptions(from, to)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	if opts == nil {
		opts = []analytics.WhereOption{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "options": opts})
}

// handleAnalyticsTools → skill/plugin/MCP attribution aggregates.
func (s *Server) handleAnalyticsTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	from, to := analyticsWindow(r)
	rows, err := s.Analytics.Tools(from, to, queryLimit(r, 50, 500), analytics.ParseWhere(r.URL.Query().Get("where")))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "tools": rows})
}

// handleAnalyticsIngest triggers an on-demand re-scan (POST; CSRF-guarded at the
// mux). Returns the pass stats so the UI can show "N files, M bytes".
func (s *Server) handleAnalyticsIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	stats, err := s.Analytics.Ingest()
	if errors.Is(err, analytics.ErrIngestBusy) {
		writeErr(w, http.StatusConflict, "a scan is already running; try again shortly")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ingest failed")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}
