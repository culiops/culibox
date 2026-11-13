// Package upgradebackup copies the small data-root files into a timestamped
// backup directory the first time `culibox serve` starts on a new version, so
// an upgrade that migrates them can be rolled back with a plain cp.
package upgradebackup

import (
	"errors"
	"fmt"
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

	found, err := hasIncluded(root, o.IncludeConfig)
	if err != nil {
		return res, fmt.Errorf("upgrade backup: %w", err)
	}
	if !found {
		if err := writeStamp(root, o.Version, o.Commit); err != nil {
			return res, fmt.Errorf("upgrade backup: write stamp: %w", err)
		}
		return res, nil
	}

	name := dirName(from, o.Version, o.Now)
	if err := copyBackup(root, name); err != nil {
		return res, fmt.Errorf("upgrade backup: %w", err)
	}
	res.Taken = true
	res.Dir = filepath.Join(o.DataDir, backupsDir, name)
	if err := writeStamp(root, o.Version, o.Commit); err != nil {
		return res, fmt.Errorf("upgrade backup: write stamp: %w", err)
	}
	return res, nil
}

// hasIncluded reports whether any allowlisted top-level file exists. An entry
// of any type counts (Lstat, never followed): it is data the upgrade could touch.
func hasIncluded(root *os.Root, includeConfig bool) (bool, error) {
	names := topFiles
	if includeConfig {
		names = append(append([]string(nil), topFiles...), "config.json")
	}
	for _, n := range names {
		_, err := root.Lstat(n)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("stat %s: %w", n, err)
		}
	}
	return false, nil
}

// copyBackup is a stub until the copy lands: it builds backups/.tmp-<name> and
// renames it to backups/<name>, both mode 0700.
func copyBackup(root *os.Root, name string) error {
	if err := root.Mkdir(backupsDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	tmp := filepath.Join(backupsDir, ".tmp-"+name)
	if err := root.Mkdir(tmp, 0o700); err != nil {
		return err
	}
	return root.Rename(tmp, filepath.Join(backupsDir, name))
}
