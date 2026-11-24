package clone

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"
	"time"
)

// The return state is the host's own memory of which of a clone's branches did
// not come home on the last auto-return run, and why. Like the base record it
// lives in the host data dir and is only ever reached through os.Root, so
// nothing a box writes can forge or redirect it.
const (
	returnStateDir     = "clone-return" // <dataDir>/clone-return/<cloneKeyHash>.json, 0600, dir 0700
	maxReturnStateFile = 64 << 10
	maxNotHome         = 100
)

// Reason values, mirrored verbatim in workhome.js.
const (
	ReasonOff        = "off"         // auto-return is off for the profile
	ReasonNotFF      = "not-ff"      // host box/<b> has commits the branch lacks
	ReasonCheckedOut = "checked-out" // box/<b> is checked out in a worktree of clone_of
	ReasonRefused    = "refused"     // a history or object guard of Fetch refused it
	ReasonTooLarge   = "too-large"   // bundle over maxBundleBytes
	ReasonName       = "name"        // the clone's branch name fails ValidBranch
	ReasonDeferred   = "deferred"    // not tried this run (time budget or attempt cap)
	ReasonFailed     = "failed"      // anything else (container, git error)
)

type NotHome struct {
	Branch string `json:"branch"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"` // sanitized, <= 200 runes
}

type ReturnState struct {
	V       int       `json:"v"` // 1
	At      time.Time `json:"at"`
	Base    string    `json:"base,omitempty"`
	NotHome []NotHome `json:"not_home,omitempty"`
	Error   string    `json:"error,omitempty"` // the last run could not check the clone
}

// ReadReturnState reads the state of cloneDir. ok is false when there is no
// file. A symlink, a non-regular file or a file over the cap is an error.
func ReadReturnState(dataDir, cloneDir string) (st ReturnState, ok bool, err error) {
	name, err := returnStateName(cloneDir)
	if err != nil {
		return ReturnState{}, false, err
	}
	root, err := os.OpenRoot(dataDir)
	if errors.Is(err, fs.ErrNotExist) {
		return ReturnState{}, false, nil
	}
	if err != nil {
		return ReturnState{}, false, err
	}
	defer root.Close()
	dir, err := root.OpenRoot(returnStateDir)
	if errors.Is(err, fs.ErrNotExist) {
		return ReturnState{}, false, nil
	}
	if err != nil {
		return ReturnState{}, false, err
	}
	defer dir.Close()
	return readReturnStateIn(dir, name)
}

// WriteReturnState replaces the state of cloneDir.
func WriteReturnState(dataDir, cloneDir string, st ReturnState) error {
	name, err := returnStateName(cloneDir)
	if err != nil {
		return err
	}
	dir, unlock, err := lockReturnState(dataDir)
	if err != nil {
		return err
	}
	defer unlock()
	return writeReturnStateIn(dir, name, st)
}

// ForgetNotHome drops one branch's entry, leaving the rest of the state as it
// was. No file is not an error and creates none.
func ForgetNotHome(dataDir, cloneDir, branch string) error {
	name, err := returnStateName(cloneDir)
	if err != nil {
		return err
	}
	dir, unlock, err := lockReturnState(dataDir)
	if err != nil {
		return err
	}
	defer unlock()
	st, ok, err := readReturnStateIn(dir, name)
	if err != nil || !ok {
		return err
	}
	kept := st.NotHome[:0]
	for _, n := range st.NotHome {
		if n.Branch != branch {
			kept = append(kept, n)
		}
	}
	if len(kept) == len(st.NotHome) {
		return nil
	}
	st.NotHome = kept
	return writeReturnStateIn(dir, name, st)
}

func returnStateName(cloneDir string) (string, error) {
	h, err := cloneKeyHash(cloneDir)
	if err != nil {
		return "", err
	}
	return h + ".json", nil
}

// lockReturnState opens <dataDir>/clone-return as a Root and takes an exclusive
// lock for a read-modify-write: the daemon and the CLI are separate processes.
// Closing the lock file drops it.
func lockReturnState(dataDir string) (*os.Root, func(), error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, nil, err
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	if err := root.MkdirAll(returnStateDir, 0o700); err != nil {
		return nil, nil, err
	}
	dir, err := root.OpenRoot(returnStateDir)
	if err != nil {
		return nil, nil, err
	}
	// A first creation of .lock racing another writer can fail with a spurious
	// ENOENT on macOS (#293, #346); see openCreateRetryENOENT.
	f, err := openCreateRetryENOENT(func() (*os.File, error) {
		return dir.OpenFile(".lock", os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	})
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

func readReturnStateIn(dir *os.Root, name string) (ReturnState, bool, error) {
	fi, err := dir.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return ReturnState{}, false, nil
	}
	if err != nil {
		return ReturnState{}, false, err
	}
	if !fi.Mode().IsRegular() {
		return ReturnState{}, false, fmt.Errorf("return state %s is not a regular file", name)
	}
	f, err := dir.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return ReturnState{}, false, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxReturnStateFile+1))
	if err != nil {
		return ReturnState{}, false, err
	}
	if len(raw) > maxReturnStateFile {
		return ReturnState{}, false, fmt.Errorf("return state %s is larger than %d bytes", name, maxReturnStateFile)
	}
	var st ReturnState
	if err := json.Unmarshal(raw, &st); err != nil {
		return ReturnState{}, false, fmt.Errorf("return state %s: %w", name, err)
	}
	return cleanReturnState(st), true, nil
}

// cleanReturnState makes a state safe to hand on: every string re-sanitized
// (the file is host-owned, but the strings began as box text) and the entries
// capped.
func cleanReturnState(st ReturnState) ReturnState {
	st.Base = sanitize(st.Base)
	st.Error = sanitize(st.Error)
	if len(st.NotHome) > maxNotHome {
		st.NotHome = st.NotHome[:maxNotHome]
	}
	for i, n := range st.NotHome {
		st.NotHome[i] = NotHome{Branch: sanitize(n.Branch), Reason: sanitize(n.Reason), Detail: sanitize(n.Detail)}
	}
	return st
}

// writeReturnStateIn writes through a temp file in the same dir and renames it
// over name (the rename replaces, never follows, a symlink at name).
func writeReturnStateIn(dir *os.Root, name string, st ReturnState) error {
	st = cleanReturnState(st)
	st.V = 1
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	// Sanitized strings can still be multi-byte; drop the tail rather than write
	// a file the reader would refuse.
	for len(data) > maxReturnStateFile && len(st.NotHome) > 0 {
		st.NotHome = st.NotHome[:len(st.NotHome)-1]
		if data, err = json.Marshal(st); err != nil {
			return err
		}
	}
	if len(data) > maxReturnStateFile {
		return fmt.Errorf("return state is larger than %d bytes", maxReturnStateFile)
	}
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
