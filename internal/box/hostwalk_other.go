//go:build !darwin

package box

import (
	"strings"

	"golang.org/x/sys/unix"
)

// hostNoFollowStat is the host's reading of the folder p names, made without
// following a symlink in any component: each folder above the leaf is opened
// relative to the one before it with O_NOFOLLOW, and the leaf is stat'ed
// there with AT_SYMLINK_NOFOLLOW, so a link above it is an error and a link at
// it is reported as one. An os.Lstat would follow every link above the leaf.
// (macOS has a single-open form, see hostwalk_darwin.go; Linux's own host
// reading is the walk of noFollowStat, and this one serves the tests that stand
// in for macOS.)
func hostNoFollowStat(p string) (unix.Stat_t, error) {
	var st unix.Stat_t
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return st, err
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for i, part := range parts {
		if part == "" { // p is "/"
			continue
		}
		if i == len(parts)-1 {
			err = unix.Fstatat(fd, part, &st, unix.AT_SYMLINK_NOFOLLOW)
			unix.Close(fd)
			return st, err
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			if err == unix.ENOTDIR {
				err = unix.ELOOP // a link in the way (Linux says ENOTDIR for a link opened with O_DIRECTORY)
			}
			return st, err
		}
		fd = next
	}
	err = unix.Fstat(fd, &st)
	unix.Close(fd)
	return st, err
}
