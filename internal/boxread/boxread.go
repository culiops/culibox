// Package boxread reads files that a contained agent wrote.
//
// Everything under <data_dir>/boxstate/<box>/ is written by the box as the
// daemon's own uid, so it is hostile input: a path inside it may be a symlink
// out of the directory, a FIFO that blocks the reader forever, or a file of
// any size. Every read of such a file goes through this package. Any surprise
// is reported as ok=false ("no data"); callers fall back silently.
package boxread

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// isSymlink reports whether the final component of rel is a symlink (or cannot
// be examined). os.Root already refuses any path that resolves outside the
// root, but it follows a symlink that stays inside it even when O_NOFOLLOW is
// passed (measured: Go 1.26), so the final component is checked here. A swap
// between this check and the open can only redirect to another path inside the
// root, so the race is harmless; the escape and blocking guards do not depend
// on it.
func isSymlink(root *os.Root, rel string) bool {
	fi, err := root.Lstat(rel)
	return err != nil || fi.Mode()&os.ModeSymlink != 0
}

// Open opens dir/rel for reading and returns the file with its Fstat info.
// It refuses an empty or absolute rel, anything that resolves outside dir
// (os.Root), a symlink as the final component (O_NOFOLLOW), and anything that
// is not a regular file. O_NONBLOCK keeps an open of a FIFO from blocking
// before the type check. The caller closes the returned file.
func Open(dir, rel string) (*os.File, os.FileInfo, bool) {
	if rel == "" || filepath.IsAbs(rel) {
		return nil, nil, false
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, false
	}
	// The root is only needed to resolve the open; the file stays valid.
	defer root.Close()
	if isSymlink(root, rel) {
		return nil, nil, false
	}
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, false
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, nil, false
	}
	return f, fi, true
}

// ReadFile reads dir/rel through Open. A file larger than max is refused; at
// most max bytes are read even if the file grows after the size check.
func ReadFile(dir, rel string, max int64) ([]byte, os.FileInfo, bool) {
	f, fi, ok := Open(dir, rel)
	if !ok {
		return nil, nil, false
	}
	defer f.Close()
	if max < 0 || fi.Size() > max {
		return nil, nil, false
	}
	b, err := io.ReadAll(io.LimitReader(f, max))
	if err != nil {
		return nil, nil, false
	}
	return b, fi, true
}

// ReadDir lists up to max entries of the directory dir/rel (use "." for dir
// itself). The directory is opened through the root with O_NOFOLLOW and
// O_DIRECTORY, so a symlinked final component, a path out of dir, and a
// non-directory are all refused. Entries are returned as-is: callers open
// each one with Open, never with a path they build themselves.
func ReadDir(dir, rel string, max int) ([]os.DirEntry, bool) {
	if rel == "" || filepath.IsAbs(rel) || max <= 0 {
		return nil, false
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, false
	}
	defer root.Close()
	if isSymlink(root, rel) {
		return nil, false
	}
	d, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false
	}
	defer d.Close()
	es, err := d.ReadDir(max)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false
	}
	return es, true
}
