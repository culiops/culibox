package upgradebackup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, registered as "sqlite"
)

// defaultAuditMax is the largest audit.db the snapshot will take.
const defaultAuditMax = 512 << 20

// auditTimeout bounds the whole snapshot (open, lock wait, VACUUM INTO).
const auditTimeout = 2 * time.Minute

// uriEscaper protects the characters that are special in a SQLite file: URI.
var uriEscaper = strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23")

// snapshotAudit writes a consistent copy of <dataDir>/audit.db to dstAbs with
// VACUUM INTO, which includes rows still in the -wal file and leaves the
// -wal/-shm files out. A missing audit.db is skipped silently; a symlink or
// another non-regular file is logged and skipped; an audit.db over max bytes
// is logged with its size and skipped (skipped = that size). Any other
// failure, including a corrupt database, is returned.
func snapshotAudit(r *os.Root, dataDir, dstAbs string, max int64, logf func(string, ...any)) (skipped int64, err error) {
	fi, err := r.Lstat(auditName)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", auditName, err)
	}
	if !fi.Mode().IsRegular() {
		logf("upgrade backup: skipping %s: not a regular file (%s)", auditName, fi.Mode().Type())
		return 0, nil
	}
	if max <= 0 {
		max = defaultAuditMax
	}
	if fi.Size() > max {
		logf("upgrade backup: skipping %s: %d bytes is over the %d byte limit", auditName, fi.Size(), max)
		return fi.Size(), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), auditTimeout)
	defer cancel()
	src := filepath.Join(dataDir, auditName)
	db, err := sql.Open("sqlite", "file:"+uriEscaper.Replace(src)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return 0, fmt.Errorf("snapshot %s: %w", auditName, err)
	}
	_, err = db.ExecContext(ctx, "VACUUM INTO ?", dstAbs)
	if cerr := db.Close(); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil {
		return 0, fmt.Errorf("snapshot %s: %w", auditName, err)
	}

	// VACUUM INTO creates the file with the default mode: set the source's.
	f, err := os.OpenFile(dstAbs, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return 0, fmt.Errorf("snapshot %s: %w", auditName, err)
	}
	defer f.Close()
	if err := f.Chmod(fi.Mode().Perm()); err != nil {
		return 0, fmt.Errorf("snapshot %s: %w", auditName, err)
	}
	if err := f.Sync(); err != nil {
		return 0, fmt.Errorf("snapshot %s: %w", auditName, err)
	}
	return 0, nil
}
