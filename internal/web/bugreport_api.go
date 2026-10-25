package web

import (
	"errors"
	"net/http"
)

// ErrBugReportBusy is what a BugReport builder returns when an earlier build is
// still running. The builder owns that state, not the HTTP handler: a request
// that is cancelled stops waiting but not the doctor run behind it, so the
// handler returning must not free the slot.
var ErrBugReportBusy = errors.New("a report is already being built")

// handleBugReport serves POST /api/bugreport: the redacted diagnostic bundle and
// a prefilled issue link, built on demand. It is a POST because a build is
// expensive and side-effecting (doctor spawns processes and makes AWS calls):
// a cookie POST goes through csrfGuard's same-origin check, a cross-site GET
// navigation (SameSite=Lax sends the cookie) would not. The bundle describes the
// machine, so it is the session owner's alone: a bearer token is refused, like
// token management. A request while a build is still running is refused rather
// than queued.
func (s *Server) handleBugReport(w http.ResponseWriter, r *http.Request) {
	if credKindOf(r) != credCookie {
		writeErr(w, http.StatusForbidden, "the bug report requires a session")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.BugReport == nil {
		writeErr(w, http.StatusServiceUnavailable, "bug report unavailable")
		return
	}
	md, issue, err := s.BugReport(r.Context())
	if errors.Is(err, ErrBugReportBusy) {
		writeErr(w, http.StatusTooManyRequests, ErrBugReportBusy.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not build the report")
		return
	}
	// The bundle is about this machine; no cache or proxy may keep a copy.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"markdown": md, "issue_url": issue})
}
