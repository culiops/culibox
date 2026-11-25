package box

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"testing"
)

// realTemp is t.TempDir() with symlinks resolved, as Wrap hands inPlaceMounts.
func realTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, at string) {
	t.Helper()
	if err := os.Symlink(target, at); err != nil {
		t.Fatal(err)
	}
}

// gitRepo makes ws a repository the way `git init` leaves it, with cfg as its
// .git/config.
func gitRepo(t *testing.T, ws, cfg string) {
	t.Helper()
	mkdirs(t, filepath.Join(ws, ".git", "hooks"))
	writeFile(t, filepath.Join(ws, ".git", "config"), cfg)
}

// wantMounts compares the set of in-box paths with ws joined to rels, and
// checks every guard is a required self-bind.
func wantMounts(t *testing.T, ws string, got []Mount, rels ...string) {
	t.Helper()
	var have, want []string
	for _, mt := range got {
		if mt.Host != mt.Box || mt.Optional {
			t.Errorf("guard %+v must be a required self-bind", mt)
		}
		have = append(have, mt.Box)
	}
	for _, r := range rels {
		want = append(want, filepath.Join(ws, r))
	}
	sort.Strings(have)
	sort.Strings(want)
	if strings.Join(have, "\n") != strings.Join(want, "\n") {
		t.Fatalf("guard mounts:\n got  %q\n want %q", have, want)
	}
}

func TestInPlaceGitDirWorkspace(t *testing.T) {
	ws := realTemp(t)
	gitRepo(t, ws, "[core]\n\tbare = false\n")
	for _, f := range []string{"CLAUDE.md", "CLAUDE.local.md", ".mcp.json"} {
		writeFile(t, filepath.Join(ws, f), "x")
	}
	got, err := inPlaceMounts(ws)
	if err != nil {
		t.Fatal(err)
	}
	wantMounts(t, ws, got, ".git", ".claude", "CLAUDE.md", "CLAUDE.local.md", ".mcp.json")
}

func TestInPlaceGitFileWorktree(t *testing.T) {
	tmp := realTemp(t)
	gitdir := filepath.Join(tmp, "main", ".git", "worktrees", "wt")
	mkdirs(t, gitdir)
	writeFile(t, filepath.Join(tmp, "main", ".git", "config"), "[core]\n\thooksPath = .githooks\n")
	writeFile(t, filepath.Join(gitdir, "commondir"), "../..\n")
	ws := filepath.Join(tmp, "ws")
	mkdirs(t, filepath.Join(ws, ".githooks"))
	writeFile(t, filepath.Join(ws, ".git"), "gitdir: "+gitdir+"\n")
	got, err := inPlaceMounts(ws)
	if err != nil {
		t.Fatal(err)
	}
	wantMounts(t, ws, got, ".git", ".claude", ".githooks")
}

func TestInPlaceNoGit(t *testing.T) {
	ws := realTemp(t)
	got, err := inPlaceMounts(ws)
	if err != nil {
		t.Fatal(err)
	}
	wantMounts(t, ws, got, ".claude")
}

// A workspace below a repository root is still under that repository's rules:
// host git at the root recurses into submodules under ws and runs hooks from a
// core.hooksPath under ws, both of which the box could plant.
func TestInPlaceSubdirOfRepo(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(t *testing.T, repo string) // ws = <repo>/sub exists
		mounts []string                        // relative to ws, besides .claude
		refuse string                          // a substring of the refusal; "" = none
	}{
		{name: "plain subdir", setup: func(t *testing.T, repo string) { gitRepo(t, repo, "[core]\n\tbare = false\n") }},
		{name: "enclosing hooksPath into ws", setup: func(t *testing.T, repo string) {
			gitRepo(t, repo, "[core]\n\thooksPath = sub/hooks\n")
			mkdirs(t, filepath.Join(repo, "sub", "hooks"))
		}, mounts: []string{"hooks"}},
		{name: "enclosing hooksPath nested in ws", setup: func(t *testing.T, repo string) {
			gitRepo(t, repo, "[core]\n\thooksPath = sub/tools/hooks\n")
			mkdirs(t, filepath.Join(repo, "sub", "tools", "hooks"))
		}, mounts: []string{"tools"}},
		{name: "enclosing hooksPath outside ws", setup: func(t *testing.T, repo string) {
			gitRepo(t, repo, "[core]\n\thooksPath = .githooks\n")
			mkdirs(t, filepath.Join(repo, ".githooks"))
		}},
		{name: "enclosing hooksPath missing in ws", setup: func(t *testing.T, repo string) {
			gitRepo(t, repo, "[core]\n\thooksPath = sub/hooks\n")
		}, refuse: "does not exist"},
		{name: "enclosing hooksPath is ws", setup: func(t *testing.T, repo string) {
			gitRepo(t, repo, "[core]\n\thooksPath = sub\n")
		}, refuse: "workspace itself"},
		{name: "enclosing submodules", setup: func(t *testing.T, repo string) {
			gitRepo(t, repo, "")
			writeFile(t, filepath.Join(repo, ".gitmodules"), "[submodule \"lib\"]\n\tpath = sub/lib\n")
		}, refuse: "trusted host"},
		{name: "enclosing include", setup: func(t *testing.T, repo string) {
			gitRepo(t, repo, "[include]\n\tpath = x\n")
		}, refuse: "include"},
		{name: "enclosing config leads into ws", setup: func(t *testing.T, repo string) {
			gitRepo(t, repo, "")
			writeFile(t, filepath.Join(repo, "sub", "cfg"), "")
			if err := os.Remove(filepath.Join(repo, ".git", "config")); err != nil {
				t.Fatal(err)
			}
			symlink(t, "../sub/cfg", filepath.Join(repo, ".git", "config"))
		}, refuse: "leads into the workspace"},
		{name: "enclosing worktree gitfile", setup: func(t *testing.T, repo string) {
			main := repo + "-main"
			t.Cleanup(func() { os.RemoveAll(main) })
			gd := filepath.Join(main, ".git", "worktrees", "wt")
			mkdirs(t, gd)
			writeFile(t, filepath.Join(main, ".git", "config"), "[core]\n\thooksPath = sub/hooks\n")
			writeFile(t, filepath.Join(gd, "commondir"), "../..\n")
			writeFile(t, filepath.Join(repo, ".git"), "gitdir: "+gd+"\n")
			mkdirs(t, filepath.Join(repo, "sub", "hooks"))
		}, mounts: []string{"hooks"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := realTemp(t)
			ws := filepath.Join(repo, "sub")
			mkdirs(t, ws)
			tc.setup(t, repo)
			got, err := inPlaceMounts(ws)
			if tc.refuse != "" {
				if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), tc.refuse) {
					t.Fatalf("want ErrUnboxableWorkspace mentioning %q, got %v (mounts %v)", tc.refuse, err, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantMounts(t, ws, got, append([]string{".claude"}, tc.mounts...)...)
		})
	}
}

// A workspace inside the enclosing repository's git directory writes its
// config and hooks directly.
func TestInPlaceRefusesWorkspaceInsideGitDir(t *testing.T) {
	repo := realTemp(t)
	gitRepo(t, repo, "")
	ws := filepath.Join(repo, ".git", "info")
	mkdirs(t, ws)
	if _, err := inPlaceMounts(ws); !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "git directory") {
		t.Fatalf("want ErrUnboxableWorkspace for a workspace inside .git, got %v", err)
	}
}

// Fix round 1, measured in a real box (--cap-drop ALL, non-root): with only the
// leaf ws/tools/hooks bound read-only, `mv tools/hooks` fails (EBUSY: a mount
// point), but `mv tools tools2 && mkdir -p tools/hooks && echo PLANTED >
// tools/hooks/pre-commit` succeeds, and host git then runs the planted hook. So
// a nested hooks dir is guarded by binding its top-level dir under ws, which
// cannot be renamed because ws itself is a mount point.
func TestInPlaceNestedHooksBindTheirTopDir(t *testing.T) {
	ws := realTemp(t)
	gitRepo(t, ws, "[core]\n\thooksPath = tools/git/hooks\n")
	mkdirs(t, filepath.Join(ws, "tools", "git", "hooks"))
	got, err := inPlaceMounts(ws)
	if err != nil {
		t.Fatal(err)
	}
	for _, mt := range got {
		if mt.Box == filepath.Join(ws, "tools", "git", "hooks") || mt.Box == filepath.Join(ws, "tools", "git") {
			t.Fatalf("a nested mount can be renamed away with its parent; bind %s instead: %v", filepath.Join(ws, "tools"), got)
		}
	}
	wantMounts(t, ws, got, ".git", ".claude", "tools")
}

func TestInPlaceCreatesClaudeDir(t *testing.T) {
	// The mode asked for is 0755; pin the umask so the test does not depend on
	// the caller's. No test in this package runs in parallel.
	defer syscall.Umask(syscall.Umask(0o022))
	ws := realTemp(t)
	if _, err := inPlaceMounts(ws); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(filepath.Join(ws, ".claude"))
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o755 {
		t.Fatalf("want a 0755 directory, got %v", fi.Mode())
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		t.Fatalf("want .claude owned by uid %d, got %+v", os.Getuid(), fi.Sys())
	}
}

func TestInPlaceHooksPath(t *testing.T) {
	cases := []struct {
		name   string
		hooks  func(ws string) string // the core.hooksPath value
		setup  func(t *testing.T, ws string)
		mounts []string // besides .git and .claude; nil + refuse=false: none
		refuse bool
	}{
		{name: "relative", hooks: func(string) string { return ".githooks" },
			setup: func(t *testing.T, ws string) { mkdirs(t, filepath.Join(ws, ".githooks")) }, mounts: []string{".githooks"}},
		{name: "absolute inside", hooks: func(ws string) string { return filepath.Join(ws, ".githooks") },
			setup: func(t *testing.T, ws string) { mkdirs(t, filepath.Join(ws, ".githooks")) }, mounts: []string{".githooks"}},
		// Nested: the top-level dir is bound, not the leaf (see TestInPlaceNestedHooksBindTheirTopDir).
		{name: "nested relative", hooks: func(string) string { return "tools/hooks" },
			setup: func(t *testing.T, ws string) { mkdirs(t, filepath.Join(ws, "tools", "hooks")) }, mounts: []string{"tools"}},
		{name: "husky v9", hooks: func(string) string { return ".husky/_" },
			setup: func(t *testing.T, ws string) { mkdirs(t, filepath.Join(ws, ".husky", "_")) }, mounts: []string{".husky"}},
		{name: "nested symlinked leaf", hooks: func(string) string { return "tools/hooks" },
			setup: func(t *testing.T, ws string) {
				mkdirs(t, filepath.Join(ws, "tools", "real"))
				symlink(t, "real", filepath.Join(ws, "tools", "hooks"))
			}, refuse: true},
		{name: "nested missing", hooks: func(string) string { return "tools/hooks" },
			setup: func(t *testing.T, ws string) { mkdirs(t, filepath.Join(ws, "tools")) }, refuse: true},
		{name: "inside .git", hooks: func(string) string { return ".git/hooks" }},
		{name: "absolute inside .git", hooks: func(ws string) string { return filepath.Join(ws, ".git", "hooks") }},
		{name: "inside .claude", hooks: func(string) string { return ".claude/hooks" }},
		{name: "dev null", hooks: func(string) string { return "/dev/null" }},
		{name: "home", hooks: func(string) string { return "~/hooks" }},
		{name: "outside", hooks: func(string) string { return "../outside" }},
		{name: "missing", hooks: func(string) string { return ".githooks" }, refuse: true},
		{name: "workspace root", hooks: func(string) string { return "." }, refuse: true},
		{name: "a file", hooks: func(string) string { return ".githooks" },
			setup: func(t *testing.T, ws string) { writeFile(t, filepath.Join(ws, ".githooks"), "x") }, refuse: true},
		{name: "symlinked parent", hooks: func(string) string { return "link/.githooks" },
			setup: func(t *testing.T, ws string) {
				mkdirs(t, filepath.Join(ws, "real", ".githooks"))
				symlink(t, "real", filepath.Join(ws, "link"))
			}, refuse: true},
		{name: "symlinked target", hooks: func(string) string { return ".githooks" },
			setup: func(t *testing.T, ws string) {
				mkdirs(t, filepath.Join(ws, "real"))
				symlink(t, "real", filepath.Join(ws, ".githooks"))
			}, refuse: true},
		// The default hooks directory, linked out of the read-only .git into
		// files the box can write.
		{name: "symlinked .git/hooks", hooks: func(string) string { return "" },
			setup: func(t *testing.T, ws string) {
				mkdirs(t, filepath.Join(ws, "githooks"))
				if err := os.Remove(filepath.Join(ws, ".git", "hooks")); err != nil {
					t.Fatal(err)
				}
				symlink(t, "../githooks", filepath.Join(ws, ".git", "hooks"))
			}, refuse: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := realTemp(t)
			cfg := "[core]\n\tbare = false\n"
			if hp := tc.hooks(ws); hp != "" {
				cfg += "\thooksPath = " + hp + "\n"
			}
			gitRepo(t, ws, cfg)
			if tc.setup != nil {
				tc.setup(t, ws)
			}
			got, err := inPlaceMounts(ws)
			if tc.refuse {
				if !errors.Is(err, ErrUnboxableWorkspace) {
					t.Fatalf("want ErrUnboxableWorkspace, got %v (mounts %v)", err, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantMounts(t, ws, got, append([]string{".git", ".claude"}, tc.mounts...)...)
		})
	}
}

// A ~ path expands to the home directory, as git expands it: one that lands
// inside the workspace is guarded like any other.
func TestInPlaceHooksPathTildeInsideWorkspace(t *testing.T) {
	home := realTemp(t)
	t.Setenv("HOME", home)
	ws := filepath.Join(home, "proj")
	gitRepo(t, ws, "[core]\n\thooksPath = ~/proj/.githooks\n")
	mkdirs(t, filepath.Join(ws, ".githooks"))
	got, err := inPlaceMounts(ws)
	if err != nil {
		t.Fatal(err)
	}
	wantMounts(t, ws, got, ".git", ".claude", ".githooks")
}

// A hooksPath spelled outside the workspace that a HOST symlink (one the box
// cannot replace) leads back into it is guarded at its real path.
func TestInPlaceHooksPathThroughHostSymlink(t *testing.T) {
	tmp := realTemp(t)
	ws := filepath.Join(tmp, "ws")
	symlink(t, ws, filepath.Join(tmp, "alias"))
	gitRepo(t, ws, "[core]\n\thooksPath = "+filepath.Join(tmp, "alias", ".githooks")+"\n")
	mkdirs(t, filepath.Join(ws, ".githooks"))
	got, err := inPlaceMounts(ws)
	if err != nil {
		t.Fatal(err)
	}
	wantMounts(t, ws, got, ".git", ".claude", ".githooks")

	// ... and refused when what it points at does not exist yet: the box could
	// create it.
	if err := os.Remove(filepath.Join(ws, ".githooks")); err != nil {
		t.Fatal(err)
	}
	if _, err := inPlaceMounts(ws); !errors.Is(err, ErrUnboxableWorkspace) {
		t.Fatalf("want ErrUnboxableWorkspace for a missing hooks dir behind a host symlink, got %v", err)
	}
}

func TestInPlaceRefuses(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, ws string)
		msg   string // a substring the refusal must carry, besides the path
	}{
		{"submodules", func(t *testing.T, ws string) {
			gitRepo(t, ws, "")
			writeFile(t, filepath.Join(ws, ".gitmodules"), "")
		}, "trusted host"},
		{"symlinked .git", func(t *testing.T, ws string) {
			gitRepo(t, filepath.Join(ws, "real"), "")
			symlink(t, "real/.git", filepath.Join(ws, ".git"))
		}, ".git"},
		{"symlinked .claude", func(t *testing.T, ws string) {
			mkdirs(t, filepath.Join(ws, "real"))
			symlink(t, "real", filepath.Join(ws, ".claude"))
		}, ".claude"},
		{"symlinked CLAUDE.md", func(t *testing.T, ws string) {
			writeFile(t, filepath.Join(ws, "doc.md"), "x")
			symlink(t, "doc.md", filepath.Join(ws, "CLAUDE.md"))
		}, "CLAUDE.md"},
		{"symlinked CLAUDE.local.md", func(t *testing.T, ws string) {
			symlink(t, "nowhere.md", filepath.Join(ws, "CLAUDE.local.md"))
		}, "CLAUDE.local.md"},
		{"symlinked AGENTS.md", func(t *testing.T, ws string) {
			writeFile(t, filepath.Join(ws, "doc.md"), "x")
			symlink(t, "doc.md", filepath.Join(ws, "AGENTS.md"))
		}, "AGENTS.md"},
		{"symlinked .mcp.json", func(t *testing.T, ws string) {
			writeFile(t, filepath.Join(ws, "m.json"), "{}")
			symlink(t, "m.json", filepath.Join(ws, ".mcp.json"))
		}, ".mcp.json"},
		{"include", func(t *testing.T, ws string) { gitRepo(t, ws, "[include]\n\tpath = x\n") }, "include"},
		{"includeIf", func(t *testing.T, ws string) { gitRepo(t, ws, "[includeIf \"gitdir:/x\"]\n\tpath = y\n") }, "include"},
		{"gitdir inside the workspace", func(t *testing.T, ws string) {
			mkdirs(t, filepath.Join(ws, "evil"))
			writeFile(t, filepath.Join(ws, "evil", "config"), "")
			writeFile(t, filepath.Join(ws, ".git"), "gitdir: ./evil\n")
		}, "gitdir"},
		{"gitdir leading back in through a host symlink", func(t *testing.T, ws string) {
			mkdirs(t, filepath.Join(ws, "evil"))
			alias := filepath.Join(filepath.Dir(ws), filepath.Base(ws)+"-alias")
			symlink(t, ws, alias)
			t.Cleanup(func() { os.Remove(alias) })
			writeFile(t, filepath.Join(ws, ".git"), "gitdir: "+filepath.Join(alias, "evil")+"\n")
		}, "gitdir"},
		{"gitdir that does not exist", func(t *testing.T, ws string) {
			writeFile(t, filepath.Join(ws, ".git"), "gitdir: /nonexistent/agent-box-test/wt\n")
		}, "gitdir"},
		{"not a gitfile", func(t *testing.T, ws string) {
			writeFile(t, filepath.Join(ws, ".git"), "hello\n")
		}, "gitdir"},
		{"commondir inside the workspace", func(t *testing.T, ws string) {
			gd := filepath.Join(filepath.Dir(ws), filepath.Base(ws)+"-gd")
			mkdirs(t, gd, filepath.Join(ws, "evil"))
			t.Cleanup(func() { os.RemoveAll(gd) })
			writeFile(t, filepath.Join(gd, "commondir"), filepath.Join(ws, "evil")+"\n")
			writeFile(t, filepath.Join(ws, ".git"), "gitdir: "+gd+"\n")
		}, "commondir"},
		{".git directory with a commondir", func(t *testing.T, ws string) {
			gitRepo(t, ws, "")
			writeFile(t, filepath.Join(ws, ".git", "commondir"), "../evil\n")
		}, "commondir"},
		{"symlinked .git/config", func(t *testing.T, ws string) {
			gitRepo(t, ws, "")
			writeFile(t, filepath.Join(ws, "cfg"), "")
			if err := os.Remove(filepath.Join(ws, ".git", "config")); err != nil {
				t.Fatal(err)
			}
			symlink(t, "../cfg", filepath.Join(ws, ".git", "config"))
		}, "config"},
		{"include in config.worktree", func(t *testing.T, ws string) {
			gitRepo(t, ws, "")
			writeFile(t, filepath.Join(ws, ".git", "config.worktree"), "[include]\npath = x\n")
		}, "include"},
		{"unparsable config", func(t *testing.T, ws string) {
			gitRepo(t, ws, "[core]\n\thooksPath = \"open\n")
		}, "config"},
		{"CLAUDE.md is a directory", func(t *testing.T, ws string) {
			mkdirs(t, filepath.Join(ws, "CLAUDE.md"))
		}, "CLAUDE.md"},
		{".claude is a file", func(t *testing.T, ws string) {
			writeFile(t, filepath.Join(ws, ".claude"), "x")
		}, ".claude"},
		{".git is a fifo", func(t *testing.T, ws string) {
			if err := syscall.Mkfifo(filepath.Join(ws, ".git"), 0o644); err != nil {
				t.Skip("mkfifo:", err)
			}
		}, ".git"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := realTemp(t)
			tc.setup(t, ws)
			got, err := inPlaceMounts(ws)
			if !errors.Is(err, ErrUnboxableWorkspace) {
				t.Fatalf("want ErrUnboxableWorkspace, got %v (mounts %v)", err, got)
			}
			if !strings.Contains(err.Error(), ws) || !strings.Contains(err.Error(), tc.msg) {
				t.Fatalf("the refusal must name %s and %q: %v", ws, tc.msg, err)
			}
		})
	}
}

func TestInPlaceRefusesUnreadableConfig(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file")
	}
	ws := realTemp(t)
	gitRepo(t, ws, "[core]\n")
	cfg := filepath.Join(ws, ".git", "config")
	if err := os.Chmod(cfg, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(cfg, 0o644) })
	if _, err := inPlaceMounts(ws); !errors.Is(err, ErrUnboxableWorkspace) {
		t.Fatalf("want ErrUnboxableWorkspace for an unreadable config, got %v", err)
	}
}

func TestGitConfigCore(t *testing.T) {
	cases := []struct {
		name, in string
		hooks    string
		include  bool
	}{
		{"case-insensitive", "[CORE]\n\tHOOKSPATH = a\n", "a", false},
		{"subsection is not core", "[core \"x\"]\nhooksPath=b\n", "", false},
		{"dotted subsection is not core", "[core.x]\nhooksPath=b\n", "", false},
		{"last wins", "[core]\nhooksPath = a\n[core]\nhooksPath = b\n", "b", false},
		{"other section after", "[core]\nhooksPath = a\n[other]\nhooksPath = b\n", "a", false},
		{"quoted with comment", "[core]\nhooksPath = \"q q\" ; c\n", "q q", false},
		{"commented out", "[core]\n# hooksPath=z\n", "", false},
		{"semicolon comment", "[core]\n; hooksPath=z\n", "", false},
		{"key on the header line", "[core] hooksPath = h\n", "h", false},
		{"internal spaces kept", "[core]\nhooksPath = a  b\t\n", "a  b", false},
		{"continuation", "[core]\nhooksPath = a\\\nb\n", "ab", false},
		{"escaped quote", "[core]\nhooksPath = a\\\"b\n", "a\"b", false},
		{"crlf", "[core]\r\nhooksPath = c\r\n", "c", false},
		{"no trailing newline", "[core]\nhooksPath = e", "e", false},
		{"include", "[include]\npath = x\n", "", true},
		{"includeIf", "[includeIf \"gitdir:/x\"]\npath = y\n", "", true},
		{"include any case", "[IncludeIf \"onbranch:m\"]\n", "", true},
		{"no config", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hp, inc := gitConfigCore([]byte(tc.in))
			if hp != tc.hooks || inc != tc.include {
				t.Fatalf("gitConfigCore(%q) = %q, %v; want %q, %v", tc.in, hp, inc, tc.hooks, tc.include)
			}
		})
	}
}

// Malformed config is refused rather than half-read: git itself stops at the
// bad line, so a reader that carried on could miss the value git uses.
func TestGitConfigMalformed(t *testing.T) {
	for _, in := range []string{
		"[core]\nhooksPath = \"open\n",
		"[core\nhooksPath = a\n",
		"[ core]\n",
		"[core]\nhooksPath # c\n",
		"[core]\n1key = a\n",
		"[core]\nhooksPath = a\\q\n",
		"[core]\nhooksPath\n", // a bare key is boolean true: no path, and git dies on it
	} {
		if !parseGitCore([]byte(in)).bad {
			t.Errorf("parseGitConfigCore(%q) accepted malformed config", in)
		}
	}
}

func guardMgr(t *testing.T, on bool) (*Manager, *fakeClient) {
	t.Helper()
	m, f := newTestManagerWithFake(t)
	m.opts.GuardInPlace = on
	return m, f
}

func TestWrapGuardOnlyWhenEnabled(t *testing.T) {
	ctx := context.Background()

	m, f := guardMgr(t, false)
	ws := realTemp(t)
	gitRepo(t, ws, "")
	if _, _, _, err := m.Wrap(ctx, ws, "s1", BoxKey{}, []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if hasDst(f.runArgs[0], filepath.Join(ws, ".claude")) || hasDst(f.runArgs[0], filepath.Join(ws, ".git")) {
		t.Fatalf("guards are off by default: %q", f.runArgs[0])
	}
	if _, err := os.Lstat(filepath.Join(ws, ".claude")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("with guards off nothing is created in the workspace: %v", err)
	}

	m, f = guardMgr(t, true)
	ws = realTemp(t)
	gitRepo(t, ws, "")
	// Spare capacity: appending the guards must not write into the caller's array.
	caller := make([]Mount, 1, 8)
	caller[0] = bundleRoot
	if _, _, _, err := m.Wrap(ctx, ws, "s2", BoxKey{}, []string{"claude"}, nil, caller); err != nil {
		t.Fatal(err)
	}
	if spare := caller[:2]; spare[1] != (Mount{}) {
		t.Fatalf("Wrap wrote its guards into the caller's slice: %+v", spare)
	}
	args := f.runArgs[0]
	g := filepath.Join(ws, ".git")
	if !containsPair(args, "--mount", "type=bind,src="+g+",dst="+g+",readonly") {
		t.Fatalf("want .git bound read-only onto itself: %q", args)
	}
	if !containsPair(args, "--mount", "type=bind,src="+filepath.Join(ws, ".claude")+",dst="+filepath.Join(ws, ".claude")+",readonly") ||
		!hasDst(args, bundleRoot.Box) {
		t.Fatalf("want .claude guarded and the caller's mounts kept: %q", args)
	}
}

// macOS (#68): docker only sees the box VM's shared directories. Every guard is
// derived below the workspace, so a workspace under a share gets all of them
// there, and one outside the shares is refused before any guard is derived or
// any docker call is made: a guard is never silently left out.
func TestWrapGuardsStayUnderTheVMShares(t *testing.T) {
	ctx := context.Background()
	shared := realTemp(t)
	f := newFake()
	m, err := New(Options{DockerBin: "/d", Image: "i", Instance: "T", StateRoot: t.TempDir(), Client: f,
		GuardInPlace: true,
		Shares:       func() (ShareView, error) { return ShareView{Applied: []VMShare{{Path: shared, Writable: true}}}, nil }})
	if err != nil {
		t.Fatal(err)
	}

	outside := realTemp(t)
	gitRepo(t, outside, "")
	_, _, _, err = m.Wrap(ctx, outside, "s1", BoxKey{}, []string{"claude"}, nil, nil)
	if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "is not shared with the box VM") {
		t.Fatalf("want a shared-dirs refusal, got %v", err)
	}
	if f.runs() != 0 {
		t.Fatalf("a refused workspace must not start a box, runs=%d", f.runs())
	}
	if _, err := os.Lstat(filepath.Join(outside, ".claude")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("no guard may be derived for a refused workspace (it creates .claude): %v", err)
	}

	inside := filepath.Join(shared, "proj")
	gitRepo(t, inside, "[core]\n\thooksPath = .husky/_\n")
	mkdirs(t, filepath.Join(inside, ".husky", "_"))
	writeFile(t, filepath.Join(inside, "CLAUDE.md"), "x")
	writeFile(t, filepath.Join(inside, ".mcp.json"), "{}")
	if _, _, _, err := m.Wrap(ctx, inside, "s2", BoxKey{}, []string{"claude"}, nil, nil); err != nil {
		t.Fatalf("a workspace under a share must box: %v", err)
	}
	args := f.runArgs[0]
	for _, rel := range []string{".git", ".claude", "CLAUDE.md", ".mcp.json", ".husky"} {
		g := filepath.Join(inside, rel)
		if !containsPair(args, "--mount", "type=bind,src="+g+",dst="+g+",readonly") {
			t.Errorf("want %s bound read-only onto itself: %q", rel, args)
		}
		if !UnderMount(g, []string{shared}) {
			t.Errorf("guard %s is outside the VM's share %q", g, shared)
		}
	}
}

// The workspace is spelled through a symlink: the guards are derived from, and
// mounted at, its resolved path.
func TestWrapGuardsTheResolvedWorkspace(t *testing.T) {
	m, f := guardMgr(t, true)
	tmp := realTemp(t)
	ws := filepath.Join(tmp, "ws")
	gitRepo(t, ws, "")
	symlink(t, ws, filepath.Join(tmp, "alias"))
	if _, _, _, err := m.Wrap(context.Background(), filepath.Join(tmp, "alias"), "s1", BoxKey{}, []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !hasDst(f.runArgs[0], filepath.Join(ws, ".git")) {
		t.Fatalf("want the guard at the resolved path: %q", f.runArgs[0])
	}
}

func TestLiveBoxWithoutGuardMountsIsRefused(t *testing.T) {
	m, f := guardMgr(t, true)
	ws := realTemp(t)
	gitRepo(t, ws, "")
	name := mustBoxName(t, m, ws)
	f.running[name] = true
	f.mounts[name] = []string{ws, boxHome} // created before the guards existed
	m.AdoptBox(name)
	_, _, _, err := m.Wrap(context.Background(), ws, "s2", BoxKey{}, []string{"bash"}, nil, nil)
	if !errors.Is(err, ErrBoxUnavailable) {
		t.Fatalf("want ErrBoxUnavailable, got %v", err)
	}
	if !strings.Contains(err.Error(), "was started without") || !strings.Contains(err.Error(), ".claude") {
		t.Fatalf("the refusal must name the missing guard: %v", err)
	}
	if f.stops() != 0 || f.runs() != 0 {
		t.Fatalf("a box with a live session must not be touched: stops=%d runs=%d", f.stops(), f.runs())
	}
}

func TestIdleBoxWithoutGuardMountsIsRecreated(t *testing.T) {
	m, f := guardMgr(t, true)
	ws := realTemp(t)
	gitRepo(t, ws, "")
	name := mustBoxName(t, m, ws)
	f.running[name] = true
	f.mounts[name] = []string{ws, boxHome}
	if _, _, _, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if f.stops() != 1 || f.runs() != 1 || !hasDst(f.runArgs[0], filepath.Join(ws, ".git")) {
		t.Fatalf("want the idle box recreated with its guards: stops=%d runs=%d", f.stops(), f.runs())
	}
}

func TestWrapRefusesSubmoduleWorkspaceBeforeDocker(t *testing.T) {
	m, f := guardMgr(t, true)
	ws := realTemp(t)
	gitRepo(t, ws, "")
	writeFile(t, filepath.Join(ws, ".gitmodules"), "[submodule \"lib\"]\n")
	_, _, _, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"bash"}, nil, nil)
	if !errors.Is(err, ErrUnboxableWorkspace) {
		t.Fatalf("want ErrUnboxableWorkspace, got %v", err)
	}
	if f.runs() != 0 || f.stops() != 0 {
		t.Fatalf("the refusal comes before any docker call: runs=%d stops=%d", f.runs(), f.stops())
	}
}

// Spec correction 12: git resolves a relative core.hooksPath against
// core.worktree when it is set, and a bare repository has no worktree at all;
// neither is modelled, so either one in the effective config (the workspace's
// or the enclosing repository's, config or config.worktree) refuses. core.bare
// is accepted only when it is explicitly false, which `git init` writes into
// every repository.
func TestInPlaceRefusesCoreWorktreeAndBare(t *testing.T) {
	refusing := map[string]string{
		"worktree":           "[core]\n\tworktree = /elsewhere\n",
		"worktree relative":  "[core]\n\tworktree = ..\n",
		"worktree empty":     "[core]\n\tworktree =\n",
		"worktree any case":  "[Core]\n\tWorkTree = /x\n",
		"bare true":          "[core]\n\tbare = true\n",
		"bare bare key":      "[core]\n\tbare\n",
		"bare yes":           "[core]\n\tbare = yes\n",
		"bare 1":             "[core]\n\tbare = 1\n",
		"bare false then on": "[core]\n\tbare = false\n\tbare = on\n",
		"bare garbage":       "[core]\n\tbare = maybe\n",
	}
	accepted := map[string]string{
		"bare false":          "[core]\n\tbare = false\n",
		"bare False":          "[core]\n\tBARE = False\n",
		"bare no":             "[core]\n\tbare = no\n",
		"bare off":            "[core]\n\tbare = off\n",
		"bare 0":              "[core]\n\tbare = 0\n",
		"worktree subsection": "[core \"x\"]\n\tworktree = /x\n\tbare = true\n",
	}
	// where writes cfg into one of the four config files git reads for ws.
	wheres := map[string]func(t *testing.T, ws, cfg string){
		"ws config": func(t *testing.T, ws, cfg string) { gitRepo(t, ws, cfg) },
		"ws config.worktree": func(t *testing.T, ws, cfg string) {
			gitRepo(t, ws, "[core]\n\tbare = false\n")
			writeFile(t, filepath.Join(ws, ".git", "config.worktree"), cfg)
		},
		"enclosing config": func(t *testing.T, ws, cfg string) {
			gitRepo(t, filepath.Dir(ws), cfg)
		},
		"enclosing config.worktree": func(t *testing.T, ws, cfg string) {
			gitRepo(t, filepath.Dir(ws), "[core]\n\tbare = false\n")
			writeFile(t, filepath.Join(filepath.Dir(ws), ".git", "config.worktree"), cfg)
		},
	}
	for wname, where := range wheres {
		for name, cfg := range refusing {
			t.Run(wname+"/"+name, func(t *testing.T) {
				ws := filepath.Join(realTemp(t), "sub")
				mkdirs(t, ws)
				where(t, ws, cfg)
				got, err := inPlaceMounts(ws)
				if !errors.Is(err, ErrUnboxableWorkspace) {
					t.Fatalf("want ErrUnboxableWorkspace, got %v (mounts %v)", err, got)
				}
				key := "core.worktree"
				if strings.HasPrefix(name, "bare") {
					key = "core.bare"
				}
				if !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "trusted host") {
					t.Fatalf("the refusal must name %s and the way out: %v", key, err)
				}
				top := ws
				if strings.HasPrefix(wname, "enclosing") {
					top = filepath.Dir(ws)
				}
				if !strings.Contains(err.Error(), top) {
					t.Fatalf("the refusal must name %s: %v", top, err)
				}
			})
		}
		for name, cfg := range accepted {
			t.Run(wname+"/"+name, func(t *testing.T) {
				ws := filepath.Join(realTemp(t), "sub")
				mkdirs(t, ws)
				where(t, ws, cfg)
				if _, err := inPlaceMounts(ws); err != nil {
					t.Fatalf("want no refusal: %v", err)
				}
			})
		}
	}
}

func TestGitConfigValues(t *testing.T) {
	cases := []struct {
		name, in, section, key string
		want                   []string
		ok                     bool
	}{
		{"one", "[remote \"origin\"]\n\turl = /a\n", "remote.origin", "url", []string{"/a"}, true},
		{"two in order", "[remote \"origin\"]\n\turl = /a\n\turl = /b\n", "remote.origin", "url", []string{"/a", "/b"}, true},
		{"subsection case is kept", "[remote \"Origin\"]\n\turl = /a\n", "remote.origin", "url", nil, true},
		{"section name is case-insensitive", "[REMOTE \"origin\"]\n\turl = /a\n", "remote.origin", "url", []string{"/a"}, true},
		{"key is case-insensitive", "[remote \"origin\"]\n\tURL = /a\n", "remote.origin", "url", []string{"/a"}, true},
		{"key on the header line", "[remote \"origin\"] url=/b\n", "remote.origin", "url", []string{"/b"}, true},
		{"other section ignored", "[core]\n\turl = /x\n[remote \"origin\"]\n\tfetch = f\n", "remote.origin", "url", nil, true},
		{"matching bare key", "[remote \"origin\"]\n\turl\n", "remote.origin", "url", nil, false},
		{"other bare key", "[core]\n\tbare\n[remote \"origin\"]\n\turl = /a\n", "remote.origin", "url", []string{"/a"}, true},
		{"include section", "[include]\n\tpath = other\n[remote \"origin\"]\n\turl = /a\n", "remote.origin", "url", nil, false},
		{"includeIf section", "[remote \"origin\"]\n\turl = /a\n[includeIf \"gitdir:/x/\"]\n\tpath = other\n", "remote.origin", "url", nil, false},
		{"empty file", "", "remote.origin", "url", nil, true},
		{"unterminated section", "[remote \"origin\"\n\turl = /a\n", "remote.origin", "url", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := GitConfigValues([]byte(c.in), c.section, c.key)
			if ok != c.ok || !slices.Equal(got, c.want) {
				t.Fatalf("GitConfigValues(%q, %q, %q) = %q, %v; want %q, %v", c.in, c.section, c.key, got, ok, c.want, c.ok)
			}
		})
	}
}

// #305: git resolves a gitfile's gitdir: and a commondir one component at a
// time through the real directories (and so does the kernel for a linked
// config). A path that passes below the workspace on its way, even one that
// ends outside it, is one the box can redirect by swapping a component for a
// link, so it is refused; a ".." that leaves ws right after it, or one wholly
// outside it, is accepted. <ws> and <tmp> are replaced; the main repository's
// git dir is <tmp>/wt/.git and holds a worktree git dir worktrees/w.
func TestInPlaceGitfileWalk(t *testing.T) {
	cases := []struct {
		name      string
		gitdir    string      // the gitdir: line of <ws>/.git
		commondir string      // for worktrees/w; "" = none
		links     [][2]string // (at, target)
		config    string      // when set, <tmp>/wt/.git/config is a link to it
		refuse    string      // a substring of the refusal; "" = accepted
	}{
		{name: "relative gitdir outside", gitdir: "../wt/.git"},
		{name: "relative gitdir with .. right after ws", gitdir: "../Repo/../wt/.git"},
		{name: "gitdir through a host link to the parent of ws", gitdir: "<tmp>/Alias/wt/.git",
			links: [][2]string{{"<tmp>/Alias", "<tmp>"}}},
		{name: "relative worktree commondir", gitdir: "<tmp>/wt/.git/worktrees/w", commondir: "../.."},
		{name: "gitdir .. after a box dir", gitdir: "sub/../../wt/.git", refuse: "gitdir"},
		{name: "gitdir .. after a box link", gitdir: "sub/../../wt/.git", refuse: "gitdir",
			links: [][2]string{{"<ws>/sub", "<ws>/a/b"}}},
		{name: "gitdir through a host link into ws then .. after a box dir", gitdir: "<tmp>/Lnk/sub/../../wt/.git",
			links: [][2]string{{"<tmp>/Lnk", "<ws>"}}, refuse: "gitdir"},
		{name: "gitdir through a host link into ws then a box link out", gitdir: "<tmp>/Lnk/x/wt/.git",
			links: [][2]string{{"<tmp>/Lnk", "<ws>"}, {"<ws>/x", "<tmp>"}}, refuse: "gitdir"},
		{name: "commondir .. after a box dir", gitdir: "<tmp>/wt/.git/worktrees/w",
			commondir: "<ws>/sub/../../wt/.git", refuse: "commondir"},
		{name: "relative commondir .. after a box dir", gitdir: "<tmp>/wt/.git/worktrees/w",
			commondir: "../../../../Repo/sub/../../wt/.git", refuse: "commondir"},
		{name: "config linked through a box dir", gitdir: "../wt/.git", config: "<ws>/sub/../../cfg",
			refuse: "leads into the workspace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := realTemp(t)
			ws := filepath.Join(tmp, "Repo")
			r := strings.NewReplacer("<ws>", ws, "<tmp>", tmp)
			main := filepath.Join(tmp, "wt", ".git")
			mkdirs(t, filepath.Join(ws, "sub"), filepath.Join(ws, "a", "b"), filepath.Join(ws, "wt", ".git"),
				filepath.Join(main, "hooks"), filepath.Join(main, "worktrees", "w"))
			writeFile(t, filepath.Join(tmp, "cfg"), "")
			if tc.config != "" {
				symlink(t, r.Replace(tc.config), filepath.Join(main, "config"))
			} else {
				writeFile(t, filepath.Join(main, "config"), "")
			}
			if tc.commondir != "" {
				writeFile(t, filepath.Join(main, "worktrees", "w", "commondir"), r.Replace(tc.commondir)+"\n")
			}
			for _, l := range tc.links {
				if err := os.RemoveAll(r.Replace(l[0])); err != nil {
					t.Fatal(err)
				}
				symlink(t, r.Replace(l[1]), r.Replace(l[0]))
			}
			writeFile(t, filepath.Join(ws, ".git"), "gitdir: "+r.Replace(tc.gitdir)+"\n")
			got, err := inPlaceMounts(ws)
			if tc.refuse != "" {
				if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), tc.refuse) {
					t.Fatalf("want a refusal mentioning %q, got %v (mounts %v)", tc.refuse, err, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantMounts(t, ws, got, ".git", ".claude")
		})
	}
}

// The enclosing repository of a workspace below its root: its .git and config
// are read outside ws, and a host link that passes below ws on its way is
// refused even when it ends outside.
func TestInPlaceSubdirGitPathsThroughWorkspace(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, repo string) // ws = <repo>/sub with <repo>/sub/a
	}{
		{"config linked through a box dir", func(t *testing.T, repo string) {
			gitRepo(t, repo, "")
			writeFile(t, filepath.Join(repo, "cfg"), "")
			if err := os.Remove(filepath.Join(repo, ".git", "config")); err != nil {
				t.Fatal(err)
			}
			symlink(t, "../sub/a/../../cfg", filepath.Join(repo, ".git", "config"))
		}},
		{".git linked through a box dir", func(t *testing.T, repo string) {
			gitRepo(t, filepath.Join(repo, "real"), "")
			symlink(t, "sub/a/../../real/.git", filepath.Join(repo, ".git"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := realTemp(t)
			ws := filepath.Join(repo, "sub")
			mkdirs(t, filepath.Join(ws, "a"))
			tc.setup(t, repo)
			got, err := inPlaceMounts(ws)
			if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "leads into the workspace") {
				t.Fatalf("want a leads-into-the-workspace refusal, got %v (mounts %v)", err, got)
			}
		})
	}
}

func TestGitShapeOf(t *testing.T) {
	t.Run("not git", func(t *testing.T) {
		ws := realTemp(t)
		got, err := GitShapeOf(ws)
		if err != nil || got != (GitShape{}) {
			t.Fatalf("GitShapeOf = %+v, %v; want the zero value", got, err)
		}
	})
	t.Run("a file is not a repo", func(t *testing.T) {
		f := filepath.Join(realTemp(t), "file")
		writeFile(t, f, "x")
		if got, err := GitShapeOf(f); err != nil || got != (GitShape{}) {
			t.Fatalf("GitShapeOf = %+v, %v; want the zero value", got, err)
		}
	})
	t.Run("plain repo", func(t *testing.T) {
		ws := realTemp(t)
		gitRepo(t, ws, "")
		got, err := GitShapeOf(ws)
		if err != nil || got != (GitShape{Repo: ws}) {
			t.Fatalf("GitShapeOf = %+v, %v; want Repo=%s only", got, err, ws)
		}
	})
	t.Run("linked worktree", func(t *testing.T) {
		ws := realTemp(t)
		writeFile(t, filepath.Join(ws, ".git"), "gitdir: /elsewhere/.git/worktrees/x\n")
		got, err := GitShapeOf(ws)
		if err != nil || got != (GitShape{Repo: ws, Worktree: true}) {
			t.Fatalf("GitShapeOf = %+v, %v; want a worktree at %s", got, err, ws)
		}
	})
	t.Run("subdirectory of a repo", func(t *testing.T) {
		top := realTemp(t)
		gitRepo(t, top, "")
		ws := filepath.Join(top, "a", "b")
		mkdirs(t, ws)
		got, err := GitShapeOf(ws)
		if err != nil || got != (GitShape{Repo: top, Inside: true}) {
			t.Fatalf("GitShapeOf = %+v, %v; want Inside, Repo=%s", got, err, top)
		}
	})
	t.Run("subdirectory of a linked worktree", func(t *testing.T) {
		top := realTemp(t)
		writeFile(t, filepath.Join(top, ".git"), "gitdir: /elsewhere/.git/worktrees/x\n")
		ws := filepath.Join(top, "sub")
		mkdirs(t, ws)
		got, err := GitShapeOf(ws)
		if err != nil || got != (GitShape{Repo: top, Inside: true, Worktree: true}) {
			t.Fatalf("GitShapeOf = %+v, %v; want Inside a worktree at %s", got, err, top)
		}
	})
	t.Run("lfs", func(t *testing.T) {
		ws := realTemp(t)
		gitRepo(t, ws, "")
		writeFile(t, filepath.Join(ws, ".gitattributes"), "*.bin filter=lfs diff=lfs merge=lfs -text\n")
		got, err := GitShapeOf(ws)
		if err != nil || got != (GitShape{Repo: ws, LFS: true}) {
			t.Fatalf("GitShapeOf = %+v, %v; want LFS", got, err)
		}
	})
	t.Run("lfs named at the repo root of a subdirectory", func(t *testing.T) {
		top := realTemp(t)
		gitRepo(t, top, "")
		writeFile(t, filepath.Join(top, ".gitattributes"), "*.bin filter=lfs\n")
		ws := filepath.Join(top, "sub")
		mkdirs(t, ws)
		if got, err := GitShapeOf(ws); err != nil || !got.LFS || !got.Inside {
			t.Fatalf("GitShapeOf = %+v, %v; want Inside and LFS", got, err)
		}
	})
	t.Run("gitattributes without lfs", func(t *testing.T) {
		ws := realTemp(t)
		gitRepo(t, ws, "")
		writeFile(t, filepath.Join(ws, ".gitattributes"), "*.sh text eol=lf\n")
		if got, err := GitShapeOf(ws); err != nil || got.LFS {
			t.Fatalf("GitShapeOf = %+v, %v; want no LFS", got, err)
		}
	})
	t.Run("gitattributes symlink is not read", func(t *testing.T) {
		ws := realTemp(t)
		gitRepo(t, ws, "")
		real := filepath.Join(realTemp(t), "attrs")
		writeFile(t, real, "*.bin filter=lfs\n")
		symlink(t, real, filepath.Join(ws, ".gitattributes"))
		got, err := GitShapeOf(ws)
		if err != nil || got != (GitShape{Repo: ws}) {
			t.Fatalf("GitShapeOf = %+v, %v; want a repo without LFS and no error", got, err)
		}
	})
	t.Run("gitattributes over 64 KiB is not read", func(t *testing.T) {
		ws := realTemp(t)
		gitRepo(t, ws, "")
		writeFile(t, filepath.Join(ws, ".gitattributes"), strings.Repeat("# pad\n", 12000)+"*.bin filter=lfs\n")
		if got, err := GitShapeOf(ws); err != nil || got.LFS {
			t.Fatalf("GitShapeOf = %+v, %v; want no LFS for an oversized file", got, err)
		}
	})
}

func TestNeedsCopyErrorMessages(t *testing.T) {
	cases := []struct {
		name string
		e    NeedsCopyError
		want []string
	}{
		{"repo", NeedsCopyError{Repo: "/r"}, []string{"private copy", "Host"}},
		{"worktree", NeedsCopyError{Repo: "/r", Worktree: true}, []string{"linked git worktree cannot be boxed yet", "Host", "main repo"}},
		{"inside", NeedsCopyError{Repo: "/home/u", Inside: true}, []string{"/home/u", "repo root", "Host", "move the folder"}},
		{"inside worktree", NeedsCopyError{Repo: "/w", Inside: true, Worktree: true}, []string{"/w", "worktree", "Host", "main repo"}},
	}
	for _, c := range cases {
		e := c.e
		for _, w := range c.want {
			if !strings.Contains(e.Error(), w) {
				t.Errorf("%s: %q lacks %q", c.name, e.Error(), w)
			}
		}
		if !errors.Is(&e, ErrUnboxableWorkspace) {
			t.Errorf("%s: must wrap ErrUnboxableWorkspace", c.name)
		}
	}
}

// refuseGitInPlace turns the refusal of a git work tree back on (TestMain turns
// it off for the guard tests, which reach the guards through Wrap).
func refuseGitInPlace(t *testing.T) {
	t.Helper()
	old := gitInPlaceRefusal
	gitInPlaceRefusal = NeedsCopy
	t.Cleanup(func() { gitInPlaceRefusal = old })
}

func TestWrapRefusesInPlaceGitRepo(t *testing.T) {
	refuseGitInPlace(t)
	ctx := context.Background()
	m, f := guardMgr(t, true)

	repo := realTemp(t)
	gitRepo(t, repo, "")
	_, _, _, err := m.Wrap(ctx, repo, "s1", BoxKey{}, []string{"bash"}, nil, nil)
	var nc *NeedsCopyError
	if !errors.As(err, &nc) || nc.Repo != repo || nc.Inside || nc.Worktree {
		t.Fatalf("a git repo in place: err = %v, want *NeedsCopyError for %s", err, repo)
	}
	if !errors.Is(err, ErrUnboxableWorkspace) {
		t.Fatalf("must be an ErrUnboxableWorkspace: %v", err)
	}
	if f.runs() != 0 {
		t.Fatalf("a refused workspace must not start a box, runs=%d", f.runs())
	}
	if _, err := os.Lstat(filepath.Join(repo, ".claude")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the refusal comes before the guards, which create .claude: %v", err)
	}

	// A folder inside a repo: refused, naming the repo root.
	sub := filepath.Join(repo, "pkg")
	mkdirs(t, sub)
	_, _, _, err = m.Wrap(ctx, sub, "s2", BoxKey{}, []string{"bash"}, nil, nil)
	if !errors.As(err, &nc) || nc.Repo != repo || !nc.Inside || !strings.Contains(err.Error(), repo) {
		t.Fatalf("a folder inside a repo: err = %v, want *NeedsCopyError naming %s", err, repo)
	}

	// Not git: the guards as today.
	plain := realTemp(t)
	if _, _, _, err := m.Wrap(ctx, plain, "s3", BoxKey{}, []string{"bash"}, nil, nil); err != nil {
		t.Fatalf("a non-git folder boxes in place: %v", err)
	}
	if !hasDst(f.runArgs[0], filepath.Join(plain, ".claude")) {
		t.Fatalf("a non-git folder is still guarded: %q", f.runArgs[0])
	}

	// A clone spawn is not affected, whatever its workspace holds.
	cl := realTemp(t)
	gitRepo(t, cl, "")
	if _, _, _, err := m.Wrap(ctx, cl, "s4", BoxKey{Clone: &CloneMounts{Hash: "h"}}, []string{"bash"}, nil, nil); err != nil {
		t.Fatalf("a clone workspace boxes: %v", err)
	}
}

func TestWrapRefusesAnEnclosingRepoAtHome(t *testing.T) {
	refuseGitInPlace(t)
	m, _ := guardMgr(t, true)
	home := realTemp(t)
	gitRepo(t, home, "") // a dotfiles repo at ~
	ws := filepath.Join(home, "projects", "app")
	mkdirs(t, ws)
	_, _, _, err := m.Wrap(context.Background(), ws, "s1", BoxKey{}, []string{"bash"}, nil, nil)
	var nc *NeedsCopyError
	if !errors.As(err, &nc) || nc.Repo != home {
		t.Fatalf("err = %v, want a refusal naming %s", err, home)
	}
	for _, w := range []string{home, "Host", "move the folder"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("the message %q must say %q", err, w)
		}
	}
}
