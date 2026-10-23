//go:build linux

package box

import (
	"golang.org/x/sys/unix"
	"path/filepath"
	"strings"
)

// ctxWalkHook, when set (tests only), runs after leafID opens each directory
// above the leaf, with that directory's path.
var ctxWalkHook func(dir string)

// leafID returns the identity of the leaf p names, reached without following
// any symlink. It opens one component at a time, each relative to the
// directory opened before it and with O_NOFOLLOW, so a component swapped for a
// symlink during the walk is refused or never consulted: the leaf found sits
// in the directories that were really there. A Lstat of the whole path would
// not do: it follows symlinks in every component but the last.
func leafID(p string) (*FileID, error) {
	fd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ctxPathRefuse(p, "cannot be read (%v)", err)
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	cur := "/"
	for i, part := range parts {
		if part == "" {
			continue
		}
		flags := unix.O_PATH | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY // an intermediate symlink fails here
		}
		next, err := unix.Openat(fd, part, flags, 0)
		unix.Close(fd)
		cur = filepath.Join(cur, part)
		if err != nil {
			return nil, ctxPathRefuse(p, "changed while it was checked (%s: %v)", cur, err)
		}
		fd = next
		if i < len(parts)-1 && ctxWalkHook != nil {
			ctxWalkHook(cur)
		}
	}
	var st unix.Stat_t
	err = unix.Fstat(fd, &st)
	unix.Close(fd)
	if err != nil {
		return nil, ctxPathRefuse(p, "cannot be read (%v)", err)
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR, unix.S_IFREG:
	case unix.S_IFLNK:
		return nil, ctxPathRefuse(p, "%s is a symlink; list the real path", p)
	default:
		return nil, ctxPathRefuse(p, "must be a directory or a regular file")
	}
	return &FileID{Dev: uint64(st.Dev), Ino: uint64(st.Ino)}, nil
}
