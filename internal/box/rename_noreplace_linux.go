package box

import "golang.org/x/sys/unix"

func renameExcl(fromfd int, from string, tofd int, to string) error {
	return unix.Renameat2(fromfd, from, tofd, to, unix.RENAME_NOREPLACE)
}

// noReplaceUnsupported: ENOSYS from a kernel without renameat2, EINVAL from a
// filesystem without RENAME_NOREPLACE.
func noReplaceUnsupported(err error) bool {
	return err == unix.ENOSYS || err == unix.EINVAL
}
