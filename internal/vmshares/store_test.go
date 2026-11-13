package vmshares

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/datacompat"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := []Share{{Path: "/a/b", Mode: RW}, {Path: "/c", Mode: RO}}
	if err := Save(dir, in); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", fi, err)
	}
	got, exists, err := Load(dir)
	if err != nil || !exists || len(got) != 2 || got[0] != in[0] || got[1] != in[1] {
		t.Fatalf("%v %v %v", got, exists, err)
	}
	if err := Save(dir, nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	if !strings.Contains(string(b), `"shares":[]`) {
		t.Fatalf("empty list must serialise as []: %s", b)
	}
	got, exists, err = Load(dir)
	if err != nil || !exists || len(got) != 0 {
		t.Fatalf("%v %v %v", got, exists, err)
	}
}

func TestLoadMissing(t *testing.T) {
	got, exists, err := Load(t.TempDir())
	if got != nil || exists || err != nil {
		t.Fatalf("%v %v %v", got, exists, err)
	}
}

func TestLoadCorruptFailsClosed(t *testing.T) {
	for _, body := range []string{
		`{`,
		`{"version":2}`,
		`{"version":1,"shares":[{"path":"rel","mode":"rw"}]}`,
		`{"version":1,"shares":[{"path":"/abs","mode":"rx"}]}`,
	} {
		dir := t.TempDir()
		p := filepath.Join(dir, FileName)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, exists, err := Load(dir)
		if !errors.Is(err, ErrCorrupt) || !exists {
			t.Fatalf("%s: exists=%v err=%v", body, exists, err)
		}
		if !strings.Contains(err.Error(), p) {
			t.Fatalf("error must name the file: %v", err)
		}
		if after, _ := os.ReadFile(p); string(after) != body {
			t.Fatalf("%s: file was rewritten to %s", body, after)
		}
	}
}

func TestSaveRefusesSymlinkedTemp(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "victim")
	if err := os.Symlink(outside, filepath.Join(dir, FileName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, fmt.Sprintf("%s.tmp.%d", FileName, os.Getpid()))); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, []Share{{Path: "/a", Mode: RO}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(outside); err == nil {
		t.Fatal("write followed a planted symlink out of the data dir")
	}
	if got, _, err := Load(dir); err != nil || len(got) != 1 {
		t.Fatalf("file must be a regular file again: %v %v", got, err)
	}
}

// A file from a newer CuliBox stays ErrCorrupt (so the read-only handling is
// unchanged) but is named as newer, not "damaged", even when its body no longer
// fits this build's types.
func TestLoadNewerVersion(t *testing.T) {
	for _, body := range []string{
		`{"version":2,"shares":[]}`,
		`{"version":2,"shares":"x"}`,
	} {
		dir := t.TempDir()
		p := filepath.Join(dir, FileName)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, exists, err := Load(dir)
		if !exists || !errors.Is(err, ErrCorrupt) || !errors.Is(err, datacompat.ErrNewer) {
			t.Fatalf("%s: exists=%v err=%v", body, exists, err)
		}
		if strings.Contains(err.Error(), "damaged") || !strings.Contains(err.Error(), p) {
			t.Fatalf("%s: message must name the file and not say damaged: %v", body, err)
		}
		if after, _ := os.ReadFile(p); string(after) != body {
			t.Fatalf("%s: file was rewritten to %s", body, after)
		}
	}
}

func TestLoadVersionZeroIsNotNewer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(`{"version":0,"shares":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, exists, err := Load(dir)
	if !exists || !errors.Is(err, ErrCorrupt) || errors.Is(err, datacompat.ErrNewer) {
		t.Fatalf("exists=%v err=%v", exists, err)
	}
}
