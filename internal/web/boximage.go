package web

import "net/http"

// handleBoxImage serves GET /api/box-image: the running box image, the image
// for this version and how they compare (boximagecheck.Report). The hook reads
// docker and the cache file only, never the network.
func (s *Server) handleBoxImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.BoxImage == nil {
		writeErr(w, http.StatusNotFound, "box image information is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, s.BoxImage(r.Context()))
}
