//go:build !linux && !darwin

package box

import "golang.org/x/sys/unix"

// Neither Linux nor macOS: no no-replace rename is used, renameNoReplace
// falls back to its checked plain rename.
func renameExcl(int, string, int, string) error { return unix.ENOSYS }

func noReplaceUnsupported(err error) bool { return err == unix.ENOSYS }
