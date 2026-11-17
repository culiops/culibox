package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func oursContent() []byte { return []byte("# " + Marker + "\n[Unit]\n") }

func TestInspect(t *testing.T) {
	dir := t.TempDir()
	ours := filepath.Join(dir, "ours")
	os.WriteFile(ours, oursContent(), 0o644)
	plain := filepath.Join(dir, "plain")
	os.WriteFile(plain, []byte("[Unit]\nDescription=mine\n"), 0o644)
	late := filepath.Join(dir, "late")
	os.WriteFile(late, []byte(strings.Repeat("x", 512)+Marker), 0o644)
	link := filepath.Join(dir, "link")
	os.Symlink(ours, link)
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	for path, want := range map[string]FileState{
		filepath.Join(dir, "absent"): Absent, ours: Ours, plain: Foreign, late: Foreign, link: Foreign, sub: Foreign,
	} {
		got, err := Inspect(path)
		if err != nil || got != want {
			t.Errorf("Inspect(%s) = %v, %v; want %v", filepath.Base(path), got, err, want)
		}
	}
}

func TestWriteUnitRefusesForeign(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	os.WriteFile(plain, []byte("mine\n"), 0o600)
	target := filepath.Join(dir, "target")
	os.WriteFile(target, oursContent(), 0o644)
	link := filepath.Join(dir, "link")
	os.Symlink(target, link)
	for _, p := range []string{plain, link} {
		if err := WriteUnit(p, []byte("new")); !errors.Is(err, ErrForeign) {
			t.Errorf("WriteUnit(%s) = %v, want ErrForeign", filepath.Base(p), err)
		}
	}
	if b, _ := os.ReadFile(plain); string(b) != "mine\n" {
		t.Errorf("plain changed: %q", b)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link replaced: %v %v", fi, err)
	}
	if b, _ := os.ReadFile(target); string(b) != string(oursContent()) {
		t.Errorf("symlink target changed: %q", b)
	}
}

func TestWriteUnitReplacesOurs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "systemd", "user")
	p := filepath.Join(dir, SystemdUnit)
	if err := WriteUnit(p, oursContent()); err != nil {
		t.Fatal(err)
	}
	next := append(oursContent(), "changed\n"...)
	if err := WriteUnit(p, next); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	fi, _ := os.Stat(p)
	if string(b) != string(next) || fi.Mode().Perm() != 0o644 {
		t.Fatalf("content %q mode %v", b, fi.Mode())
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("leftovers in dir: %v", ents)
	}
}

func TestRemoveUnit(t *testing.T) {
	dir := t.TempDir()
	if removed, err := RemoveUnit(filepath.Join(dir, "absent")); removed || err != nil {
		t.Errorf("absent: %v %v", removed, err)
	}
	target := filepath.Join(dir, "target")
	os.WriteFile(target, oursContent(), 0o644)
	link := filepath.Join(dir, "link")
	os.Symlink(target, link)
	if _, err := RemoveUnit(link); !errors.Is(err, ErrForeign) {
		t.Errorf("link: %v, want ErrForeign", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("link removed")
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("target removed")
	}
	if removed, err := RemoveUnit(target); !removed || err != nil {
		t.Errorf("ours: %v %v", removed, err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ours still there")
	}
}
