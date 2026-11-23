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

// #364 round 3: git status, worktreeOf and alternates keep working for a HOST request when home (or an extra root) is
// reached through a symlink, asked by the link's spelling or the real one. The link-refusing walks judge the resolved
// spelling, never the request's; this pins that.
func TestGitStatusHomeAndRootThroughLink(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realHome, realExtra := filepath.Join(base, "data", "home"), filepath.Join(base, "data", "extra")
	mustMkdirAll(t, realHome)
	mustMkdirAll(t, realExtra)
	mustSymlink(t, filepath.Join("data", "home"), filepath.Join(base, "home"))
	mustSymlink(t, filepath.Join("data", "extra"), filepath.Join(base, "extra"))
	s := &Server{Home: filepath.Join(base, "home"), Roots: fakeRoots{realExtra}, Git: &fakeGit{}}
	for _, c := range []struct{ real, link string }{
		{realHome, filepath.Join(base, "home")},
		{realExtra, filepath.Join(base, "extra")},
	} {
		repo := filepath.Join(c.real, "repo")
		wts := altRepo(t, repo, "a")
		mustMkdirAll(t, filepath.Join(repo, "sub"))
		mustWrite(t, filepath.Join(wts[0], "f.txt"))
		for _, top := range []string{repo, filepath.Join(c.link, "repo")} {
			st := decodeStatus(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(filepath.Join(top, "sub"))))
			if st.State != "ok" || st.RootRel != "sub" || st.WorktreeOf != "" {
				t.Errorf("status %s/sub = %+v, want ok, root_rel sub, no worktreeOf", top, st)
			}
			wt := filepath.Join(top, ".claude", "worktrees", "a")
			if st := decodeStatus(t, gitPost(s.handleGitStatus, "/api/git/status", pathBody(wt))); st.State != "ok" || st.WorktreeOf != repo {
				t.Errorf("status %s = %+v, want ok, worktreeOf %s", wt, st, repo)
			}
			if got := altGet(t, s, top, "f.txt"); len(got) != 1 || got[0].Path != filepath.Join(wts[0], "f.txt") {
				t.Errorf("alternates from %s = %+v, want [%s]", top, got, filepath.Join(wts[0], "f.txt"))
			}
		}
	}
}
