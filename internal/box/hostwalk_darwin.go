package box

import "golang.org/x/sys/unix"

// hostNoFollowStat is the host's reading of the folder p names, made without
// following a symlink in any component, the last included: one open with
// O_NOFOLLOW_ANY, which makes the kernel refuse a link anywhere in the path
// (macOS 11+; CuliBox requires 13), then an fstat of what was opened. An
// os.Lstat would follow every link above the leaf. It needs search permission on
// the folders above p and read permission on p, as opening the folder to derive
// its guards does. O_NONBLOCK keeps a FIFO at p from blocking the open.
func hostNoFollowStat(p string) (unix.Stat_t, error) {
	var st unix.Stat_t
	fd, err := unix.Open(p, unix.O_RDONLY|unix.O_NOFOLLOW_ANY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return st, err
	}
	defer unix.Close(fd)
	return st, unix.Fstat(fd, &st)
}
