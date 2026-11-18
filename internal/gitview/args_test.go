package gitview

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

func prefix(mounts ...string) []string {
	a := []string{
		"run", "--rm", "--init", "--name", "n", "--pull=never", "--network", "none", "--cap-drop=ALL", "--security-opt", "no-new-privileges", "--read-only",
		"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m", "--pids-limit", "128", "--memory", "512m",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--label", "io.culiops.agent-box.gitstatus=1",
		"-e", "HOME=/tmp", "-e", "GIT_CONFIG_NOSYSTEM=1", "-e", "GIT_OPTIONAL_LOCKS=0",
		"-e", "GIT_CONFIG_GLOBAL=/dev/null", "-e", "GIT_ATTR_NOSYSTEM=1", "-e", "GIT_LITERAL_PATHSPECS=1",
	}
	for _, m := range mounts {
		a = append(a, "--mount", "type=bind,src="+m+",dst="+m+",readonly")
	}
	return a
}

func gitFlags(top string) []string {
	return []string{"--no-pager", "-C", top, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "core.attributesFile=/dev/null", "-c", "safe.directory=*"}
}

func want(mounts []string, top string, sub ...string) []string {
	w := prefix(mounts...)
	w = append(w, "--entrypoint", "git", "img")
	w = append(w, gitFlags(top)...)
	return append(w, sub...)
}

var statusSub = []string{"status", "--porcelain=v2", "-z", "--branch", "--untracked-files=normal", "--no-renames", "--ignore-submodules=all"}

func TestStatusArgsGolden(t *testing.T) {
	uid, gid := os.Getuid(), os.Getgid()
	cases := []struct {
		name   string
		t      Target
		mounts []string
	}{
		{"common inside top", Target{Top: "/w/repo", Common: "/w/repo/.git"}, []string{"/w/repo"}},
		{"common equals top", Target{Top: "/w/repo", Common: "/w/repo"}, []string{"/w/repo"}},
		{"common empty", Target{Top: "/w/repo"}, []string{"/w/repo"}},
		{"common outside top", Target{Top: "/w/wt", Common: "/w/main/.git"}, []string{"/w/wt", "/w/main/.git"}},
		{"sibling sharing a string prefix", Target{Top: "/w/repo", Common: "/w/repo2/.git"}, []string{"/w/repo", "/w/repo2/.git"}},
		// A boxed workspace: only its root is mounted, whatever Top and Common are.
		{"root only", Target{Root: "/w", Top: "/w/wt", Common: "/w/main/.git"}, []string{"/w"}},
		{"root is top", Target{Root: "/w", Top: "/w", Common: "/w/.git"}, []string{"/w"}},
	}
	for _, c := range cases {
		got := StatusArgs(c.t, "img", "n", uid, gid)
		w := want(c.mounts, c.t.Top, statusSub...)
		if !slices.Equal(got, w) {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, w)
		}
	}
}

func TestDiffArgsGolden(t *testing.T) {
	uid, gid := os.Getuid(), os.Getgid()
	in := Target{Top: "/w/repo", Common: "/w/repo/.git"}
	out := Target{Top: "/w/wt", Common: "/w/main/.git"}

	got := DiffArgs(in, "img", "n", uid, gid, "src/a b.go", false)
	w := want([]string{"/w/repo"}, "/w/repo", "diff", "--no-ext-diff", "--no-textconv", "--no-color", "HEAD", "--", "src/a b.go")
	if !slices.Equal(got, w) {
		t.Errorf("tracked:\n got %q\nwant %q", got, w)
	}

	got = DiffArgs(out, "img", "n", uid, gid, "x.txt", true)
	w = want([]string{"/w/wt", "/w/main/.git"}, "/w/wt", "diff", "--no-index", "--no-ext-diff", "--no-textconv", "--no-color", "--", "/dev/null", "x.txt")
	if !slices.Equal(got, w) {
		t.Errorf("untracked:\n got %q\nwant %q", got, w)
	}

	boxed := Target{Root: "/w", Top: "/w/wt", Common: "/w/main/.git"}
	got = DiffArgs(boxed, "img", "n", uid, gid, "*.go", false)
	w = want([]string{"/w"}, "/w/wt", "diff", "--no-ext-diff", "--no-textconv", "--no-color", "HEAD", "--", "*.go")
	if !slices.Equal(got, w) {
		t.Errorf("boxed:\n got %q\nwant %q", got, w)
	}

	// A path that looks like an option stays after "--".
	got = DiffArgs(in, "img", "n", uid, gid, "--output=/x", false)
	if got[len(got)-2] != "--" || got[len(got)-1] != "--output=/x" {
		t.Errorf("rel not after --: %q", got)
	}
}

func TestMountsAreReadOnly(t *testing.T) {
	got := StatusArgs(Target{Top: "/a", Common: "/b/.git"}, "img", "n", 1, 2)
	n := 0
	for i, a := range got {
		if a == "--mount" {
			n++
			m := got[i+1]
			if len(m) < 9 || m[len(m)-9:] != ",readonly" {
				t.Errorf("mount %q not read-only", m)
			}
		}
	}
	if n != 2 {
		t.Errorf("%d mounts, want 2", n)
	}
}

func TestTargetUnsafe(t *testing.T) {
	bad := []Target{
		{Top: "/w/a,b"},
		{Top: "/w/a\"b"},
		{Top: "/w/a\nb"},
		{Top: "/w/a\x00b"},
		{Top: "/w/a\xffb"},
		{Top: "w/repo"},
		{Top: ""},
		{Top: "/w/repo/"},
		{Top: "/w/../repo"},
		{Top: "/w/repo", Common: "/m/x,volume-opt=y/.git"},
		{Top: "/w/repo", Common: "/m/\"x/.git"},
		{Top: "/w/repo", Common: "m/.git"},
		{Top: "/w/repo", Common: "/m//.git"},
		// Root: must be absolute, clean, mount-safe, and hold Top and Common.
		{Root: "w", Top: "w/repo"},
		{Root: "/w/", Top: "/w/repo"},
		{Root: "/w,x", Top: "/w,x/repo"},
		{Root: "/w", Top: "/v/repo"},
		{Root: "/w", Top: "/w2/repo"},
		{Root: "/w", Top: "/w/repo", Common: "/v/.git"},
		{Root: "/w", Top: "/w/repo", Common: "/w2/.git"},
	}
	for _, b := range bad {
		if why := b.Unsafe(); why == "" {
			t.Errorf("%+v not refused", b)
		}
	}
	good := []Target{
		{Top: "/w/repo"},
		{Top: "/w/my repo", Common: "/w/my repo/.git"},
		{Top: "/w/wt", Common: "/w/main/.git"},
		{Top: "/w/ünï"},
		{Root: "/w", Top: "/w", Common: "/w/.git"},
		{Root: "/w", Top: "/w/wt", Common: "/w/main/.git"},
	}
	for _, g := range good {
		if why := g.Unsafe(); why != "" {
			t.Errorf("%+v refused: %s", g, why)
		}
	}
}

func TestLiteralPathspecs(t *testing.T) {
	// A file named "*.go" or ":(top)*" must name only itself, never a pattern.
	for _, a := range [][]string{
		StatusArgs(Target{Top: "/w/repo"}, "img", "n", 1, 2),
		DiffArgs(Target{Top: "/w/repo"}, "img", "n", 1, 2, ":(top)*", false),
		DiffArgs(Target{Top: "/w/repo"}, "img", "n", 1, 2, "*.go", true),
	} {
		i := slices.Index(a, "GIT_LITERAL_PATHSPECS=1")
		if i < 1 || a[i-1] != "-e" || i > slices.Index(a, "--entrypoint") {
			t.Errorf("no -e GIT_LITERAL_PATHSPECS=1 before the image: %q", a)
		}
	}
}

func TestTargetKeyIncludesRoot(t *testing.T) {
	a := Target{Top: "/w/repo", Common: "/w/repo/.git"}
	b := Target{Root: "/w", Top: "/w/repo", Common: "/w/repo/.git"}
	if a.key() == b.key() {
		t.Fatal("a boxed and a host target share a cache key")
	}
}

var baseSub = []string{"for-each-ref", "--format=%(refname)%00%(symref)%00",
	"refs/remotes/origin/HEAD", "refs/remotes/origin/main", "refs/remotes/origin/master", "refs/heads/main", "refs/heads/master"}

func TestBaseArgsGolden(t *testing.T) {
	uid, gid := os.Getuid(), os.Getgid()
	in := Target{Top: "/w/repo", Common: "/w/repo/.git"}
	out := Target{Top: "/w/wt", Common: "/w/main/.git"}
	boxed := Target{Root: "/w", Top: "/w/wt", Common: "/w/main/.git"}

	// One run lists every candidate; the names are fixed, nothing from the caller.
	for _, c := range []struct {
		name   string
		t      Target
		mounts []string
	}{
		{"in", in, []string{"/w/repo"}},
		{"common outside top", out, []string{"/w/wt", "/w/main/.git"}},
		{"boxed", boxed, []string{"/w"}},
	} {
		got := BaseArgs(c.t, "img", "n", uid, gid)
		if w := want(c.mounts, c.t.Top, baseSub...); !slices.Equal(got, w) {
			t.Errorf("BaseArgs %s:\n got %q\nwant %q", c.name, got, w)
		}
		got = MergeBaseArgs(c.t, "img", "n", uid, gid, "refs/remotes/origin/main")
		if w := want(c.mounts, c.t.Top, "merge-base", "HEAD", "refs/remotes/origin/main"); !slices.Equal(got, w) {
			t.Errorf("MergeBaseArgs %s:\n got %q\nwant %q", c.name, got, w)
		}
	}
}

func TestBaseArgsKeepTheGuards(t *testing.T) {
	// Same isolation as status: no network, read-only mounts, hostile-config flags.
	for name, a := range map[string][]string{
		"base":      BaseArgs(Target{Top: "/a", Common: "/b/.git"}, "img", "n", 1, 2),
		"mergebase": MergeBaseArgs(Target{Top: "/a", Common: "/b/.git"}, "img", "n", 1, 2, "refs/heads/main"),
	} {
		i := slices.Index(a, "--network")
		if i < 0 || a[i+1] != "none" || !slices.Contains(a, "--read-only") || !slices.Contains(a, "core.fsmonitor=false") || !slices.Contains(a, "core.hooksPath=/dev/null") {
			t.Errorf("%s lost a guard: %q", name, a)
		}
		n := 0
		for j, s := range a {
			if s == "--mount" {
				n++
				if !strings.HasSuffix(a[j+1], ",readonly") {
					t.Errorf("%s: mount %q not read-only", name, a[j+1])
				}
			}
		}
		if n != 2 {
			t.Errorf("%s: %d mounts, want 2", name, n)
		}
	}
}

func TestMergeBaseArgsRefusesInvalidRef(t *testing.T) {
	for _, ref := range []string{"", "-x", "--upload-pack=x", "HEAD", "main", "refs/tags/v1", "refs/heads/../x", "refs/heads/a b"} {
		if a := MergeBaseArgs(Target{Top: "/w/repo"}, "img", "n", 1, 2, ref); a != nil {
			t.Errorf("ref %q built an argv: %q", ref, a)
		}
	}
}

func TestValidBaseRef(t *testing.T) {
	long := "refs/heads/" + strings.Repeat("a", 255-len("refs/heads/"))
	for _, ok := range []string{
		"refs/heads/main", "refs/remotes/origin/main", "refs/remotes/origin/HEAD",
		"refs/heads/feat/x-1.2_y", "refs/heads/v1.0", long,
	} {
		if !ValidBaseRef(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{
		"", "-x", "--upload-pack=x", "-refs/heads/main", " refs/heads/main", "refs/heads/main\n", "refs/heads/main ",
		"refs/heads/../x", "refs/heads/a..b", "refs/remotes/..", "refs/tags/v1", "refs/heads/", "refs/remotes/", "refs/heads",
		"heads/main", "/refs/heads/main", "refs/heads/a b", "refs/heads/a;b", "refs/heads/a$b", "refs/heads/a`b`",
		"refs/heads/a\x00b", "refs/heads/é", "refs/heads/a:b", "refs/heads/a~1", "refs/heads/a^", "refs/heads/a@{1}",
		long + "a", "refs/heads/" + strings.Repeat("a", 300),
	} {
		if ValidBaseRef(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

// numstatFlags follow the revision. The trailing "--" ends the revisions: without it git
// refuses a repository that holds a file named like the revision ("ambiguous argument"),
// and an agent can create one.
var numstatFlags = []string{"--raw", "--numstat", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", "--no-color", "--ignore-submodules=all", "--"}

func TestNumstatArgsGolden(t *testing.T) {
	uid, gid := os.Getuid(), os.Getgid()
	for _, c := range []struct {
		name   string
		t      Target
		mounts []string
	}{
		{"in", Target{Top: "/w/repo", Common: "/w/repo/.git"}, []string{"/w/repo"}},
		{"common outside top", Target{Top: "/w/wt", Common: "/w/main/.git"}, []string{"/w/wt", "/w/main/.git"}},
		{"boxed", Target{Root: "/w", Top: "/w/wt", Common: "/w/main/.git"}, []string{"/w"}},
	} {
		got := NumstatArgs(c.t, "img", "n", uid, gid, Scope{})
		w := want(c.mounts, c.t.Top, append([]string{"diff", "HEAD"}, numstatFlags...)...)
		if !slices.Equal(got, w) {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, w)
		}
	}
	got := NumstatArgs(Target{Top: "/a", Common: "/b/.git"}, "img", "n", 1, 2, Scope{})
	for _, must := range []string{"core.fsmonitor=false", "--numstat", "-z", "--no-ext-diff", "--no-textconv", "--read-only", "core.hooksPath=/dev/null"} {
		if !slices.Contains(got, must) {
			t.Errorf("argv lacks %q: %q", must, got)
		}
	}
	if i := slices.Index(got, "--network"); i < 0 || got[i+1] != "none" {
		t.Errorf("no --network none: %q", got)
	}
	mounts := 0
	for i, a := range got {
		if a == "--mount" {
			mounts++
			if !strings.HasSuffix(got[i+1], ",readonly") {
				t.Errorf("mount %q not read-only", got[i+1])
			}
		}
	}
	if mounts != 2 {
		t.Errorf("%d mounts, want 2", mounts)
	}
}

func TestNumstatArgsBranchScope(t *testing.T) {
	uid, gid := os.Getuid(), os.Getgid()
	tt := Target{Top: "/w/repo", Common: "/w/repo/.git"}
	got := NumstatArgs(tt, "img", "n", uid, gid, Scope{Branch: true, BaseRef: "refs/remotes/origin/main"})
	w := want([]string{"/w/repo"}, "/w/repo", append([]string{"diff", "--merge-base", "refs/remotes/origin/main"}, numstatFlags...)...)
	if !slices.Equal(got, w) {
		t.Errorf("branch:\n got %q\nwant %q", got, w)
	}
	// The ref is re-validated here, whoever the caller is: a bad one builds no argv.
	for _, ref := range []string{"", "-x", "--output=/x", "HEAD", "main", "refs/tags/v1", "refs/heads/../x", "refs/heads/a b", "refs/heads/a\n"} {
		if a := NumstatArgs(tt, "img", "n", uid, gid, Scope{Branch: true, BaseRef: ref}); a != nil {
			t.Errorf("ref %q built an argv: %q", ref, a)
		}
	}
	// Outside Branch scope the ref is never read, so a stray one cannot reach git.
	got = NumstatArgs(tt, "img", "n", uid, gid, Scope{BaseRef: "-x"})
	if w := want([]string{"/w/repo"}, "/w/repo", append([]string{"diff", "HEAD"}, numstatFlags...)...); !slices.Equal(got, w) {
		t.Errorf("head scope with a stray ref:\n got %q\nwant %q", got, w)
	}
}
