package web

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/culiops/agent-box/internal/gitrepo"
)

// maxAltWorktrees bounds the directory entries scanned for linked worktrees.
const maxAltWorktrees = 64

type altCandidate struct {
	Path     string `json:"path"`
	Worktree string `json:"worktree"`
	Mtime    int64  `json:"mtime"`
}

// handleFsAlternates serves GET /api/fs/alternates?base=<abs dir>&rel=<relative>:
// where rel (a path an agent printed relative to base) exists in the live linked
// worktrees under <repo>/.claude/worktrees, newest first. A subagent working in
// a worktree prints paths relative to the main checkout; the file tree joins
// them onto base and finds nothing.
//
// Only a missing or malformed parameter is a 400. Every other refusal or failure
// is 200 with an empty list, so the answer never reveals which paths exist
// outside the viewable tree. Nothing here runs git: the lookups are bounded
// stats and one non-blocking open per candidate.
func (s *Server) handleFsAlternates(w http.ResponseWriter, r *http.Request) {
	base := r.URL.Query().Get("base")
	rel := r.URL.Query().Get("rel")
	if !filepath.IsAbs(base) || rel == "" || filepath.IsAbs(rel) {
		writeErr(w, http.StatusBadRequest, "bad path")
		return
	}
	out := []altCandidate{}
	if c := filepath.Clean(rel); c != "." && filepath.IsLocal(c) {
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
	// A main checkout only: inside a linked worktree the path already resolves
	// there, and a submodule's common dir is not named .git.
	if !ok || repo.Linked || filepath.Base(repo.Common) != ".git" {
		return nil, false
	}
	root := filepath.Dir(repo.Common)
	if !p.inGitTree(req, root) || !p.inGitTree(req, repo.Common) {
		return nil, false
	}
	sub, err := filepath.Rel(root, abs)
	if err != nil || !(sub == "." || filepath.IsLocal(sub)) {
		return nil, false
	}

	out := []altCandidate{}
	for _, wt := range gitrepo.LinkedWorktrees(root, repo.Common, maxAltWorktrees) {
		// LinkedWorktrees follows a linked .claude or .claude/worktrees; a
		// worktree reached through any link is not served.
		if r, err := filepath.EvalSymlinks(wt); err != nil || r != wt {
			continue
		}
		cand := filepath.Join(wt, sub, rel)
		if r, err := filepath.Rel(wt, cand); err != nil || !filepath.IsLocal(r) {
			continue
		}
		if mt, ok := p.altFile(req, cand); ok {
			out = append(out, altCandidate{Path: cand, Worktree: filepath.Base(wt), Mtime: mt})
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
