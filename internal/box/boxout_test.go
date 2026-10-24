package box

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const outTestBox = "ab-box-0123456789ab"

func TestOutputDirPath(t *testing.T) {
	if !isBoxName(outTestBox) {
		t.Fatalf("%q is not a generated box name shape", outTestBox)
	}
	got := OutputDir("/d", outTestBox)
	if want := "/d/boxstate/.boxout/ab-box-0123456789ab/playwright-mcp"; got != want {
		t.Fatalf("OutputDir = %q, want %q", got, want)
	}
	if PlaywrightOutBox != "/tmp/playwright-mcp" {
		t.Fatalf("PlaywrightOutBox = %q", PlaywrightOutBox)
	}
}

func ageFile(t *testing.T, p string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareOutputDirCreatesAndPrunes(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	now := time.Now()
	old, fresh := now.Add(-8*24*time.Hour), now.Add(-time.Hour)

	leaf, err := m.prepareOutputDir(outTestBox, now)
	if err != nil {
		t.Fatal(err)
	}
	if leaf != OutputDir(m.opts.StateRoot, outTestBox) {
		t.Fatalf("returned %q, want the OutputDir path", leaf)
	}
	st, err := os.Stat(leaf)
	if err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
		t.Fatalf("leaf must be a 0700 directory: %v %v", st, err)
	}

	// What a box left behind: an old and a new file, an old directory holding a
	// file, and an old symlink to a host file outside the out dir.
	outside := filepath.Join(t.TempDir(), "host-secret")
	if err := os.WriteFile(outside, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"old.png", "new.png"} {
		if err := os.WriteFile(filepath.Join(leaf, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(leaf, "olddir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leaf, "olddir", "inner.png"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(leaf, "oldlink")); err != nil {
		t.Fatal(err)
	}
	ageFile(t, filepath.Join(leaf, "old.png"), old)
	ageFile(t, filepath.Join(leaf, "new.png"), fresh)
	ageFile(t, filepath.Join(leaf, "olddir"), old)
	// Chtimes follows the link; age the link itself.
	ts := []unix.Timeval{unix.NsecToTimeval(old.UnixNano()), unix.NsecToTimeval(old.UnixNano())}
	if err := unix.Lutimes(filepath.Join(leaf, "oldlink"), ts); err != nil {
		t.Fatal(err)
	}

	if _, err := m.prepareOutputDir(outTestBox, now); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"old.png", "olddir", "oldlink"} {
		if _, err := os.Lstat(filepath.Join(leaf, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s should have been pruned: %v", gone, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(leaf, "new.png")); err != nil {
		t.Fatalf("a recent file must survive: %v", err)
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "keep me" {
		t.Fatalf("the symlink's outside target must be untouched: %q %v", b, err)
	}
}

func TestPrepareOutputDirRefusesSymlinkLeaf(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	target := t.TempDir()
	victim := filepath.Join(target, "old.png")
	if err := os.WriteFile(victim, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ageFile(t, victim, time.Now().Add(-30*24*time.Hour))

	leaf := OutputDir(m.opts.StateRoot, outTestBox)
	if err := os.MkdirAll(filepath.Dir(leaf), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, leaf); err != nil {
		t.Fatal(err)
	}
	if _, err := m.prepareOutputDir(outTestBox, time.Now()); err == nil {
		t.Fatal("a symlink where the out dir belongs must be refused")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("the link's target must not be pruned: %v", err)
	}
}

func TestPrepareOutputDirRefusesFileLeaf(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	leaf := OutputDir(m.opts.StateRoot, outTestBox)
	if err := os.MkdirAll(filepath.Dir(leaf), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(leaf, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.prepareOutputDir(outTestBox, time.Now()); err == nil {
		t.Fatal("a regular file where the out dir belongs must be refused")
	}
}
