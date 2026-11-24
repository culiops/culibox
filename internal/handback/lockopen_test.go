package handback

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

// macOS can return a spurious ENOENT when two writers create .lock for the
// first time at once (#346 on darwin; same cause as clone's #293). The store's
// lock retries that open; a real ENOENT is returned after the bound, and any
// other error at once.
func TestLockRetriesSpuriousENOENT(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	enoent := &os.PathError{Op: "openat", Path: ".lock", Err: syscall.ENOENT}
	orig := lockOpen
	t.Cleanup(func() { lockOpen = orig })

	calls := 0
	lockOpen = func(dir *os.Root) (*os.File, error) {
		calls++
		if calls < 3 {
			return nil, enoent
		}
		return orig(dir)
	}
	if _, err := s.Merge(CloneKey("/repo", "b"), Record{Kind: "clone", CloneOf: "/repo", Branch: "b"}, time.Unix(1, 0), Report{}); err != nil || calls != 3 {
		t.Fatalf("err=%v calls=%d; want success on the 3rd open", err, calls)
	}

	calls = 0
	lockOpen = func(*os.Root) (*os.File, error) { calls++; return nil, enoent }
	if _, err := s.Merge(CloneKey("/repo", "b"), Record{Kind: "clone", CloneOf: "/repo", Branch: "b"}, time.Unix(2, 0), Report{}); !errors.Is(err, syscall.ENOENT) || calls != lockOpenTries {
		t.Fatalf("err=%v calls=%d; want ENOENT after %d tries", err, calls, lockOpenTries)
	}

	calls = 0
	eacces := &os.PathError{Op: "openat", Path: ".lock", Err: syscall.EACCES}
	lockOpen = func(*os.Root) (*os.File, error) { calls++; return nil, eacces }
	if _, err := s.Merge(CloneKey("/repo", "b"), Record{Kind: "clone", CloneOf: "/repo", Branch: "b"}, time.Unix(3, 0), Report{}); !errors.Is(err, syscall.EACCES) || calls != 1 {
		t.Fatalf("err=%v calls=%d; any other error is returned at once", err, calls)
	}
}
