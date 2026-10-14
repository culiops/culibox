// Package instancelock makes sure only one agent-box serves a data dir. A
// second instance would sweep the first one's abduco attach clients and adopt
// its sessions during startup, before it ever failed on a busy port.
package instancelock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrLocked means another live process holds the data dir.
var ErrLocked = errors.New("instancelock: another agent-box is already serving this data dir")

// Acquire takes an exclusive, non-blocking flock on <dataDir>/serve.lock. The
// kernel drops it when the process exits, however it exits, so a crash leaves
// no stale lock. flock is per open file description, so even a second Acquire
// in the same process is refused.
func Acquire(dataDir string) (func(), error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dataDir, "serve.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("instancelock: %w", err)
	}
	return func() { f.Close() }, nil
}
