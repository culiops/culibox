package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/culiops/culibox/internal/upgradebackup"
)

// upgradeBackup runs the version-change backup of the data root for serve. It
// returns an error holding the full fail-closed text when the backup cannot be
// made; serve then refuses to start, before anything migrates the data. cfgFiles
// are the loaded config files: config.json joins the backup only when one of
// them is <dataDir>/config.json.
func upgradeBackup(dataDir string, cfgFiles []string, ver, commit string, now time.Time, exe string, logf func(string, ...any)) error {
	res, err := upgradebackup.Run(upgradebackup.Options{
		DataDir:       dataDir,
		Version:       ver,
		Commit:        commit,
		IncludeConfig: configInRoot(dataDir, cfgFiles),
		Now:           now,
		Logf:          logf,
	})
	if err != nil {
		return fmt.Errorf("%v. Nothing in the data root was changed. Fix the cause and start again, "+
			"or go back to the previous binary (%s.prev, kept by deploy-prod.sh and setup.sh). "+
			"See docs/UPGRADING.md.", failText(err), exe)
	}
	if res.Taken {
		logf("upgrade backup: saved the data root to %s (previous version %s)", res.Dir, res.From)
		logf("upgrade backup: to undo this upgrade's data changes, stop culibox, put the previous binary back, then run: %s",
			upgradebackup.RestoreCommand(dataDir, res.Dir))
		logf("upgrade backup: (see docs/UPGRADING.md)")
	}
	if len(res.Pruned) > 0 {
		logf("upgrade backup: removed old backups: %s", strings.Join(res.Pruned, ", "))
	}
	return nil
}

// failText is err's text under the "upgrade backup failed: " heading, without
// repeating the package's own "upgrade backup: " prefix.
func failText(err error) string {
	return "upgrade backup failed: " + strings.TrimPrefix(err.Error(), "upgrade backup: ")
}

// configInRoot reports whether any loaded config file is <dataDir>/config.json.
func configInRoot(dataDir string, cfgFiles []string) bool {
	want := filepath.Join(dataDir, "config.json")
	for _, f := range cfgFiles {
		if abs, err := filepath.Abs(f); err == nil && abs == want {
			return true
		}
	}
	return false
}

// exePath is the running binary with symlinks resolved, or "culibox" when the
// OS cannot say. The message appends ".prev" to it.
func exePath() string {
	exe, err := os.Executable()
	if err != nil {
		return "culibox"
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		return p
	}
	return exe
}
