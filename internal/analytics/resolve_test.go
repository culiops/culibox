package analytics

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolve(t *testing.T) {
	profiles := []ProfileRef{
		{Name: "dev", DefaultCwd: "/work/clone-a", CloneOf: "/work/repo-a", Account: "acc"},
		{Name: "solo", DefaultCwd: "/work/solo", Account: "acc"}, // not a clone box
		{Name: "p", Account: "acc"}, // an in-place box profile
	}
	convKey := func(ws, acct, prof string) string {
		if prof == "p" && ws == "/work/inplace" {
			return "key-p"
		}
		return ""
	}
	hostRoot := func(cwd string) string {
		switch cwd {
		case "/r/sub", "/r":
			return "/r"
		}
		return ""
	}
	env := ResolveEnv{
		Profiles:  profiles,
		CloneDirs: []string{"/work/old-clone", "/work/clone-a"},
		ConvKey:   convKey,
		RepoRoot:  hostRoot,
	}
	fold := env
	fold.Profiles = []ProfileRef{{Name: "Dev", DefaultCwd: "/Work/Clone", CloneOf: "/Work/Repo", Account: "acc"}}
	fold.CloneDirs = nil
	fold.FoldCase = true
	noFold := fold
	noFold.FoldCase = false

	legacyEnv := ResolveEnv{
		Profiles: []ProfileRef{
			{Name: "dev", DefaultCwd: "/work/clone-a", CloneOf: "/work/repo-a"},
			{Name: "bare", DefaultCwd: "/work/bare-clone"},
		},
		CloneDirs: []string{"/work/old-clone", "/work/clone-a"},
	}

	tests := []struct {
		name  string
		d     DimIn
		env   ResolveEnv
		known []string
		want  Resolved
	}{
		{"clone exact", DimIn{Cwd: "/work/clone-a", Runtime: "box", RootKey: "dev"}, env, nil,
			Resolved{Workspace: "/work/repo-a", Profile: "dev", Via: "clone", ViaPath: "/work/clone-a"}},
		{"clone subdir", DimIn{Cwd: "/work/clone-a/pkg/x", Runtime: "box", RootKey: "dev"}, env, nil,
			Resolved{Workspace: "/work/repo-a", Profile: "dev", Via: "clone", ViaPath: "/work/clone-a"}},
		{"clone trailing slash", DimIn{Cwd: "/work/clone-a/", Runtime: "box"}, env, nil,
			Resolved{Workspace: "/work/repo-a", Profile: "dev", Via: "clone", ViaPath: "/work/clone-a"}},
		{"clone prefix is a path boundary, not a string prefix", DimIn{Cwd: "/work/clone-ab", Runtime: "box"}, env, nil,
			Resolved{Workspace: "/work/clone-ab", Via: "inplace"}},
		{"profile without clone_of is not a clone", DimIn{Cwd: "/work/solo/x", Runtime: "box"}, env, nil,
			Resolved{Workspace: "/work/solo/x", Via: "inplace"}},
		{"worktree", DimIn{Cwd: "/work/repo-z/.claude/worktrees/w1", Runtime: "box"}, env, nil,
			Resolved{Workspace: "/work/repo-z", Via: "worktree", ViaPath: "/work/repo-z/.claude/worktrees/w1"}},
		{"worktree subdir", DimIn{Cwd: "/work/repo-z/.claude/worktrees/w1/src", Runtime: "box"}, env, nil,
			Resolved{Workspace: "/work/repo-z", Via: "worktree", ViaPath: "/work/repo-z/.claude/worktrees/w1"}},
		{".worktrees", DimIn{Cwd: "/work/repo-z/.worktrees/w2", Runtime: "box"}, env, nil,
			Resolved{Workspace: "/work/repo-z", Via: "worktree", ViaPath: "/work/repo-z/.worktrees/w2"}},
		{"worktree marker with no name is not a worktree", DimIn{Cwd: "/work/repo-z/.worktrees", Runtime: "box"}, env, nil,
			Resolved{Workspace: "/work/repo-z/.worktrees", Via: "inplace"}},
		{"worktree in clone", DimIn{Cwd: "/work/clone-a/.claude/worktrees/w3/src", Runtime: "box", RootKey: "dev"}, env, nil,
			Resolved{Workspace: "/work/repo-a", Profile: "dev", Via: "worktree", ViaPath: "/work/clone-a/.claude/worktrees/w3"}},
		{"worktree in in-place box names the profile", DimIn{Cwd: "/work/inplace/.worktrees/w", Runtime: "box", RootKey: ".conversations/key-p"}, env, nil,
			Resolved{Workspace: "/work/inplace", Profile: "p", Via: "worktree", ViaPath: "/work/inplace/.worktrees/w"}},
		{"host worktree goes to the host repo root", DimIn{Cwd: "/r/.claude/worktrees/w", Runtime: "host"}, env, nil,
			Resolved{Workspace: "/r", Via: "worktree", ViaPath: "/r/.claude/worktrees/w"}},
		{"host subdir", DimIn{Cwd: "/r/sub", Runtime: "host"}, env, nil,
			Resolved{Workspace: "/r", Via: "host"}},
		{"host non-repo", DimIn{Cwd: "/not/a/repo", Runtime: "host"}, env, nil,
			Resolved{Workspace: "/not/a/repo", Via: "host"}},
		{"host with nil RepoRoot is the cwd", DimIn{Cwd: "/r/sub", Runtime: "host"}, ResolveEnv{}, nil,
			Resolved{Workspace: "/r/sub", Via: "host"}},
		{"host in a clone dir folds to the clone's repo, profile stays empty", DimIn{Cwd: "/work/clone-a/x", Runtime: "host"}, env, nil,
			Resolved{Workspace: "/work/repo-a", Via: "clone", ViaPath: "/work/clone-a"}},
		{"in-place box, no key match", DimIn{Cwd: "/work/other", Runtime: "box", RootKey: ".conversations/zzz"}, env, nil,
			Resolved{Workspace: "/work/other", Via: "inplace"}},
		{"in-place box, key match", DimIn{Cwd: "/work/inplace", Runtime: "box", RootKey: ".conversations/key-p"}, env, nil,
			Resolved{Workspace: "/work/inplace", Profile: "p", Via: "inplace"}},
		{"in-place box, key of a plain box root does not match", DimIn{Cwd: "/work/inplace", Runtime: "box", RootKey: "key-p"}, env, nil,
			Resolved{Workspace: "/work/inplace", Via: "inplace"}},
		{"in-place box, nil ConvKey", DimIn{Cwd: "/work/inplace", Runtime: "box", RootKey: ".conversations/key-p"},
			ResolveEnv{Profiles: profiles}, nil,
			Resolved{Workspace: "/work/inplace", Via: "inplace"}},
		{"in-place box, ConvKey returns empty never matches", DimIn{Cwd: "/work/inplace", Runtime: "box", RootKey: ".conversations/"},
			ResolveEnv{Profiles: profiles, ConvKey: func(string, string, string) string { return "" }}, nil,
			Resolved{Workspace: "/work/inplace", Via: "inplace"}},
		{"unknown runtime is handled as a box", DimIn{Cwd: "/work/other/", Runtime: ""}, env, nil,
			Resolved{Workspace: "/work/other", Via: "inplace"}},
		{"legacy unique", DimIn{Project: "-work-known-app"}, legacyEnv, []string{"/work/known/app", "/work/other"},
			Resolved{Workspace: "/work/known/app", Via: "legacy"}},
		{"legacy unique, duplicates of one candidate count once", DimIn{Project: "-work-known-app"}, legacyEnv,
			[]string{"/work/known/app", "/work/known/app/", "/work/other"},
			Resolved{Workspace: "/work/known/app", Via: "legacy"}},
		{"legacy ambiguous", DimIn{Project: "-work-known-app"}, legacyEnv, []string{"/work/known/app", "/work/known-app"},
			Resolved{Workspace: "-work-known-app", Via: "legacy", Unmatched: true}},
		{"legacy none", DimIn{Project: "-work-nothing"}, legacyEnv, []string{"/work/known/app"},
			Resolved{Workspace: "-work-nothing", Via: "legacy", Unmatched: true}},
		{"legacy clone dir maps to its profile's clone_of", DimIn{Project: "-work-clone-a"}, legacyEnv, nil,
			Resolved{Workspace: "/work/repo-a", Via: "legacy"}},
		{"legacy clone dir and its clone_of are one workspace", DimIn{Project: "-work-clone-a"}, legacyEnv, []string{"/work/repo-a", "/work/clone-a"},
			Resolved{Workspace: "/work/repo-a", Via: "legacy"}},
		{"legacy clone dir with no clone_of stays the clone dir", DimIn{Project: "-work-bare-clone"}, legacyEnv, nil,
			Resolved{Workspace: "/work/bare-clone", Via: "legacy"}},
		{"legacy clone dir with no profile stays the clone dir", DimIn{Project: "-work-old-clone"}, legacyEnv, nil,
			Resolved{Workspace: "/work/old-clone", Via: "legacy"}},
		{"legacy clone_of is a candidate", DimIn{Project: "-work-repo-a"}, legacyEnv, nil,
			Resolved{Workspace: "/work/repo-a", Via: "legacy"}},
		{"legacy with no config and no known", DimIn{Project: "-x"}, ResolveEnv{}, nil,
			Resolved{Workspace: "-x", Via: "legacy", Unmatched: true}},
		{"case fold", DimIn{Cwd: "/work/CLONE/Sub", Runtime: "box", RootKey: "Dev"}, fold, nil,
			Resolved{Workspace: "/Work/Repo", Profile: "Dev", Via: "clone", ViaPath: "/Work/Clone"}},
		{"case fold worktree marker", DimIn{Cwd: "/WORK/clone/.Claude/Worktrees/w", Runtime: "box"}, fold, nil,
			Resolved{Workspace: "/Work/Repo", Profile: "Dev", Via: "worktree", ViaPath: "/WORK/clone/.Claude/Worktrees/w"}},
		{"case fold keeps a path boundary", DimIn{Cwd: "/work/CLONEx", Runtime: "box"}, fold, nil,
			Resolved{Workspace: "/work/CLONEx", Via: "inplace"}},
		{"no fold without FoldCase", DimIn{Cwd: "/work/CLONE/Sub", Runtime: "box"}, noFold, nil,
			Resolved{Workspace: "/work/CLONE/Sub", Via: "inplace"}},
		{"legacy known worktree cwd folds to its repo prefix", DimIn{Project: "-work-known-app--worktrees-w"}, legacyEnv, []string{"/work/known/app/.worktrees/w"},
			Resolved{Workspace: "/work/known/app", Via: "legacy"}},
		{"legacy known claude worktree cwd folds to its repo prefix", DimIn{Project: "-work-known-app--claude-worktrees-w"}, legacyEnv, []string{"/work/known/app/.claude/worktrees/w"},
			Resolved{Workspace: "/work/known/app", Via: "legacy"}},
		{"legacy known clone dir subdir folds to clone_of", DimIn{Project: "-work-clone-a-sub"}, legacyEnv, []string{"/work/clone-a/sub"},
			Resolved{Workspace: "/work/repo-a", Via: "legacy"}},
		{"legacy known worktree inside a clone dir folds to clone_of", DimIn{Project: "-work-clone-a--claude-worktrees-w"}, legacyEnv, []string{"/work/clone-a/.claude/worktrees/w"},
			Resolved{Workspace: "/work/repo-a", Via: "legacy"}},
		{"legacy known subdir of a clone dir without clone_of stays itself", DimIn{Project: "-work-bare-clone-sub"}, legacyEnv, []string{"/work/bare-clone/sub"},
			Resolved{Workspace: "/work/bare-clone/sub", Via: "legacy"}},
		{"case fold legacy known clone dir in other case gets the config spelling", DimIn{Project: "-work-OLD"},
			ResolveEnv{FoldCase: true, CloneDirs: []string{"/Work/Old"}}, []string{"/work/OLD"},
			Resolved{Workspace: "/Work/Old", Via: "legacy"}},
		{"case fold legacy known clone subdir gets the config clone_of spelling", DimIn{Project: "-work-clone-Sub"},
			ResolveEnv{FoldCase: true, Profiles: []ProfileRef{{Name: "Dev", DefaultCwd: "/Work/Clone", CloneOf: "/Work/Repo"}}}, []string{"/work/CLONE/Sub"},
			Resolved{Workspace: "/Work/Repo", Via: "legacy"}},
		{"case fold legacy", DimIn{Project: "-Work-Known-App"}, ResolveEnv{FoldCase: true}, []string{"/work/known/app"},
			Resolved{Workspace: "/work/known/app", Via: "legacy"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Resolve(tc.d, tc.env, tc.known); got != tc.want {
				t.Fatalf("Resolve(%+v)\n got %+v\nwant %+v", tc.d, got, tc.want)
			}
		})
	}
}

// A box-runtime cwd never reaches RepoRoot, whatever shape it has.
func TestResolveBoxNeverCallsRepoRoot(t *testing.T) {
	env := ResolveEnv{
		Profiles: []ProfileRef{{Name: "dev", DefaultCwd: "/work/clone-a", CloneOf: "/work/repo-a"}},
		RepoRoot: func(cwd string) string {
			t.Fatalf("RepoRoot called for %q", cwd)
			return ""
		},
	}
	for _, cwd := range []string{"/work/clone-a", "/work/x", "/work/x/.claude/worktrees/w", "/work/clone-a/.worktrees/w"} {
		Resolve(DimIn{Cwd: cwd, Runtime: "box"}, env, nil)
	}
	Resolve(DimIn{Project: "-work-x"}, env, []string{"/work/x"})
}

// A box-supplied path is cleaned and nothing more: a symlink in it is not
// followed, so a box cannot make the host resolve a link it planted.
func TestResolveBoxCwdIsNotResolved(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	sub := filepath.Join(link, "sub")
	got := Resolve(DimIn{Cwd: sub, Runtime: "box"}, ResolveEnv{}, nil)
	if got.Workspace != sub || got.Via != "inplace" {
		t.Fatalf("box cwd was altered: %+v, want workspace %q", got, sub)
	}
	// A host cwd that is not a repo falls back to the normalised spelling.
	host := Resolve(DimIn{Cwd: link, Runtime: "host"}, ResolveEnv{RepoRoot: func(string) string { return "" }}, nil)
	want, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}
	if host.Workspace != want || host.Via != "host" {
		t.Fatalf("host fallback = %+v, want workspace %q", host, want)
	}
}

func TestProjectSlugMatchesClaudeEncoding(t *testing.T) {
	if got := projectSlug("/home/x/my.app_v2"); got != "-home-x-my-app-v2" {
		t.Fatalf("projectSlug = %q", got)
	}
}
