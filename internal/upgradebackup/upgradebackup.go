// Package upgradebackup copies the small data-root files into a timestamped
// backup directory the first time `culibox serve` starts on a new version, so
// an upgrade that migrates them can be rolled back with a plain cp.
package upgradebackup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Options configures Run.
type Options struct {
	DataDir       string // absolute; serve's dataDir
	Version       string // main.version; "" = dev build
	Commit        string
	IncludeConfig bool // true when the loaded config file is <DataDir>/config.json
	Now           time.Time
	AuditMaxBytes int64 // 0 means 512<<20
	Logf          func(format string, args ...any)
}

// Result reports what Run did.
type Result struct {
	Taken        bool     // a backup dir was created
	Dir          string   // absolute path of the new backup ("" if none)
	From         string   // previous version, or "unknown"
	AuditSkipped int64    // audit.db size when skipped over the bound, else 0
	Pruned       []string // dir names removed by retention
}

// backupsDir holds every backup, under the data root.
const backupsDir = "backups"

// auditName is the SQLite audit log; it is snapshotted, not byte-copied.
const auditName = "audit.db"

// topFiles are the top-level data-root files that go into a backup (spec,
// "What is in the backup"). A missing one is skipped. config.json is handled
// separately (Options.IncludeConfig) and audit.db is a SQLite snapshot, not a
// byte copy.
var topFiles = []string{
	"profiles.json", "credentials.json", "accounts.json", "tokens.json",
	"sessions.json", "session-ids.json", "boxed-workspaces.json",
	"vm-shares.json", "inplace-locks.json", "clone-dirs",
	"login.token", "cookie.secret", "broker.secret", "egress.secret",
	"relay2.secret", "relay.secret",
}

// Run backs up the data root when the running version differs from the stamp,
// then records the new version in the stamp. A dev build (empty version) and a
// start on the same version do nothing; a fresh install (no included file
// yet) only writes the stamp. On a backup failure the stamp is left alone so
// the next start retries, and the error is returned for serve to refuse to
// start.
func Run(o Options) (Result, error) {
	if o.Version == "" {
		return Result{}, nil
	}
	root, err := os.OpenRoot(o.DataDir)
	if err != nil {
		return Result{}, fmt.Errorf("upgrade backup: open data dir: %w", err)
	}
	defer root.Close()

	from := readStamp(root)
	if from == stampField(o.Version) {
		return Result{}, nil
	}
	res := Result{From: from}

	found, err := hasIncluded(root, o)
	if err != nil {
		return res, fmt.Errorf("upgrade backup: %w", err)
	}
	if !found {
		if err := writeStamp(root, o.Version, o.Commit); err != nil {
			return res, fmt.Errorf("upgrade backup: write stamp: %w", err)
		}
		return res, nil
	}

	name, err := copyBackup(root, from, o)
	if err != nil {
		return res, fmt.Errorf("upgrade backup: %w", err)
	}
	res.Taken = true
	res.Dir = filepath.Join(o.DataDir, backupsDir, name)
	if err := writeStamp(root, o.Version, o.Commit); err != nil {
		return res, fmt.Errorf("upgrade backup: write stamp: %w", err)
	}
	return res, nil
}

// hasIncluded reports whether the data root holds anything the backup would
// take: any allowlisted top-level entry (of any type, Lstat, never followed),
// config.json when included, a non-empty topDir or bin/, an account file under
// accounts/*/claude/, or audit.db (existence only; its snapshot is Task 3's).
func hasIncluded(root *os.Root, o Options) (bool, error) {
	names := append([]string(nil), topFiles...)
	if o.IncludeConfig {
		names = append(names, "config.json")
	}
	names = append(names, auditName)
	for _, n := range names {
		ok, err := exists(root, n)
		if err != nil || ok {
			return ok, err
		}
	}
	for _, d := range append(append([]string(nil), topDirs...), binDir) {
		fi, err := root.Lstat(d)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("stat %s: %w", d, err)
		}
		if !fi.IsDir() {
			return true, nil
		}
		ents, err := fs.ReadDir(root.FS(), d)
		if err != nil {
			return false, fmt.Errorf("read %s: %w", d, err)
		}
		if len(ents) > 0 {
			return true, nil
		}
	}
	found := false
	err := forEachAccount(root, o.logf, func(claude string) error {
		for _, f := range accountFiles {
			ok, err := exists(root, filepath.Join(claude, f))
			if err != nil {
				return err
			}
			found = found || ok
		}
		return nil
	})
	return found, err
}

func exists(root *os.Root, rel string) (bool, error) {
	_, err := root.Lstat(rel)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("stat %s: %w", rel, err)
}
