package upgradebackup

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

// StampName is the data-root file that records the version that last ran
// `serve` here: one line, "<version> <commit>\n".
const StampName = "last-version"

// unknownVersion stands in for a missing or unreadable stamp.
const unknownVersion = "unknown"

// maxStampBytes bounds the stamp read; a real stamp is a few dozen bytes.
const maxStampBytes = 4 << 10

// stampField makes s safe as one space-free token on the stamp line. Both the
// writer and the comparison use it, so a version holding a space or control
// byte still compares equal to its own stamp instead of backing up every start.
func stampField(s string) string {
	return strings.Map(func(r rune) rune {
		if r == utf8.RuneError || unicode.IsSpace(r) || unicode.IsControl(r) {
			return '_'
		}
		return r
	}, s)
}

// readStamp returns the version recorded in the stamp. A missing, empty,
// non-regular or malformed stamp is "unknown", never an error: it only decides
// whether to back up, and a bad stamp must not stop serve.
func readStamp(root *os.Root) string {
	fi, err := root.Lstat(StampName)
	if err != nil || !fi.Mode().IsRegular() {
		return unknownVersion
	}
	f, err := root.Open(StampName)
	if err != nil {
		return unknownVersion
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxStampBytes))
	if err != nil || !utf8.Valid(b) {
		return unknownVersion
	}
	line, _, _ := strings.Cut(string(b), "\n")
	fields := strings.Fields(line)
	if len(fields) == 0 || stampField(fields[0]) != fields[0] {
		return unknownVersion
	}
	return fields[0]
}

// writeStamp replaces the stamp atomically: write a sibling tmp file, fsync it,
// rename it over the stamp, then fsync the data root so the rename is durable.
func writeStamp(root *os.Root, version, commit string) error {
	if commit == "" {
		commit = unknownVersion
	}
	body := []byte(stampField(version) + " " + stampField(commit) + "\n")
	tmp := StampName + ".tmp"
	// os.Root follows a symlink inside the root, so a planted
	// last-version.tmp -> credentials.json would be truncated and overwritten.
	// Remove whatever is at the temp name (a link is removed, never followed),
	// then create it fresh and refuse a link.
	if err := root.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := root.Rename(tmp, StampName); err != nil {
		return err
	}
	return syncRoot(root)
}

// syncRoot fsyncs the data root directory itself.
func syncRoot(root *os.Root) error {
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// stampLayout is the UTC timestamp that ends every backup dir name; dirName
// writes it and backupTime (retention.go) parses it.
const stampLayout = "20060102T150405Z"

// dirName is the backup directory's base name: <from>-to-<to>-<UTC stamp>.
// Every character outside [A-Za-z0-9._-] becomes "_", so a version such as
// "a/b c" still yields one safe name inside backups/.
func dirName(from, to string, now time.Time) string {
	return fmt.Sprintf("%s-to-%s-%s", sanitizeName(from), sanitizeName(to), now.UTC().Format(stampLayout))
}

func sanitizeName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			return r
		}
		return '_'
	}, s)
}
