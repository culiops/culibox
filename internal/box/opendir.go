package box

import (
	"io/fs"
	"os"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

// openDirHook, when set (tests only), runs in openDirIn right before the open,
// with the name it is about to open: a test swaps that directory for a FIFO
// there, the race a box can win between a listing or an Lstat and the open.
var openDirHook func(rel string)

// openDirIn opens rel under root as a directory for reading, without ever
// blocking (#330). A box home and a conversations folder are written by boxes
// (the folder by every live box of the profile), so a name that was a directory
// when it was listed or Lstat'ed can be a FIFO by the time it is opened, and a
// plain open of a FIFO blocks until a writer comes: under the box manager's
// opMu, every later spawn, stop and reclaim would wait for a daemon restart.
// root.Open adds no O_NONBLOCK, so this asks for O_DIRECTORY (anything but a
// directory fails with ENOTDIR before it is opened) and O_NONBLOCK (a FIFO
// opened anyway would not wait), on Linux and macOS alike; os.Root adds
// O_NOFOLLOW on the last component and follows a link only inside the root.
// The descriptor's own type is checked after the open, so what comes back is a
// directory.
func openDirIn(root *os.Root, rel string) (*os.File, error) {
	if openDirHook != nil {
		openDirHook(rel)
	}
	f, err := root.OpenFile(rel, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err == nil && !fi.IsDir() {
		err = &fs.PathError{Op: "open", Path: rel, Err: unix.ENOTDIR}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// dirFS is root.FS() for fs.WalkDir, except that every directory it reads is
// opened with openDirIn: a directory swapped for a FIFO during a walk fails to
// open instead of blocking the walk.
type dirFS struct{ root *os.Root }

func (d dirFS) Open(name string) (fs.File, error) { return d.root.FS().Open(name) }

func (d dirFS) Stat(name string) (fs.FileInfo, error) { return fs.Stat(d.root.FS(), name) }

// ReadDir is fs.ReadDirFS's: every entry, sorted by name, types read with
// lstat on the directory's descriptor (os.Root's own ReadDir does the same).
func (d dirFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	f, err := openDirIn(d.root, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ents, err := f.ReadDir(-1)
	slices.SortFunc(ents, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return ents, err
}
