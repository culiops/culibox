package gitrepo

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, p, s string) {
	t.Helper()
	mkdir(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLocateMainCheckout(t *testing.T) {
	repo := t.TempDir()
	mkdir(t, filepath.Join(repo, ".git"))
	sub := filepath.Join(repo, "a", "b")
	mkdir(t, sub)
	r, ok := Locate(sub)
	if !ok || r.Top != repo || r.Common != filepath.Join(repo, ".git") || r.Linked {
		t.Fatalf("Locate = %+v, %v", r, ok)
	}
	common, wt, ok := CheckoutOf(sub)
	if !ok || common != filepath.Join(repo, ".git") || wt != "" {
		t.Fatalf("CheckoutOf = %q %q %v", common, wt, ok)
	}
}

func TestLocateLinkedWorktree(t *testing.T) {
	repo := t.TempDir()
	common := filepath.Join(repo, ".git")
	mkdir(t, filepath.Join(common, "worktrees", "wt"))
	wt := filepath.Join(t.TempDir(), "wt")
	write(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(common, "worktrees", "wt")+"\n")
	mkdir(t, filepath.Join(wt, "x"))
	r, ok := Locate(filepath.Join(wt, "x"))
	if !ok || r.Top != wt || r.Common != common || !r.Linked {
		t.Fatalf("Locate = %+v, %v", r, ok)
	}
	c, root, ok := CheckoutOf(wt)
	if !ok || c != common || root != wt {
		t.Fatalf("CheckoutOf = %q %q %v", c, root, ok)
	}
}

func TestLocateSubmoduleGitFile(t *testing.T) {
	d := t.TempDir()
	write(t, filepath.Join(d, ".git"), "gitdir: /m/.git/worktrees/wt/modules/sub\n")
	r, ok := Locate(d)
	if !ok || r.Linked || r.Top != d || r.Common != "/m/.git/worktrees/wt/modules/sub" {
		t.Fatalf("Locate = %+v, %v", r, ok)
	}
	if _, wt, _ := CheckoutOf(d); wt != "" {
		t.Fatalf("submodule taken as worktree %q", wt)
	}
}

func TestLocateNone(t *testing.T) {
	if r, ok := Locate(t.TempDir()); ok {
		// a stray .git above the tmp dir would make this flaky; report it plainly
		t.Skipf("a .git exists above the temp dir: %+v", r)
	}
}

// all is a check that serves every dir.
func all(string) bool { return true }

// addWorktree registers a linked worktree the way git does: <common>/worktrees/<name>/gitdir names <wt>/.git, and
// <wt>/.git names that gitdir back.
func addWorktree(t *testing.T, common, name, wt string) {
	t.Helper()
	gd := filepath.Join(common, "worktrees", name)
	write(t, filepath.Join(gd, "gitdir"), filepath.Join(wt, ".git")+"\n")
	write(t, filepath.Join(wt, ".git"), "gitdir: "+gd+"\n")
}

// Worktrees finds a linked worktree wherever it lives: beside the repository, under .worktrees, under
// .claude/worktrees. Relative pointers (git worktree add --relative-paths) count too, both ways.
func TestWorktreesSiblingAndNested(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "proj")
	common := filepath.Join(root, ".git")
	mkdir(t, common)
	sib, dot, cl := filepath.Join(base, "proj-wt"), filepath.Join(root, ".worktrees", "x"), filepath.Join(root, ".claude", "worktrees", "y")
	addWorktree(t, common, "proj-wt", sib)
	addWorktree(t, common, "x", dot)
	addWorktree(t, common, "y", cl)
	write(t, filepath.Join(cl, ".git"), "gitdir: ../../../.git/worktrees/y\n")
	write(t, filepath.Join(common, "worktrees", "proj-wt", "gitdir"), "../../../../proj-wt/.git\n")
	want := []string{dot, cl, sib}
	sort.Strings(want)
	got := Worktrees(common, 64, all)
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("Worktrees = %v, want %v", got, want)
	}
	if got := Worktrees(common, 0, all); len(got) != 0 {
		t.Fatalf("max=0: %v", got)
	}
	if got := Worktrees(filepath.Join(base, "nothing", ".git"), 64, all); len(got) != 0 {
		t.Fatalf("no worktrees dir: %v", got)
	}
}

// Everything in <common>/worktrees is written by whoever owns the repository (an agent): a pointer that is not
// answered by the checkout it names, a link, a FIFO, a huge file or a relative path is skipped, and nothing blocks.
func TestWorktreesHostile(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "proj")
	common := filepath.Join(root, ".git")
	good := filepath.Join(base, "good")
	addWorktree(t, common, "good", good)
	// gitdir names a folder whose .git does not point back (the classic forged pointer to some other place).
	write(t, filepath.Join(common, "worktrees", "etc", "gitdir"), "/etc/.git\n")
	elsewhere := filepath.Join(base, "elsewhere")
	write(t, filepath.Join(elsewhere, ".git"), "gitdir: "+filepath.Join(base, "other", ".git", "worktrees", "z")+"\n")
	write(t, filepath.Join(common, "worktrees", "noback", "gitdir"), filepath.Join(elsewhere, ".git")+"\n")
	// gitdir is a symlink to a real gitdir file.
	real := filepath.Join(base, "realwt")
	addWorktree(t, common, "realwt", real)
	mkdir(t, filepath.Join(common, "worktrees", "lnk"))
	if err := os.Symlink(filepath.Join(common, "worktrees", "realwt", "gitdir"), filepath.Join(common, "worktrees", "lnk", "gitdir")); err != nil {
		t.Fatal(err)
	}
	// gitdir is a FIFO.
	mkdir(t, filepath.Join(common, "worktrees", "fifo"))
	if err := syscall.Mkfifo(filepath.Join(common, "worktrees", "fifo", "gitdir"), 0o644); err != nil {
		t.Skip("mkfifo:", err)
	}
	// gitdir is 1 MiB: the read is cut at 4 KiB and what is left is no path.
	big := make([]byte, 1<<20)
	for i := range big {
		big[i] = 'a'
	}
	write(t, filepath.Join(common, "worktrees", "big", "gitdir"), "/"+string(big))
	// A relative gitdir that leads to no checkout, and one that does not end in .git.
	write(t, filepath.Join(common, "worktrees", "rel", "gitdir"), "../good/.git\n")
	write(t, filepath.Join(common, "worktrees", "nogit", "gitdir"), good+"\n")
	// The worktree dir itself is a symlink (its .git does point back).
	lw := filepath.Join(base, "linkwt")
	if err := os.Symlink(good, lw); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(common, "worktrees", "linkwt", "gitdir"), filepath.Join(lw, ".git")+"\n")
	// A plain file among the entries.
	write(t, filepath.Join(common, "worktrees", "plainfile"), "x")

	done := make(chan []string, 1)
	go func() { done <- Worktrees(common, 64, all) }()
	select {
	case got := <-done:
		want := []string{good, real}
		sort.Strings(want)
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("Worktrees = %v, want %v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Worktrees blocked")
	}
}

// <common>/worktrees itself being a link is refused whole; and the directory read is bounded.
func TestWorktreesLinkedDirAndBound(t *testing.T) {
	base := t.TempDir()
	common := filepath.Join(base, "proj", ".git")
	store := filepath.Join(base, "store")
	addWorktree(t, store, "a", filepath.Join(base, "a"))
	mkdir(t, common)
	if err := os.Symlink(filepath.Join(store, "worktrees"), filepath.Join(common, "worktrees")); err != nil {
		t.Fatal(err)
	}
	if got := Worktrees(common, 64, all); len(got) != 0 {
		t.Fatalf("linked worktrees dir: %v", got)
	}
	c2 := filepath.Join(base, "p2", ".git")
	for i := 0; i < 100; i++ {
		n := string(rune('a'+i/26)) + string(rune('a'+i%26))
		addWorktree(t, c2, n, filepath.Join(base, "w", n))
	}
	if got := Worktrees(c2, 64, all); len(got) > 64 || !sort.StringsAreSorted(got) {
		t.Fatalf("max=64: %d %v", len(got), sort.StringsAreSorted(got))
	}
	if got := Worktrees(c2, 1000, all); len(got) != 100 {
		t.Fatalf("max=1000: %d", len(got))
	}
}

// A boxed agent owns the files under its mount and can swap .git between a
// regular file and a FIFO, so a check-then-open loses the race and the plain open
// of the FIFO blocks the poll forever. OpenRegular must decide on the OPENED fd
// and never block.
func TestOpenRegularRejectsFIFOWithoutBlocking(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".git")
	if err := syscall.Mkfifo(p, 0o644); err != nil {
		t.Skip("mkfifo:", err)
	}
	done := make(chan bool, 1)
	go func() {
		f, ok := OpenRegular(p)
		if f != nil {
			f.Close()
		}
		done <- ok
	}()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("a FIFO was accepted as a regular file")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OpenRegular blocked on a FIFO")
	}
}

func TestOpenRegularRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	write(t, target, "gitdir: /x/.git/worktrees/wt")
	link := filepath.Join(dir, ".git")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if f, ok := OpenRegular(link); ok {
		f.Close()
		t.Fatal("a symlinked .git was followed")
	}
	if f, ok := OpenRegular(target); !ok {
		t.Fatal("a regular file was rejected")
	} else {
		f.Close()
	}
}

// real is the symlink-free spelling of a temp dir (macOS temp dirs sit behind
// /var -> /private/var), the form WorkspaceRoot reports.
func real(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestWorkspaceRootMainCheckout(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	mkdir(t, filepath.Join(repo, ".git"))
	mkdir(t, filepath.Join(repo, "a", "b"))
	if got, want := WorkspaceRoot(filepath.Join(repo, "a", "b")), real(t, repo); got != want {
		t.Fatalf("WorkspaceRoot = %q, want %q", got, want)
	}
}

func TestWorkspaceRootLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	mkdir(t, filepath.Join(repo, ".git", "worktrees", "wt"))
	wt := filepath.Join(root, "wt")
	write(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n")
	mkdir(t, filepath.Join(wt, "sub"))
	if got, want := WorkspaceRoot(filepath.Join(wt, "sub")), real(t, repo); got != want {
		t.Fatalf("WorkspaceRoot = %q, want the main repo %q", got, want)
	}
}

// The .git file is box-writable: a pointer at a worktree gitdir that does not
// exist (or is not a directory) must not make the session report that other
// repository, only its own checkout.
func TestWorkspaceRootIgnoresAForgedGitFile(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(root, "victim")
	mkdir(t, filepath.Join(victim, ".git", "worktrees"))
	wt := filepath.Join(root, "wt")
	mkdir(t, wt)
	forged := filepath.Join(victim, ".git", "worktrees", "y")
	write(t, filepath.Join(wt, ".git"), "gitdir: "+forged+"\n")
	if got, want := WorkspaceRoot(wt), real(t, wt); got != want {
		t.Fatalf("missing gitdir: WorkspaceRoot = %q, want the checkout's own top %q", got, want)
	}
	// A file where the gitdir should be is no gitdir either.
	write(t, forged, "x")
	if got, want := WorkspaceRoot(wt), real(t, wt); got != want {
		t.Fatalf("gitdir is a file: WorkspaceRoot = %q, want the checkout's own top %q", got, want)
	}
}

// A worktree of a bare repository has no main checkout to group under: its own
// top is the workspace.
func TestWorkspaceRootWorktreeOfBareRepo(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "bare.git", "worktrees", "wt"))
	wt := filepath.Join(root, "wt")
	write(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(root, "bare.git", "worktrees", "wt")+"\n")
	if got, want := WorkspaceRoot(wt), real(t, wt); got != want {
		t.Fatalf("WorkspaceRoot = %q, want the worktree's own top %q", got, want)
	}
}

func TestWorkspaceRootOutsideRepo(t *testing.T) {
	if got := WorkspaceRoot(t.TempDir()); got != "" {
		t.Fatalf("WorkspaceRoot outside a repo = %q, want empty", got)
	}
	if got := WorkspaceRoot(""); got != "" {
		t.Fatalf("WorkspaceRoot of empty = %q, want empty", got)
	}
}

func TestWorkspaceRootResolvesSymlinks(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	mkdir(t, filepath.Join(repo, ".git"))
	mkdir(t, filepath.Join(repo, "a"))
	link := filepath.Join(root, "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	viaLink, direct := WorkspaceRoot(filepath.Join(link, "a")), WorkspaceRoot(filepath.Join(repo, "a"))
	if viaLink != direct || viaLink != Normalize(link) || viaLink != real(t, repo) {
		t.Fatalf("via link %q, direct %q, Normalize(link) %q: want one spelling", viaLink, direct, Normalize(link))
	}
}

func TestNormalize(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(root, filepath.Join(root, "l")); err != nil {
		t.Fatal(err)
	}
	if got, want := Normalize(filepath.Join(root, "l")+"/"), real(t, root); got != want {
		t.Fatalf("Normalize(link) = %q, want %q", got, want)
	}
	// A path that does not exist falls back to Clean.
	if got := Normalize("/no/such/../dir/"); got != "/no/dir" {
		t.Fatalf("Normalize(missing) = %q, want /no/dir", got)
	}
	if got := Normalize(""); got != "" {
		t.Fatalf("Normalize(empty) = %q, want empty", got)
	}
}

// recordIO swaps the package's filesystem seams for ones that note every path they are handed, for the test's life.
func recordIO(t *testing.T) *[]string {
	t.Helper()
	var seen []string
	ol, oo := lstat, openFile
	lstat = func(p string) (os.FileInfo, error) { seen = append(seen, p); return ol(p) }
	openFile = func(p string, flag int, perm os.FileMode) (*os.File, error) {
		seen = append(seen, p)
		return oo(p, flag, perm)
	}
	t.Cleanup(func() { lstat, openFile = ol, oo })
	return &seen
}

// #364: a worktree dir read from git's registry is text the repository's owner (an agent) wrote. The caller's
// no-I/O check (allow) judges it before anything stats or opens it, and nothing on the way to it follows a link: a
// planted pointer to a path outside what the caller serves, or to a path inside it that crosses a link out, makes no
// filesystem call on that path at all.
func TestWorktreesAskBeforeTouching(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	served := filepath.Join(base, "served")
	common := filepath.Join(served, "proj", ".git")
	inside := filepath.Join(served, "inside")
	addWorktree(t, common, "inside", inside)
	// A real checkout that answers the pointer, but outside what the caller serves.
	outside := filepath.Join(base, "outside")
	addWorktree(t, common, "far", filepath.Join(outside, "far"))
	// A path inside the served tree, through a link that leads outside it.
	if err := os.Symlink(outside, filepath.Join(served, "evil")); err != nil {
		t.Fatal(err)
	}
	gd := filepath.Join(common, "worktrees", "via")
	write(t, filepath.Join(gd, "gitdir"), filepath.Join(served, "evil", "via", ".git")+"\n")
	write(t, filepath.Join(outside, "via", ".git"), "gitdir: "+gd+"\n")

	var asked []string
	allow := func(dir string) bool {
		asked = append(asked, dir)
		return dir == served || strings.HasPrefix(dir, served+"/")
	}
	seen := recordIO(t)
	got := Worktrees(common, 64, allow)
	if len(got) != 1 || got[0] != inside {
		t.Errorf("Worktrees = %v, want [%s]", got, inside)
	}
	for _, p := range *seen {
		if strings.HasPrefix(p, outside+"/") || strings.HasPrefix(p, filepath.Join(served, "evil")+"/") {
			t.Errorf("touched %s: a path outside the served tree", p)
		}
	}
	if !slices.Contains(asked, filepath.Join(outside, "far")) {
		t.Errorf("allow was not asked about the outside checkout: %v", asked)
	}
	// A nil check serves nothing: the caller must say what it serves.
	if got := Worktrees(common, 64, nil); len(got) != 0 {
		t.Errorf("nil allow: %v", got)
	}
}
