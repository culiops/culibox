package abduco

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// resolveFixture is a culibox "install dir" with an optional abduco next to it,
// and a Lookup whose PATH holds exactly onPath (or nothing when it is "").
type resolveFixture struct {
	dir    string // where culibox lives
	exe    string // dir/culibox
	onPath string // what LookPath("abduco") returns; "" = not found
}

// realTempDir is t.TempDir() by its resolved path: on macOS it sits under /var, a
// symlink, and the resolver compares resolved paths.
func realTempDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// t.TempDir makes its folder 0777 &^ umask: 0775 under umask 002, which the
	// resolver refuses. The tests set every mode they rely on themselves.
	if err := os.Chmod(d, 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

// writeExec writes a script at p with mode 0755 whatever the umask.
func writeExec(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

// mkdir755 makes dir with mode 0755 whatever the umask.
func mkdir755(t *testing.T, dir string) {
	t.Helper()
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func newResolveFixture(t *testing.T) *resolveFixture {
	t.Helper()
	dir := realTempDir(t)
	exe := filepath.Join(dir, "culibox")
	writeExec(t, exe)
	return &resolveFixture{dir: dir, exe: exe}
}

// bundle writes dir/abduco with mode (chmod after create, so the umask cannot
// drop a bit the test means to set).
func (f *resolveFixture) bundle(t *testing.T, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(f.dir, "abduco")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func (f *resolveFixture) lookup(uid int) Lookup {
	return Lookup{
		Executable: func() (string, error) { return f.exe, nil },
		LookPath: func(name string) (string, error) {
			if strings.Contains(name, "/") {
				return exec.LookPath(name)
			}
			if name == "abduco" && f.onPath != "" {
				return f.onPath, nil
			}
			return "", exec.ErrNotFound
		},
		Uid: uid,
	}
}

func TestResolveConfiguredWins(t *testing.T) {
	f := newResolveFixture(t)
	f.bundle(t, 0o755)
	f.onPath = "/usr/bin/abduco"
	other := filepath.Join(realTempDir(t), "my-abduco")
	writeExec(t, other)
	r, err := f.lookup(os.Getuid()).Resolve(other)
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != SourceConfig || r.Path != other {
		t.Fatalf("got %+v, want %s from abduco_bin", r, other)
	}
}

func TestResolveConfiguredMissing(t *testing.T) {
	f := newResolveFixture(t)
	f.bundle(t, 0o755)
	f.onPath = "/usr/bin/abduco"
	_, err := f.lookup(os.Getuid()).Resolve(filepath.Join(realTempDir(t), "nope"))
	if err == nil || !strings.Contains(err.Error(), "abduco_bin") {
		t.Fatalf("err = %v, want an abduco_bin error and no fallback", err)
	}
}

func TestResolveBundledBeforePath(t *testing.T) {
	f := newResolveFixture(t)
	want := f.bundle(t, 0o755)
	f.onPath = "/usr/bin/abduco"
	r, err := f.lookup(os.Getuid()).Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != SourceBundled || r.Path != want || r.Skipped != "" {
		t.Fatalf("got %+v, want %s next to culibox", r, want)
	}
}

func TestResolvePathWhenNoBundled(t *testing.T) {
	f := newResolveFixture(t)
	f.onPath = "/usr/bin/abduco"
	r, err := f.lookup(os.Getuid()).Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != SourcePath || r.Path != "/usr/bin/abduco" || r.Skipped != "" {
		t.Fatalf("got %+v, want /usr/bin/abduco from PATH with nothing skipped", r)
	}
}

// A culibox reached through a symlink (~/.local/bin/culibox -> /opt/culibox/culibox)
// finds the abduco next to the real file, not next to the link.
func TestResolveFollowsExecutableSymlink(t *testing.T) {
	f := newResolveFixture(t)
	want := f.bundle(t, 0o755)
	linkDir := realTempDir(t)
	link := filepath.Join(linkDir, "culibox")
	if err := os.Symlink(f.exe, link); err != nil {
		t.Fatal(err)
	}
	f.exe = link
	f.onPath = "/usr/bin/abduco"
	r, err := f.lookup(os.Getuid()).Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	// t.TempDir may sit under a symlinked parent (macOS /var): compare real paths.
	wantReal, _ := filepath.EvalSymlinks(want)
	gotReal, _ := filepath.EvalSymlinks(r.Path)
	if r.Source != SourceBundled || gotReal != wantReal {
		t.Fatalf("got %+v, want %s next to the link's target", r, want)
	}
}

// Someone else's file next to culibox (a plant in a sticky shared dir like /tmp) is
// never run.
func TestResolveSkipsForeignOwner(t *testing.T) {
	f := newResolveFixture(t)
	f.bundle(t, 0o755)
	f.onPath = "/usr/bin/abduco"
	r, err := f.lookup(os.Getuid() + 1).Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	// The folder is checked first, and it is "someone else's" too.
	if r.Source != SourcePath || !strings.Contains(r.Skipped, "folder") || !strings.Contains(r.Skipped, "owned by uid") {
		t.Fatalf("got %+v, want PATH with a folder 'owned by uid' skip reason", r)
	}
}

// The file's own owner check, which a test cannot reach through Resolve without
// root (a folder we own always fails first for a fake uid).
func TestOwnedByForeignUid(t *testing.T) {
	p := filepath.Join(realTempDir(t), "f")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if why := ownedBy(fi, os.Getuid()); why != "" {
		t.Fatalf("own file: %q, want \"\"", why)
	}
	if why := ownedBy(fi, os.Getuid()+1); !strings.Contains(why, "owned by uid") {
		t.Fatalf("foreign uid: %q, want 'owned by uid'", why)
	}
}

func TestResolveSkipsWritableByOthers(t *testing.T) {
	for _, mode := range []os.FileMode{0o775, 0o757} {
		f := newResolveFixture(t)
		f.bundle(t, mode)
		f.onPath = "/usr/bin/abduco"
		r, err := f.lookup(os.Getuid()).Resolve("")
		if err != nil {
			t.Fatal(err)
		}
		if r.Source != SourcePath || !strings.Contains(r.Skipped, "writable") {
			t.Errorf("mode %o: got %+v, want PATH with a 'writable' skip reason", mode, r)
		}
	}
}

func TestResolveSkipsDirectory(t *testing.T) {
	f := newResolveFixture(t)
	mkdir755(t, filepath.Join(f.dir, "abduco"))
	f.onPath = "/usr/bin/abduco"
	r, err := f.lookup(os.Getuid()).Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != SourcePath || r.Skipped == "" {
		t.Fatalf("got %+v, want PATH with a skip reason", r)
	}
}

func TestResolveSkipsNotExecutable(t *testing.T) {
	f := newResolveFixture(t)
	f.bundle(t, 0o644)
	f.onPath = "/usr/bin/abduco"
	r, err := f.lookup(os.Getuid()).Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != SourcePath || r.Skipped == "" {
		t.Fatalf("got %+v, want PATH with a skip reason", r)
	}
}

func TestResolveExecutableError(t *testing.T) {
	f := newResolveFixture(t)
	f.onPath = "/usr/bin/abduco"
	l := f.lookup(os.Getuid())
	l.Executable = func() (string, error) { return "", errors.New("no /proc") }
	r, err := l.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != SourcePath || r.Path != "/usr/bin/abduco" {
		t.Fatalf("got %+v, want the PATH abduco", r)
	}
}

func TestResolveNothingFound(t *testing.T) {
	f := newResolveFixture(t)
	_, err := f.lookup(os.Getuid()).Resolve("")
	if err == nil || !strings.Contains(err.Error(), "next to culibox") || !strings.Contains(err.Error(), "PATH") {
		t.Fatalf("err = %v, want one naming both places", err)
	}
}

// A sticky world-writable folder (/tmp): another user can plant a symlink to a
// root-owned binary there and retarget it later, so nothing in it is trusted.
func TestResolveSkipsSymlinkPlantInSharedDir(t *testing.T) {
	f := newResolveFixture(t)
	if err := os.Chmod(f.dir, 0o1777); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/sh", filepath.Join(f.dir, "abduco")); err != nil {
		t.Fatal(err)
	}
	f.onPath = "/usr/bin/abduco"
	r, err := f.lookup(os.Getuid()).Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != SourcePath || !strings.Contains(r.Skipped, "folder") {
		t.Fatalf("got %+v, want PATH with a skip reason about the folder", r)
	}
}

func TestResolveSkipsDirWritableByOthers(t *testing.T) {
	for _, mode := range []os.FileMode{0o775, 0o757, 0o1777} {
		f := newResolveFixture(t)
		f.bundle(t, 0o755)
		if err := os.Chmod(f.dir, mode); err != nil {
			t.Fatal(err)
		}
		f.onPath = "/usr/bin/abduco"
		r, err := f.lookup(os.Getuid()).Resolve("")
		if err != nil {
			t.Fatal(err)
		}
		if r.Source != SourcePath || !strings.Contains(r.Skipped, "folder") {
			t.Errorf("folder mode %o: got %+v, want PATH with a folder skip reason", mode, r)
		}
	}
}

// A symlink next to culibox is only as safe as the folder its target sits in.
func TestResolveSkipsSymlinkIntoSharedDir(t *testing.T) {
	f := newResolveFixture(t)
	shared := realTempDir(t)
	target := filepath.Join(shared, "abduco")
	writeExec(t, target)
	if err := os.Chmod(shared, 0o1777); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(f.dir, "abduco")); err != nil {
		t.Fatal(err)
	}
	f.onPath = "/usr/bin/abduco"
	r, err := f.lookup(os.Getuid()).Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != SourcePath || !strings.Contains(r.Skipped, "link target's folder") {
		t.Fatalf("got %+v, want PATH with a link-target folder skip reason", r)
	}
}

// A symlink is followed one hop only: a link that points at another link (here
// in a shared folder whose owner can retarget it) is refused.
func TestResolveSkipsSymlinkChain(t *testing.T) {
	f := newResolveFixture(t)
	target := filepath.Join(realTempDir(t), "abduco")
	writeExec(t, target)
	shared := realTempDir(t)
	mid := filepath.Join(shared, "abduco")
	if err := os.Symlink(target, mid); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o1777); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(mid, filepath.Join(f.dir, "abduco")); err != nil {
		t.Fatal(err)
	}
	f.onPath = "/usr/bin/abduco"
	r, err := f.lookup(os.Getuid()).Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != SourcePath || !strings.Contains(r.Skipped, "chain") {
		t.Fatalf("got %+v, want PATH with a symlink chain skip reason", r)
	}
}

// A Homebrew-style relative link (bin/abduco -> ../cellar/abduco) is one hop.
func TestResolveAcceptsRelativeSymlink(t *testing.T) {
	root := realTempDir(t)
	bin, cellar := filepath.Join(root, "bin"), filepath.Join(root, "cellar")
	for _, d := range []string{bin, cellar} {
		mkdir755(t, d)
	}
	f := &resolveFixture{dir: bin, exe: filepath.Join(bin, "culibox"), onPath: "/usr/bin/abduco"}
	writeExec(t, f.exe)
	writeExec(t, filepath.Join(cellar, "abduco"))
	if err := os.Symlink(filepath.Join("..", "cellar", "abduco"), filepath.Join(bin, "abduco")); err != nil {
		t.Fatal(err)
	}
	r, err := f.lookup(os.Getuid()).Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != SourceBundled {
		t.Fatalf("got %+v, want the relative link next to culibox", r)
	}
}

// umask 002 systems (RHEL, Fedora) make ~/.local/bin group-writable, but its group
// is the user's private group: nobody else is in it, so that write bit is safe.
func TestResolveAcceptsPrivateGroupWritable(t *testing.T) {
	gid := os.Getgid()
	if gid == 0 {
		t.Skip("group 0 is never a private group (PrivateGid 0 means none)")
	}
	f := newResolveFixture(t)
	p := f.bundle(t, 0o775)
	// Set the group rather than assume it: a new file takes the process's group on
	// Linux but its folder's on macOS, and /tmp there is wheel, gid 0 (#313). The
	// owner may always change a file's group to its own primary group.
	for _, q := range []string{f.dir, p} {
		if err := os.Chown(q, -1, gid); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(f.dir, 0o775); err != nil {
		t.Fatal(err)
	}
	f.onPath = "/usr/bin/abduco"
	l := f.lookup(os.Getuid())
	l.PrivateGid = gid
	r, err := l.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != SourceBundled {
		t.Fatalf("got %+v, want the abduco next to culibox", r)
	}
	// Writable by others stays refused, private group or not.
	if err := os.Chmod(f.dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if r, _ = l.Resolve(""); r.Source != SourcePath {
		t.Fatalf("world-writable folder: got %+v, want PATH", r)
	}
}

// A symlink to a safe file in a safe folder is used, by its own name (the adapter
// matches attach clients by the basename "abduco").
func TestResolveAcceptsSafeSymlink(t *testing.T) {
	f := newResolveFixture(t)
	target := filepath.Join(realTempDir(t), "abduco-0.6")
	writeExec(t, target)
	link := filepath.Join(f.dir, "abduco")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	f.onPath = "/usr/bin/abduco"
	r, err := f.lookup(os.Getuid()).Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != SourceBundled || filepath.Base(r.Path) != "abduco" {
		t.Fatalf("got %+v, want the link next to culibox", r)
	}
}

// Upgrade: a session started by the PATH abduco (bare "abduco") is listed, attached
// and ended by an adapter that runs a bundled copy by its absolute path.
func TestBundledAdapterAdoptsPathAbducoSession(t *testing.T) {
	old := testAdapter(t)
	id := "upgrade-1"
	if err := old.Create(id, "", []string{"sh", "-c", "sleep 300"}, os.Environ()); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = old.Kill(id) })

	src, err := exec.LookPath("abduco")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	bundled := filepath.Join(realTempDir(t), "abduco")
	if err := os.WriteFile(bundled, b, 0o755); err != nil {
		t.Fatal(err)
	}
	a, err := New(bundled, old.SocketDir, old.PidDir)
	if err != nil {
		t.Fatal(err)
	}
	if ls, _ := a.List(); len(ls) != 1 || ls[0] != id {
		t.Fatalf("List = %v, want [%s]", ls, id)
	}
	if !a.Alive(id) || !a.InnerAlive(id) {
		t.Fatal("the PATH abduco's session is not alive through the bundled adapter")
	}
	cmd, f, err := a.Attach(id, 24, 80)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if cmd.Path != bundled {
		t.Errorf("attach ran %s, want %s", cmd.Path, bundled)
	}
	_ = f.Close()
	if err := a.Kill(id); err != nil {
		t.Fatalf("kill: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && a.Alive(id) {
		time.Sleep(50 * time.Millisecond)
	}
	if a.Alive(id) || a.InnerAlive(id) {
		t.Fatal("session still alive after the bundled adapter killed it")
	}
}

// A `..` after a symlink in a link's target hides that symlink from the one-hop
// check (both spellings clean to the same path), so such a target is refused.
func TestResolveSkipsDotDotThroughSymlink(t *testing.T) {
	f := newResolveFixture(t)
	x := realTempDir(t)
	for _, d := range []string{filepath.Join(x, "t"), filepath.Join(x, "b")} {
		mkdir755(t, d)
	}
	writeExec(t, filepath.Join(x, "b", "abduco"))
	if err := os.Symlink(filepath.Join(x, "t"), filepath.Join(x, "s")); err != nil {
		t.Fatal(err)
	}
	// Not filepath.Join: that would clean the `..` away.
	sep := string(filepath.Separator)
	if err := os.Symlink(x+sep+"s"+sep+".."+sep+"b"+sep+"abduco", filepath.Join(f.dir, "abduco")); err != nil {
		t.Fatal(err)
	}
	f.onPath = "/usr/bin/abduco"
	r, err := f.lookup(os.Getuid()).Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != SourcePath || !strings.Contains(r.Skipped, "..") {
		t.Fatalf("got %+v, want PATH with a `..` skip reason", r)
	}
}

// When the abduco next to culibox is refused and PATH has none, the error says why
// it was refused: serve's fatal line and doctor's FAIL row print this error.
func TestResolveRefusedAndNoPathSaysWhy(t *testing.T) {
	f := newResolveFixture(t)
	f.bundle(t, 0o757)
	r, err := f.lookup(os.Getuid()).Resolve("")
	if err == nil || !strings.Contains(err.Error(), "ignored "+filepath.Join(f.dir, "abduco")) || !strings.Contains(err.Error(), "writable by group or others") {
		t.Fatalf("err = %v, want it to name the ignored file and why", err)
	}
	if r.Skipped == "" {
		t.Fatalf("Skipped is empty: %+v", r)
	}
}
