package abduco

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// resolveFixture is a culibox "install dir" with an optional abduco next to it,
// and a Lookup whose PATH holds exactly onPath (or nothing when it is "").
type resolveFixture struct {
	dir    string // where culibox lives
	exe    string // dir/culibox
	onPath string // what LookPath("abduco") returns; "" = not found
}

func newResolveFixture(t *testing.T) *resolveFixture {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "culibox")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
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
	other := filepath.Join(t.TempDir(), "my-abduco")
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
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
	_, err := f.lookup(os.Getuid()).Resolve(filepath.Join(t.TempDir(), "nope"))
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
	linkDir := t.TempDir()
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
	if r.Source != SourcePath || !strings.Contains(r.Skipped, "owned by uid") {
		t.Fatalf("got %+v, want PATH with an 'owned by uid' skip reason", r)
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
	if err := os.Mkdir(filepath.Join(f.dir, "abduco"), 0o755); err != nil {
		t.Fatal(err)
	}
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
