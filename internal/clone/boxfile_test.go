package clone

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
)

func openTestRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestOpenBoxFileFIFODoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "f"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := openTestRoot(t, dir)
	done := make(chan error, 1)
	go func() {
		f, _, err := openBoxFile(root, "f")
		if f != nil {
			f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("openBoxFile on a FIFO returned no error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("openBoxFile on a FIFO blocked")
	}
}

func TestReadBoxFileRefuses(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, n int) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Repeat("x", n)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("ok", 10)
	write("big", 11)
	if err := os.Symlink("ok", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := openTestRoot(t, dir)

	got, err := readBoxFile(root, "ok", 10)
	if err != nil || string(got) != strings.Repeat("x", 10) {
		t.Fatalf("regular file at the limit = %q, %v", got, err)
	}
	for _, name := range []string{"link", "d", "big", "absent"} {
		if _, err := readBoxFile(root, name, 10); err == nil {
			t.Errorf("readBoxFile(%q) returned no error", name)
		}
	}
}

// headFx is a real clone (made by EnsureClone) on branch "feat" with one commit.
type headFx struct {
	t   *testing.T
	dir string
}

func newHeadFx(t *testing.T) *headFx {
	t.Helper()
	rec := newRec(t)
	of := makeOf(t, true)
	fixtureGit(t, of, "branch", "-M", "main")
	c := config.Clone{Of: of, Dir: filepath.Join(t.TempDir(), "clone")}
	if _, err := EnsureClone(context.Background(), rec, c, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	x := &headFx{t: t, dir: c.Dir}
	fixtureGit(t, x.dir, "checkout", "-q", "-b", "feat")
	if err := os.WriteFile(filepath.Join(x.dir, "feat.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, x.dir, "add", "feat.txt")
	fixtureGit(t, x.dir, "commit", "-q", "-m", "add feat")
	return x
}

func (x *headFx) head() string {
	x.t.Helper()
	g, err := NewExecGit()
	if err != nil {
		x.t.Skip("no git on PATH")
	}
	out, err := g.Run(context.Background(), x.dir, CleanEnv(), "rev-parse", "HEAD")
	if err != nil {
		x.t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func (x *headFx) writeHEAD(s string) {
	x.t.Helper()
	if err := os.WriteFile(filepath.Join(x.dir, ".git", "HEAD"), []byte(s), 0o644); err != nil {
		x.t.Fatal(err)
	}
}

func TestReadHeadBranchLooseRef(t *testing.T) {
	x := newHeadFx(t)
	b, sha, ok := ReadHead(x.dir)
	if b != "feat" || sha != x.head() || !ok {
		t.Fatalf("ReadHead = %q, %q, %v; want feat, %s, true", b, sha, ok, x.head())
	}
}

func TestReadHeadPackedRef(t *testing.T) {
	x := newHeadFx(t)
	fixtureGit(t, x.dir, "pack-refs", "--all")
	if _, err := os.Lstat(filepath.Join(x.dir, ".git", "refs", "heads", "feat")); err == nil {
		t.Fatal("pack-refs left the loose ref in place; the test would not reach packed-refs")
	}
	b, sha, ok := ReadHead(x.dir)
	if b != "feat" || sha != x.head() || !ok {
		t.Fatalf("ReadHead = %q, %q, %v; want feat, %s, true", b, sha, ok, x.head())
	}
}

func TestReadHeadDetached(t *testing.T) {
	x := newHeadFx(t)
	fixtureGit(t, x.dir, "checkout", "-q", "--detach")
	b, sha, ok := ReadHead(x.dir)
	if b != "" || sha != x.head() || !ok {
		t.Fatalf("ReadHead = %q, %q, %v; want \"\", %s, true", b, sha, ok, x.head())
	}
}

func TestReadHeadRefusesSymlinkOut(t *testing.T) {
	x := newHeadFx(t)
	hp := filepath.Join(x.dir, ".git", "HEAD")
	// Both targets hold a valid SHA, so a followed link would give ok == true:
	// one outside the clone (os.Root containment), one inside (the Lstat and
	// SameFile checks, which os.Root's O_NOFOLLOW handling does not provide).
	outside := filepath.Join(t.TempDir(), "head")
	inside := filepath.Join(x.dir, ".git", "HEAD.real")
	for _, p := range []string{outside, inside} {
		if err := os.WriteFile(p, []byte(x.head()+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{"outside": outside, "inside": "HEAD.real"} {
		if err := os.Remove(hp); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.Symlink(target, hp); err != nil {
			t.Fatal(err)
		}
		if b, sha, ok := ReadHead(x.dir); ok {
			t.Errorf("%s: ReadHead followed a symlinked HEAD: %q, %q", name, b, sha)
		}
	}
}

func TestReadHeadRefusesFIFORef(t *testing.T) {
	x := newHeadFx(t)
	rp := filepath.Join(x.dir, ".git", "refs", "heads", "feat")
	if err := os.Remove(rp); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(rp, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() { _, _, ok := ReadHead(x.dir); done <- ok }()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("ReadHead accepted a FIFO ref")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadHead blocked on a FIFO ref")
	}
}

func TestReadHeadRefusesBadContent(t *testing.T) {
	x := newHeadFx(t)
	for name, content := range map[string]string{
		"leading dash":   "ref: refs/heads/-x\n",
		"dotdot":         "ref: refs/heads/a..b\n",
		"not a sha":      "zz" + strings.Repeat("0", 38) + "\n",
		"upper-case":     strings.Repeat("A", 40) + "\n",
		"empty":          "",
		"oversized":      "ref: refs/heads/" + strings.Repeat("a", 5<<10) + "\n",
		"other ref kind": "ref: refs/remotes/origin/main\n",
	} {
		x.writeHEAD(content)
		if b, sha, ok := ReadHead(x.dir); ok {
			t.Errorf("%s: ReadHead = %q, %q, true; want ok == false", name, b, sha)
		}
	}
}

func TestCheckExistingRefusesFIFOConfig(t *testing.T) {
	rec := newRec(t)
	of := makeOf(t, true)
	c := config.Clone{Of: of, Dir: filepath.Join(t.TempDir(), "clone")}
	if _, err := EnsureClone(context.Background(), rec, c, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(c.Dir, ".git", "config")
	if err := os.Remove(cp); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(cp, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := EnsureClone(context.Background(), rec, c, t.TempDir())
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, box.ErrUnboxableWorkspace) {
			t.Fatalf("err = %v, want ErrUnboxableWorkspace", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("EnsureClone blocked on a FIFO .git/config")
	}
}

// refsFx is a clone fixture of plain files: RefsFingerprint reads metadata
// only, so no git is needed.
type refsFx struct {
	dir string
}

func newRefsFx(t *testing.T) *refsFx {
	t.Helper()
	x := &refsFx{dir: t.TempDir()}
	x.write(t, ".git/HEAD", "ref: refs/heads/main\n")
	x.write(t, ".git/index", "index")
	x.write(t, ".git/refs/heads/main", "0123456789012345678901234567890123456789\n")
	return x
}

func (x *refsFx) write(t *testing.T, name, content string) {
	t.Helper()
	p := filepath.Join(x.dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (x *refsFx) fp(t *testing.T) string {
	t.Helper()
	fp, ok := RefsFingerprint(x.dir)
	if !ok || fp == "" {
		t.Fatalf("RefsFingerprint(%s) = %q, %v; want a fingerprint", x.dir, fp, ok)
	}
	return fp
}

func TestRefsFingerprint(t *testing.T) {
	t.Run("same repo twice is equal", func(t *testing.T) {
		x := newRefsFx(t)
		if a, b := x.fp(t), x.fp(t); a != b {
			t.Fatalf("fingerprint changed with no change: %s vs %s", a, b)
		}
	})
	t.Run("a new loose ref changes it", func(t *testing.T) {
		x := newRefsFx(t)
		before := x.fp(t)
		x.write(t, ".git/refs/heads/feat", "0123456789012345678901234567890123456789\n")
		if x.fp(t) == before {
			t.Fatal("a new loose ref did not change the fingerprint")
		}
	})
	t.Run("a nested loose ref changes it", func(t *testing.T) {
		x := newRefsFx(t)
		before := x.fp(t)
		x.write(t, ".git/refs/heads/feat/deep/x", "0123456789012345678901234567890123456789\n")
		if x.fp(t) == before {
			t.Fatal("a nested loose ref did not change the fingerprint")
		}
	})
	t.Run("rewriting packed-refs changes it", func(t *testing.T) {
		x := newRefsFx(t)
		before := x.fp(t) // packed-refs absent
		x.write(t, ".git/packed-refs", "# pack-refs\n")
		withPacked := x.fp(t)
		if withPacked == before {
			t.Fatal("creating packed-refs did not change the fingerprint")
		}
		x.write(t, ".git/packed-refs", "# pack-refs with: peeled\n0123456789012345678901234567890123456789 refs/heads/main\n")
		if x.fp(t) == withPacked {
			t.Fatal("rewriting packed-refs did not change the fingerprint")
		}
	})
	t.Run("touching the index mtime changes it", func(t *testing.T) {
		x := newRefsFx(t)
		before := x.fp(t)
		later := time.Now().Add(time.Hour)
		if err := os.Chtimes(filepath.Join(x.dir, ".git/index"), later, later); err != nil {
			t.Fatal(err)
		}
		if x.fp(t) == before {
			t.Fatal("touching .git/index did not change the fingerprint")
		}
	})
	t.Run("a symlinked ref is recorded, never followed", func(t *testing.T) {
		x := newRefsFx(t)
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(outside, []byte("one"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(x.dir, ".git/refs/heads/x")); err != nil {
			t.Fatal(err)
		}
		before := x.fp(t)
		if err := os.WriteFile(outside, []byte("changed and longer"), 0o644); err != nil {
			t.Fatal(err)
		}
		later := time.Now().Add(time.Hour)
		if err := os.Chtimes(outside, later, later); err != nil {
			t.Fatal(err)
		}
		if x.fp(t) != before {
			t.Fatal("the fingerprint followed a symlink out of the clone")
		}
	})
	t.Run("a symlinked directory under refs/heads is not descended", func(t *testing.T) {
		x := newRefsFx(t)
		outDir := t.TempDir()
		if err := os.Symlink(outDir, filepath.Join(x.dir, ".git/refs/heads/d")); err != nil {
			t.Fatal(err)
		}
		before := x.fp(t)
		if err := os.WriteFile(filepath.Join(outDir, "new"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if x.fp(t) != before {
			t.Fatal("the fingerprint walked into a symlinked directory")
		}
	})
	t.Run("more than 2000 loose refs is not ok", func(t *testing.T) {
		x := newRefsFx(t)
		for i := 0; i < 2001; i++ {
			x.write(t, fmt.Sprintf(".git/refs/heads/r%04d", i), "x")
		}
		if fp, ok := RefsFingerprint(x.dir); ok {
			t.Fatalf("RefsFingerprint over 2001 refs = %q, true; want ok=false", fp)
		}
	})
	t.Run("exactly 2000 entries is ok", func(t *testing.T) {
		x := newRefsFx(t)
		x.write(t, ".git/refs/heads/r0", "x")
		for i := 1; i < 1999; i++ { // main + r0..r1998 = 2000
			x.write(t, fmt.Sprintf(".git/refs/heads/r%04d", i), "x")
		}
		x.fp(t)
	})
	t.Run("changing HEAD changes it", func(t *testing.T) {
		x := newRefsFx(t)
		before := x.fp(t)
		x.write(t, ".git/HEAD", "ref: refs/heads/other-branch\n")
		if x.fp(t) == before {
			t.Fatal("rewriting .git/HEAD did not change the fingerprint")
		}
	})
	t.Run("refs/heads absent is ok and stable", func(t *testing.T) {
		x := newRefsFx(t)
		if err := os.RemoveAll(filepath.Join(x.dir, ".git/refs")); err != nil {
			t.Fatal(err)
		}
		if a, b := x.fp(t), x.fp(t); a != b {
			t.Fatalf("fingerprint with no refs/heads is unstable: %s vs %s", a, b)
		}
	})
	t.Run("refs/heads itself a symlink is recorded, not followed", func(t *testing.T) {
		x := newRefsFx(t)
		outDir := t.TempDir()
		if err := os.RemoveAll(filepath.Join(x.dir, ".git/refs/heads")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outDir, filepath.Join(x.dir, ".git/refs/heads")); err != nil {
			t.Fatal(err)
		}
		before := x.fp(t)
		if err := os.WriteFile(filepath.Join(outDir, "new"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if x.fp(t) != before {
			t.Fatal("the fingerprint followed a symlinked refs/heads")
		}
	})
	t.Run("a FIFO under refs/heads does not block", func(t *testing.T) {
		x := newRefsFx(t)
		if err := syscall.Mkfifo(filepath.Join(x.dir, ".git/refs/heads/x"), 0o644); err != nil {
			t.Fatal(err)
		}
		done := make(chan bool, 1)
		go func() {
			_, ok := RefsFingerprint(x.dir)
			done <- ok
		}()
		select {
		case ok := <-done:
			if !ok {
				t.Fatal("a FIFO ref (metadata only) should still fingerprint")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("RefsFingerprint blocked on a FIFO")
		}
	})
	t.Run("a FIFO in place of refs/heads does not block", func(t *testing.T) {
		x := newRefsFx(t)
		if err := os.RemoveAll(filepath.Join(x.dir, ".git/refs/heads")); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(filepath.Join(x.dir, ".git/refs/heads"), 0o644); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			RefsFingerprint(x.dir)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("RefsFingerprint blocked on a FIFO refs/heads")
		}
	})
	t.Run("removing an entry changes it", func(t *testing.T) {
		x := newRefsFx(t)
		x.write(t, ".git/refs/heads/a.lock", "x")
		with := x.fp(t)
		if err := os.Remove(filepath.Join(x.dir, ".git/refs/heads/a.lock")); err != nil {
			t.Fatal(err)
		}
		if x.fp(t) == with {
			t.Fatal("removing an entry did not change the fingerprint")
		}
	})
	t.Run("the cap is enforced while reading a directory", func(t *testing.T) {
		x := newRefsFx(t)
		for i := 0; i < 2600; i++ { // several 256-entry batches past the cap
			x.write(t, fmt.Sprintf(".git/refs/heads/r%04d", i), "x")
		}
		if _, ok := RefsFingerprint(x.dir); ok {
			t.Fatal("2601 refs in one directory should be refused")
		}
	})
	t.Run("no .git is not ok", func(t *testing.T) {
		if fp, ok := RefsFingerprint(t.TempDir()); ok {
			t.Fatalf("RefsFingerprint with no .git = %q, true", fp)
		}
	})
	t.Run(".git a file is not ok", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if fp, ok := RefsFingerprint(dir); ok {
			t.Fatalf("RefsFingerprint with .git as a file = %q, true", fp)
		}
	})
	t.Run("a missing clone dir is not ok", func(t *testing.T) {
		if fp, ok := RefsFingerprint(filepath.Join(t.TempDir(), "nope")); ok {
			t.Fatalf("RefsFingerprint of a missing dir = %q, true", fp)
		}
	})
}
