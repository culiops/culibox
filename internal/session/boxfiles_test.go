package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newBoxFilesManager(t *testing.T) (*Manager, string) {
	t.Helper()
	state := t.TempDir()
	m := &Manager{sessions: map[string]*Session{}, stateDir: state}
	return m, state
}

// Two sessions in one box are one read; an exited session that is still
// retained in the manager (never deleted) still counts; host sessions do not.
func TestBoxFilesDistinctPairs(t *testing.T) {
	m, state := newBoxFilesManager(t)
	m.sessions["a"] = &Session{ID: "a", Box: "box-1"}
	m.sessions["b"] = &Session{ID: "b", Box: "box-1"}
	m.sessions["c"] = &Session{ID: "c", Box: "box-2", Account: "work"}
	m.sessions["d"] = &Session{ID: "d", Box: "box-3", Account: "default"}
	m.sessions["h"] = &Session{ID: "h"}
	for _, b := range []string{"box-1", "box-2", "box-3"} {
		writeBoxFile(t, filepath.Join(boxHome(state, b), ".claude", "x.json"), `{"box":"`+b+`"}`, time.Now())
	}
	got := m.BoxFiles(".claude/x.json", 4096)
	if len(got) != 3 {
		t.Fatalf("want 3 reads, got %+v", got)
	}
	acct := map[string]string{}
	for _, f := range got {
		acct[string(f.Data)] = f.Account
	}
	if acct[`{"box":"box-1"}`] != "default" || acct[`{"box":"box-2"}`] != "work" || acct[`{"box":"box-3"}`] != "default" {
		t.Fatalf("accounts = %v", acct)
	}
}

func TestBoxFilesSkipsSymlinkMissingAndEmptyState(t *testing.T) {
	m, state := newBoxFilesManager(t)
	m.sessions["a"] = &Session{ID: "a", Box: "box-1"}
	m.sessions["b"] = &Session{ID: "b", Box: "box-2"} // no files at all
	dir := filepath.Join(boxHome(state, "box-1"), ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "x.json")); err != nil {
		t.Fatal(err)
	}
	if got := m.BoxFiles(".claude/x.json", 4096); len(got) != 0 {
		t.Fatalf("symlink / missing must be skipped, got %+v", got)
	}
	m.stateDir = ""
	if got := m.BoxFiles(".claude/x.json", 4096); got != nil {
		t.Fatalf("empty stateDir = %+v", got)
	}
}
