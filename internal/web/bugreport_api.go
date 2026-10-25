package web

import "net/http"

// handleBugReport serves GET /api/bugreport: the redacted diagnostic bundle and
// a prefilled issue link, built on demand. The bundle describes the machine, so
// it is the session owner's alone: a bearer token is refused, like token
// management. One build runs at a time because doctor probes the host; a second
// request while one is running is refused rather than queued.
func (s *Server) handleBugReport(w http.ResponseWriter, r *http.Request) {
	if credKindOf(r) != credCookie {
		writeErr(w, http.StatusForbidden, "the bug report requires a session")
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.BugReport == nil {
		writeErr(w, http.StatusServiceUnavailable, "bug report unavailable")
		return
	}
	if !s.bugMu.TryLock() {
		writeErr(w, http.StatusTooManyRequests, "a report is already being built")
		return
	}
	defer s.bugMu.Unlock()
	md, issue := s.BugReport(r.Context())
	// The bundle is about this machine; no cache or proxy may keep a copy.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"markdown": md, "issue_url": issue})
}
