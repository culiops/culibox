package web

import (
	"path/filepath"
	"testing"
)

// The status reply names the main checkout of a linked worktree (worktreeOf), read from the files git keeps: the
// tree's root header says "worktree" with it. A main checkout says nothing.
func TestGitStatusWorktreeOf(t *testing.T) {
	s, h, _ := hostGitServer(t)
	repo := filepath.Join(h, "repo")
	mainRepo(t, repo)
	mustMkdirAll(t, filepath.Join(repo, ".git", "worktrees", "w"))
	wt := filepath.Join(h, "wt")
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(repo, ".git", "worktrees", "w")+"\n")
	if st := decodeStatus(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(wt))); st.WorktreeOf != repo {
		t.Errorf("linked worktree: worktreeOf = %q, want %q", st.WorktreeOf, repo)
	}
	if st := decodeStatus(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(repo))); st.WorktreeOf != "" {
		t.Errorf("main checkout: worktreeOf = %q, want none", st.WorktreeOf)
	}
}

// A submodule-style .git file (its gitdir is not under a worktrees folder) and a bare repository's worktree (its common
// dir is not named .git: there is no main checkout) name nothing.
func TestGitStatusWorktreeOfNone(t *testing.T) {
	s, h, _ := hostGitServer(t)
	bare := filepath.Join(h, "proj.git")
	mustMkdirAll(t, filepath.Join(bare, "worktrees", "w"))
	wt := filepath.Join(h, "wt")
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(bare, "worktrees", "w")+"\n")
	if st := decodeStatus(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(wt))); st.WorktreeOf != "" {
		t.Errorf("bare repo worktree: worktreeOf = %q, want none", st.WorktreeOf)
	}
	sup := filepath.Join(h, "super")
	mainRepo(t, sup)
	mustMkdirAll(t, filepath.Join(sup, ".git", "modules", "m"))
	sub := filepath.Join(sup, "m")
	mustWrite(t, filepath.Join(sub, ".git"), "gitdir: "+filepath.Join(sup, ".git", "modules", "m")+"\n")
	if st := decodeStatus(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(sub))); st.WorktreeOf != "" {
		t.Errorf("submodule: worktreeOf = %q, want none", st.WorktreeOf)
	}
}
