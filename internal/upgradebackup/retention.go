package upgradebackup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// retainWindow keeps every backup newer than this.
	retainWindow = 30 * 24 * time.Hour
	// retainMin is how many of the newest backups are always kept.
	retainMin = 3
	// retainMax caps the total number of backups, oldest deleted first.
	retainMax = 20
)

type backupEntry struct {
	name string
	at   time.Time
}

// backupTime parses the trailing "-YYYYMMDDTHHMMSSZ" of a backup dir name. The
// collision counter (<from>-to-<to>_<N>-<ts>) sits before it, so only the tail
// matters. ok is false for anything else, so a foreign dir is never pruned.
func backupTime(name string) (time.Time, bool) {
	n := len(stampLayout)
	if strings.HasPrefix(name, ".") || len(name) <= n+1 || name[len(name)-n-1] != '-' {
		return time.Time{}, false
	}
	t, err := time.Parse(stampLayout, name[len(name)-n:])
	return t, err == nil
}

// prune applies the retention rule to <R>/backups and returns the names it
// removed. Newest first, an entry is kept when it is one of the retainMin
// newest or no older than retainWindow, and only while fewer than retainMax are
// kept. Non-directories (a symlink included, it is never followed), names
// starting with "." (.tmp-*) and names that do not end in a timestamp are left
// alone and do not count. It keeps going after a failed removal and returns the
// joined errors.
func prune(r *os.Root, now time.Time) (removed []string, err error) {
	ents, err := fs.ReadDir(r.FS(), backupsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", backupsDir, err)
	}
	var bs []backupEntry
	for _, e := range ents {
		if !e.Type().IsDir() {
			continue
		}
		if t, ok := backupTime(e.Name()); ok {
			bs = append(bs, backupEntry{e.Name(), t})
		}
	}
	// Newest first; the name breaks ties so the order is deterministic.
	sort.Slice(bs, func(i, j int) bool {
		if !bs[i].at.Equal(bs[j].at) {
			return bs[i].at.After(bs[j].at)
		}
		return bs[i].name > bs[j].name
	})
	var errs []error
	kept := 0
	for i, b := range bs {
		if (i < retainMin || now.Sub(b.at) <= retainWindow) && kept < retainMax {
			kept++
			continue
		}
		if err := r.RemoveAll(filepath.Join(backupsDir, b.name)); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", b.name, err))
			continue
		}
		removed = append(removed, b.name)
	}
	return removed, errors.Join(errs...)
}
