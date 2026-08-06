package web

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/culiops/agent-box/internal/analytics"
)

// Analytics endpoints. All are registered only when s.Analytics != nil (the
// feature is enabled). Reads are plain GETs; the ingest trigger is a POST and so
// inherits csrfGuard's same-origin check at the mux layer.

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
	rows, err := s.Analytics.ByDayModel(from, to)
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
	rows, err := s.Analytics.ByModel(from, to)
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
	rows, err := s.Analytics.ByProject(from, to)
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
	rows, err := s.Analytics.Sessions(from, to, queryLimit(r, 200, 2000))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "sessions": rows})
}

// handleAnalyticsTools → skill/plugin/MCP attribution aggregates.
func (s *Server) handleAnalyticsTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	from, to := analyticsWindow(r)
	rows, err := s.Analytics.Tools(from, to, queryLimit(r, 50, 500))
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
