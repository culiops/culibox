package web

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/culiops/culibox/internal/gitrepo"
)

// maxAltWorktrees bounds the directory entries scanned for linked worktrees.
const maxAltWorktrees = 64

type altCandidate struct {
	Path     string `json:"path"`
	Worktree string `json:"worktree"`
	Mtime    int64  `json:"mtime"`
}

// handleFsAlternates serves GET /api/fs/alternates?base=<abs dir>&rel=<relative>:
// where rel (a path an agent printed relative to base) exists in the OTHER
// checkouts of base's repository: the main checkout and every live linked
// worktree git has registered (gitrepo.Worktrees), newest first. A path printed in one
// checkout is clicked in another (a subagent works in a worktree and prints
// paths relative to the main checkout, or the agent moves between them), joins
// onto the wrong tree and finds nothing. The main checkout's candidate has
// worktree "".
//
// Only a missing or relative base is a 400. Every other refusal or failure
// (absolute, empty or climbing rel included) is 200 with an empty list, so the
// answer never reveals which paths exist outside the viewable tree. Nothing
// here runs git: the lookups are bounded stats and one non-blocking open per
// candidate.
func (s *Server) handleFsAlternates(w http.ResponseWriter, r *http.Request) {
	base := r.URL.Query().Get("base")
	rel := r.URL.Query().Get("rel")
	if !filepath.IsAbs(base) {
		writeErr(w, http.StatusBadRequest, "bad path")
		return
	}
	out := []altCandidate{}
	if c := filepath.Clean(rel); rel != "" && !filepath.IsAbs(rel) && c != "." && filepath.IsLocal(c) {
		if got, ok := s.altCandidates(base, c); ok {
			out = got
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": out})
}

func (s *Server) altCandidates(base, rel string) ([]altCandidate, bool) {
	p, err := s.fsPolicy()
	if err != nil {
		return nil, false
	}
	req, err := p.resolve(base)
	if err != nil {
		return nil, false
	}
	// Repo discovery compares paths lexically, so it is fed a link-free path in
	// the request's own tree (see inGitTree).
	abs, err := filepath.EvalSymlinks(req.Abs)
	if err != nil || !p.inGitTree(req, abs) {
		return nil, false
	}
	if fi, err := os.Lstat(abs); err != nil || !fi.IsDir() {
		return nil, false
	}
	repo, ok := gitrepo.Locate(abs)
	// A common dir not named .git is a submodule's: not a checkout family.
	if !ok || filepath.Base(repo.Common) != ".git" {
		return nil, false
	}
	root := filepath.Dir(repo.Common)
	for _, x := range []string{repo.Top, root, repo.Common} {
		if !p.inGitTree(req, x) {
			return nil, false
		}
	}
	prefix, err := filepath.Rel(repo.Top, abs)
	if err != nil || !(prefix == "." || filepath.IsLocal(prefix)) {
		return nil, false
	}

	// The other checkouts: the main root (shown as ""), then the live linked
	// worktrees. The checkout base lies in is never offered.
	type checkout struct{ dir, name string }
	cks := []checkout{{root, ""}}
	for _, wt := range gitrepo.Worktrees(repo.Common, maxAltWorktrees) {
		cks = append(cks, checkout{wt, filepath.Base(wt)})
	}
	out := []altCandidate{}
	for _, ck := range cks {
		if ck.dir == repo.Top {
			continue
		}
		// A worktree's dir comes from a text file the repository's owner writes: a checkout reached through any
		// link is not served, nor one the fs policy does not serve in the request's own tree.
		if r, err := filepath.EvalSymlinks(ck.dir); err != nil || r != ck.dir || !p.inGitTree(req, ck.dir) {
			continue
		}
		cand := filepath.Join(ck.dir, prefix, rel)
		if r, err := filepath.Rel(ck.dir, cand); err != nil || !filepath.IsLocal(r) {
			continue
		}
		if mt, ok := p.altFile(req, cand); ok {
			out = append(out, altCandidate{Path: cand, Worktree: ck.name, Mtime: mt})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Mtime != out[j].Mtime {
			return out[i].Mtime > out[j].Mtime
		}
		return out[i].Path < out[j].Path
	})
	return out, true
}

// altFile reports the mtime (unix seconds) of cand when the fs policy serves it
// in the same tree as req, no component of it is a link, and it is a regular
// file. The file is opened through its os.Root with O_NONBLOCK and judged on the
// opened descriptor, so a FIFO swapped in by a box neither blocks nor passes.
func (p fsPolicy) altFile(req fsTarget, cand string) (int64, bool) {
	if r, err := filepath.EvalSymlinks(cand); err != nil || r != cand {
		return 0, false
	}
	t, err := p.resolve(cand)
	if err != nil || t.Strict != req.Strict || (req.Strict && t.Root != req.Root) {
		return 0, false
	}
	f, err := openFsTarget(t, 0)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return 0, false
	}
	return fi.ModTime().Unix(), true
}
