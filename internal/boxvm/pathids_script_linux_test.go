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
