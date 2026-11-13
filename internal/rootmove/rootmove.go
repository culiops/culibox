// Package rootmove moves the data root from before the rebrand
// (~/.local/share/agent-box) to ~/.local/share/culibox once, at serve start,
// and only when that is safe (#255, spec D2). It is crash-safe: a journal
// written before anything moves lets the next start finish an interrupted move.
// Every effect outside the filesystem (keeper, lock, live sessions, rewrites,
// logging) is injected, so the package is tested with fakes on any OS.
package rootmove

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/culiops/culibox/internal/instancelock"
	"github.com/culiops/culibox/internal/secretstore"
)

// JournalName is the move journal, written into the legacy root before the key
// carry; it travels with the rename and is removed when the move is done.
const JournalName = ".culibox-move"

// Deps are Run's effects: serve binds the real ones, tests bind fakes.
type Deps struct {
	// GOOS is the target OS ("darwin" enables precondition (f)).
	GOOS string
	// Keeper is the store keeper for a data dir (secretstore.NewKeeper).
	Keeper func(dataDir string) secretstore.Keeper
	// KeeperForHash is the keeper for a KeyHash (secretstore.NewKeeperForHash):
	// resume reads the old key by the hash its journal recorded.
	KeeperForHash func(hash string) secretstore.Keeper
	// Live counts the live sessions on the legacy root.
	Live func() (int, error)
	// Lock takes the instance lock on dir (instancelock.Acquire).
	Lock func(dir string) (release func(), err error)
	// Rewrite are the stored-path rewriters (analytics, sessions.json,
	// account settings), each bound by the caller and idempotent.
	Rewrite []func(oldRoot, newRoot string) error
	// ConfigTexts are the raw config file contents scanned by precondition (e),
	// in this order: config.json, credentials.json.
	ConfigTexts []string
	// Logf logs one line.
	Logf func(format string, args ...any)
}

// configNames names ConfigTexts by index in a Reason.
var configNames = []string{"config.json", "credentials.json"}

// Outcome is what Run did. Moved: the root is now the new one, moved by this
// call (Resumed: by finishing an interrupted move). Reason says why it stayed
// put; it is "" when moved or when there was nothing to move.
type Outcome struct {
	Moved, Resumed bool
	Reason         string
}

// Run moves legacyRoot to newRoot when every precondition of spec D2 holds,
// or finishes an interrupted move when <newRoot>/.culibox-move exists. It
// returns an error only for a state that must stop serve; a move that does
// not happen is an Outcome with a Reason, and the next start tries again.
func Run(newRoot, legacyRoot string, d Deps) (Outcome, error) {
	if d.Logf == nil {
		d.Logf = func(string, ...any) {}
	}
	newRoot, legacyRoot = filepath.Clean(newRoot), filepath.Clean(legacyRoot)
	if !filepath.IsAbs(newRoot) || !filepath.IsAbs(legacyRoot) {
		return Outcome{}, fmt.Errorf("data root move: %s and %s must be absolute paths", newRoot, legacyRoot)
	}
	if journalPending(newRoot, legacyRoot) {
		return resume(newRoot, legacyRoot, d)
	}
	return move(newRoot, legacyRoot, d)
}

// journalPending reports an interrupted move to finish: new is a real
// directory holding the journal, and legacy is no longer a real directory (so
// the journal cannot be legacy's own, seen through a link or a bind mount).
func journalPending(newRoot, legacyRoot string) bool {
	nfi, err := os.Lstat(newRoot)
	if err != nil || !nfi.IsDir() {
		return false
	}
	if jfi, err := os.Lstat(filepath.Join(newRoot, JournalName)); err != nil || !jfi.Mode().IsRegular() {
		return false
	}
	lfi, err := os.Lstat(legacyRoot)
	return err != nil || !lfi.IsDir()
}

func move(newRoot, legacy string, d Deps) (Outcome, error) {
	// Nothing to move: no legacy root, or it is the link a move left.
	lfi, err := os.Lstat(legacy)
	if errors.Is(err, os.ErrNotExist) {
		return Outcome{}, nil
	}
	if err != nil {
		return Outcome{Reason: fmt.Sprintf("%s cannot be checked: %v", legacy, err)}, nil
	}
	lst, err := os.Stat(legacy)
	if err != nil || !lst.IsDir() {
		return Outcome{Reason: "not a real directory"}, nil
	}
	if lfi.Mode()&os.ModeSymlink != 0 {
		if nst, err := os.Stat(newRoot); err == nil && os.SameFile(nst, lst) {
			return Outcome{}, nil
		}
	}

	// (c) No other serve holds the legacy root; held through the whole move.
	release, err := d.Lock(legacy)
	if errors.Is(err, instancelock.ErrLocked) {
		return Outcome{Reason: "another serve holds it"}, nil
	}
	if err != nil {
		return Outcome{Reason: fmt.Sprintf("the instance lock could not be taken: %v", err)}, nil
	}
	defer release()

	// (d) No live session.
	n, err := d.Live()
	if err != nil {
		return Outcome{Reason: fmt.Sprintf("running sessions could not be counted: %v", err)}, nil
	}
	if n > 0 {
		return Outcome{Reason: fmt.Sprintf("%d session(s) running", n)}, nil
	}
	// (e) No config file names a path inside legacy.
	for i, txt := range d.ConfigTexts {
		if namesPathInside(txt, legacy) {
			name := "a config file"
			if i < len(configNames) {
				name = configNames[i]
			}
			return Outcome{Reason: fmt.Sprintf("%s names a path inside %s", name, legacy)}, nil
		}
	}
	// (f) darwin only: a box VM keeps absolute paths into legacy.
	if d.GOOS == "darwin" {
		if _, err := os.Lstat(filepath.Join(legacy, "vm")); err == nil {
			return Outcome{Reason: "the box VM lives here"}, nil
		}
	}
	// (b) Legacy is a real directory, not a symlink or a mount point; new is
	// absent or a real empty directory; both on one filesystem.
	if reason := checkLayout(newRoot, legacy, lfi); reason != "" {
		return Outcome{Reason: reason}, nil
	}
	_, nerr := os.Lstat(newRoot)
	emptyNew := nerr == nil

	// 1. The journal, durable before anything else changes.
	journal := filepath.Join(legacy, JournalName)
	legacyHash := secretstore.KeyHash(legacy)
	if err := writeJournal(journal, legacy, legacyHash); err != nil {
		os.Remove(journal)
		return Outcome{Reason: fmt.Sprintf("the move journal could not be written: %v", err)}, nil
	}
	abort := func(reason string) (Outcome, error) {
		os.Remove(journal)
		syncDir(legacy)
		return Outcome{Reason: reason}, nil
	}

	// 2. Carry the store key to the hash new will have, before anything moves.
	if reason := carryKey(newRoot, legacy, d); reason != "" {
		return abort(reason)
	}

	// 3. Rename.
	if emptyNew {
		if err := os.Remove(newRoot); err != nil {
			return abort(fmt.Sprintf("the empty %s could not be removed: %v", newRoot, err))
		}
	}
	if err := os.Rename(legacy, newRoot); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			return abort("on another filesystem")
		}
		return abort(fmt.Sprintf("the rename failed: %v", err))
	}
	syncDir(filepath.Dir(newRoot))
	if filepath.Dir(legacy) != filepath.Dir(newRoot) {
		syncDir(filepath.Dir(legacy))
	}

	// 4-6 (link first, then the rewrites: see finish).
	finish(newRoot, legacy, d, false)
	return Outcome{Moved: true}, nil
}

// checkLayout is precondition (b); "" when it holds.
func checkLayout(newRoot, legacy string, lfi os.FileInfo) string {
	if lfi.Mode()&os.ModeSymlink != 0 || !lfi.IsDir() {
		return "not a real directory"
	}
	if mp, err := isMountPoint(legacy); err != nil || mp {
		return "not a real directory"
	}
	nfi, err := os.Lstat(newRoot)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return fmt.Sprintf("%s cannot be checked: %v", newRoot, err)
	case nfi.Mode()&os.ModeSymlink != 0 || !nfi.IsDir() || os.SameFile(nfi, lfi):
		return fmt.Sprintf("%s exists and is not an empty directory", newRoot)
	default:
		if mp, err := isMountPoint(newRoot); err != nil || mp {
			return fmt.Sprintf("%s is a mount point", newRoot)
		}
		ents, err := os.ReadDir(newRoot)
		if err != nil {
			return fmt.Sprintf("%s cannot be read: %v", newRoot, err)
		}
		if len(ents) > 0 {
			return fmt.Sprintf("%s exists and is not an empty directory", newRoot)
		}
	}
	same, err := sameDevice(legacy, filepath.Dir(newRoot))
	if err != nil {
		return fmt.Sprintf("%s cannot be checked: %v", filepath.Dir(newRoot), err)
	}
	if !same {
		return "on another filesystem"
	}
	return ""
}

// namesPathInside reports text holding legacy as a whole path component:
// followed by '/', '"', '\' or the end of text. <legacy>-prod does not count.
func namesPathInside(text, legacy string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], legacy)
		if j < 0 {
			return false
		}
		end := i + j + len(legacy)
		if end == len(text) || strings.ContainsRune(`/"\`, rune(text[end])) {
			return true
		}
		i += j + 1
	}
}

// carryKey is step 2: save the unlocked identity under the hash new will have.
// "" means go on (carried, or nothing to carry); otherwise the move aborts.
func carryKey(newRoot, legacy string, d Deps) string {
	src := d.Keeper(legacy)
	identity, expires, err := src.Load()
	if err != nil {
		_, serr := os.Lstat(filepath.Join(legacy, "store"))
		storePresent := serr == nil
		// A keeper that needs no passphrase (the macOS Keychain) holds the only
		// copy of the key: no move without it.
		if storePresent && !src.NeedsPassphrase() {
			return "the store key could not be read from the Keychain"
		}
		if storePresent {
			d.Logf("data root move: the store key was not carried over (%v); unlock the store again after the move", err)
		}
		return ""
	}
	ttl := time.Hour
	if !expires.IsZero() {
		ttl = time.Until(expires)
		if ttl < time.Second { // about to lock: as good as locked
			d.Logf("data root move: the store key was not carried over (it is about to lock); unlock the store again after the move")
			return ""
		}
	}
	target, err := keyTarget(newRoot)
	if err != nil {
		return fmt.Sprintf("the store key could not be saved for %s: %v", newRoot, err)
	}
	if err := d.Keeper(target).Save(identity, ttl); err != nil {
		return fmt.Sprintf("the store key could not be saved for %s: %v", newRoot, err)
	}
	return ""
}

// keyTarget is the path whose KeyHash equals KeyHash(newRoot) once newRoot
// exists: its parent resolved, so a symlinked ~ or ~/.local gives the same
// hash before and after the rename.
func keyTarget(newRoot string) (string, error) {
	parent, err := filepath.EvalSymlinks(filepath.Dir(newRoot))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(newRoot)), nil
}

// resume finishes a move a kill interrupted after the rename (steps 4-6),
// whether or not the link was made yet.
func resume(newRoot, legacyArg string, d Deps) (Outcome, error) {
	release, err := d.Lock(newRoot)
	if errors.Is(err, instancelock.ErrLocked) {
		return Outcome{Reason: "another serve holds it"}, nil
	}
	if err != nil {
		return Outcome{Reason: fmt.Sprintf("the instance lock could not be taken: %v", err)}, nil
	}
	defer release()
	journal := filepath.Join(newRoot, JournalName)
	legacy, hash, err := readJournal(journal)
	if err != nil {
		return Outcome{}, fmt.Errorf("data root move: the journal %s is unreadable (%v); finish the move by hand or remove the file", journal, err)
	}
	if legacy != legacyArg {
		d.Logf("data root move: the journal names %s (not %s); finishing the move from there", legacy, legacyArg)
	}
	d.Logf("data root move: finishing an interrupted move from %s to %s", legacy, newRoot)
	keyPending := recopyKey(newRoot, hash, d)
	finish(newRoot, legacy, d, keyPending)
	return Outcome{Moved: true, Resumed: true}, nil
}

// recopyKey copies the store key from the journal's hash when new has none.
// It reports a copy still pending that only a retry can make: the old key is
// the only copy (no passphrase) and could not be read or saved now.
func recopyKey(newRoot, hash string, d Deps) bool {
	dst := d.Keeper(newRoot)
	if _, _, err := dst.Load(); err == nil {
		return false
	}
	src := d.KeeperForHash(hash)
	_, serr := os.Lstat(filepath.Join(newRoot, "store"))
	storePresent := serr == nil
	onlyCopy := storePresent && !src.NeedsPassphrase()
	identity, expires, err := src.Load()
	if err != nil {
		if errors.Is(err, secretstore.ErrNotInitialized) || !onlyCopy {
			if storePresent {
				d.Logf("data root move: the store key was not carried over (%v); unlock the store again", err)
			}
			return false
		}
		d.Logf("WARNING: data root move: the store key could not be read from the Keychain (%v); the next serve start retries", err)
		return true
	}
	ttl := time.Hour
	if !expires.IsZero() {
		if ttl = time.Until(expires); ttl < time.Second {
			return false
		}
	}
	if err := dst.Save(identity, ttl); err != nil {
		d.Logf("WARNING: data root move: the store key could not be saved for %s (%v); the next serve start retries", newRoot, err)
		return onlyCopy
	}
	return false
}

// finish is steps 4-6, each idempotent. keepJournal leaves the journal so the
// next start runs them again (a key copy is still pending).
//
// The link (spec step 5) is made before the rewrites (spec step 4), ruling
// R13: right after the rename the old path is briefly empty, and an older
// agent-box serve started then would create a fresh, empty root there. Linking
// first shrinks that window to the gap between two syscalls. Crash safety is unchanged:
// the journal stays until step 6, so a kill after the link resumes, accepts the
// link and runs the rewrites.
func finish(newRoot, legacy string, d Deps, keepJournal bool) {
	// 5. The compat link.
	linked := leaveLink(newRoot, legacy, d)
	// 4. Rewrite stored paths. A failure loses nothing: the link keeps the old
	// paths resolving.
	for i, rw := range d.Rewrite {
		if err := rw(legacy, newRoot); err != nil {
			d.Logf("WARNING: data root move: stored path rewrite %d of %d failed (%v); paths under %s keep working through the link at the old path", i+1, len(d.Rewrite), err, legacy)
		}
	}
	// 6. Done.
	if keepJournal {
		return
	}
	if err := os.Remove(filepath.Join(newRoot, JournalName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		d.Logf("WARNING: data root move: %s could not be removed (%v); the next serve start finishes the move again", filepath.Join(newRoot, JournalName), err)
	}
	syncDir(newRoot)
	if linked {
		d.Logf("data root moved: %s → %s (a link was left at the old path)", legacy, newRoot)
	} else {
		d.Logf("data root moved: %s → %s (no link was left at the old path)", legacy, newRoot)
	}
}

// leaveLink is step 5: legacy → newRoot. It reports whether the link is there.
func leaveLink(newRoot, legacy string, d Deps) bool {
	if fi, err := os.Lstat(legacy); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			if a, err := os.Stat(legacy); err == nil {
				if b, err := os.Stat(newRoot); err == nil && os.SameFile(a, b) {
					return true
				}
			}
		}
		d.Logf("WARNING: data root move: something already sits at %s, so no link to %s was left there; old paths that name it will not resolve", legacy, newRoot)
		return false
	}
	if err := os.Symlink(newRoot, legacy); err != nil {
		d.Logf("WARNING: data root move: the link %s → %s could not be made (%v); old paths that name it will not resolve", legacy, newRoot, err)
		return false
	}
	syncDir(filepath.Dir(legacy))
	return true
}

// writeJournal writes the two journal lines (legacy path, legacy KeyHash) and
// makes them durable.
func writeJournal(path, legacy, hash string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(legacy + "\n" + hash + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

func readJournal(path string) (legacy, hash string, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		return "", "", errors.New("want two lines")
	}
	legacy, hash = lines[0], lines[1]
	if !filepath.IsAbs(legacy) || filepath.Clean(legacy) != legacy {
		return "", "", errors.New("the first line is not a clean absolute path")
	}
	if len(hash) != 16 || strings.Trim(hash, "0123456789abcdef") != "" {
		return "", "", errors.New("the second line is not a key hash")
	}
	return legacy, hash, nil
}

// isMountPoint reports dir sitting on another device than its parent.
func isMountPoint(dir string) (bool, error) {
	same, err := sameDevice(dir, filepath.Dir(dir))
	return !same, err
}

func sameDevice(a, b string) (bool, error) {
	var sa, sb syscall.Stat_t
	if err := syscall.Lstat(a, &sa); err != nil {
		return false, err
	}
	if err := syscall.Stat(b, &sb); err != nil {
		return false, err
	}
	return sa.Dev == sb.Dev, nil
}

// syncDir makes a rename, create or remove in dir durable (best effort: not
// every platform supports fsync on a directory).
func syncDir(dir string) {
	if f, err := os.Open(dir); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
}

// MoveXDGConfig moves the legacy XDG config file
// <xdgBase>/agent-box/config.json to <xdgBase>/culibox/config.json (the
// directory created 0700) and leaves a symlink at the old path. Other files in
// <xdgBase>/agent-box, such as a key a config names by absolute path, stay
// where they are. A no-op (false, nil) when the new file already exists or
// there is no legacy regular file. The caller holds the instance lock (spec D2
// "Config fallback": preconditions (a) and (c)).
func MoveXDGConfig(xdgBase string) (moved bool, err error) {
	oldDir := filepath.Join(xdgBase, "agent-box")
	newDir := filepath.Join(xdgBase, "culibox")
	oldF, newF := filepath.Join(oldDir, "config.json"), filepath.Join(newDir, "config.json")
	if _, err := os.Lstat(newF); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	fi, err := os.Lstat(oldF)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return false, nil
	}
	if err := os.MkdirAll(newDir, 0o700); err != nil {
		return false, err
	}
	if err := os.Rename(oldF, newF); err != nil {
		return false, err
	}
	syncDir(newDir)
	if err := os.Symlink(newF, oldF); err != nil {
		return true, fmt.Errorf("config moved to %s, but no link was left at %s: %w", newF, oldF, err)
	}
	syncDir(oldDir)
	return true, nil
}
