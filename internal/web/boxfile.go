package web

import (
	"net/http"
	"path/filepath"
	"strings"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/boxread"
)

// handleBoxFile serves GET /api/sessions/{sid}/boxfile?path=<abs box path>.
//
// The path is a path INSIDE the box. Only the Playwright output dir
// (box.PlaywrightOutBox) is served, from the host directory that is mounted
// over it. The path must be absolute and, once cleaned, strictly under that
// dir: anything else is 403 so the client can say "only inside the box". Every
// other refusal (no such session, a host session, a symlink, a FIFO, a
// non-regular file, a missing file) is the same quiet 404, so the response
// does not tell a hostile box which of its files exist or why one was refused.
func (a *API) handleBoxFile(w http.ResponseWriter, r *http.Request, sid string) {
	p := r.URL.Query().Get("path")
	if !filepath.IsAbs(p) {
		writeErr(w, http.StatusForbidden, msgBoxOnly)
		return
	}
	rel, ok := strings.CutPrefix(filepath.Clean(p), box.PlaywrightOutBox+"/")
	if !ok || rel == "" {
		writeErr(w, http.StatusForbidden, msgBoxOnly)
		return
	}
	dir, ok := a.Sessions.BoxOutputDir(sid)
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	f, fi, ok := boxread.Open(dir, rel)
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	defer f.Close()
	serveFsFile(w, r, f, fi, filepath.Base(rel), true)
}
