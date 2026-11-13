package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/analytics"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/instancelock"
	"github.com/culiops/culibox/internal/rootmove"
	"github.com/culiops/culibox/internal/secretstore"
)

// rootEnv is one test's home with a legacy data root, fake keepers and a log.
type rootEnv struct {
	home, legacy, newRoot, xdg string
	logs                       []string
}

func (e *rootEnv) logf(f string, a ...any) { e.logs = append(e.logs, fmt.Sprintf(f, a...)) }

func newRootEnv(t *testing.T) *rootEnv {
	t.Helper()
	home := t.TempDir()
	e := &rootEnv{home: home}
	e.newRoot, e.legacy = config.DefaultRoots(home)
	e.xdg = filepath.Join(home, ".config")
	keepers := map[string]*secretstore.FakeKeeper{}
	forHash := func(h string) secretstore.Keeper {
		if keepers[h] == nil {
			keepers[h] = secretstore.NewFakeKeeper(true)
		}
		return keepers[h]
	}
	oldK, oldH := newKeeper, newKeeperForHash
	newKeeper = func(dir string) secretstore.Keeper { return forHash(secretstore.KeyHash(dir)) }
	newKeeperForHash = forHash
	t.Cleanup(func() { newKeeper, newKeeperForHash = oldK, oldH })
	if err := os.MkdirAll(e.legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	return e
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMoveDataRootBindsDeps(t *testing.T) {
	e := newRootEnv(t)
	writeFile(t, filepath.Join(e.legacy, "config.json"), `{"bind_addr":"127.0.0.1:8899"}`)
	// Each rewriter has something to rewrite: the sidecar, an account's status
	// line and an analytics ingest row.
	writeFile(t, filepath.Join(e.legacy, "sessions.json"),
		`{"claude-1":{"agent":"claude","config_dir":"`+e.legacy+`/accounts/w/claude"}}`)
	writeFile(t, filepath.Join(e.legacy, "accounts", "w", "claude", "settings.json"),
		`{"statusLine":{"type":"command","command":"`+e.legacy+`/bin/sl.sh"}}`)
	st, err := analytics.Open(filepath.Join(e.legacy, "analytics.db"))
	if err != nil {
		t.Fatal(err)
	}
	transcript := e.legacy + "/boxstate/b/.claude/projects/p/a.jsonl"
	p := &analytics.Parsed{Tokens: map[analytics.TokKey]*analytics.Tok{}, Attrs: map[analytics.AttrKey]int64{}}
	if err := st.Apply("S1", "proj", transcript, p, 10, 10, 1, nil); err != nil {
		t.Fatal(err)
	}
	st.Close()

	out, err := moveDataRoot(e.home, e.xdg, "linux", e.logf)
	if err != nil || !out.Moved || out.Reason != "" {
		t.Fatalf("outcome %+v err %v; logs %v", out, err, e.logs)
	}
	if fi, err := os.Lstat(e.newRoot); err != nil || !fi.IsDir() {
		t.Fatalf("new root: %v", err)
	}
	if fi, err := os.Lstat(e.legacy); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("no link left at the old path: %v", err)
	}
	if _, src, err := config.Resolve("", false, e.home, e.xdg, ""); err != nil || src.Root.Kind != config.RootLinked || src.Root.Path != e.newRoot {
		t.Fatalf("Resolve after the move: %+v %v", src.Root, err)
	}
	if b, _ := os.ReadFile(filepath.Join(e.newRoot, "sessions.json")); !strings.Contains(string(b), e.newRoot+"/accounts/w/claude") || strings.Contains(string(b), e.legacy) {
		t.Errorf("sidecar not rewritten: %s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(e.newRoot, "accounts", "w", "claude", "settings.json")); !strings.Contains(string(b), e.newRoot+"/bin/sl.sh") {
		t.Errorf("status line not rewritten: %s", b)
	}
	st, err = analytics.Open(filepath.Join(e.newRoot, "analytics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	moved := strings.Replace(transcript, e.legacy, e.newRoot, 1)
	if _, _, ok, _, err := st.State(moved); err != nil || !ok {
		t.Errorf("analytics ingest row not rewritten to %s: ok=%v err=%v", moved, ok, err)
	}
	// A second start has nothing to move.
	if out, err := moveDataRoot(e.home, e.xdg, "linux", e.logf); err != nil || out.Moved || out.Reason != "" {
		t.Fatalf("second start: %+v %v", out, err)
	}
}

func TestMoveDataRootSkipsWithEnvHome(t *testing.T) {
	e := newRootEnv(t)
	out, err := maybeMoveDataRoot("serve", "/somewhere/else", e.home, e.xdg, "linux", e.logf)
	if err != nil || out.Moved || out.Reason != "" {
		t.Fatalf("%+v %v", out, err)
	}
	if fi, err := os.Lstat(e.legacy); err != nil || !fi.IsDir() {
		t.Fatal("the legacy root must stay untouched with a root env var")
	}
	// Only serve moves.
	if _, err := maybeMoveDataRoot("doctor", "", e.home, e.xdg, "linux", e.logf); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(e.legacy); !fi.IsDir() {
		t.Fatal("doctor must not move the root")
	}
	if _, err := os.Lstat(e.newRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new root exists: %v", err)
	}
	// And with no env var, serve does.
	if out, err := maybeMoveDataRoot("serve", "", e.home, e.xdg, "linux", e.logf); err != nil || !out.Moved {
		t.Fatalf("serve: %+v %v", out, err)
	}
}

func TestMoveDataRootSkipsExplicitDataDir(t *testing.T) {
	e := newRootEnv(t)
	writeFile(t, filepath.Join(e.legacy, "config.json"), `{"data_dir":"`+filepath.Join(e.home, "elsewhere")+`"}`)
	out, err := moveDataRoot(e.home, e.xdg, "linux", e.logf)
	if err != nil || out.Moved || !strings.Contains(out.Reason, "data_dir") {
		t.Fatalf("%+v %v", out, err)
	}
	if fi, err := os.Lstat(e.legacy); err != nil || !fi.IsDir() {
		t.Fatal("the legacy root must stay")
	}
	if !strings.Contains(strings.Join(e.logs, "\n"), "data root: kept at "+e.legacy+" (config.json sets data_dir); the next serve start tries again") {
		t.Fatalf("logs: %v", e.logs)
	}
}

// Precondition (e) reads config.json then credentials.json, by position.
func TestMoveDataRootConfigTextsInOrder(t *testing.T) {
	e := newRootEnv(t)
	writeFile(t, filepath.Join(e.legacy, "config.json"), `{"bind_addr":"127.0.0.1:8899"}`)
	writeFile(t, filepath.Join(e.legacy, "credentials.json"), `{"note":"`+e.legacy+`/key"}`)
	out, err := moveDataRoot(e.home, e.xdg, "linux", e.logf)
	if err != nil || out.Moved || !strings.Contains(out.Reason, "credentials.json names a path inside") {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestMoveDataRootKeepsRootWithLiveLock(t *testing.T) {
	e := newRootEnv(t)
	release, err := instancelock.Acquire(e.legacy)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	out, err := moveDataRoot(e.home, e.xdg, "linux", e.logf)
	if err != nil || out.Moved || out.Reason != "another serve holds it" {
		t.Fatalf("%+v %v", out, err)
	}
}

// R2: the ~/.config file moves only while this process holds the instance lock
// on the root in use; a lock held elsewhere skips it.
func TestMoveDataRootXDGMoveHoldsTheLock(t *testing.T) {
	e := newRootEnv(t)
	writeFile(t, filepath.Join(e.xdg, "agent-box", "config.json"), `{"bind_addr":"127.0.0.1:8899"}`)
	real := rootmoveXDG
	var heldDuring bool
	rootmoveXDG = func(base string) (bool, error) {
		_, err := instancelock.Acquire(e.legacy)
		heldDuring = errors.Is(err, instancelock.ErrLocked)
		return real(base)
	}
	t.Cleanup(func() { rootmoveXDG = real })
	if _, err := moveDataRoot(e.home, e.xdg, "linux", e.logf); err != nil {
		t.Fatal(err)
	}
	if !heldDuring {
		t.Fatal("the XDG move ran without the instance lock held")
	}
	if fi, err := os.Lstat(filepath.Join(e.xdg, "culibox", "config.json")); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("config not moved: %v", err)
	}
	if fi, err := os.Lstat(filepath.Join(e.xdg, "agent-box", "config.json")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("no link at the old file: %v", err)
	}
}

func TestMoveDataRootHeldLockSkipsXDGMove(t *testing.T) {
	e := newRootEnv(t)
	writeFile(t, filepath.Join(e.xdg, "agent-box", "config.json"), `{"bind_addr":"127.0.0.1:8899"}`)
	release, err := instancelock.Acquire(e.legacy)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	real := rootmoveXDG
	called := false
	rootmoveXDG = func(base string) (bool, error) { called = true; return real(base) }
	t.Cleanup(func() { rootmoveXDG = real })
	if _, err := moveDataRoot(e.home, e.xdg, "linux", e.logf); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("the XDG move ran although another serve holds the root")
	}
	if _, err := os.Lstat(filepath.Join(e.xdg, "culibox")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("~/.config/culibox exists: %v", err)
	}
	if !strings.Contains(strings.Join(e.logs, "\n"), "another serve holds") {
		t.Fatalf("no log line: %v", e.logs)
	}
}

// A root that cannot be resolved skips the whole move: the normal start reports it.
func TestMoveDataRootRootErrorSkips(t *testing.T) {
	e := newRootEnv(t)
	writeFile(t, filepath.Join(e.legacy, "a"), "x")
	writeFile(t, filepath.Join(e.newRoot, "b"), "x")
	writeFile(t, filepath.Join(e.xdg, "agent-box", "config.json"), `{}`)
	out, err := moveDataRoot(e.home, e.xdg, "linux", e.logf)
	if err != nil || out != (rootmove.Outcome{}) {
		t.Fatalf("%+v %v", out, err)
	}
	if _, err := os.Lstat(filepath.Join(e.xdg, "culibox")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the XDG move must be skipped too: %v", err)
	}
}

// Run is called on every serve start, not only for a legacy root: a journal in
// the new root is an interrupted move to finish.
func TestMoveDataRootResumesInterruptedMove(t *testing.T) {
	e := newRootEnv(t)
	if err := os.Remove(e.legacy); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(e.newRoot, "config.json"), `{"bind_addr":"127.0.0.1:8899"}`)
	writeFile(t, filepath.Join(e.newRoot, rootmove.JournalName), e.legacy+"\n"+secretstore.KeyHash(e.legacy)+"\n")
	out, err := moveDataRoot(e.home, e.xdg, "linux", e.logf)
	if err != nil || !out.Moved || !out.Resumed {
		t.Fatalf("%+v %v; logs %v", out, err, e.logs)
	}
	if _, err := os.Lstat(filepath.Join(e.newRoot, rootmove.JournalName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal left: %v", err)
	}
	if fi, err := os.Lstat(e.legacy); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("no link left: %v", err)
	}
}

// The "profiles used to live there" check follows the resolution: with only the
// new root present it names the new root, not a hard-coded legacy path.
func TestWarnLeftBehindProfilesUsesResolvedRoot(t *testing.T) {
	e := newRootEnv(t)
	if err := os.Remove(e.legacy); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(e.newRoot, "profiles.json"), `{}`)
	got := captureLog(t, func() {
		warnLeftBehindProfiles(config.Config{}, e.home, filepath.Join(e.home, "other", "profiles.json"))
	})
	if !strings.Contains(got, e.newRoot+"/profiles.json") {
		t.Fatalf("log: %q", got)
	}
}
