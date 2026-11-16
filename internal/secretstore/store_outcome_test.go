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

func actionsOf(rec *recorder) []string {
	var out []string
	for _, e := range rec.events {
		out = append(out, e.Action)
	}
	return out
}

// The unlock row is written before the keeper is asked (an audit failure must
// refuse the unlock), so when the keeper then refuses the identity the trail
// needs a second row saying the unlock did not take effect (#288).
func TestUnlockKeeperSaveFailureIsAudited(t *testing.T) {
	s, k, rec, _ := initLinux(t)
	k.FailSave(errors.New("kernel keyring add: operation not permitted"))
	if err := s.Unlock([]byte(goodPass), time.Hour, "web"); err == nil {
		t.Fatal("unlock succeeded although the keeper refused")
	}
	if !s.Status().Locked {
		t.Fatal("store is not locked")
	}
	if want := []string{"init", "unlocked", "unlock_failed"}; !slices.Equal(actionsOf(rec), want) {
		t.Fatalf("audit trail %v, want %v", actionsOf(rec), want)
	}
	e, _ := rec.find("unlock_failed")
	if e.Actor != "web" || e.Detail != unlockNotKept {
		t.Fatalf("unlock_failed row %+v", e)
	}
	if strings.Contains(e.Detail, "keyring add") || strings.Contains(e.Detail, "operation") {
		t.Fatalf("the row carries the raw keeper error: %q", e.Detail)
	}

	// A later unlock that works records a plain unlocked row and no failure.
	k.FailSave(nil)
	rec.events = nil
	if err := s.Unlock([]byte(goodPass), time.Hour, "web"); err != nil {
		t.Fatal(err)
	}
	if rec.has("unlock_failed") || !rec.has("unlocked") {
		t.Fatalf("trail after a good unlock: %+v", rec.events)
	}
}

// failRowRecorder accepts every row except the ones named in refuse.
type failRowRecorder struct {
	recorder
	refuse map[string]bool
}

func (r *failRowRecorder) RecordStore(e Event) error {
	if r.refuse[e.Action] {
		return errors.New("audit down")
	}
	return r.recorder.RecordStore(e)
}

// The failure row is best effort: the unlock already failed, and the keeper's
// error is what the caller must see, not the audit error.
func TestUnlockKeeperSaveFailureSurvivesAnAuditError(t *testing.T) {
	dir := t.TempDir()
	k := NewFakeKeeper(true)
	rec := &failRowRecorder{refuse: map[string]bool{}}
	s := Open(dir, k, rec)
	if err := s.Init([]byte(goodPass), "cli"); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("keeper refused")
	k.FailSave(boom)
	rec.refuse["unlock_failed"] = true
	if err := s.Unlock([]byte(goodPass), time.Hour, "cli"); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}

// macOS: the Keychain save is Init's (Unlock is ErrNotApplicable there). The
// init row is written before it, so a refused save must leave an init_failed
// row after it, and no store behind.
func TestInitKeeperSaveFailureIsAudited(t *testing.T) {
	s, k, rec, dir := newStore(t, false)
	k.FailSave(errors.New("security: User interaction is not allowed."))
	if err := s.Init(nil, "cli"); err == nil {
		t.Fatal("init succeeded although the keeper refused")
	}
	if _, err := os.Stat(filepath.Join(dir, "store")); !os.IsNotExist(err) {
		t.Fatalf("a store was left behind: %v", err)
	}
	if want := []string{"init", "init_failed"}; !slices.Equal(actionsOf(rec), want) {
		t.Fatalf("audit trail %v, want %v", actionsOf(rec), want)
	}
	if e, _ := rec.find("init_failed"); e.Actor != "cli" || e.Detail != initNotCreated {
		t.Fatalf("init_failed row %+v", e)
	}
}

// A good init leaves no failure row.
func TestInitSuccessWritesNoFailureRow(t *testing.T) {
	s, _, rec, _ := newStore(t, false)
	if err := s.Init(nil, "cli"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"init"}; !slices.Equal(actionsOf(rec), want) {
		t.Fatalf("audit trail %v, want %v", actionsOf(rec), want)
	}
}
