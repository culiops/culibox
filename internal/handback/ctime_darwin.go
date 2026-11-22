package handback

import (
	"io/fs"
	"syscall"
	"time"
)

// ctimeOf returns the inode change time of fi. Unlike mtime it cannot be set
// from user space: a box that touches a file back to the year 2000 still
// moves its ctime. ok is false when fi carries no stat data.
func ctimeOf(fi fs.FileInfo) (time.Time, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return time.Time{}, false
	}
	sec, nsec := st.Ctimespec.Unix()
	return time.Unix(sec, nsec), true
}
