//go:build linux

package boxvm

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// localSh runs the command `colima ssh --profile P -- <argv>` would run in the
// VM, here, with the host's sh and GNU coreutils: the VM is Ubuntu (dash, GNU
// coreutils), so on a Linux dev box this proves the script itself. Output is
// combined like ExecRunner's.
type localSh struct{}

func (localSh) Run(ctx context.Context, env []string, _ string, args ...string) (string, error) {
	if len(args) < 6 || args[0] != "ssh" || args[3] != "--" {
		return "", fmt.Errorf("unexpected argv %q", args)
	}
	out, err := exec.CommandContext(ctx, args[4], args[5:]...).CombinedOutput()
	return string(out), err
}

func skipWithoutGNU(t *testing.T) {
	t.Helper()
	for _, c := range [][]string{{"sh", "-c", "true"}, {"base64", "--version"}, {"realpath", "-e", "/"}, {"stat", "-c", "%d:%i", "/"}} {
		if err := exec.Command(c[0], c[1:]...).Run(); err != nil {
			t.Skipf("needs GNU coreutils (%v): %v", c, err)
		}
	}
}

func devIno(t *testing.T, p string) FileID {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	return FileID{Dev: uint64(st.Dev), Ino: uint64(st.Ino)}
}

// scriptTree is a real directory tree under a short path (a unix socket's path
// is capped near 100 bytes) with no symlink in its own path.
func scriptTree(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(shortRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestPathIDsScriptReturnsTheRealIdentity(t *testing.T) {
	skipWithoutGNU(t)
	root := scriptTree(t)
	var paths []string
	for _, name := range []string{"plain", "with space", "$HOME $(id)", "it's", "back`tick`", "Tài liệu Việt", "new\nline", "semi;&|\"\\"} {
		p := filepath.Join(root, name)
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	file := filepath.Join(root, "plain", "a file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths = append(paths, file)

	ids, err := vmWith(t, localSh{}).PathIDs(context.Background(), paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != len(paths) {
		t.Fatalf("%d ids for %d paths", len(ids), len(paths))
	}
	for i, p := range paths {
		if want := devIno(t, p); ids[i] != want {
			t.Errorf("%q: id %+v, want %+v", p, ids[i], want)
		}
	}
	// Nothing in those names ran: the tree holds only what the test made.
	if ents, _ := os.ReadDir(root); len(ents) != 8 {
		t.Errorf("tree has %d entries, want the 8 the test made", len(ents))
	}
}

func TestPathIDsScriptRefusesWhatTheLinuxWalkRefuses(t *testing.T) {
	skipWithoutGNU(t)
	root := scriptTree(t)
	mk := func(parts ...string) string {
		p := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ln := func(target, link string) string {
		link = filepath.Join(root, link)
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		return link
	}
	real := mk("real", "sub")
	_ = real
	linkParent := ln(filepath.Join(root, "real"), "linkparent") // linkparent/sub is real/sub through a link
	linkLeaf := ln(filepath.Join(root, "real", "sub"), "linkleaf")
	dangling := ln(filepath.Join(root, "nowhere"), "dangling")
	relLink := ln("real", "rellink")

	fifo := filepath.Join(root, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(root, "sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	for name, p := range map[string]string{
		"symlinked parent":   filepath.Join(linkParent, "sub"),
		"symlinked leaf":     linkLeaf,
		"dangling leaf":      dangling,
		"relative symlink":   filepath.Join(relLink, "sub"),
		"fifo":               fifo,
		"unix socket":        sock,
		"missing":            filepath.Join(root, "missing"),
		"relative path":      "real/sub",
		"unclean path":       filepath.Join(root, "real") + "/../real/sub",
		"trailing slash":     filepath.Join(root, "real") + "/",
		"character device":   "/dev/null",
		"empty":              "",
		"parent dir symlink": filepath.Join(root, "real", "..", "linkparent"),
	} {
		t.Run(name, func(t *testing.T) {
			ids, err := vmWith(t, localSh{}).PathIDs(context.Background(), []string{p})
			if err == nil || ids != nil {
				t.Fatalf("%q must be refused: ids=%v err=%v", p, ids, err)
			}
		})
	}

	// One bad path among good ones refuses the whole batch.
	ids, err := vmWith(t, localSh{}).PathIDs(context.Background(), []string{filepath.Join(root, "real"), linkLeaf, filepath.Join(root, "real", "sub")})
	if err == nil || ids != nil {
		t.Fatalf("a batch with a symlink must be refused: ids=%v err=%v", ids, err)
	}
}

// A PARENT of the listed path flipped between a real directory and a symlink to
// another directory while the script runs. GNU stat follows every parent
// component, so a script that stats the full path more than once can see the
// symlink at one stat, the real directory at its symlink check, and the symlink
// again at the next stat, and print the TARGET's identity: docker, resolving
// through the same link, then mounts the target and the in-box compare agrees.
// The script must hold one directory and check the leaf relative to it, so it
// can refuse, but never print the secret's identity.
func TestPathIDsScriptNeverPrintsATargetWhenAParentIsFlipped(t *testing.T) {
	skipWithoutGNU(t)
	root := scriptTree(t)
	for _, d := range []string{"shared/team/docs", "secret/docs"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	team, swap := filepath.Join(root, "shared", "team"), filepath.Join(root, "shared", "swap")
	if err := os.Symlink(filepath.Join(root, "secret"), swap); err != nil {
		t.Fatal(err)
	}
	listed := filepath.Join(team, "docs")
	real, secret := devIno(t, listed), devIno(t, filepath.Join(root, "secret", "docs"))
	if real == secret {
		t.Fatal("the two directories share an identity; the test cannot tell them apart")
	}
	flip := func() error {
		return unix.Renameat2(unix.AT_FDCWD, team, unix.AT_FDCWD, swap, unix.RENAME_EXCHANGE)
	}
	if err := flip(); err != nil { // also proves the filesystem can exchange a dir and a symlink
		t.Skipf("RENAME_EXCHANGE unavailable here: %v", err)
	}
	if err := flip(); err != nil {
		t.Fatal(err)
	}

	stop, flips := make(chan struct{}), make(chan int)
	go func() {
		n := 0
		for {
			select {
			case <-stop:
				flips <- n
				return
			default:
			}
			if flip() != nil {
				flips <- n
				return
			}
			n++
		}
	}()

	const runs = 300
	v := vmWith(t, localSh{})
	var accepted, refused, leaked int
	for i := 0; i < runs; i++ {
		ids, err := v.PathIDs(context.Background(), []string{listed})
		switch {
		case err != nil:
			refused++
		case ids[0] == secret:
			leaked++
		case ids[0] == real:
			accepted++
		default:
			t.Errorf("run %d printed %+v, which is neither directory", i, ids[0])
		}
	}
	close(stop)
	n := <-flips
	t.Logf("%d runs while %d flips: %d accepted the real dir, %d refused, %d printed the secret's identity", runs, n, accepted, refused, leaked)
	if n == 0 {
		t.Fatal("the parent was never flipped; the test proved nothing")
	}
	if leaked != 0 {
		t.Fatalf("%d of %d runs printed the identity of the directory a flipped parent led to", leaked, runs)
	}
}

// Paths the script splits into parent and leaf must stay refusable: a leaf of
// "." or ".." would name the parent itself, and a root-level path has "/" as
// its parent.
func TestPathIDsScriptSplitsParentAndLeafStrictly(t *testing.T) {
	skipWithoutGNU(t)
	root := scriptTree(t)
	if err := os.MkdirAll(filepath.Join(root, "real", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]string{
		"dot leaf":       filepath.Join(root, "real") + "/.",
		"dotdot leaf":    filepath.Join(root, "real", "sub") + "/..",
		"double slash":   "/" + filepath.Join(root, "real"),
		"repeated slash": filepath.Join(root, "real") + "//sub",
		"root":           "/",
		"only slashes":   "//",
	} {
		t.Run(name, func(t *testing.T) {
			if ids, err := vmWith(t, localSh{}).PathIDs(context.Background(), []string{p}); err == nil || ids != nil {
				t.Fatalf("%q must be refused: ids=%v err=%v", p, ids, err)
			}
		})
	}
	// A root-level path has "/" as its parent and must still work.
	if fi, err := os.Lstat("/tmp"); err == nil && fi.IsDir() {
		ids, err := vmWith(t, localSh{}).PathIDs(context.Background(), []string{"/tmp"})
		if err != nil || len(ids) != 1 || ids[0] != devIno(t, "/tmp") {
			t.Fatalf("/tmp: ids=%v err=%v, want its identity", ids, err)
		}
	}
	// A leaf with its own awkward bytes under a parent with awkward bytes.
	both := filepath.Join(root, "par ent $x", "le'af `y`")
	if err := os.MkdirAll(both, 0o755); err != nil {
		t.Fatal(err)
	}
	if ids, err := vmWith(t, localSh{}).PathIDs(context.Background(), []string{both}); err != nil || len(ids) != 1 || ids[0] != devIno(t, both) {
		t.Fatalf("%q: ids=%v err=%v, want its identity", both, ids, err)
	}
}
