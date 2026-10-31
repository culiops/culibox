package clone

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// The base record is the host's own memory of which commits it put into a
// clone as `main` (spec: clone refresh). It lives in the host data dir, never in
// the clone, so nothing a box writes can forge it. `clone fetch` compares it
// with the host's history to catch a rewrite.
const (
	baseRecordDir = "clone-bases"
	// maxBaseLines bounds the record; it only matters after years of daily spawns.
	maxBaseLines = 1000
	// unverifiedMarker is the first line of a record that was not started by a
	// clone creation: its earliest SHAs were not seen at the time they were made.
	unverifiedMarker = "unverified"
	// maxBaseFile bounds the read: 1000 SHA-256 lines plus the marker is ~65 KB.
	maxBaseFile = 1 << 20
)

// BaseRecord is one clone's record. Exists is false when there is no record
// (SHAs is then empty).
type BaseRecord struct {
	SHAs       []string
	Unverified bool
	Exists     bool
}

// baseRecordPath is <dataDir>/clone-bases/<hex sha256 of the resolved clone
// dir>, so two spellings of one directory share a record.
func baseRecordPath(dataDir, cloneDir string) (string, error) {
	key, err := cloneKeyHash(cloneDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, baseRecordDir, key), nil
}

// cloneKeyHash is the hex sha256 of the resolved clone dir: the file name every
// host-owned per-clone record uses.
func cloneKeyHash(cloneDir string) (string, error) {
	real, err := resolvePath(cloneDir)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(real))
	return hex.EncodeToString(sum[:]), nil
}

// ReadBaseRecord reads the record of cloneDir. A missing file is not an error.
// The file is host-owned, but it is still opened without following a symlink.
func ReadBaseRecord(dataDir, cloneDir string) (BaseRecord, error) {
	p, err := baseRecordPath(dataDir, cloneDir)
	if err != nil {
		return BaseRecord{}, err
	}
	return readBaseFile(p)
}

func readBaseFile(p string) (BaseRecord, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return BaseRecord{}, nil
	}
	if err != nil {
		return BaseRecord{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return BaseRecord{}, err
	}
	if !st.Mode().IsRegular() {
		return BaseRecord{}, fmt.Errorf("base record %s is not a regular file", p)
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxBaseFile+1))
	if err != nil {
		return BaseRecord{}, err
	}
	if len(raw) > maxBaseFile {
		return BaseRecord{}, fmt.Errorf("base record %s is larger than %d bytes", p, maxBaseFile)
	}
	rec := BaseRecord{Exists: true}
	text := strings.TrimSuffix(string(raw), "\n")
	if text == "" {
		return rec, nil
	}
	for i, line := range strings.Split(text, "\n") {
		switch {
		case i == 0 && line == unverifiedMarker:
			rec.Unverified = true
		case shaRE.MatchString(line):
			rec.SHAs = append(rec.SHAs, line)
		default:
			return BaseRecord{}, fmt.Errorf("base record %s: line %d is not a commit SHA", p, i+1)
		}
	}
	return rec, nil
}

// ResetBaseRecord replaces the record with exactly sha. It is for clone
// creation, where a new clone is trusted.
func ResetBaseRecord(dataDir, cloneDir, sha string) error {
	if !shaRE.MatchString(sha) {
		return fmt.Errorf("base record: %q is not a commit SHA", sanitize(sha))
	}
	p, err := baseRecordPath(dataDir, cloneDir)
	if err != nil {
		return err
	}
	unlock, err := lockBases(dataDir)
	if err != nil {
		return err
	}
	defer unlock()
	return writeBaseFile(p, false, []string{sha})
}

// removeBaseRecord deletes the record of cloneDir, so the clone reads as
// unverified. A missing record is not an error.
func removeBaseRecord(dataDir, cloneDir string) error {
	p, err := baseRecordPath(dataDir, cloneDir)
	if err != nil {
		return err
	}
	unlock, err := lockBases(dataDir)
	if err != nil {
		return err
	}
	defer unlock()
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// AppendBase records sha as the newest base, unless it already is. With no
// record yet, one is created marked unverified (never a trusted record). A
// corrupt record is an error and is left alone.
func AppendBase(dataDir, cloneDir, sha string) error {
	if !shaRE.MatchString(sha) {
		return fmt.Errorf("base record: %q is not a commit SHA", sanitize(sha))
	}
	p, err := baseRecordPath(dataDir, cloneDir)
	if err != nil {
		return err
	}
	unlock, err := lockBases(dataDir)
	if err != nil {
		return err
	}
	defer unlock()
	rec, err := readBaseFile(p)
	if err != nil {
		return err
	}
	if !rec.Exists {
		rec.Unverified = true
	}
	if n := len(rec.SHAs); n > 0 && rec.SHAs[n-1] == sha {
		return nil
	}
	shas := append(rec.SHAs, sha)
	if len(shas) > maxBaseLines {
		shas = shas[len(shas)-maxBaseLines:]
	}
	return writeBaseFile(p, rec.Unverified, shas)
}

// lockBases takes an exclusive lock for a read-modify-write: the daemon
// (refresh) and the CLI (`clone fetch`) are separate processes.
func lockBases(dataDir string) (func(), error) {
	dir := filepath.Join(dataDir, baseRecordDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil // closing drops the lock
}

// writeBaseFile writes the record through a temp file in the same dir and
// renames it over p (the rename replaces, never follows, a symlink at p).
func writeBaseFile(p string, unverified bool, shas []string) error {
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "tmp-*")
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	var b strings.Builder
	if unverified {
		b.WriteString(unverifiedMarker + "\n")
	}
	for _, s := range shas {
		b.WriteString(s + "\n")
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return err
	}
	ok = true
	return nil
}
