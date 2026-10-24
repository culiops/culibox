package boxread

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRegular(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "sub", "f"), "hello")
	f, fi, ok := Open(dir, "sub/f")
	if !ok {
		t.Fatal("Open refused a regular file")
	}
	defer f.Close()
	if fi.Size() != 5 {
		t.Fatalf("size = %d, want 5", fi.Size())
	}
	buf := make([]byte, 5)
	if _, err := f.Read(buf); err != nil || string(buf) != "hello" {
		t.Fatalf("read after Open: %q, %v", buf, err)
	}
}

func TestOpenRefusesFinalSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	write(t, filepath.Join(outside, "secret"), "x")
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "l")); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := Open(dir, "l"); ok {
		t.Fatal("Open followed a final symlink")
	}
	// A symlink that stays inside the root is refused too (O_NOFOLLOW).
	write(t, filepath.Join(dir, "real"), "x")
	if err := os.Symlink("real", filepath.Join(dir, "inner")); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := Open(dir, "inner"); ok {
		t.Fatal("Open followed an in-root final symlink")
	}
}

func TestOpenRefusesEscapingDirSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	write(t, filepath.Join(outside, "f"), "x")
	if err := os.Symlink(outside, filepath.Join(dir, "a")); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := Open(dir, "a/f"); ok {
		t.Fatal("Open followed a directory symlink out of the root")
	}
}

func TestOpenRefusesDotDot(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "box")
	write(t, filepath.Join(dir, "ok"), "x")
	write(t, filepath.Join(parent, "x"), "x")
	if _, _, ok := Open(dir, "../x"); ok {
		t.Fatal("Open accepted ..")
	}
}

func TestOpenRefusesEmptyAndAbsolute(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "f"), "x")
	if _, _, ok := Open(dir, ""); ok {
		t.Fatal("Open accepted empty rel")
	}
	if _, _, ok := Open(dir, filepath.Join(dir, "f")); ok {
		t.Fatal("Open accepted absolute rel")
	}
}

func TestOpenRefusesFIFO(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "p"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() {
		f, _, ok := Open(dir, "p")
		if f != nil {
			f.Close()
		}
		done <- ok
	}()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("Open accepted a FIFO")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Open blocked on a FIFO")
	}
}

func TestOpenRefusesDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := Open(dir, "d"); ok {
		t.Fatal("Open accepted a directory")
	}
}

func TestReadFileRefusesOversize(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "f"), "12345678901")
	if _, _, ok := ReadFile(dir, "f", 10); ok {
		t.Fatal("ReadFile accepted an oversize file")
	}
}

func TestReadFileAtCap(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "f"), "1234567890")
	b, fi, ok := ReadFile(dir, "f", 10)
	if !ok || string(b) != "1234567890" || fi == nil {
		t.Fatalf("ReadFile at cap: %q %v %v", b, fi, ok)
	}
}

func TestReadDirListsEntries(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "d", "a"), "x")
	write(t, filepath.Join(dir, "d", "b"), "x")
	es, ok := ReadDir(dir, "d", 10)
	if !ok || len(es) != 2 {
		t.Fatalf("ReadDir = %d entries, ok=%v", len(es), ok)
	}
}

func TestReadDirCapsEntries(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a", "b", "c"} {
		write(t, filepath.Join(dir, "d", n), "x")
	}
	es, ok := ReadDir(dir, "d", 2)
	if !ok || len(es) != 2 {
		t.Fatalf("ReadDir cap: %d entries, ok=%v", len(es), ok)
	}
}

func TestReadDirRefusesSymlinkedDir(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	write(t, filepath.Join(outside, "f"), "x")
	if err := os.Symlink(outside, filepath.Join(dir, "d")); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadDir(dir, "d", 10); ok {
		t.Fatal("ReadDir followed a symlink out of the root")
	}
}

func TestReadDirRefusesInRootSymlinkedDir(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "real", "f"), "x")
	if err := os.Symlink("real", filepath.Join(dir, "d")); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadDir(dir, "d", 10); ok {
		t.Fatal("ReadDir followed an in-root symlinked dir")
	}
}

func TestReadDirRefusesFile(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "f"), "x")
	if _, ok := ReadDir(dir, "f", 10); ok {
		t.Fatal("ReadDir accepted a regular file")
	}
}

func TestMissingRootIsFalse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nope")
	if _, _, ok := Open(dir, "f"); ok {
		t.Fatal("Open ok on a missing root")
	}
	if _, _, ok := ReadFile(dir, "f", 10); ok {
		t.Fatal("ReadFile ok on a missing root")
	}
	if _, ok := ReadDir(dir, ".", 10); ok {
		t.Fatal("ReadDir ok on a missing root")
	}
}
