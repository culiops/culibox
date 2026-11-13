package box

import "golang.org/x/sys/unix"

// hostNoFollowStat is the host's reading of the folder p names, made without
// following a symlink in any component, the last included: one open with
// O_NOFOLLOW_ANY, which makes the kernel refuse a link anywhere in the path
// (macOS 11+; CuliBox requires 13), then an fstat of what was opened. An
// os.Lstat would follow every link above the leaf. It needs search permission on
// the folders above p and read permission on p, as opening the folder to derive
// its guards does. O_DIRECTORY keeps a device or FIFO at p from being opened at
// all (ENOTDIR); O_NONBLOCK is belt and braces.
//
// An ENOTDIR is told from a link at p, which may answer with it too: a link is
// ELOOP (a changed path), anything else stays ENOTDIR (not a directory). The
// Lstat only picks which refusal to word; the open already refused.
func hostNoFollowStat(p string) (unix.Stat_t, error) {
	var st unix.Stat_t
	fd, err := unix.Open(p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW_ANY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if err == unix.ENOTDIR {
			var ls unix.Stat_t
			if unix.Lstat(p, &ls) == nil && ls.Mode&unix.S_IFMT == unix.S_IFLNK {
				err = unix.ELOOP
			}
		}
		return st, err
	}
	defer unix.Close(fd)
	return st, unix.Fstat(fd, &st)
}
