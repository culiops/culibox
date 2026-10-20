package secretstore

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const goodPass = "correct horse battery"

func init() { scryptWorkFactor = 10 } // fast suite; TestDefaultWorkFactor covers the default

type recorder struct {
	events []Event
	err    error
}

func (r *recorder) RecordStore(e Event) error {
	if r.err != nil {
		return r.err
	}
	r.events = append(r.events, e)
	return nil
}

func (r *recorder) has(action string) bool {
	for _, e := range r.events {
		if e.Action == action {
			return true
		}
	}
	return false
}

func (r *recorder) find(action string) (Event, bool) {
	for _, e := range r.events {
		if e.Action == action {
			return e, true
		}
	}
	return Event{}, false
}

func newStore(t *testing.T, needsPass bool) (*Store, *FakeKeeper, *recorder, string) {
	t.Helper()
	dir := t.TempDir()
	k := NewFakeKeeper(needsPass)
	rec := &recorder{}
	return Open(dir, k, rec), k, rec, dir
}

func initLinux(t *testing.T) (*Store, *FakeKeeper, *recorder, string) {
	t.Helper()
	s, k, rec, dir := newStore(t, true)
	if err := s.Init([]byte(goodPass), "cli"); err != nil {
		t.Fatal(err)
	}
	return s, k, rec, dir
}

func TestInitCreatesLayout(t *testing.T) {
	s, _, rec, dir := newStore(t, true)
	if err := s.Init([]byte(goodPass), "cli"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "store"))
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("store dir: %v %v", fi, err)
	}
	rcp, err := os.ReadFile(filepath.Join(dir, "store", "recipient"))
	if err != nil || !strings.HasPrefix(string(rcp), "age1") {
		t.Fatalf("recipient: %q %v", rcp, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "store", "identity.age")); err != nil {
		t.Fatal(err)
	}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if fi, _ := d.Info(); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %o", p, fi.Mode().Perm())
		}
		b, _ := os.ReadFile(p)
		if bytes.Contains(b, []byte("AGE-SECRET-KEY-")) {
			t.Errorf("%s holds the identity in the clear", p)
		}
		return nil
	})
	if !rec.has("init") {
		t.Fatal("no init event")
	}
	if err := s.Init([]byte(goodPass), "cli"); !errors.Is(err, ErrExists) {
		t.Fatalf("second init: %v", err)
	}
}

func TestInitShortPassphrase(t *testing.T) {
	s, _, rec, dir := newStore(t, true)
	if err := s.Init([]byte("12345678901"), "cli"); err == nil {
		t.Fatal("11 chars accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "store")); !os.IsNotExist(err) {
		t.Fatalf("something was written: %v", err)
	}
	if len(rec.events) != 0 {
		t.Fatalf("events: %v", rec.events)
	}
}

func TestSetWhileLockedGetNeedsUnlock(t *testing.T) {
	s, _, _, _ := initLinux(t)
	if err := s.Lock("cli"); err != nil {
		t.Fatal(err)
	}
	v := []byte("line1\nline2 secret")
	if err := s.Set("CLAUDE_CODE_OAUTH_TOKEN", v, "cli"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("CLAUDE_CODE_OAUTH_TOKEN"); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked get: %v", err)
	}
	if err := s.Unlock([]byte(goodPass), time.Hour, "cli"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("CLAUDE_CODE_OAUTH_TOKEN")
	if err != nil || !bytes.Equal(got, v) {
		t.Fatalf("get: %q %v", got, err)
	}
}

func TestUnlockWrongPassphrase(t *testing.T) {
	s, _, rec, _ := initLinux(t)
	err := s.Unlock([]byte("wrong wrong wrong"), time.Hour, "cli")
	if !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("got %v", err)
	}
	if !rec.has("unlock_failed") {
		t.Fatal("no unlock_failed event")
	}
	if !s.Status().Locked {
		t.Fatal("not locked")
	}
}

func TestTTLExpiry(t *testing.T) {
	s, k, _, _ := initLinux(t)
	base := time.Now()
	k.SetNow(func() time.Time { return base })
	if err := s.Set("a", []byte("v"), "cli"); err != nil {
		t.Fatal(err)
	}
	if err := s.Unlock([]byte(goodPass), time.Hour, "cli"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("a"); err != nil {
		t.Fatal(err)
	}
	st := s.Status()
	if st.Locked || !st.ExpiresAt.Equal(base.Add(time.Hour)) {
		t.Fatalf("status %+v", st)
	}
	k.SetNow(func() time.Time { return base.Add(61 * time.Minute) })
	if _, err := s.Get("a"); !errors.Is(err, ErrLocked) {
		t.Fatalf("got %v", err)
	}
	if !s.Status().Locked {
		t.Fatal("status not locked")
	}
}

func TestPasswd(t *testing.T) {
	s, _, _, _ := initLinux(t)
	if err := s.Set("a", []byte("v"), "cli"); err != nil {
		t.Fatal(err)
	}
	if err := s.Passwd([]byte("nope nope nope"), []byte("a brand new phrase"), "cli"); !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("wrong old: %v", err)
	}
	if err := s.Passwd([]byte(goodPass), []byte("short"), "cli"); err == nil {
		t.Fatal("short new passphrase accepted")
	}
	if err := s.Passwd([]byte(goodPass), []byte("a brand new phrase"), "cli"); err != nil {
		t.Fatal(err)
	}
	if err := s.Unlock([]byte(goodPass), time.Hour, "cli"); !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("old still unlocks: %v", err)
	}
	if err := s.Unlock([]byte("a brand new phrase"), time.Hour, "cli"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get("a"); err != nil || string(got) != "v" {
		t.Fatalf("get: %q %v", got, err)
	}
}

func TestIDRule(t *testing.T) {
	for _, id := range []string{"a", "CLAUDE_CODE_OAUTH_TOKEN", "gh-key_1", strings.Repeat("a", 64)} {
		if !ValidID(id) {
			t.Errorf("rejected %q", id)
		}
	}
	for _, id := range []string{"", "-a", "_a", "a/b", "../x", "a.b", strings.Repeat("a", 65)} {
		if ValidID(id) {
			t.Errorf("accepted %q", id)
		}
	}
	s, _, _, _ := initLinux(t)
	if err := s.Set("../x", []byte("v"), "cli"); !errors.Is(err, ErrBadID) {
		t.Fatalf("set: %v", err)
	}
	if _, err := s.Get("a/b"); !errors.Is(err, ErrBadID) {
		t.Fatalf("get: %v", err)
	}
	if err := s.Remove("-a", "cli"); !errors.Is(err, ErrBadID) {
		t.Fatalf("remove: %v", err)
	}
}

func TestValueTooLarge(t *testing.T) {
	s, _, _, _ := initLinux(t)
	if err := s.Set("big", make([]byte, 64<<10+1), "cli"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
	if err := s.Set("max", make([]byte, 64<<10), "cli"); err != nil {
		t.Fatalf("64 KiB exactly: %v", err)
	}
}

func TestSetEmptyValueRefused(t *testing.T) {
	s, _, _, _ := initLinux(t)
	if err := s.Set("a", nil, "cli"); err == nil {
		t.Fatal("empty value accepted")
	}
}

func TestRefusesSymlinkAndLoosePerms(t *testing.T) {
	s, _, _, dir := initLinux(t)
	if err := s.Unlock([]byte(goodPass), time.Hour, "cli"); err != nil {
		t.Fatal(err)
	}
	vals := filepath.Join(dir, "store", "values")
	if err := s.Set("real", []byte("v"), "cli"); err != nil {
		t.Fatal(err)
	}
	// symlink
	if err := os.Symlink(filepath.Join(vals, "real.age"), filepath.Join(vals, "x.age")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("x"); err == nil || errors.Is(err, ErrNoSuchValue) {
		t.Fatalf("symlink: %v", err)
	}
	// loose mode
	if err := os.Chmod(filepath.Join(vals, "real.age"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("real"); err == nil {
		t.Fatal("0644 value read")
	}
}

func TestRemoveAndList(t *testing.T) {
	s, _, rec, _ := initLinux(t)
	for _, id := range []string{"zeta", "alpha", "Mid"} {
		if err := s.Set(id, []byte("v"), "cli"); err != nil {
			t.Fatal(err)
		}
	}
	l, err := s.List()
	if err != nil || len(l) != 3 {
		t.Fatalf("list: %v %v", l, err)
	}
	if l[0].ID != "Mid" || l[1].ID != "alpha" || l[2].ID != "zeta" {
		t.Fatalf("order: %v", l)
	}
	if l[0].Updated.IsZero() {
		t.Fatal("no Updated")
	}
	if s.Status().Values != 3 {
		t.Fatalf("status values %d", s.Status().Values)
	}
	if err := s.Remove("alpha", "web"); err != nil {
		t.Fatal(err)
	}
	if l, _ = s.List(); len(l) != 2 {
		t.Fatalf("after remove: %v", l)
	}
	if err := s.Remove("alpha", "web"); !errors.Is(err, ErrNoSuchValue) {
		t.Fatalf("remove missing: %v", err)
	}
	if e, ok := rec.find("value_set"); !ok || e.Detail != "zeta" || e.Actor != "cli" {
		t.Fatalf("value_set event %+v", e)
	}
	if e, ok := rec.find("value_removed"); !ok || e.Detail != "alpha" || e.Actor != "web" {
		t.Fatalf("value_removed event %+v", e)
	}
}

func TestAuditFailClosed(t *testing.T) {
	boom := errors.New("audit down")

	// Init changes nothing.
	s, k, rec, dir := newStore(t, true)
	rec.err = boom
	if err := s.Init([]byte(goodPass), "cli"); !errors.Is(err, boom) {
		t.Fatalf("init: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "store")); !os.IsNotExist(err) {
		t.Fatalf("init wrote: %v", err)
	}

	// Set / Remove change nothing.
	s, k, rec, dir = initLinux(t)
	if err := s.Set("keep", []byte("v"), "cli"); err != nil {
		t.Fatal(err)
	}
	rec.err = boom
	if err := s.Set("new", []byte("v"), "cli"); !errors.Is(err, boom) {
		t.Fatalf("set: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "store", "values", "new.age")); !os.IsNotExist(err) {
		t.Fatalf("set wrote: %v", err)
	}
	if err := s.Remove("keep", "cli"); !errors.Is(err, boom) {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "store", "values", "keep.age")); err != nil {
		t.Fatalf("remove deleted: %v", err)
	}

	// Unlock leaves the keeper empty.
	if err := s.Unlock([]byte(goodPass), time.Hour, "cli"); !errors.Is(err, boom) {
		t.Fatalf("unlock: %v", err)
	}
	if _, _, err := k.Load(); !errors.Is(err, ErrLocked) {
		t.Fatalf("keeper got an identity: %v", err)
	}

	// Passwd leaves identity.age untouched.
	before, _ := os.ReadFile(filepath.Join(dir, "store", "identity.age"))
	if err := s.Passwd([]byte(goodPass), []byte("another good phrase"), "cli"); !errors.Is(err, boom) {
		t.Fatalf("passwd: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "store", "identity.age"))
	if !bytes.Equal(before, after) {
		t.Fatal("passwd changed identity.age")
	}

	// Lock still clears the keeper.
	rec.err = nil
	if err := s.Unlock([]byte(goodPass), time.Hour, "cli"); err != nil {
		t.Fatal(err)
	}
	rec.err = boom
	if err := s.Lock("cli"); err != nil {
		t.Fatalf("lock: %v", err)
	}
	if _, _, err := k.Load(); !errors.Is(err, ErrLocked) {
		t.Fatalf("lock did not clear: %v", err)
	}
}

func TestDarwinModeNoPassphrase(t *testing.T) {
	s, _, _, dir := newStore(t, false)
	if err := s.Init(nil, "cli"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "store", "identity.age")); !os.IsNotExist(err) {
		t.Fatalf("identity.age exists: %v", err)
	}
	if err := s.Unlock(nil, time.Hour, "cli"); !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("unlock: %v", err)
	}
	if err := s.Lock("cli"); !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("lock: %v", err)
	}
	if err := s.Passwd(nil, nil, "cli"); !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("passwd: %v", err)
	}
	if err := s.Set("a", []byte("v"), "cli"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get("a"); err != nil || string(got) != "v" {
		t.Fatalf("get: %q %v", got, err)
	}
	st := s.Status()
	if !st.Initialized || st.Locked || st.PassphraseMode {
		t.Fatalf("status %+v", st)
	}
}

func TestNotInitialized(t *testing.T) {
	s, _, _, _ := newStore(t, true)
	if err := s.Set("a", []byte("v"), "cli"); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("set: %v", err)
	}
	if _, err := s.Get("a"); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("get: %v", err)
	}
	if err := s.Unlock([]byte(goodPass), time.Hour, "cli"); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("unlock: %v", err)
	}
	st := s.Status()
	if st.Initialized || st.Values != 0 || st.Platform == "" {
		t.Fatalf("status %+v", st)
	}
}

func TestNoPlaintextOnDisk(t *testing.T) {
	s, _, _, dir := initLinux(t)
	secret := []byte("distinctive-VALUE-0123456789")
	if err := s.Set("tok", secret, "cli"); err != nil {
		t.Fatal(err)
	}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := os.ReadFile(p)
		if bytes.Contains(b, secret) || bytes.Contains(b, []byte(goodPass)) {
			t.Errorf("%s contains plaintext", p)
		}
		return nil
	})
}

func TestNoTempFilesLeft(t *testing.T) {
	s, _, _, dir := initLinux(t)
	if err := s.Set("a", []byte("v1"), "cli"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("a", []byte("v2"), "cli"); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(filepath.Join(dir, "store", "values"))
	if len(ents) != 1 || ents[0].Name() != "a.age" {
		t.Fatalf("values dir: %v", ents)
	}
}

func TestDefaultWorkFactor(t *testing.T) {
	old := scryptWorkFactor
	scryptWorkFactor = 0
	defer func() { scryptWorkFactor = old }()
	s, _, _, _ := newStore(t, true)
	if err := s.Init([]byte(goodPass), "cli"); err != nil {
		t.Fatal(err)
	}
	if err := s.Unlock([]byte(goodPass), time.Hour, "cli"); err != nil {
		t.Fatal(err)
	}
}
