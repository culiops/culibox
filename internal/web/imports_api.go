package web

import (
	"net/http"

	"github.com/culiops/culibox/internal/bundle"
)

// handleBundleImports serves GET /api/bundle/imports: the CLAUDE.md import
// files a box would receive, resolved fresh and read-only. It is a GET on
// purpose: a bounded read (at most 64 files of 256 KiB) that writes nothing and
// builds nothing, so the "work-doing endpoints are POST" rule does not apply.
// The response carries each file's host path, for the authenticated owner's UI.
func (s *Server) handleBundleImports(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	set := s.Imports()
	if set.Files == nil {
		set.Files = []bundle.ImportFile{} // [] not null: the UI iterates it
	}
	writeJSON(w, http.StatusOK, set)
}
