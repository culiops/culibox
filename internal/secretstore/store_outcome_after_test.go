package secretstore

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Lock, Passwd, Set and Remove write their row before they act, like Unlock
// and Init, so an audit failure still refuses the action. When the action then
// fails, the trail needs a second row with a fixed reason class (#332): the
// same shape as unlock_failed and init_failed (#288).

// The locked row is written before the keeper is asked, and a lock never waits
// on the audit. When the key store then refuses to clear the kept unlock the
// store is still unlocked, so the trail needs a lock_failed row.
func TestLockKeeperClearFailureIsAudited(t *testing.T) {
	s, k, rec, _ := initLinux(t)
	if err := s.Unlock([]byte(goodPass), time.Hour, "cli"); err != nil {
		t.Fatal(err)
	}
	k.FailClear(errors.New("kernel keyring revoke: /run/user/1000 operation not permitted"))
	rec.events = nil
	if err := s.Lock("web"); err == nil {
		t.Fatal("lock succeeded although the keeper refused")
	}
	if s.Status().Locked {
		t.Fatal("store is locked although the keeper kept the unlock")
	}
	if want := []string{"locked", "lock_failed"}; !slices.Equal(actionsOf(rec), want) {
		t.Fatalf("audit trail %v, want %v", actionsOf(rec), want)
	}
	e, _ := rec.find("lock_failed")
	if e.Actor != "web" || e.Detail != lockNotCleared {
		t.Fatalf("lock_failed row %+v", e)
	}
	if strings.Contains(e.Detail, "revoke") || strings.Contains(e.Detail, "/run") {
		t.Fatalf("the row carries the raw keeper error: %q", e.Detail)
	}

	// A lock that works leaves no failure row.
	k.FailClear(nil)
	rec.events = nil
	if err := s.Lock("web"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"locked"}; !slices.Equal(actionsOf(rec), want) {
		t.Fatalf("trail after a good lock: %v, want %v", actionsOf(rec), want)
	}
}

// The failure row is best effort: the caller sees the keeper's error.
func TestLockKeeperClearFailureSurvivesAnAuditError(t *testing.T) {
	dir := t.TempDir()
	k := NewFakeKeeper(true)
	rec := &failRowRecorder{refuse: map[string]bool{}}
	s := Open(dir, k, rec)
	if err := s.Init([]byte(goodPass), "cli"); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("keeper refused")
	k.FailClear(boom)
	rec.refuse["lock_failed"] = true
	if err := s.Lock("cli"); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}

// replaceWithDir swaps the file at path for a non-empty directory: a rename
// onto it and a remove of it both fail, on Linux and on macOS, whoever runs
// the test.
func replaceWithDir(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// hookRecorder runs after, if set, once the row for action is accepted: the
// step that follows a before-row then meets whatever the test staged.
type hookRecorder struct {
	recorder
	action string
	after  func()
}

func (r *hookRecorder) RecordStore(e Event) error {
	if err := r.recorder.RecordStore(e); err != nil {
		return err
	}
	if e.Action == r.action && r.after != nil {
		r.after()
	}
	return nil
}

// The passwd_changed row is written before identity.age is rewritten, so a
// failed rewrite needs a passwd_failed row.
func TestPasswdWriteFailureIsAudited(t *testing.T) {
	dir := t.TempDir()
	k := NewFakeKeeper(true)
	rec := &hookRecorder{action: "passwd_changed"}
	s := Open(dir, k, rec)
	if err := s.Init([]byte(goodPass), "cli"); err != nil {
		t.Fatal(err)
	}
	rec.after = func() { replaceWithDir(t, filepath.Join(dir, "store", "identity.age")) }
	rec.events = nil
	if err := s.Passwd([]byte(goodPass), []byte("another-long-passphrase"), "web"); err == nil {
		t.Fatal("passwd succeeded although identity.age could not be written")
	}
	if want := []string{"passwd_changed", "passwd_failed"}; !slices.Equal(actionsOf(&rec.recorder), want) {
		t.Fatalf("audit trail %v, want %v", actionsOf(&rec.recorder), want)
	}
	e, _ := rec.find("passwd_failed")
	if e.Actor != "web" || e.Detail != passwdNotChanged {
		t.Fatalf("passwd_failed row %+v", e)
	}
	if strings.Contains(e.Detail, dir) || strings.Contains(e.Detail, "identity.age") {
		t.Fatalf("the row carries a path: %q", e.Detail)
	}
}

// A good passwd, and one refused before its row (wrong old passphrase), leave
// no failure row.
func TestPasswdWritesNoFailureRowWhenRefusedOrGood(t *testing.T) {
	s, _, rec, _ := initLinux(t)
	rec.events = nil
	if err := s.Passwd([]byte("not the passphrase"), []byte("another-long-passphrase"), "cli"); err == nil {
		t.Fatal("wrong old passphrase accepted")
	}
	if rec.has("passwd_changed") || rec.has("passwd_failed") {
		t.Fatalf("a refused passwd wrote %v", actionsOf(rec))
	}
	rec.events = nil
	if err := s.Passwd([]byte(goodPass), []byte("another-long-passphrase"), "cli"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"passwd_changed"}; !slices.Equal(actionsOf(rec), want) {
		t.Fatalf("audit trail %v, want %v", actionsOf(rec), want)
	}
}

// The value_set row is written before the value file is, so a failed write
// needs a value_set_failed row, with neither a path nor the value in it.
func TestSetWriteFailureIsAudited(t *testing.T) {
	s, _, rec, dir := initLinux(t)
	replaceWithDir(t, filepath.Join(dir, "store", "values", "API_KEY.age"))
	rec.events = nil
	if err := s.Set("API_KEY", []byte("s3cret-value"), "web"); err == nil {
		t.Fatal("set succeeded although the value could not be written")
	}
	if want := []string{"value_set", "value_set_failed"}; !slices.Equal(actionsOf(rec), want) {
		t.Fatalf("audit trail %v, want %v", actionsOf(rec), want)
	}
	e, _ := rec.find("value_set_failed")
	if e.Actor != "web" || e.Detail != "API_KEY" {
		t.Fatalf("value_set_failed row %+v", e)
	}
	if strings.Contains(e.Detail, dir) || strings.Contains(e.Detail, "s3cret") {
		t.Fatalf("the row carries a path or the value: %q", e.Detail)
	}
}

// A set refused before its row writes nothing, and a good one writes no
// failure row.
func TestSetWritesNoFailureRowWhenRefusedOrGood(t *testing.T) {
	s, _, rec, _ := initLinux(t)
	rec.events = nil
	if err := s.Set("API_KEY", nil, "web"); err == nil {
		t.Fatal("empty value accepted")
	}
	if len(rec.events) != 0 {
		t.Fatalf("a refused set wrote %v", actionsOf(rec))
	}
	if err := s.Set("API_KEY", []byte("v"), "web"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"value_set"}; !slices.Equal(actionsOf(rec), want) {
		t.Fatalf("audit trail %v, want %v", actionsOf(rec), want)
	}
}

// The value_removed row is written before the file is removed, so a failed
// remove needs a value_removed_failed row.
func TestRemoveFailureIsAudited(t *testing.T) {
	s, _, rec, dir := initLinux(t)
	if err := s.Set("API_KEY", []byte("v"), "web"); err != nil {
		t.Fatal(err)
	}
	replaceWithDir(t, filepath.Join(dir, "store", "values", "API_KEY.age"))
	rec.events = nil
	if err := s.Remove("API_KEY", "web"); err == nil {
		t.Fatal("remove succeeded although the value could not be removed")
	}
	if want := []string{"value_removed", "value_removed_failed"}; !slices.Equal(actionsOf(rec), want) {
		t.Fatalf("audit trail %v, want %v", actionsOf(rec), want)
	}
	e, _ := rec.find("value_removed_failed")
	if e.Actor != "web" || e.Detail != "API_KEY" {
		t.Fatalf("value_removed_failed row %+v", e)
	}
	if strings.Contains(e.Detail, dir) {
		t.Fatalf("the row carries a path: %q", e.Detail)
	}
}

// Removing what is not there writes no row at all, and a good remove writes
// no failure row.
func TestRemoveWritesNoFailureRowWhenMissingOrGood(t *testing.T) {
	s, _, rec, _ := initLinux(t)
	if err := s.Set("API_KEY", []byte("v"), "web"); err != nil {
		t.Fatal(err)
	}
	rec.events = nil
	if err := s.Remove("OTHER", "web"); !errors.Is(err, ErrNoSuchValue) {
		t.Fatalf("got %v", err)
	}
	if len(rec.events) != 0 {
		t.Fatalf("a refused remove wrote %v", actionsOf(rec))
	}
	if err := s.Remove("API_KEY", "web"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"value_removed"}; !slices.Equal(actionsOf(rec), want) {
		t.Fatalf("audit trail %v, want %v", actionsOf(rec), want)
	}
}

// The audit-before-act refusal still holds: when the before row cannot be
// written, a set, remove or passwd changes nothing and no failure row follows.
func TestActionsStillRefusedWhenTheBeforeRowFails(t *testing.T) {
	s, _, rec, dir := initLinux(t)
	if err := s.Set("API_KEY", []byte("v"), "web"); err != nil {
		t.Fatal(err)
	}
	rec.err = errors.New("audit down")
	if err := s.Set("OTHER", []byte("v"), "web"); err == nil {
		t.Fatal("set went ahead without its audit row")
	}
	if _, err := os.Stat(filepath.Join(dir, "store", "values", "OTHER.age")); !os.IsNotExist(err) {
		t.Fatalf("a value was written: %v", err)
	}
	if err := s.Remove("API_KEY", "web"); err == nil {
		t.Fatal("remove went ahead without its audit row")
	}
	if _, err := os.Stat(filepath.Join(dir, "store", "values", "API_KEY.age")); err != nil {
		t.Fatalf("the value is gone: %v", err)
	}
	if err := s.Passwd([]byte(goodPass), []byte("another-long-passphrase"), "web"); err == nil {
		t.Fatal("passwd went ahead without its audit row")
	}
	rec.err = nil
	if err := s.Unlock([]byte(goodPass), time.Hour, "web"); err != nil {
		t.Fatalf("old passphrase no longer opens the store: %v", err)
	}
	// Only the setup rows and this unlock: the refusals left nothing behind.
	if want := []string{"init", "value_set", "unlocked"}; !slices.Equal(actionsOf(rec), want) {
		t.Fatalf("rows after the refusals: %v, want %v", actionsOf(rec), want)
	}
}
