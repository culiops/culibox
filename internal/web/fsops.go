package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// File mutations: create a directory, rename, delete. Read-only browsing lives
// in fsapi.go, transfer in fsupload.go / fsarchive.go.

// maxOpBody bounds the three JSON mutation bodies. None is more than a few
// hundred bytes; the upload path already established that an unbounded request
// body is not acceptable on this surface.
const maxOpBody = 8 << 10

// Bounds for the delete measurement. Generous — they only decide when the
// dialog says "over N files" instead of a number, exactly as the archive probe
// already does when it short-circuits.
const (
	deleteMeasureMaxBytes = 1 << 40
	deleteMeasureMaxFiles = 100000
)

// decodeOpBody applies the guards every mutation shares and decodes the body.
// It writes the response itself on failure and reports whether to continue.
//
// sameOrigin is checked here even though csrfGuard already covers the mux: the
// same belt-and-braces handleFsUpload uses, so a future routing mistake
// downgrades the guard instead of removing it.
func decodeOpBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return false
	}
	if !sameOrigin(r) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxOpBody)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request body")
		return false
	}
	return true
}

// resolveParent is THE rule for every mutation: resolve the PARENT directory and
// keep the base name UNRESOLVED.
//
// containedPath runs EvalSymlinks over the whole path, so using it on a mutation
// target hands back the symlink's TARGET — a delete would destroy a tree the
// user never pointed at and leave the link behind, and a rename would rename the
// target and leave the link dangling. It also makes a link pointing outside home
// undeletable (403) and a dangling one undeletable (404), while /api/fs/list
// shows both.
//
// The returned root must be closed by the caller.
func (s *Server) resolveParent(abs string) (*os.Root, string, string, error) {
	if abs == "" || !filepath.IsAbs(abs) {
		return nil, "", "", errBadPath
	}
	clean := filepath.Clean(abs)
	base := filepath.Base(clean)
	// safeName is deliberately NOT used here: a file created outside the console
	// may legally carry a name it rejects, and refusing to delete it would be a
	// trap. Only the structural cases are refused.
	if base == "." || base == ".." || base == string(os.PathSeparator) {
		return nil, "", "", errBadPath
	}
	parent, err := containedDir(s.Home, filepath.Dir(clean))
	if err != nil {
		return nil, "", "", err
	}
	root, rhome, err := openHomeRoot(s.Home)
	if err != nil {
		return nil, "", "", err
	}
	relParent, err := relUnderHome(rhome, parent)
	if err != nil {
		root.Close()
		return nil, "", "", err
	}
	return root, relParent, base, nil
}

// relUnderHome expresses dir relative to the resolved home, refusing anything
// that climbs out of it.
func relUnderHome(rhome, dir string) (string, error) {
	rel, err := filepath.Rel(rhome, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", errOutsideHome
	}
	return rel, nil
}

// relOf joins a resolved parent with a base name for use against the root.
// filepath.Join cleans away the "." a top-level entry's parent produces.
func relOf(relParent, base string) string { return filepath.Join(relParent, base) }

// typedName validates a name a HUMAN typed, which is not the same problem as a
// filename a browser sent. safeName STRIPS directories — correct for an upload,
// where a Windows browser legitimately sends "C:\dir\file.txt" — but here it
// would silently turn a typed "sub/evil" into "evil" and put the folder
// somewhere the user did not ask for. Refuse instead, then reuse safeName for
// the character and length rules.
func typedName(raw string) (string, error) {
	n := strings.TrimSpace(raw)
	if n == "" || n == "." || n == ".." ||
		strings.ContainsRune(n, '/') || strings.ContainsRune(n, '\\') {
		return "", errBadName
	}
	return safeName(n)
}

type mkdirReq struct {
	Dir  string `json:"dir"`
	Name string `json:"name"`
}

// handleFsMkdir serves POST /api/fs/mkdir — a new directory inside an existing
// one. root.Mkdir fails when the name exists in any form, symlink included, so
// there is no check-then-create window.
func (s *Server) handleFsMkdir(w http.ResponseWriter, r *http.Request) {
	var req mkdirReq
	if !decodeOpBody(w, r, &req) {
		return
	}
	name, err := typedName(req.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad name")
		return
	}
	dir, err := containedDir(s.Home, req.Dir)
	if err != nil {
		if errors.Is(err, errNotDir) {
			writeErr(w, http.StatusBadRequest, "not a directory")
			return
		}
		writeFsErr(w, err)
		return
	}
	root, rhome, err := openHomeRoot(s.Home)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "cannot open home")
		return
	}
	defer root.Close()
	relParent, err := relUnderHome(rhome, dir)
	if err != nil {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := root.Mkdir(relOf(relParent, name), 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			writeErr(w, http.StatusConflict, "name already exists")
			return
		}
		writeErr(w, http.StatusInternalServerError, "cannot create directory")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": filepath.Join(dir, name)})
}
