package box

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnderMountFold(t *testing.T) {
	cases := []struct {
		path, mount string
		fold, want  bool
	}{
		{"/a/Repo/x", "/a/repo", true, true},
		{"/a/Repo/x", "/a/repo", false, false},
		{"/a/repox", "/a/repo", true, false},
		{"/a/repox", "/a/repo", false, false},
		{"/a/Repo", "/a/repo", true, true},
		{"/a/repo", "/a/repo", false, true},
	}
	for _, c := range cases {
		if got := underMount(c.path, []string{c.mount}, c.fold); got != c.want {
			t.Errorf("underMount(%q, %q, fold=%v) = %v, want %v", c.path, c.mount, c.fold, got, c.want)
		}
	}
}

// withFold sets the host-path case folding for one test (macOS volumes are
// case-insensitive, so there the two spellings are one folder).
func withFold(t *testing.T, v bool) {
	t.Helper()
	old := inPlaceFold
	inPlaceFold = v
	t.Cleanup(func() { inPlaceFold = old })
}

// On Linux the twin spellings are different folders, so <tmp>/repo does not
// exist: the seam is driven with fold on to stand in for a case-insensitive Mac.
func TestInPlaceGuardFoldsCase(t *testing.T) {
	setup := func(t *testing.T, hooksPath string) (ws string, other string) {
		tmp := realTemp(t)
		ws = filepath.Join(tmp, "Repo")
		other = filepath.Join(tmp, "repo")
		gitRepo(t, ws, "[core]\n\thooksPath = "+strings.ReplaceAll(hooksPath, "<o>", other)+"\n")
		mkdirs(t, filepath.Join(ws, "hooks"))
		return ws, other
	}
	t.Run("hooks dir in other case is bound spelled under ws", func(t *testing.T) {
		withFold(t, true)
		ws, _ := setup(t, "<o>/hooks")
		got, err := inPlaceMounts(ws)
		if err != nil {
			t.Fatal(err)
		}
		wantMounts(t, ws, got, ".git", ".claude", "hooks")
	})
	t.Run("nested hooks dir in other case binds its top dir under ws", func(t *testing.T) {
		withFold(t, true)
		ws, _ := setup(t, "<o>/tools/hooks")
		mkdirs(t, filepath.Join(ws, "tools", "hooks"))
		got, err := inPlaceMounts(ws)
		if err != nil {
			t.Fatal(err)
		}
		wantMounts(t, ws, got, ".git", ".claude", "tools")
	})
	t.Run("without fold the other case is outside", func(t *testing.T) {
		withFold(t, false)
		ws, _ := setup(t, "<o>/hooks")
		got, err := inPlaceMounts(ws)
		if err != nil {
			t.Fatal(err)
		}
		wantMounts(t, ws, got, ".git", ".claude")
	})
	t.Run("hooksPath is the workspace in other case", func(t *testing.T) {
		withFold(t, true)
		ws, _ := setup(t, "<o>")
		_, err := inPlaceMounts(ws)
		if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "workspace itself") {
			t.Fatalf("want a workspace-itself refusal, got %v", err)
		}
	})
	t.Run("gitdir in other case inside ws is refused", func(t *testing.T) {
		withFold(t, true)
		tmp := realTemp(t)
		ws := filepath.Join(tmp, "Repo")
		mkdirs(t, ws)
		writeFile(t, filepath.Join(ws, ".git"), "gitdir: "+filepath.Join(tmp, "repo", ".gitx")+"\n")
		_, err := inPlaceMounts(ws)
		if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "inside the workspace") {
			t.Fatalf("want an inside-the-workspace refusal, got %v", err)
		}
	})
	t.Run("workspace inside the git dir in other case is refused", func(t *testing.T) {
		withFold(t, true)
		tmp := realTemp(t)
		ws := filepath.Join(tmp, "wsq")
		mkdirs(t, ws, filepath.Join(tmp, "WSQ", "g"))
		// The git dir is spelled WSQ/g, so the workspace wsq is not inside it as
		// spelled, but it is the folder that holds it: refused as inside ws.
		writeFile(t, filepath.Join(ws, ".git"), "gitdir: "+filepath.Join(tmp, "WSQ", "g")+"\n")
		_, err := inPlaceMounts(ws)
		if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "inside the workspace") {
			t.Fatalf("want an inside-the-workspace refusal, got %v", err)
		}
	})
	t.Run("workspace inside an enclosing git dir in other case is refused", func(t *testing.T) {
		withFold(t, true)
		tmp := realTemp(t)
		top := filepath.Join(tmp, "a")
		ws := filepath.Join(top, "G", "x")
		mkdirs(t, ws, filepath.Join(top, "g"))
		writeFile(t, filepath.Join(top, ".git"), "gitdir: "+filepath.Join(top, "g")+"\n")
		_, err := inPlaceMounts(ws)
		if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "inside the git directory") {
			t.Fatalf("want an inside-the-git-directory refusal, got %v", err)
		}
	})
	t.Run("other-case host symlink is resolved before folding", func(t *testing.T) {
		for _, fold := range []bool{false, true} {
			withFold(t, fold)
			tmp := realTemp(t)
			ws := filepath.Join(tmp, "Repo")
			mkdirs(t, ws)
			if _, err := os.Stat(filepath.Join(tmp, "repo")); err == nil {
				t.Skipf("volume is case-insensitive: Repo and repo are one folder, so no symlink can be named repo")
			}
			gitRepo(t, ws, "[core]\n\thooksPath = "+filepath.Join(tmp, "repo", "hooks")+"\n")
			mkdirs(t, filepath.Join(ws, "hooks"), filepath.Join(ws, "sub", "hooks"))
			symlink(t, filepath.Join(ws, "sub"), filepath.Join(tmp, "repo"))
			got, err := inPlaceMounts(ws)
			if err != nil {
				t.Fatal(err)
			}
			wantMounts(t, ws, got, ".git", ".claude", "sub")
		}
	})
	t.Run("other-case hooks path that is the workspace on disk is walked like the workspace", func(t *testing.T) {
		withFold(t, true)
		old := sameHostDir
		sameHostDir = func(a, b string) bool { return true }
		t.Cleanup(func() { sameHostDir = old })
		msg := func(spelling string) string {
			tmp := realTemp(t)
			mkdirs(t, filepath.Join(tmp, "Repo"))
			if _, err := os.Stat(filepath.Join(tmp, "repo")); err == nil {
				t.Skip("volume is case-insensitive: the stand-in repo folder cannot exist beside Repo; TestInPlaceHooksCaseVariantSymlinkIsRefused covers it")
			}
			ws := filepath.Join(tmp, "Repo")
			mkdirs(t, filepath.Join(tmp, "out", "hooks"))
			gitRepo(t, ws, "[core]\n\thooksPath = "+filepath.Join(tmp, spelling, "link", "hooks")+"\n")
			symlink(t, filepath.Join(tmp, "out"), filepath.Join(ws, "link"))
			if spelling != "Repo" {
				// Stands in for APFS, where repo/link IS Repo/link: here repo is
				// its own folder whose link leads out, as resolution would follow.
				mkdirs(t, filepath.Join(tmp, spelling))
				symlink(t, filepath.Join(tmp, "out"), filepath.Join(tmp, spelling, "link"))
			}
			_, err := inPlaceMounts(ws)
			if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "reached through the symlink") {
				t.Fatalf("%s: want a symlink refusal, got %v", spelling, err)
			}
			return strings.ReplaceAll(err.Error(), tmp, "<tmp>")
		}
		if same, other := msg("Repo"), msg("repo"); same != other {
			t.Fatalf("other-case refusal differs:\n same  %s\n other %s", same, other)
		}
	})
}

// #304: git resolves .. on the real filesystem, the guard cleans it as text, so a
// .. that follows a component below the workspace (one the box can swap for a
// symlink) is refused. A .. after ws itself is safe: ws is a mount point.
func TestInPlaceHooksPathDotDot(t *testing.T) {
	cases := []struct {
		name, hp string // <ws> and <tmp> are replaced
		refuse   bool
		mounts   []string
	}{
		{"dotdot after a dir below ws", "sub/../hooks", true, nil},
		{"absolute dotdot below ws", "<ws>/sub/../hooks", true, nil},
		{"second dotdot below ws", "sub/x/../../hooks", true, nil},
		{"dotdot leaving ws then below it again", "../Repo/sub/../hooks", true, nil},
		{"dotdot outside ws", "<tmp>/other/../other/hooks", false, []string{}},
		{"relative dotdot from ws itself", "../other/hooks", false, []string{}},
		{"dotdot after ws itself and back in", "../Repo/hooks", false, []string{"hooks"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := realTemp(t)
			ws := filepath.Join(tmp, "Repo")
			hp := strings.NewReplacer("<ws>", ws, "<tmp>", tmp).Replace(tc.hp)
			gitRepo(t, ws, "[core]\n\thooksPath = "+hp+"\n")
			mkdirs(t, filepath.Join(ws, "sub", "x"), filepath.Join(ws, "hooks"), filepath.Join(tmp, "other", "hooks"))
			got, err := inPlaceMounts(ws)
			if tc.refuse {
				if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "..") {
					t.Fatalf("want a .. refusal, got %v (mounts %v)", err, got)
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

// #304: git hands core.hooksPath to the kernel, which resolves it one component
// at a time, following every link and taking ".." from the real directory. A
// host link (one the box cannot touch) can lead the walk into the workspace,
// where the box can swap any component for a link afterwards: from there on a
// "..", a symlink or a missing component is refused, whatever the walk's end
// point. <ws> and <tmp> are replaced; links are made as (at, target).
func TestInPlaceHooksPathPhysicalWalk(t *testing.T) {
	cases := []struct {
		name   string
		links  [][2]string
		hp     string
		refuse string   // a substring of the refusal; "" = accepted
		mounts []string // besides .git and .claude, when accepted
	}{
		{name: "host link into ws then .. after a box link",
			links: [][2]string{{"<tmp>/Lnk", "<ws>"}, {"<ws>/x", "<ws>/a/b"}},
			hp:    "<tmp>/Lnk/x/../hooks", refuse: ".."},
		{name: "host link into ws then .. after a box dir",
			links: [][2]string{{"<tmp>/Lnk", "<ws>"}},
			hp:    "<tmp>/Lnk/a/../hooks", refuse: ".."},
		{name: "host link into a box dir then ..",
			links: [][2]string{{"<tmp>/Lnk", "<ws>/a/b"}},
			hp:    "<tmp>/Lnk/../../hooks", refuse: ".."},
		{name: "host link whose target has .. after a box dir",
			links: [][2]string{{"<tmp>/Lnk", "<ws>/a/../../out"}},
			hp:    "<tmp>/Lnk/hooks", refuse: ".."},
		{name: "host link into ws then a box link out",
			links: [][2]string{{"<tmp>/Lnk", "<ws>"}, {"<ws>/x", "<tmp>/out"}},
			hp:    "<tmp>/Lnk/x/hooks", refuse: "reached through the symlink"},
		{name: "two host links into ws then a box link out",
			links: [][2]string{{"<tmp>/Lnk", "<ws>"}, {"<tmp>/L2", "Lnk"}, {"<ws>/x", "<tmp>/out"}},
			hp:    "<tmp>/L2/x/hooks", refuse: "reached through the symlink"},
		{name: "host link into ws then a dangling box link",
			links: [][2]string{{"<tmp>/Lnk", "<ws>"}, {"<ws>/x", "<tmp>/nowhere"}},
			hp:    "<tmp>/Lnk/x/hooks", refuse: "reached through the symlink"},
		{name: "dangling box link",
			links: [][2]string{{"<ws>/x", "<tmp>/nowhere"}},
			hp:    "x/hooks", refuse: "reached through the symlink"},
		{name: "host link into ws and missing dir below",
			links: [][2]string{{"<tmp>/Lnk", "<ws>"}},
			hp:    "<tmp>/Lnk/missing/hooks", refuse: "does not exist"},
		{name: "host link loop",
			links: [][2]string{{"<tmp>/L1", "L2"}, {"<tmp>/L2", "L1"}},
			hp:    "<tmp>/L1/hooks", refuse: "cannot be resolved"},
		{name: "host link to the parent of ws (like /tmp on macOS)",
			links: [][2]string{{"<tmp>/Alias", "<tmp>"}},
			hp:    "<tmp>/Alias/Repo/hooks", mounts: []string{"hooks"}},
		{name: "host link into a nested dir binds its top dir",
			links: [][2]string{{"<tmp>/Lnk", "<ws>/a"}},
			hp:    "<tmp>/Lnk/b", mounts: []string{"a"}},
		{name: "host link outside ws with ..",
			links: [][2]string{{"<tmp>/Lnk", "<tmp>/out"}},
			hp:    "<tmp>/Lnk/x/../hooks"},
		{name: "host link into ws and .. right after ws",
			links: [][2]string{{"<tmp>/Lnk", "<ws>"}},
			hp:    "<tmp>/Lnk/../out/hooks"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := realTemp(t)
			ws := filepath.Join(tmp, "Repo")
			r := strings.NewReplacer("<ws>", ws, "<tmp>", tmp)
			mkdirs(t, filepath.Join(ws, "a", "b"), filepath.Join(ws, "hooks"), filepath.Join(tmp, "out", "x"), filepath.Join(tmp, "out", "hooks"))
			gitRepo(t, ws, "[core]\n\thooksPath = "+r.Replace(tc.hp)+"\n")
			for _, l := range tc.links {
				symlink(t, r.Replace(l[1]), r.Replace(l[0]))
			}
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
			wantMounts(t, ws, got, append([]string{".git", ".claude"}, tc.mounts...)...)
		})
	}
}

// The same walk when a host link names the workspace in another letter case:
// on a case-insensitive volume that is the workspace (on Linux the folder does
// not exist, which the fold seam also judges as the workspace).
func TestInPlaceHooksPathPhysicalWalkFold(t *testing.T) {
	cases := []struct {
		name   string
		boxTo  string // where the box link <ws>/x points
		hp     string
		refuse string
	}{
		{"other-case host link into ws then .. after a box link", "<ws>/a/b", "<tmp>/Lnk/x/../hooks", ".."},
		{"other-case host link into ws then a box link out", "<tmp>/out", "<tmp>/Lnk/x/hooks", "reached through the symlink"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withFold(t, true)
			tmp := realTemp(t)
			ws := filepath.Join(tmp, "Repo")
			r := strings.NewReplacer("<ws>", ws, "<tmp>", tmp)
			mkdirs(t, filepath.Join(ws, "a", "b"), filepath.Join(ws, "hooks"), filepath.Join(tmp, "out", "hooks"))
			gitRepo(t, ws, "[core]\n\thooksPath = "+r.Replace(tc.hp)+"\n")
			symlink(t, filepath.Join(tmp, "repo"), filepath.Join(tmp, "Lnk"))
			symlink(t, r.Replace(tc.boxTo), filepath.Join(ws, "x"))
			got, err := inPlaceMounts(ws)
			if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), tc.refuse) {
				t.Fatalf("want a refusal mentioning %q, got %v (mounts %v)", tc.refuse, err, got)
			}
		})
	}
}

// A workspace below a repository root, with a hooks path relative to that root:
// a ".." that leaves the workspace right after it, or one wholly outside it, is
// host territory and stays accepted.
func TestInPlaceSubdirHooksPathDotDot(t *testing.T) {
	for _, hp := range []string{"sub/../.githooks", "../outhooks", "sub/../sub/../.githooks"} {
		t.Run(hp, func(t *testing.T) {
			repo := realTemp(t)
			ws := filepath.Join(repo, "sub")
			mkdirs(t, filepath.Join(ws, "hooks"), filepath.Join(repo, ".githooks"))
			gitRepo(t, repo, "[core]\n\thooksPath = "+hp+"\n")
			got, err := inPlaceMounts(ws)
			if err != nil {
				t.Fatal(err)
			}
			wantMounts(t, ws, got, ".claude")
		})
	}
}

// A directory that is the workspace on disk under another path (a host bind
// mount, stood in for by the sameDirAsWS seam) is the workspace: a box link in
// it is refused. A ".." taken there goes to the parent of that path, as the
// kernel takes it, not to the workspace's own parent (#304 review M1).
func TestInPlaceHooksPathWorkspaceAlias(t *testing.T) {
	cases := []struct {
		name   string
		hp     string // <tmp> is replaced
		refuse string
		mounts []string
	}{
		{"box link out in the alias", "<tmp>/mnt/alias/x/hooks", "reached through the symlink", nil},
		{"hooks in the alias are guarded", "<tmp>/mnt/alias/hooks", "", []string{"hooks"}},
		{".. after the alias is the alias's parent", "<tmp>/mnt/alias/../back/hooks", "", []string{"hooks"}},
		{".. after the alias then outside", "<tmp>/mnt/alias/../out/hooks", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := realTemp(t)
			ws := filepath.Join(tmp, "Repo")
			alias := filepath.Join(tmp, "mnt", "alias")
			mkdirs(t, filepath.Join(ws, "hooks"), alias, filepath.Join(tmp, "mnt", "out", "hooks"), filepath.Join(tmp, "out"))
			old := sameDirAsWS
			sameDirAsWS = func(p string, fi, wsFi fs.FileInfo) bool { return p == alias || old(p, fi, wsFi) }
			t.Cleanup(func() { sameDirAsWS = old })
			symlink(t, ws, filepath.Join(tmp, "mnt", "back"))
			symlink(t, filepath.Join(tmp, "out"), filepath.Join(ws, "x"))
			gitRepo(t, ws, "[core]\n\thooksPath = "+strings.ReplaceAll(tc.hp, "<tmp>", tmp)+"\n")
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
			wantMounts(t, ws, got, append([]string{".git", ".claude"}, tc.mounts...)...)
		})
	}
}
