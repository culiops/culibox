package clone

import (
	"fmt"
	"math"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// freeBytes is the space a normal user can still use on the filesystem holding
// dir, or, when dir does not exist yet, the nearest ancestor that does (the
// copy is created there). Statfs field types differ between Linux and macOS,
// so every term goes through uint64.
func freeBytes(dir string) (int64, error) {
	cur := filepath.Clean(dir)
	for {
		if _, err := os.Stat(cur); err == nil {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return 0, fmt.Errorf("no existing ancestor of %s", dir)
		}
		cur = parent
	}
	var st unix.Statfs_t
	if err := unix.Statfs(cur, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", cur, err)
	}
	free := uint64(st.Bavail) * uint64(st.Bsize)
	if free > math.MaxInt64 {
		return math.MaxInt64, nil
	}
	return int64(free), nil
}
