package handback

import (
	"context"
	"encoding/binary"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// elf returns a 64-bit little-endian ELF executable head (classify_test.go's
// elfHead builds it; ET_EXEC is type 2).
func elfExec() []byte { return elfHead(binary.LittleEndian, 2) }

func put(t *testing.T, root, rel string, data []byte, mode fs.FileMode) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func flagFor(rep Report, path string) (Flag, bool) {
	for _, f := range rep.Flags {
		if f.Path == path {
			return f, true
		}
	}
	return Flag{}, false
}

func hasClass(f Flag, c Class) bool {
	for _, x := range f.Classes {
		if x == c {
			return true
		}
	}
	return false
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func scan(t *testing.T, root string, since time.Time, lim Limits) Report {
	t.Helper()
	rep, err := InspectDir(context.Background(), root, since, lim)
	if err != nil {
		t.Fatalf("InspectDir: %v", err)
	}
	return rep
}

func TestInspectDirFlagsNewFiles(t *testing.T) {
	root := t.TempDir()
	put(t, root, "report.pdf", elfExec(), 0o644)
	put(t, root, "run.sh", []byte("#!/bin/sh\necho hi\n"), 0o755)
	put(t, root, "plain.txt", []byte("hello\n"), 0o644)
	suid := false
	put(t, root, "suid", []byte("data\n"), 0o644)
	if err := os.Chmod(filepath.Join(root, "suid"), 0o755|fs.ModeSetuid); err == nil {
		if fi, err := os.Lstat(filepath.Join(root, "suid")); err == nil && fi.Mode()&fs.ModeSetuid != 0 {
			suid = true
		}
	}
	put(t, root, "sub/x", []byte("x\n"), 0o644)
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub/x", filepath.Join(root, "in")); err != nil {
		t.Fatal(err)
	}

	rep := scan(t, root, time.Now().Add(-time.Minute), DefaultLimits())
	if rep.Truncated != "" {
		t.Fatalf("Truncated = %q, want empty", rep.Truncated)
	}

	f, ok := flagFor(rep, "report.pdf")
	if !ok || !hasClass(f, ClassMismatch) || !hasClass(f, ClassExecutable) {
		t.Fatalf("report.pdf flag = %+v ok=%v, want mismatch+executable", f, ok)
	}
	if !sha256Hex.MatchString(f.SHA256) {
		t.Errorf("report.pdf SHA256 = %q, want 64 hex", f.SHA256)
	}
	if f, ok := flagFor(rep, "run.sh"); !ok || !hasClass(f, ClassScript) {
		t.Errorf("run.sh flag = %+v ok=%v, want exec-script", f, ok)
	}
	if suid {
		if f, ok := flagFor(rep, "suid"); !ok || !hasClass(f, ClassSetID) {
			t.Errorf("suid flag = %+v ok=%v, want setuid-setgid", f, ok)
		}
	} else {
		t.Log("filesystem refused the setuid bit; skipping that row")
	}
	f, ok = flagFor(rep, "out")
	if !ok || !hasClass(f, ClassSymlinkOut) || f.Target != "/etc/passwd" || f.SHA256 != "" {
		t.Errorf("out flag = %+v ok=%v, want symlink-outside to /etc/passwd with no SHA256", f, ok)
	}
	for _, name := range []string{"in", "plain.txt", "sub/x"} {
		if f, ok := flagFor(rep, name); ok {
			t.Errorf("%s must not be flagged, got %+v", name, f)
		}
	}
	if rep.Inspected == 0 {
		t.Error("Inspected = 0, want files counted")
	}
	// Mismatch sorts first.
	if len(rep.Flags) == 0 || rep.Flags[0].Path != "report.pdf" {
		t.Errorf("flags not sorted, first = %+v", rep.Flags)
	}
}

func TestInspectDirIgnoresOldFiles(t *testing.T) {
	root := t.TempDir()
	put(t, root, "report.pdf", elfExec(), 0o644)
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "out")); err != nil {
		t.Fatal(err)
	}
	rep := scan(t, root, time.Now().Add(time.Minute), DefaultLimits())
	if len(rep.Flags) != 0 {
		t.Fatalf("flags = %+v, want none for files older than the window", rep.Flags)
	}
}

func TestInspectDirTouchBackStillCandidate(t *testing.T) {
	root := t.TempDir()
	put(t, root, "report.pdf", elfExec(), 0o644)
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(root, "report.pdf"), old, old); err != nil {
		t.Fatal(err)
	}
	rep := scan(t, root, time.Now().Add(-time.Minute), DefaultLimits())
	if _, ok := flagFor(rep, "report.pdf"); !ok {
		t.Fatalf("a file touched back to 2000 must still be flagged (ctime moved), got %+v", rep.Flags)
	}
}

func TestInspectDirSkipsGitObjects(t *testing.T) {
	root := t.TempDir()
	put(t, root, ".git/objects/aa/bb", elfExec(), 0o644)
	put(t, root, ".git/hooks/x", elfExec(), 0o755)
	rep := scan(t, root, time.Now().Add(-time.Minute), DefaultLimits())
	if f, ok := flagFor(rep, ".git/objects/aa/bb"); ok {
		t.Errorf(".git/objects must be skipped, got %+v", f)
	}
	if _, ok := flagFor(rep, ".git/hooks/x"); !ok {
		t.Errorf(".git/hooks/x must be flagged, got %+v", rep.Flags)
	}
}

func TestInspectDirBounds(t *testing.T) {
	since := time.Now().Add(-time.Minute)

	t.Run("fifo does not block and is not read", func(t *testing.T) {
		root := t.TempDir()
		if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o644); err != nil {
			t.Skipf("mkfifo: %v", err)
		}
		put(t, root, "report.pdf", elfExec(), 0o644)
		done := make(chan Report, 1)
		go func() {
			rep, _ := InspectDir(context.Background(), root, since, DefaultLimits())
			done <- rep
		}()
		select {
		case rep := <-done:
			if _, ok := flagFor(rep, "report.pdf"); !ok {
				t.Errorf("report.pdf not flagged next to a FIFO: %+v", rep.Flags)
			}
			if _, ok := flagFor(rep, "pipe"); ok {
				t.Error("FIFO must not be flagged")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("walk blocked on a FIFO")
		}
	})

	t.Run("symlink loop is not followed", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink("b", filepath.Join(root, "a")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("a", filepath.Join(root, "b")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(".", filepath.Join(root, "self")); err != nil {
			t.Fatal(err)
		}
		rep := scan(t, root, since, DefaultLimits())
		if len(rep.Flags) != 0 || rep.Truncated != "" {
			t.Errorf("loop: flags=%+v truncated=%q, want neither", rep.Flags, rep.Truncated)
		}
	})

	t.Run("symlinked directory is not entered", func(t *testing.T) {
		outside := t.TempDir()
		put(t, outside, "evil.pdf", elfExec(), 0o644)
		root := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
		rep := scan(t, root, since, DefaultLimits())
		if _, ok := flagFor(rep, "link/evil.pdf"); ok {
			t.Error("walker followed a directory symlink")
		}
		if f, ok := flagFor(rep, "link"); !ok || !hasClass(f, ClassSymlinkOut) {
			t.Errorf("link flag = %+v ok=%v, want symlink-outside", f, ok)
		}
	})

	t.Run("deep chain stops at the depth cap", func(t *testing.T) {
		root := t.TempDir()
		p := root
		for i := 0; i < 100; i++ {
			p = filepath.Join(p, "d")
		}
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		put(t, p, "deep.pdf", elfExec(), 0o644)
		lim := DefaultLimits()
		lim.Depth = 64
		rep := scan(t, root, since, lim)
		if !strings.Contains(rep.Truncated, "depth") {
			t.Errorf("Truncated = %q, want it to mention depth", rep.Truncated)
		}
		if len(rep.Flags) != 0 {
			t.Errorf("file below the depth cap was flagged: %+v", rep.Flags)
		}
	})

	t.Run("entry cap", func(t *testing.T) {
		root := t.TempDir()
		for i := 0; i < 50; i++ {
			put(t, root, "f"+string(rune('a'+i%26))+string(rune('a'+i/26)), []byte("x"), 0o644)
		}
		lim := DefaultLimits()
		lim.Entries = 10
		rep := scan(t, root, since, lim)
		if !strings.Contains(rep.Truncated, "entries") {
			t.Errorf("Truncated = %q, want it to mention entries", rep.Truncated)
		}
	})

	t.Run("candidate cap", func(t *testing.T) {
		root := t.TempDir()
		for i := 0; i < 5; i++ {
			put(t, root, "f"+string(rune('a'+i)), []byte("x"), 0o644)
		}
		lim := DefaultLimits()
		lim.Candidates = 2
		rep := scan(t, root, since, lim)
		if !strings.Contains(rep.Truncated, "candidate") {
			t.Errorf("Truncated = %q, want it to mention candidates", rep.Truncated)
		}
		if rep.Inspected > 2 {
			t.Errorf("Inspected = %d, want at most 2", rep.Inspected)
		}
	})

	t.Run("flag cap keeps one and counts the rest", func(t *testing.T) {
		root := t.TempDir()
		for i := 0; i < 4; i++ {
			put(t, root, "e"+string(rune('a'+i))+".pdf", elfExec(), 0o644)
		}
		lim := DefaultLimits()
		lim.MaxFlags = 1
		rep := scan(t, root, since, lim)
		if len(rep.Flags) != 1 {
			t.Fatalf("flags = %d, want 1", len(rep.Flags))
		}
		if !strings.Contains(rep.Truncated, "3 more") {
			t.Errorf("Truncated = %q, want it to say 3 more", rep.Truncated)
		}
	})

	t.Run("time budget", func(t *testing.T) {
		root := t.TempDir()
		put(t, root, "a", []byte("x"), 0o644)
		lim := DefaultLimits()
		lim.Budget = time.Nanosecond
		rep := scan(t, root, since, lim)
		if !strings.Contains(rep.Truncated, "time") {
			t.Errorf("Truncated = %q, want it to mention the time budget", rep.Truncated)
		}
	})

	t.Run("cancelled context returns promptly", func(t *testing.T) {
		root := t.TempDir()
		for i := 0; i < 20; i++ {
			put(t, root, "f"+string(rune('a'+i)), []byte("x"), 0o644)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		start := time.Now()
		rep, err := InspectDir(ctx, root, since, DefaultLimits())
		if err != nil {
			t.Fatalf("err = %v, want nil (error is only for an unopenable root)", err)
		}
		if time.Since(start) > 5*time.Second {
			t.Error("cancelled scan did not return promptly")
		}
		if rep.Truncated == "" {
			t.Error("cancelled scan must set Truncated")
		}
	})
}

func TestInspectDirRootMissing(t *testing.T) {
	if _, err := InspectDir(context.Background(), filepath.Join(t.TempDir(), "nope"), time.Now(), DefaultLimits()); err == nil {
		t.Fatal("want an error when root cannot be opened")
	}
}

func TestInspectDirHashCaps(t *testing.T) {
	root := t.TempDir()
	big := append(elfExec(), make([]byte, 1024-64)...)
	put(t, root, "big.pdf", big, 0o644)
	lim := DefaultLimits()
	lim.HashPerFile = 16
	rep := scan(t, root, time.Now().Add(-time.Minute), lim)
	f, ok := flagFor(rep, "big.pdf")
	if !ok {
		t.Fatalf("big.pdf not flagged: %+v", rep.Flags)
	}
	if f.SHA256 != "" || f.HashNote != "not hashed: larger than 64 MiB" {
		t.Errorf("SHA256=%q HashNote=%q, want no hash and the per-file note", f.SHA256, f.HashNote)
	}
}

func TestInspectDirHashBudget(t *testing.T) {
	root := t.TempDir()
	put(t, root, "a.pdf", elfExec(), 0o644)
	put(t, root, "b.pdf", elfExec(), 0o644)
	lim := DefaultLimits()
	lim.HashTotal = 100 // room for one 64-byte file, not two
	rep := scan(t, root, time.Now().Add(-time.Minute), lim)
	hashed, noted := 0, 0
	for _, f := range rep.Flags {
		if f.SHA256 != "" {
			hashed++
		}
		if f.HashNote == "not hashed: hash budget for this scan used up" {
			noted++
		}
	}
	if hashed != 1 || noted != 1 {
		t.Errorf("hashed=%d noted=%d, want 1 and 1: %+v", hashed, noted, rep.Flags)
	}
}

func TestInspectDirLongPath(t *testing.T) {
	root := t.TempDir()
	name := strings.Repeat("a", 250-4) + ".pdf"
	if len(name) != 250 {
		t.Fatalf("name is %d bytes", len(name))
	}
	put(t, root, name, elfExec(), 0o644)
	rep := scan(t, root, time.Now().Add(-time.Minute), DefaultLimits())
	if len(rep.Flags) != 1 {
		t.Fatalf("flags = %+v, want one", rep.Flags)
	}
	f := rep.Flags[0]
	if !strings.HasSuffix(f.Path, ".pdf") || !hasClass(f, ClassMismatch) {
		t.Errorf("flag = %+v, want a mismatch whose path ends .pdf", f)
	}
}

func TestInspectDirSanitizesPaths(t *testing.T) {
	root := t.TempDir()
	put(t, root, "a‮gpj.pdf", elfExec(), 0o644)
	rep := scan(t, root, time.Now().Add(-time.Minute), DefaultLimits())
	if len(rep.Flags) != 1 || strings.ContainsRune(rep.Flags[0].Path, '‮') {
		t.Fatalf("flags = %+v, want one with the bidi control replaced", rep.Flags)
	}
}

func TestHashCapped(t *testing.T) {
	budget := int64(100)
	sum, note := hashCapped(strings.NewReader("hello"), 10, &budget)
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if sum != want || note != "" || budget != 95 {
		t.Errorf("sum=%q note=%q budget=%d", sum, note, budget)
	}
	sum, note = hashCapped(strings.NewReader("hello world!"), 10, &budget)
	if sum != "" || note != "not hashed: larger than 64 MiB" {
		t.Errorf("over per-file cap: sum=%q note=%q", sum, note)
	}
	budget = 3
	sum, note = hashCapped(strings.NewReader("hello"), 10, &budget)
	if sum != "" || note != "not hashed: hash budget for this scan used up" || budget != 0 {
		t.Errorf("over budget: sum=%q note=%q budget=%d", sum, note, budget)
	}
	sum, note = hashCapped(strings.NewReader("hello"), 10, &budget)
	if sum != "" || note != "not hashed: hash budget for this scan used up" {
		t.Errorf("empty budget: sum=%q note=%q", sum, note)
	}
}
