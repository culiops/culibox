package handback

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

// maxRecordFile bounds one record file, on write and on read. The reader
// treats the file as untrusted even though only the host writes it.
const maxRecordFile = 4 << 20

// recordIDRe is the shape of a record id and of a record file name stem.
var recordIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Record kinds.
const (
	KindClone   = "clone"
	KindInPlace = "in-place"
)

// Record is what one workspace (in-place) or one box branch (clone) has
// handed back and the user has not yet marked reviewed. Every string in it
// began as box text and was sanitized before it reached the store; the store
// sanitizes again on read.
type Record struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"` // "clone" | "in-place"
	Workspace string    `json:"workspace,omitempty"`
	CloneOf   string    `json:"clone_of,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	LastScan  time.Time `json:"last_scan"`
	Flags     []Flag    `json:"flags"`
	Inspected int       `json:"inspected"`
	Truncated string    `json:"truncated,omitempty"`
}

// Store persists records as <Dir>/<id>.json (0600) inside Dir (0700), which is
// <dataDir>/handback. The daemon and the `clone fetch` CLI are separate
// processes that both write, so every read-modify-write holds an exclusive
// flock on <Dir>/.lock. All file access goes through os.Root, so nothing
// planted in Dir can redirect it.
type Store struct{ Dir string }

// InPlaceKey identifies the record of one in-place workspace.
func InPlaceKey(workspace string) string { return "in-place\x00" + workspace }

// CloneKey identifies the record of one branch of one clone.
func CloneKey(of, branch string) string { return "clone\x00" + of + "\x00" + branch }

// RecordID is the first 32 hex characters of sha256(key). It is derived from
// the key, never taken from a client path or from file content.
func RecordID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:32]
}

func recordName(id string) string { return id + ".json" }

// Merge folds one scan into the record for key. A flag for a path replaces the
// earlier flag for that path, flags for other paths stay, LastScan becomes
// scanAt, Truncated becomes r.Truncated when that is non-empty, and Inspected
// is the latest scan's count. Kind, Workspace, CloneOf and Branch come from
// base. When the merge holds more than MaxFlags flags the strongest are kept.
func (s Store) Merge(key string, base Record, scanAt time.Time, r Report) (Record, error) {
	id := RecordID(key)
	dir, unlock, err := s.lock()
	if err != nil {
		return Record{}, err
	}
	defer unlock()
	rec, _, err := readRecordIn(dir, id)
	if err != nil {
		return Record{}, err
	}
	rec.ID = id
	rec.Kind, rec.Workspace, rec.CloneOf, rec.Branch = base.Kind, base.Workspace, base.CloneOf, base.Branch
	rec.LastScan = scanAt
	rec.Inspected = r.Inspected
	if r.Truncated != "" {
		rec.Truncated = r.Truncated
	}

	byPath := make(map[string]Flag, len(rec.Flags)+len(r.Flags))
	for _, f := range rec.Flags {
		byPath[f.Path] = f
	}
	for _, f := range r.Flags {
		f = cleanFlag(f)
		byPath[f.Path] = f
	}
	set := NewFlagSet(DefaultLimits().MaxFlags)
	for _, f := range byPath {
		set.Offer(f, nil)
	}
	rec.Flags = set.Sorted()
	rec = cleanRecord(rec, id)
	if err := writeRecordIn(dir, rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// LastScan reports when key was last scanned; false when it never was.
func (s Store) LastScan(key string) (time.Time, bool, error) {
	dir, err := s.openRead()
	if err != nil || dir == nil {
		return time.Time{}, false, err
	}
	defer dir.Close()
	rec, ok, err := readRecordIn(dir, RecordID(key))
	if err != nil || !ok {
		return time.Time{}, false, err
	}
	return rec.LastScan, true, nil
}

// Pending returns the records that still hold at least one flag, newest
// LastScan first. A record file that cannot be trusted is skipped and
// reported in the returned error (errors.Join), so one bad file does not hide
// the others: callers should use the records even when err is not nil.
func (s Store) Pending() ([]Record, error) {
	dir, err := s.openRead()
	if err != nil || dir == nil {
		return nil, err
	}
	defer dir.Close()
	d, err := dir.Open(".")
	if err != nil {
		return nil, err
	}
	names, err := d.Readdirnames(-1)
	d.Close()
	if err != nil {
		return nil, err
	}
	var out []Record
	var errs []error
	for _, n := range names {
		id, ok := strings.CutSuffix(n, ".json")
		if !ok || !recordIDRe.MatchString(id) {
			continue
		}
		rec, ok, err := readRecordIn(dir, id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if ok && len(rec.Flags) > 0 {
			out = append(out, rec)
		}
	}
	slices.SortFunc(out, func(a, b Record) int {
		if c := b.LastScan.Compare(a.LastScan); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, errors.Join(errs...)
}

// Reviewed marks a record reviewed: its flags and Truncated note are cleared,
// LastScan is kept (the next in-place scan starts there). An unknown id is a
// no-op so a double click is harmless; an id that is not 32 lower-case hex
// characters is refused.
func (s Store) Reviewed(id string) error {
	if !recordIDRe.MatchString(id) {
		return fmt.Errorf("handback: invalid record id")
	}
	dir, unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	rec, ok, err := readRecordIn(dir, id)
	if err != nil || !ok {
		return err
	}
	rec.Flags = []Flag{}
	rec.Truncated = ""
	return writeRecordIn(dir, rec)
}

// openRead opens the store dir as a Root; nil, nil when it does not exist yet.
func (s Store) openRead() (*os.Root, error) {
	root, err := os.OpenRoot(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return root, err
}

// lock creates the store dir if needed, opens it as a Root and takes an
// exclusive flock on .lock (modelled on clone.lockReturnState). Closing the
// lock file drops the lock.
func (s Store) lock() (*os.Root, func(), error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, nil, err
	}
	dir, err := os.OpenRoot(s.Dir)
	if err != nil {
		return nil, nil, err
	}
	f, err := dir.OpenFile(".lock", os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		dir.Close()
		return nil, nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		dir.Close()
		return nil, nil, err
	}
	return dir, func() { f.Close(); dir.Close() }, nil
}

// readRecordIn reads and cleans <id>.json. A missing file is (zero, false,
// nil); a symlink, a non-regular file, an oversized file or bad JSON is an
// error and is never followed or trusted.
func readRecordIn(dir *os.Root, id string) (Record, bool, error) {
	name := recordName(id)
	fi, err := dir.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	if !fi.Mode().IsRegular() {
		return Record{}, false, fmt.Errorf("handback record %s is not a regular file", name)
	}
	// O_NONBLOCK: if the name was swapped for a FIFO after the Lstat, the
	// open must not hang; the fstat below then refuses it.
	f, err := dir.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return Record{}, false, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return Record{}, false, err
	} else if !fi.Mode().IsRegular() {
		return Record{}, false, fmt.Errorf("handback record %s is not a regular file", name)
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxRecordFile+1))
	if err != nil {
		return Record{}, false, err
	}
	if len(raw) > maxRecordFile {
		return Record{}, false, fmt.Errorf("handback record %s is larger than %d bytes", name, maxRecordFile)
	}
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return Record{}, false, fmt.Errorf("handback record %s: %w", name, err)
	}
	return cleanRecord(rec, id), true, nil
}

func cleanFlag(f Flag) Flag {
	f.Path = SanitizePath(f.Path)
	f.Detail = SanitizePath(f.Detail)
	f.Target = SanitizePath(f.Target)
	f.SHA256 = SanitizePath(f.SHA256)
	f.HashNote = SanitizePath(f.HashNote)
	classes := make([]Class, len(f.Classes))
	for i, c := range f.Classes {
		classes[i] = Class(SanitizePath(string(c)))
	}
	f.Classes = classes
	return f
}

// cleanRecord makes a record safe to hand on: the id is the file's, every
// string is sanitized again and the flags are capped.
func cleanRecord(rec Record, id string) Record {
	rec.ID = id
	rec.Kind = SanitizePath(rec.Kind)
	rec.Workspace = SanitizePath(rec.Workspace)
	rec.CloneOf = SanitizePath(rec.CloneOf)
	rec.Branch = SanitizePath(rec.Branch)
	rec.Truncated = SanitizePath(rec.Truncated)
	if max := DefaultLimits().MaxFlags; len(rec.Flags) > max {
		rec.Flags = rec.Flags[:max]
	}
	flags := make([]Flag, len(rec.Flags))
	for i, f := range rec.Flags {
		flags[i] = cleanFlag(f)
	}
	rec.Flags = flags
	return rec
}

// writeRecordIn writes through a temp file in the same dir and renames it over
// the record (the rename replaces, never follows, a symlink at the name).
func writeRecordIn(dir *os.Root, rec Record) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	// 5,000 flags of 1,024-rune strings can exceed the read cap. Drop the
	// weakest (the flags are sorted strongest first) rather than write a
	// file the reader would refuse, and say so.
	if len(data) > maxRecordFile && len(rec.Flags) > 0 {
		total := len(rec.Flags)
		keep := total
		for len(data) > maxRecordFile && keep > 0 {
			keep = min(keep-1, keep*(maxRecordFile-4096)/len(data))
			rec.Flags = rec.Flags[:max(keep, 0)]
			note := fmt.Sprintf("record size cap reached; %d weaker flagged files are not listed", total-len(rec.Flags))
			if rec.Truncated == "" {
				rec.Truncated = note
			} else {
				rec.Truncated = SanitizePath(rec.Truncated + "; " + note)
			}
			if data, err = json.Marshal(rec); err != nil {
				return err
			}
		}
	}
	if len(data) > maxRecordFile {
		return fmt.Errorf("handback record is larger than %d bytes", maxRecordFile)
	}
	name := recordName(rec.ID)
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	tmpName := name + ".tmp-" + hex.EncodeToString(rnd[:])
	tmp, err := dir.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			dir.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
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
	if err := dir.Rename(tmpName, name); err != nil {
		return err
	}
	ok = true
	return nil
}
