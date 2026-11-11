package box

import (
	"os"

	"golang.org/x/sys/unix"
)

// pcCaseSensitive is _PC_CASE_SENSITIVE from <sys/unistd.h>: pathconf answers
// 1 on a case-sensitive volume, 0 on a case-insensitive one (the APFS default).
// golang.org/x/sys/unix does not name it.
const pcCaseSensitive = 11

// darwinFlags is the guard locks' flag surface on macOS: UF_IMMUTABLE (the
// owner-settable `uchg`), read with fstat and set with fchflags on the open
// file, so the file flagged is the one opened and checked (guardLocks.hold).
type darwinFlags struct{}

func platformFlagOps() flagOps { return darwinFlags{} }

func (darwinFlags) caseSensitive(dir string) (bool, error) {
	v, err := unix.Pathconf(dir, pcCaseSensitive)
	if err != nil {
		return false, err
	}
	return v == 1, nil
}

func (darwinFlags) flags(f *os.File) (uint32, error) {
	var st unix.Stat_t
	var serr error
	rc, err := f.SyscallConn()
	if err != nil {
		return 0, err
	}
	if err := rc.Control(func(fd uintptr) { serr = unix.Fstat(int(fd), &st) }); err != nil {
		return 0, err
	}
	return st.Flags, serr
}

func (d darwinFlags) immutable(f *os.File) (bool, error) {
	fl, err := d.flags(f)
	return fl&unix.UF_IMMUTABLE != 0, err
}

func (d darwinFlags) setImmutable(f *os.File, on bool) error {
	fl, err := d.flags(f)
	if err != nil {
		return err
	}
	if on {
		fl |= unix.UF_IMMUTABLE
	} else {
		fl &^= unix.UF_IMMUTABLE
	}
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var cerr error
	if err := rc.Control(func(fd uintptr) { cerr = unix.Fchflags(int(fd), int(fl)) }); err != nil {
		return err
	}
	return cerr
}
