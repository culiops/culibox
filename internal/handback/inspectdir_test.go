package handback

import (
	"context"
	"encoding/binary"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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

func TestInspectDirGitObjects(t *testing.T) {
	root := t.TempDir()
	loose := strings.Repeat("c", 38)
	loose256 := strings.Repeat("d", 62)
	put(t, root, ".git/objects/aa/"+loose, elfExec(), 0o644)
	put(t, root, "sub/.git/objects/bb/"+loose256, elfExec(), 0o644)
	put(t, root, ".git/objects/pack/pack-abc.pack", elfExec(), 0o644)
	put(t, root, ".git/objects/pack/pack-abc.idx", elfExec(), 0o644)
	put(t, root, ".git/objects/pack/x.pdf", elfExec(), 0o644)
	put(t, root, ".git/objects/aa/notahash.pdf", elfExec(), 0o644)
	put(t, root, ".git/objects/info/alternates", elfExec(), 0o644)
	put(t, root, ".git/hooks/x", elfExec(), 0o755)
	rep := scan(t, root, time.Now().Add(-time.Minute), DefaultLimits())
	for _, p := range []string{".git/objects/aa/" + loose, "sub/.git/objects/bb/" + loose256,
		".git/objects/pack/pack-abc.pack", ".git/objects/pack/pack-abc.idx"} {
		if f, ok := flagFor(rep, p); ok {
			t.Errorf("%s is an object-store file and must be skipped, got %+v", p, f)
		}
	}
	if f, ok := flagFor(rep, ".git/objects/pack/x.pdf"); !ok || !hasClass(f, ClassMismatch) {
		t.Errorf(".git/objects/pack/x.pdf = %+v ok=%v, want mismatch", f, ok)
	}
	for _, p := range []string{".git/objects/aa/notahash.pdf", ".git/objects/info/alternates", ".git/hooks/x"} {
		if _, ok := flagFor(rep, p); !ok {
			t.Errorf("%s must be inspected", p)
		}
	}
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root can read chmod 000 entries")
	}
}

func TestInspectDirUnreadableEntries(t *testing.T) {
	skipIfRoot(t)
	since := time.Now().Add(-time.Minute)

	t.Run("chmod 000 file", func(t *testing.T) {
		root := t.TempDir()
		put(t, root, "hidden.pdf", elfExec(), 0o000)
		rep := scan(t, root, since, DefaultLimits())
		if !strings.Contains(rep.Truncated, "could not be read") {
			t.Errorf("Truncated = %q, want an unreadable-entries reason", rep.Truncated)
		}
	})

	t.Run("chmod 000 directory", func(t *testing.T) {
		root := t.TempDir()
		put(t, root, "d/x.pdf", elfExec(), 0o644)
		if err := os.Chmod(filepath.Join(root, "d"), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(filepath.Join(root, "d"), 0o755) })
		rep := scan(t, root, since, DefaultLimits())
		if !strings.Contains(rep.Truncated, "could not be read") {
			t.Errorf("Truncated = %q, want an unreadable-entries reason", rep.Truncated)
		}
	})

	t.Run("unreadable setuid file is still flagged from its mode", func(t *testing.T) {
		root := t.TempDir()
		put(t, root, "su", elfExec(), 0o644)
		if err := os.Chmod(filepath.Join(root, "su"), fs.ModeSetuid); err != nil {
			t.Skipf("chmod: %v", err)
		}
		if fi, err := os.Lstat(filepath.Join(root, "su")); err != nil || fi.Mode()&fs.ModeSetuid == 0 {
			t.Skip("filesystem refused the setuid bit")
		}
		rep := scan(t, root, since, DefaultLimits())
		if f, ok := flagFor(rep, "su"); !ok || !hasClass(f, ClassSetID) {
			t.Errorf("su = %+v ok=%v, want setuid-setgid from the mode", f, ok)
		}
		if !strings.Contains(rep.Truncated, "could not be read") {
			t.Errorf("Truncated = %q, want an unreadable-entries reason", rep.Truncated)
		}
	})

	t.Run("empty file is not opened", func(t *testing.T) {
		root := t.TempDir()
		put(t, root, "empty", nil, 0o000)
		rep := scan(t, root, since, DefaultLimits())
		if rep.Truncated != "" {
			t.Errorf("Truncated = %q, want none: an empty file is never opened", rep.Truncated)
		}
	})
}

func TestReadHeadRefusesSwappedSymlink(t *testing.T) {
	root := t.TempDir()
	put(t, root, "orig", []byte("original"), 0o644)
	put(t, root, "other", elfExec(), 0o644)
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	lst, err := r.Lstat("orig")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "orig")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("other", filepath.Join(root, "orig")); err != nil {
		t.Fatal(err)
	}
	if f, _, _, ok := readHead(r, "orig", lst); ok {
		f.Close()
		t.Fatal("a file swapped for a symlink after the Lstat must be refused")
	}
	// Sanity: the untouched file is accepted.
	lst2, _ := r.Lstat("other")
	f, head, _, ok := readHead(r, "other", lst2)
	if !ok {
		t.Fatal("readHead refused a plain regular file")
	}
	f.Close()
	if len(head) == 0 {
		t.Error("empty head")
	}
}

// cancelAfter is a context that reports Canceled once Err has been asked n
// times, so a test can cancel deterministically in the middle of a walk.
type cancelAfter struct {
	context.Context
	n int
}

func (c *cancelAfter) Err() error {
	c.n--
	if c.n < 0 {
		return context.Canceled
	}
	return nil
}

func TestInspectDirCancelMidWalk(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 3000; i++ {
		put(t, root, "d/f"+strconv.Itoa(i), []byte("x"), 0o644)
	}
	ctx := &cancelAfter{Context: context.Background(), n: 500}
	start := time.Now()
	rep, err := InspectDir(ctx, root, time.Now().Add(-time.Minute), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("did not return promptly")
	}
	if !strings.Contains(rep.Truncated, "cancelled") {
		t.Errorf("Truncated = %q, want it to say cancelled", rep.Truncated)
	}
	if rep.Inspected == 0 || rep.Inspected >= 3000 {
		t.Errorf("Inspected = %d, want a mid-walk stop (0 < n < 3000)", rep.Inspected)
	}
}

func TestInspectDirFlagCapKeepsStrongest(t *testing.T) {
	root := t.TempDir()
	for i := 1; i <= 5; i++ {
		put(t, root, "a"+strconv.Itoa(i), []byte("#!/bin/sh\n"), 0o755)
	}
	put(t, root, "zz.pdf", elfExec(), 0o644)
	lim := DefaultLimits()
	lim.MaxFlags = 3
	rep := scan(t, root, time.Now().Add(-time.Minute), lim)
	if len(rep.Flags) != 3 {
		t.Fatalf("flags = %d, want 3", len(rep.Flags))
	}
	if _, ok := flagFor(rep, "zz.pdf"); !ok {
		t.Errorf("the mismatch must survive the flag cap: %+v", rep.Flags)
	}
	if rep.Flags[0].Path != "zz.pdf" {
		t.Errorf("mismatch should sort first, got %+v", rep.Flags[0])
	}
	if !strings.Contains(rep.Truncated, "3 more") {
		t.Errorf("Truncated = %q, want 3 more", rep.Truncated)
	}
}

func TestInspectDirEmptyFilesDoNotUseCandidates(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		put(t, root, "e"+strconv.Itoa(i), nil, 0o644)
	}
	put(t, root, "x1.pdf", elfExec(), 0o644)
	put(t, root, "x2.pdf", elfExec(), 0o644)
	lim := DefaultLimits()
	lim.Candidates = 2
	rep := scan(t, root, time.Now().Add(-time.Minute), lim)
	if rep.Truncated != "" || len(rep.Flags) != 2 {
		t.Errorf("flags=%+v truncated=%q, want both ELF files and no truncation", rep.Flags, rep.Truncated)
	}
}

func TestInspectDirCandidateCapKeepsCheapSignals(t *testing.T) {
	root := t.TempDir()
	for i := 1; i <= 3; i++ {
		put(t, root, "a"+strconv.Itoa(i), []byte("content"), 0o644)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "zz-link")); err != nil {
		t.Fatal(err)
	}
	lim := DefaultLimits()
	lim.Candidates = 2
	rep := scan(t, root, time.Now().Add(-time.Minute), lim)
	if f, ok := flagFor(rep, "zz-link"); !ok || !hasClass(f, ClassSymlinkOut) {
		t.Errorf("zz-link = %+v ok=%v, want it flagged after the candidate cap", f, ok)
	}
	if !strings.Contains(rep.Truncated, "candidate") {
		t.Errorf("Truncated = %q, want the candidate cap", rep.Truncated)
	}
}

func TestInspectDirCandidateCapKeepsSetID(t *testing.T) {
	root := t.TempDir()
	for i := 1; i <= 3; i++ {
		put(t, root, "a"+strconv.Itoa(i), []byte("content"), 0o644)
	}
	put(t, root, "su", []byte("content"), 0o644)
	if err := os.Chmod(filepath.Join(root, "su"), 0o755|fs.ModeSetuid); err != nil {
		t.Skip("chmod refused")
	}
	if fi, _ := os.Lstat(filepath.Join(root, "su")); fi.Mode()&fs.ModeSetuid == 0 {
		t.Skip("filesystem refused the setuid bit")
	}
	lim := DefaultLimits()
	lim.Candidates = 1
	rep := scan(t, root, time.Now().Add(-time.Minute), lim)
	if f, ok := flagFor(rep, "su"); !ok || !hasClass(f, ClassSetID) {
		t.Errorf("su = %+v ok=%v, want setuid flagged from the mode past the cap", f, ok)
	}
}

func TestHashCappedCtx(t *testing.T) {
	ctx := &cancelAfter{Context: context.Background(), n: 1}
	// 1 MiB is 32 chunks of io.Copy's 32 KiB buffer; the ctx dies on the second read.
	budget := int64(1 << 30)
	sum, note := hashCappedCtx(ctx, strings.NewReader(strings.Repeat("a", 1<<20)), 1<<30, &budget)
	if sum != "" || note != "not hashed: scan cancelled" {
		t.Errorf("sum=%q note=%q, want a cancelled note", sum, note)
	}
}

func TestGitObjectStoreFile(t *testing.T) {
	h38, h62 := strings.Repeat("0", 38), strings.Repeat("f", 62)
	for p, want := range map[string]bool{
		".git/objects/aa/" + h38:            true,
		"x/y/.git/objects/ab/" + h62:        true,
		".git/objects/aa/" + h38 + ".pdf":   false,
		".git/objects/aa/" + h38[1:]:        false,
		".git/objects/AA/" + h38:            false,
		".git/objects/zz/" + h38:            false,
		".git/objects/pack/pack-1.pack":     true,
		".git/objects/pack/pack-1.idx":      true,
		".git/objects/pack/pack-1.rev":      true,
		".git/objects/pack/pack-1.bitmap":   true,
		".git/objects/pack/pack-1.mtimes":   true,
		".git/objects/pack/pack-1.promisor": true,
		".git/objects/pack/pack-1.keep":     true,
		".git/objects/pack/pack-1.pdf":      false,
		".git/objects/pack/x.pack":          false,
		".git/objects/info/alternates":      false,
		".git/hooks/pre-commit":             false,
		"objects/aa/" + h38:                 false,
		".git/objects/aa/sub/" + h38:        false,
	} {
		if got := gitObjectStoreFile(p); got != want {
			t.Errorf("gitObjectStoreFile(%q) = %v, want %v", p, got, want)
		}
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

// R21: Incomplete is set only when the walk stopped before it had seen
// everything (budget, entry cap, cancel, candidate cap). A reason that recurs
// on every scan (an unreadable directory, the depth cap, the flag cap) must not
// pin the next scan's window.
func TestInspectDirIncomplete(t *testing.T) {
	since := time.Now().Add(-time.Minute)

	t.Run("an unreadable directory is not incomplete", func(t *testing.T) {
		skipIfRoot(t)
		root := t.TempDir()
		put(t, root, "pgdata/x", []byte("x"), 0o644)
		if err := os.Chmod(filepath.Join(root, "pgdata"), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(filepath.Join(root, "pgdata"), 0o755) })
		put(t, root, "run.sh", []byte("#!/bin/sh\n"), 0o755)
		rep := scan(t, root, since, DefaultLimits())
		if rep.Truncated == "" || rep.Incomplete {
			t.Errorf("Truncated=%q Incomplete=%v, want a note and Incomplete false", rep.Truncated, rep.Incomplete)
		}
	})

	t.Run("the depth cap is not incomplete", func(t *testing.T) {
		root := t.TempDir()
		put(t, root, "a/b/c/d/x", []byte("x"), 0o644)
		lim := DefaultLimits()
		lim.Depth = 2
		rep := scan(t, root, since, lim)
		if rep.Truncated == "" || rep.Incomplete {
			t.Errorf("Truncated=%q Incomplete=%v, want a note and Incomplete false", rep.Truncated, rep.Incomplete)
		}
	})

	t.Run("the flag cap is not incomplete", func(t *testing.T) {
		root := t.TempDir()
		for i := 1; i <= 4; i++ {
			put(t, root, "a"+strconv.Itoa(i), []byte("#!/bin/sh\n"), 0o755)
		}
		lim := DefaultLimits()
		lim.MaxFlags = 2
		rep := scan(t, root, since, lim)
		if rep.Truncated == "" || rep.Incomplete {
			t.Errorf("Truncated=%q Incomplete=%v, want a note and Incomplete false", rep.Truncated, rep.Incomplete)
		}
	})

	t.Run("the time budget is incomplete", func(t *testing.T) {
		root := t.TempDir()
		put(t, root, "a", []byte("x"), 0o644)
		lim := DefaultLimits()
		lim.Budget = time.Nanosecond
		time.Sleep(time.Millisecond)
		rep := scan(t, root, since, lim)
		if !rep.Incomplete {
			t.Errorf("Truncated=%q Incomplete=%v, want Incomplete", rep.Truncated, rep.Incomplete)
		}
	})

	t.Run("the entry cap is incomplete", func(t *testing.T) {
		root := t.TempDir()
		for i := 0; i < 5; i++ {
			put(t, root, "f"+strconv.Itoa(i), []byte("x"), 0o644)
		}
		lim := DefaultLimits()
		lim.Entries = 2
		if rep := scan(t, root, since, lim); !rep.Incomplete {
			t.Errorf("Incomplete = false, Truncated=%q", rep.Truncated)
		}
	})

	t.Run("a cancelled walk is incomplete", func(t *testing.T) {
		root := t.TempDir()
		for i := 0; i < 3000; i++ {
			put(t, root, "d/f"+strconv.Itoa(i), []byte("x"), 0o644)
		}
		ctx := &cancelAfter{Context: context.Background(), n: 500}
		rep, err := InspectDir(ctx, root, since, DefaultLimits())
		if err != nil || !rep.Incomplete {
			t.Errorf("Incomplete=%v err=%v", rep.Incomplete, err)
		}
	})

	t.Run("the candidate cap is incomplete", func(t *testing.T) {
		root := t.TempDir()
		for i := 1; i <= 4; i++ {
			put(t, root, "a"+strconv.Itoa(i), []byte("content"), 0o644)
		}
		lim := DefaultLimits()
		lim.Candidates = 2
		if rep := scan(t, root, since, lim); !rep.Incomplete {
			t.Errorf("Incomplete = false, Truncated=%q", rep.Truncated)
		}
	})

	t.Run("a finished walk is complete", func(t *testing.T) {
		root := t.TempDir()
		put(t, root, "a.pdf", elfExec(), 0o644)
		if rep := scan(t, root, since, DefaultLimits()); rep.Incomplete || rep.Truncated != "" {
			t.Errorf("Incomplete=%v Truncated=%q", rep.Incomplete, rep.Truncated)
		}
	})
}
