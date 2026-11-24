package box

import "golang.org/x/sys/unix"

// renameNoReplace renames from (in directory fromfd) to to (in directory tofd)
// unless to exists in any form, an empty directory included: then it fails
// with EEXIST and moves nothing (#330). The kernel checks and renames in one
// step (renameExcl: renameat2 RENAME_NOREPLACE on Linux, renameatx_np
// RENAME_EXCL on macOS), so nothing created at to after a caller's own check
// is ever replaced.
//
// Where the call is not supported (a Linux kernel before 3.15 or a filesystem
// without it, a macOS volume without VOL_CAP_INT_RENAME_EXCL, another OS) it
// falls back to a plain renameat, which the caller must have preceded with its
// own check that to is absent: that still never replaces a file or a non-empty
// directory, but it does replace an empty directory created at to in between.
func renameNoReplace(fromfd int, from string, tofd int, to string) error {
	err := renameExcl(fromfd, from, tofd, to)
	if err != nil && noReplaceUnsupported(err) {
		return unix.Renameat(fromfd, from, tofd, to)
	}
	return err
}
