package boxread

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Open and ReadDir resolve a path through os.Root, which follows a link that
// stays inside the root when it is a middle component. That is harmless where
// every file of the root is the box's own anyway (the Playwright output dir).
// OpenIn and ReadDirIn are for a root whose files are not all equal (a kept
// folder, where a path the user asked for must name that very file): they
// walk rel one descriptor at a time and follow no link at all.

// Entry is one name of a directory ReadDirIn listed, as lstat saw it.
type Entry struct {
	Name    string
	Mode    fs.FileMode // the type bits only: fs.ModeDir, fs.ModeSymlink, ...; 0 for a regular file
	Size    int64
	ModTime time.Time
}

// walkRel reports whether rel is a clean, local, slash-separated path ("." is
// the root itself).
func walkRel(rel string) bool {
	if rel == "." {
		return true
	}
	return rel != "" && !strings.HasPrefix(rel, "/") && path.Clean(rel) == rel &&
		!strings.HasPrefix(rel, "../") && rel != ".." && !strings.Contains(rel, "\x00")
}

// openAt opens the entry name of the directory dfd, which lstat says has type
// want (unix.S_IFDIR or unix.S_IFREG), with O_NOFOLLOW and O_NONBLOCK, and
// checks that the descriptor is the very entry lstat saw. A link, a FIFO, a
// device or a socket is refused before it is opened.
func openAt(dfd int, name string, want uint32) (*os.File, bool) {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return nil, false
	}
	var st unix.Stat_t
	if err := unix.Fstatat(dfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil || uint32(st.Mode)&unix.S_IFMT != want {
		return nil, false
	}
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	if want == unix.S_IFDIR {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(dfd, name, flags, 0)
	if err != nil {
		return nil, false
	}
	var got unix.Stat_t
	if err := unix.Fstat(fd, &got); err != nil || got.Dev != st.Dev || got.Ino != st.Ino || uint32(got.Mode)&unix.S_IFMT != want {
		unix.Close(fd)
		return nil, false
	}
	return os.NewFile(uintptr(fd), name), true
}

// openDirPath opens the directory rel under root, one component at a time
// from the root's own descriptor (openAt): every component must be a real
// directory.
func openDirPath(root *os.Root, rel string) (*os.File, bool) {
	d, err := root.OpenFile(".", os.O_RDONLY|unix.O_DIRECTORY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, false
	}
	if rel == "." {
		return d, true
	}
	for _, c := range strings.Split(rel, "/") {
		next, ok := openAt(int(d.Fd()), c, unix.S_IFDIR)
		d.Close()
		if !ok {
			return nil, false
		}
		d = next
	}
	return d, true
}

// OpenIn opens the regular file rel (clean, local) under root for reading and
// returns it with its Fstat info. No link is followed anywhere on the way, and
// anything but a regular file is refused before it is opened, so a FIFO or a
// device is never opened at all. The caller closes the file.
func OpenIn(root *os.Root, rel string) (*os.File, os.FileInfo, bool) {
	if rel == "." || !walkRel(rel) {
		return nil, nil, false
	}
	dir, name := path.Split(rel)
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		dir = "."
	}
	d, ok := openDirPath(root, dir)
	if !ok {
		return nil, nil, false
	}
	defer d.Close()
	f, ok := openAt(int(d.Fd()), name, unix.S_IFREG)
	if !ok {
		return nil, nil, false
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, nil, false
	}
	return f, fi, true
}

// ReadDirIn lists the directory rel under root ("." for root itself), reached
// as OpenIn reaches a file. Each name is lstat'ed on the directory's
// descriptor, never by a path. At most max entries come back; more says there
// were others.
func ReadDirIn(root *os.Root, rel string, max int) (entries []Entry, more, ok bool) {
	if !walkRel(rel) || max <= 0 {
		return nil, false, false
	}
	d, ok := openDirPath(root, rel)
	if !ok {
		return nil, false, false
	}
	defer d.Close()
	var names []string
	for len(names) <= max {
		chunk, err := d.Readdirnames(256)
		names = append(names, chunk...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, false, false
		}
	}
	if len(names) > max {
		names, more = names[:max], true
	}
	dfd := int(d.Fd())
	entries = make([]Entry, 0, len(names))
	for _, n := range names {
		var st unix.Stat_t
		if err := unix.Fstatat(dfd, n, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			continue // gone since it was listed
		}
		sec, nsec := st.Mtim.Unix()
		entries = append(entries, Entry{Name: n, Mode: typeBits(uint32(st.Mode)), Size: st.Size, ModTime: time.Unix(sec, nsec)})
	}
	return entries, more, true
}

func typeBits(mode uint32) fs.FileMode {
	switch mode & unix.S_IFMT {
	case unix.S_IFDIR:
		return fs.ModeDir
	case unix.S_IFLNK:
		return fs.ModeSymlink
	case unix.S_IFIFO:
		return fs.ModeNamedPipe
	case unix.S_IFSOCK:
		return fs.ModeSocket
	case unix.S_IFCHR:
		return fs.ModeDevice | fs.ModeCharDevice
	case unix.S_IFBLK:
		return fs.ModeDevice
	case unix.S_IFREG:
		return 0
	}
	return fs.ModeIrregular
}
