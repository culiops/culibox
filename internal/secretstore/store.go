// Package secretstore is an age-encrypted value store in the agent-box data
// dir. Values are encrypted to a public recipient, so they can be written
// while the store is locked; reading needs the master identity, which a Keeper
// holds outside this process. Nothing here caches a value, the identity or a
// passphrase, and no error message carries one.
package secretstore

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"filippo.io/age"
)

const (
	// MaxValueBytes bounds one stored value (same limit as a file reference).
	MaxValueBytes = 64 << 10
	// MinPassphraseChars is the Linux passphrase minimum.
	MinPassphraseChars = 12

	maxCiphertext = MaxValueBytes + 4<<10
	maxSmallFile  = 64 << 10

	storeDirName = "store"
	valuesDir    = "values"
	recipientFn  = "recipient"
	identityFn   = "identity.age"
	valueExt     = ".age"
)

// Status is what the CLI, doctor and web show. Locked is only meaningful once
// Initialized.
type Status struct {
	Initialized    bool
	Locked         bool
	ExpiresAt      time.Time
	Values         int
	Platform       string
	PassphraseMode bool
}

// Entry names one stored value. Updated is the file's modification time.
type Entry struct {
	ID      string
	Updated time.Time
}

// Store is the encrypted value store under <dataDir>/store.
type Store struct {
	dir string // <dataDir>/store
	k   Keeper
	rec Recorder
	mu  sync.Mutex
}

// Open returns a Store over dataDir. It touches nothing on disk. A nil
// Recorder records nothing.
func Open(dataDir string, k Keeper, rec Recorder) *Store {
	return &Store{dir: filepath.Join(dataDir, storeDirName), k: k, rec: rec}
}

// ValidID reports whether id matches ^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$.
func ValidID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || (c != '_' && c != '-')) {
			return false
		}
	}
	return true
}

func (s *Store) record(action, actor, detail string) error {
	if s.rec == nil {
		return nil
	}
	return s.rec.RecordStore(Event{Action: action, Actor: actor, Detail: detail})
}

// Init creates the store. On Linux it needs a passphrase of at least 12
// characters and leaves the store locked; on macOS the passphrase is ignored
// and the identity goes to the keeper.
func (s *Store) Init(passphrase []byte, actor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Lstat(filepath.Join(s.dir, recipientFn)); err == nil {
		return ErrExists
	}
	needsPass := s.k.NeedsPassphrase()
	if needsPass {
		if err := checkNewPassphrase(passphrase); err != nil {
			return err
		}
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return fmt.Errorf("generate identity: %w", err)
	}
	var wrapped []byte
	if needsPass {
		if wrapped, err = wrapIdentity(id.String(), passphrase); err != nil {
			return err
		}
	}
	if err := s.record("init", actor, ""); err != nil {
		return err
	}

	_, statErr := os.Lstat(s.dir)
	created := os.IsNotExist(statErr)
	root, err := s.openRoot(true)
	if err != nil {
		return err
	}
	defer root.Close()
	fail := func(err error) error {
		root.Close()
		if created {
			_ = os.RemoveAll(s.dir)
		}
		return err
	}
	if err := root.Mkdir(valuesDir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return fail(fmt.Errorf("create values dir: %w", unwrapPath(err)))
	}
	if needsPass {
		if err := writeAtomic(root, identityFn, wrapped); err != nil {
			return fail(err)
		}
	} else if err := s.k.Save(id.String(), 0); err != nil {
		return fail(fmt.Errorf("save master key: %w", err))
	}
	if err := writeAtomic(root, recipientFn, []byte(id.Recipient().String()+"\n")); err != nil {
		if !needsPass {
			_ = s.k.Clear()
		}
		return fail(err)
	}
	if needsPass {
		// A previous store's unlocked identity must not carry over to this one.
		_ = s.k.Clear()
	}
	return nil
}

// Status reports the store's state without touching any value.
func (s *Store) Status() Status {
	st := Status{Platform: runtime.GOOS, PassphraseMode: s.k.NeedsPassphrase()}
	fi, err := os.Lstat(filepath.Join(s.dir, recipientFn))
	if err != nil || !fi.Mode().IsRegular() {
		return st
	}
	st.Initialized = true
	if _, exp, err := s.keeperIdentity(); err != nil {
		st.Locked = true
	} else {
		st.ExpiresAt = exp
	}
	if ents, err := os.ReadDir(filepath.Join(s.dir, valuesDir)); err == nil {
		for _, e := range ents {
			if e.Type().IsRegular() && isValueFile(e.Name()) {
				st.Values++
			}
		}
	}
	return st
}

func isValueFile(name string) bool {
	return strings.HasSuffix(name, valueExt) && ValidID(strings.TrimSuffix(name, valueExt))
}

// Set encrypts value to the store's public recipient and writes it atomically.
// It works while the store is locked. An id that differs from an existing one
// only in case is refused (CaseCollisionError); the exact id updates in place.
func (s *Store) Set(id string, value []byte, actor string) error {
	if !ValidID(id) {
		return ErrBadID
	}
	if len(value) > MaxValueBytes {
		return ErrTooLarge
	}
	if len(value) == 0 {
		return errors.New("value is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.openRoot(false)
	if err != nil {
		return err
	}
	defer root.Close()
	rcpt, err := readRecipient(root)
	if err != nil {
		return err
	}
	// Before anything is encrypted or audited: a refused set writes nothing.
	existing, err := listValues(root)
	if err != nil {
		return err
	}
	for _, e := range existing {
		if e.ID != id && strings.EqualFold(e.ID, id) {
			return &CaseCollisionError{ID: id, Existing: e.ID}
		}
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, rcpt)
	if err != nil {
		return errors.New("encrypt value failed")
	}
	if _, err := w.Write(value); err != nil {
		return errors.New("encrypt value failed")
	}
	if err := w.Close(); err != nil {
		return errors.New("encrypt value failed")
	}
	if err := s.record("value_set", actor, id); err != nil {
		return err
	}
	return writeAtomic(root, valuePath(id), buf.Bytes())
}

// Remove deletes a stored value.
func (s *Store) Remove(id, actor string) error {
	if !ValidID(id) {
		return ErrBadID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.openRoot(false)
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err := root.Lstat(valuePath(id)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNoSuchValue
		}
		return fmt.Errorf("value %q: %w", id, unwrapPath(err))
	}
	if err := s.record("value_removed", actor, id); err != nil {
		return err
	}
	if err := root.Remove(valuePath(id)); err != nil {
		return fmt.Errorf("remove value %q: %w", id, unwrapPath(err))
	}
	return nil
}

// List returns the stored ids, sorted. It works while the store is locked.
func (s *Store) List() ([]Entry, error) {
	root, err := s.openRoot(false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return listValues(root)
}

// listValues is List over an already open root, so Set can check the ids that
// exist under the lock it already holds.
func listValues(root *os.Root) ([]Entry, error) {
	d, err := root.Open(valuesDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotInitialized
		}
		return nil, fmt.Errorf("list values: %w", unwrapPath(err))
	}
	defer d.Close()
	ents, err := d.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("list values: %w", unwrapPath(err))
	}
	out := []Entry{}
	for _, e := range ents {
		if !e.Type().IsRegular() || !isValueFile(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Entry{ID: strings.TrimSuffix(e.Name(), valueExt), Updated: fi.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Get decrypts one value and returns its raw bytes. The file is checked
// before the identity is fetched, and neither is kept.
func (s *Store) Get(id string) ([]byte, error) {
	if !ValidID(id) {
		return nil, ErrBadID
	}
	root, err := s.openRoot(false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if _, err := readRecipient(root); err != nil {
		return nil, err
	}
	ct, err := readSafe(root, valuePath(id), maxCiphertext)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNoSuchValue
		}
		return nil, err
	}
	x, _, err := s.keeperIdentityFor(root)
	if err != nil {
		return nil, err
	}
	r, err := age.Decrypt(bytes.NewReader(ct), x)
	if err != nil {
		return nil, fmt.Errorf("decrypt value %q failed", id)
	}
	val, err := io.ReadAll(io.LimitReader(r, MaxValueBytes+1))
	if err != nil || len(val) > MaxValueBytes {
		return nil, fmt.Errorf("decrypt value %q failed", id)
	}
	return val, nil
}

// Unlock verifies the passphrase against identity.age and hands the identity
// to the keeper for ttl. Linux only.
func (s *Store) Unlock(passphrase []byte, ttl time.Duration, actor string) error {
	if !s.k.NeedsPassphrase() {
		return ErrNotApplicable
	}
	if ttl <= 0 {
		return errors.New("unlock ttl must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ident, err := s.openIdentity(passphrase)
	if err != nil {
		if errors.Is(err, ErrBadPassphrase) {
			_ = s.record("unlock_failed", actor, "")
		}
		return err
	}
	if err := s.record("unlocked", actor, "ttl="+ttl.String()); err != nil {
		return err
	}
	if err := s.k.Save(ident, ttl); err != nil {
		return fmt.Errorf("save master key: %w", err)
	}
	return nil
}

// Lock clears the keeper. Auditing is best-effort so a lock is never blocked.
func (s *Store) Lock(actor string) error {
	if !s.k.NeedsPassphrase() {
		return ErrNotApplicable
	}
	_ = s.record("locked", actor, "")
	return s.k.Clear()
}

// Passwd re-encrypts identity.age under a new passphrase. Values and the
// keeper are untouched. Linux only.
func (s *Store) Passwd(oldPass, newPass []byte, actor string) error {
	if !s.k.NeedsPassphrase() {
		return ErrNotApplicable
	}
	if err := checkNewPassphrase(newPass); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ident, err := s.openIdentity(oldPass)
	if err != nil {
		return err
	}
	wrapped, err := wrapIdentity(ident, newPass)
	if err != nil {
		return err
	}
	if err := s.record("passwd_changed", actor, ""); err != nil {
		return err
	}
	root, err := s.openRoot(false)
	if err != nil {
		return err
	}
	defer root.Close()
	return writeAtomic(root, identityFn, wrapped)
}

// openIdentity decrypts identity.age with the passphrase.
func (s *Store) openIdentity(passphrase []byte) (string, error) {
	root, err := s.openRoot(false)
	if err != nil {
		return "", err
	}
	defer root.Close()
	rcpt, err := readRecipient(root)
	if err != nil {
		return "", err
	}
	ct, err := readSafe(root, identityFn, maxSmallFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", ErrNotInitialized
		}
		return "", err
	}
	si, err := age.NewScryptIdentity(string(passphrase))
	if err != nil {
		return "", ErrBadPassphrase
	}
	r, err := age.Decrypt(bytes.NewReader(ct), si)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return "", ErrBadPassphrase
		}
		return "", errors.New("identity file is unreadable")
	}
	b, err := io.ReadAll(io.LimitReader(r, 4<<10))
	if err != nil {
		return "", errors.New("identity file is unreadable")
	}
	ident := strings.TrimSpace(string(b))
	x, err := age.ParseX25519Identity(ident)
	if err != nil {
		return "", errors.New("identity file is unreadable")
	}
	if x.Recipient().String() != rcpt.String() {
		return "", errors.New("identity.age does not match the store recipient")
	}
	return ident, nil
}

// keeperIdentity returns the keeper's identity when it belongs to this store.
func (s *Store) keeperIdentity() (*age.X25519Identity, time.Time, error) {
	root, err := s.openRoot(false)
	if err != nil {
		return nil, time.Time{}, ErrLocked
	}
	defer root.Close()
	return s.keeperIdentityFor(root)
}

// keeperIdentityFor loads the identity from the keeper and checks it against
// the recipient file. An identity that is not this store's (a stale entry from
// an earlier store in the same data dir) counts as locked.
func (s *Store) keeperIdentityFor(root *os.Root) (*age.X25519Identity, time.Time, error) {
	ident, exp, err := s.k.Load()
	if err != nil {
		return nil, time.Time{}, err
	}
	x, err := age.ParseX25519Identity(strings.TrimSpace(ident))
	if err != nil {
		return nil, time.Time{}, errors.New("master key is malformed")
	}
	rcpt, err := readRecipient(root)
	if err != nil {
		return nil, time.Time{}, err
	}
	if x.Recipient().String() != rcpt.String() {
		return nil, time.Time{}, ErrLocked
	}
	return x, exp, nil
}

func checkNewPassphrase(p []byte) error {
	if utf8.RuneCount(p) < MinPassphraseChars {
		return fmt.Errorf("passphrase must be at least %d characters", MinPassphraseChars)
	}
	return nil
}

// scryptWorkFactor overrides age's default scrypt work factor when > 0. Only
// tests set it, to keep the suite fast; production uses age's default.
var scryptWorkFactor int

func wrapIdentity(identity string, passphrase []byte) ([]byte, error) {
	r, err := age.NewScryptRecipient(string(passphrase))
	if err != nil {
		return nil, errors.New("invalid passphrase")
	}
	if scryptWorkFactor > 0 {
		r.SetWorkFactor(scryptWorkFactor)
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, r)
	if err != nil {
		return nil, errors.New("encrypt identity failed")
	}
	if _, err := io.WriteString(w, identity+"\n"); err != nil {
		return nil, errors.New("encrypt identity failed")
	}
	if err := w.Close(); err != nil {
		return nil, errors.New("encrypt identity failed")
	}
	return buf.Bytes(), nil
}

func valuePath(id string) string { return valuesDir + "/" + id + valueExt }

// openRoot opens the store dir as an os.Root after checking it is a real
// directory owned by this user and closed to group and others. create makes
// it (0700) when missing.
func (s *Store) openRoot(create bool) (*os.Root, error) {
	if create {
		if err := os.MkdirAll(s.dir, 0o700); err != nil {
			return nil, fmt.Errorf("create store dir: %w", unwrapPath(err))
		}
	}
	fi, err := os.Lstat(s.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotInitialized
		}
		return nil, fmt.Errorf("store dir: %w", unwrapPath(err))
	}
	if !fi.IsDir() {
		return nil, errors.New("store dir is not a directory (symlinks are refused)")
	}
	if err := checkOwnerMode(fi, true); err != nil {
		return nil, fmt.Errorf("store dir: %w", err)
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return nil, fmt.Errorf("open store dir: %w", unwrapPath(err))
	}
	return root, nil
}

func readRecipient(root *os.Root) (*age.X25519Recipient, error) {
	b, err := readSafe(root, recipientFn, maxSmallFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotInitialized
		}
		return nil, err
	}
	r, err := age.ParseX25519Recipient(strings.TrimSpace(string(b)))
	if err != nil {
		return nil, errors.New("recipient file is malformed")
	}
	return r, nil
}

// checkOwnerMode mirrors credref's file rule: owned by the current user, no
// group or other bits.
func checkOwnerMode(fi os.FileInfo, dir bool) error {
	if !dir && !fi.Mode().IsRegular() {
		return errors.New("not a regular file (symlinks are refused)")
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return errors.New("not owned by the current user")
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("mode %04o lets group or others in; use 0600/0700", fi.Mode().Perm())
	}
	return nil
}

// readSafe reads a store file through root: regular file, never a symlink,
// owned by us, mode 0600, re-checked after open. A missing file returns
// fs.ErrNotExist. Errors name the relative path, never contents.
func readSafe(root *os.Root, rel string, max int64) ([]byte, error) {
	fi, err := root.Lstat(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fs.ErrNotExist
		}
		return nil, fmt.Errorf("%s: %w", rel, unwrapPath(err))
	}
	if err := checkOwnerMode(fi, false); err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel, unwrapPath(err))
	}
	defer f.Close()
	fi2, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel, unwrapPath(err))
	}
	if err := checkOwnerMode(fi2, false); err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, fmt.Errorf("%s: read failed", rel)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s: file too large", rel)
	}
	return data, nil
}

// writeAtomic writes data to rel (0600) via a temp file in the same dir,
// fsync, then rename.
func writeAtomic(root *os.Root, rel string, data []byte) error {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	dir := filepath.Dir(rel)
	tmp := filepath.Join(dir, ".tmp-"+hex.EncodeToString(rnd[:]))
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("write %s: %w", rel, unwrapPath(err))
	}
	cleanup := func() { _ = root.Remove(tmp) }
	if _, err := f.Write(data); err != nil {
		f.Close()
		cleanup()
		return fmt.Errorf("write %s: %w", rel, unwrapPath(err))
	}
	if err := f.Sync(); err != nil {
		f.Close()
		cleanup()
		return fmt.Errorf("write %s: %w", rel, unwrapPath(err))
	}
	if err := f.Close(); err != nil {
		cleanup()
		return fmt.Errorf("write %s: %w", rel, unwrapPath(err))
	}
	if err := root.Rename(tmp, rel); err != nil {
		cleanup()
		return fmt.Errorf("write %s: %w", rel, unwrapPath(err))
	}
	if d, err := root.Open(dir); err == nil { // best-effort directory fsync
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// unwrapPath drops the absolute path an *fs.PathError carries.
func unwrapPath(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Err
	}
	return err
}
