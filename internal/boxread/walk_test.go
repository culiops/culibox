package boxread

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestOpenInRegular(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a", "b", "f.txt"), "hello")
	f, fi, ok := OpenIn(openRoot(t, dir), "a/b/f.txt")
	if !ok {
		t.Fatal("OpenIn refused a regular file")
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil || string(b) != "hello" || fi.Size() != 5 || !fi.Mode().IsRegular() {
		t.Fatalf("read %q %v, size %d mode %v", b, err, fi.Size(), fi.Mode())
	}
}

// os.Root follows a link that stays inside it in a middle component; OpenIn
// walks one descriptor at a time and follows none.
func TestOpenInRefusesLinks(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "real", "f.txt"), "x")
	if err := os.Symlink("real", filepath.Join(dir, "mid")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real/f.txt", filepath.Join(dir, "last")); err != nil {
		t.Fatal(err)
	}
	root := openRoot(t, dir)
	for _, rel := range []string{"mid/f.txt", "last", "real/../real/f.txt", "/real/f.txt", "", ".", "real", "real/"} {
		if f, _, ok := OpenIn(root, rel); ok {
			f.Close()
			t.Errorf("OpenIn(%q) opened, want refused", rel)
		}
	}
}

func TestOpenInRefusesFIFO(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "p"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() {
		f, _, ok := OpenIn(openRoot(t, dir), "p")
		if ok {
			f.Close()
		}
		done <- ok
	}()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("OpenIn opened a FIFO")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OpenIn blocked on a FIFO")
	}
}

func TestReadDirIn(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "d", "f.txt"), "12345")
	if err := os.Mkdir(filepath.Join(dir, "d", "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(dir, "d", "l")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "d", "p"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("d", filepath.Join(dir, "ld")); err != nil {
		t.Fatal(err)
	}
	root := openRoot(t, dir)
	es, more, ok := ReadDirIn(root, "d", 100)
	if !ok || more {
		t.Fatalf("ReadDirIn(d): ok %v more %v", ok, more)
	}
	got := map[string]Entry{}
	var names []string
	for _, e := range es {
		got[e.Name] = e
		names = append(names, e.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"f.txt", "l", "p", "sub"}) {
		t.Fatalf("names %v", names)
	}
	if e := got["f.txt"]; !e.Mode.IsRegular() || e.Size != 5 || e.ModTime.IsZero() {
		t.Errorf("f.txt %+v", e)
	}
	if !got["sub"].Mode.IsDir() || got["l"].Mode&fs.ModeSymlink == 0 || got["p"].Mode&fs.ModeNamedPipe == 0 {
		t.Errorf("types: sub %v l %v p %v", got["sub"].Mode, got["l"].Mode, got["p"].Mode)
	}
	if _, _, ok := ReadDirIn(root, ".", 100); !ok {
		t.Error(`ReadDirIn(".") refused the root itself`)
	}
	for _, rel := range []string{"ld", "d/l", "d/f.txt", "d/p", "../x", "", "/d"} {
		if _, _, ok := ReadDirIn(root, rel, 100); ok {
			t.Errorf("ReadDirIn(%q) listed, want refused", rel)
		}
	}
	es, more, ok = ReadDirIn(root, "d", 2)
	if !ok || !more || len(es) != 2 {
		t.Fatalf("capped listing: %d entries, more %v, ok %v; want 2, true, true", len(es), more, ok)
	}
}
