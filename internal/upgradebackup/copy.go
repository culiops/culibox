package upgradebackup

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// topDirs are the top-level data-root dirs copied whole (regular files only).
// `store` is ciphertext and is copied as bytes, never opened as a secret store.
var topDirs = []string{"store", "clone-bases", "clone-imports", "clone-return"}

// accountFiles are the files CuliBox itself writes in accounts/<name>/claude/,
// relative to that dir. They are the account package's MCPFileName,
// DelegateFileName and manifest name, plus settings.json; a test pins the match
// (importing account here would pull the secretstore package in).
var accountFiles = []string{
	"settings.json", ".agentbox-manifest.json",
	"agentbox-mcp.json", ".agentbox-statusline-delegate",
}

// binDir holds the statusLine script.
const binDir = "bin"

// maxCollisions bounds the numeric suffix search for a free backup dir name.
const maxCollisions = 1000

// tmpPrefix marks a backup dir that is still being built.
const tmpPrefix = ".tmp-"

func (o Options) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

// uniqueName returns the first backup dir name not yet in backups/. A second
// attempt in the same second (the stamp was lost) gets "<from>-to-<to>_2-<ts>",
// "_3", ...: the counter sits in the version part so the trailing timestamp
// stays parseable by retention.
func uniqueName(root *os.Root, from, to string, now time.Time) (string, error) {
	for n := 1; n <= maxCollisions; n++ {
		name := dirName(from, to, now)
		if n > 1 {
			name = dirName(from, to+"_"+strconv.Itoa(n), now)
		}
		_, err := root.Lstat(filepath.Join(backupsDir, name))
		if errors.Is(err, fs.ErrNotExist) {
			return name, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("no free backup dir name for %s-to-%s", from, to)
}

// copyBackup builds backups/.tmp-<name>, copies the allowlist into it, fsyncs
// it, renames it to backups/<name> and fsyncs backups/. It returns <name>. A
// failed attempt leaves its .tmp- dir for the next attempt to remove.
func copyBackup(root *os.Root, from string, o Options) (string, error) {
	if err := prepareBackupsDir(root); err != nil {
		return "", err
	}
	name, err := uniqueName(root, from, o.Version, o.Now)
	if err != nil {
		return "", err
	}
	tmp := filepath.Join(backupsDir, tmpPrefix+name)
	if err := root.Mkdir(tmp, 0o700); err != nil {
		return "", err
	}
	c := &copier{root: root, tmp: tmp, logf: o.logf}
	if err := c.copyAll(o.IncludeConfig); err != nil {
		return "", err
	}
	if err := c.syncDirs(); err != nil {
		return "", err
	}
	if err := root.Rename(tmp, filepath.Join(backupsDir, name)); err != nil {
		return "", err
	}
	if err := syncDir(root, backupsDir); err != nil {
		return "", err
	}
	return name, nil
}

// prepareBackupsDir makes sure backups/ is a real directory and removes the
// .tmp- dirs earlier failed attempts left behind.
func prepareBackupsDir(root *os.Root) error {
	fi, err := root.Lstat(backupsDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return root.Mkdir(backupsDir, 0o700)
	case err != nil:
		return err
	case !fi.IsDir():
		return fmt.Errorf("%s is not a directory", backupsDir)
	}
	ents, err := fs.ReadDir(root.FS(), backupsDir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), tmpPrefix) {
			continue
		}
		p := filepath.Join(backupsDir, e.Name())
		if e.Type().IsDir() {
			err = root.RemoveAll(p)
		} else {
			err = root.Remove(p)
		}
		if err != nil {
			return fmt.Errorf("remove leftover %s: %w", p, err)
		}
	}
	return nil
}

func syncDir(root *os.Root, rel string) error {
	d, err := root.Open(rel)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// copier copies allowlisted regular files from the data root into tmp (a path
// relative to the same root). It never follows a symlink: every source is
// Lstat'd first and anything but a regular file or a real directory is logged
// and skipped.
type copier struct {
	root *os.Root
	tmp  string
	logf func(format string, args ...any)
}

func (c *copier) copyAll(includeConfig bool) error {
	names := topFiles
	if includeConfig {
		names = append(append([]string(nil), topFiles...), "config.json")
	}
	// The stamp is copied as it is before this run, so a restore puts it back.
	names = append(append([]string(nil), names...), StampName)
	for _, n := range names {
		if err := c.file(n); err != nil {
			return err
		}
	}
	for _, d := range append(append([]string(nil), topDirs...), binDir) {
		if err := c.dir(d); err != nil {
			return err
		}
	}
	return forEachAccount(c.root, c.logf, func(claudeRel string) error {
		for _, f := range accountFiles {
			if err := c.file(filepath.Join(claudeRel, f)); err != nil {
				return err
			}
		}
		return nil
	})
}

// file copies one regular file; a missing file is skipped silently.
func (c *copier) file(rel string) error {
	fi, err := c.root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", rel, err)
	}
	if !fi.Mode().IsRegular() {
		c.logf("upgrade backup: skipping %s: not a regular file (%s)", rel, fi.Mode().Type())
		return nil
	}
	src, err := c.root.Open(rel)
	if err != nil {
		return fmt.Errorf("read %s: %w", rel, err)
	}
	defer src.Close()
	if cur, err := src.Stat(); err != nil || !os.SameFile(fi, cur) {
		return fmt.Errorf("read %s: changed while opening", rel)
	}
	if err := c.root.MkdirAll(filepath.Join(c.tmp, filepath.Dir(rel)), 0o700); err != nil {
		return fmt.Errorf("copy %s: %w", rel, err)
	}
	// Create 0600 (never wider than the source), then set the exact mode: the
	// umask would otherwise narrow it.
	dst, err := c.root.OpenFile(filepath.Join(c.tmp, rel), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("copy %s: %w", rel, err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return fmt.Errorf("copy %s: %w", rel, err)
	}
	if err := dst.Chmod(fi.Mode().Perm()); err != nil {
		dst.Close()
		return fmt.Errorf("copy %s: %w", rel, err)
	}
	if err := dst.Sync(); err != nil {
		dst.Close()
		return fmt.Errorf("copy %s: %w", rel, err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("copy %s: %w", rel, err)
	}
	return nil
}

// dir copies the regular files under a real directory. A missing dir is
// skipped silently; a symlink or other type at its path is logged and skipped.
func (c *copier) dir(rel string) error {
	fi, err := c.root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", rel, err)
	}
	if !fi.IsDir() {
		c.logf("upgrade backup: skipping %s: not a directory (%s)", rel, fi.Mode().Type())
		return nil
	}
	return fs.WalkDir(c.root.FS(), rel, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		p = filepath.FromSlash(p)
		switch {
		case d.IsDir():
			if err := c.root.MkdirAll(filepath.Join(c.tmp, p), 0o700); err != nil {
				return fmt.Errorf("copy %s: %w", p, err)
			}
			return nil
		case d.Type().IsRegular():
			return c.file(p)
		default:
			c.logf("upgrade backup: skipping %s: not a regular file (%s)", p, d.Type())
			return nil
		}
	})
}

// syncDirs fsyncs every directory of the finished tmp tree, children first
// enough for durability: each dir is synced after all its entries exist.
func (c *copier) syncDirs() error {
	var dirs []string
	err := fs.WalkDir(c.root.FS(), filepath.ToSlash(c.tmp), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, filepath.FromSlash(p))
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := syncDir(c.root, dirs[i]); err != nil {
			return fmt.Errorf("sync %s: %w", dirs[i], err)
		}
	}
	return nil
}

// forEachAccount calls fn with accounts/<name>/claude for every account whose
// claude dir is a real directory. Symlinks and other types are logged and
// skipped; a missing accounts/ is silent.
func forEachAccount(root *os.Root, logf func(string, ...any), fn func(claudeRel string) error) error {
	fi, err := root.Lstat("accounts")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat accounts: %w", err)
	}
	if !fi.IsDir() {
		logf("upgrade backup: skipping accounts: not a directory (%s)", fi.Mode().Type())
		return nil
	}
	ents, err := fs.ReadDir(root.FS(), "accounts")
	if err != nil {
		return fmt.Errorf("read accounts: %w", err)
	}
	for _, e := range ents {
		rel := filepath.Join("accounts", e.Name())
		if !e.Type().IsDir() {
			logf("upgrade backup: skipping %s: not a directory (%s)", rel, e.Type())
			continue
		}
		claude := filepath.Join(rel, "claude")
		cfi, err := root.Lstat(claude)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("stat %s: %w", claude, err)
		}
		if !cfi.IsDir() {
			logf("upgrade backup: skipping %s: not a directory (%s)", claude, cfi.Mode().Type())
			continue
		}
		if err := fn(claude); err != nil {
			return err
		}
	}
	return nil
}
