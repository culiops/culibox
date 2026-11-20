package gitrepo

import (
	"os"
	"path/filepath"
	"sort"
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

func TestLinkedWorktreesFilters(t *testing.T) {
	root := t.TempDir()
	common := filepath.Join(root, ".git")
	wts := filepath.Join(root, ".claude", "worktrees")
	mkdir(t, wts)

	// valid, absolute gitdir
	mkdir(t, filepath.Join(common, "worktrees", "b"))
	write(t, filepath.Join(wts, "b", ".git"), "gitdir: "+filepath.Join(common, "worktrees", "b")+"\n")
	// valid, relative gitdir
	mkdir(t, filepath.Join(common, "worktrees", "a"))
	write(t, filepath.Join(wts, "a", ".git"), "gitdir: ../../../.git/worktrees/a\n")

	// symlinked entry to a valid-looking worktree
	mkdir(t, filepath.Join(common, "worktrees", "link"))
	real := filepath.Join(t.TempDir(), "real")
	write(t, filepath.Join(real, ".git"), "gitdir: "+filepath.Join(common, "worktrees", "link")+"\n")
	if err := os.Symlink(real, filepath.Join(wts, "link")); err != nil {
		t.Fatal(err)
	}
	// plain file
	write(t, filepath.Join(wts, "plainfile"), "x")
	// .git pointing at another repo's common dir
	other := t.TempDir()
	mkdir(t, filepath.Join(other, ".git", "worktrees", "o"))
	write(t, filepath.Join(wts, "o", ".git"), "gitdir: "+filepath.Join(other, ".git", "worktrees", "o")+"\n")
	// .git naming a missing gitdir
	write(t, filepath.Join(wts, "gone", ".git"), "gitdir: "+filepath.Join(common, "worktrees", "gone")+"\n")
	// entry with no .git at all
	mkdir(t, filepath.Join(wts, "nogit"))

	want := []string{filepath.Join(wts, "a"), filepath.Join(wts, "b")}
	got := LinkedWorktrees(root, common, 64)
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("LinkedWorktrees = %v, want %v", got, want)
	}
	if got := LinkedWorktrees(root, common, 0); len(got) != 0 {
		t.Fatalf("max=0: %v", got)
	}
	if got := LinkedWorktrees(t.TempDir(), common, 64); len(got) != 0 {
		t.Fatalf("no .claude/worktrees: %v", got)
	}
}

// The directory read itself is bounded: with many valid entries, max caps how
// many come back, and the result is sorted.
func TestLinkedWorktreesBoundsTheRead(t *testing.T) {
	root := t.TempDir()
	common := filepath.Join(root, ".git")
	wts := filepath.Join(root, ".claude", "worktrees")
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		mkdir(t, filepath.Join(common, "worktrees", n))
		write(t, filepath.Join(wts, n, ".git"), "gitdir: "+filepath.Join(common, "worktrees", n)+"\n")
	}
	got := LinkedWorktrees(root, common, 3)
	if len(got) != 3 || !sort.StringsAreSorted(got) {
		t.Fatalf("max=3: %v", got)
	}
	if got := LinkedWorktrees(root, common, 100); len(got) != 6 {
		t.Fatalf("max=100: %v", got)
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
