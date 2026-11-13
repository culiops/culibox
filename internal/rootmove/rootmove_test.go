package rootmove

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/instancelock"
	"github.com/culiops/culibox/internal/secretstore"
)

const ident = "AGE-SECRET-KEY-1TESTIDENTITY"

// keepers is a fake keyring/Keychain: one FakeKeeper per KeyHash, created on
// first use. needs is NeedsPassphrase (true like Linux, false like macOS).
type keepers struct {
	mu    sync.Mutex
	needs bool
	m     map[string]*secretstore.FakeKeeper
}

func newKeepers(needs bool) *keepers {
	return &keepers{needs: needs, m: map[string]*secretstore.FakeKeeper{}}
}

func (k *keepers) forHash(h string) *secretstore.FakeKeeper {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.m[h] == nil {
		k.m[h] = secretstore.NewFakeKeeper(k.needs)
	}
	return k.m[h]
}

func (k *keepers) forDir(d string) *secretstore.FakeKeeper { return k.forHash(secretstore.KeyHash(d)) }

// loaded is the identity the keeper for hash h holds, or "" with the error.
func (k *keepers) loaded(h string) string {
	id, _, err := k.forHash(h).Load()
	if err != nil {
		return ""
	}
	return id
}

// env is one test's layout: <base>/home/.local/share/{agent-box,culibox}.
type env struct {
	t              *testing.T
	home           string
	legacy, newDir string
	keys           *keepers
	logs           []string
	rewrites       [][2]string
	d              Deps
}

func newEnv(t *testing.T, goos string, needs bool) *env {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	return newEnvAt(t, home, goos, needs)
}

func newEnvAt(t *testing.T, home, goos string, needs bool) *env {
	t.Helper()
	share := filepath.Join(home, ".local", "share")
	e := &env{t: t, home: home, legacy: filepath.Join(share, "agent-box"), newDir: filepath.Join(share, "culibox"), keys: newKeepers(needs)}
	mkdir(t, e.legacy)
	write(t, filepath.Join(e.legacy, "config.json"), `{"bind_addr":"127.0.0.1:8899"}`)
	write(t, filepath.Join(e.legacy, "sessions.json"), `{}`)
	e.d = Deps{
		GOOS:          goos,
		Keeper:        func(dir string) secretstore.Keeper { return e.keys.forDir(dir) },
		KeeperForHash: func(h string) secretstore.Keeper { return e.keys.forHash(h) },
		Live:          func() (int, error) { return 0, nil },
		Lock:          instancelock.Acquire,
		Rewrite: []func(string, string) error{
			func(o, n string) error { e.rewrites = append(e.rewrites, [2]string{o, n}); return nil },
			func(o, n string) error { e.rewrites = append(e.rewrites, [2]string{o, n}); return nil },
		},
		ConfigTexts: []string{`{"bind_addr":"127.0.0.1:8899"}`, ""},
		Logf:        func(f string, a ...any) { e.logs = append(e.logs, fmt.Sprintf(f, a...)) },
	}
	return e
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, p, s string) {
	t.Helper()
	mkdir(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// saveLegacyKey puts the unlocked identity under the legacy root's hash.
func (e *env) saveLegacyKey() {
	e.t.Helper()
	if err := e.keys.forDir(e.legacy).Save(ident, time.Hour); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) run() Outcome {
	e.t.Helper()
	out, err := Run(e.newDir, e.legacy, e.d)
	if err != nil {
		e.t.Fatalf("Run: %v", err)
	}
	return out
}

// assertMoved checks the finished state: the data is under new, a link to new
// sits at legacy, and no journal is left.
func (e *env) assertMoved(out Outcome) {
	e.t.Helper()
	if !out.Moved || out.Reason != "" {
		e.t.Fatalf("Outcome = %+v, want Moved", out)
	}
	if b, err := os.ReadFile(filepath.Join(e.newDir, "config.json")); err != nil || !strings.Contains(string(b), "8899") {
		e.t.Errorf("config.json not under new: %q %v", b, err)
	}
	fi, err := os.Lstat(e.legacy)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		e.t.Fatalf("legacy is not a link: %v %v", fi, err)
	}
	if tgt, _ := os.Readlink(e.legacy); tgt != e.newDir {
		e.t.Errorf("link target = %q, want %q", tgt, e.newDir)
	}
	if exists(filepath.Join(e.newDir, JournalName)) {
		e.t.Error("journal left in the new root")
	}
	e.assertUnlocked(e.newDir)
}

// assertStayed checks nothing moved: legacy is still the real directory and no
// journal is left in it.
func (e *env) assertStayed(out Outcome, reason string) {
	e.t.Helper()
	if out.Moved || out.Resumed || out.Reason != reason {
		e.t.Fatalf("Outcome = %+v, want Reason %q", out, reason)
	}
	fi, err := os.Lstat(e.legacy)
	if err != nil || !fi.IsDir() {
		e.t.Fatalf("legacy is no longer a real directory: %v %v", fi, err)
	}
	if !exists(filepath.Join(e.legacy, "config.json")) {
		e.t.Error("legacy lost its config.json")
	}
	if exists(filepath.Join(e.legacy, JournalName)) {
		e.t.Error("journal left in legacy after an aborted move")
	}
	if len(e.rewrites) != 0 {
		e.t.Errorf("rewrites ran: %v", e.rewrites)
	}
}

// assertUnlocked checks Run released the instance lock.
func (e *env) assertUnlocked(dir string) {
	e.t.Helper()
	rel, err := instancelock.Acquire(dir)
	if err != nil {
		e.t.Fatalf("lock still held on %s after Run: %v", dir, err)
	}
	rel()
}

func TestMoveHappyPath(t *testing.T) {
	e := newEnv(t, "linux", true)
	mkdir(t, filepath.Join(e.legacy, "store"))
	write(t, filepath.Join(e.legacy, "accounts", "w", "claude", "settings.json"), `{}`)
	e.saveLegacyKey()
	out := e.run()
	e.assertMoved(out)
	if out.Resumed {
		t.Error("a fresh move reported Resumed")
	}
	if !exists(filepath.Join(e.newDir, "accounts", "w", "claude", "settings.json")) {
		t.Error("nested file did not move")
	}
	if got := e.keys.loaded(secretstore.KeyHash(e.newDir)); got != ident {
		t.Errorf("key under the new hash = %q, want the identity", got)
	}
	if got := e.keys.loaded(secretstore.KeyHash(e.legacy)); got != ident {
		t.Error("the old key item was removed")
	}
	if len(e.rewrites) != 2 || e.rewrites[0] != [2]string{e.legacy, e.newDir} || e.rewrites[1] != [2]string{e.legacy, e.newDir} {
		t.Errorf("rewrites = %v, want both with (legacy, new)", e.rewrites)
	}
	want := fmt.Sprintf("data root moved: %s → %s (a link was left at the old path)", e.legacy, e.newDir)
	if len(e.logs) == 0 || e.logs[len(e.logs)-1] != want {
		t.Errorf("last log line = %q, want %q (all: %q)", last(e.logs), want, e.logs)
	}
	// The next start sees a migrated root and does nothing.
	e.rewrites = nil
	out, err := Run(e.newDir, e.legacy, e.d)
	if err != nil || out != (Outcome{}) || len(e.rewrites) != 0 {
		t.Errorf("second Run = %+v, %v (rewrites %v), want a no-op", out, err, e.rewrites)
	}
}

func last(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[len(s)-1]
}

func TestMoveNothingToDo(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	share := filepath.Join(home, ".local", "share")
	mkdir(t, share)
	e := &env{t: t, legacy: filepath.Join(share, "agent-box"), newDir: filepath.Join(share, "culibox"), keys: newKeepers(true)}
	e.d = Deps{Lock: func(string) (func(), error) { t.Fatal("locked with nothing to move"); return nil, nil }}
	if out := e.run(); out != (Outcome{}) {
		t.Errorf("no legacy: %+v", out)
	}
	if exists(e.legacy) {
		t.Error("Run created the legacy root")
	}
}

func TestMoveIntoEmptyNew(t *testing.T) {
	e := newEnv(t, "linux", true)
	mkdir(t, e.newDir)
	e.saveLegacyKey()
	e.assertMoved(e.run())
	if got := e.keys.loaded(secretstore.KeyHash(e.newDir)); got != ident {
		t.Errorf("key under the new hash = %q", got)
	}
}

func TestMoveSkipsWhenLocked(t *testing.T) {
	e := newEnv(t, "linux", true)
	e.saveLegacyKey()
	// An older serve still holds the legacy root.
	rel, err := instancelock.Acquire(e.legacy)
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	e.assertStayed(e.run(), "another serve holds it")
	if got := e.keys.loaded(secretstore.KeyHash(e.newDir)); got != "" {
		t.Error("key carried over without a move")
	}
	if exists(e.newDir) {
		t.Error("new root created")
	}
}

func TestMoveSkipsWhenLockedFake(t *testing.T) {
	e := newEnv(t, "linux", true)
	e.d.Lock = func(string) (func(), error) { return nil, instancelock.ErrLocked }
	e.assertStayed(e.run(), "another serve holds it")
}

func TestMoveSkipsLiveSessions(t *testing.T) {
	e := newEnv(t, "linux", true)
	e.d.Live = func() (int, error) { return 2, nil }
	e.assertStayed(e.run(), "2 session(s) running")
	e.assertUnlocked(e.legacy)
}

func TestMoveSkipsWhenSessionsUncountable(t *testing.T) {
	e := newEnv(t, "linux", true)
	e.d.Live = func() (int, error) { return 0, errors.New("walk failed") }
	out := e.run()
	if out.Moved || !strings.Contains(out.Reason, "walk failed") {
		t.Fatalf("Outcome = %+v, want a Reason naming the error", out)
	}
	e.assertUnlocked(e.legacy)
}

func TestMoveSkipsConfigNamesLegacyPath(t *testing.T) {
	for i, name := range []string{"config.json", "credentials.json"} {
		for _, text := range []string{
			`{"profiles_path":"LEG/profiles.json"}`, // followed by /
			`{"data_dir":"LEG"}`,                    // followed by "
			`{"key_file":"LEG\/gh_key"}`,            // followed by \ (an escaped /)
			`LEG`,                                   // end of text
		} {
			e := newEnv(t, "linux", true)
			e.d.ConfigTexts = []string{"{}", "{}"}
			e.d.ConfigTexts[i] = strings.ReplaceAll(text, "LEG", e.legacy)
			e.assertStayed(e.run(), name+" names a path inside "+e.legacy)
			e.assertUnlocked(e.legacy)
		}
	}
}

func TestMoveConfigSiblingPathDoesNotBlock(t *testing.T) {
	e := newEnv(t, "linux", true)
	e.d.ConfigTexts = []string{`{"data_dir":"` + e.legacy + `-prod/x","profiles_path":"` + e.legacy + `x/p.json"}`, "{}"}
	e.assertMoved(e.run())
}

func TestMoveSkipsDarwinVM(t *testing.T) {
	e := newEnv(t, "darwin", false)
	mkdir(t, filepath.Join(e.legacy, "vm"))
	e.assertStayed(e.run(), "the box VM lives here")
	e.assertUnlocked(e.legacy)
}

func TestMoveLinuxIgnoresVMDir(t *testing.T) {
	e := newEnv(t, "linux", true)
	mkdir(t, filepath.Join(e.legacy, "vm"))
	e.assertMoved(e.run())
}

func TestMoveSkipsLegacySymlink(t *testing.T) {
	e := newEnv(t, "linux", true)
	real := filepath.Join(filepath.Dir(e.legacy), "elsewhere")
	if err := os.Rename(e.legacy, real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, e.legacy); err != nil {
		t.Fatal(err)
	}
	out := e.run()
	if out.Moved || out.Reason != "not a real directory" {
		t.Fatalf("Outcome = %+v", out)
	}
	if fi, _ := os.Lstat(e.legacy); fi.Mode()&os.ModeSymlink == 0 || exists(e.newDir) {
		t.Error("layout changed")
	}
	if exists(filepath.Join(real, JournalName)) {
		t.Error("journal written")
	}
}

func TestMoveSkipsLegacyNotADirectory(t *testing.T) {
	e := newEnv(t, "linux", true)
	if err := os.RemoveAll(e.legacy); err != nil {
		t.Fatal(err)
	}
	write(t, e.legacy, "a file")
	e.d.Lock = func(string) (func(), error) { t.Fatal("locked a file"); return nil, nil }
	if out := e.run(); out.Moved || out.Reason != "not a real directory" {
		t.Fatalf("Outcome = %+v", out)
	}
}

// New a link to legacy: both names reach one directory, so there is nothing to
// rename onto. It stays as is.
func TestMoveSkipsNewLinkToLegacy(t *testing.T) {
	e := newEnv(t, "linux", true)
	if err := os.Symlink(e.legacy, e.newDir); err != nil {
		t.Fatal(err)
	}
	out := e.run()
	if out.Moved || out.Reason == "" {
		t.Fatalf("Outcome = %+v, want a Reason", out)
	}
	e.assertStayed(out, out.Reason)
	if fi, _ := os.Lstat(e.newDir); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("new link replaced")
	}
}

// New the same directory as legacy by another route (here a link to a link to
// legacy, standing in for a bind mount, which needs root to build): same
// inode, so it stays as is.
func TestMoveSkipsNewSameInode(t *testing.T) {
	e := newEnv(t, "linux", true)
	mid := filepath.Join(filepath.Dir(e.legacy), "mid")
	if err := os.Symlink(e.legacy, mid); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(mid, e.newDir); err != nil {
		t.Fatal(err)
	}
	out := e.run()
	if out.Moved || out.Reason == "" {
		t.Fatalf("Outcome = %+v, want a Reason", out)
	}
	e.assertStayed(out, out.Reason)
}

func TestMoveSkipsNewNotEmpty(t *testing.T) {
	e := newEnv(t, "linux", true)
	write(t, filepath.Join(e.newDir, "config.json"), "{}")
	out := e.run()
	if want := e.newDir + " exists and is not an empty directory"; out.Reason != want {
		t.Fatalf("Reason = %q, want %q", out.Reason, want)
	}
	e.assertStayed(out, out.Reason)
	e.assertUnlocked(e.legacy)
}

func TestMountPointDetected(t *testing.T) {
	if _, err := os.Stat("/proc/self"); err != nil {
		t.Skip("no /proc")
	}
	var a, b syscall.Stat_t
	if syscall.Stat("/proc", &a) != nil || syscall.Stat("/", &b) != nil || a.Dev == b.Dev {
		t.Skip("/proc is not a separate mount here")
	}
	if mp, err := isMountPoint("/proc"); err != nil || !mp {
		t.Errorf("isMountPoint(/proc) = %v, %v; want true", mp, err)
	}
	if mp, err := isMountPoint(t.TempDir()); err != nil || mp {
		t.Errorf("isMountPoint(tempdir) = %v, %v; want false", mp, err)
	}
}

// A new root on another filesystem than legacy aborts before anything changes.
func TestMoveSkipsOtherFilesystem(t *testing.T) {
	other, err := os.MkdirTemp("/dev/shm", "rootmove-")
	if err != nil {
		t.Skip("no /dev/shm")
	}
	defer os.RemoveAll(other)
	e := newEnv(t, "linux", true)
	var a, b syscall.Stat_t
	if syscall.Stat(other, &a) != nil || syscall.Stat(e.legacy, &b) != nil || a.Dev == b.Dev {
		t.Skip("/dev/shm is on the same filesystem")
	}
	e.saveLegacyKey()
	e.newDir = filepath.Join(other, "culibox")
	e.assertStayed(e.run(), "on another filesystem")
	e.assertUnlocked(e.legacy)
	if exists(e.newDir) {
		t.Error("new root created")
	}
}

func TestMoveDarwinKeyUnreadableAborts(t *testing.T) {
	e := newEnv(t, "darwin", false)
	mkdir(t, filepath.Join(e.legacy, "store"))
	e.saveLegacyKey()
	e.keys.forDir(e.legacy).FailLoad(errors.New("User interaction is not allowed"))
	e.assertStayed(e.run(), "the store key could not be read from the Keychain")
	e.assertUnlocked(e.legacy)
	if got := e.keys.loaded(secretstore.KeyHash(e.newDir)); got != "" {
		t.Error("a key was saved under the new hash")
	}
}

// A Mac with no store has no key to read: the move goes ahead.
func TestMoveDarwinNoStoreMoves(t *testing.T) {
	e := newEnv(t, "darwin", false)
	e.assertMoved(e.run())
}

func TestMoveDarwinCarriesKey(t *testing.T) {
	e := newEnv(t, "darwin", false)
	mkdir(t, filepath.Join(e.legacy, "store"))
	e.saveLegacyKey()
	e.assertMoved(e.run())
	if got := e.keys.loaded(secretstore.KeyHash(e.newDir)); got != ident {
		t.Errorf("key under the new hash = %q", got)
	}
}

func TestMoveLinuxLockedStoreStillMoves(t *testing.T) {
	e := newEnv(t, "linux", true)
	mkdir(t, filepath.Join(e.legacy, "store"))
	// No unlocked identity: Load returns ErrLocked.
	e.assertMoved(e.run())
	if got := e.keys.loaded(secretstore.KeyHash(e.newDir)); got != "" {
		t.Error("a key appeared under the new hash")
	}
}

func TestMoveKeySaveFailsNoRename(t *testing.T) {
	for _, tc := range []struct {
		goos  string
		needs bool
	}{{"linux", true}, {"darwin", false}} {
		t.Run(tc.goos, func(t *testing.T) {
			e := newEnv(t, tc.goos, tc.needs)
			mkdir(t, filepath.Join(e.legacy, "store"))
			e.saveLegacyKey()
			e.keys.forHash(secretstore.KeyHash(e.newDir)).FailSave(errors.New("keychain write failed"))
			out := e.run()
			if out.Moved || !strings.Contains(out.Reason, "could not be saved") {
				t.Fatalf("Outcome = %+v", out)
			}
			e.assertStayed(out, out.Reason)
			e.assertUnlocked(e.legacy)
			if exists(e.newDir) {
				t.Error("new root created")
			}
		})
	}
}

// writeJournal leaves the state a kill between steps 3 and 6 leaves: the data
// renamed to new with the journal inside, no link at legacy.
func (e *env) afterRename(hash string) {
	e.t.Helper()
	write(e.t, filepath.Join(e.legacy, JournalName), e.legacy+"\n"+hash+"\n")
	if err := os.Rename(e.legacy, e.newDir); err != nil {
		e.t.Fatal(err)
	}
}

func TestResumeAfterRenameRecopiesKey(t *testing.T) {
	for _, tc := range []struct {
		goos  string
		needs bool
	}{{"linux", true}, {"darwin", false}} {
		t.Run(tc.goos, func(t *testing.T) {
			e := newEnv(t, tc.goos, tc.needs)
			mkdir(t, filepath.Join(e.legacy, "store"))
			e.saveLegacyKey()
			oldHash := secretstore.KeyHash(e.legacy)
			e.afterRename(oldHash)
			// Keeper(legacy) would now hash an absent path the same way, so make
			// sure the key is read by the journal's hash: a keeper reached any
			// other way holds nothing.
			e.d.Keeper = func(dir string) secretstore.Keeper {
				if dir != e.newDir {
					t.Errorf("resume asked Keeper for %s; it must use the journal hash", dir)
				}
				return e.keys.forDir(dir)
			}
			out := e.run()
			if !out.Resumed {
				t.Fatalf("Outcome = %+v, want Resumed", out)
			}
			e.assertMoved(out)
			if got := e.keys.loaded(secretstore.KeyHash(e.newDir)); got != ident {
				t.Errorf("key under the new hash = %q, want the identity", got)
			}
			if len(e.rewrites) != 2 || e.rewrites[0] != [2]string{e.legacy, e.newDir} {
				t.Errorf("rewrites = %v", e.rewrites)
			}
		})
	}
}

// A key already readable under the new hash is not overwritten on resume.
func TestResumeKeepsNewKey(t *testing.T) {
	e := newEnv(t, "darwin", false)
	mkdir(t, filepath.Join(e.legacy, "store"))
	e.saveLegacyKey()
	oldHash := secretstore.KeyHash(e.legacy)
	e.afterRename(oldHash)
	if err := e.keys.forDir(e.newDir).Save("AGE-SECRET-KEY-1NEW", 0); err != nil {
		t.Fatal(err)
	}
	e.assertMoved(e.run())
	if got := e.keys.loaded(secretstore.KeyHash(e.newDir)); got != "AGE-SECRET-KEY-1NEW" {
		t.Errorf("new key = %q, overwritten", got)
	}
}

// On a Mac a key copy that fails on resume keeps the journal, so the next
// start retries: the Keychain holds the only copy.
func TestResumeDarwinKeyCopyFailsKeepsJournal(t *testing.T) {
	e := newEnv(t, "darwin", false)
	mkdir(t, filepath.Join(e.legacy, "store"))
	e.saveLegacyKey()
	oldHash := secretstore.KeyHash(e.legacy)
	e.afterRename(oldHash)
	e.keys.forHash(oldHash).FailLoad(errors.New("User interaction is not allowed"))
	out := e.run()
	if !out.Resumed {
		t.Fatalf("Outcome = %+v", out)
	}
	if !exists(filepath.Join(e.newDir, JournalName)) {
		t.Error("journal removed with the key still missing")
	}
	e.keys.forHash(oldHash).FailLoad(nil)
	e.assertMoved(e.run())
	if got := e.keys.loaded(secretstore.KeyHash(e.newDir)); got != ident {
		t.Errorf("key under the new hash = %q after the retry", got)
	}
}

func TestResumeLockedSkips(t *testing.T) {
	e := newEnv(t, "linux", true)
	e.afterRename(secretstore.KeyHash(e.legacy))
	rel, err := instancelock.Acquire(e.newDir)
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	out := e.run()
	if out.Moved || out.Resumed || out.Reason != "another serve holds it" {
		t.Fatalf("Outcome = %+v", out)
	}
	if !exists(filepath.Join(e.newDir, JournalName)) {
		t.Error("journal removed while locked")
	}
}

func TestResumeBadJournalIsAnError(t *testing.T) {
	e := newEnv(t, "linux", true)
	write(t, filepath.Join(e.legacy, JournalName), "relative/path\n")
	if err := os.Rename(e.legacy, e.newDir); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(e.newDir, e.legacy, e.d); err == nil || !strings.Contains(err.Error(), JournalName) {
		t.Fatalf("err = %v, want one naming the journal", err)
	}
}

// A kill between steps 1 and 3 leaves the journal in legacy; the next start
// moves as usual.
func TestMoveAfterCrashBeforeRename(t *testing.T) {
	e := newEnv(t, "linux", true)
	write(t, filepath.Join(e.legacy, JournalName), e.legacy+"\nffffffffffffffff\n")
	e.saveLegacyKey()
	e.assertMoved(e.run())
	if got := e.keys.loaded(secretstore.KeyHash(e.newDir)); got != ident {
		t.Errorf("key under the new hash = %q", got)
	}
}

// ~ reached through a symlink: the hash saved before the rename is the one
// KeyHash(new) gives once new exists.
func TestKeyHashPrecomputedMatchesAfterRename(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "data", "u")
	mkdir(t, real)
	home := filepath.Join(base, "home-u")
	if err := os.Symlink(real, home); err != nil {
		t.Fatal(err)
	}
	e := newEnvAt(t, home, "darwin", false)
	mkdir(t, filepath.Join(e.legacy, "store"))
	e.saveLegacyKey()
	// The naive hash of new before the move (path absent, link not resolved)
	// differs, so the test can tell the two apart.
	naive := secretstore.KeyHash(e.newDir)
	e.assertMoved(e.run())
	after := secretstore.KeyHash(e.newDir)
	if naive == after {
		t.Fatal("test layout does not exercise a symlinked parent")
	}
	if got := e.keys.loaded(after); got != ident {
		t.Errorf("key under KeyHash(new) after the move = %q, want the identity", got)
	}
}

// Something sits at the old path when the link is due (a kill after the
// rename, then something else took the path before the next start): the move
// warns and still finishes. A fresh move has no hook between its rename and its
// link (R13 makes them adjacent), so the state is set up as the resume sees it.
func TestMoveLinkFailureWarnsAndFinishes(t *testing.T) {
	e := newEnv(t, "linux", true)
	e.afterRename(secretstore.KeyHash(e.legacy))
	write(t, e.legacy, "x")
	out := e.run()
	if !out.Moved || out.Reason != "" {
		t.Fatalf("Outcome = %+v", out)
	}
	if exists(filepath.Join(e.newDir, JournalName)) {
		t.Error("journal left")
	}
	if len(e.rewrites) != 2 {
		t.Errorf("rewrites = %v, want both run", e.rewrites)
	}
	if !hasLog(e.logs, "WARNING") || !hasLog(e.logs, e.legacy) {
		t.Errorf("no WARNING naming the old path: %q", e.logs)
	}
	if strings.Contains(last(e.logs), "a link was left") {
		t.Errorf("final line claims a link: %q", last(e.logs))
	}
	if !strings.HasPrefix(last(e.logs), "data root moved: "+e.legacy+" → "+e.newDir) {
		t.Errorf("final line = %q", last(e.logs))
	}
}

// R13: the compat link is made right after the rename, before the rewrites, so
// an older serve started meanwhile finds the moved root through it.
func TestMoveLinksBeforeRewrites(t *testing.T) {
	e := newEnv(t, "linux", true)
	linkedFirst := false
	e.d.Rewrite = []func(string, string) error{func(o, n string) error {
		fi, err := os.Lstat(o)
		linkedFirst = err == nil && fi.Mode()&os.ModeSymlink != 0
		return nil
	}}
	e.assertMoved(e.run())
	if !linkedFirst {
		t.Error("the rewrites ran before the link was made")
	}
}

// A kill between the link and the rewrites: the journal is in the new root and
// the link at the old path. The next start accepts the link, runs the rewrites
// and removes the journal.
func TestResumeAfterLinkBeforeRewrites(t *testing.T) {
	e := newEnv(t, "linux", true)
	e.afterRename(secretstore.KeyHash(e.legacy))
	if err := os.Symlink(e.newDir, e.legacy); err != nil {
		t.Fatal(err)
	}
	out := e.run()
	if !out.Resumed {
		t.Fatalf("Outcome = %+v, want Resumed", out)
	}
	e.assertMoved(out)
	if len(e.rewrites) != 2 || e.rewrites[0] != [2]string{e.legacy, e.newDir} {
		t.Errorf("rewrites = %v, want both with (legacy, new)", e.rewrites)
	}
	if hasLog(e.logs, "WARNING") {
		t.Errorf("an existing correct link warned: %q", e.logs)
	}
	if !strings.Contains(last(e.logs), "a link was left") {
		t.Errorf("final line = %q", last(e.logs))
	}
}

func TestRewriteFailureWarnsAndFinishes(t *testing.T) {
	e := newEnv(t, "linux", true)
	called := false
	e.d.Rewrite = []func(string, string) error{
		func(string, string) error { return errors.New("database is locked") },
		func(string, string) error { called = true; return nil },
	}
	e.assertMoved(e.run())
	if !called {
		t.Error("a failed rewrite stopped the next one")
	}
	if !hasLog(e.logs, "WARNING") || !hasLog(e.logs, "database is locked") {
		t.Errorf("no WARNING with the error: %q", e.logs)
	}
}

func hasLog(logs []string, s string) bool {
	for _, l := range logs {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

func TestMoveXDGConfig(t *testing.T) {
	xdg := t.TempDir()
	oldF := filepath.Join(xdg, "agent-box", "config.json")
	newF := filepath.Join(xdg, "culibox", "config.json")
	write(t, oldF, `{"a":1}`)
	write(t, filepath.Join(xdg, "agent-box", "gh_key"), "k")
	moved, err := MoveXDGConfig(xdg)
	if err != nil || !moved {
		t.Fatalf("MoveXDGConfig = %v, %v", moved, err)
	}
	if b, _ := os.ReadFile(newF); string(b) != `{"a":1}` {
		t.Errorf("new config = %q", b)
	}
	if fi, err := os.Lstat(oldF); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("old path is not a link: %v %v", fi, err)
	}
	if b, _ := os.ReadFile(oldF); string(b) != `{"a":1}` {
		t.Errorf("old path reads %q through the link", b)
	}
	if !exists(filepath.Join(xdg, "agent-box", "gh_key")) || exists(filepath.Join(xdg, "culibox", "gh_key")) {
		t.Error("sibling file moved")
	}
	if fi, _ := os.Stat(filepath.Join(xdg, "culibox")); fi.Mode().Perm() != 0o700 {
		t.Errorf("culibox dir mode = %o, want 700", fi.Mode().Perm())
	}
	// Second call: no-op.
	if moved, err := MoveXDGConfig(xdg); err != nil || moved {
		t.Errorf("second call = %v, %v; want false, nil", moved, err)
	}
}

func TestMoveXDGConfigNoOpWhenNewExists(t *testing.T) {
	xdg := t.TempDir()
	oldF := filepath.Join(xdg, "agent-box", "config.json")
	newF := filepath.Join(xdg, "culibox", "config.json")
	write(t, oldF, "old")
	write(t, newF, "new")
	if moved, err := MoveXDGConfig(xdg); err != nil || moved {
		t.Fatalf("MoveXDGConfig = %v, %v; want false, nil", moved, err)
	}
	if b, _ := os.ReadFile(oldF); string(b) != "old" {
		t.Error("old file changed")
	}
	if b, _ := os.ReadFile(newF); string(b) != "new" {
		t.Error("new file changed")
	}
}

func TestMoveXDGConfigNoOldFile(t *testing.T) {
	xdg := t.TempDir()
	if moved, err := MoveXDGConfig(xdg); err != nil || moved {
		t.Fatalf("MoveXDGConfig = %v, %v; want false, nil", moved, err)
	}
	if exists(filepath.Join(xdg, "culibox")) {
		t.Error("culibox dir created with nothing to move")
	}
}
