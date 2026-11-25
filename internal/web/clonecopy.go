package web

import (
	"context"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/culiops/culibox/internal/box"
)

// estimateTimeout bounds one estimate: the .git walk stops at 5 s, and the HEAD
// listing of a very large repo gets the rest.
const estimateTimeout = 30 * time.Second

// cleanAbs is whether p is an absolute clean path with no control character.
func cleanAbs(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p &&
		!strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// handleCloneEstimate is GET /api/clone/estimate?of=<repo>&dir=<copy>: what a
// private copy of the repo would take and whether the disk holds it (#371). The
// hook refuses a repo a box owns before any git runs there.
func (s *Server) handleCloneEstimate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	of, dir := r.URL.Query().Get("of"), r.URL.Query().Get("dir")
	if !cleanAbs(of) || !cleanAbs(dir) {
		writeErr(w, http.StatusBadRequest, "of and dir must be absolute clean paths")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), estimateTimeout)
	defer cancel()
	e, err := s.CloneEstimate(ctx, of, dir)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, e)
	case errors.Is(err, box.ErrUnboxableWorkspace):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, box.ErrBoxUnavailable):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
	default:
		log.Printf("clone estimate of %s: %v", of, err)
		writeErr(w, http.StatusInternalServerError, "could not estimate the copy; see the CuliBox log")
	}
}

// copyNameOK is whether s can be the one path element of a suggested copy: a
// clean name of [A-Za-z0-9._-], not starting with a dot (no "." or "..", no
// hidden folder).
func copyNameOK(s string) bool {
	if s == "" || s[0] == '.' {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// suggestCopyDir is where a git repo's private copy goes by default (#371):
// <home>/box-clones/<profile>, or <home>/box-clones/<repo's folder name> when
// the profile name is not usable as a folder name, else "copy". The user can
// change it; the save-time checks still judge whatever is chosen.
func suggestCopyDir(home, profile, repo string) string {
	name := "copy"
	switch base := filepath.Base(repo); {
	case copyNameOK(profile):
		name = profile
	case repo != "" && copyNameOK(base):
		name = base
	}
	return filepath.Join(home, "box-clones", name)
}
