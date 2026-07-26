package web

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSafeName(t *testing.T) {
	ok := map[string]string{
		"report.md":            "report.md",
		".hidden":              ".hidden",
		"a/b/c.txt":            "c.txt",    // browsers may send a path
		`C:\Users\me\shot.png`: "shot.png", // Windows-style path from the browser
		"  spaced.txt  ":       "spaced.txt",
	}
	for in, want := range ok {
		got, err := safeName(in)
		if err != nil || got != want {
			t.Errorf("safeName(%q) = %q, %v; want %q, nil", in, got, err, want)
		}
	}
	for _, bad := range []string{"", ".", "..", "/", "../..", "\x01evil", "nul\x00byte"} {
		if got, err := safeName(bad); err == nil {
			t.Errorf("safeName(%q) = %q, nil; want error", bad, got)
		}
	}
}

func TestCreateExclusiveNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shot.png"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, name, err := createExclusive(dir, "shot.png")
	if err != nil {
		t.Fatalf("createExclusive: %v", err)
	}
	f.Close()
	if name != "shot-1.png" {
		t.Fatalf("name = %q, want shot-1.png", name)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "shot.png"))
	if string(got) != "original" {
		t.Fatalf("original file was modified: %q", got)
	}

	f2, name2, err := createExclusive(dir, "shot.png")
	if err != nil {
		t.Fatalf("createExclusive #2: %v", err)
	}
	f2.Close()
	if name2 != "shot-2.png" {
		t.Fatalf("name2 = %q, want shot-2.png", name2)
	}
}

// The escape test: a symlink sitting at the target name must never be written
// through. O_EXCL fails on an existing symlink, so the upload sidesteps it.
func TestCreateExclusiveDoesNotFollowSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(outside, []byte("do not touch"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "bait.txt")); err != nil {
		t.Fatal(err)
	}
	f, name, err := createExclusive(dir, "bait.txt")
	if err != nil {
		t.Fatalf("createExclusive: %v", err)
	}
	if _, err := f.WriteString("payload"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if name != "bait-1.txt" {
		t.Fatalf("name = %q, want bait-1.txt", name)
	}
	got, _ := os.ReadFile(outside)
	if string(got) != "do not touch" {
		t.Fatalf("wrote through the symlink: %q", got)
	}
}

func TestCreateExclusiveGivesUpAfterManyCollisions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= maxNameAttempts; i++ {
		p := filepath.Join(dir, "x-"+strconv.Itoa(i)+".txt")
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := createExclusive(dir, "x.txt"); !errors.Is(err, errTooManyCollisions) {
		t.Fatalf("err = %v, want errTooManyCollisions", err)
	}
}

func TestContainedNewFile(t *testing.T) {
	home := t.TempDir()
	sub := filepath.Join(home, "work")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(home, "file.txt")
	if err := os.WriteFile(regular, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	gotDir, gotName, err := containedNewFile(home, sub, "new.png")
	if err != nil {
		t.Fatalf("containedNewFile: %v", err)
	}
	if gotName != "new.png" || !strings.HasPrefix(gotDir, home) {
		t.Fatalf("got %q, %q", gotDir, gotName)
	}

	if _, _, err := containedNewFile(home, filepath.Dir(home), "x.txt"); !errors.Is(err, errOutsideHome) {
		t.Errorf("outside home: err = %v, want errOutsideHome", err)
	}
	if _, _, err := containedNewFile(home, filepath.Join(home, "nope"), "x.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing dir: err = %v, want os.ErrNotExist", err)
	}
	if _, _, err := containedNewFile(home, regular, "x.txt"); err == nil {
		t.Error("dir that is a regular file: want error")
	}
	if _, _, err := containedNewFile(home, sub, ".."); !errors.Is(err, errBadName) {
		t.Errorf("bad name: err = %v, want errBadName", err)
	}
}
