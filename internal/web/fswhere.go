package web

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/culiops/culibox/internal/gitrepo"
)

// handleFsWhere serves GET /api/fs/where?path=<abs> → {"path": <resolved>, "top"?: <checkout top>}: where a file or
// folder really is, for the dock's tree to reveal it. "path" is the spelling the fs API resolves the request to (the one
// /api/fs/list answers with for a folder), so a file opened through a link can still be found under a root reached
// another way. "top" is the git checkout holding it, given only when the fs policy serves that folder in the request's
// own tree: it is what the tree is rooted at when the user reveals a file outside its root. Nothing runs git; the
// checkout is found by reading the filesystem (gitrepo.Locate). Refusals are the fs API's own (writeFsErr).
func (s *Server) handleFsWhere(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	p, err := s.fsPolicy()
	if err != nil {
		writeFsErr(w, err)
		return
	}
	req, err := p.resolve(r.URL.Query().Get("path"))
	if err != nil {
		writeFsErr(w, err)
		return
	}
	// A boxed target's remainder is only ever opened through its os.Root: stat it that way, never by path.
	f, err := openFsTarget(req, 0)
	if err != nil {
		writeOpenErr(w, err, req)
		return
	}
	fi, err := f.Stat()
	f.Close()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "stat error")
		return
	}
	resp := map[string]any{"path": req.Abs}
	if top := p.checkoutTop(req, fi.IsDir()); top != "" {
		resp["top"] = top
	}
	writeJSON(w, http.StatusOK, resp)
}

// checkoutTop is the top of the git checkout holding req (a directory when isDir, else a file), or "" when there is
// none the fs policy serves in req's own tree. Repo discovery compares paths lexically, so it starts from a link-free
// spelling that inGitTree vouches for, as gitTarget does.
func (p fsPolicy) checkoutTop(req fsTarget, isDir bool) string {
	abs, err := evalSymlinks(req.Abs)
	if err != nil {
		return ""
	}
	dir := abs
	if !isDir {
		dir = filepath.Dir(abs)
	}
	if !p.inGitTree(req, dir) {
		return ""
	}
	repo, ok := gitrepo.Locate(dir)
	if !ok || !p.inGitTree(req, repo.Top) {
		return ""
	}
	if fi, err := os.Lstat(repo.Top); err != nil || !fi.IsDir() {
		return ""
	}
	return repo.Top
}
