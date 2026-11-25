package web

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/clone"
)

// estimateTimeout bounds one estimate: the .git walk stops at 5 s, and the HEAD
// listing of a very large repo gets the rest.
const estimateTimeout = 30 * time.Second

// cleanAbs is whether p is an absolute clean path with no control character.
func cleanAbs(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p &&
		!strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// maxEstimateBytes bounds the sizes a free-space-only estimate is sent back:
// far above any repo, and far below where the free-space rule would overflow.
const maxEstimateBytes = 1 << 50

// estimateRequest is the body of POST /api/clone/estimate. FreeOnly asks for
// the free space at dir alone, judged against sizes the page already got from
// an earlier full estimate of the same repo (GitBytes, TreeBytes): an edit of
// the copy location does not walk the repo again (#371 C4).
type estimateRequest struct {
	Of        string `json:"of"`
	Dir       string `json:"dir"`
	FreeOnly  bool   `json:"free_only"`
	GitBytes  int64  `json:"git_bytes"`
	TreeBytes int64  `json:"tree_bytes"`
}

// handleCloneEstimate is POST /api/clone/estimate {of, dir}: what a private
// copy of the repo would take and whether the disk holds it (#371). The hook
// refuses a repo a box owns before any git runs there. It is a POST because it
// does work on the host (a walk of the repo, host git, a statfs): a cookie POST
// passes csrfGuard's same-origin check, a cross-site GET navigation would not.
// With free_only it reads nothing in the repo: only the free space at dir.
func (s *Server) handleCloneEstimate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req estimateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "the body must be JSON: {\"of\": <repo>, \"dir\": <copy folder>}")
		return
	}
	if !cleanAbs(req.Of) || !cleanAbs(req.Dir) {
		writeErr(w, http.StatusBadRequest, "of and dir must be absolute clean paths")
		return
	}
	var (
		e   clone.Estimate
		err error
	)
	if req.FreeOnly {
		if req.GitBytes < 0 || req.TreeBytes < 0 || req.GitBytes > maxEstimateBytes || req.TreeBytes > maxEstimateBytes {
			writeErr(w, http.StatusBadRequest, "git_bytes and tree_bytes must be sizes from an earlier estimate")
			return
		}
		e, err = clone.FreeFor(req.Dir, req.GitBytes, req.TreeBytes)
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), estimateTimeout)
		defer cancel()
		e, err = s.CloneEstimate(ctx, req.Of, req.Dir)
	}
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, e)
	case errors.Is(err, box.ErrUnboxableWorkspace):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, box.ErrBoxUnavailable):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
	default:
		log.Printf("clone estimate of %s: %v", req.Of, err)
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

// gitIsHome reports whether repo, a git repo root, is the user's home directory:
// the repo is a dotfiles repo at ~ and copying it would copy the whole home
// (#371). Both sides are resolved (the page's home may be unloaded or a
// symlink; the server's is the one that counts). No repo or no home is false.
func gitIsHome(home, repo string) bool {
	if home == "" || repo == "" {
		return false
	}
	return resolvedOrClean(home) == resolvedOrClean(repo)
}
