//go:build linux

package box

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// ctxWalkHook, when set (tests only), runs after leafID opens each directory
// above the leaf, with that directory's path.
var ctxWalkHook func(dir string)

// noFollowStat returns the Stat_t of the leaf p names, reached without following
// any symlink. It opens one component at a time, each relative to the
// directory opened before it and with O_NOFOLLOW, so a component swapped for a
// symlink during the walk is refused or never consulted: the leaf found sits
// in the directories that were really there. A Lstat of the whole path would
// not do: it follows symlinks in every component but the last.
//
// On an openat failure at is the path walked so far, up to and including the
// component that failed; it is "" when opening "/" or the final Fstat failed.
// ctxWalkHook runs after each directory above the leaf is opened.
func noFollowStat(p string) (st unix.Stat_t, at string, err error) {
	fd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return st, "", err
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
			return st, cur, err
		}
		fd = next
		if i < len(parts)-1 && ctxWalkHook != nil {
			ctxWalkHook(cur)
		}
	}
	err = unix.Fstat(fd, &st)
	unix.Close(fd)
	return st, "", err
}

// leafID returns the identity of the leaf p names, reached without following
// any symlink (see noFollowStat).
func leafID(p string) (*FileID, error) {
	st, at, err := noFollowStat(p)
	if err != nil {
		if at != "" {
			return nil, ctxPathRefuse(p, "changed while it was checked (%s: %v)", at, err)
		}
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

// workspaceID returns the identity of the workspace directory p, which the
// caller has already resolved (absolute, no symlink in it). The same no-follow
// walk as leafID, so a component that has become a symlink since the
// resolution, the leaf included, is refused as ErrWorkspaceMountChanged rather
// than followed: the identity is that of the directory p was resolved to, never
// of where a link now leads. A leaf that is not a directory is the user's to
// fix and is an ErrUnboxableWorkspace.
func workspaceID(p string) (*FileID, error) {
	st, at, err := noFollowStat(p)
	if err != nil {
		if at != "" {
			return nil, wsChanged(p, fmt.Sprintf("its path changed while it was being checked (%s: %v)", at, err))
		}
		return nil, wsChanged(p, fmt.Sprintf("it cannot be read (%v)", err))
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
	case unix.S_IFLNK:
		return nil, wsChanged(p, "its path is now a symlink")
	default:
		return nil, fmt.Errorf("%w: %s: it is not a directory", ErrUnboxableWorkspace, p)
	}
	return &FileID{Dev: uint64(st.Dev), Ino: uint64(st.Ino)}, nil
}

// fileInfoID is the identity of an open file's FileInfo, nil when it carries
// none.
func fileInfoID(fi fs.FileInfo) *FileID {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return &FileID{Dev: uint64(st.Dev), Ino: uint64(st.Ino)}
}
