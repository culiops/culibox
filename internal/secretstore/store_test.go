package secretstore

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
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
	s, _, _, dir := newStore(t, true)
	if err := s.Init([]byte(goodPass), "cli"); err != nil {
		t.Fatal(err)
	}
	if err := s.Unlock([]byte(goodPass), time.Hour, "cli"); err != nil {
		t.Fatal(err)
	}
	if got := scryptLogN(t, filepath.Join(dir, "store", "identity.age")); got < 18 {
		t.Fatalf("scrypt logN %d, want >= 18 (age default)", got)
	}
}

// scryptLogN reads the work factor from the scrypt stanza of an age file:
// "-> scrypt <salt> <logN>".
func scryptLogN(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) == 4 && f[0] == "->" && f[1] == "scrypt" {
			n, err := strconv.Atoi(f[3])
			if err != nil {
				t.Fatalf("bad logN %q", f[3])
			}
			return n
		}
		if strings.HasPrefix(line, "---") {
			break
		}
	}
	t.Fatal("no scrypt stanza in identity.age")
	return 0
}

func TestReinitDoesNotInheritUnlock(t *testing.T) {
	s, k, _, dir := initLinux(t)
	if err := s.Unlock([]byte(goodPass), time.Hour, "cli"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "store")); err != nil {
		t.Fatal(err)
	}
	if err := s.Init([]byte(goodPass), "cli"); err != nil {
		t.Fatal(err)
	}
	if !s.Status().Locked {
		t.Fatal("re-init inherited the previous unlocked identity")
	}
	if _, _, err := k.Load(); !errors.Is(err, ErrLocked) {
		t.Fatalf("keeper still holds an identity: %v", err)
	}
}

func TestKeeperIdentityMismatchIsLocked(t *testing.T) {
	s, k, _, _ := initLinux(t)
	if err := s.Set("a", []byte("v"), "cli"); err != nil {
		t.Fatal(err)
	}
	other, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Save(other.String(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("a"); !errors.Is(err, ErrLocked) {
		t.Fatalf("get with a foreign identity: %v", err)
	}
	if !s.Status().Locked {
		t.Fatal("status unlocked with a foreign identity")
	}
}

func TestUnlockMismatchedPair(t *testing.T) {
	s, k, rec, dir := initLinux(t)
	// A second store with the same passphrase: its identity.age decrypts fine
	// but belongs to a different recipient.
	s2, _, _, dir2 := initLinux(t)
	_ = s2
	b, err := os.ReadFile(filepath.Join(dir2, "store", "identity.age"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "store", "identity.age"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Unlock([]byte(goodPass), time.Hour, "cli"); err == nil {
		t.Fatal("unlock accepted an identity that does not match the recipient")
	}
	if _, _, err := k.Load(); !errors.Is(err, ErrLocked) {
		t.Fatalf("something was saved: %v", err)
	}
	if rec.has("unlocked") {
		t.Fatal("unlocked event recorded")
	}
	if err := s.Passwd([]byte(goodPass), []byte("another good phrase"), "cli"); err == nil {
		t.Fatal("passwd accepted a mismatched pair")
	}
}

// On a case-insensitive filesystem (macOS APFS by default) "CaseTest" and
// "casetest" are the same file, so a second set would silently replace the first
// value. The store refuses it on every platform so behaviour does not differ:
// the check reads the ids that exist, not the filesystem's opinion of them.
func TestSetRefusesAnIdThatDiffersOnlyInCase(t *testing.T) {
	s, _, rec, _ := newStore(t, false)
	if err := s.Init(nil, "cli"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("CaseTest", []byte("first"), "cli"); err != nil {
		t.Fatal(err)
	}
	err := s.Set("casetest", []byte("second"), "cli")
	if !errors.Is(err, ErrCaseCollision) {
		t.Fatalf("case variant: %v", err)
	}
	want := `"casetest" collides with existing "CaseTest" (ids are case-insensitive on macOS); use the exact id`
	if err.Error() != want {
		t.Errorf("message %q, want %q", err, want)
	}
	if got, err := s.Get("CaseTest"); err != nil || string(got) != "first" {
		t.Fatalf("the existing value must be untouched: %q %v", got, err)
	}
	if l, _ := s.List(); len(l) != 1 || l[0].ID != "CaseTest" {
		t.Fatalf("no second value may be written: %v", l)
	}
	for _, e := range rec.events {
		if e.Action == "value_set" && e.Detail == "casetest" {
			t.Errorf("a refused set must not be audited as a write: %+v", e)
		}
	}
}

// Case twins stored before the check existed (Linux allowed GH and gh) stay
// updatable by their exact ids; only a new case variant is refused.
func TestSetUpdatesALegacyCaseTwinByItsExactId(t *testing.T) {
	s, _, _, dir := newStore(t, false)
	if err := s.Init(nil, "cli"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("GH", []byte("upper"), "cli"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, storeDirName, valuePath("GH")))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, storeDirName, valuePath("gh")), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.List(); len(ids) != 2 {
		t.Skipf("this filesystem folds case, so twins cannot exist: %v", ids)
	}
	for _, id := range []string{"gh", "GH"} {
		if err := s.Set(id, []byte("rotated-"+id), "cli"); err != nil {
			t.Fatalf("set %s next to its twin: %v", id, err)
		}
		if got, err := s.Get(id); err != nil || string(got) != "rotated-"+id {
			t.Fatalf("get %s: %q %v", id, got, err)
		}
	}
	if err := s.Set("Gh", []byte("x"), "cli"); !errors.Is(err, ErrCaseCollision) {
		t.Fatalf("a new case variant must still be refused: %v", err)
	}
}

func TestSetSameIdUpdatesAndDistinctIdsCoexist(t *testing.T) {
	s, _, _, _ := newStore(t, false)
	if err := s.Init(nil, "cli"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ id, val string }{
		{"CaseTest", "one"}, {"CaseTest", "two"}, {"case-test", "x"}, {"other", "y"},
	} {
		if err := s.Set(c.id, []byte(c.val), "cli"); err != nil {
			t.Fatalf("set %s: %v", c.id, err)
		}
	}
	if got, err := s.Get("CaseTest"); err != nil || string(got) != "two" {
		t.Fatalf("exact id must update: %q %v", got, err)
	}
	if l, _ := s.List(); len(l) != 3 {
		t.Fatalf("distinct ids coexist: %v", l)
	}
}
