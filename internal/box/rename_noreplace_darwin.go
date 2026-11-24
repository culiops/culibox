package box

import "golang.org/x/sys/unix"

func renameExcl(fromfd int, from string, tofd int, to string) error {
	return unix.RenameatxNp(fromfd, from, tofd, to, unix.RENAME_EXCL)
}

// noReplaceUnsupported: ENOTSUP from a volume without RENAME_EXCL, ENOSYS or
// EINVAL from a system without the call or the flag.
func noReplaceUnsupported(err error) bool {
	return err == unix.ENOTSUP || err == unix.EOPNOTSUPP || err == unix.ENOSYS || err == unix.EINVAL
}
