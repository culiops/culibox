package clone

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/config"
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
	if err := os.Remove(hp); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", hp); err != nil {
		t.Fatal(err)
	}
	if b, sha, ok := ReadHead(x.dir); ok {
		t.Fatalf("ReadHead followed a symlink out of the clone: %q, %q", b, sha)
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
